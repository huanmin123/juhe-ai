import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { PageDataActivationHandle } from '@/shared/pageDataActivationCoordinator'
import { currentPageDataSecurityGeneration } from '@/shared/pageDataGenerationFences'
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

export interface AccountTagOptionsLoadResult {
  data: AccountTagSummary[]
  superseded: boolean
}

interface AccountTagOptionsMemoryEntry {
  securityGeneration: number
  options: AccountTagSummary[]
}

const accountTagOptionsMemory = new Map<string, AccountTagOptionsMemoryEntry>()
const accountTagOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export function resolveAccountTagOptionsScopeKey(isManagementView: boolean, scopeParams?: AccountScopeParams): string | undefined {
  if (!isManagementView) return 'self'
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? `management:${systemAccountId}` : undefined
}

export function readAccountTagOptionsCache(scopeKey: string): AccountTagSummary[] | undefined {
  const entry = accountTagOptionsMemory.get(scopeKey)
  if (!entry) return undefined
  if (entry.securityGeneration !== currentPageDataSecurityGeneration()) {
    accountTagOptionsMemory.delete(scopeKey)
    return undefined
  }
  return [...entry.options]
}

export function writeAccountTagOptionsCache(scopeKey: string, options: AccountTagSummary[]): void {
  accountTagOptionsMemory.set(scopeKey, {
    securityGeneration: currentPageDataSecurityGeneration(),
    options: [...options]
  })
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

export async function loadAccountTagOptionsCached(input: AccountTagOptionsLoadInput): Promise<AccountTagOptionsLoadResult> {
  const scopeKey = resolveAccountTagOptionsScopeKey(input.isManagementView, input.scopeParams)
  if (!scopeKey) return { data: [], superseded: false }
  const securityGeneration = currentPageDataSecurityGeneration()

  if (!input.force && !input.revalidate) {
    const cached = readAccountTagOptionsCache(scopeKey)
    if (cached) return { data: cached, superseded: false }
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
  const superseded = result.superseded || securityGeneration !== currentPageDataSecurityGeneration()
  if (!superseded) writeAccountTagOptionsCache(scopeKey, result.data)
  void result.confirmation?.then((outcome) => {
    if (
      outcome.state !== 'superseded'
      && outcome.data
      && securityGeneration === currentPageDataSecurityGeneration()
    ) writeAccountTagOptionsCache(scopeKey, outcome.data)
  })
  return { data: result.data, superseded }
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
