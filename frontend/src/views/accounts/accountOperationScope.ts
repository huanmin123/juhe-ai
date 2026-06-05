import type { AccountSummary } from '@/types/domain'

export type AccountScopeParams = { systemAccountId: string } | undefined

export function accountOperationSystemAccountId(account?: AccountSummary, fallback?: AccountScopeParams): string | undefined {
  if (!account) return fallback?.systemAccountId
  if (account.accessType === 'authorized') {
    return account.bindingSystemAccountId
      ?? account.systemAccountId
      ?? account.ownerSystemAccountId
      ?? fallback?.systemAccountId
  }
  return account.systemAccountId ?? account.ownerSystemAccountId ?? fallback?.systemAccountId
}

export function accountOperationScopeParams(account: AccountSummary, fallback?: AccountScopeParams): AccountScopeParams {
  const systemAccountId = accountOperationSystemAccountId(account, fallback)
  return systemAccountId ? { systemAccountId } : undefined
}
