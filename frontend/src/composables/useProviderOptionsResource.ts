import { api } from '@/api/client'
import type { ProviderDefinition, ProviderListItem, ProviderOption } from '@/types/domain'

interface ProviderOptionsResourceOptions {
  apply?: (providers: ProviderDefinition[]) => void
  force?: boolean
  includeDisabled?: boolean
  includeDefinitions?: boolean
  listItemsOnly?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  systemAccountId?: string
  viewScope?: 'admin' | 'self'
}

export interface ProviderOptionsResourceResult {
  state: 'ready'
  data: ProviderDefinition[]
}

export async function loadProviderOptionsResource(options: ProviderOptionsResourceOptions): Promise<ProviderOptionsResourceResult> {
  const includeDisabled = options.includeDisabled === true && options.isManagementView
  const data = await (includeDisabled
    ? (options.listItemsOnly
        ? api.providers.listItems(providerResourceParams(options)).then((items) => items.map(providerListItemToDefinition))
        : api.providers.list(providerResourceParams(options)))
    : options.listItemsOnly
      ? api.providers.listItems(providerResourceParams(options)).then((items) => items.map(providerListItemToDefinition))
      : options.includeDefinitions
        ? api.providers.definitions(providerResourceParams(options))
        : api.providers.options(providerResourceParams(options)).then((items) => items.map(providerOptionToDefinition)))
  applyIfCurrent(options, data)
  return { state: 'ready', data }
}

function providerResourceParams(options: ProviderOptionsResourceOptions): { systemAccountId?: string; viewScope?: 'admin' | 'self' } | undefined {
  if (!options.systemAccountId && !options.viewScope) return undefined
  return { systemAccountId: options.systemAccountId, viewScope: options.viewScope }
}

function providerListItemToDefinition(item: ProviderListItem): ProviderDefinition {
  return {
    ...item,
    defaultProtocolProfileId: '',
    protocolVersion: '',
    protocolProfiles: []
  }
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
