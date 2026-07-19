import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { ProviderModelPricing, ProviderModelsParams } from '@/types/domain'
import type { PageDataLoadResult } from '@/shared/pageDataCache'

interface ProviderModelCatalogResourceOptions {
  force?: boolean
  isManagementView: boolean
  providerCode: string
  query?: ProviderModelsParams
}

const providerModelCatalogResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadProviderModelCatalogResource(
  options: ProviderModelCatalogResourceOptions
): Promise<PageDataLoadResult<ProviderModelPricing[]>> {
  const providerCode = options.providerCode.trim()
  const query = options.query ? { ...options.query } : undefined
  const systemAccountId = query?.systemAccountId?.trim() || undefined
  const route = `/providers/${providerCode}/models`
  const scope = providerModelCatalogScope(options.isManagementView, systemAccountId)
  if (options.force) await providerModelCatalogResourceCache.invalidate('providers.catalog', scope, route)
  return providerModelCatalogResourceCache.load<ProviderModelPricing[]>({
    cacheKey: { scope, route, query, version: 1 },
    domain: 'providers.catalog',
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && systemAccountId ? { targetSystemAccountId: systemAccountId } : {}),
    loadNetwork: () => api.providers.models(providerCode, query)
  })
}

function providerModelCatalogScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
