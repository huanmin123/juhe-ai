import { errorLogFields, logger } from '../../../shared/logger.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { AccountRuntimeAvailability } from '../../db-service/db-service-types.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import { exponentialRetryPolicy, retryDueAtMs, waitForRetryDelayMs } from '../../../shared/retry-policy.js'
import { createRuntimeProbeStateStore } from '../../../shared/runtime-probe-state-store.js'
import {
  getAccountCurrentConcurrencyAsync,
  getAccountCurrentConcurrency,
  subscribeAccountConcurrencyRelease
} from '../../../shared/account-concurrency.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import {
  accountDiagnosticRetryMaxTotalTimeoutMs,
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride
} from '../../accounts/account-diagnostic-retry-policy.js'
import { accountSummaryFromGatewayPrecheckAccount } from './account-precheck-summary.mapper.js'
import {
  activateLocalAccountRuntimeDegradation,
  clearLocalAccountSuppression,
  clearLocalAccountDegradation,
  clearLocalAccountSuppressionsForTest,
  cleanupExpiredLocalSuppressions,
  countLocalAccountDegradations,
  countVisibleLocalSuppressions,
  degradeLocalAccountForGatewayFailure,
  ageLocalAccountDegradationForTest,
  filterLocalAccountSuppressions,
  localDegradationMinObservationMs,
  localSuppressionMaxMs,
  orderLocalAccountDegradations,
  releaseLocalAccountHalfOpenLease,
  snapshotLocalAccountRuntimeAvailability,
  suppressLocalAccount,
  suppressLocalAccountForGatewayFailure,
  type GatewayAccountHalfOpenLease,
  type GatewayAccountLocalSuppressionResult,
  type LocalAccountSuppression,
  type LocalAccountDegradationOrderResult,
  type LocalAccountDegradationOrderOptions,
  type LocalAccountSuppressionFilterOptions,
  type LocalAccountSuppressionFilterResult
} from './account-local-suppression-store.js'
import {
  gatewayAccountId,
  gatewayAccountRuntimeClearKeys,
  gatewayAccountRuntimeKey,
  type GatewayAccountRuntimeClearTarget,
  type SuppressibleGatewayAccount
} from './account-runtime-keys.js'
import {
  AccountSideEffectQueue,
  type AccountErrorHandlingOperation,
  type AccountSideEffectOperation
} from './account-side-effect-queue.js'
import {
  accountErrorHandlingOperationRuntimeKey,
  shouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess,
  shouldCoalesceQueuedAccountErrorHandlingSideEffect,
  shouldSkipHealthySuccessfulAccountSideEffect
} from './account-side-effect-policy.js'

export type { GatewayAccountRuntimeClearTarget, SuppressibleGatewayAccount } from './account-runtime-keys.js'
export type {
  GatewayAccountHalfOpenLease,
  GatewayAccountLocalSuppressionResult,
  LocalAccountDegradationOrderResult,
  LocalAccountSuppressionFilterResult
} from './account-local-suppression-store.js'

export interface GatewayAccountRuntimeClearResult {
  cleared: boolean
  clearedKeys: string[]
}

export interface GatewayAccountFailurePrecheckInput {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
  endpoint?: string
  reason: string
  statusCode?: number
  forcePrecheck?: boolean
  localSuppressionDelayMs?: number
}

interface FailureStormEntry {
  firstSeenMs: number
  lastSeenMs: number
  failureCount: number
  clientIps: Set<string>
  apiKeyIds: Set<string>
}

interface SuccessObservationEntry {
  firstSeenMs: number
  lastSeenMs: number
  successCount: number
}

interface FailureStormPrecheckDecision {
  trigger: boolean
  successCount: number
  failureRatio: number
  skippedReason?:
    | 'below_threshold'
    | 'observation_window'
    | 'recent_success'
    | 'failure_ratio'
}

interface PrecheckState {
  generation: number
  account: OpenAIAccountSecret
  settings?: GatewaySettings
  systemAccountId: string
  groupId: string
  startedAtMs: number
  lastAttemptAtMs?: number
  attemptCount: number
  failureCount: number
  reason: string
  distinctClientIpCount: number
  distinctApiKeyCount: number
  running: boolean
  waitingForConcurrencyDrain?: boolean
}

interface RecoveryProbeState {
  generation: number
  account: OpenAIAccountSecret
  settings?: GatewaySettings
  systemAccountId: string
  groupId: string
  startedAtMs: number
  lastObservedAtMs: number
  nextProbeAtMs: number
  attemptCount: number
  failureCount: number
  reason: string
  distinctClientIpCount: number
  distinctApiKeyCount: number
  running: boolean
  precheckRequested: boolean
}

interface DistributedRecoveryProbeState {
  runtimeKey: string
  generation: number
  accountId: string
  accountName?: string
  providerCode?: string
  settings?: GatewaySettings
  systemAccountId: string
  groupId: string
  startedAtMs: number
  lastObservedAtMs: number
  nextProbeAtMs: number
  attemptCount: number
  failureCount: number
  reason: string
  distinctClientIpCount: number
  distinctApiKeyCount: number
  precheckRequested: boolean
}

export interface GatewayAccountSideEffectState {
  queueLength: number
  processing: boolean
  enqueuedCount: number
  completedCount: number
  coalescedCount: number
  canceledBySuccessCount: number
  skippedHealthySuccessCount: number
  failedAttemptCount: number
  droppedCount: number
  expiredCount: number
  localSuppressedAccountCount: number
  degradedAccountCount: number
  precheckPendingAccountCount: number
  nextAttemptAt?: string
}

const sideEffectRetentionMs = 10 * 60_000
const failureStormWindowMs = 5 * 60_000
const failureStormThresholdCount = 5
const failureStormDistinctIpThreshold = 2
const failureStormMinObservationMs = 60_000
const failureStormRecentSuccessGraceMs = 5_000
const failureStormFailureRatioThreshold = 0.9
const precheckMinIntervalMs = 60_000
const precheckMaxAttempts = accountDiagnosticRetryTimeoutMs.length
const precheckSuppressionGuardMs = accountDiagnosticRetryMaxTotalTimeoutMs + 15_000
const precheckConcurrencyDrainPollMs = 1_000
const recoveryProbeRetryDelayMs = 10_000
const recoveryProbePrecheckFailureThreshold = 2
const recoveryProbeMaxConcurrentRuns = 4
const recoveryProbeAccountMinIntervalMs = 3_000
const recoveryProbeScopeMinIntervalMs = 1_000
const recoveryProbeBudgetDelayMs = 1_000
const recoveryProbeJitterMs = 750
const distributedRecoveryProbeStateTtlMs = Math.max(localSuppressionMaxMs, precheckSuppressionGuardMs) + 5 * 60_000
const distributedRecoveryProbeSweepIntervalMs = 1_000
const distributedRecoveryProbeSweepBatchSize = 25
const distributedRecoveryProbeDueRetryDelayMs = 250
const distributedRecoveryProbeSuppressionCacheTtlMs = 1000
const distributedRecoveryProbeSuppressionNegativeCacheTtlMs = 500
const distributedRecoveryProbeSuppressionCacheMaxEntries = 5000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)
const maxSideEffectQueueLength = 5000

const distributedRecoveryProbeStore = createRuntimeProbeStateStore<DistributedRecoveryProbeState>('gateway-account-recovery')
const sideEffectQueue = new AccountSideEffectQueue()
const failureStorms = new Map<string, FailureStormEntry>()
const successObservations = new Map<string, SuccessObservationEntry>()
const precheckStates = new Map<string, PrecheckState>()
const recoveryProbeStates = new Map<string, RecoveryProbeState>()
const recoveryProbeTimers = new Map<string, NodeJS.Timeout>()
const precheckRunTimers = new Map<string, NodeJS.Timeout>()
const precheckConcurrencyDrainWaits = new Map<string, { unsubscribe: () => void; timer: NodeJS.Timeout }>()
const runtimeProbeGenerationCounters = new Map<string, number>()
const recoveryProbeLastStartedAtByScope = new Map<string, number>()
const distributedRecoveryProbeSuppressionCache = new Map<string, { state?: DistributedRecoveryProbeState; expiresAtMs: number }>()
let processingSideEffects = false
let drainTimer: NodeJS.Timeout | undefined
let drainTimerDueAtMs: number | undefined
let distributedRecoveryProbeSweepTimer: NodeJS.Timeout | undefined
let distributedRecoveryProbeSweepDueAtMs: number | undefined
let distributedRecoveryProbeSweepRunning = false
let runningRecoveryProbeCount = 0
let enqueuedCount = 0
let completedCount = 0
let coalescedCount = 0
let canceledBySuccessCount = 0
let skippedHealthySuccessCount = 0
let failedAttemptCount = 0
let droppedCount = 0
let expiredCount = 0

export async function enqueueGatewayAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): Promise<void> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    if (shouldSkipHealthySuccessfulAccountSideEffect(operation)) {
      skippedHealthySuccessCount += 1
      return
    }
    enqueuedCount += 1
    try {
      await executeAccountSideEffect(operation)
      completedCount += 1
    } catch (error) {
      failedAttemptCount += 1
      logger.error(errorLogFields(error, {
        event: 'gateway_account_side_effect_direct_write_failed',
        operationType: operation.type,
        accountId: operationAccountId(operation)
      }), '高性能模式账号副作用直接写入 DB service 失败，禁止回退本机队列')
      throw error
    }
    return
  }
  if (operation.input.success) {
    const runtimeKey = accountErrorHandlingOperationRuntimeKey(operation)
    recordGatewayAccountSuccessObservation(runtimeKey)
    const canceledCount = cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKey(runtimeKey)
    if (canceledCount > 0) {
      canceledBySuccessCount += canceledCount
      clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
    }
  } else if (coalesceQueuedAccountErrorHandlingSideEffect(operation)) {
    return
  }
  if (shouldSkipHealthySuccessfulAccountSideEffect(operation)) {
    clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    skippedHealthySuccessCount += 1
    return
  }
  enqueueAccountSideEffect(operation)
}

