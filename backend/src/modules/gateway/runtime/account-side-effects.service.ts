import { createHash } from 'node:crypto'

import pLimit from 'p-limit'

import { errorLogFields, logger } from '../../../shared/logger.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { AccountRuntimeAvailability } from '../../db-service/db-service-types.js'
import type { AccountProbeObservation, AccountRuntimeProbePresentation } from '../../../domain/types.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import { exponentialRetryPolicy, retryDueAtMs, waitForRetryDelayMs } from '../../../shared/retry-policy.js'
import { createRuntimeProbeStateStore } from '../../../shared/runtime-probe-state-store.js'
import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'
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
import {
  automaticAccountProbeObservation,
  transportProbeOutcomeFromAccountTestResult,
  type AutomaticAccountProbeOutcome,
  type TransportProbeOutcome
} from '../../accounts/automatic-account-probe-outcome.js'
import type { UpstreamAttempt } from '../upstream/attempt.js'
import {
  accountPrecheckMinimumObservationMs,
  nextAccountPrecheckProbeAtMs
} from './account-probe-confirmation-policy.js'
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
import { gatewayAccountConcurrencyAccountId } from '../dispatch/account-concurrency-identity.js'
import { notifyOneRecoverableUnavailableRuntimeWaiter } from './recoverable-unavailable-wait.js'

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
  probePresentation?: AccountRuntimeProbePresentation
  attemptCount: number
  failureCount: number
  reason: string
  distinctClientIpCount: number
  distinctApiKeyCount: number
  running: boolean
  waitingForConcurrencyDrain?: boolean
  minimumObservationCompletedForTest?: boolean
  halfOpenLeaseId?: string
  halfOpenLeaseUntilMs?: number
  halfOpenPreviousNextProbeAtMs?: number
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
  probePresentation: AccountRuntimeProbePresentation
}

interface DistributedRecoveryProbeState {
  runtimeKey: string
  phase: 'recovery_wait' | 'precheck_pending'
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
  clientIpMarkers?: string[]
  apiKeyMarkers?: string[]
  precheckRequested: boolean
  probePresentation: AccountRuntimeProbePresentation
  probeRunId?: string
  probeRunUntilMs?: number
  halfOpenLeaseId?: string
  halfOpenLeaseUntilMs?: number
}

interface GatewayAutomaticProbeResult {
  success: boolean
  statusCode?: number
  errorCode?: string
  message?: string
  traceId?: string
  durationMs?: number
  accountFailureEligible?: boolean
  attemptedAt: string
  probeOutcome: Exclude<AutomaticAccountProbeOutcome, 'stale'>
  transportOutcome: TransportProbeOutcome
}

interface ConfiguredPolicyAvoidanceState {
  runtimeKey: string
  accountId: string
  reason: string
  startedAtMs: number
  untilMs: number
}

function visibleRuntimeProbePresentation(
  presentation: AccountRuntimeProbePresentation | undefined,
  input: { taskScheduled: boolean; running: boolean },
  now = Date.now()
): AccountRuntimeProbePresentation {
  const lastObservation = presentation?.lastObservation
  if (input.running) return { lastObservation, schedule: { state: 'running' } }
  const nextAttemptAt = presentation?.schedule.nextAttemptAt
  if (!input.taskScheduled || !nextAttemptAt) return { lastObservation, schedule: { state: 'none' } }
  const nextAttemptAtMs = Date.parse(nextAttemptAt)
  if (!Number.isFinite(nextAttemptAtMs)) return { lastObservation, schedule: { state: 'none' } }
  return {
    lastObservation,
    schedule: {
      state: nextAttemptAtMs <= now ? 'due_waiting' : 'scheduled',
      nextAttemptAt
    }
  }
}

function runtimeProbeScheduledPresentation(
  lastObservation: AccountProbeObservation | undefined,
  nextAttemptAtMs: number
): AccountRuntimeProbePresentation {
  return {
    lastObservation,
    schedule: { state: 'scheduled', nextAttemptAt: new Date(nextAttemptAtMs).toISOString() }
  }
}

function runtimeProbeObservation(
  runtimeKey: string,
  generation: number,
  attemptCount: number,
  result: GatewayAutomaticProbeResult
): AccountProbeObservation | undefined {
  return automaticAccountProbeObservation({
    runtimeKey,
    generation,
    attemptCount,
    attemptedAt: result.attemptedAt,
    probeOutcome: result.probeOutcome,
    success: result.transportOutcome.kind === 'framing_complete',
    statusCode: result.statusCode,
    errorCode: result.transportOutcome.kind === 'framing_complete' ? undefined : result.errorCode,
    reason: result.transportOutcome.kind === 'framing_complete' ? undefined : accountPrecheckFailureReason(result),
    traceId: result.traceId
  })
}

function runtimeProbeStateRunning(state: DistributedRecoveryProbeState, now = Date.now()): boolean {
  return Boolean(
    (state.probeRunId && (state.probeRunUntilMs ?? 0) > now)
    || (state.halfOpenLeaseId && (state.halfOpenLeaseUntilMs ?? 0) > now)
  )
}

export async function loadDistributedGatewayAccountRuntimeAvailability(
  runtimeKeys: string[]
): Promise<Record<string, AccountRuntimeAvailability>> {
  const [states, scheduledRuntimeKeys, configuredPolicyAvoidances] = await Promise.all([
    distributedRecoveryProbeStore.getMany(runtimeKeys),
    distributedRecoveryProbeStore.scheduledRuntimeKeys(runtimeKeys),
    configuredPolicyAvoidanceStore.getJsonMany<ConfiguredPolicyAvoidanceState>(runtimeKeys)
  ])
  const result: Record<string, AccountRuntimeAvailability> = {}
  for (const [runtimeKey, state] of states) {
    if (state.phase === 'recovery_wait' && state.attemptCount === 0) continue
    result[runtimeKey] = {
      status: state.phase === 'precheck_pending' ? 'precheck_pending' : 'degraded',
      reason: state.reason,
      since: new Date(state.startedAtMs).toISOString(),
      ...(state.phase === 'precheck_pending' ? { until: new Date(state.nextProbeAtMs).toISOString() } : {}),
      failureCount: state.failureCount,
      distinctClientIpCount: state.distinctClientIpCount,
      distinctApiKeyCount: state.distinctApiKeyCount,
      precheckAttemptCount: state.attemptCount,
      probePresentation: visibleRuntimeProbePresentation(state.probePresentation, {
        taskScheduled: scheduledRuntimeKeys.has(runtimeKey),
        running: runtimeProbeStateRunning(state)
      })
    }
  }
  configuredPolicyAvoidances.forEach((state, index) => {
    if (!state) return
    result[runtimeKeys[index]] = configuredPolicyAvoidanceAvailability(state)
  })
  return result
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
  recoveryProbePendingAccountCount: number
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
const recoveryProbeMaxConcurrentRuns = 3
const recoveryProbeAccountMinIntervalMs = 3_000
const recoveryProbeScopeMinIntervalMs = 1_000
const recoveryProbeBudgetDelayMs = 1_000
const recoveryProbeJitterMs = 750
const distributedRecoveryProbeStateTtlMs = Math.max(localSuppressionMaxMs, precheckSuppressionGuardMs) + 5 * 60_000
const distributedRecoveryProbeSweepIntervalMs = 1_000
const distributedRecoveryProbeSweepBatchSize = 25
const distributedRecoveryProbeDueRetryDelayMs = 250
const configuredPolicyAvoidanceCacheTtlMs = 1000
const configuredPolicyAvoidanceNegativeCacheTtlMs = 500
const configuredPolicyAvoidanceCacheMaxEntries = 5000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)
const maxSideEffectQueueLength = 5000
const gatewayAutomaticProbeLimit = pLimit(recoveryProbeMaxConcurrentRuns)

