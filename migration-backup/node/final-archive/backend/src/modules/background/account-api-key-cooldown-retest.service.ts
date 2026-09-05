import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { isAccountStatusEligibleForRecoveryProbe } from '../../storage/account-status.js'
import {
  type AccountApiKeyRuntimeProbeCandidate
} from '../../storage/account-api-key-runtime-state.repository.js'
import { testOpenAIAccountDiagnosticAttempt } from '../accounts/account-test.service.js'
import type { AccountTestDiagnosticProtocol } from '../accounts/account-test-response-diagnostics.js'
import type { AccountTestResult } from '../../domain/types.js'
import { automaticAccountProbeOutcome } from '../accounts/automatic-account-probe-outcome.js'
import { runAccountApiKeyPoolDiagnostic } from '../accounts/account-api-key-pool-diagnostic.js'
import { isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs, globalSharedQueueConcurrency, runWithBackgroundFullDiagnosticSlot } from './account-probe-limits.js'
import { systemInsufficientQuotaRuleMatches } from '../accounts/account-error-policy-system-rules.js'
import { parseAccountTestUpstreamErrorCode, parseAccountTestUpstreamMessage } from '../accounts/account-test-response-diagnostics.js'
import {
  API_KEY_QUOTA_RECOVERY_TIMEOUT_ERROR_CODE,
  apiKeyQuotaObservationExceeded,
  apiKeyQuotaRecoveryModeFromErrorCode,
  extractApiKeyQuotaRecoveryHint,
  genericApiKeyQuotaCooldownUntil,
  quotaRecoveryErrorCode,
  type ApiKeyQuotaRecoveryHint,
  type ApiKeyQuotaRecoveryMode
} from '../gateway/policy/api-key-quota-recovery.js'
import { normalizeQuotaRecoveryPolicy } from '../accounts/quota-recovery-policy.js'

interface AccountApiKeyCooldownRetestQueueItem extends AccountApiKeyRuntimeProbeCandidate {
  maxRecoveryHours: number
}

const accountApiKeyCooldownRetestRetryPolicy = sequenceRetryPolicy('account_api_key_cooldown_retest_revival', [], 0)

const accountApiKeyCooldownRetestQueue = createRetryQueue<AccountApiKeyCooldownRetestQueueItem>({
  name: 'account-api-key-cooldown-retest',
  policy: accountApiKeyCooldownRetestRetryPolicy,
  concurrency: globalSharedQueueConcurrency,
  run: async (item, context) => await runAccountApiKeyCooldownRetestQueueItem(item, context),
  onExhausted: (event) => {
    logger.warn({
      event: 'background_account_api_key_cooldown_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      keyFingerprint: event.item.keyFingerprint,
      attemptCount: event.attemptIndex + 1
    }, '账户内 API Key 复测重试已用尽，本轮保留冷却状态等待下个周期')
  }
})

export function enqueueAccountApiKeyCooldownRetest(
  candidate: AccountApiKeyRuntimeProbeCandidate,
  strategy: { maxRecoveryHours: number }
): boolean {
  return accountApiKeyCooldownRetestQueue.enqueue(`${candidate.accountId}:${candidate.keyFingerprint}`, {
    ...candidate,
    maxRecoveryHours: strategy.maxRecoveryHours
  })
}

export function getAccountApiKeyCooldownRetestQueueSnapshot() {
  return accountApiKeyCooldownRetestQueue.snapshot()
}

export async function stopAccountApiKeyCooldownRetestQueue(timeoutMs = 10_000): Promise<{ drained: boolean; activeCount: number }> {
  return await accountApiKeyCooldownRetestQueue.stopAndDrain(timeoutMs)
}

export interface AccountApiKeyQuotaRetestDecision {
  quotaFailure: boolean
  statusCode: number
  errorCode?: string
  message: string
  previousRecoveryMode?: ApiKeyQuotaRecoveryMode
  recoveryMode?: ApiKeyQuotaRecoveryMode
  recoveryHint?: ApiKeyQuotaRecoveryHint
  timedOut: boolean
  cooldownUntil?: string
}

