import type { AccountRuntimeAvailability, AccountSummary, GroupSummary } from '../../../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { loadAccountCurrentConcurrencyByIdsAsync } from '../../../shared/account-concurrency.js'
import { requestServerAccountConcurrencySnapshot, requestServerAccountRuntimeSnapshot } from '../../db-service/db-service-ipc.js'

type AccountConcurrencySnapshot = Record<string, number>
export type AccountRuntimeAvailabilitySnapshot = Record<string, AccountRuntimeAvailability>
type AccountRuntimeSnapshot = {
  accountConcurrency?: AccountConcurrencySnapshot
  accountRuntimeAvailability?: AccountRuntimeAvailabilitySnapshot
}

interface RuntimeSnapshotCache<T> {
  value?: T
  updatedAtMs: number
  refreshStartedAtMs: number
  refresh?: Promise<T | undefined>
}

export interface AccountRuntimeSnapshotStatus {
  accountConcurrencyAvailable: boolean
  accountRuntimeAvailabilityAvailable: boolean
}

const serverRuntimeSnapshotCacheTtlMs = 300
const serverRuntimeSnapshotMaxStaleMs = 5_000
const serverRuntimeSnapshotRefreshMinIntervalMs = 100
const accountConcurrencySnapshotCache: RuntimeSnapshotCache<AccountConcurrencySnapshot> = {
  updatedAtMs: 0,
  refreshStartedAtMs: 0
}
const accountRuntimeSnapshotCache: RuntimeSnapshotCache<AccountRuntimeSnapshot> = {
  updatedAtMs: 0,
  refreshStartedAtMs: 0
}

export async function applyServerAccountConcurrencyToAccountList<T extends { items: AccountSummary[] }>(
  result: T
): Promise<T & { runtimeSnapshot: AccountRuntimeSnapshotStatus }> {
  if (!accountsRequireServerConcurrencySnapshot(result.items)) {
    return {
      ...result,
      runtimeSnapshot: {
        accountConcurrencyAvailable: true,
        accountRuntimeAvailabilityAvailable: true
      }
    }
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return await applyRedisAccountRuntimeToAccountList(result)
  }
  const runtime = await loadServerAccountRuntimeSnapshot()
  if (!runtime?.accountConcurrency) {
    return {
      ...result,
      runtimeSnapshot: {
        accountConcurrencyAvailable: false,
        accountRuntimeAvailabilityAvailable: Boolean(runtime?.accountRuntimeAvailability)
      },
      items: result.items.map(markAccountConcurrencyUnavailable)
    }
  }
  const runtimeAvailability = runtime.accountRuntimeAvailability
  return {
    ...result,
    runtimeSnapshot: {
      accountConcurrencyAvailable: true,
      accountRuntimeAvailabilityAvailable: Boolean(runtimeAvailability)
    },
    items: result.items.map((account) => applyAccountRuntimeAvailability(
      applyAccountConcurrency(account, runtime.accountConcurrency ?? {}),
      runtimeAvailability
    ))
  }
}

export async function applyServerAccountRuntimeToAccount(account: AccountSummary): Promise<AccountSummary> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    const runtimeAvailability = peekServerAccountRuntimeAvailabilitySnapshot()
    const concurrency = await loadRedisAccountConcurrencySnapshot([accountConcurrencySnapshotId(account)])
    const withConcurrency = concurrency
      ? applyAccountConcurrency(account, concurrency)
      : markAccountConcurrencyUnavailable(account)
    return applyAccountRuntimeAvailability(withConcurrency, runtimeAvailability)
  }
  const runtime = await loadServerAccountRuntimeSnapshot()
  if (!runtime?.accountConcurrency && !runtime?.accountRuntimeAvailability) {
    return account
  }
  return applyAccountRuntimeAvailability(
    runtime.accountConcurrency ? applyAccountConcurrency(account, runtime.accountConcurrency) : account,
    runtime.accountRuntimeAvailability
  )
}

export async function applyServerAccountConcurrencyToGroups(groups: GroupSummary[]): Promise<GroupSummary[]> {
  if (!groupsRequireServerConcurrencySnapshot(groups)) {
    return groups
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    const accountIds = groups.flatMap((group) => group.accessType === 'authorized' ? [] : group.accountIds)
    const concurrency = await loadRedisAccountConcurrencySnapshot(accountIds)
    if (!concurrency) {
      return groups.map(markGroupConcurrencyUnavailable)
    }
    return applyAccountConcurrencyToGroups(groups, concurrency)
  }
  const concurrency = await loadServerAccountConcurrencySnapshot()
  if (!concurrency) {
    return groups.map(markGroupConcurrencyUnavailable)
  }
  return applyAccountConcurrencyToGroups(groups, concurrency)
}