const distributedRecoveryProbeStore = createRuntimeProbeStateStore<DistributedRecoveryProbeState>('gateway-account-recovery')
const configuredPolicyAvoidanceStore = createRuntimeStateStore('gateway-configured-account-policy-avoidance')
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
const configuredPolicyAvoidancesMemory = new Map<string, ConfiguredPolicyAvoidanceState>()
const configuredPolicyAvoidanceCache = new Map<string, { state?: ConfiguredPolicyAvoidanceState; expiresAtMs: number }>()
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
  if (operation.input.trafficSource === 'gateway' && !operation.input.policyDecision) {
    return
  }
  if (operation.input.success) {
    const runtimeKey = accountErrorHandlingOperationRuntimeKey(operation)
    recordGatewayAccountSuccessObservation(runtimeKey)
    const canceledCount = cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKey(runtimeKey)
    if (canceledCount > 0) {
      canceledBySuccessCount += canceledCount
      await clearGatewayAccountRuntimeAvailabilityForRuntimeKey(runtimeKey)
    }
  } else if (coalesceQueuedAccountErrorHandlingSideEffect(operation)) {
    return
  }
  if (shouldSkipHealthySuccessfulAccountSideEffect(operation)) {
    await clearGatewayAccountRuntimeAvailabilityForRuntimeKey(gatewayAccountRuntimeKey(operation.account))
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
  return {
    runtimeKey,
    action: 'redis_managed',
    reason,
    localFailureCount: 0
  }
}

export async function suppressGatewayAccountLocallyForSeconds(
  account: SuppressibleGatewayAccount | string,
  seconds: number | undefined,
  reason = '响应检查策略运行态避让'
): Promise<void> {
  const value = typeof seconds === 'number' && Number.isFinite(seconds) ? Math.max(1, Math.trunc(seconds)) : 60
  const ttlMs = value * 1000
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const now = Date.now()
  const state: ConfiguredPolicyAvoidanceState = {
    runtimeKey,
    accountId: gatewayAccountId(account),
    reason,
    startedAtMs: now,
    untilMs: now + ttlMs
  }
  await configuredPolicyAvoidanceStore.setJson(runtimeKey, state, ttlMs)
  rememberConfiguredPolicyAvoidanceState(runtimeKey, state)
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    configuredPolicyAvoidancesMemory.set(runtimeKey, state)
  }
  clearGatewayRuntimeCache()
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
    running: false,
    minimumObservationCompletedForTest: true
  })
  await runGatewayAccountPrecheck(runtimeKey)
}

export function setGatewayAccountPrecheckRuntimeForTest(
  account: OpenAIAccountSecret,
  input: {
    systemAccountId: string
    groupId: string
    reason: string
    nextAttemptAtMs: number
    lastObservation?: AccountProbeObservation
  }
): void {
  if (!canUseProcessLocalGatewayAccountRuntimeState()) return
  const runtimeKey = gatewayAccountRuntimeKey(account)
  clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
  precheckStates.set(runtimeKey, {
    generation: nextRuntimeProbeGeneration(runtimeKey),
    account,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    startedAtMs: Date.now(),
    attemptCount: 1,
    failureCount: 1,
    reason: input.reason,
    distinctClientIpCount: 0,
    distinctApiKeyCount: 0,
    running: false,
    probePresentation: runtimeProbeScheduledPresentation(input.lastObservation, input.nextAttemptAtMs)
  })
  scheduleGatewayAccountPrecheckRun(runtimeKey, Math.max(0, input.nextAttemptAtMs - Date.now()))
}