export function suppressGatewayAccountLocally(
  account: SuppressibleGatewayAccount | string,
  _settings: GatewaySettings | undefined,
  reason = '上游账号请求失败'
): GatewayAccountLocalSuppressionResult {
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const accountId = gatewayAccountId(account)
  return suppressLocalAccountForGatewayFailure(runtimeKey, accountId, reason)
}

export function suppressGatewayAccountLocallyForSeconds(
  account: SuppressibleGatewayAccount | string,
  seconds: number | undefined,
  reason = '响应检查策略运行态避让'
): void {
  const value = typeof seconds === 'number' && Number.isFinite(seconds) ? Math.max(1, Math.trunc(seconds)) : 60
  suppressLocalAccount(gatewayAccountRuntimeKey(account), Math.min(value * 1000, localSuppressionMaxMs), reason, 'local_suppressed', {
    accountId: gatewayAccountId(account)
  })
}

export function recordGatewayAccountFailureForPrecheck(
  account: OpenAIAccountSecret,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
): void {
  recordGatewayAccountFailureForPrecheckInternal(account, settings, input, true)
}

export function recordGatewayAccountFailureForPrecheckForTest(
  account: OpenAIAccountSecret,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
): void {
  recordGatewayAccountFailureForPrecheckInternal(account, settings, input, false)
}

export function releaseGatewayAccountHalfOpenLease(
  lease: Pick<GatewayAccountHalfOpenLease, 'runtimeKey' | 'accountId' | 'leaseId'>
): boolean {
  return releaseLocalAccountHalfOpenLease(lease)
}

export async function completeGatewayAccountPrecheckForTest(
  account: OpenAIAccountSecret,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
): Promise<void> {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const now = Date.now()
  precheckStates.set(runtimeKey, {
    generation: nextRuntimeProbeGeneration(runtimeKey),
    account,
    settings,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    startedAtMs: now,
    attemptCount: precheckMaxAttempts,
    failureCount: failureStormThresholdCount,
    reason: input.reason,
    distinctClientIpCount: input.clientIp ? 1 : 0,
    distinctApiKeyCount: input.apiKeyId ? 1 : 0,
    running: false
  })
  await runGatewayAccountPrecheck(runtimeKey)
}

function recordGatewayAccountFailureForPrecheckInternal(
  account: OpenAIAccountSecret,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput,
  runPrecheck: boolean
): void {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    if (runPrecheck) {
      void recordDistributedGatewayAccountFailureForPrecheck(account, settings, input).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'gateway_account_distributed_recovery_probe_schedule_failed',
          accountId: account.id,
          accountName: account.name,
          runtimeKey: gatewayAccountRuntimeKey(account)
        }), 'Redis 运行态恢复探针调度失败，高性能模式禁止回退进程内状态')
      })
    }
    return
  }
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  cleanupExpiredFailureStorms()
  cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const now = Date.now()
  const current = failureStorms.get(runtimeKey)
  const entry: FailureStormEntry = current && now - current.firstSeenMs <= failureStormWindowMs
    ? current
    : {
        firstSeenMs: now,
        lastSeenMs: now,
        failureCount: 0,
        clientIps: new Set<string>(),
        apiKeyIds: new Set<string>()
      }
  entry.lastSeenMs = now
  entry.failureCount += 1
  if (input.clientIp) entry.clientIps.add(input.clientIp)
  if (input.apiKeyId) entry.apiKeyIds.add(input.apiKeyId)
  failureStorms.set(runtimeKey, entry)

  const forcePrecheck = input.forcePrecheck === true
  if (runPrecheck) {
    scheduleGatewayAccountRecoveryProbe(runtimeKey, {
      account,
      settings,
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      reason: input.reason,
      failureCount: entry.failureCount,
      distinctClientIpCount: entry.clientIps.size,
      distinctApiKeyCount: entry.apiKeyIds.size,
      precheckRequested: forcePrecheck,
      delayMs: input.localSuppressionDelayMs
    })
  }
  const precheckDecision = shouldTriggerFailureStormPrecheck(runtimeKey, entry, forcePrecheck, now)
  if (!precheckDecision.trigger) {
    return
  }

  const existingPrecheck = precheckStates.get(runtimeKey)
  if (existingPrecheck && now - existingPrecheck.startedAtMs < precheckMinIntervalMs) {
    clearGatewayAccountRecoveryProbe(runtimeKey)
    suppressLocalAccount(runtimeKey, precheckSuppressionMs(), existingPrecheck.reason, 'precheck_pending', {
      accountId: account.id,
      failureCount: entry.failureCount,
      distinctClientIpCount: entry.clientIps.size,
      distinctApiKeyCount: entry.apiKeyIds.size,
      precheckAttemptCount: existingPrecheck.attemptCount
    })
    return
  }

  const reason = `${forcePrecheck ? '短暂避让半开探测连续失败' : '多来源短窗口失败'}，等待事前确认；${input.reason}`.slice(0, 1000)
  const state: PrecheckState = {
    generation: nextRuntimeProbeGeneration(runtimeKey),
    account,
    settings,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    startedAtMs: now,
    attemptCount: 0,
    failureCount: entry.failureCount,
    reason,
    distinctClientIpCount: entry.clientIps.size,
    distinctApiKeyCount: entry.apiKeyIds.size,
    running: false
  }
  clearGatewayAccountRecoveryProbe(runtimeKey)
  precheckStates.set(runtimeKey, state)
  suppressLocalAccount(runtimeKey, precheckSuppressionMs(), reason, 'precheck_pending', {
    accountId: account.id,
    failureCount: entry.failureCount,
    distinctClientIpCount: entry.clientIps.size,
    distinctApiKeyCount: entry.apiKeyIds.size,
    precheckAttemptCount: 0
  })
  logger.warn({
    event: 'gateway_account_precheck_scheduled',
    accountId: account.id,
    accountName: account.name,
    runtimeKey,
    failureCount: entry.failureCount,
    distinctClientIpCount: entry.clientIps.size,
    distinctApiKeyCount: entry.apiKeyIds.size,
    successCount: precheckDecision.successCount,
    failureRatio: precheckDecision.failureRatio,
    forcePrecheck,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    apiKeyId: input.apiKeyId,
    endpoint: input.endpoint,
    statusCode: input.statusCode
  }, '网关检测到账号多来源短窗口失败，已进入运行态待确认')
  if (runPrecheck) {
    scheduleGatewayAccountPrecheckRun(runtimeKey, 0)
  }
}

function scheduleGatewayAccountRecoveryProbe(
  runtimeKey: string,
  input: {
    account: OpenAIAccountSecret
    settings?: GatewaySettings
    systemAccountId: string
    groupId: string
    reason: string
    failureCount: number
    distinctClientIpCount: number
    distinctApiKeyCount: number
    precheckRequested: boolean
    delayMs?: number
  }
): void {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  if (precheckStates.has(runtimeKey)) {
    return
  }
  const now = Date.now()
  const delayMs = recoveryProbeDelayMs(input.delayMs)
  const nextProbeAtMs = now + delayMs
  const current = recoveryProbeStates.get(runtimeKey)
  const generation = nextRuntimeProbeGeneration(runtimeKey)
  const state: RecoveryProbeState = current
    ? {
        ...current,
        generation,
        account: input.account,
        settings: input.settings,
        systemAccountId: input.systemAccountId,
        groupId: input.groupId,
        lastObservedAtMs: now,
        nextProbeAtMs: Math.min(current.nextProbeAtMs, nextProbeAtMs),
        failureCount: Math.max(current.failureCount, input.failureCount),
        reason: input.reason,
        distinctClientIpCount: Math.max(current.distinctClientIpCount, input.distinctClientIpCount),
        distinctApiKeyCount: Math.max(current.distinctApiKeyCount, input.distinctApiKeyCount),
        precheckRequested: current.precheckRequested || input.precheckRequested
      }
    : {
        generation,
        account: input.account,
        settings: input.settings,
        systemAccountId: input.systemAccountId,
        groupId: input.groupId,
        startedAtMs: now,
        lastObservedAtMs: now,
        nextProbeAtMs,
        attemptCount: 0,
        failureCount: input.failureCount,
        reason: input.reason,
        distinctClientIpCount: input.distinctClientIpCount,
        distinctApiKeyCount: input.distinctApiKeyCount,
        running: false,
        precheckRequested: input.precheckRequested
      }
  recoveryProbeStates.set(runtimeKey, state)
  scheduleRecoveryProbeTimer(runtimeKey, Math.max(0, state.nextProbeAtMs - now))
  logger.info({
    event: 'gateway_account_recovery_probe_scheduled',
    accountId: input.account.id,
    accountName: input.account.name,
    runtimeKey,
    generation: state.generation,
    nextProbeAt: new Date(state.nextProbeAtMs).toISOString(),
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    precheckRequested: state.precheckRequested
  }, '账号运行态后台恢复探针已调度')
}

function scheduleRecoveryProbeTimer(runtimeKey: string, delayMs: number): void {
  const existing = recoveryProbeTimers.get(runtimeKey)
  if (existing) {
    clearTimeout(existing)
  }
  const timer = setTimeout(() => {
    recoveryProbeTimers.delete(runtimeKey)
    void runGatewayAccountRecoveryProbe(runtimeKey)
  }, Math.max(0, delayMs))
  timer.unref()
  recoveryProbeTimers.set(runtimeKey, timer)
}

