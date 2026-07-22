import { api } from '@/api/client'
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

export async function loadProviderOptionsResource(options: ProviderOptionsResourceOptions): Promise<ProviderDefinition[]> {
  const includeDisabled = options.includeDisabled === true && options.isManagementView
  const params = options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined
  const providers = includeDisabled
    ? await api.providers.list(params)
    : options.includeDefinitions
      ? await api.providers.definitions(params)
      : (await api.providers.options(params)).map(providerOptionToDefinition)
  applyIfCurrent(options, providers)
  return providers
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