export async function applyServerAccountConcurrencyToGroupList<T extends { items: GroupSummary[] }>(
  result: T
): Promise<T & { runtimeSnapshot: Pick<AccountRuntimeSnapshotStatus, 'accountConcurrencyAvailable'> }> {
  const requiresSnapshot = groupsRequireServerConcurrencySnapshot(result.items)
  const items = await applyServerAccountConcurrencyToGroups(result.items)
  return {
    ...result,
    items,
    runtimeSnapshot: {
      accountConcurrencyAvailable: !requiresSnapshot || items.every((group) => group.accountStats.currentConcurrencyAvailable !== false)
    }
  }
}

async function loadServerAccountConcurrencySnapshot(): Promise<AccountConcurrencySnapshot | undefined> {
  return await loadCachedServerRuntimeSnapshot(accountConcurrencySnapshotCache, () => requestServerAccountConcurrencySnapshot(80))
}

async function loadServerAccountRuntimeSnapshot(): Promise<AccountRuntimeSnapshot | undefined> {
  return await loadCachedServerRuntimeSnapshot(accountRuntimeSnapshotCache, () => requestServerAccountRuntimeSnapshot(80))
}

export async function loadServerAccountRuntimeAvailabilitySnapshot(): Promise<AccountRuntimeAvailabilitySnapshot | undefined> {
  return (await loadServerAccountRuntimeSnapshot())?.accountRuntimeAvailability
}

export function peekServerAccountRuntimeAvailabilitySnapshot(): AccountRuntimeAvailabilitySnapshot | undefined {
  return peekCachedServerRuntimeSnapshot(
    accountRuntimeSnapshotCache,
    () => requestServerAccountRuntimeSnapshot(80)
  )?.accountRuntimeAvailability
}

async function loadCachedServerRuntimeSnapshot<T>(
  cache: RuntimeSnapshotCache<T>,
  loader: () => Promise<T | undefined>
): Promise<T | undefined> {
  if (runtimeConfig.processRole !== 'db-service') {
    return undefined
  }
  const now = Date.now()
  if (cache.value && now - cache.updatedAtMs <= serverRuntimeSnapshotCacheTtlMs) {
    return cache.value
  }
  if (cache.value && now - cache.updatedAtMs <= serverRuntimeSnapshotMaxStaleMs) {
    scheduleServerRuntimeSnapshotRefresh(cache, loader, now)
    return cache.value
  }
  if (cache.refresh) {
    return await cache.refresh
  }
  cache.refresh = refreshServerRuntimeSnapshotCache(cache, loader, now)
    .finally(() => {
      cache.refresh = undefined
    })
  return await cache.refresh
}

function peekCachedServerRuntimeSnapshot<T>(
  cache: RuntimeSnapshotCache<T>,
  loader: () => Promise<T | undefined>
): T | undefined {
  if (runtimeConfig.processRole !== 'db-service') {
    return undefined
  }
  const now = Date.now()
  const ageMs = now - cache.updatedAtMs
  if (!cache.value || ageMs > serverRuntimeSnapshotCacheTtlMs) {
    scheduleServerRuntimeSnapshotRefresh(cache, loader, now)
  }
  return cache.value && ageMs <= serverRuntimeSnapshotMaxStaleMs ? cache.value : undefined
}

function scheduleServerRuntimeSnapshotRefresh<T>(
  cache: RuntimeSnapshotCache<T>,
  loader: () => Promise<T | undefined>,
  now = Date.now()
): void {
  if (cache.refresh || now - cache.refreshStartedAtMs < serverRuntimeSnapshotRefreshMinIntervalMs) {
    return
  }
  cache.refresh = refreshServerRuntimeSnapshotCache(cache, loader, now)
    .catch(() => undefined)
    .finally(() => {
      cache.refresh = undefined
    })
}

async function refreshServerRuntimeSnapshotCache<T>(
  cache: RuntimeSnapshotCache<T>,
  loader: () => Promise<T | undefined>,
  now = Date.now()
): Promise<T | undefined> {
  cache.refreshStartedAtMs = now
  const value = await loader().catch(() => undefined)
  if (value !== undefined) {
    cache.value = value
    cache.updatedAtMs = Date.now()
  }
  return value ?? cache.value
}