export function dropGatewayAccountPrecheckTaskForTest(account: OpenAIAccountSecret): void {
  clearGatewayAccountPrecheckRunTimer(gatewayAccountRuntimeKey(account))
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
  if (current) return
  const generation = nextRuntimeProbeGeneration(runtimeKey)
  const state: RecoveryProbeState = {
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
    precheckRequested: input.precheckRequested,
    probePresentation: runtimeProbeScheduledPresentation(undefined, nextProbeAtMs)
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
  const state = recoveryProbeStates.get(runtimeKey)
  if (state) {
    state.probePresentation = runtimeProbeScheduledPresentation(
      state.probePresentation.lastObservation,
      Date.now() + Math.max(0, delayMs)
    )
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
  const current = await distributedRecoveryProbeStore.get(runtimeKey)
  if (current) {
    scheduleDistributedRecoveryProbeSweep(Math.max(0, current.nextProbeAtMs - Date.now()))
    return
  }
  const now = Date.now()
  const delayMs = recoveryProbeDelayMs(input.localSuppressionDelayMs)
  const generation = await distributedRecoveryProbeStore.nextGeneration(runtimeKey, distributedRecoveryProbeStateTtlMs)
  const nextProbeAtMs = now + delayMs
  const clientIpMarker = runtimeProbeObservationMarker(input.clientIp)
  const apiKeyMarker = runtimeProbeObservationMarker(input.apiKeyId)
  const state: DistributedRecoveryProbeState = {
    runtimeKey,
    phase: 'recovery_wait',
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
    distinctClientIpCount: clientIpMarker ? 1 : 0,
    distinctApiKeyCount: apiKeyMarker ? 1 : 0,
    ...(clientIpMarker ? { clientIpMarkers: [clientIpMarker] } : {}),
    ...(apiKeyMarker ? { apiKeyMarkers: [apiKeyMarker] } : {}),
    precheckRequested: input.forcePrecheck === true,
    probePresentation: runtimeProbeScheduledPresentation(undefined, nextProbeAtMs)
  }
  const created = await distributedRecoveryProbeStore.setIfAbsent(state, distributedRecoveryProbeStateTtlMs)
  if (!created) {
    const existing = await distributedRecoveryProbeStore.get(runtimeKey).catch(() => undefined)
    if (existing) scheduleDistributedRecoveryProbeSweep(Math.max(0, existing.nextProbeAtMs - Date.now()))
    return
  }
  scheduleDistributedRecoveryProbeSweep(Math.max(0, state.nextProbeAtMs - Date.now()))
  logger.info({
    event: 'gateway_account_distributed_recovery_probe_scheduled',
    accountId: account.id,
    accountName: account.name,
    runtimeKey,
    generation: state.generation,
    nextProbeAt: new Date(state.nextProbeAtMs).toISOString(),
    failureCount: state.failureCount,
    distinctClientIpCount: state.distinctClientIpCount,
    distinctApiKeyCount: state.distinctApiKeyCount,
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
  state.probePresentation = {
    lastObservation: state.probePresentation.lastObservation,
    schedule: { state: 'running' }
  }
  runningRecoveryProbeCount += 1
  markRecoveryProbeStarted(state, now)
  try {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[0] ?? 10_000
    const result = await runWithGatewayAutomaticProbeSlot(() => runSingleGatewayAccountPrecheck(state, timeoutMs))
    const latest = currentRecoveryProbeState(runtimeKey, generation)
    if (!latest) {
      rescheduleLatestRecoveryProbeAfterStaleResult(runtimeKey, generation, 'gateway_account_recovery_probe_stale_result_ignored')
      return
    }
    if (result.transportOutcome.kind === 'unknown') {
      latest.running = false
      latest.nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
      latest.probePresentation = runtimeProbeScheduledPresentation(latest.probePresentation.lastObservation, latest.nextProbeAtMs)
      recoveryProbeStates.set(runtimeKey, latest)
      scheduleRecoveryProbeTimer(runtimeKey, recoveryProbeRetryDelayMs)
      logger.info({
        event: 'gateway_account_recovery_probe_inconclusive_rescheduled',
        accountId: latest.account.id,
        accountName: latest.account.name,
        runtimeKey,
        generation,
        nextProbeAt: new Date(latest.nextProbeAtMs).toISOString(),
        failureKind: result.transportOutcome.failureKind
      }, '账号运行态后台恢复探针结论未知，已保留状态并有界重排')
      return
    }
    if (result.transportOutcome.kind === 'framing_complete') {
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
    latest.probePresentation = {
      lastObservation: runtimeProbeObservation(runtimeKey, generation, latest.attemptCount, result),
      schedule: { state: 'none' }
    }
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
  let claimedState: DistributedRecoveryProbeState | undefined
  let runId: string | undefined
  try {
    const persisted = await distributedRecoveryProbeStore.get(runtimeKey)
    if (!persisted) {
      await distributedRecoveryProbeStore.delete(runtimeKey)
      return
    }
    const now = Date.now()
    if (persisted.nextProbeAtMs > now) {
      scheduleDistributedRecoveryProbeSweep(persisted.nextProbeAtMs - now)
      return
    }
    if (persisted.phase === 'precheck_pending') {
      const precheckAccount = await loadDistributedRecoveryProbeStateWithAccount(persisted)
      if (!precheckAccount) {
        await clearDistributedRecoveryProbeStateGeneration(runtimeKey, persisted.generation)
        return
      }
      await runDistributedGatewayAccountPrecheck(persisted, precheckAccount.account)
      return
    }

    const timeoutMs = accountDiagnosticRetryTimeoutMs[0] ?? 10_000
    runId = distributedProbeRunId(runtimeKey, persisted.generation)
    claimedState = await distributedRecoveryProbeStore.acquireGenerationRun(
      runtimeKey,
      persisted.generation,
      runId,
      now + timeoutMs + 15_000,
      distributedRecoveryProbeStateTtlMs
    )
    if (!claimedState) return
    const state = await loadDistributedRecoveryProbeStateWithAccount(claimedState)
    if (!state) {
      await clearDistributedRecoveryProbeRun(runtimeKey, persisted.generation, runId)
      return
    }
    const budgetDelayMs = recoveryProbeBudgetWaitMs(state, now)
    if (budgetDelayMs > 0) {
      const nextProbeAtMs = now + budgetDelayMs
      const delayed = await commitDistributedRecoveryProbeRun({
        ...claimedState,
        nextProbeAtMs,
        probePresentation: runtimeProbeScheduledPresentation(claimedState.probePresentation.lastObservation, nextProbeAtMs)
      }, runId)
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

    const generation = claimedState.generation
    runningRecoveryProbeCount += 1
    markRecoveryProbeStarted(state, now)
    try {
      const result = await runWithGatewayAutomaticProbeSlot(() => runSingleGatewayAccountPrecheck(state, timeoutMs))
      if (result.transportOutcome.kind === 'unknown') {
        const nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
        const rescheduled = await commitDistributedRecoveryProbeRun({
          ...claimedState,
          nextProbeAtMs,
          probePresentation: runtimeProbeScheduledPresentation(claimedState.probePresentation.lastObservation, nextProbeAtMs)
        }, runId)
        if (!rescheduled) {
          logStaleDistributedRecoveryProbeResult(runtimeKey, generation, 'gateway_account_distributed_recovery_probe_stale_unknown_ignored')
          return
        }
        logger.info({
          event: 'gateway_account_distributed_recovery_probe_inconclusive_rescheduled',
          accountId: state.account.id,
          accountName: state.account.name,
          runtimeKey,
          generation,
          nextProbeAt: new Date(nextProbeAtMs).toISOString(),
          failureKind: result.transportOutcome.failureKind
        }, 'Redis 运行态账号恢复探针结论未知，已保留状态并有界重排')
        return
      }
      if (result.transportOutcome.kind === 'framing_complete') {
        const cleared = await clearDistributedRecoveryProbeRun(runtimeKey, generation, runId)
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
          attemptCount: claimedState.attemptCount + 1
        }, 'Redis 运行态账号恢复探针通过，已清理共享运行态')
        return
      }

      const failedState: DistributedRecoveryProbeState = {
        ...claimedState,
        attemptCount: claimedState.attemptCount + 1,
        reason: accountPrecheckFailureReason(result),
        probePresentation: {
          lastObservation: runtimeProbeObservation(runtimeKey, generation, claimedState.attemptCount + 1, result),
          schedule: { state: 'none' }
        }
      }
      const observedForMs = Date.now() - failedState.startedAtMs
      const shouldEnterPrecheck = observedForMs >= localDegradationMinObservationMs
        && (failedState.precheckRequested || failedState.attemptCount >= recoveryProbePrecheckFailureThreshold)
      if (shouldEnterPrecheck) {
        await promoteDistributedRecoveryProbeToPrecheck(failedState)
        return
      }

      const delayMs = recoveryProbeFollowupDelayMs(observedForMs)
      const nextProbeAtMs = Date.now() + delayMs
      const persistedFailure = await commitDistributedRecoveryProbeRun({
        ...failedState,
        nextProbeAtMs,
        probePresentation: runtimeProbeScheduledPresentation(failedState.probePresentation.lastObservation, nextProbeAtMs)
      }, runId)
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
        nextProbeAt: new Date(nextProbeAtMs).toISOString()
      }, 'Redis 运行态账号恢复探针未通过，已按时间窗口等待下一轮')
    } finally {
      runningRecoveryProbeCount = Math.max(0, runningRecoveryProbeCount - 1)
    }
  } catch (error) {
    if (claimedState && runId) {
      const nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
      await commitDistributedRecoveryProbeRun({
        ...claimedState,
        nextProbeAtMs,
        probePresentation: runtimeProbeScheduledPresentation(claimedState.probePresentation.lastObservation, nextProbeAtMs)
      }, runId).catch(() => undefined)
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_distributed_recovery_probe_exception',
      runtimeKey
    }), 'Redis 运行态账号恢复探针执行异常，已等待下一轮')
  }
}

async function promoteDistributedRecoveryProbeToPrecheck(state: DistributedRecoveryProbeState): Promise<void> {
  const generation = await distributedRecoveryProbeStore.nextGeneration(state.runtimeKey, distributedRecoveryProbeStateTtlMs)
  const nextProbeAtMs = Date.now()
  const precheckState: DistributedRecoveryProbeState = {
    ...state,
    phase: 'precheck_pending',
    generation,
    attemptCount: 0,
    precheckRequested: true,
    reason: `后台恢复探针连续失败，等待事前确认；${state.reason}`.slice(0, 1000),
    nextProbeAtMs,
    probePresentation: runtimeProbeScheduledPresentation(state.probePresentation.lastObservation, nextProbeAtMs),
    probeRunId: undefined,
    probeRunUntilMs: undefined,
    halfOpenLeaseId: undefined,
    halfOpenLeaseUntilMs: undefined
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
  const generation = initialState.generation
  const runtimeKey = initialState.runtimeKey
  const timeoutMs = accountDiagnosticRetryTimeoutMs[initialState.attemptCount] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
  const runId = distributedProbeRunId(runtimeKey, generation)
  const claimed = await distributedRecoveryProbeStore.acquireGenerationRun(
    runtimeKey,
    generation,
    runId,
    Date.now() + timeoutMs + 15_000,
    distributedRecoveryProbeStateTtlMs
  )
  if (!claimed) return
  let state: DistributedRecoveryProbeState = claimed
  try {
    if (state.attemptCount < precheckMaxAttempts) {
      const attempt = state.attemptCount
      state = {
        ...state,
        nextProbeAtMs: Date.now() + precheckSuppressionMs(),
        probePresentation: { lastObservation: state.probePresentation.lastObservation, schedule: { state: 'running' } }
      }
      const result = await runWithGatewayAutomaticProbeSlot(() => runSingleGatewayAccountPrecheck(distributedStateWithAccount(state, account), timeoutMs))
      if (result.transportOutcome.kind === 'unknown') {
        const nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
        await commitDistributedRecoveryProbeRun({
          ...state,
          attemptCount: attempt,
          nextProbeAtMs,
          probePresentation: runtimeProbeScheduledPresentation(state.probePresentation.lastObservation, nextProbeAtMs)
        }, runId)
        return
      }
      if (result.transportOutcome.kind === 'framing_complete') {
        await clearDistributedRecoveryProbeRun(runtimeKey, generation, runId)
        return
      }
      state = {
        ...state,
        attemptCount: attempt + 1,
        reason: accountPrecheckFailureReason(result),
        probePresentation: {
          lastObservation: runtimeProbeObservation(runtimeKey, generation, state.attemptCount, result),
          schedule: { state: 'none' }
        }
      }
      const nextProbeAtMs = nextAccountPrecheckProbeAtMs({
        attemptCount: state.attemptCount,
        maxAttempts: precheckMaxAttempts,
        startedAtMs: state.startedAtMs,
        nowMs: Date.now()
      })
      if (nextProbeAtMs !== undefined) {
        state = {
          ...state,
          nextProbeAtMs,
          probePresentation: runtimeProbeScheduledPresentation(state.probePresentation.lastObservation, nextProbeAtMs)
        }
        await commitDistributedRecoveryProbeRun(state, runId)
        return
      }
    }

    const confirmationAtMs = nextAccountPrecheckProbeAtMs({
      attemptCount: state.attemptCount,
      maxAttempts: precheckMaxAttempts,
      startedAtMs: state.startedAtMs,
      nowMs: Date.now()
    })
    if (confirmationAtMs !== undefined) {
      await commitDistributedRecoveryProbeRun({
        ...state,
        nextProbeAtMs: confirmationAtMs,
        probePresentation: runtimeProbeScheduledPresentation(state.probePresentation.lastObservation, confirmationAtMs)
      }, runId)
      return
    }
    const accountConcurrencyAccountId = gatewayAccountConcurrencyAccountId(account)
    const currentConcurrency = await getAccountCurrentConcurrencyAsync(accountConcurrencyAccountId)
    if (currentConcurrency > 0) {
      const reason = `事前确认探针连续失败，等待 ${currentConcurrency} 个在途请求结束后再标记临时不可调用；${state.reason}`.slice(0, 1000)
      await commitDistributedRecoveryProbeRun({
        ...state,
        reason,
        nextProbeAtMs: Date.now() + precheckConcurrencyDrainPollMs,
        probePresentation: { lastObservation: state.probePresentation.lastObservation, schedule: { state: 'none' } }
      }, runId)
      return
    }

    const reason = `事前确认探针连续失败 ${state.attemptCount} 次，已标记为临时不可调用；${state.reason}`.slice(0, 1000)
    const markResult = await requestGatewayDbService({
      type: 'mark_account_precheck_temporary_unavailable',
      account,
      reason,
      precheckStartedAt: new Date(state.startedAtMs).toISOString()
    }, { priority: 'low' })
    await clearDistributedRecoveryProbeRun(runtimeKey, generation, runId)
    if (markResult.updated) clearGatewayRuntimeCache()
  } catch (error) {
    const nextProbeAtMs = Date.now() + recoveryProbeRetryDelayMs
    await commitDistributedRecoveryProbeRun({
      ...state,
      nextProbeAtMs,
      probePresentation: runtimeProbeScheduledPresentation(state.probePresentation.lastObservation, nextProbeAtMs)
    }, runId).catch(() => undefined)
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_distributed_precheck_exception',
      runtimeKey,
      generation
    }), 'Redis 运行态账号事前确认探针执行失败，已等待下一轮')
  }
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
    running: false,
    probePresentation: {
      lastObservation: state.probePresentation.lastObservation,
      schedule: { state: 'none' }
    }
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
    if (current) {
      scheduleDistributedRecoveryProbeSweep(Math.max(0, current.nextProbeAtMs - Date.now()))
    }
    return false
  }
  scheduleDistributedRecoveryProbeSweep(Math.max(0, state.nextProbeAtMs - Date.now()))
  return true
}

