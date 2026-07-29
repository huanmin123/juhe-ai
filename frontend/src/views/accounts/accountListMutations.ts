import { toRaw } from 'vue'

import type { AccountListSortParam } from '@/api/client'
import type {
  AccountBalanceSnapshot,
  AccountListItem,
  AuthorizedAccountDispatchMutationResult
} from '@/types/domain'

export function cloneAccountListCacheResult<T>(value: T): T {
  return structuredClone(toRaw(value as object)) as T
}

function effectiveAvailabilityWithPreservedRuntime(previous: AccountListItem, incoming: AccountListItem) {
  if (!previous.runtimeAvailability) return incoming.effectiveAvailability
  const incomingAvailability = incoming.effectiveAvailability
  if (incomingAvailability?.available === false && incomingAvailability.blockerScope !== 'runtime') {
    return incomingAvailability
  }
  if (previous.effectiveAvailability?.available === false
    && previous.effectiveAvailability.blockerScope !== 'runtime') {
    return incomingAvailability
  }
  return previous.effectiveAvailability
}

function presentationWithPreservedRuntime(
  previous: AccountListItem,
  incoming: AccountListItem,
  effectiveAvailability: AccountListItem['effectiveAvailability']
) {
  return effectiveAvailability?.blockerScope === 'runtime'
    ? previous.availabilityPresentation
    : incoming.availabilityPresentation
}

export function replaceAccountListRow(
  accounts: AccountListItem[],
  updated: AccountListItem
): AccountListItem[] {
  const accountIndex = accounts.findIndex((account) => account.id === updated.id)
  if (accountIndex < 0) return accounts

  const current = accounts[accountIndex]
  if (isOlderConfigRevision(current.configRevision, updated.configRevision)) return accounts
  const preserveRuntime = current.runtimeAvailability !== undefined
  const effectiveAvailability = preserveRuntime
    ? effectiveAvailabilityWithPreservedRuntime(current, updated)
    : updated.effectiveAvailability
  const availabilityPresentation = preserveRuntime
    ? presentationWithPreservedRuntime(current, updated, effectiveAvailability)
    : updated.availabilityPresentation
  const nextAccounts = [...accounts]
  nextAccounts[accountIndex] = {
    ...updated,
    currentConcurrency: current.currentConcurrency,
    runtimeAvailability: preserveRuntime ? current.runtimeAvailability : updated.runtimeAvailability,
    effectiveAvailability,
    availabilityPresentation,
    balanceSnapshot: current.balanceSnapshot,
    lastUsedAt: current.lastUsedAt,
    todayUsage: current.todayUsage
  }
  return nextAccounts
}

export function accountListSortChanged(
  previous: AccountListItem,
  updated: AccountListItem,
  sorts: AccountListSortParam[]
): boolean {
  return sorts.some((sort) => accountListSortValue(previous, sort.field) !== accountListSortValue(updated, sort.field))
}

export function sortAccountListRows(accounts: AccountListItem[], sorts: AccountListSortParam[]): AccountListItem[] {
  if (accounts.length < 2 || sorts.length === 0) return accounts
  const originalIndex = new Map(accounts.map((account, index) => [account.id, index]))
  return [...accounts].sort((left, right) => {
    for (const sort of sorts) {
      const compared = compareAccountListSortValues(
        accountListSortValue(left, sort.field),
        accountListSortValue(right, sort.field),
        sort.order,
        sort.field === 'lastUsedAt'
      )
      if (compared !== 0) return compared
    }
    return (originalIndex.get(left.id) ?? 0) - (originalIndex.get(right.id) ?? 0)
  })
}

export function mergeAuthorizedDispatchMutation(
  account: AccountListItem,
  mutation: AuthorizedAccountDispatchMutationResult
): AccountListItem {
  const {
    failureStateCleared: _failureStateCleared,
    ...listPatch
  } = mutation.patch
  return {
    ...account,
    ...listPatch,
    configRevision: mutation.configRevision
  }
}

export function replaceAccountBalanceSnapshot(
  accounts: AccountListItem[],
  accountId: string,
  snapshot: AccountBalanceSnapshot | undefined
): AccountListItem[] {
  const accountIndex = accounts.findIndex((account) => account.id === accountId)
  if (accountIndex < 0) return accounts

  const nextAccounts = [...accounts]
  nextAccounts[accountIndex] = {
    ...accounts[accountIndex],
    balanceSnapshot: snapshot
  }
  return nextAccounts
}

function isOlderConfigRevision(current: number | undefined, incoming: number | undefined): boolean {
  return Number.isInteger(current)
    && Number.isInteger(incoming)
    && Number(incoming) < Number(current)
}

function accountListSortValue(account: AccountListItem, field: AccountListSortParam['field']): string | number | boolean | undefined {
  if (field === 'priority') return account.priority
  if (field === 'superPriority') return account.superPriorityEnabled
  if (field === 'fallback') return account.fallbackEnabled
  if (field === 'name') return account.name
  if (field === 'type') return account.type
  if (field === 'providerCode') return account.providerCode
  if (field === 'systemAccount') return account.systemAccountName ?? account.systemAccountId
  if (field === 'concurrency') return account.concurrencyLimit
  if (field === 'status') return account.status
  if (field === 'accountExpiresAt') {
    return account.authorizationExpiresAt
      ?? account.authorizationInstanceSourceAccountExpiresAt
      ?? account.accountExpiresAt
  }
  return account.lastUsedAt
}

function compareAccountListSortValues(
  left: string | number | boolean | undefined,
  right: string | number | boolean | undefined,
  order: AccountListSortParam['order'],
  nullsAlwaysLast: boolean
): number {
  const leftMissing = left === undefined || left === ''
  const rightMissing = right === undefined || right === ''
  if (leftMissing || rightMissing) {
    if (leftMissing && rightMissing) return 0
    if (nullsAlwaysLast) return leftMissing ? 1 : -1
    const compared = leftMissing ? -1 : 1
    return order === 'desc' ? -compared : compared
  }
  if (left === right) return 0
  const compared = left < right ? -1 : 1
  return order === 'desc' ? -compared : compared
}
