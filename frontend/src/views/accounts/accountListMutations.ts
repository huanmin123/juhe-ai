import { toRaw } from 'vue'

import type { AccountListSortParam } from '@/api/client'
import { compareServerDateTime } from '@/shared/formatters'
import type {
  AccountBalanceSnapshot,
  AccountListItem,
  AuthorizedAccountDispatchMutationResult
} from '@/types/domain'
import type { AccountFilters } from './accountFormTypes'

export interface AccountListRevisionOverlay {
  configRevision: number
  row: AccountListItem
}

export interface AccountListPageWindowContext {
  filters: AccountFilters
  isManagementView: boolean
  sorts: AccountListSortParam[]
}

export function accountListHasAccumulatedPageWindow(
  itemCount: number,
  currentPage: number,
  pageSize: number
): boolean {
  return currentPage > 1 && itemCount > pageSize
}

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

export function mergeAccountListPageWithRevisionOverlays(
  incoming: AccountListItem[],
  current: AccountListItem[],
  overlays: Map<string, AccountListRevisionOverlay>
): AccountListItem[] {
  if (overlays.size === 0) return incoming
  const currentById = new Map(current.map((account) => [account.id, account]))
  return incoming.map((account) => {
    const overlay = overlays.get(account.id)
    if (!overlay) return account
    const incomingRevision = normalizedConfigRevision(account.configRevision)
    if (incomingRevision !== undefined && incomingRevision >= overlay.configRevision) {
      overlays.delete(account.id)
      return account
    }
    const currentAccount = currentById.get(account.id)
    const currentRevision = normalizedConfigRevision(currentAccount?.configRevision)
    return currentAccount && currentRevision !== undefined && currentRevision >= overlay.configRevision
      ? currentAccount
      : overlay.row
  })
}

export function accountListPageWindowChanged(
  previous: AccountListItem,
  updated: AccountListItem,
  context: AccountListPageWindowContext
): boolean {
  if (accountListSortChanged(previous, updated, context.sorts)) return true
  const { filters } = context
  if (filters.keyword.trim() && previous.name !== updated.name) return true
  if (filters.providerCode !== 'all' && previous.providerCode !== updated.providerCode) return true
  if (filters.type !== 'all' && previous.type !== updated.type) return true
  if (filters.groupId && previous.boundGroupId !== updated.boundGroupId) return true
  if (filters.tagIds.length > 0 && selectedTagMembershipChanged(previous, updated, filters.tagIds)) return true
  if (filters.status.length > 0 && accountStatusFilterFactsChanged(previous, updated)) return true
  if (context.isManagementView
    && filters.systemAccountId
    && (previous.systemAccountId !== updated.systemAccountId
      || previous.ownerSystemAccountId !== updated.ownerSystemAccountId)) {
    return true
  }
  return false
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
        sort.field === 'lastUsedAt' || sort.field === 'accountExpiresAt',
        sort.field === 'lastUsedAt' || sort.field === 'accountExpiresAt'
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

function normalizedConfigRevision(value: number | undefined): number | undefined {
  const revision = Number(value)
  return Number.isInteger(revision) && revision >= 1 ? revision : undefined
}

function selectedTagMembershipChanged(
  previous: AccountListItem,
  updated: AccountListItem,
  selectedTagIds: string[]
): boolean {
  const selected = new Set(selectedTagIds)
  const previousIds = new Set((previous.tags ?? []).map((tag) => tag.id).filter((id) => selected.has(id)))
  const updatedIds = new Set((updated.tags ?? []).map((tag) => tag.id).filter((id) => selected.has(id)))
  return previousIds.size !== updatedIds.size || [...previousIds].some((id) => !updatedIds.has(id))
}

function accountStatusFilterFactsChanged(previous: AccountListItem, updated: AccountListItem): boolean {
  return JSON.stringify([
    previous.status,
    previous.schedulable,
    previous.effectiveAvailability,
    previous.authorizationQuotaExceeded,
    previous.authorizationInstanceSourceAccountStatus,
    previous.authorizationInstanceSourceAccountSchedulable,
    previous.authorizationInstanceSourceAccountExpiresAt,
    previous.authorizationInstanceSourceAccountCooldownUntil
  ]) !== JSON.stringify([
    updated.status,
    updated.schedulable,
    updated.effectiveAvailability,
    updated.authorizationQuotaExceeded,
    updated.authorizationInstanceSourceAccountStatus,
    updated.authorizationInstanceSourceAccountSchedulable,
    updated.authorizationInstanceSourceAccountExpiresAt,
    updated.authorizationInstanceSourceAccountCooldownUntil
  ])
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
  nullsAlwaysLast: boolean,
  absoluteTime: boolean
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
  if (absoluteTime && typeof left === 'string' && typeof right === 'string') {
    const leftTimestamp = compareServerDateTime(left, right)
    if (leftTimestamp !== 0) return order === 'desc' ? -leftTimestamp : leftTimestamp
  }
  const compared = left < right ? -1 : 1
  return order === 'desc' ? -compared : compared
}