async function commitDistributedRecoveryProbeRun(
  state: DistributedRecoveryProbeState,
  runId: string
): Promise<boolean> {
  const committed = await distributedRecoveryProbeStore.commitGenerationRun(state, runId, distributedRecoveryProbeStateTtlMs)
  if (!committed) return false
  scheduleDistributedRecoveryProbeSweep(Math.max(0, state.nextProbeAtMs - Date.now()))
  notifyOneRecoverableUnavailableRuntimeWaiter(state.runtimeKey)
  clearGatewayRuntimeCache()
  return true
}

async function clearDistributedRecoveryProbeRun(
  runtimeKey: string,
  generation: number,
  runId: string
): Promise<boolean> {
  const cleared = await distributedRecoveryProbeStore.deleteGenerationRun(runtimeKey, generation, runId)
  if (!cleared) return false
  notifyOneRecoverableUnavailableRuntimeWaiter(runtimeKey)
  clearGatewayRuntimeCache()
  return true
}

function distributedProbeRunId(runtimeKey: string, generation: number): string {
  return `${process.pid}:${generation}:${Date.now()}:${createHash('sha256').update(`${runtimeKey}:${Math.random()}`).digest('base64url').slice(0, 12)}`
}

async function clearDistributedRecoveryProbeState(runtimeKey: string): Promise<void> {
  await distributedRecoveryProbeStore.delete(runtimeKey)
  notifyOneRecoverableUnavailableRuntimeWaiter(runtimeKey)
  clearGatewayRuntimeCache()
}

async function clearDistributedRecoveryProbeStateGeneration(runtimeKey: string, generation: number): Promise<boolean> {
  const cleared = await distributedRecoveryProbeStore.deleteGeneration(runtimeKey, generation)
  if (cleared) {
    notifyOneRecoverableUnavailableRuntimeWaiter(runtimeKey)
    clearGatewayRuntimeCache()
    return true
  }
  const current = await distributedRecoveryProbeStore.get(runtimeKey).catch(() => undefined)
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
    precheckRequested: state.precheckRequested,
    probePresentation: state.probePresentation ?? runtimeProbeScheduledPresentation(undefined, state.nextProbeAtMs)
  }
}

