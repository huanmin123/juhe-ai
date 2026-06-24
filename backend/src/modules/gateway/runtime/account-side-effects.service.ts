import { errorLogFields, logger } from '../../../shared/logger.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import type { AccountRuntimeAvailability } from '../../db-service/db-service-types.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import type { AccountErrorPolicyDecision, GatewaySettings } from '../policy/account-error-policy.service.js'
import { exponentialRetryPolicy, retryDueAtMs, waitForRetryDelayMs } from '../../../shared/retry-policy.js'
import {
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
  clearLocalAccountSuppression,
  clearLocalAccountDegradation,
  clearLocalAccountSuppressionsForTest,
  cleanupExpiredLocalSuppressions,
  countLocalAccountDegradations,
  countVisibleLocalSuppressions,
  degradeLocalAccountForGatewayFailure,
  filterLocalAccountSuppressions,
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
  errorPolicyDecision?: AccountErrorPolicyDecision
  forcePrecheck?: boolean
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
  account: OpenAIAccountSecret
  settings?: GatewaySettings
  systemAccountId: string
  groupId: string
  startedAtMs: number
  lastAttemptAtMs?: number
  attemptCount: number
  failureCount: number
  reason: string
  errorPolicyDecision?: AccountErrorPolicyDecision
  distinctClientIpCount: number
  distinctApiKeyCount: number
  running: boolean
  waitingForConcurrencyDrain?: boolean
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
const failureStormWindowMs = 10_000
const failureStormThresholdCount = 5
const failureStormDistinctIpThreshold = 2
const failureStormMinObservationMs = 2_000
const failureStormRecentSuccessGraceMs = 5_000
const failureStormFailureRatioThreshold = 0.9
const precheckMinIntervalMs = 60_000
const precheckMaxAttempts = accountDiagnosticRetryTimeoutMs.length
const precheckSuppressionGuardMs = accountDiagnosticRetryMaxTotalTimeoutMs + 15_000
const precheckConcurrencyDrainPollMs = 1_000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)
const maxSideEffectQueueLength = 5000

const sideEffectQueue = new AccountSideEffectQueue()
const failureStorms = new Map<string, FailureStormEntry>()
const successObservations = new Map<string, SuccessObservationEntry>()
const precheckStates = new Map<string, PrecheckState>()
const precheckConcurrencyDrainWaits = new Map<string, { unsubscribe: () => void; timer: NodeJS.Timeout }>()
let processingSideEffects = false
let drainTimer: NodeJS.Timeout | undefined
let drainTimerDueAtMs: number | undefined
let enqueuedCount = 0
let completedCount = 0
let coalescedCount = 0
let canceledBySuccessCount = 0
let skippedHealthySuccessCount = 0
let failedAttemptCount = 0
let droppedCount = 0
let expiredCount = 0

