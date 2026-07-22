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

export interface ProviderOptionsResourceResult {
  state: 'ready'
  data: ProviderDefinition[]
}

export async function loadProviderOptionsResource(options: ProviderOptionsResourceOptions): Promise<ProviderOptionsResourceResult> {
  const includeDisabled = options.includeDisabled === true && options.isManagementView
  const data = await (includeDisabled
    ? api.providers.list(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined)
    : options.includeDefinitions
      ? api.providers.definitions(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined)
      : api.providers.options(options.systemAccountId ? { systemAccountId: options.systemAccountId } : undefined).then((items) => items.map(providerOptionToDefinition)))
  applyIfCurrent(options, data)
  return { state: 'ready', data }
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