function runtimeProbeObservationMarker(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  if (!normalized) return undefined
  return createHash('sha256').update(normalized).digest('base64url').slice(0, 32)
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

function scheduleGatewayAccountPrecheckRun(runtimeKey: string, delayMs: number, visibleProbe = true): void {
  const existing = precheckRunTimers.get(runtimeKey)
  if (existing) {
    clearTimeout(existing)
  }
  if (visibleProbe) {
    const state = precheckStates.get(runtimeKey)
    if (state) {
      state.probePresentation = runtimeProbeScheduledPresentation(
        state.probePresentation?.lastObservation,
        Date.now() + Math.max(0, delayMs)
      )
    }
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
  cleanupExpiredConfiguredPolicyAvoidancesMemory()
  const snapshot = snapshotLocalAccountRuntimeAvailability(isPrecheckRuntimeBlocking)
  for (const [runtimeKey, state] of recoveryProbeStates) {
    if (!snapshot[runtimeKey]) continue
    snapshot[runtimeKey] = {
      ...snapshot[runtimeKey],
      probePresentation: visibleRuntimeProbePresentation(state.probePresentation, {
        taskScheduled: recoveryProbeTimers.has(runtimeKey),
        running: state.running
      })
    }
  }
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
      precheckAttemptCount: state.attemptCount,
      probePresentation: visibleRuntimeProbePresentation(state.probePresentation, {
        taskScheduled: precheckRunTimers.has(runtimeKey),
        running: state.running || Boolean(state.halfOpenLeaseId && (state.halfOpenLeaseUntilMs ?? 0) > Date.now())
      })
    }
  }
  for (const [runtimeKey, state] of configuredPolicyAvoidancesMemory) {
    snapshot[runtimeKey] = configuredPolicyAvoidanceAvailability(state)
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
  const configuredPolicyResult = await filterConfiguredPolicyAvoidances(accounts)
  if (configuredPolicyResult.accounts.length === 0) {
    return configuredPolicyResult
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    const distributedPrecheckResult = await filterDistributedPrecheckSuppressions(configuredPolicyResult.accounts, options)
    const nextRetryAtMs = earliestTime(configuredPolicyResult.nextRetryAtMs, distributedPrecheckResult.nextRetryAtMs)
    return {
      ...distributedPrecheckResult,
      suppressedCount: configuredPolicyResult.suppressedCount + distributedPrecheckResult.suppressedCount,
      allSuppressed: distributedPrecheckResult.accounts.length === 0 && accounts.length > 0,
      suppressedAccountIds: [...configuredPolicyResult.suppressedAccountIds, ...distributedPrecheckResult.suppressedAccountIds],
      precheckSuppressedAccountIds: distributedPrecheckResult.precheckSuppressedAccountIds,
      configuredPolicySuppressedAccountIds: configuredPolicyResult.configuredPolicySuppressedAccountIds,
      nextRetryAtMs,
      nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - Date.now())
    }
  }
  const localResult = filterLocallySuppressedGatewayAccounts(configuredPolicyResult.accounts, options)
  if (options.acquirePrecheckHalfOpenLease && localResult.allSuppressed) {
    const groupLease = acquirePrecheckHalfOpenGroupLease(options.precheckHalfOpenGroupKey)
    const precheckLease = groupLease ? acquireMemoryPrecheckHalfOpenLease(configuredPolicyResult.accounts) : undefined
    if (groupLease && !precheckLease) releasePrecheckHalfOpenGroupLease(groupLease)
    if (precheckLease) {
      precheckLease.lease = withPrecheckHalfOpenGroupLease(precheckLease.lease, groupLease!)
      return {
        accounts: [precheckLease.account],
        suppressedCount: configuredPolicyResult.suppressedCount + Math.max(0, localResult.suppressedCount - 1),
        allSuppressed: false,
        suppressedAccountIds: [
          ...configuredPolicyResult.suppressedAccountIds,
          ...localResult.suppressedAccountIds.filter((accountId) => accountId !== precheckLease.account.id)
        ],
        acquiredHalfOpenLeases: [precheckLease.lease],
        precheckSuppressedAccountIds: localResult.suppressedAccountIds.filter((accountId) => accountId !== precheckLease.account.id),
        configuredPolicySuppressedAccountIds: configuredPolicyResult.configuredPolicySuppressedAccountIds,
        nextRetryAtMs: localResult.nextRetryAtMs,
        nextRetryAfterMs: localResult.nextRetryAfterMs
      }
    }
  }
  const nextRetryAtMs = earliestTime(configuredPolicyResult.nextRetryAtMs, localResult.nextRetryAtMs)
  return {
    ...localResult,
    suppressedCount: configuredPolicyResult.suppressedCount + localResult.suppressedCount,
    allSuppressed: localResult.accounts.length === 0 && accounts.length > 0,
    suppressedAccountIds: [...configuredPolicyResult.suppressedAccountIds, ...localResult.suppressedAccountIds],
    precheckSuppressedAccountIds: configuredPolicyResult.accounts
      .filter((account) => precheckStates.has(gatewayAccountRuntimeKey(account)))
      .map((account) => account.id),
    precheckSuppressedRuntimeScopes: configuredPolicyResult.accounts.flatMap((account) => {
      const runtimeKey = gatewayAccountRuntimeKey(account)
      const state = precheckStates.get(runtimeKey)
      return state ? [{ runtimeKey, generation: state.generation }] : []
    }),
    configuredPolicySuppressedAccountIds: configuredPolicyResult.configuredPolicySuppressedAccountIds,
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - Date.now())
  }
}

async function filterDistributedPrecheckSuppressions<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountSuppressionFilterOptions = {}
): Promise<LocalAccountSuppressionFilterResult<T>> {
  const now = Date.now()
  const runtimeKeys = accounts.map((account) => gatewayAccountRuntimeKey(account))
  const states = new Map<string, DistributedRecoveryProbeState>()
  for (let index = 0; index < runtimeKeys.length; index += 100) {
    const batch = await distributedRecoveryProbeStore.getMany(runtimeKeys.slice(index, index + 100))
    for (const [runtimeKey, state] of batch) states.set(runtimeKey, state)
  }
  const blocked = accounts.flatMap((account, index) => {
    const state = states.get(runtimeKeys[index]!)
    return state?.phase === 'precheck_pending' ? [{ account, state }] : []
  })
  const acquiredHalfOpenLeases: GatewayAccountHalfOpenLease[] = []
  const groupAcquisition = options.acquirePrecheckHalfOpenLease
    ? await acquireWithPrecheckHalfOpenGroupGate(
        options.precheckHalfOpenGroupKey,
        () => acquireDistributedPrecheckHalfOpenLease(blocked)
      )
    : undefined
  const halfOpenAccount = groupAcquisition?.value
  if (halfOpenAccount && groupAcquisition) {
    halfOpenAccount.lease = withPrecheckHalfOpenGroupLease(halfOpenAccount.lease, groupAcquisition.groupLease)
  }
  if (halfOpenAccount) acquiredHalfOpenLeases.push(halfOpenAccount.lease)
  const blockedRuntimeKeys = new Set(blocked
    .filter((item) => item.state.runtimeKey !== halfOpenAccount?.state.runtimeKey)
    .map((item) => item.state.runtimeKey))
  const visibleAccounts = accounts.filter((_account, index) => !blockedRuntimeKeys.has(runtimeKeys[index]!))
  const remainingBlocked = blocked.filter((item) => item.state.runtimeKey !== halfOpenAccount?.state.runtimeKey)
  const nextRetryAtMs = remainingBlocked
    .map((item) => item.state.nextProbeAtMs)
    .reduce<number | undefined>((earliest, value) => {
      const retryAtMs = value <= now ? now + distributedRecoveryProbeDueRetryDelayMs : value
      return earliest === undefined ? retryAtMs : Math.min(earliest, retryAtMs)
    }, undefined)
  return {
    accounts: visibleAccounts,
    suppressedCount: remainingBlocked.length,
    allSuppressed: visibleAccounts.length === 0 && accounts.length > 0,
    suppressedAccountIds: remainingBlocked.map((item) => item.account.id),
    acquiredHalfOpenLeases,
    precheckSuppressedAccountIds: remainingBlocked.map((item) => item.account.id),
    precheckSuppressedRuntimeScopes: remainingBlocked.map((item) => ({
      runtimeKey: item.state.runtimeKey,
      generation: item.state.generation
    })),
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - now)
  }
}

const precheckHalfOpenLeaseMs = 180_000
const precheckHalfOpenGroupLeases = new Map<string, { leaseId: string; untilMs: number }>()

