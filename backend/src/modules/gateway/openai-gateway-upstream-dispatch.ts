import type { Request } from 'express'

import { tryAcquireAccountConcurrency, type AccountConcurrencySlot } from '../../shared/account-concurrency.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { exponentialRetryPolicy, fixedRetryPolicy, retryAttemptCount, retryDelayMs, waitForRetryDelayMs } from '../../shared/retry-policy.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import {
  buildPreparedUpstreamRequestParts,
  handleUnavailableProxyProfile,
  prepareUpstreamAccount,
  skipAccountForFailedProxyDispatch
} from './openai-gateway-account-preparation.js'
import {
  temporaryUnschedulableRetryPolicy,
  throwIfRequestAborted
} from './openai-gateway-dispatch-helpers.js'
import type { ClientIpAccountAvoidanceTracker } from './openai-gateway-client-ip-account-avoidance.service.js'
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
import { recordFailedUpstreamAttempt, type GatewayUsageContext } from './openai-gateway-usage-records.js'
import { type GatewayUpstreamResponse } from './openai-gateway-upstream.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'

export interface OpenAIUpstreamDispatchResult {
  account: UpstreamAccount
  response: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  releaseConcurrency: () => void
  markFirstOutput: () => void
}

export class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

interface AccountConcurrencyAcquireResult {
  slot: AccountConcurrencySlot
  retryCount: number
  waitedMs: number
  remainingWaitBudgetMs: number
}

const accountConcurrencyRetryBudgetMs = 1200
const accountConcurrencyRetryPolicy = exponentialRetryPolicy('gateway_account_concurrency_short_wait', 120, 480)
const accountPreparationRetryPolicy = fixedRetryPolicy('gateway_account_preparation_no_retry', 0, 0)

