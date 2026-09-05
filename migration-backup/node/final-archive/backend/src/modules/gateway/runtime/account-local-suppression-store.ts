import type { AccountRuntimeAvailability } from '../../db-service/db-service-types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { getAccountCurrentConcurrency } from '../../../shared/account-concurrency.js'
import { logger } from '../../../shared/logger.js'
import {
  gatewayAccountRuntimeKey,
  runtimeAccountIdFromKey,
  type SuppressibleGatewayAccount
} from './account-runtime-keys.js'
import { preserveGatewayAccountDispatchPriorityTiers } from './account-dispatch-priority-order.js'
import { gatewayAccountConcurrencyAccountId } from '../dispatch/account-concurrency-identity.js'

export interface LocalAccountSuppression {
  accountId: string
  accountConcurrencyAccountId?: string
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

interface LocalAccountDegradation {
  accountId: string
  reason: string
  sinceMs: number
  firstFailureMs: number
  lastFailureMs: number
  failureCount: number
}

export interface GatewayAccountLocalSuppressionResult {
  runtimeKey: string
  action: 'suppressed' | 'precheck_required' | 'redis_managed'
  reason: string
  localFailureCount: number
  delayMs?: number
  until?: string
}

export interface GatewayAccountHalfOpenLease {
  runtimeKey: string
  accountId: string
  leaseId: string
  generation?: number
  release: () => boolean | Promise<boolean>
  completeSuccess?: () => Promise<boolean>
}

export interface LocalAccountSuppressionFilterResult<T> {
  accounts: T[]
  suppressedCount: number
  allSuppressed: boolean
  suppressedAccountIds: string[]
  acquiredHalfOpenLeases: GatewayAccountHalfOpenLease[]
  precheckSuppressedAccountIds?: string[]
  configuredPolicySuppressedAccountIds?: string[]
  precheckSuppressedRuntimeScopes?: Array<{ runtimeKey: string; generation: number }>
  nextRetryAtMs?: number
  nextRetryAfterMs?: number
}

export interface LocalAccountDegradationOrderResult<T> {
  accounts: T[]
  degradedCount: number
  degradedAccountIds: string[]
  applied: boolean
  bypassedAllDegraded: boolean
}

export interface LocalAccountDegradationOrderOptions {
  modelRankByAccountId?: ReadonlyMap<string, number>
}

export interface LocalAccountSuppressionFilterOptions {
  acquireHalfOpenLease?: boolean
  acquirePrecheckHalfOpenLease?: boolean
  precheckHalfOpenGroupKey?: string
}

type PrecheckRuntimeBlockingPredicate = (runtimeKey: string) => boolean

export const localSuppressionMaxMs = 10 * 60_000
export const localDegradationWindowMs = 5 * 60_000
export const localDegradationActivationFailureThreshold = 2
export const localDegradationMinObservationMs = 60_000
export const localSuppressionPrecheckMinObservationMs = 60_000

const localSuppressionDelayMs = [3_000, 5_000, 10_000] as const
const localSuppressionHalfOpenLeaseMs = 180_000
const localSuppressionIdleRetentionMs = 60_000

const localAccountSuppressions = new Map<string, LocalAccountSuppression>()
const localAccountDegradations = new Map<string, LocalAccountDegradation>()
let localHalfOpenLeaseSequence = 0

export function degradeLocalAccountForGatewayFailure(runtimeKey: string, accountId: string, reason: string): AccountRuntimeAvailability {
  if (!canUseProcessLocalAccountRuntimeState()) {
    return {
      status: 'normal',
      reason,
      since: new Date().toISOString(),
      failureCount: 0
    }
  }
  const now = Date.now()
  cleanupExpiredLocalDegradations(now)
  const currentSuppression = localAccountSuppressions.get(runtimeKey)
  const shouldAdvanceFailureCount = shouldAdvanceLocalDegradationFailureCount(currentSuppression, now)
  const current = localAccountDegradations.get(runtimeKey)
  const withinWindow = current !== undefined && now - current.firstFailureMs <= localDegradationWindowMs
  const nextFailureCount = shouldAdvanceFailureCount
    ? withinWindow ? current.failureCount + 1 : 1
    : Math.max(1, current?.failureCount ?? 1)
  const degradation: LocalAccountDegradation = {
    accountId,
    reason,
    sinceMs: current?.sinceMs ?? now,
    firstFailureMs: withinWindow ? current.firstFailureMs : now,
    lastFailureMs: now,
    failureCount: nextFailureCount
  }
  localAccountDegradations.set(runtimeKey, degradation)
  if (!shouldAdvanceFailureCount) {
    return isLocalAccountDegradationActive(degradation)
      ? localAccountDegradationAvailability(degradation)
      : localAccountDegradationObservationAvailability(degradation)
  }
  if (!isLocalAccountDegradationActive(degradation)) {
    logger.info({
      event: 'gateway_account_runtime_degradation_observed',
      accountId,
      runtimeKey,
      failureCount: degradation.failureCount,
      activationFailureThreshold: localDegradationActivationFailureThreshold,
      observationWindowSeconds: Math.trunc(localDegradationWindowMs / 1000),
      reason
    }, '账号近期失败已记录，暂未达到运行态调度降级门槛')
    return localAccountDegradationObservationAvailability(degradation)
  }
  logger.warn({
    event: 'gateway_account_runtime_degraded',
    accountId,
    runtimeKey,
    failureCount: degradation.failureCount,
    activationFailureThreshold: localDegradationActivationFailureThreshold,
    observationWindowSeconds: Math.trunc(localDegradationWindowMs / 1000),
    reason
  }, '账号近期失败，已进入运行态调度降级，仅在普通候选不足时兜底尝试')
  return localAccountDegradationAvailability(degradation)
}

export function suppressLocalAccountForGatewayFailure(
  runtimeKey: string,
  accountId: string,
  reason: string,
  accountConcurrencyAccountId = accountId
): GatewayAccountLocalSuppressionResult {
  if (!canUseProcessLocalAccountRuntimeState()) {
    return {
      runtimeKey,
      action: 'redis_managed',
      reason,
      localFailureCount: 0
    }
  }
  const now = Date.now()
  const current = localAccountSuppressions.get(runtimeKey)
  const currentFailureCount = current?.localFailureCount ?? 0
  const shouldAdvanceFailureCount = !current
    || current.status === 'half_open'
    || (current.status === 'local_suppressed' && current.untilMs <= now)
  const localFailureCount = shouldAdvanceFailureCount ? currentFailureCount + 1 : Math.max(1, currentFailureCount)
  if (localFailureCount > localSuppressionDelayMs.length) {
    const fallbackDelayMs = localSuppressionDelayMs[localSuppressionDelayMs.length - 1]
    const observedForMs = current ? now - current.sinceMs : 0
    if (observedForMs < localSuppressionPrecheckMinObservationMs) {
      suppressLocalAccount(runtimeKey, fallbackDelayMs, reason, 'local_suppressed', {
        accountId,
        accountConcurrencyAccountId,
        localFailureCount
      })
      logger.warn({
        event: 'gateway_account_local_suppression_precheck_delayed',
        accountId,
        runtimeKey,
        localFailureCount,
        observedForMs,
        minObservationMs: localSuppressionPrecheckMinObservationMs,
        reason
      }, '账号短暂避让半开探测失败，但未达到事前确认最小观察时间')
      return {
        runtimeKey,
        action: 'suppressed',
        reason,
        localFailureCount,
        delayMs: fallbackDelayMs,
        until: new Date(Date.now() + fallbackDelayMs).toISOString()
      }
    }
    suppressLocalAccount(runtimeKey, fallbackDelayMs, reason, 'local_suppressed', {
      accountId,
      accountConcurrencyAccountId,
      localFailureCount
    })
    logger.warn({
      event: 'gateway_account_local_suppression_precheck_required',
      accountId,
      runtimeKey,
      localFailureCount,
      observedForMs,
      minObservationMs: localSuppressionPrecheckMinObservationMs,
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
    accountConcurrencyAccountId,
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
  metadata: Partial<Pick<LocalAccountSuppression, 'accountId' | 'accountConcurrencyAccountId' | 'sinceMs' | 'failureCount' | 'distinctClientIpCount' | 'distinctApiKeyCount' | 'precheckAttemptCount' | 'localFailureCount' | 'halfOpenLeaseUntilMs' | 'halfOpenLeaseId'>> = {}
): void {
  if (!canUseProcessLocalAccountRuntimeState()) return
  const untilMs = Date.now() + durationMs
  const current = localAccountSuppressions.get(runtimeKey)
  const accountId = metadata.accountId ?? current?.accountId ?? runtimeAccountIdFromKey(runtimeKey)
  const accountConcurrencyAccountId = metadata.accountConcurrencyAccountId ?? current?.accountConcurrencyAccountId ?? accountId
  const shouldPreserveLongerUntil = current
    && current.untilMs >= untilMs
    && !(current.status === 'half_open' && status === 'local_suppressed')
  if (shouldPreserveLongerUntil) {
    localAccountSuppressions.set(runtimeKey, {
      ...current,
      accountId,
      accountConcurrencyAccountId,
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
    accountConcurrencyAccountId,
    untilMs,
    reason,
    sinceMs: metadata.sinceMs ?? current?.sinceMs ?? Date.now(),
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
  if (!canUseProcessLocalAccountRuntimeState()) return false
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
  if (!canUseProcessLocalAccountRuntimeState()) return {}
  cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking)
  const now = Date.now()
  cleanupExpiredLocalDegradations(now)
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
  for (const [runtimeKey, degradation] of localAccountDegradations) {
    if (!isLocalAccountDegradationActive(degradation) || snapshot[runtimeKey] || isPrecheckRuntimeBlocking(runtimeKey)) {
      continue
    }
    snapshot[runtimeKey] = localAccountDegradationAvailability(degradation)
  }
  return snapshot
}

export function orderLocalAccountDegradations<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  options: LocalAccountDegradationOrderOptions = {}
): LocalAccountDegradationOrderResult<T> {
  if (!canUseProcessLocalAccountRuntimeState()) {
    return {
      accounts,
      degradedCount: 0,
      degradedAccountIds: [],
      applied: false,
      bypassedAllDegraded: false
    }
  }
  if (accounts.length === 0) {
    return {
      accounts,
      degradedCount: 0,
      degradedAccountIds: [],
      applied: false,
      bypassedAllDegraded: false
    }
  }

  cleanupExpiredLocalDegradations()
  const normalAccounts: T[] = []
  const degradedAccounts: T[] = []
  const degradedAccountIds: string[] = []
  for (const account of accounts) {
    const runtimeKey = gatewayAccountRuntimeKey(account)
    const degradation = localAccountDegradations.get(runtimeKey)
    if (degradation && isLocalAccountDegradationActive(degradation)) {
      degradedAccounts.push(account)
      degradedAccountIds.push(account.id)
    } else {
      normalAccounts.push(account)
    }
  }

  if (degradedAccounts.length === 0) {
    return {
      accounts,
      degradedCount: 0,
      degradedAccountIds: [],
      applied: false,
      bypassedAllDegraded: false
    }
  }

  if (normalAccounts.length === 0) {
    return {
      accounts,
      degradedCount: degradedAccounts.length,
      degradedAccountIds,
      applied: false,
      bypassedAllDegraded: true
    }
  }

  return {
    accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, [...normalAccounts, ...degradedAccounts], {
      modelRankByAccountId: options.modelRankByAccountId
    }),
    degradedCount: degradedAccounts.length,
    degradedAccountIds,
    applied: true,
    bypassedAllDegraded: false
  }
}

export function filterLocalAccountSuppressions<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate,
  options: LocalAccountSuppressionFilterOptions = {}
): LocalAccountSuppressionFilterResult<T> {
  if (!canUseProcessLocalAccountRuntimeState()) {
    return {
      accounts,
      suppressedCount: 0,
      allSuppressed: false,
      suppressedAccountIds: [],
      acquiredHalfOpenLeases: []
    }
  }
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
  if (!canUseProcessLocalAccountRuntimeState()) return false
  return localAccountSuppressions.delete(runtimeKey)
}

export function clearLocalAccountDegradation(runtimeKey: string): boolean {
  if (!canUseProcessLocalAccountRuntimeState()) return false
  return localAccountDegradations.delete(runtimeKey)
}

export function ageLocalAccountDegradationForTest(runtimeKey: string, ageMs: number): void {
  if (!canUseProcessLocalAccountRuntimeState()) return
  const current = localAccountDegradations.get(runtimeKey)
  if (!current) {
    return
  }
  const firstFailureMs = Date.now() - Math.max(0, Math.trunc(ageMs))
  localAccountDegradations.set(runtimeKey, {
    ...current,
    sinceMs: Math.min(current.sinceMs, firstFailureMs),
    firstFailureMs
  })
}

export function activateLocalAccountRuntimeDegradation(
  runtimeKey: string,
  accountId: string,
  reason: string,
  input: { sinceMs?: number; failureCount?: number } = {}
): AccountRuntimeAvailability {
  if (!canUseProcessLocalAccountRuntimeState()) {
    return {
      status: 'normal',
      reason,
      since: new Date().toISOString(),
      failureCount: 0
    }
  }
  const now = Date.now()
  const sinceMs = input.sinceMs ?? now - localDegradationMinObservationMs
  const failureCount = Math.max(
    localDegradationActivationFailureThreshold,
    Math.trunc(input.failureCount ?? localDegradationActivationFailureThreshold)
  )
  const degradation: LocalAccountDegradation = {
    accountId,
    reason,
    sinceMs,
    firstFailureMs: Math.min(sinceMs, now - localDegradationMinObservationMs),
    lastFailureMs: now,
    failureCount
  }
  localAccountDegradations.set(runtimeKey, degradation)
  logger.warn({
    event: 'gateway_account_runtime_degraded',
    accountId,
    runtimeKey,
    failureCount,
    activationFailureThreshold: localDegradationActivationFailureThreshold,
    observationWindowSeconds: Math.trunc(localDegradationWindowMs / 1000),
    reason
  }, '后台探针确认账号近期不稳，已进入运行态调度降级')
  return localAccountDegradationAvailability(degradation)
}

export function clearLocalAccountSuppressionsForTest(): void {
  localAccountSuppressions.clear()
  localAccountDegradations.clear()
}

export function cleanupExpiredLocalSuppressions(isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate): void {
  if (!canUseProcessLocalAccountRuntimeState()) return
  const now = Date.now()
  for (const [accountId, suppression] of localAccountSuppressions) {
    if (isPrecheckRuntimeBlocking(accountId)) {
      continue
    }
    if (suppression.status === 'half_open' && getAccountCurrentConcurrency(localSuppressionConcurrencyAccountId(suppression)) > 0) {
      continue
    }
    const retainUntilMs = Math.max(suppression.untilMs, suppression.halfOpenLeaseUntilMs ?? 0) + localSuppressionIdleRetentionMs
    if (retainUntilMs <= now) {
      localAccountSuppressions.delete(accountId)
    }
  }
}

export function countVisibleLocalSuppressions(isPrecheckRuntimeBlocking: PrecheckRuntimeBlockingPredicate): number {
  if (!canUseProcessLocalAccountRuntimeState()) return 0
  const now = Date.now()
  let count = 0
  for (const [runtimeKey, suppression] of localAccountSuppressions) {
    if (isLocalSuppressionVisible(runtimeKey, suppression, now, isPrecheckRuntimeBlocking)) {
      count += 1
    }
  }
  return count
}

export function countLocalAccountDegradations(): number {
  if (!canUseProcessLocalAccountRuntimeState()) return 0
  cleanupExpiredLocalDegradations()
  let count = 0
  for (const degradation of localAccountDegradations.values()) {
    if (isLocalAccountDegradationActive(degradation)) {
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
      || getAccountCurrentConcurrency(localSuppressionConcurrencyAccountId(suppression)) > 0
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
      && getAccountCurrentConcurrency(localSuppressionConcurrencyAccountId(suppression)) <= 0
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
    accountConcurrencyAccountId: gatewayAccountConcurrencyAccountId(account),
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
  return getAccountCurrentConcurrency(localSuppressionConcurrencyAccountId(suppression)) > 0
    ? Math.max(leaseUntilMs, now + 1000)
    : leaseUntilMs
}

function localSuppressionConcurrencyAccountId(suppression: LocalAccountSuppression): string {
  return suppression.accountConcurrencyAccountId || suppression.accountId
}

function minRetryAtMs(current: number | undefined, candidate: number): number {
  return current === undefined ? candidate : Math.min(current, candidate)
}

function canUseProcessLocalAccountRuntimeState(): boolean {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return true
  localAccountSuppressions.clear()
  localAccountDegradations.clear()
  return false
}

function localAccountDegradationAvailability(degradation: LocalAccountDegradation): AccountRuntimeAvailability {
  return {
    status: 'degraded',
    reason: degradation.reason,
    since: new Date(degradation.sinceMs).toISOString(),
    failureCount: degradation.failureCount
  }
}

function shouldAdvanceLocalDegradationFailureCount(
  currentSuppression: LocalAccountSuppression | undefined,
  now: number
): boolean {
  if (!currentSuppression) return true
  if (currentSuppression.status === 'half_open') return true
  return currentSuppression.status === 'local_suppressed' && currentSuppression.untilMs <= now
}

function localAccountDegradationObservationAvailability(degradation: LocalAccountDegradation): AccountRuntimeAvailability {
  return {
    status: 'normal',
    reason: degradation.reason,
    since: new Date(degradation.sinceMs).toISOString(),
    failureCount: degradation.failureCount
  }
}

function isLocalAccountDegradationActive(degradation: LocalAccountDegradation): boolean {
  return degradation.failureCount >= localDegradationActivationFailureThreshold
    && degradation.lastFailureMs - degradation.firstFailureMs >= localDegradationMinObservationMs
}

function cleanupExpiredLocalDegradations(now = Date.now()): void {
  for (const [runtimeKey, degradation] of localAccountDegradations) {
    if (isLocalAccountDegradationActive(degradation)) {
      continue
    }
    if (now - degradation.firstFailureMs > localDegradationWindowMs) {
      localAccountDegradations.delete(runtimeKey)
    }
  }
}