function acquirePrecheckHalfOpenGroupLease(groupKey: string | undefined): { groupKey: string; leaseId: string } | undefined {
  const normalized = groupKey?.trim()
  if (!normalized) return undefined
  const now = Date.now()
  const current = precheckHalfOpenGroupLeases.get(normalized)
  if (current && current.untilMs > now) return undefined
  const leaseId = `${process.pid}:${now}:${Math.random().toString(16).slice(2)}`
  precheckHalfOpenGroupLeases.set(normalized, { leaseId, untilMs: now + precheckHalfOpenLeaseMs })
  return { groupKey: normalized, leaseId }
}

function releasePrecheckHalfOpenGroupLease(lease: { groupKey: string; leaseId: string }): boolean {
  if (precheckHalfOpenGroupLeases.get(lease.groupKey)?.leaseId !== lease.leaseId) return false
  return precheckHalfOpenGroupLeases.delete(lease.groupKey)
}

async function acquireWithPrecheckHalfOpenGroupGate<T>(
  groupKey: string | undefined,
  acquire: () => Promise<T | undefined>
): Promise<{ value: T; groupLease: { groupKey: string; leaseId: string } } | undefined> {
  const groupLease = acquirePrecheckHalfOpenGroupLease(groupKey)
  if (!groupLease) return undefined
  let transferred = false
  try {
    const value = await acquire()
    if (value === undefined) return undefined
    transferred = true
    return { value, groupLease }
  } finally {
    if (!transferred) releasePrecheckHalfOpenGroupLease(groupLease)
  }
}

export async function acquirePrecheckHalfOpenGroupGateForTest<T>(
  groupKey: string,
  acquire: () => Promise<T>
): Promise<T | undefined> {
  const acquired = await acquireWithPrecheckHalfOpenGroupGate(groupKey, acquire)
  if (!acquired) return undefined
  try {
    return acquired.value
  } finally {
    releasePrecheckHalfOpenGroupLease(acquired.groupLease)
  }
}

export function precheckHalfOpenGroupLeaseCountForTest(): number {
  return precheckHalfOpenGroupLeases.size
}

export { recoverableUnavailableWaitCoordinatorSnapshotForTest } from './recoverable-unavailable-wait.js'

function withPrecheckHalfOpenGroupLease(
  accountLease: GatewayAccountHalfOpenLease,
  groupLease: { groupKey: string; leaseId: string }
): GatewayAccountHalfOpenLease {
  return {
    ...accountLease,
    release: async () => {
      try {
        return await accountLease.release()
      } finally {
        releasePrecheckHalfOpenGroupLease(groupLease)
      }
    },
    completeSuccess: accountLease.completeSuccess
      ? async () => {
          try {
            return await accountLease.completeSuccess!()
          } finally {
            releasePrecheckHalfOpenGroupLease(groupLease)
          }
        }
      : undefined
  }
}

function acquireMemoryPrecheckHalfOpenLease<T extends SuppressibleGatewayAccount>(
  accounts: T[]
): { account: T; lease: GatewayAccountHalfOpenLease } | undefined {
  const now = Date.now()
  for (const account of accounts) {
    const runtimeKey = gatewayAccountRuntimeKey(account)
    const state = precheckStates.get(runtimeKey)
    if (!state) continue
    if (state.halfOpenLeaseId && (state.halfOpenLeaseUntilMs ?? 0) > now) continue
    const leaseId = `${process.pid}:${now}:${Math.random().toString(16).slice(2)}`
    const generation = state.generation
    state.halfOpenLeaseId = leaseId
    state.halfOpenLeaseUntilMs = now + precheckHalfOpenLeaseMs
    scheduleGatewayAccountPrecheckRun(runtimeKey, precheckHalfOpenLeaseMs, false)
    return {
      account,
      lease: {
        runtimeKey,
        accountId: account.id,
        generation,
        leaseId,
        release: async () => releaseMemoryPrecheckHalfOpenLease(runtimeKey, generation, leaseId),
        completeSuccess: async () => completeMemoryPrecheckHalfOpenSuccess(runtimeKey, generation, leaseId)
      }
    }
  }
  return undefined
}

async function acquireDistributedPrecheckHalfOpenLease<T extends SuppressibleGatewayAccount>(
  blocked: Array<{ account: T; state: DistributedRecoveryProbeState }>
): Promise<{ account: T; state: DistributedRecoveryProbeState; lease: GatewayAccountHalfOpenLease } | undefined> {
  const now = Date.now()
  for (const item of blocked) {
    const leaseId = `${process.pid}:${now}:${Math.random().toString(16).slice(2)}`
    const leased = await distributedRecoveryProbeStore.acquireGenerationLease(
      item.state.runtimeKey,
      item.state.generation,
      leaseId,
      now + precheckHalfOpenLeaseMs,
      distributedRecoveryProbeStateTtlMs
    )
    if (!leased) continue
    return {
      account: item.account,
      state: leased,
      lease: {
        runtimeKey: leased.runtimeKey,
        accountId: item.account.id,
        generation: leased.generation,
        leaseId,
        release: () => distributedRecoveryProbeStore.releaseGenerationLease(leased.runtimeKey, leased.generation, leaseId, distributedRecoveryProbeStateTtlMs),
        completeSuccess: () => completeDistributedPrecheckHalfOpenSuccess(leased.runtimeKey, leased.generation, leaseId)
      }
    }
  }
  return undefined
}

async function completeDistributedPrecheckHalfOpenSuccess(runtimeKey: string, generation: number, leaseId: string): Promise<boolean> {
  const completed = await distributedRecoveryProbeStore.completeGenerationLease(runtimeKey, generation, leaseId)
  if (completed) clearGatewayRuntimeCache()
  return completed
}

function releaseMemoryPrecheckHalfOpenLease(runtimeKey: string, generation: number, leaseId: string): boolean {
  const state = precheckStates.get(runtimeKey)
  if (!state || state.generation !== generation || state.halfOpenLeaseId !== leaseId) return false
  state.halfOpenLeaseId = undefined
  state.halfOpenLeaseUntilMs = undefined
  scheduleGatewayAccountPrecheckRun(runtimeKey, 0)
  return true
}

function completeMemoryPrecheckHalfOpenSuccess(runtimeKey: string, generation: number, leaseId: string): boolean {
  const state = precheckStates.get(runtimeKey)
  if (!state || state.generation !== generation || state.halfOpenLeaseId !== leaseId) return false
  clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
  return true
}

async function filterConfiguredPolicyAvoidances<T extends SuppressibleGatewayAccount>(
  accounts: T[]
): Promise<LocalAccountSuppressionFilterResult<T>> {
  const now = Date.now()
  const runtimeKeys = accounts.map((account) => gatewayAccountRuntimeKey(account))
  const avoidanceStates = await loadConfiguredPolicyAvoidanceStates(runtimeKeys, now)
  const states = accounts.map((account, index) => ({ account, runtimeKey: runtimeKeys[index], state: avoidanceStates[index] }))
  const suppressed = states.filter((item) => item.state !== undefined)
  const suppressedRuntimeKeys = new Set(suppressed.map((item) => item.runtimeKey))
  const visibleAccounts = states
    .filter((item) => !suppressedRuntimeKeys.has(item.runtimeKey))
    .map((item) => item.account)
  const nextRetryAtMs = suppressed
    .map((item) => item.state?.untilMs ?? now + distributedRecoveryProbeDueRetryDelayMs)
    .reduce<number | undefined>((earliest, value) => {
      const retryAtMs = value <= now ? now + distributedRecoveryProbeDueRetryDelayMs : value
      return earliest === undefined ? retryAtMs : Math.min(earliest, retryAtMs)
    }, undefined)
  return {
    accounts: visibleAccounts,
    suppressedCount: suppressed.length,
    allSuppressed: visibleAccounts.length === 0 && accounts.length > 0,
    suppressedAccountIds: suppressed.map((item) => item.account.id),
    acquiredHalfOpenLeases: [],
    configuredPolicySuppressedAccountIds: suppressed.map((item) => item.account.id),
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - now)
  }
}

