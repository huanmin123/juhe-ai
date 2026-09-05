import type { AccountRuntimeAvailability, AccountSummary, GroupSummary } from '../../../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import { publicAccountRuntimeAvailability } from '../../../domain/account-runtime-availability-public.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { loadAccountCurrentConcurrencyByIdsAsync } from '../../../shared/account-concurrency.js'
import { requestServerAccountConcurrencySnapshot, requestServerAccountRuntimeSnapshot } from '../../db-service/db-service-ipc.js'
import { loadDistributedGatewayAccountRuntimeAvailability } from './account-side-effects.service.js'
import { loadPublicAccountCircuitSummaries, type PublicAccountCircuitSummary } from './account-circuit-control-plane-bridge.js'

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
  accountCircuitSummaryAvailable: boolean
}

/**
 * Background-only dependency probe. It deliberately uses synthetic keys so
 * both Redis-backed reads execute without coupling health to a customer
 * account or turning an unavailable dependency into an empty map.
 */
export async function probeAccountRuntimeState(): Promise<{
  accountConcurrencyAvailable: boolean
  accountRuntimeAvailabilityAvailable: boolean
}> {
  const [runtime, concurrency] = await Promise.all([
    loadAccountRuntimeAvailabilityByKeys(['__account_list_projection_runtime_probe__']),
    loadAccountConcurrencyByIds(['__account_list_projection_concurrency_probe__'])
  ])
  return {
    accountConcurrencyAvailable: concurrency.available,
    accountRuntimeAvailabilityAvailable: runtime.available
  }
}

export async function loadAccountRuntimeAvailabilityByKeys(runtimeKeys: string[]): Promise<{
  available: boolean
  values: AccountRuntimeAvailabilitySnapshot
}> {
  const keys = [...new Set(runtimeKeys.filter(Boolean))].slice(0, 100)
  try {
    if (runtimeConfig.runtimeStateDriver === 'redis') {
      return { available: true, values: await loadDistributedGatewayAccountRuntimeAvailability(keys) }
    }
    const runtime = await loadServerAccountRuntimeSnapshot()
    if (!runtime?.accountRuntimeAvailability) return { available: false, values: {} }
    return {
      available: true,
      values: Object.fromEntries(keys.flatMap((key) => {
        const value = runtime.accountRuntimeAvailability?.[key]
        return value ? [[key, value]] : []
      }))
    }
  } catch {
    return { available: false, values: {} }
  }
}

export async function loadAccountConcurrencyByIds(accountIds: string[]): Promise<{
  available: boolean
  values: Record<string, number>
}> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  try {
    if (runtimeConfig.runtimeStateDriver === 'redis') {
      const values = await loadRedisAccountConcurrencySnapshot(ids)
      return { available: Boolean(values), values: values ?? {} }
    }
    const runtime = await loadServerAccountRuntimeSnapshot()
    if (!runtime?.accountConcurrency) return { available: false, values: {} }
    return {
      available: true,
      values: Object.fromEntries(ids.map((id) => [id, numberValue(runtime.accountConcurrency?.[id])]))
    }
  } catch {
    return { available: false, values: {} }
  }
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
        accountRuntimeAvailabilityAvailable: true,
        accountCircuitSummaryAvailable: true
      }
    }
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return await applyRedisAccountRuntimeToAccountList(result)
  }
  const [runtime, circuits] = await Promise.all([
    loadServerAccountRuntimeSnapshot(),
    loadAccountCircuitSummarySnapshot(result.items)
  ])
  if (!runtime?.accountConcurrency) {
    return {
      ...result,
      runtimeSnapshot: {
        accountConcurrencyAvailable: false,
        accountRuntimeAvailabilityAvailable: Boolean(runtime?.accountRuntimeAvailability),
        accountCircuitSummaryAvailable: circuits.available
      },
      items: result.items.map((account) => applyAccountCircuitSummary(markAccountConcurrencyUnavailable(account), circuits.values))
    }
  }
  const runtimeAvailability = runtime.accountRuntimeAvailability
  return {
    ...result,
    runtimeSnapshot: {
      accountConcurrencyAvailable: true,
      accountRuntimeAvailabilityAvailable: Boolean(runtimeAvailability),
      accountCircuitSummaryAvailable: circuits.available
    },
    items: result.items.map((account) => applyAccountCircuitSummary(applyAccountRuntimeAvailability(
      applyAccountConcurrency(account, runtime.accountConcurrency ?? {}), runtimeAvailability
    ), circuits.values))
  }
}

export async function applyServerAccountRuntimeToAccount(account: AccountSummary): Promise<AccountSummary> {
  const circuitsPromise = loadAccountCircuitSummarySnapshot([account])
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    const runtimeAvailability = peekServerAccountRuntimeAvailabilitySnapshot()
    const concurrency = await loadRedisAccountConcurrencySnapshot([accountConcurrencySnapshotId(account)])
    const withConcurrency = concurrency
      ? applyAccountConcurrency(account, concurrency)
      : markAccountConcurrencyUnavailable(account)
    const circuits = await circuitsPromise
    return applyAccountCircuitSummary(applyAccountRuntimeAvailability(withConcurrency, runtimeAvailability), circuits.values)
  }
  const [runtime, circuits] = await Promise.all([loadServerAccountRuntimeSnapshot(), circuitsPromise])
  if (!runtime?.accountConcurrency && !runtime?.accountRuntimeAvailability) {
    return applyAccountCircuitSummary(account, circuits.values)
  }
  return applyAccountCircuitSummary(applyAccountRuntimeAvailability(
    runtime.accountConcurrency ? applyAccountConcurrency(account, runtime.accountConcurrency) : account,
    runtime.accountRuntimeAvailability
  ), circuits.values)
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
  const [concurrency, circuits] = await Promise.all([
    loadRedisAccountConcurrencySnapshot(accountConcurrencySnapshotIds(result.items)),
    loadAccountCircuitSummarySnapshot(result.items)
  ])
  return {
    ...result,
    runtimeSnapshot: {
      accountConcurrencyAvailable: Boolean(concurrency),
      accountRuntimeAvailabilityAvailable: false,
      accountCircuitSummaryAvailable: circuits.available
    },
    items: result.items.map((account) => {
      const withConcurrency = concurrency
        ? applyAccountConcurrency(account, concurrency)
        : markAccountConcurrencyUnavailable(account)
      return applyAccountCircuitSummary(withConcurrency, circuits.values)
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
  if (!runtimeStatus) return account
  const withAvailability = accountSummaryWithEffectiveAvailability({
        ...account,
        runtimeAvailability: runtimeStatus
      })
  return {
    ...withAvailability,
    runtimeAvailability: publicAccountRuntimeAvailability(runtimeStatus)
  }
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

async function loadAccountCircuitSummarySnapshot(accounts: AccountSummary[]): Promise<{
  available: boolean
  values: Record<string, PublicAccountCircuitSummary>
}> {
  try {
    const keys = accounts.map(accountRuntimeAvailabilityKey)
    return { available: true, values: await loadPublicAccountCircuitSummaries(keys) }
  } catch {
    return { available: false, values: {} }
  }
}

function applyAccountCircuitSummary(
  account: AccountSummary,
  summaries: Record<string, PublicAccountCircuitSummary>
): AccountSummary {
  const summary = summaries[accountRuntimeAvailabilityKey(account)]
  return summary ? { ...account, circuitSummary: summary } : account
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
