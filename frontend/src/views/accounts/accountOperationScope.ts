import type { AccountSummary } from '@/types/domain'

export type AccountScopeParams = { systemAccountId: string } | undefined

export function accountOperationScopeParams(account: AccountSummary, fallback?: AccountScopeParams): AccountScopeParams {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? fallback?.systemAccountId
    : fallback?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}
