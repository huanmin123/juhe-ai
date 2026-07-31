import type { AccountSummary } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccountWithDiagnosticRetries } from '../accounts/account-test.service.js'
import { automaticAccountProbeOutcome } from '../accounts/automatic-account-probe-outcome.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs, runWithBackgroundFullDiagnosticSlot } from './account-probe-limits.js'

interface CooldownAccountRetestQueueItem {
  accountId: string
  accountName: string
  configRevision: number
  dispatchRevision: number
  cooldownRetestObservationStartedAt: string
  cooldownRetestGeneration: string
  cooldownUntil?: string
  sourceConfigRevision?: number
  maxPauseMinutes: number
  maxRecoveryHours: number
}

const cooldownAccountRetestRetryPolicy = sequenceRetryPolicy('cooldown_account_retest_revival', [], 0)
const queuedCooldownFenceByAccountId = new Map<string, string>()
const cooldownRetestNeutralMinDelaySeconds = 60
const cooldownRetestNeutralMaxDelaySeconds = 5 * 60

const cooldownAccountRetestQueue = createRetryQueue<CooldownAccountRetestQueueItem>({
  name: 'cooldown-account-retest',
  policy: cooldownAccountRetestRetryPolicy,
  concurrency: 1,
  run: (item, context) => runWithBackgroundFullDiagnosticSlot(() => runCooldownAccountRetestQueueItem(item, context)),
  onSuccess: (event) => {
    releaseQueuedCooldownFence(event.item)
  },
  onExhausted: (event) => {
    releaseQueuedCooldownFence(event.item)
    logger.warn(errorLogFields(event.error, {
      event: 'background_cooldown_account_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }), '冷却账户复测重试已用尽，本轮保留冷却状态等待下个周期')
  }
})

export function enqueueCooldownAccountRetest(
  account: AccountSummary,
  strategy: { maxPauseMinutes: number; maxRecoveryHours: number }
): boolean {
  const dispatchRevision = account.cooldownRetestDispatchRevision
  const generation = account.cooldownRetestGeneration?.trim()
  const sourceConfigRevision = account.accessType === 'authorized'
    ? account.cooldownRetestSourceConfigRevision
    : undefined
  if (
    !account.cooldownRetestObservationStartedAt
    || !generation
    || !Number.isInteger(dispatchRevision)
    || dispatchRevision! < 1
    || (account.accessType === 'authorized' && (!Number.isInteger(sourceConfigRevision) || sourceConfigRevision! < 1))
  ) {
    logger.warn({
      event: 'background_cooldown_account_retest_missing_generation',
      accountId: account.id,
      accountName: account.name
    }, '冷却账户缺少复测观察代次，已拒绝投递以避免旧结果越权写回')
    return false
  }
  const item: CooldownAccountRetestQueueItem = {
    accountId: account.id,
    accountName: account.name,
    configRevision: account.configRevision ?? 1,
    dispatchRevision: dispatchRevision!,
    cooldownRetestObservationStartedAt: account.cooldownRetestObservationStartedAt,
    cooldownRetestGeneration: generation,
    cooldownUntil: account.cooldownUntil,
    sourceConfigRevision,
    maxPauseMinutes: strategy.maxPauseMinutes,
    maxRecoveryHours: strategy.maxRecoveryHours
  }
  const queueFence = cooldownQueueFence(item)
  const queuedFence = queuedCooldownFenceByAccountId.get(account.id)
  if (queuedFence === queueFence) return false
  const enqueued = cooldownAccountRetestQueue.enqueue(account.id, item, {
    replaceExisting: queuedFence !== undefined
  })
  if (enqueued) queuedCooldownFenceByAccountId.set(account.id, queueFence)
  return enqueued
}

export function getCooldownAccountRetestQueueSnapshot() {
  return cooldownAccountRetestQueue.snapshot()
}

export function setCooldownAccountRetestQueueConcurrency(concurrency: number): void {
  cooldownAccountRetestQueue.setConcurrency(concurrency)
}

async function runCooldownAccountRetestQueueItem(
  item: CooldownAccountRetestQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await cooldownRetestAccountForQueueItem(item)
  if (
    !account
    || !isAccountDueForCooldownRetest(account)
    || (account.configRevision ?? 1) !== item.configRevision
    || account.cooldownRetestDispatchRevision !== item.dispatchRevision
    || account.cooldownRetestObservationStartedAt !== item.cooldownRetestObservationStartedAt
    || account.cooldownRetestGeneration !== item.cooldownRetestGeneration
    || account.cooldownRetestSourceConfigRevision !== item.sourceConfigRevision
  ) {
    logger.debug({
      event: 'background_cooldown_account_retest_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      attemptIndex: context.attemptIndex,
      accountStatus: account?.status,
      boundGroupId: account?.boundGroupId,
      cooldownUntil: account?.cooldownUntil
    }, '冷却账户复测任务已失效，跳过队列项')
    return true
  }

  const groupId = account.boundGroupId
  const diagnosticStartedAt = Date.now()
  let upstreamAttempt: UpstreamAttempt | undefined
  let diagnosticCanceled = false
  let diagnosticTimeoutExhausted = false
  const result = await testOpenAIAccountWithDiagnosticRetries(account, {
    diagnostics: 'full',
    groupId,
    trafficSource: 'cooldown_retest',
    testEndpointMode: account.healthCheckEndpointMode,
    disableAccountStateMutation: true,
    retryAllFailures: true,
    onDiagnosticAttemptProgress: () => {
      upstreamAttempt = undefined
    },
    onDiagnosticAttemptResult: (attempt) => {
      upstreamAttempt = attempt.upstreamAttempt
      diagnosticCanceled = attempt.canceled
      diagnosticTimeoutExhausted = attempt.diagnosticTimeoutExhausted
    },
    onUpstreamAttempt: (attempt) => {
      upstreamAttempt = attempt
    },
    shouldRetryFailure: (attemptResult) => {
      const probeOutcome = automaticAccountProbeOutcome(attemptResult, {
        upstreamAttempt,
        canceled: diagnosticCanceled,
        timeout: diagnosticTimeoutExhausted,
        diagnosticTimeoutExhausted
      })
      // Keep retrying incomplete diagnostics so the completed phase can
      // distinguish a real upstream timeout ladder from a local task failure.
      return probeOutcome === 'upstream_failure' || probeOutcome === 'probe_task_failure'
    },
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  }).catch((error: unknown) => ({
    success: false as const,
    message: error instanceof Error ? error.message : '后台冷却复测执行失败',
    durationMs: Date.now() - diagnosticStartedAt,
    accountStatus: account.status,
    accountFailureEligible: false,
    statusCode: undefined,
    errorCode: undefined,
    traceId: undefined
  }))
  const probeOutcome = automaticAccountProbeOutcome(result, {
    upstreamAttempt,
    canceled: diagnosticCanceled,
    timeout: diagnosticTimeoutExhausted,
    diagnosticTimeoutExhausted
  })
  if (probeOutcome === 'complete_success') {
    const restored = await requestBackgroundWorkerDbService({
      type: 'record_cooldown_account_retest_success',
      accountId: account.id,
      expectedConfigRevision: item.configRevision,
      expectedDispatchRevision: item.dispatchRevision,
      expectedObservationStartedAt: item.cooldownRetestObservationStartedAt,
      expectedGeneration: item.cooldownRetestGeneration,
      expectedSourceConfigRevision: item.sourceConfigRevision
    }, backgroundProbeDbServiceTimeoutMs)
    logger.info({
      event: 'background_cooldown_account_retest_restored',
      accountId: account.id,
      accountName: account.name,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      accountStatus: restored?.accountStatus ?? result.accountStatus,
      restored: restored?.changed ?? false
    }, '冷却账户复测通过，账号已尝试恢复到可用状态')
    return true
  }

  if (probeOutcome !== 'upstream_failure') {
    const delaySeconds = cooldownRetestNeutralDeferDelaySeconds({
      accountId: item.accountId,
      generation: item.cooldownRetestGeneration,
      cooldownUntil: item.cooldownUntil,
      observationStartedAt: item.cooldownRetestObservationStartedAt
    })
    const deferred = await requestBackgroundWorkerDbService({
      type: 'defer_cooldown_account_retest',
      accountId: account.id,
      delaySeconds,
      expectedConfigRevision: item.configRevision,
      expectedDispatchRevision: item.dispatchRevision,
      expectedObservationStartedAt: item.cooldownRetestObservationStartedAt,
      expectedGeneration: item.cooldownRetestGeneration,
      expectedSourceConfigRevision: item.sourceConfigRevision
    }, backgroundProbeDbServiceTimeoutMs)
    logger.warn({
      event: 'background_cooldown_account_retest_task_failed',
      accountId: account.id,
      accountName: account.name,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      probeOutcome,
      durationMs: result.durationMs,
      nextDelaySeconds: delaySeconds,
      nextCooldownUntil: deferred?.cooldownUntil,
      message: result.message
    }, '冷却账户复测未形成传输失败证据，已保留账户状态')
    return true
  }

  const failure = await requestBackgroundWorkerDbService({
    type: 'record_cooldown_account_retest_failure',
    accountId: account.id,
    input: {
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      errorMessage: result.message,
      traceId: result.traceId,
      expectedConfigRevision: item.configRevision,
      expectedDispatchRevision: item.dispatchRevision,
      expectedObservationStartedAt: item.cooldownRetestObservationStartedAt,
      expectedGeneration: item.cooldownRetestGeneration,
      expectedSourceConfigRevision: item.sourceConfigRevision,
      maxPauseMinutes: item.maxPauseMinutes,
      maxRecoveryHours: item.maxRecoveryHours
    }
  }, backgroundProbeDbServiceTimeoutMs)
  const logFields = {
    event: 'background_cooldown_account_retest_failed',
    accountId: account.id,
    accountName: account.name,
    accountStatus: account.status,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    accountFailureEligible: result.accountFailureEligible,
    probeOutcome,
    durationMs: result.durationMs,
    retestFailureCount: failure?.failureCount ?? 0,
    retestAction: failure?.action,
    recoveryStage: failure?.recoveryStage,
    nextCooldownUntil: failure?.cooldownUntil,
    nextBackoffSeconds: failure?.backoffSeconds,
    maxPauseSeconds: failure?.maxPauseSeconds,
    maxRecoverySeconds: failure?.maxRecoverySeconds,
    longTermIntervalSeconds: failure?.longTermIntervalSeconds,
    maxedFailureCount: failure?.maxedFailureCount,
    observationStartedAt: failure?.observationStartedAt,
    observationElapsedSeconds: failure?.observationElapsedSeconds,
    observationTimeoutSeconds: failure?.observationTimeoutSeconds,
    transitionedToError: failure?.transitionedToError,
    message: result.message
  }
  if (failure?.transitionedToError) {
    logger.error(logFields, '冷却账户从观察开始已持续 7 天未恢复，账户已转为异常')
  } else if (failure?.recoveryStage === 'long_term') {
    logger.warn(logFields, '冷却账户复测超过自动恢复观察窗口，已进入长期不可用每 1 小时复测')
  } else if (failure?.recoveryStage === 'slow') {
    logger.warn(logFields, '冷却账户复测未通过，已进入慢速恢复通道')
  } else {
    logger.debug(logFields, '冷却账户快速恢复通道复测未通过，已按短退避等待下次复测')
  }
  return true
}

export function cooldownRetestNeutralDeferDelaySeconds(input: {
  accountId: string
  generation: string
  observationStartedAt: string
  cooldownUntil?: string
}): number {
  const range = cooldownRetestNeutralMaxDelaySeconds - cooldownRetestNeutralMinDelaySeconds + 1
  const rawMarker = input.cooldownUntil?.trim() || input.observationStartedAt
  const markerMs = Date.parse(rawMarker)
  const scheduleMarker = Number.isFinite(markerMs) ? new Date(markerMs).toISOString() : rawMarker
  return cooldownRetestNeutralMinDelaySeconds
    + stableCooldownRetestHash(`${input.accountId}:${input.generation}:${scheduleMarker}`) % range
}

function stableCooldownRetestHash(value: string): number {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}

async function cooldownRetestAccountForQueueItem(item: CooldownAccountRetestQueueItem): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_cooldown_retest',
    accountId: item.accountId
  }, backgroundProbeDbServiceTimeoutMs)
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
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

function isAccountDueForCooldownRetest(account: AccountSummary): boolean {
  if (account.status !== 'temporary_unavailable' && account.status !== 'rate_limited') {
    return false
  }
  if (!account.schedulable || !account.cooldownUntil) {
    return false
  }
  if (!account.boundGroupId) {
    return false
  }
  const cooldownUntilMs = Date.parse(account.cooldownUntil)
  if (!Number.isFinite(cooldownUntilMs) || cooldownUntilMs > Date.now()) {
    return false
  }
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) {
      return false
    }
  }
  return true
}

function releaseQueuedCooldownFence(item: CooldownAccountRetestQueueItem): void {
  if (queuedCooldownFenceByAccountId.get(item.accountId) === cooldownQueueFence(item)) {
    queuedCooldownFenceByAccountId.delete(item.accountId)
  }
}

function cooldownQueueFence(item: CooldownAccountRetestQueueItem): string {
  return JSON.stringify([
    item.configRevision,
    item.dispatchRevision,
    item.cooldownRetestObservationStartedAt,
    item.cooldownRetestGeneration,
    item.sourceConfigRevision ?? null
  ])
}