export function resolveAccountApiKeyQuotaRetestDecision(input: {
  result: Pick<AccountTestResult, 'statusCode' | 'errorCode' | 'message'>
  upstreamAttempt?: UpstreamAttempt
  protocol?: AccountTestDiagnosticProtocol
  previousErrorCode?: string
  recoveryStartedAt?: string
  recoverySeed?: string
  quotaRecoveryPolicy?: Record<string, unknown>
  observedAt?: Date
}): AccountApiKeyQuotaRetestDecision {
  const observedAt = input.observedAt ?? new Date()
  const upstreamResponseBodyText = input.upstreamAttempt?.responseBodyText
  const upstreamResponseHeaders = input.upstreamAttempt?.responseHeaders
    ? new Headers(input.upstreamAttempt.responseHeaders)
    : undefined
  const errorCode = input.result.errorCode
    ?? (upstreamResponseBodyText ? parseAccountTestUpstreamErrorCode(upstreamResponseBodyText) : undefined)
  const message = upstreamResponseBodyText
    ? parseAccountTestUpstreamMessage(upstreamResponseBodyText, input.protocol ?? 'openai', { rawFallback: true }) ?? input.result.message
    : input.result.message
  const searchableText = [message, upstreamResponseBodyText, input.result.message]
    .filter((value): value is string => Boolean(value?.trim()))
    .join('\n')
  const statusCode = input.upstreamAttempt?.status ?? input.result.statusCode ?? 0
  const quotaFailure = systemInsufficientQuotaRuleMatches({ statusCode, errorCode, searchableText })
  const recoveryHint = quotaFailure
    ? extractApiKeyQuotaRecoveryHint({ bodyText: upstreamResponseBodyText, headers: upstreamResponseHeaders, now: observedAt })
    : undefined
  const previousRecoveryMode = apiKeyQuotaRecoveryModeFromErrorCode(input.previousErrorCode)
  const recoveryMode = quotaFailure ? recoveryHint?.mode ?? 'generic' : undefined
  const timedOut = recoveryMode === 'generic'
    && apiKeyQuotaObservationExceeded(input.recoveryStartedAt, observedAt)
  return {
    quotaFailure,
    statusCode,
    ...(errorCode ? { errorCode } : {}),
    message,
    ...(previousRecoveryMode ? { previousRecoveryMode } : {}),
    ...(recoveryMode ? { recoveryMode } : {}),
    ...(recoveryHint ? { recoveryHint } : {}),
    timedOut,
    ...(!timedOut && recoveryMode
      ? {
          cooldownUntil: recoveryHint?.cooldownUntil ?? genericApiKeyQuotaCooldownUntil({
            now: observedAt,
            seed: input.recoverySeed ?? 'api-key:default',
            policy: input.quotaRecoveryPolicy
              ? normalizeQuotaRecoveryPolicy(input.quotaRecoveryPolicy)
              : undefined
          })
        }
      : {})
  }
}

