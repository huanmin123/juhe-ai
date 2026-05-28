import { errorLogFields, logger } from '../../shared/logger.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { AccountRuntimeAvailability, DbServiceOperation } from '../db-service/db-service-types.js'
import { clearGatewayRuntimeCache } from './gateway-runtime-cache.service.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { exponentialRetryPolicy, retryDueAtMs, waitForRetryDelayMs } from '../../shared/retry-policy.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import type { AccountSummary } from '../../domain/types.js'

type AccountErrorHandlingOperation = Extract<DbServiceOperation, { type: 'apply_account_error_handling' }>
type StreamFailureOperation = Extract<DbServiceOperation, { type: 'record_account_stream_failure' }>
type AccountSideEffectOperation = AccountErrorHandlingOperation | StreamFailureOperation

interface QueuedAccountSideEffect {
  operation: AccountSideEffectOperation
  attempts: number
  enqueuedAtMs: number
  nextAttemptAtMs: number
  expiresAtMs: number
}

interface LocalAccountSuppression {
  untilMs: number
  reason: string
  sinceMs: number
  status: AccountRuntimeAvailability['status']
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
}

type SuppressibleGatewayAccount = {
  id: string
  accessType?: 'owner' | 'authorized'
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  bindingSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  boundGroupId?: string
  accountAuthorizationId?: string
}

export interface GatewayAccountRuntimeClearTarget {
  accountId: string
  authorizedBinding?: {
    systemAccountId?: string
    groupId?: string
    accountAuthorizationId?: string
  }
}

export interface GatewayAccountRuntimeClearResult {
  cleared: boolean
  clearedKeys: string[]
}

interface GatewayAccountFailurePrecheckInput {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
  endpoint?: string
  reason: string
  statusCode?: number
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
  distinctClientIpCount: number
  distinctApiKeyCount: number
  running: boolean
}

export interface LocalAccountSuppressionFilterResult<T> {
  accounts: T[]
  suppressedCount: number
  allSuppressed: boolean
  suppressedAccountIds: string[]
  nextRetryAtMs?: number
  nextRetryAfterMs?: number
}

export type LocalAccountSuppressionWaitResult<T> =
  | {
    ready: true
    waitedMs: number
    filter: LocalAccountSuppressionFilterResult<T>
  }
  | {
    ready: false
    reason: 'timeout' | 'aborted'
    waitedMs: number
    filter: LocalAccountSuppressionFilterResult<T>
  }

export interface GatewayAccountSideEffectState {
  queueLength: number
  processing: boolean
  enqueuedCount: number
  completedCount: number
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
const failureStormWindowMs = 10_000
const failureStormThresholdCount = 5
const failureStormDistinctIpThreshold = 2
const precheckMinIntervalMs = 60_000
const precheckMaxAttempts = 2
const precheckAttemptTimeoutMs = 45_000
const precheckRetryDelayMs = 5_000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)
const maxSideEffectQueueLength = 5000

const sideEffectQueue: QueuedAccountSideEffect[] = []
const localAccountSuppressions = new Map<string, LocalAccountSuppression>()
const failureStorms = new Map<string, FailureStormEntry>()
const precheckStates = new Map<string, PrecheckState>()
let processingSideEffects = false
let drainTimer: NodeJS.Timeout | undefined
let drainTimerDueAtMs: number | undefined
let enqueuedCount = 0
let completedCount = 0
let skippedHealthySuccessCount = 0
let failedAttemptCount = 0
let droppedCount = 0
let expiredCount = 0