async function recordDistributedGatewayAccountFailureForPrecheck(
  account: OpenAIAccountSecret,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
): Promise<void> {
  ensureDistributedRecoveryProbeSweeper()
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const now = Date.now()
  const delayMs = recoveryProbeDelayMs(input.localSuppressionDelayMs)
  const current = await distributedRecoveryProbeStore.get(runtimeKey)
  const generation = await distributedRecoveryProbeStore.nextGeneration(runtimeKey, distributedRecoveryProbeStateTtlMs)
  const nextProbeAtMs = now + delayMs
  const state: DistributedRecoveryProbeState = current
    ? {
        ...current,
        generation,
        accountId: account.id,
        accountName: account.name,
        providerCode: account.providerCode,
        settings,
        systemAccountId: input.systemAccountId,
        groupId: input.groupId,
        lastObservedAtMs: now,
        nextProbeAtMs: Math.min(current.nextProbeAtMs, nextProbeAtMs),
        failureCount: current.failureCount + 1,
        reason: input.reason,
        distinctClientIpCount: Math.max(current.distinctClientIpCount, input.clientIp ? 1 : 0),
        distinctApiKeyCount: Math.max(current.distinctApiKeyCount, input.apiKeyId ? 1 : 0),
        precheckRequested: current.precheckRequested || input.forcePrecheck === true
      }
    : {
        runtimeKey,
        generation,
        accountId: account.id,
        accountName: account.name,
        providerCode: account.providerCode,
        settings,
        systemAccountId: input.systemAccountId,
        groupId: input.groupId,
        startedAtMs: now,
        lastObservedAtMs: now,
        nextProbeAtMs,
        attemptCount: 0,
        failureCount: 1,
        reason: input.reason,
        distinctClientIpCount: input.clientIp ? 1 : 0,
        distinctApiKeyCount: input.apiKeyId ? 1 : 0,
        precheckRequested: input.forcePrecheck === true
      }
  const persisted = await persistDistributedRecoveryProbeState(state)
  if (!persisted) {
    logStaleDistributedRecoveryProbeResult(runtimeKey, generation, 'gateway_account_distributed_recovery_probe_stale_schedule_ignored')
    return
  }
  logger.info({
    event: 'gateway_account_distributed_recovery_probe_scheduled',
    accountId: account.id,
    accountName: account.name,
    runtimeKey,
    generation: state.generation,
    nextProbeAt: new Date(state.nextProbeAtMs).toISOString(),
    failureCount: state.failureCount,
    precheckRequested: state.precheckRequested
  }, 'Redis 运行态账号恢复探针已调度')
}

function ensureDistributedRecoveryProbeSweeper(): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis' || runtimeConfig.processRole !== 'server') return
  scheduleDistributedRecoveryProbeSweep(distributedRecoveryProbeSweepIntervalMs)
}

function scheduleDistributedRecoveryProbeSweep(delayMs: number): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis' || runtimeConfig.processRole !== 'server') return
  const dueAtMs = Date.now() + Math.max(0, delayMs)
  if (distributedRecoveryProbeSweepTimer) {
    if (distributedRecoveryProbeSweepDueAtMs !== undefined && distributedRecoveryProbeSweepDueAtMs <= dueAtMs) {
      return
    }
    clearTimeout(distributedRecoveryProbeSweepTimer)
  }
  distributedRecoveryProbeSweepDueAtMs = dueAtMs
  distributedRecoveryProbeSweepTimer = setTimeout(() => {
    distributedRecoveryProbeSweepTimer = undefined
    distributedRecoveryProbeSweepDueAtMs = undefined
    void runDistributedRecoveryProbeSweep()
  }, Math.max(0, delayMs))
  distributedRecoveryProbeSweepTimer.unref()
}

async function runDistributedRecoveryProbeSweep(): Promise<void> {
  if (runtimeConfig.runtimeStateDriver !== 'redis' || runtimeConfig.processRole !== 'server') return
  if (distributedRecoveryProbeSweepRunning) {
    scheduleDistributedRecoveryProbeSweep(distributedRecoveryProbeSweepIntervalMs)
    return
  }
  distributedRecoveryProbeSweepRunning = true
  try {
    const runtimeKeys = await distributedRecoveryProbeStore.listDue(Date.now(), distributedRecoveryProbeSweepBatchSize)
    await Promise.all(runtimeKeys.map((runtimeKey) => runDistributedGatewayAccountRecoveryProbe(runtimeKey)))
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'gateway_account_distributed_recovery_probe_sweep_failed'
    }), 'Redis 运行态账号恢复探针 sweep 失败')
  } finally {
    distributedRecoveryProbeSweepRunning = false
    scheduleDistributedRecoveryProbeSweep(distributedRecoveryProbeSweepIntervalMs)
  }
}

async function runGatewayAccountRecoveryProbe(runtimeKey: string): Promise<void> {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  const state = recoveryProbeStates.get(runtimeKey)
  if (!state || state.running) {
    return
  }
  if (precheckStates.has(runtimeKey)) {
    clearGatewayAccountRecoveryProbe(runtimeKey)
    return
  }
  const now = Date.now()
  if (state.nextProbeAtMs > now) {
    scheduleRecoveryProbeTimer(runtimeKey, state.nextProbeAtMs - now)
    return
  }
  const budgetDelayMs = recoveryProbeBudgetWaitMs(state, now)
  if (budgetDelayMs > 0) {
    state.nextProbeAtMs = now + budgetDelayMs
    recoveryProbeStates.set(runtimeKey, state)
    scheduleRecoveryProbeTimer(runtimeKey, budgetDelayMs)
    logger.info({
      event: 'gateway_account_recovery_probe_budget_delayed',
      accountId: state.account.id,
      accountName: state.account.name,
      runtimeKey,
      generation: state.generation,
      runningRecoveryProbeCount,
      nextProbeAt: new Date(state.nextProbeAtMs).toISOString()
    }, '账号运行态恢复探针触发过密，已按预算延后')
    return
  }
  const generation = state.generation
  state.running = true
  runningRecoveryProbeCount += 1
  markRecoveryProbeStarted(state, now)
  try {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[0] ?? 10_000
    const result = await runSingleGatewayAccountPrecheck(state, timeoutMs)
    const latest = currentRecoveryProbeState(runtimeKey, generation)
    if (!latest) {
      rescheduleLatestRecoveryProbeAfterStaleResult(runtimeKey, generation, 'gateway_account_recovery_probe_stale_result_ignored')
      return
    }
    if (result.success || result.accountFailureEligible === false) {
      clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
      logger.info({
        event: 'gateway_account_recovery_probe_success',
        accountId: latest.account.id,
        accountName: latest.account.name,
        runtimeKey,
        generation,
        statusCode: result.statusCode,
        durationMs: result.durationMs,
        attemptCount: latest.attemptCount + 1
      }, '账号运行态后台恢复探针通过，已清理本地运行态')
      return
    }

    latest.running = false
    latest.attemptCount += 1
    latest.reason = accountPrecheckFailureReason(result)
    const observedForMs = Date.now() - latest.startedAtMs
    const shouldEnterPrecheck = observedForMs >= localDegradationMinObservationMs
      && (latest.precheckRequested || latest.attemptCount >= recoveryProbePrecheckFailureThreshold)
    if (observedForMs >= localDegradationMinObservationMs) {
      activateLocalAccountRuntimeDegradation(runtimeKey, latest.account.id, latest.reason, {
        sinceMs: latest.startedAtMs,
        failureCount: latest.failureCount
      })
    }
    if (shouldEnterPrecheck) {
      promoteRecoveryProbeToPrecheck(runtimeKey, latest)
      return
    }

    const delayMs = recoveryProbeFollowupDelayMs(observedForMs)
    latest.nextProbeAtMs = Date.now() + delayMs
    recoveryProbeStates.set(runtimeKey, latest)
    suppressLocalAccount(runtimeKey, delayMs, latest.reason, 'local_suppressed', {
      accountId: latest.account.id,
      sinceMs: latest.startedAtMs,
      failureCount: latest.failureCount,
      distinctClientIpCount: latest.distinctClientIpCount,
      distinctApiKeyCount: latest.distinctApiKeyCount,
      precheckAttemptCount: latest.attemptCount
    })
    scheduleRecoveryProbeTimer(runtimeKey, delayMs)
    logger.warn({
      event: 'gateway_account_recovery_probe_failed',
      accountId: latest.account.id,
      accountName: latest.account.name,
      runtimeKey,
      generation,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      attemptCount: latest.attemptCount,
      observedForMs,
      nextProbeAt: new Date(latest.nextProbeAtMs).toISOString()
    }, '账号运行态后台恢复探针未通过，已按时间窗口等待下一轮')
  } catch (error) {
    const latest = currentRecoveryProbeState(runtimeKey, generation)
    if (!latest) {
      rescheduleLatestRecoveryProbeAfterStaleResult(runtimeKey, generation, 'gateway_account_recovery_probe_stale_exception_ignored')
      logger.warn(errorLogFields(error, {
        event: 'gateway_account_recovery_probe_stale_exception_ignored',
        runtimeKey,
        generation
      }), '账号运行态恢复探针旧 generation 执行异常，已忽略状态写入')
      return
    }
    if (latest) {
      latest.running = false
      latest.nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
      scheduleRecoveryProbeTimer(runtimeKey, recoveryProbeRetryDelayMs)
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_recovery_probe_exception',
      runtimeKey,
      generation
    }), '账号运行态后台恢复探针执行异常，已等待下一轮')
  } finally {
    runningRecoveryProbeCount = Math.max(0, runningRecoveryProbeCount - 1)
  }
}

