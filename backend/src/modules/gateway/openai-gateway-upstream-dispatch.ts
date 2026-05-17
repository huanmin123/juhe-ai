import type { Request } from 'express'

import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import {
  buildPreparedUpstreamRequestParts,
  handleUnavailableProxyProfile,
  prepareUpstreamAccount,
  skipAccountForFailedProxyDispatch
} from './openai-gateway-account-preparation.js'
import {
  throwIfRequestAborted
} from './openai-gateway-dispatch-helpers.js'
import {
  flushDeferredAccountFailures,
  handleFailedUpstreamResponse,
  handleUpstreamRequestError,
  UpstreamRejectedRequestError,
  type DeferredAccountFailure
} from './openai-gateway-failure-dispatch.js'
import {
  buildUpstreamUrlsForAccount,
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'
import { rememberOpenAIAccountForSession } from './openai-gateway-session-affinity.service.js'
import { performUpstreamRequestAttempt } from './openai-gateway-upstream-attempts.js'
import { type UpstreamAttempt } from './openai-gateway-usage.js'
import { type GatewayUsageContext } from './openai-gateway-usage-records.js'
import { type GatewayUpstreamResponse } from './openai-gateway-upstream.js'

export interface OpenAIUpstreamDispatchResult {
  account: UpstreamAccount
  response: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  releaseConcurrency: () => void
}

export class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

export async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string,
  signal?: AbortSignal
): Promise<OpenAIUpstreamDispatchResult> {
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0
  const failedProxyDispatchKeys = new Map<string, string>()
  const deferredAccountFailures: DeferredAccountFailure[] = []

  for (const originalAccount of accounts) {
    throwIfRequestAborted(signal)
    const skippedProxyAttempt = skipAccountForFailedProxyDispatch(failedProxyDispatchKeys, originalAccount)
    if (skippedProxyAttempt) {
      lastAttempt = skippedProxyAttempt
      continue
    }
    const unavailableProxyAttempt = handleUnavailableProxyProfile(req, usageContext, originalAccount, settings, failedProxyDispatchKeys)
    if (unavailableProxyAttempt) {
      lastAttempt = unavailableProxyAttempt
      continue
    }
    const account = await prepareUpstreamAccount(originalAccount, signal)
    const upstreamUrls = buildUpstreamUrlsForAccount(account, req)
    if (upstreamUrls.length === 0) {
      continue
    }
    const requestParts = await buildPreparedUpstreamRequestParts(req, account, usageContext, signal)
    const concurrencySlot = tryAcquireAccountConcurrency(account.id, account.concurrencyLimit)
    if (!concurrencySlot.acquired) {
      lastAttempt = {
        accountId: account.id,
        accountName: account.name,
        upstreamUrl: 'concurrency:limit',
        message: `账户并发已达到上限 ${concurrencySlot.current}/${concurrencySlot.limit}`
      }
      continue
    }
    const { headers, body } = requestParts
    let skipAccount = false
    let keepConcurrencySlot = false
    try {
      for (const upstreamUrl of upstreamUrls) {
        for (let attemptIndex = 0; attemptIndex <= retryAttempts; attemptIndex += 1) {
          const attemptStartedAt = Date.now()
          auditAttemptIndex += 1
          const auditAttemptId = auditCapture.startAttempt({
            account,
            attemptIndex: auditAttemptIndex,
            upstreamUrl,
            method: req.method,
            headers,
            body
          })
          try {
            const response = await performUpstreamRequestAttempt({
              req,
              account,
              upstreamUrl,
              attemptIndex,
              auditAttemptIndex,
              headers,
              body,
              settings,
              attemptStartedAt,
              signal
            })
            lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
            if (response.ok) {
              flushDeferredAccountFailures(deferredAccountFailures, sessionAffinityKey)
              rememberOpenAIAccountForSession(sessionAffinityKey, account.id, {
                systemAccountId: usageContext.systemAccountId,
                apiKeyId: usageContext.apiKeyId,
                groupId: usageContext.groupId
              })
              keepConcurrencySlot = true
              return { account, response, upstreamUrl, auditAttemptId, releaseConcurrency: concurrencySlot.release }
            }

            const failedResponseResult = await handleFailedUpstreamResponse({
              req,
              usageContext,
              auditCapture,
              auditAttemptId,
              account,
              upstreamUrl,
              response,
              settings,
              attemptStartedAt,
              attemptIndex,
              auditAttemptIndex,
              retryAttempts,
              sessionAffinityKey,
              signal,
              lastAttempt,
              deferredAccountFailures
            })
            lastAttempt = failedResponseResult.lastAttempt
            if (failedResponseResult.action === 'retry') {
              continue
            }
            skipAccount = true
            break
          } catch (error) {
            if (error instanceof UpstreamRejectedRequestError) {
              throw error
            }
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext,
              auditCapture,
              auditAttemptId,
              account,
              upstreamUrl,
              settings,
              attemptStartedAt,
              attemptIndex,
              auditAttemptIndex,
              retryAttempts,
              sessionAffinityKey,
              signal,
              lastAttempt,
              failedProxyDispatchKeys,
              error
            })
            lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
            if (requestErrorResult.action === 'retry') {
              continue
            }
            skipAccount = true
            break
          }
        }
        if (skipAccount) {
          break
        }
      }
    } finally {
      if (!keepConcurrencySlot) {
        concurrencySlot.release()
      }
    }
  }

  flushDeferredAccountFailures(deferredAccountFailures, sessionAffinityKey)
  throw new UpstreamAttemptError(
    lastAttempt
      ? '所有上游账户均失败；最后一次尝试 ' + lastAttempt.accountName + ' ' + lastAttempt.upstreamUrl + ' 返回 ' + (lastAttempt.message ?? lastAttempt.status)
      : '所有上游账户均失败',
    lastAttempt
  )
}
