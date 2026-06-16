import type { AccountRuntimeAvailability } from '../../db-service/db-service-types.js'
import { getAccountCurrentConcurrency } from '../../../shared/account-concurrency.js'
import { logger } from '../../../shared/logger.js'
import {
  gatewayAccountRuntimeKey,
  runtimeAccountIdFromKey,
  type SuppressibleGatewayAccount
} from './account-runtime-keys.js'

export interface LocalAccountSuppression {
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

export interface LocalAccountSuppressionFilterResult<T> {
  accounts: T[]
  suppressedCount: number
  allSuppressed: boolean
  suppressedAccountIds: string[]
  acquiredHalfOpenLeases: GatewayAccountHalfOpenLease[]
  nextRetryAtMs?: number
  nextRetryAfterMs?: number
}

export interface LocalAccountSuppressionFilterOptions {
  acquireHalfOpenLease?: boolean
}

type PrecheckRuntimeBlockingPredicate = (runtimeKey: string) => boolean

export const localSuppressionMaxMs = 10 * 60_000

const localSuppressionDelayMs = [3_000, 5_000, 10_000] as const
const localSuppressionHalfOpenLeaseMs = 180_000
const localSuppressionIdleRetentionMs = 60_000

const localAccountSuppressions = new Map<string, LocalAccountSuppression>()
let localHalfOpenLeaseSequence = 0

export function suppressLocalAccountForGatewayFailure(runtimeKey: string, accountId: string, reason: string): GatewayAccountLocalSuppressionResult {
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

export function suppressLocalAccount(
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

export function releaseLocalAccountHalfOpenLease(
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

export function snapshotLocalAccountRuntimeAvailability(
  isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate
): Record<string, AccountRuntimeAvailability> {
  cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
  const now = Date.now()
  const snapshot: Record<string, AccountRuntimeAvailability> = {}
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    if (!isLocalSuppressionVisible(runtimeKey, suppression, now, isPrecheckRuntimeBlocking)) {
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
  return snapshot
}

export function filterLocalAccountSuppressions<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate,
  options: LocalAccountSuppressionFilterOptions = {}
): LocalAccountSuppressionFilterResult<T> {
  cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
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

export function clearLocalAccountSuppression(runtimeKey: string): boolean {
  return localAccountSuppressions.delete(runtimeKey)
}

export function clearLocalAccountSuppressionsForTest(): void {
  localAccountSuppressions.clear()
}

export function cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate): void {
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

export function countVisibleLocalSuppressions(isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate): number {
  const now = Date.now()
  let count = 0
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    if (isLocalSuppressionVisible(runtimeKey, suppression, now, isPrecheckRuntimeBlocking)) {
      count += 1
    }
  }
  return count
}

function isLocalSuppressionVisible(
  runtimeKey: string,
  suppression: LocalAccountSuppression,
  now: number,
  isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate
): boolean {
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
    release: () => releaseLocalAccountHalfOpenLease({ runtimeKey, accountId: account.id, leaseId })
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