async function runDistributedGatewayAccountRecoveryProbe(runtimeKey: string): Promise<void> {
  try {
    const persisted = await distributedRecoveryProbeStore.get(runtimeKey)
    if (!persisted) {
      await distributedRecoveryProbeStore.delete(runtimeKey)
      rememberDistributedRecoveryProbeSuppressionState(runtimeKey, undefined)
      return
    }
    const now = Date.now()
    if (persisted.nextProbeAtMs > now) {
      scheduleDistributedRecoveryProbeSweep(persisted.nextProbeAtMs - now)
      return
    }
    const state = await loadDistributedRecoveryProbeStateWithAccount(persisted)
    if (!state) {
      await clearDistributedRecoveryProbeStateGeneration(runtimeKey, persisted.generation)
      return
    }
    const budgetDelayMs = recoveryProbeBudgetWaitMs(state, now)
    if (budgetDelayMs > 0) {
      const delayed = await persistDistributedRecoveryProbeState({
        ...persisted,
        nextProbeAtMs: now + budgetDelayMs
      })
      if (!delayed) {
        logStaleDistributedRecoveryProbeResult(runtimeKey, persisted.generation, 'gateway_account_distributed_recovery_probe_stale_budget_delay_ignored')
        return
      }
      logger.info({
        event: 'gateway_account_distributed_recovery_probe_budget_delayed',
        accountId: state.account.id,
        accountName: state.account.name,
        runtimeKey,
        generation: persisted.generation,
        nextProbeAt: new Date(now + budgetDelayMs).toISOString()
      }, 'Redis 运行态账号恢复探针触发过密，已按预算延后')
      return
    }

    const generation = persisted.generation
    runningRecoveryProbeCount += 1
    markRecoveryProbeStarted(state, now)
    try {
      const timeoutMs = accountDiagnosticRetryTimeoutMs[0] ?? 10_000
      const result = await runSingleGatewayAccountPrecheck(state, timeoutMs)
      const latest = await currentDistributedRecoveryProbeState(runtimeKey, generation)
      if (!latest) {
        logStaleDistributedRecoveryProbeResult(runtimeKey, generation, 'gateway_account_distributed_recovery_probe_stale_result_ignored')
        return
      }
      if (result.success || result.accountFailureEligible === false) {
        const cleared = await clearDistributedRecoveryProbeStateGeneration(runtimeKey, generation)
        if (!cleared) {
          logStaleDistributedRecoveryProbeResult(runtimeKey, generation, 'gateway_account_distributed_recovery_probe_stale_success_ignored')
          return
        }
        logger.info({
          event: 'gateway_account_distributed_recovery_probe_success',
          accountId: state.account.id,
          accountName: state.account.name,
          runtimeKey,
          generation,
          statusCode: result.statusCode,
          durationMs: result.durationMs,
          attemptCount: latest.attemptCount + 1
        }, 'Redis 运行态账号恢复探针通过，已清理共享运行态')
        return
      }

      const failedState: DistributedRecoveryProbeState = {
        ...latest,
        attemptCount: latest.attemptCount + 1,
        reason: accountPrecheckFailureReason(result)
      }
      const observedForMs = Date.now() - failedState.startedAtMs
      const shouldEnterPrecheck = observedForMs >= localDegradationMinObservationMs
        && (failedState.precheckRequested || failedState.attemptCount >= recoveryProbePrecheckFailureThreshold)
      if (shouldEnterPrecheck) {
        await promoteDistributedRecoveryProbeToPrecheck(failedState)
        return
      }

      const delayMs = recoveryProbeFollowupDelayMs(observedForMs)
      const persistedFailure = await persistDistributedRecoveryProbeState({
        ...failedState,
        nextProbeAtMs: Date.now() + delayMs
      })
      if (!persistedFailure) {
        logStaleDistributedRecoveryProbeResult(runtimeKey, generation, 'gateway_account_distributed_recovery_probe_stale_failure_ignored')
        return
      }
      logger.warn({
        event: 'gateway_account_distributed_recovery_probe_failed',
        accountId: state.account.id,
        accountName: state.account.name,
        runtimeKey,
        generation,
        statusCode: result.statusCode,
        errorCode: result.errorCode,
        durationMs: result.durationMs,
        attemptCount: failedState.attemptCount,
        observedForMs,
        nextProbeAt: new Date(Date.now() + delayMs).toISOString()
      }, 'Redis 运行态账号恢复探针未通过，已按时间窗口等待下一轮')
    } finally {
      runningRecoveryProbeCount = Math.max(0, runningRecoveryProbeCount - 1)
    }
  } catch (error) {
    const latest = await distributedRecoveryProbeStore.get(runtimeKey).catch(() => undefined)
    if (latest) {
      await persistDistributedRecoveryProbeState({
        ...latest,
        nextProbeAtMs: Date.now() + recoveryProbeRetryDelayMs
      }).catch(() => undefined)
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_distributed_recovery_probe_exception',
      runtimeKey
    }), 'Redis 运行态账号恢复探针执行异常，已等待下一轮')
  }
}

async function promoteDistributedRecoveryProbeToPrecheck(state: DistributedRecoveryProbeState): Promise<void> {
  const generation = await distributedRecoveryProbeStore.nextGeneration(state.runtimeKey, distributedRecoveryProbeStateTtlMs)
  const precheckState: DistributedRecoveryProbeState = {
    ...state,
    generation,
    attemptCount: 0,
    precheckRequested: true,
    reason: `后台恢复探针连续失败，等待事前确认；${state.reason}`.slice(0, 1000),
    nextProbeAtMs: Date.now()
  }
  const persisted = await persistDistributedRecoveryProbeState(precheckState)
  if (!persisted) {
    logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_schedule_ignored')
    return
  }
  const loaded = await loadDistributedRecoveryProbeStateWithAccount(precheckState)
  if (!loaded) {
    await clearDistributedRecoveryProbeStateGeneration(state.runtimeKey, generation)
    return
  }
  logger.warn({
    event: 'gateway_account_distributed_precheck_scheduled',
    accountId: loaded.account.id,
    accountName: loaded.account.name,
    runtimeKey: state.runtimeKey,
    generation,
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    source: 'recovery_probe'
  }, 'Redis 运行态账号恢复探针持续失败，已进入共享运行态事前确认')
  await runDistributedGatewayAccountPrecheck(precheckState, loaded.account)
}

async function runDistributedGatewayAccountPrecheck(
  initialState: DistributedRecoveryProbeState,
  account: OpenAIAccountSecret
): Promise<void> {
  let state = initialState
  const generation = state.generation
  for (let attempt = state.attemptCount; attempt < precheckMaxAttempts; attempt += 1) {
    const latest = await currentDistributedRecoveryProbeState(state.runtimeKey, generation)
    if (!latest) {
      logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_attempt_ignored')
      return
    }
    state = {
      ...latest,
      attemptCount: attempt + 1,
      nextProbeAtMs: Date.now() + precheckSuppressionMs()
    }
    const persistedAttempt = await persistDistributedRecoveryProbeState(state)
    if (!persistedAttempt) {
      logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_attempt_write_ignored')
      return
    }
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    const result = await runSingleGatewayAccountPrecheck(distributedStateWithAccount(state, account), timeoutMs)
    const stateAfterResult = await currentDistributedRecoveryProbeState(state.runtimeKey, generation)
    if (!stateAfterResult) {
      logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_result_ignored')
      return
    }
    if (result.success || result.accountFailureEligible === false) {
      const cleared = await clearDistributedRecoveryProbeStateGeneration(state.runtimeKey, generation)
      if (!cleared) {
        logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_recovery_ignored')
        return
      }
      logger.info({
        event: 'gateway_account_distributed_precheck_recovered',
        accountId: account.id,
        accountName: account.name,
        runtimeKey: state.runtimeKey,
        generation,
        attemptCount: stateAfterResult.attemptCount,
        statusCode: result.statusCode,
        durationMs: result.durationMs
      }, 'Redis 运行态账号事前确认探针通过，已清理共享运行态')
      return
    }
    state = {
      ...stateAfterResult,
      reason: accountPrecheckFailureReason(result)
    }
    const persistedFailure = await persistDistributedRecoveryProbeState(state)
    if (!persistedFailure) {
      logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_failure_write_ignored')
      return
    }
  }

  const finalState = await currentDistributedRecoveryProbeState(state.runtimeKey, generation)
  if (!finalState) {
    logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_final_ignored')
    return
  }
  const currentConcurrency = await getAccountCurrentConcurrencyAsync(account.id)
  if (currentConcurrency > 0) {
    const reason = `事前确认探针连续失败，等待 ${currentConcurrency} 个在途请求结束后再标记临时不可调用；${finalState.reason}`.slice(0, 1000)
    const persistedDefer = await persistDistributedRecoveryProbeState({
      ...finalState,
      reason,
      nextProbeAtMs: Date.now() + precheckConcurrencyDrainPollMs
    })
    if (!persistedDefer) {
      logStaleDistributedRecoveryProbeResult(state.runtimeKey, generation, 'gateway_account_distributed_precheck_stale_mark_defer_ignored')
      return
    }
    logger.warn({
      event: 'gateway_account_distributed_precheck_mark_deferred_for_concurrency',
      accountId: account.id,
      accountName: account.name,
      runtimeKey: finalState.runtimeKey,
      generation,
      currentConcurrency
    }, 'Redis 运行态账号事前确认探针连续失败，但仍有在途并发，已延后写入临时不可调用')
    return
  }

  const reason = `事前确认探针连续失败 ${finalState.attemptCount} 次，已标记为临时不可调用；${finalState.reason}`.slice(0, 1000)
  const markResult = await requestGatewayDbService({
    type: 'mark_account_precheck_temporary_unavailable',
    account,
    reason,
    precheckStartedAt: new Date(finalState.startedAtMs).toISOString()
  })
  await clearDistributedRecoveryProbeStateGeneration(finalState.runtimeKey, generation)
  if (markResult.updated) {
    clearGatewayRuntimeCache()
  }
  logger.warn({
    event: 'gateway_account_distributed_precheck_failed_marked',
    accountId: account.id,
    accountName: account.name,
    runtimeKey: finalState.runtimeKey,
    generation,
    updated: markResult.updated,
    skippedReason: markResult.skippedReason,
    attemptCount: finalState.attemptCount
  }, 'Redis 运行态账号事前确认探针连续失败，已写入临时不可调用')
}

