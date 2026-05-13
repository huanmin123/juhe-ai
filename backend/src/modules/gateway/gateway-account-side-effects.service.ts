import { errorLogFields, logger } from '../../shared/logger.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceOperation } from '../db-service/db-service-types.js'
import { clearGatewayRuntimeCache } from './gateway-runtime-cache.service.js'
import type { GatewaySettings } from './account-error-policy.service.js'

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
const retryBaseDelayMs = 500
const retryMaxDelayMs = 30_000

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
  if (operation.input.success) {
    clearLocalAccountSuppression(operation.account.id)
  } else {
    suppressLocalAccount(operation.account.id, localSuppressionMs(operation.input.settings), operation.input.errorMessage ?? '上游账号请求失败')
  }
  enqueueAccountSideEffect(operation)
}

export function enqueueGatewayStreamFailureSideEffect(operation: StreamFailureOperation): void {
  suppressLocalAccount(operation.input.accountId, localSuppressionMsFromMinutes(operation.input.cooldownMinutes), operation.input.reason)
  enqueueAccountSideEffect(operation)
}

export function filterLocallySuppressedGatewayAccounts<T extends { id: string }>(accounts: T[]): {
  accounts: T[]
  suppressedCount: number
  bypassedAllSuppressed: boolean
} {
  cleanupExpiredLocalSuppressions()
  const filtered = accounts.filter((account) => !localAccountSuppressions.has(account.id))
  const suppressedCount = accounts.length - filtered.length
  if (filtered.length === 0 && accounts.length > 0) {
    return {
      accounts,
      suppressedCount,
      bypassedAllSuppressed: true
    }
  }
  return {
    accounts: filtered,
    suppressedCount,
    bypassedAllSuppressed: false
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
        item.nextAttemptAtMs = Date.now() + retryDelayMs(item.attempts)
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
    const result = await requestDbService(operation, { fallbackToLocal: false })
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
    return
  }
  const result = await requestDbService(operation, { fallbackToLocal: false })
  if (result.triggered) {
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

function retryDelayMs(attempts: number): number {
  return Math.min(retryBaseDelayMs * 2 ** Math.max(0, attempts - 1), retryMaxDelayMs)
}

function operationAccountId(operation: AccountSideEffectOperation): string {
  return operation.type === 'apply_account_error_handling'
    ? operation.account.id
    : operation.input.accountId
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms)
  })
}
