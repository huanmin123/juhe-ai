import { errorLogFields, logger } from '../../shared/logger.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceOperation } from '../db-service/db-service-types.js'
import { clearGatewayRuntimeCache } from './gateway-runtime-cache.service.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { exponentialRetryPolicy, retryDueAtMs, waitForRetryDelayMs } from '../../shared/retry-policy.js'

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
}

type SuppressibleGatewayAccount = {
  id: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  bindingSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  boundGroupId?: string
  accountAuthorizationId?: string
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
  failedAttemptCount: number
  droppedCount: number
  expiredCount: number
  localSuppressedAccountCount: number
  nextAttemptAt?: string
}

const maxQueuedSideEffects = 1024
const sideEffectRetentionMs = 10 * 60_000
const localSuppressionMaxMs = 10 * 60_000
const sideEffectRetryPolicy = exponentialRetryPolicy('gateway_account_side_effect_write', 500, 30_000)

const sideEffectQueue: QueuedAccountSideEffect[] = []
const localAccountSuppressions = new Map<string, LocalAccountSuppression>()
let processingSideEffects = false
let drainTimer: NodeJS.Timeout | undefined
let drainTimerDueAtMs: number | undefined
let enqueuedCount = 0
let completedCount = 0
let failedAttemptCount = 0
let droppedCount = 0
let expiredCount = 0

export function enqueueGatewayAccountErrorHandlingSideEffect(operation: AccountErrorHandlingOperation): void {
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
  suppressLocalAccount(gatewayAccountRuntimeKey(account), localSuppressionMs(settings), reason)
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
    failedAttemptCount,
    droppedCount,
    expiredCount,
    localSuppressedAccountCount: localAccountSuppressions.size,
    nextAttemptAt: nextAttemptAtMs === undefined ? undefined : new Date(nextAttemptAtMs).toISOString()
  }
}

export async function flushGatewayAccountSideEffectsForTest(): Promise<void> {
  await flushGatewayAccountSideEffects()
}

export function suppressGatewayAccountLocallyForTest(accountId: string, durationMs: number, reason = '测试本地屏蔽'): void {
  suppressLocalAccount(accountId, Math.max(0, Math.trunc(durationMs)), reason)
}

export function clearGatewayLocalAccountSuppressionsForTest(): void {
  localAccountSuppressions.clear()
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
  const now = Date.now()
  if (sideEffectQueue.length >= maxQueuedSideEffects) {
    sideEffectQueue.shift()
    droppedCount += 1
  }
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
      clearLocalAccountSuppression(gatewayAccountRuntimeKey(operation.account))
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
  if (account.accountAccessType === 'account_authorized') {
    const systemAccountId = account.bindingSystemAccountId ?? account.groupOwnerSystemAccountId ?? ''
    const groupId = account.boundGroupId ?? ''
    const authorizationId = account.accountAuthorizationId ?? ''
    if (systemAccountId && groupId && authorizationId) {
      return `${account.id}:authorized:${systemAccountId}:${groupId}:${authorizationId}`
    }
  }
  return account.id
}

function suppressLocalAccount(accountId: string, durationMs: number, reason: string): void {
  const untilMs = Date.now() + durationMs
  const current = localAccountSuppressions.get(accountId)
  if (current && current.untilMs >= untilMs) {
    return
  }
  localAccountSuppressions.set(accountId, { untilMs, reason })
  logger.warn({
    event: 'gateway_account_local_suppressed',
    accountId,
    until: new Date(untilMs).toISOString(),
    reason
  }, '网关账号已进入 Web 进程本地短期屏蔽')
}

function clearLocalAccountSuppression(accountId: string): void {
  localAccountSuppressions.delete(accountId)
}

function cleanupExpiredLocalSuppressions(): void {
  const now = Date.now()
  for (const [accountId, suppression] of localAccountSuppressions) {
    if (suppression.untilMs <= now) {
      localAccountSuppressions.delete(accountId)
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
