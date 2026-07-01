import type { AccountRuntimeAvailability, AccountSummary, GroupSummary } from '../../../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../../../domain/account-effective-availability.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { loadAccountCurrentConcurrencyByIdsAsync } from '../../../shared/account-concurrency.js'
import { requestServerAccountConcurrencySnapshot, requestServerAccountRuntimeSnapshot } from '../../db-service/db-service-ipc.js'

type AccountConcurrencySnapshot = Record<string, number>
type AccountRuntimeAvailabilitySnapshot = Record<string, AccountRuntimeAvailability>

export interface AccountRuntimeSnapshotStatus {
  accountConcurrencyAvailable: boolean
  accountRuntimeAvailabilityAvailable: boolean
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
    const [concurrency, runtime] = await Promise.all([
      loadRedisAccountConcurrencySnapshot([account.id]),
      loadServerAccountRuntimeSnapshot()
    ])
    const withConcurrency = concurrency
      ? applyAccountConcurrency(account, concurrency)
      : markAccountConcurrencyUnavailable(account)
    return applyAccountRuntimeAvailability(withConcurrency, runtime?.accountRuntimeAvailability)
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
  return await requestServerAccountConcurrencySnapshot(80).catch(() => undefined)
}

async function loadServerAccountRuntimeSnapshot(): Promise<{
  accountConcurrency?: AccountConcurrencySnapshot
  accountRuntimeAvailability?: AccountRuntimeAvailabilitySnapshot
} | undefined> {
  return await requestServerAccountRuntimeSnapshot(80).catch(() => undefined)
}

async function applyRedisAccountRuntimeToAccountList<T extends { items: AccountSummary[] }>(
  result: T
): Promise<T & { runtimeSnapshot: AccountRuntimeSnapshotStatus }> {
  const [concurrency, runtime] = await Promise.all([
    loadRedisAccountConcurrencySnapshot(result.items.map((account) => account.id)),
    loadServerAccountRuntimeSnapshot()
  ])
  const runtimeAvailability = runtime?.accountRuntimeAvailability
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
    currentConcurrency: concurrency[account.id] ?? 0,
    currentConcurrencyAvailable: true
  }
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
  if (group.accessType === 'authorized' || group.accountIds.length === 0) {
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
  return groups.some((group) => group.accessType !== 'authorized' && group.accountIds.length > 0)
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
    total += concurrency[accountId] ?? 0
  }
  return total
}
