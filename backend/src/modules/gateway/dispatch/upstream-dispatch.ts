import type { Request } from 'express'

import { getAccountCurrentConcurrency, tryAcquireAccountConcurrency, tryAcquireAccountConcurrencyAsync, type AccountConcurrencySlot } from '../../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../../domain/group-scheduling.js'
import type { ClientCompatibilityCapability, GroupSchedulingPolicy } from '../../../domain/types.js'
import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import {
  exponentialRetryPolicy,
  fixedRetryPolicy,
  retryAttemptCount,
  retryDelayMs,
  shouldRetryPolicyAttempt,
  waitForRetryDelayMs,
  type RetryPolicy
} from '../../../shared/retry-policy.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  buildPreparedUpstreamRequestParts,
  handleUnavailableProxyProfile,
  prepareUpstreamAccount,
  selectAccountApiKeyForDispatch,
  skipAccountForFailedProxyDispatch
} from './account-preparation.js'
import {
  throwIfRequestAborted
} from './helpers.js'
import {
  filterLocallySuppressedGatewayAccounts,
  orderGatewayAccountsByRuntimeDegradation,
  type GatewayAccountHalfOpenLease
} from '../runtime/account-side-effects.service.js'
import { waitForRecoverableUnavailableState } from '../runtime/recoverable-unavailable-wait.js'
import type { ClientIpAccountAvoidanceTracker } from '../runtime/client-ip-account-avoidance.service.js'
import {
  handleFailedUpstreamResponse,
  handleUpstreamRequestError,
  type PendingAccountApiKeyFailure
} from '../response/failure-dispatch.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { buildGatewayUpstreamUrlsForAccount } from '../../providers/drivers/registry.js'
import { rememberOpenAIAccountForSession } from '../runtime/session-affinity.service.js'
import { performUpstreamRequestAttempt } from './upstream-attempts.js'
import { type UpstreamAttempt } from '../upstream/attempt.js'
import { recordFailedUpstreamAttempt, type GatewayUsageContext } from '../usage/records.js'
import { type GatewayUpstreamResponse } from '../upstream/request.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { createGatewayCompatibilityRecoveryState } from '../client-profiles/compatibility-policy.js'
import { GatewayAgentGuidanceResponse, GatewayLocalProtocolResponse, GatewayRequestValidationError } from '../request/validation-error.js'
import { recordGatewayAccountApiKeyFailure } from '../runtime/account-api-key-effects.service.js'

export interface OpenAIUpstreamDispatchResult {
  account: UpstreamAccount
  response: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  releaseConcurrency: () => void
  markFirstOutput: () => void
  confirmSameAccountApiKeyFailures: () => void
}