async function runAccountApiKeyCooldownRetestQueueItem(
  item: AccountApiKeyCooldownRetestQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await loadAccountForTestViaDbService(item.accountId)
  if (!account || account.type !== 'api_key' || !isAccountStatusEligibleForRecoveryProbe(account.status) || !account.schedulable || !account.boundGroupId) {
    logger.debug({
      event: 'background_account_api_key_cooldown_retest_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      accountStatus: account?.status,
      boundGroupId: account?.boundGroupId
    }, '账户内 API Key 复测任务已失效，跳过队列项')
    return true
  }

  const systemAccountId = account.ownerSystemAccountId ?? account.systemAccountId
  if (!systemAccountId) {
    return true
  }
  const candidateAccount = await loadOpenAIAccountForGroupViaDbService(account.boundGroupId, account.id, systemAccountId)
  if (!candidateAccount || candidateAccount.type !== 'api_key') {
    return true
  }
  const currentKey = accountApiKeyEntries(candidateAccount.credentials)
    .find((entry) => entry.fingerprint === item.keyFingerprint && entry.key === item.apiKey)
  if (!currentKey) {
    logger.debug({
      event: 'background_account_api_key_cooldown_retest_stale_credential_discarded',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex
    }, '账户内 API Key 复测凭据已轮换，已丢弃旧队列项')
    return true
  }
  const fixedKeyCandidate = {
    ...candidateAccount,
    apiKey: item.apiKey,
    selectedApiKeyFingerprint: item.keyFingerprint,
    selectedApiKeyIndex: item.keyIndex,
    apiKeyRuntimeStateDisabled: true
  }
  // The diagnostic candidate bypasses dispatch-time runtime filtering so it
  // can probe this exact Key. State writes retain the same Key identity but
  // must not carry that diagnostic-only bypass flag.
  const fixedKeyRuntimeStateMutationCandidate = {
    ...fixedKeyCandidate,
    apiKeyRuntimeStateDisabled: false
  }
  let upstreamAttempt: UpstreamAttempt | undefined
  let diagnosticCanceled = false
  let diagnosticTimeoutExhausted = false
  const diagnostic = await runAccountApiKeyPoolDiagnostic(fixedKeyCandidate, [currentKey], async ({ candidate: attemptCandidate, timeoutMs, signal }) => {
    const attempt = await runWithBackgroundFullDiagnosticSlot(async () => await testOpenAIAccountDiagnosticAttempt(account, {
      diagnostics: 'limited',
      testEndpointMode: account.healthCheckEndpointMode,
      groupId: account.boundGroupId,
      systemAccountId,
      trafficSource: 'cooldown_retest',
      candidateAccount: attemptCandidate,
      disableAccountStateMutation: true,
      signal,
      onUpstreamAttempt: (item) => {
        upstreamAttempt = item
      },
      findAccountForTest: loadAccountForTestViaDbService,
      findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService
    }, timeoutMs))
    upstreamAttempt = attempt.upstreamAttempt ?? upstreamAttempt
    diagnosticCanceled = attempt.canceled
    diagnosticTimeoutExhausted = attempt.diagnosticTimeoutExhausted
    return {
      value: attempt.result,
      success: attempt.result.success,
      timedOutAfterRealUpstreamAttempt: attempt.diagnosticTimeoutExhausted
        && Boolean(attempt.upstreamAttempt && isRealUpstreamAttempt(attempt.upstreamAttempt))
    }
  }, { allowSingleEntry: true })
  const result = diagnostic?.winner?.value ?? diagnostic?.attempts[0]?.value
  if (diagnostic?.errors.length) {
    logger.warn(errorLogFields(diagnostic.errors[0]?.error, {
      event: 'background_account_api_key_cooldown_retest_attempt_failed',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      failedKeyCount: diagnostic.errors.length
    }), '账户内 API Key 复测调用异常，已保留 Key 状态')
    throw new AggregateError(diagnostic.errors.map((item) => item.error), `账户 ${account.id} 的 API Key 复测存在调用异常`)
  }
  if (!result) {
    logger.warn({
      event: 'background_account_api_key_cooldown_retest_missing_diagnostic_result',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint
    }, '账户内 API Key 复测没有返回诊断结果，已保留 Key 状态')
    return true
  }

  const probeOutcome = automaticAccountProbeOutcome(result, {
    upstreamAttempt,
    canceled: diagnosticCanceled,
    timeout: diagnosticTimeoutExhausted,
    diagnosticTimeoutExhausted
  })
  // Relative provider hints and the recovery window are defined from the
  // response observation time, not from the moment the probe was launched.
  const responseObservedAt = new Date()
  const responseObservedAtIso = responseObservedAt.toISOString()
  const quotaDecision = resolveAccountApiKeyQuotaRetestDecision({
    result,
    upstreamAttempt,
    protocol: account.protocolCode === 'anthropic'
      ? 'anthropic'
      : account.protocolCode === 'gemini' ? 'gemini' : 'openai',
    previousErrorCode: item.lastErrorCode,
    recoveryStartedAt: item.recoveryStartedAt,
    recoverySeed: `${account.id}:${item.keyFingerprint?.trim() || 'account'}`,
    quotaRecoveryPolicy: account.credentials?.quota_recovery_policy as Record<string, unknown> | undefined,
    observedAt: responseObservedAt
  })
  const quotaFailure = quotaDecision.quotaFailure
  const quotaStatusCode = quotaDecision.statusCode
  const upstreamMessage = quotaFailure ? quotaDecision.message : undefined
  const quotaRecoveryHint = quotaDecision.recoveryHint
  const previousQuotaRecoveryMode = quotaDecision.previousRecoveryMode
  const quotaRecoveryMode = quotaDecision.recoveryMode
  if (probeOutcome === 'complete_success') {
    const restored = await requestBackgroundWorkerDbService({
      type: 'record_account_api_key_success',
      account: fixedKeyRuntimeStateMutationCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome: 'complete_success'
      },
      observedAt: responseObservedAtIso,
      expectedStatus: item.status,
      expectedNextProbeAt: item.nextProbeAt,
      expectedStateUpdatedAt: item.stateUpdatedAt,
      expectedProbeClaimToken: item.probeClaimToken,
      expectedAccountConfigRevision: item.accountConfigRevision
    }, backgroundProbeDbServiceTimeoutMs)
    logger.info({
      event: 'background_account_api_key_cooldown_retest_restored',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      restored: restored?.changed ?? false
    }, '账户内 API Key 复测通过，Key 已恢复可调度')
    return true
  }

  if (quotaFailure && quotaRecoveryMode !== undefined) {
    const timedOut = quotaDecision.timedOut
    const status = timedOut ? 'error' : 'rate_limited'
    const failure = await requestBackgroundWorkerDbService({
      type: 'record_account_api_key_failure',
      account: fixedKeyRuntimeStateMutationCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome,
        quotaRecoveryMode
      },
      input: {
        status,
        statusCode: quotaStatusCode,
        errorCode: timedOut ? API_KEY_QUOTA_RECOVERY_TIMEOUT_ERROR_CODE : quotaRecoveryErrorCode(quotaRecoveryMode),
        errorMessage: upstreamMessage ?? result.message,
        cooldownUntil: timedOut ? undefined : quotaDecision.cooldownUntil,
        quotaRecoveryMode,
        traceId: result.traceId,
        observedAt: responseObservedAtIso,
        expectedStatus: item.status,
        expectedNextProbeAt: item.nextProbeAt,
        expectedStateUpdatedAt: item.stateUpdatedAt,
        expectedProbeClaimToken: item.probeClaimToken,
        expectedAccountConfigRevision: item.accountConfigRevision
      }
    }, backgroundProbeDbServiceTimeoutMs)
    logger.info({
      event: timedOut
        ? 'background_account_api_key_quota_recovery_timeout'
        : 'background_account_api_key_quota_retest_failed',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      quotaRecoveryMode,
      status,
      statusCode: quotaStatusCode,
      errorCode: result.errorCode,
      probeOutcome,
      durationMs: result.durationMs,
      changed: failure?.changed ?? false
    }, timedOut
      ? 'API Key 额度连续确认失败已达到 30 天，进入人工恢复的异常状态'
      : quotaRecoveryMode === 'explicit_reset'
        ? 'API Key 额度仍不足，已严格按上游恢复时间等待下次复测'
        : previousQuotaRecoveryMode === 'explicit_reset'
          ? 'API Key 当前未提供恢复时间，已切换通用 30 天额度观察窗口'
          : 'API Key 额度仍不足，已按通用恢复间隔等待下次复测')
    return true
  }

  if (probeOutcome !== 'upstream_failure') {
    const deferred = await requestBackgroundWorkerDbService({
      type: 'defer_account_api_key_probe',
      account: fixedKeyRuntimeStateMutationCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome,
        ...(quotaRecoveryMode ? { quotaRecoveryMode } : {})
      },
      input: {
        expectedStatus: item.status,
        expectedNextProbeAt: item.nextProbeAt,
        expectedStateUpdatedAt: item.stateUpdatedAt,
        expectedProbeClaimToken: item.probeClaimToken,
        expectedAccountConfigRevision: item.accountConfigRevision,
        delaySeconds: quotaRecoveryMode
          ? quotaRecoveryDelaySeconds({
              account,
              keyFingerprint: item.keyFingerprint,
              recoveryStartedAt: item.recoveryStartedAt,
              now: responseObservedAt
            })
          : 60,
        observedAt: responseObservedAtIso,
        breakQuotaRecoveryWindow: previousQuotaRecoveryMode !== undefined
      }
    }, backgroundProbeDbServiceTimeoutMs)
    logger.warn({
      event: 'background_account_api_key_cooldown_retest_task_failed',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      probeOutcome,
      durationMs: result.durationMs,
      deferred: deferred?.changed ?? false,
      message: result.message
    }, '账户内 API Key 复测未形成传输失败证据，已保留 Key 状态')
    return true
  }

  if (quotaRecoveryMode) {
    const deferred = await requestBackgroundWorkerDbService({
      type: 'defer_account_api_key_probe',
      account: fixedKeyRuntimeStateMutationCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome,
        quotaRecoveryMode
      },
      input: {
        expectedStatus: item.status,
        expectedNextProbeAt: item.nextProbeAt,
        expectedStateUpdatedAt: item.stateUpdatedAt,
        expectedProbeClaimToken: item.probeClaimToken,
        expectedAccountConfigRevision: item.accountConfigRevision,
        delaySeconds: quotaRecoveryDelaySeconds({
          account,
          keyFingerprint: item.keyFingerprint,
          recoveryStartedAt: item.recoveryStartedAt,
          now: responseObservedAt
        }),
        observedAt: responseObservedAtIso,
        breakQuotaRecoveryWindow: previousQuotaRecoveryMode !== undefined
      }
    }, backgroundProbeDbServiceTimeoutMs)
    logger.warn({
      event: 'background_account_api_key_quota_retest_transport_deferred',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      probeOutcome,
      deferred: deferred?.changed ?? false
    }, 'API Key 额度复测未形成有效额度结论，按通用间隔顺延且不累计 30 天确认失败')
    return true
  }

  const failure = await requestBackgroundWorkerDbService({
    type: 'record_account_api_key_failure',
    account: fixedKeyRuntimeStateMutationCandidate,
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'upstream_failure'
    },
    input: {
      status: 'temporary_unavailable',
      statusCode: quotaStatusCode,
      errorCode: result.errorCode,
      errorMessage: upstreamMessage ?? result.message,
      traceId: result.traceId,
      breakQuotaRecoveryWindow: previousQuotaRecoveryMode !== undefined,
      observedAt: responseObservedAtIso,
      expectedStatus: item.status,
      expectedNextProbeAt: item.nextProbeAt,
      expectedStateUpdatedAt: item.stateUpdatedAt,
      expectedProbeClaimToken: item.probeClaimToken,
      expectedAccountConfigRevision: item.accountConfigRevision
    }
  }, backgroundProbeDbServiceTimeoutMs)
  logger.debug({
    event: 'background_account_api_key_cooldown_retest_failed',
    accountId: account.id,
    accountName: account.name,
    keyFingerprint: item.keyFingerprint,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    probeOutcome,
    durationMs: result.durationMs,
    changed: failure?.changed ?? false,
    message: result.message
  }, '账户内 API Key 复测未通过，已按 Key 运行态退避等待下次复测')
  return true
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope) {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_test',
    accountId,
    access
  }, backgroundProbeDbServiceTimeoutMs)
}

async function loadOpenAIAccountForGroupViaDbService(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = { ignoreAvailability: true }
) {
  return await requestBackgroundWorkerDbService({
    type: 'find_openai_account_for_group',
    groupId,
    accountId,
    systemAccountId,
    includeUnavailable: options.includeUnavailable,
    ignoreAvailability: options.ignoreAvailability
  }, backgroundProbeDbServiceTimeoutMs)
}

function quotaRecoveryDelaySeconds(input: {
  account: { id: string; credentials?: Record<string, unknown> }
  keyFingerprint: string
  recoveryStartedAt?: string
  now: Date
}): number {
  const policy = input.account.credentials?.quota_recovery_policy
    ? normalizeQuotaRecoveryPolicy(input.account.credentials.quota_recovery_policy)
    : undefined
  const until = genericApiKeyQuotaCooldownUntil({
    now: input.now,
    seed: `${input.account.id}:${input.keyFingerprint?.trim() || 'account'}`,
    policy
  })
  return Math.max(60, Math.ceil((new Date(until).getTime() - input.now.getTime()) / 1000))
}
