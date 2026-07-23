import { toRaw } from 'vue'

import type { AccountBalanceSnapshot, AccountStatusSnapshotItem, AccountStatusSnapshotResult, AccountSummary } from '@/types/domain'

export function cloneAccountListCacheResult<T>(value: T): T {
  return structuredClone(toRaw(value as object)) as T
}

function effectiveAvailabilityWithPreservedRuntime(previous: AccountSummary, incoming: AccountSummary) {
  if (!previous.runtimeAvailability) return incoming.effectiveAvailability
  const incomingAvailability = incoming.effectiveAvailability
  if (incomingAvailability?.available === false && incomingAvailability.blockerScope !== 'runtime') {
    return incomingAvailability
  }
  return previous.effectiveAvailability
}

function presentationWithPreservedRuntime(
  previous: AccountSummary,
  incoming: AccountSummary,
  effectiveAvailability: AccountSummary['effectiveAvailability']
) {
  return effectiveAvailability?.blockerScope === 'runtime'
    ? previous.availabilityPresentation
    : incoming.availabilityPresentation
}

export function replaceAccountListRow(
  accounts: AccountSummary[],
  updated: AccountSummary
): AccountSummary[] {
  const accountIndex = accounts.findIndex((account) => account.id === updated.id)
  if (accountIndex < 0) return accounts

  const current = accounts[accountIndex]
  const preserveRuntime = current.accountRuntimeAvailabilityAvailable === true
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
    currentConcurrencyAvailable: current.currentConcurrencyAvailable,
    accountRuntimeAvailabilityAvailable: current.accountRuntimeAvailabilityAvailable,
    runtimeAvailability: preserveRuntime ? current.runtimeAvailability : updated.runtimeAvailability,
    effectiveAvailability,
    availabilityPresentation,
    qualityScore: current.qualityScore,
    qualityState: current.qualityState,
    qualityEwmaFirstTokenMs: current.qualityEwmaFirstTokenMs,
    qualityRecentAvgFirstTokenMs: current.qualityRecentAvgFirstTokenMs,
    qualityRecentRequestCount: current.qualityRecentRequestCount,
    qualityRecentErrorCount: current.qualityRecentErrorCount,
    qualityRecentSuccessRate: current.qualityRecentSuccessRate,
    qualityLastErrorAt: current.qualityLastErrorAt,
    qualityLastErrorMessage: current.qualityLastErrorMessage,
    qualityUpdatedAt: current.qualityUpdatedAt,
    balanceSnapshot: current.balanceSnapshot,
    lastUsedAt: current.lastUsedAt,
    todayUsage: current.todayUsage,
    usage: current.usage,
    oauthUsage: current.oauthUsage
  }
  return nextAccounts
}

export function replaceAccountBalanceSnapshot(
  accounts: AccountSummary[],
  accountId: string,
  snapshot: AccountBalanceSnapshot | undefined
): AccountSummary[] {
  const accountIndex = accounts.findIndex((account) => account.id === accountId)
  if (accountIndex < 0) return accounts

  const nextAccounts = [...accounts]
  nextAccounts[accountIndex] = {
    ...accounts[accountIndex],
    balanceSnapshot: snapshot
  }
  return nextAccounts
}

export function mergeAccountStatusSnapshot(
  accounts: AccountSummary[],
  snapshot: AccountStatusSnapshotItem,
  runtimeSnapshot: AccountStatusSnapshotResult['runtimeSnapshot']
): AccountSummary[] {
  const accountIndex = accounts.findIndex((account) => account.id === snapshot.id)
  if (accountIndex < 0) return accounts
  const current = accounts[accountIndex]
  const nextAccounts = [...accounts]
  nextAccounts[accountIndex] = {
    ...current,
    ...snapshot,
    currentConcurrencyAvailable: runtimeSnapshot.accountConcurrencyAvailable,
    accountRuntimeAvailabilityAvailable: runtimeSnapshot.accountRuntimeAvailabilityAvailable
  }
  return nextAccounts
}
