import type { AccountBalanceSnapshot, AccountStatusSnapshotResult, AccountSummary } from '@/types/domain'

export function replaceAccountListRow(
  accounts: AccountSummary[],
  updated: AccountSummary
): AccountSummary[] {
  const accountIndex = accounts.findIndex((account) => account.id === updated.id)
  if (accountIndex < 0) return accounts

  const current = accounts[accountIndex]
  const nextAccounts = [...accounts]
  nextAccounts[accountIndex] = {
    ...updated,
    currentConcurrency: current.currentConcurrency,
    currentConcurrencyAvailable: current.currentConcurrencyAvailable,
    accountRuntimeAvailabilityAvailable: current.accountRuntimeAvailabilityAvailable,
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
  let changed = false
  const next = accounts.map((account) => {
    const item = itemsById.get(account.id)
    if (!item) return account
    changed = true
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
      runtimeAvailability: item.runtimeAvailability,
      effectiveAvailability: item.effectiveAvailability,
      lastUsedAt: item.lastUsedAt,
      todayUsage: item.todayUsage,
      accountRuntimeAvailabilityAvailable: snapshot.runtimeSnapshot.accountRuntimeAvailabilityAvailable === true
    }
  })
  return changed ? next : accounts
}
