import { toRaw } from 'vue'

import type { AccountBalanceSnapshot, AccountListResult, AccountStatusSnapshotResult, AccountSummary } from '@/types/domain'

export function cloneAccountListCacheResult(value: unknown): AccountListResult {
  return structuredClone(toRaw(value as object)) as AccountListResult
}

export function mergeAccountListRuntimeSnapshot(
  current: AccountSummary[],
  incoming: AccountSummary[],
  runtimeSnapshotAvailable: boolean,
  sameScope = true
): AccountSummary[] {
  if (runtimeSnapshotAvailable || !sameScope || current.length === 0) return incoming
  const currentByIdentity = new Map(current.map((account) => [accountRuntimeIdentity(account), account]))
  return incoming.map((account) => {
    const previous = currentByIdentity.get(accountRuntimeIdentity(account))
    if (!previous || previous.accountRuntimeAvailabilityAvailable !== true) return account
    const effectiveAvailability = effectiveAvailabilityWithPreservedRuntime(previous, account)
    return {
      ...account,
      runtimeAvailability: previous.runtimeAvailability,
      effectiveAvailability,
      availabilityPresentation: presentationWithPreservedRuntime(previous, account, effectiveAvailability),
      accountRuntimeAvailabilityAvailable: true
    }
  })
}

function accountRuntimeIdentity(account: AccountSummary): string {
  return [
    account.id,
    account.bindingSystemAccountId ?? account.systemAccountId ?? '',
    account.boundGroupId ?? '',
    account.accountAuthorizationId ?? '',
    account.authorizationInstanceSourceAccountId ?? ''
  ].join('\u0000')
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
  snapshot: AccountStatusSnapshotResult
): AccountSummary[] {
  const itemsById = new Map(snapshot.items.map((item) => [item.id, item]))
  const runtimeSnapshotAvailable = snapshot.runtimeSnapshot.accountRuntimeAvailabilityAvailable === true
  let changed = false
  const next = accounts.map((account) => {
    const item = itemsById.get(account.id)
    if (!item) return account
    changed = true
    const mergedAccount = { ...account, ...item } as AccountSummary
    const effectiveAvailability = runtimeSnapshotAvailable
      ? item.effectiveAvailability
      : effectiveAvailabilityWithPreservedRuntime(account, mergedAccount)
    return {
      ...account,
      status: item.status,
      schedulable: item.schedulable,
      currentConcurrency: item.currentConcurrency,
      currentConcurrencyAvailable: snapshot.runtimeSnapshot.accountConcurrencyAvailable === true,
      cooldownUntil: item.cooldownUntil,
      lastErrorCode: item.lastErrorCode,
      lastErrorMessage: item.lastErrorMessage,
      lastHealthCheckAt: item.lastHealthCheckAt,
      lastHealthCheckErrorCode: item.lastHealthCheckErrorCode,
      lastHealthCheckErrorMessage: item.lastHealthCheckErrorMessage,
      authorizationStatus: item.authorizationStatus,
      authorizationExpiresAt: item.authorizationExpiresAt,
      authorizationQuotaExceeded: item.authorizationQuotaExceeded,
      authorizationInstanceSourceAccountStatus: item.authorizationInstanceSourceAccountStatus,
      authorizationInstanceSourceAccountSchedulable: item.authorizationInstanceSourceAccountSchedulable,
      authorizationInstanceSourceAccountExpiresAt: item.authorizationInstanceSourceAccountExpiresAt,
      authorizationInstanceSourceAccountCooldownUntil: item.authorizationInstanceSourceAccountCooldownUntil,
      authorizationInstanceSourceAccountLastErrorCode: item.authorizationInstanceSourceAccountLastErrorCode,
      authorizationInstanceSourceAccountLastErrorMessage: item.authorizationInstanceSourceAccountLastErrorMessage,
      apiKeyRuntime: item.apiKeyRuntime,
      runtimeAvailability: runtimeSnapshotAvailable ? item.runtimeAvailability : account.runtimeAvailability,
      effectiveAvailability,
      availabilityPresentation: runtimeSnapshotAvailable
        ? item.availabilityPresentation
        : presentationWithPreservedRuntime(account, mergedAccount, effectiveAvailability),
      lastUsedAt: item.lastUsedAt,
      todayUsage: item.todayUsage,
      accountRuntimeAvailabilityAvailable: runtimeSnapshotAvailable
        ? true
        : account.accountRuntimeAvailabilityAvailable
    }
  })
  return changed ? next : accounts
}
