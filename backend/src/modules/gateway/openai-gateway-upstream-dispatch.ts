import type { Request } from 'express'

import { getAccountCurrentConcurrency, tryAcquireAccountConcurrency, type AccountConcurrencySlot } from '../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { exponentialRetryPolicy, retryDelayMs, waitForRetryDelayMs } from '../../shared/retry-policy.js'
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
  filterLocallySuppressedGatewayAccounts,
  waitForLocalAccountSuppressionRelease
} from './gateway-account-side-effects.service.js'
import type { ClientIpAccountAvoidanceTracker } from './openai-gateway-client-ip-account-avoidance.service.js'
import {
  handleFailedUpstreamResponse,
  handleUpstreamRequestError
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
import type { OpenAIGatewayRequestLane } from './openai-gateway-request-lane.js'

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
const defaultLocalAccountSuppressionWaitMs = 60_000
const accountConcurrencyRetryPolicy = exponentialRetryPolicy('gateway_account_concurrency_short_wait', 120, 480)

export async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string,
  signal?: AbortSignal,
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker,
  requestLane: OpenAIGatewayRequestLane = 'text',
  groupSchedulingPolicy?: GroupSchedulingPolicy,
  accountStateMutationEnabled = true
): Promise<OpenAIUpstreamDispatchResult> {
  const maxAttemptCount = 1
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0
  let concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
  const failedProxyDispatchKeys = new Map<string, string>()
  let dispatchAccounts = orderAccountsForRequestLane(accounts, requestLane, groupSchedulingPolicy)
  const localSuppressionWaitBudgetMs = localAccountSuppressionMaxWaitMs(groupSchedulingPolicy)
  let localSuppressionWaitStartedAtMs: number | undefined

  while (dispatchAccounts.length > 0) {
    let attemptedAccountCount = 0
    let localSuppressedSkipCount = 0

    for (const originalAccount of dispatchAccounts) {
      throwIfRequestAborted(signal)
      const localSuppression = filterLocallySuppressedGatewayAccounts([originalAccount])
      if (localSuppression.allSuppressed) {
        localSuppressedSkipCount += 1
        lastAttempt = locallySuppressedAttempt(originalAccount, localSuppression.nextRetryAfterMs)
        getRequestLogger().warn({
          event: 'gateway_local_account_suppression_dispatch_skip',
          accountId: originalAccount.id,
          accountName: originalAccount.name,
          nextRetryAfterMs: localSuppression.nextRetryAfterMs
        }, '账号在本次调度执行前已进入本地短期屏蔽，跳过当前账号')
        auditCapture.addGatewayMetadata({
          label: 'local_account_suppression_dispatch_skip',
          metadata: {
            accountId: originalAccount.id,
            nextRetryAfterMs: localSuppression.nextRetryAfterMs
          }
        })
        continue
      }
      attemptedAccountCount += 1
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
        signal,
        requestLane,
        groupSchedulingPolicy
      )
      concurrencyRetryWaitBudgetMs = concurrencyAcquire.remainingWaitBudgetMs
      const concurrencySlot = concurrencyAcquire.slot
      if (!concurrencySlot.acquired) {
        const message = concurrencyAcquire.waitedMs > 0
          ? accountConcurrencyLimitMessage(concurrencySlot, concurrencyAcquire.waitedMs)
          : accountConcurrencyLimitMessage(concurrencySlot)
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
            settings,
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
                sessionAffinityKey,
                signal,
                lastAttempt,
                clientIpAccountAvoidanceTracker,
                accountStateMutationEnabled
              })
              lastAttempt = failedResponseResult.lastAttempt
              if (failedResponseResult.action === 'retry') {
                continue
              }
              skipAccount = true
              break
            } catch (error) {
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
                settings,
                sessionAffinityKey,
                signal,
                lastAttempt,
                failedProxyDispatchKeys,
            error,
            clientIpAccountAvoidanceTracker,
            accountStateMutationEnabled
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

    if (attemptedAccountCount > 0 || localSuppressedSkipCount === 0) {
      break
    }

    const waitStartedAtMs = localSuppressionWaitStartedAtMs ?? Date.now()
    localSuppressionWaitStartedAtMs = waitStartedAtMs
    const remainingWaitMs = localSuppressionWaitBudgetMs - (Date.now() - waitStartedAtMs)
    const waitResult = await waitForLocalAccountSuppressionRelease(accounts, {
      maxWaitMs: remainingWaitMs,
      signal
    })
    auditCapture.addGatewayMetadata({
      label: 'local_account_suppression_dispatch_wait',
      metadata: {
        ready: waitResult.ready,
        reason: waitResult.ready ? undefined : waitResult.reason,
        waitedMs: waitResult.waitedMs,
        remainingSuppressedCount: waitResult.filter.suppressedCount,
        nextRetryAfterMs: waitResult.filter.nextRetryAfterMs
      }
    })
    throwIfRequestAborted(signal)
    if (!waitResult.ready) {
      lastAttempt = {
        accountId: accounts[0]?.id ?? 'local_suppression',
        accountName: accounts.length === 1 ? accounts[0]?.name ?? '上游账户' : '上游账户',
        upstreamUrl: 'account:locally_suppressed',
        message: waitResult.reason === 'timeout'
          ? '所有上游账户仍处于本地短期屏蔽'
          : '本地短期屏蔽等待已取消'
      }
      break
    }
    dispatchAccounts = orderAccountsForRequestLane(waitResult.filter.accounts, requestLane, groupSchedulingPolicy)
    if (attemptedAccountCount === 0 && localSuppressedSkipCount === 0 && dispatchAccounts.length === 0) {
      break
    }
  }

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
  signal: AbortSignal | undefined,
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy
): Promise<AccountConcurrencyAcquireResult> {
  let remainingWaitBudgetMs = Math.max(0, Math.trunc(waitBudgetMs))
  const acquireOptions = accountConcurrencyLaneAcquireOptions(concurrencyLimit, requestLane, groupSchedulingPolicy)
  let slot = tryAcquireAccountConcurrency(accountId, concurrencyLimit, acquireOptions)
  let waitedMs = 0
  let retryCount = 0
  while (!slot.acquired && remainingWaitBudgetMs > 0) {
    const delayMs = Math.min(retryDelayMs(accountConcurrencyRetryPolicy, retryCount + 1), remainingWaitBudgetMs)
    const currentDelayMs = Math.min(delayMs, remainingWaitBudgetMs)
    await waitForAccountConcurrencyRetry(currentDelayMs, signal)
    waitedMs += currentDelayMs
    remainingWaitBudgetMs -= currentDelayMs
    retryCount += 1
    slot = tryAcquireAccountConcurrency(accountId, concurrencyLimit, acquireOptions)
  }
  return {
    slot,
    retryCount,
    waitedMs,
    remainingWaitBudgetMs
  }
}

