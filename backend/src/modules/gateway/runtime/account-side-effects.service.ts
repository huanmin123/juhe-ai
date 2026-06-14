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
import type { AccountSummary } from '../../../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import {
  accountDiagnosticRetryMaxTotalTimeoutMs,
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride
} from '../../accounts/account-diagnostic-retry-policy.js'
import {
  gatewayAccountId,
  gatewayAccountRuntimeClearKeys,
  gatewayAccountRuntimeKey,
  runtimeAccountIdFromKey,
  type GatewayAccountRuntimeClearTarget,
  type SuppressibleGatewayAccount
} from './account-runtime-keys.js'
import {
  AccountSideEffectQueue,
  type AccountErrorHandlingOperation,
  type AccountSideEffectOperation,
  type StreamFailureOperation
} from './account-side-effect-queue.js'

interface LocalAccountSuppression {
  accountId: string
  untilMs: number
  reason: string
  sinceMs: number
  status: AccountRuntimeAvailability['status']
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
  localFailureCount?: number
  halfOpenLeaseUntilMs?: number
  halfOpenLeaseId?: string
}

export type { GatewayAccountRuntimeClearTarget, SuppressibleGatewayAccount } from './account-runtime-keys.js'

export interface GatewayAccountRuntimeClearResult {
  cleared: boolean
  clearedKeys: string[]
}

export interface GatewayAccountLocalSuppressionResult {
  runtimeKey: string
  action: 'suppressed' | 'precheck_required'
  reason: string
  localFailureCount: number
  delayMs?: number
  until?: string
}

export interface GatewayAccountHalfOpenLease {
  runtimeKey: string
  accountId: string
  leaseId: string
  release: () => boolean
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

export interface LocalAccountSuppressionFilterResult<T> {
  accounts: T[]
  suppressedCount: number
  allSuppressed: boolean
  suppressedAccountIds: string[]
  acquiredHalfOpenLeases: GatewayAccountHalfOpenLease[]
  nextRetryAtMs?: number
  nextRetryAfterMs?: number
}

interface LocalAccountSuppressionFilterOptions {
  acquireHalfOpenLease?: boolean
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
  precheckPendingAccountCount: number
  nextAttemptAt?: string
}

const sideEffectRetentionMs = 10 * 60_000
const localSuppressionMaxMs = 10 * 60_000
const localSuppressionDelayMs = [3_000, 5_000, 10_000] as const
const localSuppressionHalfOpenLeaseMs = 180_000
const localSuppressionIdleRetentionMs = 60_000
const failureStormWindowMs = 10_000
const failureStormThresholdCount = 5
const failureStormDistinctIpThreshold = 2
const precheckMinIntervalMs = 60_000
const precheckMaxAttempts = accountDiagnosticRetryTimeoutMs.length
const precheckSuppressionGuardMs = accountDiagnosticRetryMaxTotalTimeoutMs + 15_000
const precheckConcurrencyDrainPollMs = 1_000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)
const maxSideEffectQueueLength = 5000

const sideEffectQueue = new AccountSideEffectQueue()
const localAccountSuppressions = new Map<string, LocalAccountSuppression>()
const failureStorms = new Map<string, FailureStormEntry>()
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
let localHalfOpenLeaseSequence = 0

export function enqueueGatewayAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): void {
  if (operation.input.success) {
    const canceledCount = cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKey(gatewayAccountRuntimeKey(operation.account))
    if (canceledCount > 0) {
      canceledBySuccessCount += canceledCount
      clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    }
  } else if (coalesceQueuedAccountErrorHandlingSideEffect(operation)) {
    return
  }
  if (isHealthySuccessfulAccountSideEffect(operation)) {
    clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    skippedHealthySuccessCount += 1
    return
  }
  enqueueAccountSideEffect(operation)
}

export function enqueueGatewayStreamFailureSideEffect(operation: StreamFailureOperation): void {
  enqueueAccountSideEffect(operation)
}

