import { toRaw } from 'vue'

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