function earliestTime(first: number | undefined, second: number | undefined): number | undefined {
  if (first === undefined) return second
  if (second === undefined) return first
  return Math.min(first, second)
}

function configuredPolicyAvoidanceAvailability(state: ConfiguredPolicyAvoidanceState): AccountRuntimeAvailability {
  const recoveryAt = new Date(state.untilMs).toISOString()
  return {
    status: 'local_suppressed',
    reason: state.reason,
    since: new Date(state.startedAtMs).toISOString(),
    until: recoveryAt,
    probePresentation: {
      schedule: { state: 'none' },
      recoveryAt,
      recoveryAtKind: 'policy_ttl_expiry'
    }
  }
}

function cleanupExpiredConfiguredPolicyAvoidancesMemory(now = Date.now()): void {
  for (const [runtimeKey, state] of configuredPolicyAvoidancesMemory) {
    if (state.untilMs <= now) configuredPolicyAvoidancesMemory.delete(runtimeKey)
  }
}

async function loadConfiguredPolicyAvoidanceStates(
  runtimeKeys: string[],
  now = Date.now()
): Promise<Array<ConfiguredPolicyAvoidanceState | undefined>> {
  const states = new Array<ConfiguredPolicyAvoidanceState | undefined>(runtimeKeys.length)
  const missedIndexes: number[] = []
  const missedRuntimeKeys: string[] = []
  runtimeKeys.forEach((runtimeKey, index) => {
    const cached = cachedConfiguredPolicyAvoidanceState(runtimeKey, now)
    if (cached.hit) {
      states[index] = cached.state
      return
    }
    missedIndexes.push(index)
    missedRuntimeKeys.push(runtimeKey)
  })
  if (missedRuntimeKeys.length === 0) return states
  const loaded = await configuredPolicyAvoidanceStore.getJsonMany<ConfiguredPolicyAvoidanceState>(missedRuntimeKeys)
  loaded.forEach((state, missIndex) => {
    const runtimeKey = missedRuntimeKeys[missIndex]
    const outputIndex = missedIndexes[missIndex]
    states[outputIndex] = state
    rememberConfiguredPolicyAvoidanceState(runtimeKey, state, now)
  })
  return states
}

function cachedConfiguredPolicyAvoidanceState(
  runtimeKey: string,
  now: number
): { hit: boolean; state?: ConfiguredPolicyAvoidanceState } {
  const entry = configuredPolicyAvoidanceCache.get(runtimeKey)
  if (!entry) return { hit: false }
  if (entry.expiresAtMs <= now) {
    configuredPolicyAvoidanceCache.delete(runtimeKey)
    return { hit: false }
  }
  return { hit: true, state: entry.state }
}

function rememberConfiguredPolicyAvoidanceState(
  runtimeKey: string,
  state: ConfiguredPolicyAvoidanceState | undefined,
  now = Date.now()
): void {
  const cacheTtlMs = state ? configuredPolicyAvoidanceCacheTtlMs : configuredPolicyAvoidanceNegativeCacheTtlMs
  configuredPolicyAvoidanceCache.set(runtimeKey, {
    state,
    expiresAtMs: Math.min(now + cacheTtlMs, state?.untilMs ?? Number.POSITIVE_INFINITY)
  })
  for (const [key, entry] of configuredPolicyAvoidanceCache) {
    if (entry.expiresAtMs <= now || configuredPolicyAvoidanceCache.size > configuredPolicyAvoidanceCacheMaxEntries) {
      configuredPolicyAvoidanceCache.delete(key)
    }
    if (configuredPolicyAvoidanceCache.size <= configuredPolicyAvoidanceCacheMaxEntries) break
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
    recoveryProbePendingAccountCount: runtimeConfig.runtimeStateDriver === 'redis' ? 0 : recoveryProbeStates.size,
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
  configuredPolicyAvoidancesMemory.clear()
  configuredPolicyAvoidanceCache.clear()
  runtimeProbeGenerationCounters.clear()
  recoveryProbeLastStartedAtByScope.clear()
  precheckHalfOpenGroupLeases.clear()
  runningRecoveryProbeCount = 0
}

export function clearGatewayAutomaticAccountRuntimeAvailability(
  account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string
): GatewayAccountRuntimeClearResult {
  const clearedKeys: string[] = []
  for (const runtimeKey of gatewayAccountRuntimeClearKeys(account)) {
    if (runtimeConfig.runtimeStateDriver === 'redis') {
      void clearDistributedRecoveryProbeState(runtimeKey).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'gateway_account_automatic_runtime_availability_clear_failed',
          runtimeKey
        }), 'Redis 自动探针账号运行态清理失败')
      })
      clearedKeys.push(runtimeKey)
    } else if (clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)) {
      clearedKeys.push(runtimeKey)
    }
  }
  if (clearedKeys.length > 0) clearGatewayRuntimeCache()
  return { cleared: clearedKeys.length > 0, clearedKeys }
}

export function clearGatewayAccountRuntimeAvailability(
  account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string
): GatewayAccountRuntimeClearResult {
  const clearedKeys: string[] = []
  for (const runtimeKey of gatewayAccountRuntimeClearKeys(account)) {
    if (runtimeConfig.runtimeStateDriver === 'redis') {
      void Promise.all([
        clearDistributedRecoveryProbeState(runtimeKey),
        configuredPolicyAvoidanceStore.delete(runtimeKey)
      ]).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'gateway_account_distributed_runtime_availability_clear_failed',
          runtimeKey
        }), 'Redis 运行态账号恢复状态清理失败')
      })
      rememberConfiguredPolicyAvoidanceState(runtimeKey, undefined)
      clearedKeys.push(runtimeKey)
    } else {
      const configuredPolicyCleared = configuredPolicyAvoidancesMemory.delete(runtimeKey)
      rememberConfiguredPolicyAvoidanceState(runtimeKey, undefined)
      void configuredPolicyAvoidanceStore.delete(runtimeKey).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'gateway_account_configured_policy_avoidance_clear_failed',
          runtimeKey
        }), '用户显式策略账号运行态避让清理失败')
      })
      if (clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey) || configuredPolicyCleared) {
        clearedKeys.push(runtimeKey)
      }
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
  const result = await requestGatewayDbService(operation, { priority: 'low' })
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
  if (cleared) notifyOneRecoverableUnavailableRuntimeWaiter(accountId)
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
  return precheckStates.has(runtimeKey)
}

function precheckPolicyStartedAtMs(state: PrecheckState): number {
  return state.minimumObservationCompletedForTest
    ? Date.now() - accountPrecheckMinimumObservationMs
    : state.startedAtMs
}

function precheckSuppressionMs(): number {
  return Math.min(precheckSuppressionGuardMs, localSuppressionMaxMs)
}

function operationAccountId(operation: AccountSideEffectOperation): string {
  return operation.account.id
}

function gatewayRuntimeConcurrencyAccountId(account: SuppressibleGatewayAccount | string): string {
  return typeof account === 'string' ? account : gatewayAccountConcurrencyAccountId(account)
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
  const accountConcurrencyAccountId = gatewayAccountConcurrencyAccountId(state.account)
  const currentConcurrency = getAccountCurrentConcurrency(accountConcurrencyAccountId)
  if (currentConcurrency <= 0) {
    return false
  }
  const reason = `事前确认探针连续失败，等待 ${currentConcurrency} 个在途请求结束后再标记临时不可调用；${state.reason}`.slice(0, 1000)
  state.reason = reason
  state.running = false
  state.waitingForConcurrencyDrain = true
  schedulePrecheckAfterConcurrencyDrain(runtimeKey, accountConcurrencyAccountId)
  logger.warn({
    event: 'gateway_account_precheck_mark_deferred_for_concurrency',
    accountId: state.account.id,
    accountConcurrencyAccountId,
    accountName: state.account.name,
    runtimeKey,
    currentConcurrency
  }, '账号事前确认探针连续失败，但仍有在途并发，已延后写入临时不可调用')
  return true
}