export function suppressGatewayAccountLocally(
  account: SuppressibleGatewayAccount | string,
  _settings: GatewaySettings | undefined,
  reason = '上游账号请求失败'
): GatewayAccountLocalSuppressionResult {
  return suppressLocalAccountForGatewayFailure(gatewayAccountRuntimeKey(account), gatewayAccountId(account), reason)
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
  const current = localAccountSuppressions.get(lease.runtimeKey)
  if (!current || current.status !== 'half_open' || current.halfOpenLeaseId !== lease.leaseId) {
    return false
  }
  const now = Date.now()
  localAccountSuppressions.set(lease.runtimeKey, {
    ...current,
    status: 'local_suppressed',
    untilMs: now,
    halfOpenLeaseUntilMs: undefined,
    halfOpenLeaseId: undefined,
    reason: `半开探测请求结束，等待下一次调度确认；${current.reason}`.slice(0, 1000)
  })
  logger.info({
    event: 'gateway_account_local_half_open_released',
    accountId: lease.accountId,
    runtimeKey: lease.runtimeKey,
    localFailureCount: current.localFailureCount
  }, '账号短暂避让半开探测租约已释放')
  return true
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
  cleanupExpiredLocalSuppressions()
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
  if (!forcePrecheck && (entry.failureCount < failureStormThresholdCount || entry.clientIps.size < failureStormDistinctIpThreshold)) {
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

export function snapshotGatewayAccountRuntimeAvailability(): Record<string, AccountRuntimeAvailability> {
  cleanupExpiredLocalSuppressions()
  cleanupExpiredFailureStorms()
  const now = Date.now()
  const snapshot: Record<string, AccountRuntimeAvailability> = {}
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    if (!isLocalSuppressionVisible(runtimeKey, suppression, now)) {
      continue
    }
    snapshot[runtimeKey] = {
      status: suppression.status,
      reason: suppression.reason,
      since: new Date(suppression.sinceMs).toISOString(),
      until: new Date(localSuppressionVisibleUntilMs(suppression, now)).toISOString(),
      failureCount: suppression.failureCount,
      distinctClientIpCount: suppression.distinctClientIpCount,
      distinctApiKeyCount: suppression.distinctApiKeyCount,
      precheckAttemptCount: suppression.precheckAttemptCount,
      localFailureCount: suppression.localFailureCount
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
      precheckAttemptCount: state.attemptCount
    }
  }
  return snapshot
}

export function filterLocallySuppressedGatewayAccounts<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountSuppressionFilterOptions = {}
): LocalAccountSuppressionFilterResult<T> {
  cleanupExpiredLocalSuppressions()
  const now = Date.now()
  const filtered: T[] = []
  const suppressedAccountIds: string[] = []
  const acquiredHalfOpenLeases: GatewayAccountHalfOpenLease[] = []
  let nextRetryAtMs: number | undefined
  for (const account of accounts) {
    const runtimeKey = gatewayAccountRuntimeKey(account)
    const suppression = localAccountSuppressions.get(runtimeKey)
    if (isPrecheckRuntimeBlocking(runtimeKey)) {
      suppressedAccountIds.push(account.id)
      nextRetryAtMs = minRetryAtMs(nextRetryAtMs, Math.max(suppression?.untilMs ?? 0, now + 1000))
      continue
    }
    if (!suppression || !isLocalSuppressionBlocking(suppression, now)) {
      if (suppression && options.acquireHalfOpenLease && canAcquireLocalHalfOpenLease(suppression, now)) {
        acquiredHalfOpenLeases.push(acquireLocalHalfOpenLease(runtimeKey, account, suppression, now))
      }
      filtered.push(account)
      continue
    }
    suppressedAccountIds.push(account.id)
    nextRetryAtMs = minRetryAtMs(nextRetryAtMs, localSuppressionVisibleUntilMs(suppression, now))
  }
  const suppressedCount = suppressedAccountIds.length
  return {
    accounts: filtered,
    suppressedCount,
    allSuppressed: filtered.length === 0 && accounts.length > 0,
    suppressedAccountIds,
    acquiredHalfOpenLeases,
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - now)
  }
}

