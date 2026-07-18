import { api, pageDataApi } from '@/api/client'
import type { AccountTestOptions } from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { AccountSummary } from '@/types/domain'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'

interface AccountTestOptionsLoadInput {
  account: AccountSummary
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}

const accountTestOptionsCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))
let cacheGeneration = 0

export function invalidateAccountTestOptionsCache(): void {
  cacheGeneration += 1
  void accountTestOptionsCache.invalidate('accounts.options')
}

export async function loadAccountTestOptionsCached(input: AccountTestOptionsLoadInput): Promise<AccountTestOptions> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const loader = () => input.isManagementView
    ? api.accounts.testOptions(input.account.id, scopeParams)
    : api.myAccounts.testOptions(input.account.id)
  const configRevision = input.account.configRevision
  if (!Number.isInteger(configRevision) || Number(configRevision) < 1) {
    return loader()
  }
  const viewScope = input.isManagementView ? 'admin' as const : 'self' as const
  const route = input.isManagementView
    ? `/accounts/${input.account.id}/test-options`
    : `/my-accounts/${input.account.id}/test-options`
  const result = await accountTestOptionsCache.load<AccountTestOptions>({
    cacheKey: {
      scope: resolveAccountTestOptionsCacheKey(input, scopeParams, Number(configRevision)),
      route,
      query: {
        accountId: input.account.id,
        configRevision: Number(configRevision),
        systemAccountId: scopeParams?.systemAccountId
      },
      version: `${cacheGeneration}:1`
    },
    domain: 'accounts.options',
    viewScope,
    ...(viewScope === 'admin' && scopeParams?.systemAccountId
      ? { targetSystemAccountId: scopeParams.systemAccountId }
      : {}),
    loadNetwork: loader
  })
  return result.data
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
  const role = authState.currentUser.value?.role ?? 'anonymous'
  return `${userId}:${role}:${viewScope}:${input.account.id}:${configRevision}`
}
