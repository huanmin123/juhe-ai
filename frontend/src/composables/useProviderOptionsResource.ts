import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { ProviderDefinition, ProviderOption } from '@/types/domain'

interface ProviderOptionsResourceOptions {
  apply?: (providers: ProviderDefinition[]) => void
  force?: boolean
  includeDisabled?: boolean
  includeDefinitions?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  systemAccountId?: string
}

const providerOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadProviderOptionsResource(options: ProviderOptionsResourceOptions): Promise<ProviderDefinition[]> {
  const includeDisabled = options.includeDisabled === true && options.isManagementView
  const route = includeDisabled ? '/providers' : options.includeDefinitions ? '/providers/definitions' : '/providers/options'
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
      : options.includeDefinitions
        ? api.providers.definitions(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined)
        : api.providers.options(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined).then((items) => items.map(providerOptionToDefinition))
  })
  applyIfCurrent(options, result.data)
  void result.confirmation?.then((outcome) => {
    if (outcome.data) applyIfCurrent(options, outcome.data)
  })
  return result.data
}

function providerOptionToDefinition(option: ProviderOption): ProviderDefinition {
  return {
    id: option.id,
    code: option.code,
    name: option.name,
    enabled: option.enabled,
    defaultProtocolProfileId: '',
    protocolCode: '',
    protocolVersion: '',
    baseUrl: '',
    defaultHealthCheckModel: '',
    defaultSupportedModels: [],
    accountTypes: [],
    capabilities: [],
    protocolProfiles: []
  }
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
