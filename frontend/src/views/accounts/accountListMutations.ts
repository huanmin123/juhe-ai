import { toRaw } from 'vue'

import type { AccountBalanceSnapshot, AccountStatusSnapshotResult, AccountSummary } from '@/types/domain'

export function accountListItemHasDynamicSnapshot(account: Partial<AccountSummary>): boolean {
  return typeof account.currentConcurrency === 'number'
    && account.todayUsage !== undefined
    && account.effectiveAvailability !== undefined
}

export function cloneAccountListCacheResult<T>(value: T): T {
  return structuredClone(toRaw(value as object)) as T
}

export function mergeAccountListRuntimeSnapshot(
  current: AccountSummary[],
  incoming: AccountSummary[],
  runtimeSnapshotAvailable: boolean,
  sameScope = true
): AccountSummary[] {
  if (!sameScope || current.length === 0) return incoming
  const currentByIdentity = new Map(current.map((account) => [accountRuntimeIdentity(account), account]))
  return incoming.map((account) => {
    const previous = currentByIdentity.get(accountRuntimeIdentity(account))
    if (!previous) return account
    const preserveRuntime = !runtimeSnapshotAvailable && previous.accountRuntimeAvailabilityAvailable === true
    const effectiveAvailability = preserveRuntime
      ? effectiveAvailabilityWithPreservedRuntime(previous, account)
      : account.effectiveAvailability
    const preserveConcurrency = account.currentConcurrencyAvailable !== true
      && previous.currentConcurrencyAvailable === true
    const incomingHasDynamicSnapshot = accountListItemHasDynamicSnapshot(account)
    return {
      ...account,
      currentConcurrency: preserveConcurrency ? previous.currentConcurrency : account.currentConcurrency,
      currentConcurrencyAvailable: preserveConcurrency ? true : account.currentConcurrencyAvailable,
      todayUsage: account.todayUsage ?? previous.todayUsage,
      lastUsedAt: incomingHasDynamicSnapshot ? account.lastUsedAt : previous.lastUsedAt,
      runtimeAvailability: preserveRuntime ? previous.runtimeAvailability : account.runtimeAvailability,
      effectiveAvailability,
      availabilityPresentation: preserveRuntime
        ? presentationWithPreservedRuntime(previous, account, effectiveAvailability)
        : account.availabilityPresentation,
      accountRuntimeAvailabilityAvailable: preserveRuntime ? true : account.accountRuntimeAvailabilityAvailable
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
  const concurrencySnapshotAvailable = snapshot.runtimeSnapshot.accountConcurrencyAvailable === true
  let changed = false
  const next = accounts.map((account) => {
    const item = itemsById.get(account.id)
    if (!item) return account
    changed = true
    const mergedAccount: AccountSummary = { ...account, ...item }
    const effectiveAvailability = runtimeSnapshotAvailable
      ? item.effectiveAvailability
      : effectiveAvailabilityWithPreservedRuntime(account, mergedAccount)
    return {
      ...account,
      status: item.status,
      schedulable: item.schedulable,
      currentConcurrency: concurrencySnapshotAvailable || account.currentConcurrencyAvailable !== true
        ? item.currentConcurrency
        : account.currentConcurrency,
      currentConcurrencyAvailable: concurrencySnapshotAvailable || account.currentConcurrencyAvailable !== true
        ? concurrencySnapshotAvailable
        : true,
      cooldownUntil: item.cooldownUntil,
      lastErrorCode: item.lastErrorCode,
      lastErrorMessage: item.lastErrorMessage,
      lastErrorTraceId: item.lastErrorTraceId,
      cooldownRetestLastAt: item.cooldownRetestLastAt,
      cooldownRetestLastStatusCode: item.cooldownRetestLastStatusCode,
      lastHealthCheckAt: item.lastHealthCheckAt,
      nextHealthCheckAt: item.nextHealthCheckAt,
      lastHealthCheckStatusCode: item.lastHealthCheckStatusCode,
      lastHealthCheckErrorCode: item.lastHealthCheckErrorCode,
      lastHealthCheckErrorMessage: item.lastHealthCheckErrorMessage,
      lastHealthCheckTraceId: item.lastHealthCheckTraceId,
      authorizationStatus: item.authorizationStatus,
      authorizationExpiresAt: item.authorizationExpiresAt,
      authorizationQuotaExceeded: item.authorizationQuotaExceeded,
      authorizationInstanceSourceAccountStatus: item.authorizationInstanceSourceAccountStatus,
      authorizationInstanceSourceAccountSchedulable: item.authorizationInstanceSourceAccountSchedulable,
      authorizationInstanceSourceAccountExpiresAt: item.authorizationInstanceSourceAccountExpiresAt,
      authorizationInstanceSourceAccountCooldownUntil: item.authorizationInstanceSourceAccountCooldownUntil,
      authorizationInstanceSourceAccountLastErrorCode: item.authorizationInstanceSourceAccountLastErrorCode,
      authorizationInstanceSourceAccountLastErrorMessage: item.authorizationInstanceSourceAccountLastErrorMessage,
      authorizationInstanceSourceAccountLastErrorTraceId: item.authorizationInstanceSourceAccountLastErrorTraceId,
      authorizationInstanceSourceAccountCooldownRetestLastAt: item.authorizationInstanceSourceAccountCooldownRetestLastAt,
      authorizationInstanceSourceAccountCooldownRetestLastStatusCode: item.authorizationInstanceSourceAccountCooldownRetestLastStatusCode,
      authorizationInstanceSourceAccountLastHealthCheckAt: item.authorizationInstanceSourceAccountLastHealthCheckAt,
      authorizationInstanceSourceAccountNextHealthCheckAt: item.authorizationInstanceSourceAccountNextHealthCheckAt,
      authorizationInstanceSourceAccountLastHealthCheckStatusCode: item.authorizationInstanceSourceAccountLastHealthCheckStatusCode,
      authorizationInstanceSourceAccountLastHealthCheckErrorCode: item.authorizationInstanceSourceAccountLastHealthCheckErrorCode,
      authorizationInstanceSourceAccountLastHealthCheckErrorMessage: item.authorizationInstanceSourceAccountLastHealthCheckErrorMessage,
      authorizationInstanceSourceAccountLastHealthCheckTraceId: item.authorizationInstanceSourceAccountLastHealthCheckTraceId,
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
