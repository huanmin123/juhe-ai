import { api } from '@/api/client'
import type { AccountTestOptions } from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'
import type { AccountSummary } from '@/types/domain'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'

interface AccountTestOptionsLoadInput {
  account: AccountSummary
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}

const accountTestOptionsCache = createShortLivedRequestCache<AccountTestOptions>({
  maxEntries: 100,
  ttlMs: 5 * 60_000
})
let cacheGeneration = 0

export function invalidateAccountTestOptionsCache(): void {
  cacheGeneration += 1
  accountTestOptionsCache.clear()
}

export function loadAccountTestOptionsCached(input: AccountTestOptionsLoadInput): Promise<AccountTestOptions> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const loader = () => input.isManagementView
    ? api.accounts.testOptions(input.account.id, scopeParams)
    : api.myAccounts.testOptions(input.account.id)
  const configRevision = input.account.configRevision
  if (!Number.isInteger(configRevision) || Number(configRevision) < 1) {
    return loader()
  }
  const cacheKey = resolveAccountTestOptionsCacheKey(input, scopeParams, Number(configRevision))
  return accountTestOptionsCache.load(cacheKey, loader)
}

function resolveAccountTestOptionsCacheKey(
  input: AccountTestOptionsLoadInput,
  scopeParams: AccountScopeParams,
  configRevision: number
): string {
  const userId = authState.currentUser.value?.id ?? 'anonymous'
  const viewScope = input.isManagementView
    ? `management:${scopeParams?.systemAccountId ?? 'all'}`
    : 'self'
  return `${cacheGeneration}:${userId}:${viewScope}:${input.account.id}:${configRevision}`
}