export class UpstreamAttemptError extends Error {
  constructor(
    message: string,
    readonly lastAttempt?: UpstreamAttempt,
    readonly failedAccountIds: string[] = [],
    readonly agentGuidanceResponse?: GatewayAgentGuidanceResponse
  ) {
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
const maxAccountApiKeyAttemptsPerAccountPerRequest = 2

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
  accountStateMutationEnabled = true,
  requestClientCompatibility?: ClientCompatibilityCapability
): Promise<OpenAIUpstreamDispatchResult> {
  const sameAccountRetryPolicy = fixedRetryPolicy(
    'gateway_temporary_unschedulable_same_account_retry',
    settings.temporaryUnschedulableRetryIntervalSeconds * 1000,
    settings.temporaryUnschedulableRetryAttempts
  )
  const maxAttemptCount = retryAttemptCount(sameAccountRetryPolicy)
  let lastAttempt: UpstreamAttempt | undefined
  let agentGuidanceResponse: GatewayAgentGuidanceResponse | undefined
  let auditAttemptIndex = 0
  let concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
  const failedProxyDispatchKeys = new Map<string, string>()
  const failedAccountIds = new Set<string>()
  let dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(
    orderAccountsForRequestLane(accounts, requestLane, groupSchedulingPolicy)
  ).accounts
  const compatibilityRecoveryState = createGatewayCompatibilityRecoveryState()

  while (dispatchAccounts.length > 0) {
    let attemptedAccountCount = 0
    let localSuppressedSkipCount = 0

    for (const originalAccount of dispatchAccounts) {
      throwIfRequestAborted(signal)
      const localSuppression = filterLocallySuppressedGatewayAccounts([originalAccount], { acquireHalfOpenLease: true })
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
      const halfOpenLease = localSuppression.acquiredHalfOpenLeases[0]
      attemptedAccountCount += 1
      const skippedProxyAttempt = skipAccountForFailedProxyDispatch(failedProxyDispatchKeys, originalAccount)
      if (skippedProxyAttempt) {
        halfOpenLease?.release()
        lastAttempt = skippedProxyAttempt
        failedAccountIds.add(originalAccount.id)
        continue
      }
      const unavailableProxyAttempt = handleUnavailableProxyProfile(req, usageContext, originalAccount, settings, failedProxyDispatchKeys, accountStateMutationEnabled)
      if (unavailableProxyAttempt) {
        halfOpenLease?.release()
        lastAttempt = unavailableProxyAttempt
        failedAccountIds.add(originalAccount.id)
        continue
      }
      let concurrencyAcquire: AccountConcurrencyAcquireResult
      try {
        concurrencyAcquire = await acquireAccountConcurrencyWithShortRetry(
          originalAccount.id,
          originalAccount.concurrencyLimit,
          concurrencyRetryWaitBudgetMs,
          signal,
          requestLane,
          groupSchedulingPolicy
        )
      } catch (error) {
        halfOpenLease?.release()
        throw error
      }
      concurrencyRetryWaitBudgetMs = concurrencyAcquire.remainingWaitBudgetMs
      const concurrencySlot = concurrencyAcquire.slot
      if (!concurrencySlot.acquired) {
        halfOpenLease?.release()
        const message = concurrencyAcquire.waitedMs > 0
          ? accountConcurrencyLimitMessage(concurrencySlot, concurrencyAcquire.waitedMs)
          : accountConcurrencyLimitMessage(concurrencySlot)
        lastAttempt = {
          accountId: originalAccount.id,
          accountName: originalAccount.name,
          providerCode: originalAccount.providerCode,
          providerProtocolProfileId: originalAccount.providerProtocolProfileId,
          protocolCode: originalAccount.protocolCode,
          protocolVersion: originalAccount.protocolVersion,
          upstreamUrl: 'concurrency:limit',
          message
        }
        recordFailedUpstreamAttempt(req, usageContext, originalAccount, {
          upstreamUrl: 'concurrency:limit',
          startedAt: Date.now(),
          errorMessage: message,
          failureAttribution: 'gateway_capacity'
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
      let keepConcurrencySlot = false
      const excludedApiKeyFingerprints = new Set<string>()
      const pendingApiKeyFailures: PendingAccountApiKeyFailure[] = []
      let accountApiKeyAttemptCount = 0
      try {
        let retryAccountApiKey = false
        do {
          retryAccountApiKey = false
          let skipAccount = false
          let account = originalAccount
          let headers: Headers
          let body: Buffer | string | undefined
          let upstreamUrls: string[]
          const preparationStartedAt = Date.now()
          try {
            account = await prepareUpstreamAccount(originalAccount, signal)
            upstreamUrls = buildGatewayUpstreamUrlsForAccount(account, req)
            if (upstreamUrls.length === 0) {
              break
            }
            const selectedAccount = selectAccountApiKeyForDispatch(account, {
              excludeFingerprints: excludedApiKeyFingerprints
            })
            if (!selectedAccount) {
              lastAttempt = accountApiKeyPoolUnavailableAttempt(account)
              failedAccountIds.add(account.id)
              auditCapture.addGatewayMetadata({
                label: 'account_api_key_pool_unavailable_dispatch_skip',
                metadata: {
                  accountId: account.id,
                  accountName: account.name
                }
              })
              break
            }
            account = selectedAccount
            if (account.selectedApiKeyFingerprint) {
              accountApiKeyAttemptCount += 1
              excludedApiKeyFingerprints.add(account.selectedApiKeyFingerprint)
            }
            const requestParts = await buildPreparedUpstreamRequestParts(req, account, usageContext, signal, {
              requestClientCompatibility
            })
            headers = requestParts.headers
            body = requestParts.body
          } catch (error) {
            if (error instanceof GatewayAgentGuidanceResponse && error.accountScoped) {
              lastAttempt = accountScopedGuidanceAttempt(account, error)
              agentGuidanceResponse = error
              failedAccountIds.add(account.id)
              getRequestLogger().info({
                event: 'gateway_account_scoped_agent_guidance_dispatch_skip',
                accountId: account.id,
                accountName: account.name,
                providerCode: account.providerCode,
                providerProtocolProfileId: account.providerProtocolProfileId,
                protocolCode: account.protocolCode,
                protocolVersion: account.protocolVersion,
                guidanceCode: error.code,
                guidanceProtocol: error.protocol,
                guidanceModel: error.model
              }, '当前账号目标协议无法承载请求能力，跳过当前账号并继续调度')
              auditCapture.addGatewayMetadata({
                label: 'account_scoped_agent_guidance_dispatch_skip',
                metadata: {
                  accountId: account.id,
                  accountName: account.name,
                  providerCode: account.providerCode,
                  providerProtocolProfileId: account.providerProtocolProfileId,
                  protocolCode: account.protocolCode,
                  protocolVersion: account.protocolVersion,
                  guidanceCode: error.code,
                  guidanceProtocol: error.protocol,
                  guidanceModel: error.model
                }
              })
              continue
            }
            if (
              signal?.aborted
              || error instanceof GatewayAgentGuidanceResponse
              || error instanceof GatewayLocalProtocolResponse
              || (error instanceof OpenAIOAuthCodexAdapterError && !error.accountScoped)
              || (error instanceof GatewayRequestValidationError && !error.accountScoped)
            ) {
              throw error
            }
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext,
              auditCapture,
              auditAttemptId: '',
              account,
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
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled
            })
            lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
            failedAccountIds.add(account.id)
            if (shouldRetryAnotherAccountApiKey(account, requestErrorResult.keyScopedFailure, accountApiKeyAttemptCount, auditCapture)) {
              retryAccountApiKey = true
            }
            continue
          }
          for (const upstreamUrl of upstreamUrls) {
            let activeBodyVariant = false
            for (let attemptIndex = 0, attemptLimit = maxAttemptCount; attemptIndex < attemptLimit; attemptIndex += 1) {
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
                  signal,
                  requestClientCompatibility
                })
                lastAttempt = {
                  accountId: account.id,
                  accountName: account.name,
                  providerCode: account.providerCode,
                  providerProtocolProfileId: account.providerProtocolProfileId,
                  protocolCode: account.protocolCode,
                  protocolVersion: account.protocolVersion,
                  upstreamUrl,
                  status: response.status
                }
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
                    releaseConcurrency: releaseAccountDispatchSlot(concurrencySlot.release, halfOpenLease),
                    markFirstOutput: concurrencySlot.markFirstOutput,
                    confirmSameAccountApiKeyFailures: () => recordConfirmedSameAccountApiKeyFailures(pendingApiKeyFailures, account, usageContext)
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
                  accountStateMutationEnabled,
                  retrySameAccount: !activeBodyVariant && shouldRetrySameAccountAfterFailure(account, attemptIndex, sameAccountRetryPolicy),
                  requestBody: body,
                  compatibilityRecoveryState
                })
                lastAttempt = failedResponseResult.lastAttempt
                failedAccountIds.add(account.id)
                if (failedResponseResult.action === 'retry_with_body_variant') {
                  body = failedResponseResult.body
                  activeBodyVariant = true
                  attemptLimit += 1
                  continue
                }
                if (failedResponseResult.action === 'retry') {
                  await waitForSameAccountRetry(account, upstreamUrl, attemptIndex, sameAccountRetryPolicy, auditCapture, signal)
                  continue
                }
                if (
                  !activeBodyVariant
                  && shouldRetryAnotherAccountApiKey(account, failedResponseResult.keyScopedFailure, accountApiKeyAttemptCount, auditCapture)
                ) {
                  if (failedResponseResult.pendingApiKeyFailure) {
                    pendingApiKeyFailures.push(failedResponseResult.pendingApiKeyFailure)
                  }
                  retryAccountApiKey = true
                  break
                }
                skipAccount = true
                break
              } catch (error) {
                if (error instanceof GatewayAgentGuidanceResponse && error.accountScoped) {
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'request_validation',
                    errorMessage: error.message
                  })
                  lastAttempt = accountScopedGuidanceAttempt(account, error)
                  agentGuidanceResponse = error
                  failedAccountIds.add(account.id)
                  skipAccount = true
                  break
                }
                if (
                  error instanceof GatewayAgentGuidanceResponse
                  || error instanceof GatewayLocalProtocolResponse
                  || error instanceof GatewayRequestValidationError
                  || error instanceof OpenAIOAuthCodexAdapterError
                ) {
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'request_validation',
                    errorMessage: error.message
                  })
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
                  settings,
                  sessionAffinityKey,
                  signal,
                  lastAttempt,
                  failedProxyDispatchKeys,
                  error,
                  clientIpAccountAvoidanceTracker,
                  accountStateMutationEnabled,
                  retrySameAccount: !activeBodyVariant && shouldRetrySameAccountAfterFailure(account, attemptIndex, sameAccountRetryPolicy)
                })
                lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
                failedAccountIds.add(account.id)
                if (requestErrorResult.action === 'retry') {
                  await waitForSameAccountRetry(account, upstreamUrl, attemptIndex, sameAccountRetryPolicy, auditCapture, signal)
                  continue
                }
                if (
                  !activeBodyVariant
                  && shouldRetryAnotherAccountApiKey(account, requestErrorResult.keyScopedFailure, accountApiKeyAttemptCount, auditCapture)
                ) {
                  retryAccountApiKey = true
                  break
                }
                skipAccount = true
                break
              }
            }
            if (skipAccount || retryAccountApiKey) {
              break
            }
          }
        } while (retryAccountApiKey)
      } finally {
        if (!keepConcurrencySlot) {
          concurrencySlot.release()
          halfOpenLease?.release()
        }
      }
    }

    if (attemptedAccountCount > 0 || localSuppressedSkipCount === 0) {
      break
    }

    const suppressionFilter = filterLocallySuppressedGatewayAccounts(dispatchAccounts)
    const wait = await waitForRecoverableUnavailableState({
      scopeKey: recoverableDispatchSuppressionScopeKey(usageContext.systemAccountId, usageContext.apiKeyId, usageContext.groupId),
      reason: 'local_account_suppression_dispatch',
      initialState: suppressionFilter,
      refresh: () => filterLocallySuppressedGatewayAccounts(dispatchAccounts),
      isReady: (state) => !state.allSuppressed,
      nextRetryAfterMs: (state) => state.nextRetryAfterMs,
      waitWithoutRetryAfter: true,
      auditCapture,
      signal
    })
    if (!wait.state.allSuppressed) {
      dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(wait.state.accounts).accounts
      continue
    }

    auditCapture.addGatewayMetadata({
      label: 'local_account_suppression_dispatch_exhausted',
      metadata: {
        suppressedCount: localSuppressedSkipCount,
        accountCount: accounts.length
      }
    })
    throwIfRequestAborted(signal)
    lastAttempt = {
      accountId: accounts[0]?.id ?? 'local_suppression',
      accountName: accounts.length === 1 ? accounts[0]?.name ?? '上游账户' : '上游账户',
      providerCode: accounts[0]?.providerCode,
      providerProtocolProfileId: accounts[0]?.providerProtocolProfileId,
      protocolCode: accounts[0]?.protocolCode,
      protocolVersion: accounts[0]?.protocolVersion,
      upstreamUrl: 'account:locally_suppressed',
      message: '所有上游账户仍处于本地短期屏蔽'
    }
    break
  }

  throw new UpstreamAttemptError(buildUpstreamAttemptFailureMessage(accounts.length, lastAttempt), lastAttempt, [...failedAccountIds], agentGuidanceResponse)
}