export function enqueueGatewayAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): void {
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
  degradeLocalAccountForGatewayFailure(runtimeKey, accountId, reason)
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
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const now = Date.now()
  precheckStates.set(runtimeKey, {
    account,
    settings,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    startedAtMs: now,
    attemptCount: precheckMaxAttempts,
    failureCount: failureStormThresholdCount,
    reason: input.reason,
    errorPolicyDecision: input.errorPolicyDecision,
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
  const precheckDecision = shouldTriggerFailureStormPrecheck(runtimeKey, entry, forcePrecheck, now)
  if (!precheckDecision.trigger) {
    return
  }

  const existingPrecheck = precheckStates.get(runtimeKey)
  if (existingPrecheck && now - existingPrecheck.startedAtMs < precheckMinIntervalMs) {
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
    account,
    settings,
    systemAccountId: input.systemAccountId,
    groupId: input.groupId,
    startedAtMs: now,
    attemptCount: 0,
    failureCount: entry.failureCount,
    reason,
    errorPolicyDecision: input.errorPolicyDecision,
    distinctClientIpCount: entry.clientIps.size,
    distinctApiKeyCount: entry.apiKeyIds.size,
    running: false
  }
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
    void runGatewayAccountPrecheck(runtimeKey)
  }
}

function recordGatewayAccountSuccessObservation(runtimeKey: string): void {
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
    return { trigger: true, successCount, failureRatio }
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

export function getGatewayAccountSideEffectState(): GatewayAccountSideEffectState {
  cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
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
    precheckPendingAccountCount: precheckStates.size,
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
  accounts: T[]
): LocalAccountDegradationOrderResult<T> {
  return orderLocalAccountDegradations(accounts)
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
  metadata: Partial<Pick<LocalAccountSuppression, 'localFailureCount' | 'halfOpenLeaseUntilMs' | 'halfOpenLeaseId'>> = {}
): void {
  suppressLocalAccount(accountId, Math.max(0, Math.trunc(durationMs)), reason, status, {
    accountId,
    ...metadata
  })
}

export function clearGatewayLocalAccountSuppressionsForTest(): void {
  clearAllPrecheckConcurrencyDrainWaits()
  clearLocalAccountSuppressionsForTest()
  failureStorms.clear()
  successObservations.clear()
  precheckStates.clear()
}

export function clearGatewayAccountRuntimeAvailability(
  account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string
): GatewayAccountRuntimeClearResult {
  const clearedKeys: string[] = []
  for (const runtimeKey of gatewayAccountRuntimeClearKeys(account)) {
    if (clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)) {
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
  const result = await requestDbService(operation)
  if (result.changed) {
    clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    clearGatewayRuntimeCache()
  } else if (operation.input.success && result.accountStatus === 'active') {
    clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
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
  let cleared = false
  clearPrecheckConcurrencyDrainWait(accountId)
  cleared = clearLocalAccountSuppression(accountId) || cleared
  cleared = clearLocalAccountDegradation(accountId) || cleared
  cleared = failureStorms.delete(accountId) || cleared
  cleared = precheckStates.delete(accountId) || cleared
  return cleared
}

function cleanupExpiredFailureStorms(): void {
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

function deferPrecheckMarkUntilConcurrencyDrained(runtimeKey: string, state: PrecheckState): boolean {
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
  const state = precheckStates.get(runtimeKey)
  if (!state || state.running) {
    return
  }
  state.running = true
  try {
    for (let attempt = state.attemptCount; attempt < precheckMaxAttempts; attempt += 1) {
      const latestState = precheckStates.get(runtimeKey)
      if (!latestState) {
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
      if (result.success || result.accountFailureEligible === false) {
        clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey)
        logger.info({
          event: 'gateway_account_precheck_recovered',
          accountId: latestState.account.id,
          accountName: latestState.account.name,
          runtimeKey,
          attemptCount: latestState.attemptCount,
          statusCode: result.statusCode,
          durationMs: result.durationMs
        }, '账号事前确认探针通过，已清理运行态短避让')
        return
      }
      latestState.reason = accountPrecheckFailureReason(result)
    }

    const finalState = precheckStates.get(runtimeKey)
    if (!finalState) {
      return
    }
    if (deferPrecheckMarkUntilConcurrencyDrained(runtimeKey, finalState)) {
      return
    }
    const reason = `事前确认探针连续失败 ${finalState.attemptCount} 次，已标记为临时不可调用；${finalState.reason}`.slice(0, 1000)
    const markResult = await requestDbService({
      type: 'mark_account_precheck_temporary_unavailable',
      account: finalState.account,
      reason,
      precheckStartedAt: new Date(finalState.startedAtMs).toISOString(),
      errorPolicyDecision: finalState.errorPolicyDecision
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
      updated: markResult.updated,
      skippedReason: markResult.skippedReason,
      attemptCount: finalState.attemptCount
    }, '账号事前确认探针连续失败，已写入临时不可调用')
  } catch (error) {
    const stateAfterError = precheckStates.get(runtimeKey)
    if (stateAfterError) {
      stateAfterError.running = false
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_precheck_exception',
      runtimeKey
    }), '账号事前确认探针执行失败，保留运行态等待下一轮触发')
  }
}

async function runSingleGatewayAccountPrecheck(state: PrecheckState, timeoutMs: number): Promise<{
  success: boolean
  statusCode?: number
  errorCode?: string
  message?: string
  durationMs?: number
  accountFailureEligible?: boolean
}> {
  const { preferredSystemAccountTestModel, testOpenAIAccount } = await import('../../accounts/account-test.service.js')
  const signal = AbortSignal.timeout(timeoutMs)
  const account = accountSummaryFromGatewayPrecheckAccount(state.account, state)
  return await testOpenAIAccount(account, {
    model: preferredSystemAccountTestModel(account),
    diagnostics: 'full',
    groupId: state.groupId,
    trafficSource: 'cooldown_retest',
    signal,
    disableAccountStateMutation: true,
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