function promoteRecoveryProbeToPrecheck(runtimeKey: string, state: RecoveryProbeState): void {
  const reason = `后台恢复探针连续失败，等待事前确认；${state.reason}`.slice(0, 1000)
  const generation = nextRuntimeProbeGeneration(runtimeKey)
  clearGatewayAccountRecoveryProbe(runtimeKey)
  precheckStates.set(runtimeKey, {
    generation,
    account: state.account,
    settings: state.settings,
    systemAccountId: state.systemAccountId,
    groupId: state.groupId,
    startedAtMs: state.startedAtMs,
    attemptCount: 0,
    failureCount: state.failureCount,
    reason,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    running: false
  })
  suppressLocalAccount(runtimeKey, precheckSuppressionMs(), reason, 'precheck_pending', {
    accountId: state.account.id,
    sinceMs: state.startedAtMs,
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    precheckAttemptCount: 0
  })
  logger.warn({
    event: 'gateway_account_precheck_scheduled',
    accountId: state.account.id,
    accountName: state.account.name,
    runtimeKey,
    generation,
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    source: 'recovery_probe'
  }, '账号后台恢复探针持续失败，已进入运行态待确认')
  scheduleGatewayAccountPrecheckRun(runtimeKey, 0)
}

function recoveryProbeDelayMs(delayMs: number | undefined): number {
  if (typeof delayMs === 'number' && Number.isFinite(delayMs)) {
    return Math.max(1000, Math.min(Math.trunc(delayMs), recoveryProbeRetryDelayMs))
  }
  return 3000
}

function recoveryProbeFollowupDelayMs(observedForMs: number): number {
  if (observedForMs < localDegradationMinObservationMs) {
    return Math.max(1000, Math.min(recoveryProbeRetryDelayMs, localDegradationMinObservationMs - observedForMs))
  }
  return recoveryProbeRetryDelayMs
}

function nextRuntimeProbeGeneration(runtimeKey: string): number {
  const next = (runtimeProbeGenerationCounters.get(runtimeKey) ?? 0) + 1
  runtimeProbeGenerationCounters.set(runtimeKey, next)
  return next
}

function currentRecoveryProbeState(runtimeKey: string, generation: number): RecoveryProbeState | undefined {
  const state = recoveryProbeStates.get(runtimeKey)
  return state?.generation === generation ? state : undefined
}

function currentPrecheckState(runtimeKey: string, generation: number): PrecheckState | undefined {
  const state = precheckStates.get(runtimeKey)
  return state?.generation === generation ? state : undefined
}

function recoveryProbeBudgetWaitMs(state: RecoveryProbeState, now: number): number {
  let waitMs = runningRecoveryProbeCount >= recoveryProbeMaxConcurrentRuns ? recoveryProbeBudgetDelayMs : 0
  for (const scope of recoveryProbeBudgetScopes(state.account)) {
    const lastStartedAtMs = recoveryProbeLastStartedAtByScope.get(scope.key)
    if (lastStartedAtMs === undefined) continue
    waitMs = Math.max(waitMs, lastStartedAtMs + scope.minIntervalMs - now)
  }
  if (waitMs <= 0) return 0
  return Math.ceil(waitMs) + randomRecoveryProbeJitterMs()
}

function markRecoveryProbeStarted(state: RecoveryProbeState, now: number): void {
  cleanupRecoveryProbeBudgetScopes(now)
  for (const scope of recoveryProbeBudgetScopes(state.account)) {
    recoveryProbeLastStartedAtByScope.set(scope.key, now)
  }
}

function recoveryProbeBudgetScopes(account: OpenAIAccountSecret): Array<{ key: string; minIntervalMs: number }> {
  const scopes = [
    { key: `account:${account.id}`, minIntervalMs: recoveryProbeAccountMinIntervalMs },
    { key: `provider:${account.providerCode}`, minIntervalMs: recoveryProbeScopeMinIntervalMs },
    { key: `base_url:${normalizedRecoveryProbeBaseUrlScope(account.baseUrl)}`, minIntervalMs: recoveryProbeScopeMinIntervalMs }
  ]
  const proxyScope = account.proxyProfileId || account.proxyUrl
  if (proxyScope) {
    scopes.push({ key: `proxy:${proxyScope}`, minIntervalMs: recoveryProbeScopeMinIntervalMs })
  }
  return scopes
}

function cleanupRecoveryProbeBudgetScopes(now: number): void {
  const maxAgeMs = Math.max(recoveryProbeAccountMinIntervalMs, recoveryProbeScopeMinIntervalMs) * 10
  for (const [key, lastStartedAtMs] of recoveryProbeLastStartedAtByScope) {
    if (now - lastStartedAtMs > maxAgeMs) {
      recoveryProbeLastStartedAtByScope.delete(key)
    }
  }
}

function normalizedRecoveryProbeBaseUrlScope(value: string): string {
  try {
    const url = new URL(value)
    return `${url.protocol}//${url.host}`.toLowerCase()
  } catch {
    return value.trim().toLowerCase() || 'unknown'
  }
}

function randomRecoveryProbeJitterMs(): number {
  return Math.floor(Math.random() * recoveryProbeJitterMs)
}

function rescheduleLatestRecoveryProbeAfterStaleResult(runtimeKey: string, staleGeneration: number, event: string): void {
  const latest = recoveryProbeStates.get(runtimeKey)
  if (!latest || latest.generation === staleGeneration) {
    return
  }
  latest.running = false
  scheduleRecoveryProbeTimer(runtimeKey, Math.max(0, latest.nextProbeAtMs - Date.now()))
  logger.info({
    event,
    runtimeKey,
    staleGeneration,
    latestGeneration: latest.generation,
    accountId: latest.account.id,
    accountName: latest.account.name,
    nextProbeAt: new Date(latest.nextProbeAtMs).toISOString()
  }, '账号运行态恢复探针旧 generation 结果已忽略，并唤醒最新探针')
}

function logStalePrecheckResult(runtimeKey: string, staleGeneration: number, event: string): void {
  const latest = precheckStates.get(runtimeKey)
  logger.info({
    event,
    runtimeKey,
    staleGeneration,
    latestGeneration: latest?.generation
  }, '账号事前确认探针旧 generation 结果已忽略')
}

async function persistDistributedRecoveryProbeState(state: DistributedRecoveryProbeState): Promise<boolean> {
  const persisted = await distributedRecoveryProbeStore.set(state, distributedRecoveryProbeStateTtlMs)
  if (!persisted) {
    const current = await distributedRecoveryProbeStore.get(state.runtimeKey).catch(() => undefined)
    rememberDistributedRecoveryProbeSuppressionState(state.runtimeKey, current)
    if (current) {
      scheduleDistributedRecoveryProbeSweep(Math.max(0, current.nextProbeAtMs - Date.now()))
    }
    return false
  }
  rememberDistributedRecoveryProbeSuppressionState(state.runtimeKey, state)
  scheduleDistributedRecoveryProbeSweep(Math.max(0, state.nextProbeAtMs - Date.now()))
  return true
}

async function clearDistributedRecoveryProbeState(runtimeKey: string): Promise<void> {
  await distributedRecoveryProbeStore.delete(runtimeKey)
  rememberDistributedRecoveryProbeSuppressionState(runtimeKey, undefined)
  clearGatewayRuntimeCache()
}

async function clearDistributedRecoveryProbeStateGeneration(runtimeKey: string, generation: number): Promise<boolean> {
  const cleared = await distributedRecoveryProbeStore.deleteGeneration(runtimeKey, generation)
  if (cleared) {
    rememberDistributedRecoveryProbeSuppressionState(runtimeKey, undefined)
    clearGatewayRuntimeCache()
    return true
  }
  const current = await distributedRecoveryProbeStore.get(runtimeKey).catch(() => undefined)
  rememberDistributedRecoveryProbeSuppressionState(runtimeKey, current)
  if (current) {
    scheduleDistributedRecoveryProbeSweep(Math.max(0, current.nextProbeAtMs - Date.now()))
  }
  return false
}

async function currentDistributedRecoveryProbeState(
  runtimeKey: string,
  generation: number
): Promise<DistributedRecoveryProbeState | undefined> {
  const state = await distributedRecoveryProbeStore.get(runtimeKey)
  return state?.generation === generation ? state : undefined
}

async function loadDistributedRecoveryProbeStateWithAccount(
  state: DistributedRecoveryProbeState
): Promise<RecoveryProbeState | undefined> {
  const account = await requestGatewayDbService({
    type: 'find_openai_account_for_group',
    groupId: state.groupId,
    accountId: state.accountId,
    systemAccountId: state.systemAccountId,
    includeUnavailable: true,
    ignoreAvailability: true
  }, { timeoutMs: 10_000 })
  return account ? distributedStateWithAccount(state, account) : undefined
}

function distributedStateWithAccount(state: DistributedRecoveryProbeState, account: OpenAIAccountSecret): RecoveryProbeState {
  return {
    generation: state.generation,
    account,
    settings: state.settings,
    systemAccountId: state.systemAccountId,
    groupId: state.groupId,
    startedAtMs: state.startedAtMs,
    lastObservedAtMs: state.lastObservedAtMs,
    nextProbeAtMs: state.nextProbeAtMs,
    attemptCount: state.attemptCount,
    failureCount: state.failureCount,
    reason: state.reason,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    running: false,
    precheckRequested: state.precheckRequested
  }
}

function logStaleDistributedRecoveryProbeResult(runtimeKey: string, staleGeneration: number, event: string): void {
  logger.info({
    event,
    runtimeKey,
    staleGeneration
  }, 'Redis 运行态账号探针旧 generation 结果已忽略')
}