export async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string,
  signal?: AbortSignal,
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
): Promise<OpenAIUpstreamDispatchResult> {
  const retryPolicy = temporaryUnschedulableRetryPolicy(settings)
  const maxAttemptCount = retryAttemptCount(retryPolicy)
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0
  let concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
  const failedProxyDispatchKeys = new Map<string, string>()
  const deferredAccountFailures: DeferredAccountFailure[] = []
  const requestFailureSignatureConfirmationThreshold = Math.min(3, Math.max(2, accounts.length))

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
    const concurrencyAcquire = await acquireAccountConcurrencyWithShortRetry(
      originalAccount.id,
      originalAccount.concurrencyLimit,
      concurrencyRetryWaitBudgetMs,
      signal
    )
    concurrencyRetryWaitBudgetMs = concurrencyAcquire.remainingWaitBudgetMs
    const concurrencySlot = concurrencyAcquire.slot
    if (!concurrencySlot.acquired) {
      const message = concurrencyAcquire.waitedMs > 0
        ? `账户并发已达到上限 ${concurrencySlot.current}/${concurrencySlot.limit}（短等 ${concurrencyAcquire.waitedMs}ms 后仍未释放）`
        : `账户并发已达到上限 ${concurrencySlot.current}/${concurrencySlot.limit}`
      lastAttempt = {
        accountId: originalAccount.id,
        accountName: originalAccount.name,
        upstreamUrl: 'concurrency:limit',
        message
      }
      recordFailedUpstreamAttempt(req, usageContext, originalAccount, {
        upstreamUrl: 'concurrency:limit',
        startedAt: Date.now(),
        errorMessage: message
      })
      continue
    }
    if (concurrencyAcquire.waitedMs > 0) {
      getRequestLogger().info({
        event: 'gateway_account_concurrency_acquired_after_wait',
        accountId: originalAccount.id,
        accountName: originalAccount.name,
        retryCount: concurrencyAcquire.retryCount,
        waitedMs: concurrencyAcquire.waitedMs,
        current: concurrencySlot.current,
        limit: concurrencySlot.limit
      }, '账号并发槽短等后释放，继续使用当前账号')
    }
    let skipAccount = false
    let keepConcurrencySlot = false
    try {
      let account = originalAccount
      let headers: Headers
      let body: Buffer | string | undefined
      let upstreamUrls: string[]
      const preparationStartedAt = Date.now()
      try {
        account = await prepareUpstreamAccount(originalAccount, signal)
        upstreamUrls = buildUpstreamUrlsForAccount(account, req)
        if (upstreamUrls.length === 0) {
          continue
        }
        const requestParts = await buildPreparedUpstreamRequestParts(req, account, usageContext, signal)
        headers = requestParts.headers
        body = requestParts.body
      } catch (error) {
        if (signal?.aborted || error instanceof OpenAIOAuthCodexAdapterError) {
          throw error
        }
        const requestErrorResult = await handleUpstreamRequestError({
          req,
          usageContext,
          auditCapture,
          auditAttemptId: '',
          account: originalAccount,
          upstreamUrl: 'account:preparation',
          attemptStartedAt: preparationStartedAt,
          attemptIndex: 0,
          auditAttemptIndex,
          retryPolicy: accountPreparationRetryPolicy,
          sessionAffinityKey,
          signal,
          lastAttempt,
          failedProxyDispatchKeys,
          error,
          clientIpAccountAvoidanceTracker
        })
        lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
        continue
      }
      for (const upstreamUrl of upstreamUrls) {
        for (let attemptIndex = 0; attemptIndex < maxAttemptCount; attemptIndex += 1) {
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
              return {
                account,
                response,
                upstreamUrl,
                auditAttemptId,
                releaseConcurrency: concurrencySlot.release,
                markFirstOutput: concurrencySlot.markFirstOutput
              }
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
              retryPolicy,
              sessionAffinityKey,
              signal,
              lastAttempt,
              deferredAccountFailures,
              requestFailureSignatureConfirmationThreshold,
              clientIpAccountAvoidanceTracker
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
              attemptStartedAt,
              attemptIndex,
              auditAttemptIndex,
              retryPolicy,
              sessionAffinityKey,
              signal,
              lastAttempt,
              failedProxyDispatchKeys,
              error,
              clientIpAccountAvoidanceTracker
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
  throw new UpstreamAttemptError(buildUpstreamAttemptFailureMessage(accounts.length, lastAttempt), lastAttempt)
}

function buildUpstreamAttemptFailureMessage(accountCount: number, lastAttempt?: UpstreamAttempt): string {
  const prefix = accountCount === 1 ? '上游账户请求失败' : '所有上游账户均失败'
  if (!lastAttempt) {
    return prefix
  }
  const result = stringValue(lastAttempt.message) || numberValue(lastAttempt.status) || '未知错误'
  return `${prefix}；最后一次尝试 ${lastAttempt.accountName} ${lastAttempt.upstreamUrl} 返回 ${result}`
}

async function acquireAccountConcurrencyWithShortRetry(
  accountId: string,
  concurrencyLimit: number,
  waitBudgetMs: number,
  signal?: AbortSignal
): Promise<AccountConcurrencyAcquireResult> {
  let remainingWaitBudgetMs = Math.max(0, Math.trunc(waitBudgetMs))
  let slot = tryAcquireAccountConcurrency(accountId, concurrencyLimit)
  let waitedMs = 0
  let retryCount = 0
  while (!slot.acquired && remainingWaitBudgetMs > 0) {
    const delayMs = Math.min(retryDelayMs(accountConcurrencyRetryPolicy, retryCount + 1), remainingWaitBudgetMs)
    const currentDelayMs = Math.min(delayMs, remainingWaitBudgetMs)
    await waitForAccountConcurrencyRetry(currentDelayMs, signal)
    waitedMs += currentDelayMs
    remainingWaitBudgetMs -= currentDelayMs
    retryCount += 1
    slot = tryAcquireAccountConcurrency(accountId, concurrencyLimit)
  }
  return {
    slot,
    retryCount,
    waitedMs,
    remainingWaitBudgetMs
  }
}

async function waitForAccountConcurrencyRetry(delayMs: number, signal?: AbortSignal): Promise<void> {
  throwIfRequestAborted(signal)
  await waitForRetryDelayMs(delayMs, { signal })
  throwIfRequestAborted(signal)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function numberValue(value: unknown): string {
  return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
}