export function getGatewayAccountSideEffectState(): GatewayAccountSideEffectState {
  cleanupExpiredLocalSuppressions()
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
    localSuppressedAccountCount: countVisibleLocalSuppressions(),
    precheckPendingAccountCount: precheckStates.size,
    nextAttemptAt: nextAttemptAtMs === undefined ? undefined : new Date(nextAttemptAtMs).toISOString()
  }
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
  localAccountSuppressions.clear()
  failureStorms.clear()
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
  const runtimeKey = gatewayAccountRuntimeKey(operation.account)
  const index = sideEffectQueue.findIndex((item) => (
    item.operation.type === 'apply_account_error_handling'
    && gatewayAccountRuntimeKey(item.operation.account) === runtimeKey
  ))
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
  return sideEffectQueue.removeWhere((item) => (
    item.operation.type === 'apply_account_error_handling'
    && gatewayAccountRuntimeKey(item.operation.account) === runtimeKey
  ))
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

function isHealthySuccessfulAccountSideEffect(operation: AccountErrorHandlingOperation): boolean {
  if (!operation.input.success) {
    return false
  }
  const account = operation.account
  return account.status === 'active'
    && !account.cooldownUntil
    && !account.lastErrorMessage
    && Math.max(0, account.streamFailureCount ?? 0) === 0
    && !account.streamFailureWindowStartedAt
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
  if (operation.type === 'apply_account_error_handling') {
    const result = await requestDbService(operation)
    if (result.changed) {
      clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
      clearGatewayRuntimeCache()
    } else if (operation.input.success && result.accountStatus === 'active') {
      clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    }
    return
  }
  const result = await requestDbService(operation)
  if (result.triggered) {
    clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.input.account ?? operation.input.accountId))
    clearGatewayRuntimeCache()
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

function suppressLocalAccountForGatewayFailure(runtimeKey: string, accountId: string, reason: string): GatewayAccountLocalSuppressionResult {
  const now = Date.now()
  const current = localAccountSuppressions.get(runtimeKey)
  const currentFailureCount = current?.localFailureCount ?? 0
  const shouldAdvanceFailureCount = !current
    || current.status === 'half_open'
    || (current.status === 'local_suppressed' && current.untilMs <= now)
  const localFailureCount = shouldAdvanceFailureCount ? currentFailureCount + 1 : Math.max(1, currentFailureCount)
  if (localFailureCount > localSuppressionDelayMs.length) {
    const fallbackDelayMs = localSuppressionDelayMs[localSuppressionDelayMs.length - 1]
    suppressLocalAccount(runtimeKey, fallbackDelayMs, reason, 'local_suppressed', {
      accountId,
      localFailureCount: localSuppressionDelayMs.length
    })
    logger.warn({
      event: 'gateway_account_local_suppression_precheck_required',
      accountId,
      runtimeKey,
      localFailureCount,
      reason
    }, '账号短暂避让半开探测连续失败，要求进入事前确认')
    return {
      runtimeKey,
      action: 'precheck_required',
      reason,
      localFailureCount
    }
  }

  const delayMs = localSuppressionDelayMs[localFailureCount - 1]
  suppressLocalAccount(runtimeKey, delayMs, reason, 'local_suppressed', {
    accountId,
    localFailureCount
  })
  return {
    runtimeKey,
    action: 'suppressed',
    reason,
    localFailureCount,
    delayMs,
    until: new Date(Date.now() + delayMs).toISOString()
  }
}

function suppressLocalAccount(
  runtimeKey: string,
  durationMs: number,
  reason: string,
  status: AccountRuntimeAvailability['status'] = 'local_suppressed',
  metadata: Partial<Pick<LocalAccountSuppression, 'accountId' | 'failureCount' | 'distinctClientIpCount' | 'distinctApiKeyCount' | 'precheckAttemptCount' | 'localFailureCount' | 'halfOpenLeaseUntilMs' | 'halfOpenLeaseId'>> = {}
): void {
  const untilMs = Date.now() + durationMs
  const current = localAccountSuppressions.get(runtimeKey)
  const accountId = metadata.accountId ?? current?.accountId ?? runtimeAccountIdFromKey(runtimeKey)
  const shouldPreserveLongerUntil = current
    && current.untilMs >= untilMs
    && !(current.status === 'half_open' && status === 'local_suppressed')
  if (shouldPreserveLongerUntil) {
    localAccountSuppressions.set(runtimeKey, {
      ...current,
      accountId,
      status,
      reason,
      halfOpenLeaseUntilMs: metadata.halfOpenLeaseUntilMs,
      halfOpenLeaseId: metadata.halfOpenLeaseId,
      ...metadata
    })
    return
  }
  localAccountSuppressions.set(runtimeKey, {
    accountId,
    untilMs,
    reason,
    sinceMs: current?.sinceMs ?? Date.now(),
    status,
    localFailureCount: current?.localFailureCount,
    halfOpenLeaseUntilMs: metadata.halfOpenLeaseUntilMs,
    halfOpenLeaseId: metadata.halfOpenLeaseId,
    ...metadata
  })
  logger.warn({
    event: 'gateway_account_local_suppressed',
    accountId,
    runtimeKey,
    until: new Date(untilMs).toISOString(),
    runtimeStatus: status,
    localFailureCount: metadata.localFailureCount,
    reason
  }, '网关账号已进入 Web 进程本地短期屏蔽')
}

