import { HYBRID_PROVIDER_CODE, normalizeProviderToken } from '../../domain/provider-protocol.js'
import type { ProviderDefinition } from '../../domain/types.js'
import { listProvidersAsync } from '../../storage/provider.repository.js'
import {
  listProviderModelCatalogAsync,
  type ProviderModelCatalogItem
} from './model-catalog.service.js'

export interface ListClientModelCatalogOptions {
  systemAccountId?: string
  providerCodes?: readonly string[]
}

export async function listClientModelCatalogAsync(
  options: ListClientModelCatalogOptions = {}
): Promise<ProviderModelCatalogItem[]> {
  const providers = options.providerCodes === undefined ? await listProvidersAsync() : []
  const providerCodes = resolveClientModelCatalogProviderCodes(providers, options.providerCodes)
  if (!providerCodes.length) return []

  const systemAccountId = options.systemAccountId?.trim()
  const catalogs = await Promise.all(providerCodes.map((providerCode) => listProviderModelCatalogAsync({
    providerCode,
    ...(systemAccountId ? { systemAccountId } : {})
  })))
  return selectClientModelCatalog(catalogs.flat())
}

export function resolveClientModelCatalogProviderCodes(
  providers: ReadonlyArray<Pick<ProviderDefinition, 'code' | 'enabled'>>,
  scopedProviderCodes?: readonly string[]
): string[] {
  const sourceCodes = scopedProviderCodes === undefined
    ? providers
        .filter((provider) => provider.enabled && normalizeProviderToken(provider.code) !== HYBRID_PROVIDER_CODE)
        .map((provider) => provider.code)
    : scopedProviderCodes

  return [...new Set(sourceCodes.map(normalizeProviderToken).filter((code): code is string => Boolean(code)))]
    .sort((left, right) => left.localeCompare(right, 'en'))
}

export function selectClientModelCatalog(
  items: readonly ProviderModelCatalogItem[]
): ProviderModelCatalogItem[] {
  const candidates = items
    .filter((item) => item.status === 'active')
    .filter((item) => item.scope !== 'built_in' || item.catalogVisible !== false)
    .filter(hasClientVisiblePrice)
    .sort(compareClientCatalogCandidates)

  const byModel = new Map<string, ProviderModelCatalogItem>()
  for (const item of candidates) {
    const model = item.model.trim()
    if (model && !byModel.has(model)) byModel.set(model, item)
  }
  return [...byModel.values()].sort(compareClientCatalogItems)
}

function compareClientCatalogCandidates(left: ProviderModelCatalogItem, right: ProviderModelCatalogItem): number {
  const scopeOrder = clientCatalogScopeRank(right) - clientCatalogScopeRank(left)
  if (scopeOrder !== 0) return scopeOrder
  return compareClientCatalogItems(left, right)
}

function compareClientCatalogItems(left: ProviderModelCatalogItem, right: ProviderModelCatalogItem): number {
  const dateOrder = releaseDateValue(right).localeCompare(releaseDateValue(left), 'en')
  if (dateOrder !== 0) return dateOrder
  const providerOrder = (normalizeProviderToken(left.providerCode) ?? '')
    .localeCompare(normalizeProviderToken(right.providerCode) ?? '', 'en')
  if (providerOrder !== 0) return providerOrder
  return left.model.localeCompare(right.model, 'en')
}

function clientCatalogScopeRank(item: ProviderModelCatalogItem): number {
  if (item.scope === 'personal') return 3
  if (item.scope === 'global') return 2
  return 1
}

function releaseDateValue(item: ProviderModelCatalogItem): string {
  const value = item.releaseDate?.trim()
  if (!value || !/^\d{4}-\d{2}-\d{2}(?:$|T)/.test(value)) return ''
  const normalized = value.slice(0, 10)
  return Number.isFinite(Date.parse(`${normalized}T00:00:00.000Z`)) ? normalized : ''
}

function hasClientVisiblePrice(item: ProviderModelCatalogItem): boolean {
  return item.inputUsdPer1M !== undefined
    || item.outputUsdPer1M !== undefined
    || item.cachedInputUsdPer1M !== undefined
    || item.cacheWriteUsdPer1M !== undefined
    || item.cacheWrite1hUsdPer1M !== undefined
    || item.cacheStorageUsdPer1MPerHour !== undefined
    || item.imageInputUsdPer1M !== undefined
    || item.imageOutputUsdPer1M !== undefined
    || item.audioInputUsdPer1M !== undefined
    || item.audioOutputUsdPer1M !== undefined
    || item.outputUsdPerImage !== undefined
    || Object.keys(item.serviceTierPrices ?? {}).length > 0
}