export function enqueueGatewayAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): void {
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
  settings: GatewaySettings | undefined,
  reason = '上游账号请求失败'
): void {
  suppressLocalAccount(gatewayAccountRuntimeKey(account), localSuppressionMs(settings), reason, 'local_suppressed')
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

  if (entry.failureCount < failureStormThresholdCount || entry.clientIps.size < failureStormDistinctIpThreshold) {
    return
  }

  const existingPrecheck = precheckStates.get(runtimeKey)
  if (existingPrecheck && now - existingPrecheck.startedAtMs < precheckMinIntervalMs) {
    suppressLocalAccount(runtimeKey, localSuppressionMs(settings), existingPrecheck.reason, 'precheck_pending', {
      failureCount: entry.failureCount,
      distinctClientIpCount: entry.clientIps.size,
      distinctApiKeyCount: entry.apiKeyIds.size,
      precheckAttemptCount: existingPrecheck.attemptCount
    })
    return
  }

  const reason = `多来源短窗口失败，等待事前确认；${input.reason}`.slice(0, 1000)
  const state: PrecheckState = {
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
  precheckStates.set(runtimeKey, state)
  suppressLocalAccount(runtimeKey, localSuppressionMs(settings), reason, 'precheck_pending', {
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
  const snapshot: Record<string, AccountRuntimeAvailability> = {}
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    snapshot[runtimeKey] = {
      status: suppression.status,
      reason: suppression.reason,
      since: new Date(suppression.sinceMs).toISOString(),
      until: new Date(suppression.untilMs).toISOString(),
      failureCount: suppression.failureCount,
      distinctClientIpCount: suppression.distinctClientIpCount,
      distinctApiKeyCount: suppression.distinctApiKeyCount,
      precheckAttemptCount: suppression.precheckAttemptCount
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
  accounts: T[]
): LocalAccountSuppressionFilterResult<T> {
  cleanupExpiredLocalSuppressions()
  const now = Date.now()
  const filtered: T[] = []
  const suppressedAccountIds: string[] = []
  let nextRetryAtMs: number | undefined
  for (const account of accounts) {
    const suppression = localAccountSuppressions.get(gatewayAccountRuntimeKey(account))
    if (!suppression) {
      filtered.push(account)
      continue
    }
    suppressedAccountIds.push(account.id)
    nextRetryAtMs = nextRetryAtMs === undefined
      ? suppression.untilMs
      : Math.min(nextRetryAtMs, suppression.untilMs)
  }
  const suppressedCount = suppressedAccountIds.length
  return {
    accounts: filtered,
    suppressedCount,
    allSuppressed: filtered.length === 0 && accounts.length > 0,
    suppressedAccountIds,
    nextRetryAtMs,
    nextRetryAfterMs: nextRetryAtMs === undefined ? undefined : Math.max(0, nextRetryAtMs - now)
  }
}

export async function waitForLocalAccountSuppressionRelease<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  input: {
    maxWaitMs: number
    signal?: AbortSignal
  }
): Promise<LocalAccountSuppressionWaitResult<T>> {
  const startedAtMs = Date.now()
  let filter = filterLocallySuppressedGatewayAccounts(accounts)
  if (!filter.allSuppressed) {
    return {
      ready: true,
      waitedMs: 0,
      filter
    }
  }

  const maxWaitMs = Math.max(0, Math.trunc(input.maxWaitMs))
  while (filter.allSuppressed) {
    if (input.signal?.aborted) {
      return {
        ready: false,
        reason: 'aborted',
        waitedMs: Date.now() - startedAtMs,
        filter
      }
    }
    const elapsedMs = Date.now() - startedAtMs
    const remainingWaitMs = maxWaitMs - elapsedMs
    if (remainingWaitMs <= 0) {
      return {
        ready: false,
        reason: 'timeout',
        waitedMs: elapsedMs,
        filter
      }
    }
    const retryAfterMs = filter.nextRetryAfterMs ?? remainingWaitMs
    const waitMs = Math.min(remainingWaitMs, Math.max(1, retryAfterMs))
    await waitForRetryDelayMs(waitMs, { signal: input.signal })
    filter = filterLocallySuppressedGatewayAccounts(accounts)
  }

  if (filter.accounts.length > 0) {
    return {
      ready: true,
      waitedMs: Date.now() - startedAtMs,
      filter
    }
  }
  return {
    ready: false,
    reason: 'timeout',
    waitedMs: Date.now() - startedAtMs,
    filter
  }
}

export function getGatewayAccountSideEffectState(): GatewayAccountSideEffectState {
  cleanupExpiredLocalSuppressions()
  const nextAttemptAtMs = sideEffectQueue.reduce<number | undefined>((next, item) => {
    return next === undefined ? item.nextAttemptAtMs : Math.min(next, item.nextAttemptAtMs)
  }, undefined)
  return {
    queueLength: sideEffectQueue.length,
    processing: processingSideEffects,
    enqueuedCount,
    completedCount,
    skippedHealthySuccessCount,
    failedAttemptCount,
    droppedCount,
    expiredCount,
    localSuppressedAccountCount: localAccountSuppressions.size,
    precheckPendingAccountCount: precheckStates.size,
    nextAttemptAt: nextAttemptAtMs === undefined ? undefined : new Date(nextAttemptAtMs).toISOString()
  }
}

export async function flushGatewayAccountSideEffectsForTest(): Promise<void> {
  await flushGatewayAccountSideEffects()
}

export function clearGatewayAccountSideEffectQueueForTest(): void {
  sideEffectQueue.splice(0)
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
  status: AccountRuntimeAvailability['status'] = 'local_suppressed'
): void {
  suppressLocalAccount(accountId, Math.max(0, Math.trunc(durationMs)), reason, status)
}

export function clearGatewayLocalAccountSuppressionsForTest(): void {
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
  sortSideEffectQueue()
  enqueuedCount += 1
  scheduleSideEffectDrain(0)
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
      sortSideEffectQueue()
      const item = sideEffectQueue[0]
      if (!item) {
        break
      }
      if (item.nextAttemptAtMs > now) {
        scheduleSideEffectDrain(item.nextAttemptAtMs - now)
        break
      }
      sideEffectQueue.shift()
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
        sideEffectQueue.unshift(item)
        sortSideEffectQueue()
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
    if (operation.input.success && result.accountStatus === 'active') {
      clearGatewayAccountRuntimeAvailabilityLocal(gatewayAccountRuntimeKey(operation.account))
    }
    if (result.changed) {
      if (result.accountStatus === 'rate_limited' || result.accountStatus === 'temporary_unavailable') {
        suppressLocalAccount(gatewayAccountRuntimeKey(operation.account), localSuppressionMs(operation.input.settings), result.reason ?? operation.input.errorMessage ?? '上游账号请求失败')
      }
      clearGatewayRuntimeCache()
    }
    return
  }
  const result = await requestDbService(operation)
  if (result.triggered) {
    suppressLocalAccount(
      gatewayAccountRuntimeKey(operation.input.account ?? operation.input.accountId),
      localSuppressionMsFromMinutes(operation.input.cooldownMinutes),
      operation.input.reason
    )
    clearGatewayRuntimeCache()
  }
}

function scheduleNextDrainIfNeeded(): void {
  if (processingSideEffects || drainTimer || sideEffectQueue.length === 0) {
    return
  }
  const nextAttemptAtMs = sideEffectQueue.reduce((next, item) => Math.min(next, item.nextAttemptAtMs), Number.MAX_SAFE_INTEGER)
  scheduleSideEffectDrain(Math.max(0, nextAttemptAtMs - Date.now()))
}

function dropExpiredSideEffects(now: number): void {
  for (let index = sideEffectQueue.length - 1; index >= 0; index -= 1) {
    if (sideEffectQueue[index].expiresAtMs <= now) {
      sideEffectQueue.splice(index, 1)
      expiredCount += 1
    }
  }
}

function sortSideEffectQueue(): void {
  sideEffectQueue.sort((left, right) => left.nextAttemptAtMs - right.nextAttemptAtMs || left.enqueuedAtMs - right.enqueuedAtMs)
}

function gatewayAccountRuntimeKey(account: SuppressibleGatewayAccount | string): string {
  if (typeof account === 'string') {
    return account
  }
  if (account.accountAccessType === 'account_authorized' || account.accessType === 'authorized') {
    const systemAccountId = account.bindingSystemAccountId ?? account.groupOwnerSystemAccountId ?? ''
    const groupId = account.boundGroupId ?? ''
    const authorizationId = account.accountAuthorizationId ?? ''
    if (systemAccountId && groupId && authorizationId) {
      return `${account.id}:authorized:${systemAccountId}:${groupId}:${authorizationId}`
    }
  }
  return account.id
}

function gatewayAccountRuntimeClearKeys(account: GatewayAccountRuntimeClearTarget | SuppressibleGatewayAccount | string): string[] {
  if (typeof account === 'string') {
    return account.trim() ? [account.trim()] : []
  }
  const isClearTarget = 'accountId' in account
  const accountId = (isClearTarget ? account.accountId : account.id)?.trim()
  if (!accountId) {
    return []
  }
  const keys = new Set<string>([accountId])
  const authorizedBinding = isClearTarget
    ? account.authorizedBinding
    : account.accountAccessType === 'account_authorized' || account.accessType === 'authorized'
      ? {
          systemAccountId: account.bindingSystemAccountId ?? account.groupOwnerSystemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  const systemAccountId = authorizedBinding?.systemAccountId
  const groupId = authorizedBinding?.groupId
  const authorizationId = authorizedBinding?.accountAuthorizationId
  if (systemAccountId?.trim() && groupId?.trim() && authorizationId?.trim()) {
    keys.add(`${accountId}:authorized:${systemAccountId.trim()}:${groupId.trim()}:${authorizationId.trim()}`)
  }
  return [...keys]
}

function suppressLocalAccount(
  accountId: string,
  durationMs: number,
  reason: string,
  status: AccountRuntimeAvailability['status'] = 'local_suppressed',
  metadata: Pick<LocalAccountSuppression, 'failureCount' | 'distinctClientIpCount' | 'distinctApiKeyCount' | 'precheckAttemptCount'> = {}
): void {
  const untilMs = Date.now() + durationMs
  const current = localAccountSuppressions.get(accountId)
  if (current && current.untilMs >= untilMs) {
    localAccountSuppressions.set(accountId, {
      ...current,
      status,
      reason,
      ...metadata
    })
    return
  }
  localAccountSuppressions.set(accountId, { untilMs, reason, sinceMs: current?.sinceMs ?? Date.now(), status, ...metadata })
  logger.warn({
    event: 'gateway_account_local_suppressed',
    accountId,
    until: new Date(untilMs).toISOString(),
    runtimeStatus: status,
    reason
  }, '网关账号已进入 Web 进程本地短期屏蔽')
}

function clearLocalAccountSuppression(accountId: string): void {
  localAccountSuppressions.delete(accountId)
}

function clearGatewayAccountRuntimeAvailabilityLocal(accountId: string): boolean {
  let cleared = false
  cleared = localAccountSuppressions.delete(accountId) || cleared
  cleared = failureStorms.delete(accountId) || cleared
  cleared = precheckStates.delete(accountId) || cleared
  return cleared
}

function cleanupExpiredLocalSuppressions(): void {
  const now = Date.now()
  for (const [accountId, suppression] of localAccountSuppressions) {
    if (suppression.untilMs <= now) {
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

function localSuppressionMs(settings?: GatewaySettings): number {
  return localSuppressionMsFromMinutes(settings?.defaultTemporaryUnschedulableMinutes)
}

function localSuppressionMsFromMinutes(minutes: unknown): number {
  const value = typeof minutes === 'number' ? minutes : typeof minutes === 'string' ? Number(minutes) : NaN
  const boundedMinutes = Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
  return Math.min(boundedMinutes * 60_000, localSuppressionMaxMs)
}

function operationAccountId(operation: AccountSideEffectOperation): string {
  return operation.type === 'apply_account_error_handling'
    ? operation.account.id
    : operation.input.accountId
}

function delay(ms: number): Promise<void> {
  return waitForRetryDelayMs(ms)
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
      suppressLocalAccount(runtimeKey, localSuppressionMs(latestState.settings), latestState.reason, 'precheck_pending', {
        failureCount: latestState.failureCount,
        distinctClientIpCount: latestState.distinctClientIpCount,
        distinctApiKeyCount: latestState.distinctApiKeyCount,
        precheckAttemptCount: latestState.attemptCount
      })
      const result = await runSingleGatewayAccountPrecheck(latestState)
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
      if (attempt + 1 < precheckMaxAttempts) {
        await delay(precheckRetryDelayMs)
      }
    }

    const finalState = precheckStates.get(runtimeKey)
    if (!finalState) {
      return
    }
    const reason = `事前确认探针连续失败 ${finalState.attemptCount} 次，已标记为临时不可调用；${finalState.reason}`.slice(0, 1000)
    const markResult = await requestDbService({
      type: 'mark_account_precheck_temporary_unavailable',
      account: finalState.account,
      reason,
      precheckStartedAt: new Date(finalState.startedAtMs).toISOString()
    })
    if (markResult.updated) {
      suppressLocalAccount(runtimeKey, localSuppressionMs(finalState.settings), reason, 'precheck_failed', {
        failureCount: finalState.failureCount,
        distinctClientIpCount: finalState.distinctClientIpCount,
        distinctApiKeyCount: finalState.distinctApiKeyCount,
        precheckAttemptCount: finalState.attemptCount
      })
      clearGatewayRuntimeCache()
    } else {
      clearLocalAccountSuppression(runtimeKey)
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

async function runSingleGatewayAccountPrecheck(state: PrecheckState): Promise<{
  success: boolean
  statusCode?: number
  durationMs?: number
  accountFailureEligible?: boolean
}> {
  const { testOpenAIAccount } = await import('../accounts/account-test.service.js')
  const signal = AbortSignal.timeout(precheckAttemptTimeoutMs)
  return await testOpenAIAccount(accountSummaryFromUpstreamAccount(state.account, state), {
    diagnostics: 'limited',
    groupId: state.groupId,
    trafficSource: 'cooldown_retest',
    signal,
    disableAccountStateMutation: true,
    gatewaySettingsOverride: {
      ...state.settings,
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })
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
  return {
    id: account.id,
    systemAccountId: account.bindingSystemAccountId ?? state.systemAccountId ?? account.systemAccountId,
    ownerSystemAccountId: account.accountOwnerSystemAccountId,
    providerCode: 'openai',
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    currentConcurrency: account.currentConcurrency ?? 0,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    supportedModels: account.supportedModels,
    proxyProfileId: account.proxyProfileId,
    passthroughEnabled: account.passthroughEnabled,
    errorPolicyId: account.errorPolicyId,
    schedulable: true,
    cooldownUntil: account.cooldownUntil,
    lastErrorMessage: account.lastErrorMessage,
    streamFailureCount: account.streamFailureCount,
    streamFailureWindowStartedAt: account.streamFailureWindowStartedAt,
    todayUsage: emptyUsage,
    usage: emptyUsage,
    accessType: account.accountAccessType === 'account_authorized' ? 'authorized' : 'owner',
    accountAuthorizationId: account.accountAuthorizationId,
    boundGroupId: account.boundGroupId ?? state.groupId,
    bindingSystemAccountId: account.bindingSystemAccountId ?? state.systemAccountId,
    permissions: {
      canUse: true,
      canEdit: false,
      canDelete: false,
      canAuthorize: false,
      canViewCredentials: false
    }
  }
}
