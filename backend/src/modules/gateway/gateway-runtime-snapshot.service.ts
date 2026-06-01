import type { AccountRuntimeAvailability, AccountSummary, GroupSummary } from '../../domain/types.js'
import { requestServerAccountConcurrencySnapshot, requestServerAccountRuntimeSnapshot } from '../db-service/db-service-ipc.js'

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
  const concurrency = await loadServerAccountConcurrencySnapshot()
  if (!concurrency) {
    return groups.map(markGroupConcurrencyUnavailable)
  }
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
    ? {
        ...account,
        runtimeAvailability: runtimeStatus
      }
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