function releaseAccountDispatchSlot(releaseConcurrency: () => void, halfOpenLease?: GatewayAccountHalfOpenLease): () => void {
  let released = false
  return () => {
    if (released) {
      return
    }
    released = true
    releaseConcurrency()
    halfOpenLease?.release()
  }
}

function buildUpstreamAttemptFailureMessage(accountCount: number, lastAttempt?: UpstreamAttempt): string {
  const prefix = accountCount === 1 ? '上游账户请求失败' : '所有上游账户均失败'
  if (!lastAttempt) {
    return prefix
  }
  const result = stringValue(lastAttempt.message) || numberValue(lastAttempt.status) || '未知错误'
  const upstreamUrl = sanitizeUrlCredentialsForLog(lastAttempt.upstreamUrl) ?? lastAttempt.upstreamUrl
  return `${prefix}；最后一次尝试 ${lastAttempt.accountName} ${upstreamUrl} 返回 ${result}`
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
  let slot = await tryAcquireAccountConcurrencyAsync(accountId, concurrencyLimit, acquireOptions)
  let waitedMs = 0
  let retryCount = 0
  while (!slot.acquired && remainingWaitBudgetMs > 0) {
    const delayMs = Math.min(retryDelayMs(accountConcurrencyRetryPolicy, retryCount + 1), remainingWaitBudgetMs)
    const currentDelayMs = Math.min(delayMs, remainingWaitBudgetMs)
    await waitForAccountConcurrencyRetry(currentDelayMs, signal)
    waitedMs += currentDelayMs
    remainingWaitBudgetMs -= currentDelayMs
    retryCount += 1
    slot = await tryAcquireAccountConcurrencyAsync(accountId, concurrencyLimit, acquireOptions)
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

function accountApiKeyPoolUnavailableAttempt(account: UpstreamAccount): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'account:api_key_pool_unavailable',
    message: '账户 API Key 池暂无可用 Key'
  }
}

