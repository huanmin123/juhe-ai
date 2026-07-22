import { api } from '@/api/client'
import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'
import type { AccountSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'
import { invalidateAccountTestOptionsCache } from './accountTestOptionsCache'

export type AccountDetailLevel = 'edit-basic' | 'advanced'

interface AccountDetailLoadInput {
  accountId: string
  force?: boolean
  isManagementView: boolean
  level?: AccountDetailLevel
  scopeParams?: AccountScopeParams
}

const accountDetailLevels: AccountDetailLevel[] = ['edit-basic', 'advanced']

const accountDetailCache = createShortLivedRequestCache<AccountSummary>({
  maxEntries: 50,
  ttlMs: 5_000
})

export function resolveAccountDetailCacheKey(
  isManagementView: boolean,
  accountId: string,
  scopeParams?: AccountScopeParams,
  level: AccountDetailLevel = 'advanced'
): string {
  const scopeKey = isManagementView ? `management:${scopeParams?.systemAccountId ?? 'all'}` : 'self'
  return `${level}:${scopeKey}:${accountId}`
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
  invalidateAccountTestOptionsCache()
  for (const level of accountDetailLevels) {
    invalidateAccountDetailCache(resolveAccountDetailCacheKey(input.isManagementView, input.accountId, input.scopeParams, level))
  }
}

export async function loadAccountDetailCached(input: AccountDetailLoadInput): Promise<AccountSummary> {
  const level = input.level ?? 'advanced'
  const cacheKey = resolveAccountDetailCacheKey(input.isManagementView, input.accountId, input.scopeParams, level)
  return accountDetailCache.load(cacheKey, () => {
    if (input.isManagementView) {
      return level === 'edit-basic'
        ? api.accounts.editBasicDetail(input.accountId, input.scopeParams)
        : api.accounts.advancedDetail(input.accountId, input.scopeParams)
    }
    return level === 'edit-basic'
      ? api.myAccounts.editBasicDetail(input.accountId)
      : api.myAccounts.advancedDetail(input.accountId)
  }, input.force)
}
