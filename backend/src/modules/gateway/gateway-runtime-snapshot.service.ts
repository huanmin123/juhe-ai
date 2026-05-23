import type { AccountSummary, GroupSummary } from '../../domain/types.js'
import { requestServerAccountConcurrencySnapshot } from '../db-service/db-service-ipc.js'

type AccountConcurrencySnapshot = Record<string, number>

export interface AccountConcurrencyRuntimeSnapshotStatus {
  accountConcurrencyAvailable: boolean
}

export async function applyServerAccountConcurrencyToAccountList<T extends { items: AccountSummary[] }>(
  result: T
): Promise<T & { runtimeSnapshot: AccountConcurrencyRuntimeSnapshotStatus }> {
  if (!accountsRequireServerConcurrencySnapshot(result.items)) {
    return {
      ...result,
      runtimeSnapshot: { accountConcurrencyAvailable: true }
    }
  }
  const concurrency = await loadServerAccountConcurrencySnapshot()
  if (!concurrency) {
    return {
      ...result,
      runtimeSnapshot: { accountConcurrencyAvailable: false },
      items: result.items.map(markAccountConcurrencyUnavailable)
    }
  }
  return {
    ...result,
    runtimeSnapshot: { accountConcurrencyAvailable: true },
    items: result.items.map((account) => applyAccountConcurrency(account, concurrency))
  }
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
): Promise<T & { runtimeSnapshot: AccountConcurrencyRuntimeSnapshotStatus }> {
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

function applyAccountConcurrency(account: AccountSummary, concurrency: AccountConcurrencySnapshot): AccountSummary {
  return {
    ...account,
    currentConcurrency: concurrency[account.id] ?? 0,
    currentConcurrencyAvailable: true
  }
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

function sumCurrentConcurrency(accountIds: string[], concurrency: AccountConcurrencySnapshot): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += concurrency[accountId] ?? 0
  }
  return total
}