function clearGatewayAccountRecoveryProbe(runtimeKey: string): boolean {
  const timer = recoveryProbeTimers.get(runtimeKey)
  if (timer) {
    clearTimeout(timer)
    recoveryProbeTimers.delete(runtimeKey)
  }
  return recoveryProbeStates.delete(runtimeKey)
}

function scheduleGatewayAccountPrecheckRun(runtimeKey: string, delayMs: number): void {
  const existing = precheckRunTimers.get(runtimeKey)
  if (existing) {
    clearTimeout(existing)
  }
  const timer = setTimeout(() => {
    precheckRunTimers.delete(runtimeKey)
    void runGatewayAccountPrecheck(runtimeKey)
  }, Math.max(0, delayMs))
  timer.unref()
  precheckRunTimers.set(runtimeKey, timer)
}

function clearGatewayAccountPrecheckRunTimer(runtimeKey: string): boolean {
  const timer = precheckRunTimers.get(runtimeKey)
  if (!timer) {
    return false
  }
  clearTimeout(timer)
  precheckRunTimers.delete(runtimeKey)
  return true
}

function recordGatewayAccountSuccessObservation(runtimeKey: string): void {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  cleanupExpiredFailureStorms()
  const now = Date.now()
  const current = successObservations.get(runtimeKey)
  const entry: SuccessObservationEntry = current && now - current.firstSeenMs <= failureStormWindowMs
    ? current
    : {
        firstSeenMs: now,
        lastSeenMs: now,
        successCount: 0
      }
  entry.lastSeenMs = now
  entry.successCount += 1
  successObservations.set(runtimeKey, entry)
}

function shouldTriggerFailureStormPrecheck(
  runtimeKey: string,
  entry: FailureStormEntry,
  forcePrecheck: boolean,
  now: number
): FailureStormPrecheckDecision {
  const successObservation = successObservations.get(runtimeKey)
  const successCount = successObservation?.successCount ?? 0
  const total = entry.failureCount + successCount
  const failureRatio = total > 0 ? entry.failureCount / total : 1

  if (forcePrecheck) {
    return now - entry.firstSeenMs >= failureStormMinObservationMs
      ? { trigger: true, successCount, failureRatio }
      : { trigger: false, successCount, failureRatio, skippedReason: 'observation_window' }
  }
  if (entry.failureCount < failureStormThresholdCount || entry.clientIps.size < failureStormDistinctIpThreshold) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'below_threshold' }
  }
  if (now - entry.firstSeenMs < failureStormMinObservationMs) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'observation_window' }
  }
  if (successObservation && now - successObservation.lastSeenMs <= failureStormRecentSuccessGraceMs) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'recent_success' }
  }
  if (failureRatio < failureStormFailureRatioThreshold) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'failure_ratio' }
  }
  return { trigger: true, successCount, failureRatio }
}

export function snapshotGatewayAccountRuntimeAvailability(): Record<string, AccountRuntimeAvailability> {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return {}
  cleanupExpiredFailureStorms()
  const snapshot = snapshotLocalAccountRuntimeAvailability(isPrecheckRuntimeBlocking)
  for (const [runtimeKey, state] of precheckStates) {
    snapshot[runtimeKey] = {
      ...snapshot[runtimeKey],
      status: 'precheck_pending',
      reason: state.reason,
      since: new Date(state.startedAtMs).toISOString(),
      until: snapshot[runtimeKey]?.until,
      failureCount: state.failureCount,
      distinctClientIpCount: state.distinctClientIpCount,
      distinctApiKeyCount: state.distinctApiKeyCount,
      precheckAttemptCount: state.attemptCount
    }
  }
  return snapshot
}

export function filterLocallySuppressedGatewayAccounts<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountSuppressionFilterOptions = {}
): LocalAccountSuppressionFilterResult<T> {
  return filterLocalAccountSuppressions(accounts, isPrecheckRuntimeBlocking, options)
}

export async function filterGatewayAccountRuntimeSuppressionsAsync<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountSuppressionFilterOptions = {}
): Promise<LocalAccountSuppressionFilterResult<T>> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return filterLocallySuppressedGatewayAccounts(accounts, options)
  }
  return await filterDistributedRecoveryProbeSuppressions(accounts)
}

async function filterDistributedRecoveryProbeSuppressions<T extends SuppressibleGatewayAccount>(
  accounts: T[]
): Promise<LocalAccountSuppressionFilterResult<T>> {
  const now = Date.now()
  const states = await Promise.all(accounts.map(async (account) => {
    const runtimeKey = gatewayAccountRuntimeKey(account)
    const cached = cachedDistributedRecoveryProbeSuppressionState(runtimeKey, now)
    const state = cached.hit ? cached.state : await loadDistributedRecoveryProbeSuppressionState(runtimeKey, now)
    return { account, runtimeKey, state }
  }))
  const suppressed = states.filter((item) => item.state)
  const suppressedRuntimeKeys = new Set(suppressed.map((item) => item.runtimeKey))
  const visibleAccounts = states
    .filter((item) => !suppressedRuntimeKeys.has(item.runtimeKey))
    .map((item) => item.account)
  const nextRetryAtMs = suppressed
    .map((item) => item.state?.nextProbeAtMs ?? now + distributedRecoveryProbeDueRetryDelayMs)
    .reduce<number | undefined>((earliest, value) => {
      const retryAtMs = value <= now ? now + distributedRecoveryProbeDueRetryDelayMs : value
      return earliest === undefined ? retryAtMs : Math.min(earliest, retryAtMs)
    }, undefined)
  return {
    accounts: visibleAccounts,
    suppressedCount: suppressed.length,
    allSuppressed: accounts.length > 0 && visibleAccounts.length === 0,
    suppressedAccountIds: suppressed.map((item) => item.account.id),
    acquiredHalfOpenLeases: [],
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - now)
  }
}

function cachedDistributedRecoveryProbeSuppressionState(
  runtimeKey: string,
  now: number
): { hit: boolean; state?: DistributedRecoveryProbeState } {
  const entry = distributedRecoveryProbeSuppressionCache.get(runtimeKey)
  if (!entry) return { hit: false }
  if (entry.expiresAtMs <= now) {
    distributedRecoveryProbeSuppressionCache.delete(runtimeKey)
    return { hit: false }
  }
  return { hit: true, state: entry.state }
}

async function loadDistributedRecoveryProbeSuppressionState(
  runtimeKey: string,
  now: number
): Promise<DistributedRecoveryProbeState | undefined> {
  const state = await distributedRecoveryProbeStore.get(runtimeKey)
  rememberDistributedRecoveryProbeSuppressionState(runtimeKey, state, now)
  return state
}

function rememberDistributedRecoveryProbeSuppressionState(
  runtimeKey: string,
  state: DistributedRecoveryProbeState | undefined,
  now = Date.now()
): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  const ttlMs = state ? distributedRecoveryProbeSuppressionCacheTtlMs : distributedRecoveryProbeSuppressionNegativeCacheTtlMs
  distributedRecoveryProbeSuppressionCache.set(runtimeKey, {
    state,
    expiresAtMs: now + ttlMs
  })
  evictDistributedRecoveryProbeSuppressionCacheIfNeeded(now)
}

function evictDistributedRecoveryProbeSuppressionCacheIfNeeded(now: number): void {
  if (distributedRecoveryProbeSuppressionCache.size <= distributedRecoveryProbeSuppressionCacheMaxEntries) return
  for (const [runtimeKey, entry] of distributedRecoveryProbeSuppressionCache) {
    if (entry.expiresAtMs <= now || distributedRecoveryProbeSuppressionCache.size > distributedRecoveryProbeSuppressionCacheMaxEntries) {
      distributedRecoveryProbeSuppressionCache.delete(runtimeKey)
    }
    if (distributedRecoveryProbeSuppressionCache.size <= distributedRecoveryProbeSuppressionCacheMaxEntries) return
  }
}

export function getGatewayAccountSideEffectState(): GatewayAccountSideEffectState {
  if (canUseProcessLocalGatewayAccountRuntimeState()) {
    cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
  }
  const nextAttemptAtMs = sideEffectQueue.peek()?.nextAttemptAtMs
  return {
    queueLength: sideEffectQueue.length,
    processing: processingSideEffects,
    enqueuedCount,
    completedCount,
    coalescedCount,
    canceledBySuccessCount,
    skippedHealthySuccessCount,
    failedAttemptCount,
    droppedCount,
    expiredCount,
    localSuppressedAccountCount: countVisibleLocalSuppressions(isPrecheckRuntimeBlocking),
    degradedAccountCount: countLocalAccountDegradations(),
    precheckPendingAccountCount: runtimeConfig.runtimeStateDriver === 'redis' ? 0 : precheckStates.size,
    nextAttemptAt: nextAttemptAtMs === undefined ? undefined : new Date(nextAttemptAtMs).toISOString()
  }
}

export function degradeGatewayAccountForRuntimeFailure(
  account: SuppressibleGatewayAccount | string,
  reason = '账号近期失败'
): AccountRuntimeAvailability {
  return degradeLocalAccountForGatewayFailure(gatewayAccountRuntimeKey(account), gatewayAccountId(account), reason)
}

export function orderGatewayAccountsByRuntimeDegradation<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountDegradationOrderOptions = {}
): LocalAccountDegradationOrderResult<T> {
  return orderLocalAccountDegradations(accounts, options)
}

export async function flushGatewayAccountSideEffectsForTest(): Promise<void> {
  await flushGatewayAccountSideEffects()
}

export function clearGatewayAccountSideEffectQueueForTest(): void {
  sideEffectQueue.clear()
  if (drainTimer) {
    clearTimeout(drainTimer)
    drainTimer = undefined
    drainTimerDueAtMs = undefined
  }
}