function locallySuppressedAttempt(account: UpstreamAccount, nextRetryAfterMs?: number): UpstreamAttempt {
  const suffix = nextRetryAfterMs === undefined
    ? ''
    : `，预计 ${Math.max(1, Math.ceil(nextRetryAfterMs / 1000))} 秒后释放`
  return {
    accountId: account.id,
    accountName: account.name,
    upstreamUrl: 'account:locally_suppressed',
    message: `账号处于本地短期屏蔽${suffix}`
  }
}

function localAccountSuppressionMaxWaitMs(policy?: GroupSchedulingPolicy): number {
  const value = policy?.maxQueueWaitMs
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, Math.trunc(value))
    : defaultLocalAccountSuppressionWaitMs
}

function orderAccountsForRequestLane(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy
): UpstreamAccount[] {
  if (requestLane !== 'image' || accounts.length < 2) {
    return accounts
  }
  return [...accounts].sort((left, right) => imageLaneBusyRank(left, groupSchedulingPolicy) - imageLaneBusyRank(right, groupSchedulingPolicy))
}

function imageLaneBusyRank(account: UpstreamAccount, groupSchedulingPolicy?: GroupSchedulingPolicy): number {
  const hardLimit = Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
  const currentConcurrency = getAccountCurrentConcurrency(account.id)
  if (currentConcurrency >= hardLimit) {
    return 2
  }
  const laneLimit = effectiveImageLaneConcurrencyLimit({
    accountConcurrencyLimit: hardLimit,
    policy: groupSchedulingPolicy
  })
  return getAccountCurrentConcurrency(account.id, 'image') >= laneLimit ? 1 : 0
}

function accountConcurrencyLaneAcquireOptions(
  concurrencyLimit: number,
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy
): Parameters<typeof tryAcquireAccountConcurrency>[2] {
  if (requestLane !== 'image') {
    return { lane: 'text' }
  }
  return {
    lane: 'image',
    laneLimit: effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: concurrencyLimit,
      policy: groupSchedulingPolicy
    })
  }
}

function accountConcurrencyLimitMessage(slot: AccountConcurrencySlot, waitedMs?: number): string {
  const suffix = waitedMs && waitedMs > 0 ? `（短等 ${waitedMs}ms 后仍未释放）` : ''
  if (slot.lane === 'image' && slot.laneCurrent >= slot.laneLimit && slot.current < slot.limit) {
    return `账户图像通道并发已达到上限 ${slot.laneCurrent}/${slot.laneLimit}，已为文本通道保留并发槽${suffix}`
  }
  return `账户并发已达到上限 ${slot.current}/${slot.limit}${suffix}`
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