function schedulePrecheckAfterConcurrencyDrain(runtimeKey: string, accountConcurrencyAccountId: string): void {
  if (precheckConcurrencyDrainWaits.has(runtimeKey)) {
    return
  }

  const tryResume = (): void => {
    const state = precheckStates.get(runtimeKey)
    if (!state) {
      clearPrecheckConcurrencyDrainWait(runtimeKey)
      return
    }
    if (getAccountCurrentConcurrency(accountConcurrencyAccountId) > 0) {
      return
    }
    clearPrecheckConcurrencyDrainWait(runtimeKey)
    state.waitingForConcurrencyDrain = false
    void runGatewayAccountPrecheck(runtimeKey)
  }

  const unsubscribe = subscribeAccountConcurrencyRelease((event) => {
    if (event.accountId === accountConcurrencyAccountId) {
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
  if (state.halfOpenLeaseId && (state.halfOpenLeaseUntilMs ?? 0) > Date.now()) {
    scheduleGatewayAccountPrecheckRun(runtimeKey, (state.halfOpenLeaseUntilMs ?? Date.now()) - Date.now(), false)
    return
  }
  state.halfOpenLeaseId = undefined
  state.halfOpenLeaseUntilMs = undefined
  const generation = state.generation
  state.running = true
  state.probePresentation = {
    lastObservation: state.probePresentation?.lastObservation,
    schedule: { state: 'running' }
  }
  try {
    if (state.attemptCount < precheckMaxAttempts) {
      const attempt = state.attemptCount
      const latestState = currentPrecheckState(runtimeKey, generation)
      if (!latestState) {
        logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_attempt_ignored')
        return
      }
      latestState.lastAttemptAtMs = Date.now()
      const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
      const result = await runWithGatewayAutomaticProbeSlot(() => runSingleGatewayAccountPrecheck(latestState, timeoutMs))
      const stateAfterResult = currentPrecheckState(runtimeKey, generation)
      if (!stateAfterResult) {
        logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_result_ignored')
        return
      }
      stateAfterResult.running = false
      if (result.transportOutcome.kind === 'unknown') {
        stateAfterResult.probePresentation = runtimeProbeScheduledPresentation(
          stateAfterResult.probePresentation?.lastObservation,
          Date.now() + recoveryProbeRetryDelayMs
        )
        scheduleGatewayAccountPrecheckRun(runtimeKey, recoveryProbeRetryDelayMs)
        logger.info({
          event: 'gateway_account_precheck_inconclusive_rescheduled',
          accountId: latestState.account.id,
          accountName: latestState.account.name,
          runtimeKey,
          generation,
          failureKind: result.transportOutcome.failureKind
        }, '账号事前确认结论未知，已保留状态并有界重排')
        return
      }
      if (result.transportOutcome.kind === 'framing_complete') {
        clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
        logger.info({
          event: 'gateway_account_precheck_recovered',
          accountId: stateAfterResult.account.id,
          accountName: stateAfterResult.account.name,
          runtimeKey,
          generation,
          attemptCount: attempt + 1,
          statusCode: result.statusCode,
          durationMs: result.durationMs
        }, '账号事前确认探针通过，已清理运行态短避让')
        return
      }
      stateAfterResult.attemptCount = attempt + 1
      stateAfterResult.reason = accountPrecheckFailureReason(result)
      stateAfterResult.probePresentation = {
        lastObservation: runtimeProbeObservation(runtimeKey, generation, stateAfterResult.attemptCount, result),
        schedule: { state: 'none' }
      }
      const nextProbeAtMs = nextAccountPrecheckProbeAtMs({
        attemptCount: stateAfterResult.attemptCount,
        maxAttempts: precheckMaxAttempts,
        startedAtMs: precheckPolicyStartedAtMs(stateAfterResult),
        nowMs: Date.now()
      })
      if (nextProbeAtMs !== undefined) {
        scheduleGatewayAccountPrecheckRun(runtimeKey, Math.max(0, nextProbeAtMs - Date.now()))
        return
      }
    }

    const finalState = currentPrecheckState(runtimeKey, generation)
    if (!finalState) {
      logStalePrecheckResult(runtimeKey, generation, 'gateway_account_precheck_stale_final_ignored')
      return
    }
    const confirmationAtMs = nextAccountPrecheckProbeAtMs({
      attemptCount: finalState.attemptCount,
      maxAttempts: precheckMaxAttempts,
      startedAtMs: precheckPolicyStartedAtMs(finalState),
      nowMs: Date.now()
    })
    if (confirmationAtMs !== undefined) {
      finalState.running = false
      scheduleGatewayAccountPrecheckRun(runtimeKey, Math.max(0, confirmationAtMs - Date.now()))
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
    }, {
      priority: 'low'
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
      stateAfterError.probePresentation = {
        lastObservation: stateAfterError.probePresentation?.lastObservation,
        schedule: { state: 'none' }
      }
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
  return false
}

async function runWithGatewayAutomaticProbeSlot<T>(task: () => Promise<T>): Promise<T> {
  return await gatewayAutomaticProbeLimit(task)
}

export async function runWithGatewayAutomaticProbeSlotForTest<T>(task: () => Promise<T>): Promise<T> {
  return await runWithGatewayAutomaticProbeSlot(task)
}

async function runSingleGatewayAccountPrecheck(state: PrecheckState, timeoutMs: number): Promise<GatewayAutomaticProbeResult> {
  const attemptedAt = new Date().toISOString()
  const { testOpenAIAccount } = await import('../../accounts/account-test.service.js')
  const signal = AbortSignal.timeout(timeoutMs)
  const account = accountSummaryFromGatewayPrecheckAccount(state.account, {
    groupId: state.groupId,
    systemAccountId: state.systemAccountId
  })
  let upstreamAttempt: UpstreamAttempt | undefined
  const result = await testOpenAIAccount(account, {
    diagnostics: 'full',
    groupId: state.groupId,
    systemAccountId: state.systemAccountId,
    trafficSource: 'runtime_recovery_probe',
    testEndpointMode: account.healthCheckEndpointMode,
    signal,
    disableAccountStateMutation: true,
    onUpstreamAttempt: (attempt) => {
      upstreamAttempt = attempt
    },
    candidateAccount: state.account,
    findAccountForTest: (accountId, access) => requestGatewayDbService({
      type: 'find_account_for_test',
      accountId,
      access
    }, { timeoutMs: 10_000 }),
    findOpenAIAccountForGroup: (groupId, accountId, systemAccountId, options) => requestGatewayDbService({
      type: 'find_openai_account_for_group',
      groupId,
      accountId,
      systemAccountId,
      includeUnavailable: options?.includeUnavailable,
      ignoreAvailability: options?.ignoreAvailability
    }, { timeoutMs: 10_000 }),
    gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(state.settings, timeoutMs)
  })
  const transportOutcome = transportProbeOutcomeFromAccountTestResult(result, {
    upstreamAttempt,
    timeout: signal.aborted
  })
  const probeOutcome: GatewayAutomaticProbeResult['probeOutcome'] = transportOutcome.kind === 'framing_complete'
    ? 'complete_success'
    : transportOutcome.kind === 'transport_incomplete'
      ? 'upstream_failure'
      : 'probe_task_failure'
  return { ...result, attemptedAt, probeOutcome, transportOutcome }
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
