import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import type { AccessScope } from '../../storage/access-scope.js'
import type { AccountApiKeyPoolProbeCursor } from '../../storage/account-api-key-pool-probe-cursor.repository.js'
import { resolveAccountTestModelAsync, testOpenAIAccountDiagnosticAttempt } from '../accounts/account-test.service.js'
import { automaticAccountProbeOutcome } from '../accounts/automatic-account-probe-outcome.js'
import { accountApiKeyPoolEntriesForCandidate } from '../accounts/account-api-key-pool-runtime.js'
import {
  accountApiKeyPoolKeySetFingerprint,
  orderAccountApiKeyPoolEntries,
  runAccountApiKeyPoolDiagnostic
} from '../accounts/account-api-key-pool-diagnostic.js'
import { isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import {
  backgroundProbeDbServiceTimeoutMs,
  globalSharedQueueConcurrency,
  runWithCooldownAccountRetestDiagnosticSlot
} from './account-probe-limits.js'

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

type CooldownRetestTestHooks = {
  lookup?: (item: CooldownAccountRetestQueueItem) => Promise<AccountSummary | undefined>
  throwDiagnosticError?: boolean
  disableDiagnosticRetries?: boolean
  throwOnPersistenceType?:
    | 'record_cooldown_account_retest_success'
    | 'record_cooldown_account_retest_failure'
    | 'defer_cooldown_account_retest'
}

interface CooldownPoolAttemptValue {
  result: AccountTestResult
  upstreamAttempt?: UpstreamAttempt
  canceled: boolean
  diagnosticTimeoutExhausted: boolean
}

async function runCooldownAccountDiagnostic(
  account: AccountSummary,
  groupId: string,
  cursorFence: Pick<CooldownAccountRetestQueueItem, 'configRevision' | 'dispatchRevision' | 'cooldownRetestGeneration' | 'sourceConfigRevision'>,
  hooks: {
    onUpstreamAttempt: (attempt: UpstreamAttempt) => void
    onDiagnosticResult: (attempt: CooldownPoolAttemptValue) => void
  }
): Promise<AccountTestResult> {
  const systemAccountId = account.systemAccountId?.trim()
  const candidate = systemAccountId
    ? await loadOpenAIAccountForGroupViaDbService(groupId, account.id, systemAccountId, { ignoreAvailability: true })
    : undefined
  const entries = candidate ? accountApiKeyPoolEntriesForCandidate(candidate) : []
  if (!candidate || entries.length === 0) throw new Error('API Key 池诊断缺少可用 Key')
  await resolveAccountTestModelAsync(account, { testEndpointMode: account.healthCheckEndpointMode })
  const keySetFingerprint = accountApiKeyPoolKeySetFingerprint(entries)
  const storedCursor = await requestBackgroundWorkerDbService({
    type: 'account_api_key_pool_probe_cursor',
    action: 'read',
    accountId: account.id,
    purpose: 'cooldown_retest'
  }, backgroundProbeDbServiceTimeoutMs) as AccountApiKeyPoolProbeCursor | undefined
  const cursorMatches = storedCursor
    && storedCursor.keySetFingerprint === keySetFingerprint
    && storedCursor.configRevision === cursorFence.configRevision
    && storedCursor.dispatchRevision === cursorFence.dispatchRevision
    && storedCursor.cooldownGeneration === cursorFence.cooldownRetestGeneration
    && storedCursor.sourceConfigRevision === cursorFence.sourceConfigRevision
  const orderedEntries = orderAccountApiKeyPoolEntries(entries, cursorMatches ? storedCursor.lastCompletedKeyFingerprint : undefined)
  const diagnostic = await runAccountApiKeyPoolDiagnostic(candidate, orderedEntries, async ({ entry, candidate: fixedCandidate, timeoutMs, signal }) => {
    const attempt = await runWithCooldownAccountRetestDiagnosticSlot(async () => await testOpenAIAccountDiagnosticAttempt(account, {
      model: account.healthCheckModel,
      diagnostics: 'full',
      groupId,
      trafficSource: 'cooldown_retest',
      testEndpointMode: account.healthCheckEndpointMode,
      forceProbeKind: account.healthCheckEndpointMode === 'images_json' ? 'models_catalog' : undefined,
      requireCatalogModelEvidence: account.healthCheckEndpointMode === 'images_json',
      disableAccountStateMutation: true,
      candidateAccount: fixedCandidate,
      signal,
      onUpstreamAttempt: hooks.onUpstreamAttempt,
      findAccountForTest: loadAccountForTestViaDbService,
      findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService
    }, timeoutMs))
    const value: CooldownPoolAttemptValue = {
      result: attempt.result,
      upstreamAttempt: attempt.upstreamAttempt,
      canceled: attempt.canceled,
      diagnosticTimeoutExhausted: attempt.diagnosticTimeoutExhausted
    }
    return {
      value,
      success: attempt.result.success,
      timedOutAfterRealUpstreamAttempt: attempt.diagnosticTimeoutExhausted
        && Boolean(attempt.upstreamAttempt && isRealUpstreamAttempt(attempt.upstreamAttempt))
    }
  }, {
    allowSingleEntry: true,
    maxStages: cooldownRetestTestHooks?.disableDiagnosticRetries ? 1 : undefined,
    onEntryComplete: (item) => hooks.onDiagnosticResult(item.value)
  })
  const poolCompleted = diagnostic?.completed === true
  const lastCompletedFingerprint = diagnostic?.lastCompletedFingerprint
  if (poolCompleted) {
    await requestBackgroundWorkerDbService({
      type: 'account_api_key_pool_probe_cursor',
      action: 'delete',
      accountId: account.id,
      purpose: 'cooldown_retest'
    }, backgroundProbeDbServiceTimeoutMs)
  } else if (lastCompletedFingerprint) {
    await requestBackgroundWorkerDbService({
      type: 'account_api_key_pool_probe_cursor',
      action: 'save',
      input: {
        accountId: account.id,
        purpose: 'cooldown_retest',
        lastCompletedKeyFingerprint: lastCompletedFingerprint,
        keySetFingerprint,
        configRevision: cursorFence.configRevision,
        dispatchRevision: cursorFence.dispatchRevision,
        cooldownGeneration: cursorFence.cooldownRetestGeneration,
        sourceConfigRevision: cursorFence.sourceConfigRevision
      }
    }, backgroundProbeDbServiceTimeoutMs)
  }
  const selected = diagnostic?.winner?.value
    ?? diagnostic?.attempts.find((item) => automaticAccountProbeOutcome(item.value.result, {
      upstreamAttempt: item.value.upstreamAttempt,
      canceled: item.value.canceled,
      timeout: item.value.diagnosticTimeoutExhausted,
      diagnosticTimeoutExhausted: item.value.diagnosticTimeoutExhausted
    }) === 'upstream_failure')?.value
    ?? diagnostic?.attempts[0]?.value
  if (diagnostic?.errors.length) {
    logger.warn(errorLogFields(diagnostic.errors[0]?.error, {
      event: 'background_cooldown_account_retest_api_key_pool_attempt_failed',
      accountId: account.id,
      accountName: account.name,
      failedKeyCount: diagnostic.errors.length
    }), '冷却账户 Key 池探针存在调用异常，已保留连续完成游标')
  }
  if (diagnostic?.errors.length) {
    throw new AggregateError(diagnostic.errors.map((item) => item.error), `账户 ${account.id} 的 API Key 池探针存在调用异常`)
  }
  if (!selected) throw new Error('API Key 池诊断未返回结果')
  hooks.onDiagnosticResult(selected)
  return selected.result
}

let cooldownRetestTestHooks: CooldownRetestTestHooks | undefined

/** Test-only injection point used by the local recovery regression. */
export function setCooldownAccountRetestTestHooks(hooks?: CooldownRetestTestHooks): void {
  cooldownRetestTestHooks = hooks
}

/** Test-only delay override; production always uses the bounded 3s/10s/30s sequence. */
export function setCooldownAccountRetestRetryDelaysForTest(delaysMs?: number[]): void {
  cooldownAccountRetestRetryPolicy.delaysMs = delaysMs ?? [3_000, 10_000, 30_000]
}

const cooldownAccountRetestRetryPolicy = sequenceRetryPolicy(
  'cooldown_account_retest_revival',
  [3_000, 10_000, 30_000],
  3
)
const queuedCooldownFenceByAccountId = new Map<string, string>()
const cooldownRetestNeutralMinDelaySeconds = 60
const cooldownRetestNeutralMaxDelaySeconds = 5 * 60

const cooldownAccountRetestQueue = createRetryQueue<CooldownAccountRetestQueueItem>({
  name: 'cooldown-account-retest',
  policy: cooldownAccountRetestRetryPolicy,
  concurrency: globalSharedQueueConcurrency,
  run: async (item, context) => await runCooldownAccountRetestQueueItem(item, context),
  onSuccess: (event) => {
    releaseQueuedCooldownFence(event.item)
  },
  onExhausted: (event) => {
    releaseQueuedCooldownFence(event.item)
    logger.warn(errorLogFields(event.error, {
      event: 'background_cooldown_account_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1,
      retryablePhase: 'initial_lookup'
    }), '冷却账户复测重试已用尽或任务不可重放，本轮保留冷却状态等待下个周期')
  },
  onRetryScheduled: (event) => {
    const remaining = Math.max(0, cooldownAccountRetestRetryPolicy.maxRetries! - (event.attemptIndex + 1))
    logger.warn(errorLogFields(event.error, {
      event: 'background_cooldown_account_retest_lookup_retry_scheduled',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attempt: event.retryNumber,
      delayMs: event.delayMs,
      remaining,
      phase: 'initial_lookup'
    }), '冷却账户复测初始 DB lookup 失败，将在不访问上游的前提下重试')
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

async function runCooldownAccountRetestQueueItem(
  item: CooldownAccountRetestQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  // The initial candidate lookup is the only retryable phase. The queue item
  // retains all revision/provenance fences while waiting for the bounded retry.
  const account = await cooldownRetestAccountForQueueItem(item)

  try {
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

    const groupId = account.boundGroupId!
    const diagnosticStartedAt = Date.now()
    let upstreamAttempt: UpstreamAttempt | undefined
    let diagnosticCanceled = false
    let diagnosticTimeoutExhausted = false
    const result = await (cooldownRetestTestHooks?.throwDiagnosticError
      ? Promise.reject(new Error('cooldown retest regression injected diagnostic failure'))
      : runCooldownAccountDiagnostic(account, groupId, item, {
          onUpstreamAttempt: (attempt) => {
            upstreamAttempt = attempt
          },
          onDiagnosticResult: (attempt) => {
            upstreamAttempt = attempt.upstreamAttempt
            diagnosticCanceled = attempt.canceled
            diagnosticTimeoutExhausted = attempt.diagnosticTimeoutExhausted
          }
        })).catch((error: unknown) => {
      logger.error(errorLogFields(error, {
        event: 'background_cooldown_account_retest_non_replay_phase_error',
        accountId: item.accountId,
        accountName: item.accountName,
        attemptIndex: context.attemptIndex,
        retryNumber: context.retryNumber,
        phase: 'diagnostic',
        retry: false,
        upstreamReplayPrevented: true
      }), '冷却账户复测诊断阶段异常，已禁止重放上游请求')
      throw error
    })
    // The pool runner returns one aggregate result while the callback records
    // the winning/fallback attempt metadata for the existing outcome classifier.
    const probeOutcome = automaticAccountProbeOutcome(result, {
    upstreamAttempt,
    canceled: diagnosticCanceled,
    timeout: diagnosticTimeoutExhausted,
    diagnosticTimeoutExhausted
  })
    if (probeOutcome === 'complete_success') {
    throwCooldownRetestPersistenceTestError('record_cooldown_account_retest_success')
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
    throwCooldownRetestPersistenceTestError('defer_cooldown_account_retest')
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

    throwCooldownRetestPersistenceTestError('record_cooldown_account_retest_failure')
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
  } catch (error: unknown) {
    logger.error(errorLogFields(error, {
      event: 'background_cooldown_account_retest_non_replay_phase_error',
      accountId: item.accountId,
      accountName: item.accountName,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      phase: 'post_lookup',
      retry: false,
      upstreamReplayPrevented: true
    }), '冷却账户复测 lookup 成功后阶段异常，已禁止重放上游请求')
    return { success: false, retry: false }
  }
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
  if (cooldownRetestTestHooks?.lookup) {
    return await cooldownRetestTestHooks.lookup(item)
  }
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_cooldown_retest',
    accountId: item.accountId
  }, backgroundProbeDbServiceTimeoutMs)
}

function throwCooldownRetestPersistenceTestError(type: CooldownRetestTestHooks['throwOnPersistenceType']): void {
  if (cooldownRetestTestHooks?.throwOnPersistenceType === type) {
    throw new Error(`cooldown retest regression injected ${type} failure`)
  }
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