function accountScopedGuidanceAttempt(account: UpstreamAccount, guidance: GatewayAgentGuidanceResponse): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'gateway:agent_guidance',
    message: guidance.message
  }
}

function shouldRetryAnotherAccountApiKey(
  account: UpstreamAccount,
  keyScopedFailure: boolean | undefined,
  accountApiKeyAttemptCount: number,
  auditCapture: AuditCaptureContext
): boolean {
  if (!keyScopedFailure || !account.selectedApiKeyFingerprint) {
    return false
  }
  if ((account.apiKeys?.length ?? 0) <= accountApiKeyAttemptCount) {
    return false
  }
  if (accountApiKeyAttemptCount >= maxAccountApiKeyAttemptsPerAccountPerRequest) {
    return false
  }
  getRequestLogger().warn({
    event: 'gateway_account_api_key_request_failover_scheduled',
    accountId: account.id,
    accountName: account.name,
    selectedApiKeyIndex: account.selectedApiKeyIndex,
    accountApiKeyAttemptCount,
    maxAccountApiKeyAttemptsPerAccountPerRequest
  }, '账户内 API Key 请求失败，本次请求尝试同账户下一个 Key')
  auditCapture.addGatewayMetadata({
    label: 'account_api_key_request_failover_scheduled',
    metadata: {
      accountId: account.id,
      accountName: account.name,
      selectedApiKeyIndex: account.selectedApiKeyIndex,
      accountApiKeyAttemptCount,
      maxAccountApiKeyAttemptsPerAccountPerRequest
    }
  })
  return true
}

