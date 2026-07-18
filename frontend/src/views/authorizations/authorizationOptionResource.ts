import { pageDataApi } from '@/api/client'
import type { PageDataDomain } from '@/api/domains/pageData'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'

interface AuthorizationOptionResourceOptions<T> {
  apply: (options: T) => void
  domain: Extract<PageDataDomain, 'accounts.options' | 'groups.static' | 'systemAccounts.options' | 'teams.options'>
  isCurrent: () => boolean
  isManagementView: boolean
  loadNetwork: () => Promise<T>
  query: unknown
  route: string
  targetSystemAccountId?: string
}

const authorizationOptionResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadAuthorizationOptionResource<T>(options: AuthorizationOptionResourceOptions<T>): Promise<T> {
  const result = await authorizationOptionResourceCache.load<T>({
    cacheKey: {
      scope: authorizationOptionScope(options.isManagementView, options.targetSystemAccountId),
      route: options.route,
      query: options.query,
      version: 1
    },
    domain: options.domain,
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && options.targetSystemAccountId
      ? { targetSystemAccountId: options.targetSystemAccountId }
      : {}),
    loadNetwork: options.loadNetwork
  })
  applyIfCurrent(options, result.data)
  void result.confirmation?.then((outcome) => {
    if (outcome.data) applyIfCurrent(options, outcome.data)
  })
  return result.data
}

function applyIfCurrent<T>(options: AuthorizationOptionResourceOptions<T>, value: T): void {
  if (options.isCurrent()) options.apply(value)
}

function authorizationOptionScope(isManagementView: boolean, targetSystemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    targetSystemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
