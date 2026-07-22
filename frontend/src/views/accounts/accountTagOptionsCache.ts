import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { PageDataActivationHandle } from '@/shared/pageDataActivationCoordinator'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'

interface AccountTagOptionsLoadInput {
  activation?: PageDataActivationHandle
  force?: boolean
  isManagementView: boolean
  revalidate?: boolean
  scopeParams?: AccountScopeParams
}

const accountTagOptionsMemory = new Map<string, AccountTagSummary[]>()
const accountTagOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export function resolveAccountTagOptionsScopeKey(isManagementView: boolean, scopeParams?: AccountScopeParams): string | undefined {
  if (!isManagementView) return 'self'
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? `management:${systemAccountId}` : undefined
}

export function readAccountTagOptionsCache(scopeKey: string): AccountTagSummary[] | undefined {
  const options = accountTagOptionsMemory.get(scopeKey)
  return options ? [...options] : undefined
}

export function writeAccountTagOptionsCache(scopeKey: string, options: AccountTagSummary[]): void {
  accountTagOptionsMemory.set(scopeKey, [...options])
}

export function invalidateAccountTagOptionsCache(scopeKey?: string): void {
  if (!scopeKey) {
    accountTagOptionsMemory.clear()
    void accountTagOptionsResourceCache.invalidate('accounts.options')
    return
  }
  accountTagOptionsMemory.delete(scopeKey)
  const scope = accountTagResourceScope(scopeKey.startsWith('management:'), targetSystemAccountId(scopeKey))
  const route = scopeKey.startsWith('management:') ? '/accounts/tags' : '/my-accounts/tags'
  void accountTagOptionsResourceCache.invalidate('accounts.options', scope, route)
}

export async function loadAccountTagOptionsCached(input: AccountTagOptionsLoadInput): Promise<AccountTagSummary[]> {
  const scopeKey = resolveAccountTagOptionsScopeKey(input.isManagementView, input.scopeParams)
  if (!scopeKey) return []

  if (!input.force && !input.revalidate) {
    const cached = accountTagOptionsMemory.get(scopeKey)
    if (cached) return [...cached]
  }
  const systemAccountId = input.scopeParams?.systemAccountId?.trim()
  const scope = accountTagResourceScope(input.isManagementView, systemAccountId)
  const route = input.isManagementView ? '/accounts/tags' : '/my-accounts/tags'
  if (input.force) await accountTagOptionsResourceCache.invalidate('accounts.options', scope, route)
  const result = await accountTagOptionsResourceCache.load<AccountTagSummary[]>({
    cacheKey: {
      scope,
      route,
      query: { systemAccountId },
      version: 1
    },
    domain: 'accounts.options',
    viewScope: input.isManagementView ? 'admin' : 'self',
    activation: input.activation,
    ...(input.isManagementView && systemAccountId ? { targetSystemAccountId: systemAccountId } : {}),
    loadNetwork: () => input.isManagementView
      ? api.accounts.tags(input.scopeParams)
      : api.myAccounts.tags()
  })
  accountTagOptionsMemory.set(scopeKey, [...result.data])
  void result.confirmation?.then((outcome) => {
    if (outcome.data) accountTagOptionsMemory.set(scopeKey, [...outcome.data])
  })
  return result.data
}

function accountTagResourceScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}

function targetSystemAccountId(scopeKey: string): string | undefined {
  return scopeKey.startsWith('management:') ? scopeKey.slice('management:'.length).trim() || undefined : undefined
}