function recordConfirmedSameAccountApiKeyFailures(
  failures: PendingAccountApiKeyFailure[],
  successAccount: UpstreamAccount,
  usageContext: GatewayUsageContext
): void {
  if (!failures.length || !successAccount.selectedApiKeyFingerprint) {
    return
  }
  const successSourceAccountId = accountRuntimeSourceId(successAccount)
  for (const failure of failures) {
    if (accountRuntimeSourceId(failure.account) !== successSourceAccountId) {
      continue
    }
    if (!failure.account.selectedApiKeyFingerprint || failure.account.selectedApiKeyFingerprint === successAccount.selectedApiKeyFingerprint) {
      continue
    }
    recordGatewayAccountApiKeyFailure(failure.account, {
      status: failure.status,
      statusCode: failure.statusCode,
      errorCode: failure.errorCode,
      errorMessage: failure.errorMessage,
      cooldownUntil: failure.cooldownUntil,
      trafficSource: usageContext.trafficSource,
      clientIp: usageContext.clientIp,
      apiKeyId: usageContext.apiKeyId,
      source: 'same_account_api_key_failover_confirmed'
    })
  }
  failures.length = 0
}

function accountRuntimeSourceId(account: UpstreamAccount): string {
  return account.credentialSourceAccountId || account.id
}

function shouldRetrySameAccountAfterFailure(
  account: UpstreamAccount,
  attemptIndex: number,
  sameAccountRetryPolicy: RetryPolicy
): boolean {
  if (account.selectedApiKeyFingerprint) {
    return false
  }
  if (!shouldRetryPolicyAttempt(attemptIndex, sameAccountRetryPolicy)) {
    return false
  }
  return !filterLocallySuppressedGatewayAccounts([account]).allSuppressed
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

async function waitForSameAccountRetry(
  account: UpstreamAccount,
  upstreamUrl: string,
  attemptIndex: number,
  policy: RetryPolicy,
  auditCapture: AuditCaptureContext,
  signal?: AbortSignal
): Promise<void> {
  const retryNumber = Math.max(1, Math.trunc(attemptIndex) + 1)
  const maxRetryCount = Math.max(0, retryAttemptCount(policy) - 1)
  const delayMs = retryDelayMs(policy, retryNumber)
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? upstreamUrl
  getRequestLogger().info({
    event: 'gateway_same_account_retry_scheduled',
    accountId: account.id,
    accountName: account.name,
    upstreamUrl: safeUpstreamUrl,
    retryNumber,
    maxRetryCount,
    delayMs
  }, '上游失败后按临时状态重试配置原地重试当前账号')
  auditCapture.addGatewayMetadata({
    label: 'same_account_retry_scheduled',
    metadata: {
      accountId: account.id,
      upstreamUrl: safeUpstreamUrl,
      retryNumber,
      maxRetryCount,
      delayMs
    }
  })
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

function recoverableDispatchSuppressionScopeKey(systemAccountId: string, apiKeyId: string | undefined, groupId: string): string {
  return [systemAccountId, apiKeyId ?? '', groupId].join(':')
}