export function suppressGatewayAccountLocallyForTest(
  accountId: string,
  durationMs: number,
  reason = '测试本地屏蔽',
  status: AccountRuntimeAvailability['status'] = 'local_suppressed',
  metadata: Partial<Pick<LocalAccountSuppression, 'sinceMs' | 'localFailureCount' | 'halfOpenLeaseUntilMs' | 'halfOpenLeaseId'>> = {}
): void {
  suppressLocalAccount(accountId, Math.max(0, Math.trunc(durationMs)), reason, status, {
    accountId,
    ...metadata
  })
}

export function ageGatewayAccountRuntimeDegradationForTest(
  account: SuppressibleGatewayAccount | string,
  ageMs: number
): void {
  ageLocalAccountDegradationForTest(gatewayAccountRuntimeKey(account), ageMs)
}

export function activateGatewayAccountRuntimeDegradationForTest(
  account: SuppressibleGatewayAccount | string,
  reason = '测试激活运行态调度降级'
): AccountRuntimeAvailability {
  const runtimeKey = gatewayAccountRuntimeKey(account)
  return activateLocalAccountRuntimeDegradation(runtimeKey, gatewayAccountId(account), reason, {
    sinceMs: Date.now() - localDegradationMinObservationMs
  })
}

export function ageGatewayAccountFailureStormForTest(
  account: SuppressibleGatewayAccount | string,
  ageMs: number
): void {
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const agedFirstSeenMs = Date.now() - Math.max(0, Math.trunc(ageMs))
  const failure = failureStorms.get(runtimeKey)
  if (failure) {
    failure.firstSeenMs = Math.min(failure.firstSeenMs, agedFirstSeenMs)
    failureStorms.set(runtimeKey, failure)
  }
  const recovery = recoveryProbeStates.get(runtimeKey)
  if (recovery) {
    recovery.startedAtMs = Math.min(recovery.startedAtMs, agedFirstSeenMs)
    recovery.nextProbeAtMs = Date.now()
    recoveryProbeStates.set(runtimeKey, recovery)
  }
}

export async function flushGatewayAccountRecoveryProbesForTest(): Promise<void> {
  for (const timer of recoveryProbeTimers.values()) {
    clearTimeout(timer)
  }
  recoveryProbeTimers.clear()
  for (const state of recoveryProbeStates.values()) {
    state.nextProbeAtMs = Date.now()
  }
  const runtimeKeys = [...recoveryProbeStates.keys()]
  for (const runtimeKey of runtimeKeys) {
    await runGatewayAccountRecoveryProbe(runtimeKey)
  }
}

export function clearGatewayLocalAccountSuppressionsForTest(): void {
  clearAllPrecheckConcurrencyDrainWaits()
  clearAllGatewayAccountRecoveryProbes()
  clearAllGatewayAccountPrecheckRunTimers()
  clearLocalAccountSuppressionsForTest()
  failureStorms.clear()
  successObservations.clear()
  precheckStates.clear()
  runtimeProbeGenerationCounters.clear()
  recoveryProbeLastStartedAtByScope.clear()
  runningRecoveryProbeCount = 0
}

export function clearGatewayAccountRuntimeAvailability(
  account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string
): GatewayAccountRuntimeClearResult {
  const clearedKeys: string[] = []
  for (const runtimeKey of gatewayAccountRuntimeClearKeys(account)) {
    if (runtimeConfig.runtimeStateDriver === 'redis') {
      void clearDistributedRecoveryProbeState(runtimeKey).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'gateway_account_distributed_runtime_availability_clear_failed',
          runtimeKey
        }), 'Redis 运行态账号恢复状态清理失败')
      })
      clearedKeys.push(runtimeKey)
    } else if (clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)) {
      clearedKeys.push(runtimeKey)
    }
  }
  if (clearedKeys.length > 0) {
    clearGatewayRuntimeCache()
    logger.info({
      event: 'gateway_account_runtime_availability_cleared',
      runtimeKeys: clearedKeys
    }, '已手动清理账号网关运行态避让')
  }
  return {
    cleared: clearedKeys.length > 0,
    clearedKeys
  }
}

export async function flushGatewayAccountSideEffects(): Promise<void> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return
  }
  if (drainTimer) {
    clearTimeout(drainTimer)
    drainTimer = undefined
    drainTimerDueAtMs = undefined
  }

  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (!processingSideEffects) {
      await drainSideEffectQueue()
    }
    if (!processingSideEffects && sideEffectQueue.length === 0) {
      return
    }
    await delay(10)
  }
}

function enqueueAccountSideEffect(operation: AccountSideEffectOperation): void {
  if (sideEffectQueue.length >= maxSideEffectQueueLength) {
    droppedCount += 1
    logDroppedAccountSideEffect(operation)
    return
  }
  const now = Date.now()
  sideEffectQueue.push({
    operation,
    attempts: 0,
    enqueuedAtMs: now,
    nextAttemptAtMs: now,
    expiresAtMs: now + sideEffectRetentionMs
  })
  enqueuedCount += 1
  scheduleSideEffectDrain(0)
}

function coalesceQueuedAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): boolean {
  const index = sideEffectQueue.findIndex((item) => shouldCoalesceQueuedAccountErrorHandlingSideEffect(item, operation))
  if (index < 0) {
    return false
  }
  const now = Date.now()
  sideEffectQueue.replaceAt(index, {
    operation,
    attempts: 0,
    enqueuedAtMs: now,
    nextAttemptAtMs: now,
    expiresAtMs: now + sideEffectRetentionMs
  })
  coalescedCount += 1
  scheduleSideEffectDrain(0)
  return true
}

function cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKey(runtimeKey: string): number {
  return sideEffectQueue.removeWhere((item) => shouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess(item, runtimeKey))
}

function logDroppedAccountSideEffect(operation: AccountSideEffectOperation): void {
  if (droppedCount > 10 && droppedCount % 100 !== 0) {
    return
  }
  logger.warn({
    event: 'gateway_account_side_effect_queue_full',
    operationType: operation.type,
    accountId: operationAccountId(operation),
    queueLength: sideEffectQueue.length,
    droppedCount
  }, '网关账号副作用队列已满，已丢弃本次副作用')
}

function scheduleSideEffectDrain(delayMs: number): void {
  const dueAtMs = Date.now() + Math.max(0, delayMs)
  if (drainTimer) {
    if (drainTimerDueAtMs !== undefined && drainTimerDueAtMs <= dueAtMs) {
      return
    }
    clearTimeout(drainTimer)
    drainTimer = undefined
    drainTimerDueAtMs = undefined
  }
  drainTimerDueAtMs = dueAtMs
  drainTimer = setTimeout(() => {
    drainTimer = undefined
    drainTimerDueAtMs = undefined
    void drainSideEffectQueue()
  }, Math.max(0, delayMs))
  drainTimer.unref()
}

async function drainSideEffectQueue(): Promise<void> {
  if (processingSideEffects) {
    return
  }
  processingSideEffects = true
  try {
    while (sideEffectQueue.length > 0) {
      const now = Date.now()
      dropExpiredSideEffects(now)
      const item = sideEffectQueue.peek()
      if (!item) {
        break
      }
      if (item.nextAttemptAtMs > now) {
        scheduleSideEffectDrain(item.nextAttemptAtMs - now)
        break
      }
      sideEffectQueue.pop()
      try {
        await executeAccountSideEffect(item.operation)
        completedCount += 1
      } catch (error) {
        failedAttemptCount += 1
        if (Date.now() >= item.expiresAtMs) {
          expiredCount += 1
          logger.warn(errorLogFields(error, {
            event: 'gateway_account_side_effect_expired',
            operationType: item.operation.type,
            accountId: operationAccountId(item.operation),
            attempts: item.attempts + 1
          }), '网关账号副作用写入超过重试窗口，已丢弃')
          continue
        }
        item.attempts += 1
        item.nextAttemptAtMs = retryDueAtMs(sideEffectRetryPolicy, item.attempts)
        sideEffectQueue.push(item)
        logger.warn(errorLogFields(error, {
          event: 'gateway_account_side_effect_retry_scheduled',
          operationType: item.operation.type,
          accountId: operationAccountId(item.operation),
          attempts: item.attempts,
          retryAt: new Date(item.nextAttemptAtMs).toISOString()
        }), '网关账号副作用写入失败，已加入重试')
        scheduleSideEffectDrain(item.nextAttemptAtMs - Date.now())
        break
      }
    }
  } finally {
    processingSideEffects = false
    scheduleNextDrainIfNeeded()
  }
}

async function executeAccountSideEffect(operation: AccountSideEffectOperation): Promise<void> {
  const result = await requestGatewayDbService(operation)
  const runtimeKey = gatewayAccountRuntimeKey(operation.account)
  if (result.changed) {
    await clearGatewayAccountRuntimeAvailabilityForRuntimeKey(runtimeKey)
    clearGatewayRuntimeCache()
  } else if (operation.input.success && result.accountStatus === 'active') {
    await clearGatewayAccountRuntimeAvailabilityForRuntimeKey(runtimeKey)
  }
}

function scheduleNextDrainIfNeeded(): void {
  if (processingSideEffects || drainTimer || sideEffectQueue.length === 0) {
    return
  }
  const nextAttemptAtMs = sideEffectQueue.peek()?.nextAttemptAtMs
  if (nextAttemptAtMs !== undefined) {
    scheduleSideEffectDrain(Math.max(0, nextAttemptAtMs - Date.now()))
  }
}

function dropExpiredSideEffects(now: number): void {
  const removed = sideEffectQueue.removeWhere((item) => item.expiresAtMs <= now)
  if (removed === 0) {
    return
  }
  expiredCount += removed
}