function clearGatewayAccountRuntimeAvailabilityLocal(accountId: string): boolean {
  let cleared = false
  clearPrecheckConcurrencyDrainWait(accountId)
  cleared = localAccountSuppressions.delete(accountId) || cleared
  cleared = failureStorms.delete(accountId) || cleared
  cleared = precheckStates.delete(accountId) || cleared
  return cleared
}

function cleanupExpiredLocalSuppressions(): void {
  const now = Date.now()
  for (const [accountId, suppression] of localAccountSuppressions) {
    if (isPrecheckRuntimeBlocking(accountId)) {
      continue
    }
    if (suppression.status === 'half_open' && getAccountCurrentConcurrency(suppression.accountId) > 0) {
      continue
    }
    const retainUntilMs = Math.max(suppression.untilMs, suppression.halfOpenLeaseUntilMs ?? 0) + localSuppressionIdleRetentionMs
    if (retainUntilMs <= now) {
      localAccountSuppressions.delete(accountId)
    }
  }
}

function cleanupExpiredFailureStorms(): void {
  const now = Date.now()
  for (const [runtimeKey, entry] of failureStorms) {
    if (now - entry.lastSeenMs > failureStormWindowMs) {
      failureStorms.delete(runtimeKey)
    }
  }
}

function isPrecheckRuntimeBlocking(runtimeKey: string): boolean {
  return precheckStates.has(runtimeKey)
}

function isLocalSuppressionVisible(runtimeKey: string, suppression: LocalAccountSuppression, now: number): boolean {
  return isPrecheckRuntimeBlocking(runtimeKey) || isLocalSuppressionBlocking(suppression, now)
}

function isLocalSuppressionBlocking(suppression: LocalAccountSuppression, now: number): boolean {
  if (suppression.status === 'half_open') {
    return (suppression.halfOpenLeaseUntilMs ?? suppression.untilMs) > now
      || getAccountCurrentConcurrency(suppression.accountId) > 0
  }
  if (suppression.status === 'precheck_pending' || suppression.status === 'precheck_failed') {
    return suppression.untilMs > now
  }
  return suppression.untilMs > now
}

function canAcquireLocalHalfOpenLease(suppression: LocalAccountSuppression, now: number): boolean {
  if (suppression.status === 'local_suppressed') {
    return suppression.untilMs <= now
  }
  if (suppression.status === 'half_open') {
    return (suppression.halfOpenLeaseUntilMs ?? suppression.untilMs) <= now
      && getAccountCurrentConcurrency(suppression.accountId) <= 0
  }
  return false
}

function acquireLocalHalfOpenLease(
  runtimeKey: string,
  account: SuppressibleGatewayAccount,
  suppression: LocalAccountSuppression,
  now: number
): GatewayAccountHalfOpenLease {
  const leaseUntilMs = now + localSuppressionHalfOpenLeaseMs
  localHalfOpenLeaseSequence += 1
  const leaseId = `${now}:${localHalfOpenLeaseSequence}`
  localAccountSuppressions.set(runtimeKey, {
    ...suppression,
    accountId: account.id,
    status: 'half_open',
    untilMs: leaseUntilMs,
    halfOpenLeaseUntilMs: leaseUntilMs,
    halfOpenLeaseId: leaseId,
    reason: `短暂避让到期，允许一个请求半开探测；${suppression.reason}`.slice(0, 1000)
  })
  logger.info({
    event: 'gateway_account_local_half_open_acquired',
    accountId: account.id,
    runtimeKey,
    leaseUntil: new Date(leaseUntilMs).toISOString(),
    localFailureCount: suppression.localFailureCount,
    reason: suppression.reason
  }, '账号短暂避让到期，已放行一个真实请求进行半开探测')
  return {
    runtimeKey,
    accountId: account.id,
    leaseId,
    release: () => releaseGatewayAccountHalfOpenLease({ runtimeKey, accountId: account.id, leaseId })
  }
}

