import type { AccountSummary, GroupSummary } from '../../domain/types.js'
import { requestServerAccountConcurrencySnapshot } from '../db-service/db-service-ipc.js'

type AccountConcurrencySnapshot = Record<string, number>

export async function applyServerAccountConcurrencyToAccountList<T extends { items: AccountSummary[] }>(result: T): Promise<T> {
  if (!result.items.some((account) => account.accessType !== 'authorized')) {
    return result
  }
  const concurrency = await loadServerAccountConcurrencySnapshot()
  if (!concurrency) {
    return result
  }
  return {
    ...result,
    items: result.items.map((account) => applyAccountConcurrency(account, concurrency))
  }
}

export async function applyServerAccountConcurrencyToGroups(groups: GroupSummary[]): Promise<GroupSummary[]> {
  if (!groups.some((group) => group.accessType !== 'authorized' && group.accountIds.length > 0)) {
    return groups
  }
  const concurrency = await loadServerAccountConcurrencySnapshot()
  if (!concurrency) {
    return groups
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
        currentConcurrency
      }
    }
  })
}

export async function applyServerAccountConcurrencyToGroupList<T extends { items: GroupSummary[] }>(result: T): Promise<T> {
  return {
    ...result,
    items: await applyServerAccountConcurrencyToGroups(result.items)
  }
}

async function loadServerAccountConcurrencySnapshot(): Promise<AccountConcurrencySnapshot | undefined> {
  return await requestServerAccountConcurrencySnapshot(80).catch(() => undefined)
}

function applyAccountConcurrency(account: AccountSummary, concurrency: AccountConcurrencySnapshot): AccountSummary {
  if (account.accessType === 'authorized') {
    return account
  }
  return {
    ...account,
    currentConcurrency: concurrency[account.id] ?? 0
  }
}

function sumCurrentConcurrency(accountIds: string[], concurrency: AccountConcurrencySnapshot): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += concurrency[accountId] ?? 0
  }
  return total
}
