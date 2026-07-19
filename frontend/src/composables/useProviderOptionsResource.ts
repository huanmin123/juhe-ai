import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { ProviderDefinition } from '@/types/domain'

interface ProviderOptionsResourceOptions {
  apply?: (providers: ProviderDefinition[]) => void
  force?: boolean
  includeDisabled?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  systemAccountId?: string
}

const providerOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadProviderOptionsResource(options: ProviderOptionsResourceOptions): Promise<ProviderDefinition[]> {
  const includeDisabled = options.includeDisabled === true && options.isManagementView
  const route = includeDisabled ? '/providers' : '/providers/options'
  const scope = providerOptionsScope(options.isManagementView, options.systemAccountId)
  if (options.force) await providerOptionsResourceCache.invalidate('providers.catalog', scope, route)
  const result = await providerOptionsResourceCache.load<ProviderDefinition[]>({
    cacheKey: {
      scope,
      route,
      query: { includeDisabled, systemAccountId: options.systemAccountId },
      version: 1
    },
    domain: 'providers.catalog',
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && options.systemAccountId
      ? { targetSystemAccountId: options.systemAccountId }
      : {}),
    loadNetwork: () => includeDisabled
      ? api.providers.list(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined)
      : api.providers.options(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined)
  })
  applyIfCurrent(options, result.data)
  void result.confirmation?.then((outcome) => {
    if (outcome.data) applyIfCurrent(options, outcome.data)
  })
  return result.data
}

function applyIfCurrent(options: ProviderOptionsResourceOptions, providers: ProviderDefinition[]): void {
  if (options.isCurrent?.() === false) return
  options.apply?.(providers)
}

function providerOptionsScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