function clearGatewayAccountRuntimeAvailabilityLocal(accountId: string): boolean {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return false
  let cleared = false
  clearPrecheckConcurrencyDrainWait(accountId)
  cleared = clearGatewayAccountRecoveryProbe(accountId) || cleared
  cleared = clearGatewayAccountPrecheckRunTimer(accountId) || cleared
  cleared = clearLocalAccountSuppression(accountId) || cleared
  cleared = clearLocalAccountDegradation(accountId) || cleared
  cleared = failureStorms.delete(accountId) || cleared
  cleared = precheckStates.delete(accountId) || cleared
  return cleared
}

async function clearGatewayAccountRuntimeAvailabilityForRuntimeKey(runtimeKey: string): Promise<boolean> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await clearDistributedRecoveryProbeState(runtimeKey)
    return true
  }
  return clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
}

function cleanupExpiredFailureStorms(): void {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  const now = Date.now()
  for (const [runtimeKey, entry] of failureStorms) {
    if (now - entry.lastSeenMs > failureStormWindowMs) {
      failureStorms.delete(runtimeKey)
    }
  }
  for (const [runtimeKey, entry] of successObservations) {
    if (now - entry.lastSeenMs > failureStormWindowMs) {
      successObservations.delete(runtimeKey)
    }
  }
}

function isPrecheckRuntimeBlocking(runtimeKey: string): boolean {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return false
  return precheckStates.has(runtimeKey)
}

function precheckSuppressionMs(): number {
  return Math.min(precheckSuppressionGuardMs, localSuppressionMaxMs)
}

function operationAccountId(operation: AccountSideEffectOperation): string {
  return operation.account.id
}

function delay(ms: number): Promise<void> {
  return waitForRetryDelayMs(ms)
}

function clearPrecheckConcurrencyDrainWait(runtimeKey: string): void {
  const wait = precheckConcurrencyDrainWaits.get(runtimeKey)
  if (!wait) {
    return
  }
  wait.unsubscribe()
  clearInterval(wait.timer)
  precheckConcurrencyDrainWaits.delete(runtimeKey)
}

function clearAllPrecheckConcurrencyDrainWaits(): void {
  for (const runtimeKey of precheckConcurrencyDrainWaits.keys()) {
    clearPrecheckConcurrencyDrainWait(runtimeKey)
  }
}

function clearAllGatewayAccountRecoveryProbes(): void {
  for (const runtimeKey of recoveryProbeStates.keys()) {
    clearGatewayAccountRecoveryProbe(runtimeKey)
  }
  for (const [runtimeKey, timer] of recoveryProbeTimers) {
    clearTimeout(timer)
    recoveryProbeTimers.delete(runtimeKey)
  }
}

function clearAllGatewayAccountPrecheckRunTimers(): void {
  for (const [runtimeKey, timer] of precheckRunTimers) {
    clearTimeout(timer)
    precheckRunTimers.delete(runtimeKey)
  }
}

function deferPrecheckMarkUntilConcurrencyDrained(runtimeKey: string, state: PrecheckState): boolean {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return false
  const currentConcurrency = getAccountCurrentConcurrency(state.account.id)
  if (currentConcurrency <= 0) {
    return false
  }
  const reason = `事前确认探针连续失败，等待 ${currentConcurrency} 个在途请求结束后再标记临时不可调用；${state.reason}`.slice(0, 1000)
  state.reason = reason
  state.running = false
  state.waitingForConcurrencyDrain = true
  suppressLocalAccount(runtimeKey, precheckSuppressionMs(), reason, 'precheck_pending', {
    accountId: state.account.id,
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
    precheckAttemptCount: state.attemptCount
  })
  schedulePrecheckAfterConcurrencyDrain(runtimeKey, state.account.id)
  logger.warn({
    event: 'gateway_account_precheck_mark_deferred_for_concurrency',
    accountId: state.account.id,
    accountName: state.account.name,
    runtimeKey,
    currentConcurrency
  }, '账号事前确认探针连续失败，但仍有在途并发，已延后写入临时不可调用')
  return true
}

function schedulePrecheckAfterConcurrencyDrain(runtimeKey: string, accountId: string): void {
  if (precheckConcurrencyDrainWaits.has(runtimeKey)) {
    return
  }

  const tryResume = (): void => {
    const state = precheckStates.get(runtimeKey)
    if (!state) {
      clearPrecheckConcurrencyDrainWait(runtimeKey)
      return
    }
    if (getAccountCurrentConcurrency(accountId) > 0) {
      return
    }
    clearPrecheckConcurrencyDrainWait(runtimeKey)
    state.waitingForConcurrencyDrain = false
    void runGatewayAccountPrecheck(runtimeKey)
  }

  const unsubscribe = subscribeAccountConcurrencyRelease((event) => {
    if (event.accountId === accountId) {
      tryResume()
    }
  })
  const timer = setInterval(tryResume, precheckConcurrencyDrainPollMs)
  timer.unref()
  precheckConcurrencyDrainWaits.set(runtimeKey, { unsubscribe, timer })
  tryResume()
}

async function runGatewayAccountPrecheck(runtimeKey: string): Promise<void> {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  const state = precheckStates.get(runtimeKey)
  if (!state || state.running) {
    return
  }
  const generation = state.generation
  state.running = true
  try {
    for (let attempt = state.attemptCount; attempt < precheckMaxAttempts; attempt += 1) {
      const latestState = currentPrecheckState(runtimeKey, generation)
      if (!latestState) {
        logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_attempt_ignored')
        return
      }
      latestState.attemptCount = attempt + 1
      latestState.lastAttemptAtMs = Date.now()
      suppressLocalAccount(runtimeKey, precheckSuppressionMs(), latestState.reason, 'precheck_pending', {
        accountId: latestState.account.id,
        failureCount: latestState.failureCount,
        distinctClientIpCount: latestState.distinctClientIpCount,
        distinctApiKeyCount: latestState.distinctApiKeyCount,
        precheckAttemptCount: latestState.attemptCount
      })
      const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
      const result = await runSingleGatewayAccountPrecheck(latestState, timeoutMs)
      const stateAfterResult = currentPrecheckState(runtimeKey, generation)
      if (!stateAfterResult) {
        logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_result_ignored')
        return
      }
      if (result.success || result.accountFailureEligible === false) {
        clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
        logger.info({
          event: 'gateway_account_precheck_recovered',
          accountId: stateAfterResult.account.id,
          accountName: stateAfterResult.account.name,
          runtimeKey,
          generation,
          attemptCount: stateAfterResult.attemptCount,
          statusCode: result.statusCode,
          durationMs: result.durationMs
        }, '账号事前确认探针通过，已清理运行态短避让')
        return
      }
      stateAfterResult.reason = accountPrecheckFailureReason(result)
    }

    const finalState = currentPrecheckState(runtimeKey, generation)
    if (!finalState) {
      logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_final_ignored')
      return
    }
    if (deferPrecheckMarkUntilConcurrencyDrained(runtimeKey, finalState)) {
      return
    }
    const reason = `事前确认探针连续失败 ${finalState.attemptCount} 次，已标记为临时不可调用；${finalState.reason}`.slice(0, 1000)
    const markResult = await requestGatewayDbService({
      type: 'mark_account_precheck_temporary_unavailable',
      account: finalState.account,
      reason,
      precheckStartedAt: new Date(finalState.startedAtMs).toISOString()
    })
    if (markResult.updated) {
      clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
      clearGatewayRuntimeCache()
    } else {
      clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
    }
    precheckStates.delete(runtimeKey)
    failureStorms.delete(runtimeKey)
    logger.warn({
      event: 'gateway_account_precheck_failed_marked',
      accountId: finalState.account.id,
      accountName: finalState.account.name,
      runtimeKey,
      generation,
      updated: markResult.updated,
      skippedReason: markResult.skippedReason,
      attemptCount: finalState.attemptCount
    }, '账号事前确认探针连续失败，已写入临时不可调用')
  } catch (error) {
    const stateAfterError = currentPrecheckState(runtimeKey, generation)
    if (stateAfterError) {
      stateAfterError.running = false
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_precheck_exception',
      runtimeKey,
      generation
    }), '账号事前确认探针执行失败，保留运行态等待下一轮触发')
  }
}

function canUseProcessLocalGatewayAccountRuntimeState(): boolean {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return true
  clearAllPrecheckConcurrencyDrainWaits()
  clearAllGatewayAccountRecoveryProbes()
  clearAllGatewayAccountPrecheckRunTimers()
  failureStorms.clear()
  successObservations.clear()
  precheckStates.clear()
  recoveryProbeStates.clear()
  recoveryProbeLastStartedAtByScope.clear()
  runningRecoveryProbeCount = 0
  return false
}

async function runSingleGatewayAccountPrecheck(state: PrecheckState, timeoutMs: number): Promise<{
  success: boolean
  statusCode?: number
  errorCode?: string
  message?: string
  durationMs?: number
  accountFailureEligible?: boolean
}> {
  const { preferredSystemAccountTestModelAsync, testOpenAIAccount } = await import('../../accounts/account-test.service.js')
  const signal = AbortSignal.timeout(timeoutMs)
  const account = accountSummaryFromGatewayPrecheckAccount(state.account, state)
  return await testOpenAIAccount(account, {
    model: await preferredSystemAccountTestModelAsync(account),
    diagnostics: 'full',
    groupId: state.groupId,
    trafficSource: 'runtime_recovery_probe',
    signal,
    disableAccountStateMutation: true,
    findAccountForTest: (accountId, access) => requestGatewayDbService({
      type: 'find_account_for_test',
      accountId,
      access
    }, { timeoutMs: 10_000 }),
    gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(state.settings, timeoutMs)
  })
}

function accountPrecheckFailureReason(result: { statusCode?: number; errorCode?: string; message?: string }): string {
  const parts = ['最近事前确认探针失败']
  if (typeof result.statusCode === 'number' && Number.isFinite(result.statusCode)) {
    parts.push(`HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  }
  return parts.join('；').slice(0, 1000)
}

ensureDistributedRecoveryProbeSweeper()
