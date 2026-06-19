import { api } from '@/api/client'
import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'
import type { AccountSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'

interface AccountDetailLoadInput {
  accountId: string
  force?: boolean
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}

const accountDetailCache = createShortLivedRequestCache<AccountSummary>({
  maxEntries: 50,
  ttlMs: 5_000
})

export function resolveAccountDetailCacheKey(isManagementView: boolean, accountId: string, scopeParams?: AccountScopeParams): string {
  const scopeKey = isManagementView ? `management:${scopeParams?.systemAccountId ?? 'all'}` : 'self'
  return `${scopeKey}:${accountId}`
}

export function invalidateAccountDetailCache(cacheKey?: string): void {
  if (!cacheKey) return
  accountDetailCache.remove(cacheKey)
}

export function invalidateAccountDetailForAccount(input: {
  accountId?: string
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}): void {
  if (!input.accountId) return
  invalidateAccountDetailCache(resolveAccountDetailCacheKey(input.isManagementView, input.accountId, input.scopeParams))
}

export async function loadAccountDetailCached(input: AccountDetailLoadInput): Promise<AccountSummary> {
  const cacheKey = resolveAccountDetailCacheKey(input.isManagementView, input.accountId, input.scopeParams)
  return accountDetailCache.load(cacheKey, () => input.isManagementView
    ? api.accounts.detail(input.accountId, input.scopeParams)
    : api.myAccounts.detail(input.accountId), input.force)
}