async function applyRedisAccountRuntimeToAccountList<T extends { items: AccountSummary[] }>(
  result: T
): Promise<T & { runtimeSnapshot: AccountRuntimeSnapshotStatus }> {
  const runtimeAvailability = peekServerAccountRuntimeAvailabilitySnapshot()
  const concurrency = await loadRedisAccountConcurrencySnapshot(accountConcurrencySnapshotIds(result.items))
  return {
    ...result,
    runtimeSnapshot: {
      accountConcurrencyAvailable: Boolean(concurrency),
      accountRuntimeAvailabilityAvailable: Boolean(runtimeAvailability)
    },
    items: result.items.map((account) => {
      const withConcurrency = concurrency
        ? applyAccountConcurrency(account, concurrency)
        : markAccountConcurrencyUnavailable(account)
      return applyAccountRuntimeAvailability(withConcurrency, runtimeAvailability)
    })
  }
}

async function loadRedisAccountConcurrencySnapshot(accountIds: string[]): Promise<AccountConcurrencySnapshot | undefined> {
  try {
    const concurrencyByAccount = await loadAccountCurrentConcurrencyByIdsAsync(accountIds)
    return Object.fromEntries(concurrencyByAccount.entries())
  } catch {
    return undefined
  }
}

function applyAccountConcurrency(account: AccountSummary, concurrency: AccountConcurrencySnapshot): AccountSummary {
  return {
    ...account,
    currentConcurrency: numberValue(concurrency[accountConcurrencySnapshotId(account)]),
    currentConcurrencyAvailable: true
  }
}

function accountConcurrencySnapshotIds(accounts: AccountSummary[]): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const account of accounts) {
    const accountId = accountConcurrencySnapshotId(account)
    if (!accountId || seen.has(accountId)) {
      continue
    }
    seen.add(accountId)
    result.push(accountId)
  }
  return result
}

function accountConcurrencySnapshotId(account: AccountSummary): string {
  return account.authorizationInstanceSourceAccountId || account.id
}

function applyAccountRuntimeAvailability(
  account: AccountSummary,
  runtimeAvailability?: AccountRuntimeAvailabilitySnapshot
): AccountSummary {
  if (!runtimeAvailability) {
    return account
  }
  const runtimeStatus = runtimeAvailability[accountRuntimeAvailabilityKey(account)]
  return runtimeStatus
    ? accountSummaryWithEffectiveAvailability({
        ...account,
        runtimeAvailability: runtimeStatus
      })
    : account
}

function markAccountConcurrencyUnavailable(account: AccountSummary): AccountSummary {
  return {
    ...account,
    currentConcurrencyAvailable: false
  }
}

function markGroupConcurrencyUnavailable(group: GroupSummary): GroupSummary {
  if (group.accessType === 'authorized' || (group.accountIds.length === 0 && group.accountStats.total === 0)) {
    return group
  }
  return {
    ...group,
    accountStats: {
      ...group.accountStats,
      currentConcurrencyAvailable: false
    }
  }
}

function applyAccountConcurrencyToGroups(groups: GroupSummary[], concurrency: AccountConcurrencySnapshot): GroupSummary[] {
  return groups.map((group) => {
    if (group.accessType === 'authorized') {
      return group
    }
    if (group.accountIds.length === 0 && group.accountStats.total > 0) {
      return markGroupConcurrencyUnavailable(group)
    }
    const currentConcurrency = sumCurrentConcurrency(group.accountIds, concurrency)
    return {
      ...group,
      accountStats: {
        ...group.accountStats,
        currentConcurrency,
        currentConcurrencyAvailable: true
      }
    }
  })
}

function accountsRequireServerConcurrencySnapshot(accounts: AccountSummary[]): boolean {
  return accounts.length > 0
}

function groupsRequireServerConcurrencySnapshot(groups: GroupSummary[]): boolean {
  return groups.some((group) => group.accessType !== 'authorized' && (group.accountIds.length > 0 || group.accountStats.total > 0))
}

function accountRuntimeAvailabilityKey(account: AccountSummary): string {
  if (
    account.accessType === 'authorized'
    && account.accountAuthorizationId
    && account.boundGroupId
  ) {
    const systemAccountId = account.bindingSystemAccountId ?? account.systemAccountId ?? account.ownerSystemAccountId ?? ''
    if (systemAccountId) {
      return `${account.id}:authorized:${systemAccountId}:${account.boundGroupId}:${account.accountAuthorizationId}`
    }
  }
  return account.id
}

function sumCurrentConcurrency(accountIds: string[], concurrency: AccountConcurrencySnapshot): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += numberValue(concurrency[accountId])
  }
  return total
}

function numberValue(value: unknown): number {
  const number = typeof value === 'string' ? Number(value.trim()) : value
  return typeof number === 'number' && Number.isFinite(number) ? number : 0
}