function localSuppressionVisibleUntilMs(suppression: LocalAccountSuppression, now = Date.now()): number {
  if (suppression.status !== 'half_open') {
    return suppression.untilMs
  }
  const leaseUntilMs = suppression.halfOpenLeaseUntilMs ?? suppression.untilMs
  return getAccountCurrentConcurrency(suppression.accountId) > 0
    ? Math.max(leaseUntilMs, now + 1000)
    : leaseUntilMs
}

function minRetryAtMs(current: number | undefined, candidate: number): number {
  return current === undefined ? candidate : Math.min(current, candidate)
}

function precheckSuppressionMs(): number {
  return Math.min(precheckSuppressionGuardMs, localSuppressionMaxMs)
}

function countVisibleLocalSuppressions(): number {
  const now = Date.now()
  let count = 0
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    if (isLocalSuppressionVisible(runtimeKey, suppression, now)) {
      count += 1
    }
  }
  return count
}

function operationAccountId(operation: AccountSideEffectOperation): string {
  return operation.type === 'apply_account_error_handling'
    ? operation.account.id
    : operation.input.accountId
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
  const account = accountSummaryFromUpstreamAccount(state.account, state)
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

function accountSummaryFromUpstreamAccount(account: OpenAIAccountSecret, state: Pick<PrecheckState, 'systemAccountId' | 'groupId'>): AccountSummary {
  const emptyUsage = {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
  return accountSummaryWithEffectiveAvailability({
    id: account.id,
    systemAccountId: gatewayAccountSummarySystemAccountId(account),
    ownerSystemAccountId: account.accountOwnerSystemAccountId,
    providerCode: account.providerCode,
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    currentConcurrency: account.currentConcurrency ?? 0,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
    lastSuccessfulTestModel: account.lastSuccessfulTestModel,
    proxyProfileId: account.proxyProfileId,
    schedulable: true,
    cooldownUntil: account.cooldownUntil,
    lastErrorMessage: account.lastErrorMessage,
    streamFailureCount: account.streamFailureCount,
    streamFailureWindowStartedAt: account.streamFailureWindowStartedAt,
    todayUsage: emptyUsage,
    usage: emptyUsage,
    accessType: account.accountAccessType === 'account_authorized' ? 'authorized' : 'owner',
    accountAuthorizationId: account.accountAuthorizationId,
    boundGroupId: account.accountAccessType === 'account_authorized' ? gatewayAccountSummaryBoundGroupId(account) : state.groupId,
    bindingSystemAccountId: account.accountAccessType === 'account_authorized' ? gatewayAccountSummarySystemAccountId(account) : undefined,
    permissions: {
      canUse: true,
      canEdit: false,
      canDelete: false,
      canAuthorize: false,
      canViewCredentials: false
    }
  })
}

function gatewayAccountSummarySystemAccountId(account: OpenAIAccountSecret): string {
  if (account.accountAccessType === 'account_authorized') {
    const bindingSystemAccountId = account.bindingSystemAccountId?.trim()
    if (bindingSystemAccountId) return bindingSystemAccountId
    throw new Error('授权账户缺少绑定系统账户，无法构造测试摘要')
  }
  const systemAccountId = account.systemAccountId?.trim()
  if (systemAccountId) return systemAccountId
  throw new Error('账户缺少系统账户，无法构造测试摘要')
}

function gatewayAccountSummaryBoundGroupId(account: OpenAIAccountSecret): string {
  const boundGroupId = account.boundGroupId?.trim()
  if (boundGroupId) return boundGroupId
  throw new Error('授权账户缺少绑定分组，无法构造测试摘要')
}
