import { createAppCache, createSharedJsonCache } from '../../../shared/cache.js'
import { loadAccountCurrentConcurrencyByIds } from '../../../shared/account-concurrency.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { registerGatewayRuntimeCacheInvalidator, syncGatewayCacheInvalidationsFromRuntimeState } from '../../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { GatewayRequestEndpointFamily } from '../../../domain/types.js'
import { isDynamicRouteStrategyMode } from '../../../domain/route-strategy.js'
import { hashSecret } from '../../../storage/crypto.js'
import {
  listOpenAIAccountsForGroupResult,
  listRecoverableUnavailableOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { clearSettingsRepositoryCache } from '../../../storage/settings.repository.js'
import { clearDbServiceGatewayRuntimeCache, requestDbService } from '../../db-service/db-service-ipc.js'
import type { DbServiceGatewayRuntime } from '../../db-service/db-service-types.js'
import { readGatewaySettings, type GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  listActiveResponseInspectionPoliciesForGateway,
  type ResponseInspectionPolicySummary
} from '../../../storage/response-inspection-policy.repository.js'
import { listProviderModelCatalog, type ProviderModelCatalogItem } from '../../model-pricing/model-catalog.service.js'
import { orderGatewayApiKeyGroupBindingsForDispatch } from '../routing/api-key-group-route-selector.service.js'

const gatewayRuntimeTtlMs = 60_000
const gatewayRuntimeRetainTtlMs = 10 * 60_000
const invalidGatewayRuntimeTtlMs = 10_000
const gatewaySettingsTtlMs = 60_000
const groupUsageAccessTtlMs = 60_000
const groupUsageAccessRetainTtlMs = 10 * 60_000
const openAIAccountsTtlMs = 60_000
const openAIAccountsRetainTtlMs = 10 * 60_000
const providerModelCatalogTtlMs = 60_000
const responseInspectionPolicyRetainTtlMs = 10 * 60_000
export const gatewayRuntimeDbServiceTimeoutMs = 10_000

interface GatewayRuntimeCacheEntry {
  runtime: DbServiceGatewayRuntime
  revalidateAtMs?: number
}

interface GroupUsageAccessCacheEntry {
  value: GroupUsageAccessMetadata | false
  revalidateAtMs?: number
}

interface OpenAIAccountsCacheEntry {
  accounts: OpenAIAccountSecret[]
  revalidateAtMs?: number
}

interface ResponseInspectionPolicyCacheEntry {
  policies: ResponseInspectionPolicySummary[]
  revalidateAtMs?: number
}

interface ProviderModelRouteIndexCacheEntry {
  index: Map<string, string[]>
}

interface ProviderModelRouteIndexSharedCacheEntry {
  entries: Array<[string, string[]]>
}

interface CachedOpenAIAccountsForGroupOptions {
  requestedModel?: string
  requestedEndpointFamily?: GatewayRequestEndpointFamily
}

export type ProviderModelRouteResolution =
  | {
      outcome: 'matched'
      modelKey: string
      providerCode: string
      matchedProviderCodes: string[]
    }
  | {
      outcome: 'missing' | 'ambiguous'
      modelKey: string
      matchedProviderCodes: string[]
    }

const gatewayRuntimeCache = createAppCache<string, GatewayRuntimeCacheEntry>({
  name: 'gateway:runtime',
  max: 10000,
  ttlMs: gatewayRuntimeRetainTtlMs,
  updateAgeOnGet: true
})

const gatewaySettingsCache = createAppCache<string, GatewaySettings>({
  name: 'gateway:settings',
  max: 1,
  ttlMs: gatewaySettingsTtlMs
})

const gatewaySettingsSharedCache = createSharedJsonCache<GatewaySettings>({
  name: 'gateway:settings',
  max: 1,
  ttlMs: gatewaySettingsTtlMs
})

const groupUsageAccessCache = createAppCache<string, GroupUsageAccessCacheEntry>({
  name: 'gateway:group-usage-access',
  max: 1000,
  ttlMs: groupUsageAccessRetainTtlMs
})

const groupUsageAccessSharedCache = createSharedJsonCache<GroupUsageAccessCacheEntry>({
  name: 'gateway:group-usage-access',
  max: 1000,
  ttlMs: groupUsageAccessRetainTtlMs
})

const openAIAccountsCache = createAppCache<string, OpenAIAccountsCacheEntry>({
  name: 'gateway:openai-accounts',
  max: 1000,
  ttlMs: openAIAccountsRetainTtlMs
})

const providerModelCatalogCache = createAppCache<string, ProviderModelCatalogItem[]>({
  name: 'gateway:provider-model-catalog',
  max: 1000,
  ttlMs: providerModelCatalogTtlMs
})

const providerModelCatalogSharedCache = createSharedJsonCache<ProviderModelCatalogItem[]>({
  name: 'gateway:provider-model-catalog',
  max: 1000,
  ttlMs: providerModelCatalogTtlMs
})

const providerModelRouteIndexCache = createAppCache<string, ProviderModelRouteIndexCacheEntry>({
  name: 'gateway:provider-model-route-index',
  max: 1000,
  ttlMs: providerModelCatalogTtlMs
})

const providerModelRouteIndexSharedCache = createSharedJsonCache<ProviderModelRouteIndexSharedCacheEntry>({
  name: 'gateway:provider-model-route-index',
  max: 1000,
  ttlMs: providerModelCatalogTtlMs
})

const responseInspectionPolicyCache = createAppCache<string, ResponseInspectionPolicyCacheEntry>({
  name: 'gateway:response-inspection-policies',
  max: 100,
  ttlMs: responseInspectionPolicyRetainTtlMs
})

const responseInspectionPolicySharedCache = createSharedJsonCache<ResponseInspectionPolicyCacheEntry>({
  name: 'gateway:response-inspection-policies',
  max: 100,
  ttlMs: responseInspectionPolicyRetainTtlMs
})

const pendingGatewayRuntimeLoads = new Map<string, Promise<DbServiceGatewayRuntime>>()
const pendingGroupUsageAccessRefreshes = new Map<string, Promise<void>>()
const pendingOpenAIAccountsRefreshes = new Map<string, Promise<void>>()
const pendingResponseInspectionPolicyRefreshes = new Map<string, Promise<void>>()
let gatewayRuntimeCacheGeneration = 0

export function readCachedGatewaySettings(): GatewaySettings {
  assertLocalGatewayDatabaseAccess('readCachedGatewaySettings')
  const cached = gatewaySettingsCache.get('current')
  if (cached) {
    return cached
  }
  const value = readGatewaySettings()
  gatewaySettingsCache.set('current', value)
  return value
}

export async function readCachedGatewaySettingsAsync(): Promise<GatewaySettings> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  if (runtimeConfig.processRole !== 'server') {
    return { ...readCachedGatewaySettings() }
  }
  const cached = gatewaySettingsCache.get('current')
  if (cached) {
    return { ...cached }
  }
  const sharedCached = await getGatewaySettingsSharedCacheEntry()
  if (sharedCached) {
    gatewaySettingsCache.set('current', cloneGatewaySettings(sharedCached))
    return cloneGatewaySettings(sharedCached)
  }
  const value = await requestDbService({ type: 'read_gateway_settings' })
  await setGatewaySettingsCacheEntryAsync(value)
  return cloneGatewaySettings(value)
}

export function resolveCachedGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  assertLocalGatewayDatabaseAccess('resolveCachedGroupUsageAccessMetadata')
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = groupUsageAccessCache.get(cacheKey)
  if (cached !== undefined) {
    return groupUsageAccessFromCacheEntry(cached)
  }
  const value = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  groupUsageAccessCache.set(cacheKey, groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false), {
    ttlMs: groupUsageAccessRetainTtlMs
  })
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export async function resolveCachedGroupUsageAccessMetadataAsync(groupId: string, systemAccountId: string): Promise<GroupUsageAccessMetadata | undefined> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  if (runtimeConfig.processRole !== 'server') {
    return resolveCachedGroupUsageAccessMetadata(groupId, systemAccountId)
  }
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = groupUsageAccessCache.get(cacheKey)
  if (cached !== undefined) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      refreshGroupUsageAccessMetadataInBackground(groupId, systemAccountId, cacheKey)
    }
    return groupUsageAccessFromCacheEntry(cached)
  }
  const sharedCached = await getGroupUsageAccessSharedCacheEntry(cacheKey)
  if (sharedCached !== undefined) {
    groupUsageAccessCache.set(cacheKey, cloneGroupUsageAccessCacheEntry(sharedCached), {
      ttlMs: groupUsageAccessRetainTtlMs
    })
    if (!isGatewayRuntimeCacheEntryFresh(sharedCached)) {
      refreshGroupUsageAccessMetadataInBackground(groupId, systemAccountId, cacheKey)
    }
    return groupUsageAccessFromCacheEntry(sharedCached)
  }
  const value = await requestDbService({
    type: 'resolve_group_usage_access',
    groupId,
    systemAccountId
  })
  await setGroupUsageAccessCacheEntryAsync(cacheKey, groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false))
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export function listCachedOpenAIAccountsForGroup(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions = {}
): OpenAIAccountSecret[] {
  assertLocalGatewayDatabaseAccess('listCachedOpenAIAccountsForGroup')
  const cacheKey = gatewayOpenAIAccountsCacheKey(groupId, systemAccountId, options.requestedModel, options.requestedEndpointFamily)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cloneOpenAIAccountsWithCurrentConcurrency(cached.accounts)
  }
  const value = listOpenAIAccountsForGroupResult(groupId, systemAccountId, {
    requestedModel: options.requestedModel,
    requestedEndpointFamily: options.requestedEndpointFamily
  })
  const accounts = value.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(accounts), {
    ttlMs: openAIAccountsRetainTtlMs
  })
  return cloneOpenAIAccountsWithCurrentConcurrency(value.accounts)
}

export async function listCachedOpenAIAccountsForGroupAsync(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions = {}
): Promise<OpenAIAccountSecret[]> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  if (runtimeConfig.processRole !== 'server') {
    return listCachedOpenAIAccountsForGroup(groupId, systemAccountId, options)
  }
  const cacheKey = gatewayOpenAIAccountsCacheKey(groupId, systemAccountId, options.requestedModel, options.requestedEndpointFamily)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      refreshOpenAIAccountsForGroupInBackground(groupId, systemAccountId, cacheKey, options.requestedModel, options.requestedEndpointFamily)
    }
    return cloneOpenAIAccountsWithCurrentConcurrency(cached.accounts)
  }
  const result = await requestDbService({
    type: 'list_openai_accounts_for_group_result',
    groupId,
    systemAccountId,
    requestedModel: options.requestedModel,
    requestedEndpointFamily: options.requestedEndpointFamily
  })
  const accounts = result.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(accounts), {
    ttlMs: openAIAccountsRetainTtlMs
  })
  return cloneOpenAIAccountsWithCurrentConcurrency(result.accounts)
}

export async function listFreshOpenAIAccountsForGroupAsync(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions = {}
): Promise<OpenAIAccountSecret[]> {
  const result = runtimeConfig.processRole === 'server'
    ? await requestDbService({
        type: 'list_openai_accounts_for_group_result',
        groupId,
        systemAccountId,
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily
      })
    : listOpenAIAccountsForGroupResult(groupId, systemAccountId, {
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily
      })
  return cloneOpenAIAccountsWithCurrentConcurrency(result.accounts)
}

export async function listRecoverableUnavailableOpenAIAccountsForGroupAsync(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions & { windowMs?: number } = {}
): Promise<OpenAIAccountSecret[]> {
  const accounts = runtimeConfig.processRole === 'server'
    ? await requestDbService({
        type: 'list_recoverable_unavailable_openai_accounts_for_group',
        groupId,
        systemAccountId,
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily,
        windowMs: options.windowMs
      })
    : listRecoverableUnavailableOpenAIAccountsForGroup(groupId, systemAccountId, {
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily,
        windowMs: options.windowMs
      })
  return accounts.map(cloneStaticOpenAIAccountSecret)
}

export async function listCachedProviderModelCatalogAsync(input: {
  providerCode: string
  systemAccountId?: string
  includeInactive?: boolean
  includeUnpriced?: boolean
}): Promise<ProviderModelCatalogItem[]> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  if (runtimeConfig.processRole !== 'server') {
    return listProviderModelCatalog(input)
  }
  const cacheKey = [
    input.providerCode,
    input.systemAccountId ?? '',
    input.includeInactive === true ? 'inactive' : 'active',
    input.includeUnpriced === true ? 'unpriced' : 'priced'
  ].join(':')
  const cached = providerModelCatalogCache.get(cacheKey)
  if (cached) {
    return cached.map((item) => ({ ...item }))
  }
  const sharedCached = await getProviderModelCatalogSharedCacheEntry(cacheKey)
  if (sharedCached) {
    providerModelCatalogCache.set(cacheKey, sharedCached.map((item) => ({ ...item })))
    return sharedCached.map((item) => ({ ...item }))
  }
  const value = await requestDbService({
    type: 'list_provider_model_catalog',
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeInactive: input.includeInactive,
    includeUnpriced: input.includeUnpriced
  })
  await setProviderModelCatalogCacheEntryAsync(cacheKey, value.map((item) => ({ ...item })))
  return value.map((item) => ({ ...item }))
}

export async function resolveCachedProviderModelRouteAsync(input: {
  model: string
  providerCodes: string[]
  systemAccountId?: string
  includeUnpriced?: boolean
}): Promise<ProviderModelRouteResolution> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const modelKey = normalizeProviderModelRouteKey(input.model)
  const providerCodes = normalizedProviderRouteCodes(input.providerCodes)
  if (!modelKey || !providerCodes.length) {
    return { outcome: 'missing', modelKey, matchedProviderCodes: [] }
  }
  const cacheKey = providerModelRouteIndexCacheKey({
    providerCodes,
    systemAccountId: input.systemAccountId,
    includeUnpriced: input.includeUnpriced
  })
  let cached = providerModelRouteIndexCache.get(cacheKey)
  if (!cached) {
    const sharedCached = await getProviderModelRouteIndexSharedCacheEntry(cacheKey)
    if (sharedCached) {
      cached = sharedCached
      providerModelRouteIndexCache.set(cacheKey, cloneProviderModelRouteIndexCacheEntry(sharedCached))
    }
  }
  if (!cached) {
    cached = providerModelRouteIndexCacheEntry(await buildProviderModelRouteIndex({
      providerCodes,
      systemAccountId: input.systemAccountId,
      includeUnpriced: input.includeUnpriced
    }))
    providerModelRouteIndexCache.set(cacheKey, cloneProviderModelRouteIndexCacheEntry(cached))
    await setProviderModelRouteIndexSharedCacheEntry(cacheKey, cached)
  }
  const matchedProviderCodes = cached.index.get(modelKey) ?? []
  if (matchedProviderCodes.length === 1) {
    return {
      outcome: 'matched',
      modelKey,
      providerCode: matchedProviderCodes[0]!,
      matchedProviderCodes
    }
  }
  return {
    outcome: matchedProviderCodes.length > 1 ? 'ambiguous' : 'missing',
    modelKey,
    matchedProviderCodes
  }
}

export async function listCachedActiveResponseInspectionPoliciesAsync(input: {
  protocolCode: string
  providerCode?: string
}): Promise<ResponseInspectionPolicySummary[]> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const cacheKey = responseInspectionPolicyCacheKey(input.protocolCode, input.providerCode)
  const cached = responseInspectionPolicyCache.get(cacheKey)
  if (cached) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      refreshActiveResponseInspectionPoliciesInBackground(input, cacheKey)
    }
    return cached.policies.map(cloneResponseInspectionPolicy)
  }
  const sharedCached = await getResponseInspectionPolicySharedCacheEntry(cacheKey)
  if (sharedCached) {
    responseInspectionPolicyCache.set(cacheKey, responseInspectionPolicyCacheEntry(sharedCached.policies, Date.now(), sharedCached.revalidateAtMs), {
      ttlMs: responseInspectionPolicyRetainTtlMs
    })
    if (!isGatewayRuntimeCacheEntryFresh(sharedCached)) {
      refreshActiveResponseInspectionPoliciesInBackground(input, cacheKey)
    }
    return sharedCached.policies.map(cloneResponseInspectionPolicy)
  }
  const value = runtimeConfig.processRole !== 'server'
    ? listActiveResponseInspectionPoliciesForGateway(input)
    : await requestDbService({
        type: 'list_active_response_inspection_policies',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      })
  await setResponseInspectionPolicyCacheEntryAsync(cacheKey, responseInspectionPolicyCacheEntry(value.map(cloneResponseInspectionPolicy)))
  return value.map(cloneResponseInspectionPolicy)
}

export async function readCachedGatewayRuntimeAsync(apiKey: string): Promise<DbServiceGatewayRuntime> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const cacheKey = hashSecret(apiKey)
  const cached = gatewayRuntimeCache.get(cacheKey)
  if (cached !== undefined) {
    if (isGatewayRuntimeCacheEntryFresh(cached)) {
      if (isDynamicRouteStrategyMode(cached.runtime.apiKey?.route_strategy_mode)) {
        return await routeCachedDynamicGatewayRuntimeForDispatch(cached.runtime)
      }
      return cloneGatewayRuntimeForDispatch(cached.runtime)
    }
    refreshGatewayRuntimeInBackground(apiKey, cacheKey)
    const runtime = sanitizedGatewayRuntimeForDispatch(cached.runtime)
    if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
      return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
    }
    return runtime.apiKey ? cloneGatewayRuntimeForDispatch(runtime) : cloneStaticGatewayRuntime(runtime)
  }

  const runtime = await loadGatewayRuntimeOnce(apiKey, cacheKey)
  if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
    return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
  }
  return runtime.apiKey ? cloneGatewayRuntimeForDispatch(runtime) : cloneStaticGatewayRuntime(runtime)
}

export function clearGatewayRuntimeCache(reason?: string): void {
  clearGatewayRuntimeCacheLocal({
    clearSettings: shouldClearSettingsCacheForGatewayInvalidation(reason)
  })
  if (runtimeConfig.processRole === 'server') {
    clearDbServiceGatewayRuntimeCache()
    return
  }
  if (runtimeConfig.processRole === 'db-service') {
    clearDbServiceGatewayRuntimeCache()
    return
  }
  if (runtimeConfig.processRole === 'worker' && process.send) {
    process.send({ type: 'gateway_runtime_cache_invalidate' })
  }
}

export function clearGatewayRuntimeCacheLocal(options: { clearSettings?: boolean } = {}): void {
  gatewayRuntimeCacheGeneration += 1
  pendingGatewayRuntimeLoads.clear()
  pendingGroupUsageAccessRefreshes.clear()
  pendingOpenAIAccountsRefreshes.clear()
  pendingResponseInspectionPolicyRefreshes.clear()
  gatewayRuntimeCache.clear()
  gatewaySettingsCache.clear()
  groupUsageAccessCache.clear()
  openAIAccountsCache.clear()
  providerModelCatalogCache.clear()
  providerModelRouteIndexCache.clear()
  responseInspectionPolicyCache.clear()
  clearGatewaySettingsSharedCache()
  clearGroupUsageAccessSharedCache()
  clearProviderModelCatalogSharedCache()
  clearProviderModelRouteIndexSharedCache()
  clearResponseInspectionPolicySharedCache()
  if (options.clearSettings ?? true) {
    clearSettingsRepositoryCache()
  }
}

function shouldClearSettingsCacheForGatewayInvalidation(reason: string | undefined): boolean {
  return !reason || reason === 'settings_updated'
}

function gatewayCacheKey(groupId: string, systemAccountId: string): string {
  return `${groupId}:${systemAccountId}`
}

function gatewayOpenAIAccountsCacheKey(
  groupId: string,
  systemAccountId: string,
  requestedModel?: string,
  requestedEndpointFamily?: string
): string {
  const modelKey = normalizeProviderModelRouteKey(requestedModel)
  return modelKey
    ? `${gatewayCacheKey(groupId, systemAccountId)}:model:${modelKey}:endpoint:${requestedEndpointFamily ?? 'any'}`
    : gatewayCacheKey(groupId, systemAccountId)
}

function responseInspectionPolicyCacheKey(protocolCode: string, providerCode?: string): string {
  return `${protocolCode}:${providerCode ?? ''}`
}

function providerModelRouteIndexCacheKey(input: {
  providerCodes: string[]
  systemAccountId?: string
  includeUnpriced?: boolean
}): string {
  return [
    input.systemAccountId ?? '',
    input.includeUnpriced === true ? 'unpriced' : 'priced',
    input.providerCodes.join(',')
  ].join(':')
}

async function buildProviderModelRouteIndex(input: {
  providerCodes: string[]
  systemAccountId?: string
  includeUnpriced?: boolean
}): Promise<Map<string, string[]>> {
  const providerCodesByModel = new Map<string, Set<string>>()
  for (const providerCode of input.providerCodes) {
    const catalog = await listCachedProviderModelCatalogAsync({
      providerCode,
      systemAccountId: input.systemAccountId,
      includeUnpriced: input.includeUnpriced
    })
    for (const item of catalog) {
      const modelKey = normalizeProviderModelRouteKey(item.model)
      if (!modelKey) {
        continue
      }
      let providerCodes = providerCodesByModel.get(modelKey)
      if (!providerCodes) {
        providerCodes = new Set<string>()
        providerCodesByModel.set(modelKey, providerCodes)
      }
      providerCodes.add(providerCode)
    }
  }
  const index = new Map<string, string[]>()
  for (const [modelKey, providerCodes] of providerCodesByModel.entries()) {
    index.set(modelKey, [...providerCodes].sort((left, right) => left.localeCompare(right)))
  }
  return index
}

function providerModelRouteIndexCacheEntry(index: Map<string, string[]>): ProviderModelRouteIndexCacheEntry {
  return {
    index: new Map([...index.entries()].map(([modelKey, providerCodes]) => [modelKey, [...providerCodes]]))
  }
}

function normalizedProviderRouteCodes(providerCodes: string[]): string[] {
  return [...new Set(providerCodes.map(providerCode => providerCode.trim()).filter(Boolean))]
    .sort((left, right) => left.localeCompare(right))
}

function normalizeProviderModelRouteKey(model: string | null | undefined): string {
  return typeof model === 'string' ? model.trim() : ''
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}

async function loadGatewayRuntimeOnce(apiKey: string, cacheKey: string): Promise<DbServiceGatewayRuntime> {
  const pending = pendingGatewayRuntimeLoads.get(cacheKey)
  if (pending) {
    return await pending
  }

  const generation = gatewayRuntimeCacheGeneration
  const load = loadGatewayRuntimeAndPopulateCaches(apiKey, cacheKey, generation)
  pendingGatewayRuntimeLoads.set(cacheKey, load)
  try {
    return await load
  } finally {
    if (pendingGatewayRuntimeLoads.get(cacheKey) === load) {
      pendingGatewayRuntimeLoads.delete(cacheKey)
    }
  }
}

async function loadGatewayRuntimeAndPopulateCaches(
  apiKey: string,
  cacheKey: string,
  generation: number
): Promise<DbServiceGatewayRuntime> {
  const runtime = await requestDbService({
    type: 'read_gateway_runtime',
    key: apiKey,
    skipDynamicRouteSelection: true
  }, {
    timeoutMs: gatewayRuntimeDbServiceTimeoutMs
  })
  if (gatewayRuntimeCacheGeneration === generation) {
    populateGatewayRuntimeCaches(cacheKey, runtime)
  }
  return runtime
}

function populateGatewayRuntimeCaches(cacheKey: string, runtime: DbServiceGatewayRuntime): void {
  if (!runtime.apiKey) {
    gatewayRuntimeCache.set(cacheKey, {
      runtime: cloneStaticGatewayRuntime(runtime),
      revalidateAtMs: Date.now() + invalidGatewayRuntimeTtlMs
    }, { ttlMs: gatewayRuntimeRetainTtlMs })
    setGatewaySettingsCacheEntry(runtime.settings)
    return
  }

  const nowMs = Date.now()
  const runtimeTtlMs = gatewayRuntimeCacheTtlMs(runtime, nowMs)
  gatewayRuntimeCache.set(cacheKey, {
    runtime: cloneStaticGatewayRuntime(runtime),
    revalidateAtMs: nowMs + runtimeTtlMs
  }, { ttlMs: gatewayRuntimeRetainTtlMs })
  setGatewaySettingsCacheEntry(runtime.settings)
  if (runtime.groupAccess) {
    const cacheKey = gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id)
    const entry = groupUsageAccessCacheEntry(cloneGroupUsageAccessMetadata(runtime.groupAccess), nowMs)
    groupUsageAccessCache.set(cacheKey, entry, {
      ttlMs: groupUsageAccessRetainTtlMs
    })
    void setGroupUsageAccessSharedCacheEntry(cacheKey, entry)
  }
  if (runtime.groupAccess) {
    const accounts = runtime.accounts.map(cloneStaticOpenAIAccountSecret)
    openAIAccountsCache.set(gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id), openAIAccountsCacheEntry(accounts, nowMs), {
      ttlMs: openAIAccountsRetainTtlMs
    })
  }
  if (runtime.groupAccess && runtime.responseInspectionPolicies) {
    responseInspectionPolicyCache.set(
      responseInspectionPolicyCacheKey(runtime.groupAccess.protocolCode, runtime.groupAccess.providerCode),
      responseInspectionPolicyCacheEntry(runtime.responseInspectionPolicies.map(cloneResponseInspectionPolicy), nowMs),
      { ttlMs: responseInspectionPolicyRetainTtlMs }
    )
    void setResponseInspectionPolicySharedCacheEntry(
      responseInspectionPolicyCacheKey(runtime.groupAccess.protocolCode, runtime.groupAccess.providerCode),
      responseInspectionPolicyCacheEntry(runtime.responseInspectionPolicies.map(cloneResponseInspectionPolicy), nowMs)
    )
  }
}

function cloneStaticOpenAIAccountSecret(account: OpenAIAccountSecret): OpenAIAccountSecret {
  return {
    ...account,
    currentConcurrency: undefined,
    supportedEndpointModes: account.supportedEndpointModes ? [...account.supportedEndpointModes] : undefined,
    supportedModels: [...(account.supportedModels ?? [])],
    apiKeys: account.apiKeys ? [...account.apiKeys] : undefined,
    apiKeyRuntimeStates: account.apiKeyRuntimeStates ? account.apiKeyRuntimeStates.map((state) => ({ ...state })) : undefined,
    selectedApiKeyFingerprint: undefined,
    selectedApiKeyIndex: undefined,
    modelMappings: (account.modelMappings ?? []).map((mapping) => ({ ...mapping })),
    credentials: { ...account.credentials }
  }
}

function cloneOpenAIAccountsWithCurrentConcurrency(accounts: OpenAIAccountSecret[]): OpenAIAccountSecret[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  const output: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    const cloned = cloneOpenAIAccountSecretForDispatch(account)
    if (!cloned) {
      continue
    }
    output.push({
      ...cloned,
      currentConcurrency: concurrency.get(account.id) ?? 0
    })
  }
  return output
}

function cloneOpenAIAccountSecretForDispatch(account: OpenAIAccountSecret): OpenAIAccountSecret | undefined {
  if (!isOpenAIAccountRuntimeUsableAt(account)) {
    return undefined
  }
  return cloneStaticOpenAIAccountSecret(account)
}

function sanitizedGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime, now = Date.now()): DbServiceGatewayRuntime {
  const settings = { ...runtime.settings }
  if (!runtime.apiKey || !isGatewayApiKeyRuntimeUsableAt(runtime.apiKey, now)) {
    return {
      settings,
      accounts: []
    }
  }
  if (runtime.groupAccess && !isGroupUsageAccessRuntimeUsableAt(runtime.groupAccess, now)) {
    return {
      settings,
      accounts: []
    }
  }
  return {
    apiKey: cloneGatewayApiKeyRow(runtime.apiKey),
    settings,
    groupAccess: runtime.groupAccess ? cloneGroupUsageAccessMetadata(runtime.groupAccess) : undefined,
    accountDispatchDiagnostics: runtime.accountDispatchDiagnostics ? { ...runtime.accountDispatchDiagnostics } : undefined,
    accounts: runtime.accounts
      .filter((account) => isOpenAIAccountRuntimeUsableAt(account, now))
      .map(cloneStaticOpenAIAccountSecret),
    responseInspectionPolicies: runtime.responseInspectionPolicies ? runtime.responseInspectionPolicies.map(cloneResponseInspectionPolicy) : undefined
  }
}

function cloneGroupUsageAccessMetadata(value: GroupUsageAccessMetadata): GroupUsageAccessMetadata {
  return {
    ...value,
    schedulingPolicy: value.schedulingPolicy ? { ...value.schedulingPolicy } : undefined
  }
}

function cloneGatewaySettings(settings: GatewaySettings): GatewaySettings {
  return { ...settings }
}

function cloneStaticGatewayRuntime(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    apiKey: runtime.apiKey ? cloneGatewayApiKeyRow(runtime.apiKey) : undefined,
    accountDispatchDiagnostics: runtime.accountDispatchDiagnostics ? { ...runtime.accountDispatchDiagnostics } : undefined,
    settings: { ...runtime.settings },
    groupAccess: runtime.groupAccess ? cloneGroupUsageAccessMetadata(runtime.groupAccess) : undefined,
    accounts: runtime.accounts.map(cloneStaticOpenAIAccountSecret),
    responseInspectionPolicies: runtime.responseInspectionPolicies ? runtime.responseInspectionPolicies.map(cloneResponseInspectionPolicy) : undefined
  }
}

function cloneGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    ...cloneStaticGatewayRuntime(runtime),
    accounts: cloneOpenAIAccountsWithCurrentConcurrency(runtime.accounts)
  }
}

async function routeCachedDynamicGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime): Promise<DbServiceGatewayRuntime> {
  if (!runtime.apiKey) {
    return cloneStaticGatewayRuntime(runtime)
  }
  const systemAccountId = runtime.apiKey.system_account_id
  const orderedBindings = orderGatewayApiKeyGroupBindingsForDispatch(runtime.apiKey)
  const uniqueCandidateGroupIds = [...new Set(orderedBindings.map((binding) => binding.group_id).filter(Boolean))]
  const apiKey = {
    ...cloneGatewayApiKeyRow(runtime.apiKey),
    group_bindings: orderedBindings.length ? orderedBindings.map((binding) => ({ ...binding })) : runtime.apiKey.group_bindings?.map((binding) => ({ ...binding }))
  }

  for (const groupId of uniqueCandidateGroupIds) {
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
    if (!groupAccess) {
      continue
    }
    const accounts = await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId)
    if (!hasDispatchableCachedGatewayAccount(accounts) && uniqueCandidateGroupIds.length > 1) {
      continue
    }
    const responseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesAsync({
      protocolCode: groupAccess.protocolCode,
      providerCode: groupAccess.providerCode
    })
    return {
      apiKey: {
        ...apiKey,
        selected_group_id: groupId
      },
      settings: { ...runtime.settings },
      groupAccess,
      accounts,
      responseInspectionPolicies
    }
  }

  return {
    apiKey,
    settings: { ...runtime.settings },
    accounts: [],
    responseInspectionPolicies: []
  }
}

function hasDispatchableCachedGatewayAccount(accounts: OpenAIAccountSecret[]): boolean {
  return accounts.some((account) => account.status === 'active' && account.proxyProfileUnavailable !== true)
}

function cloneResponseInspectionPolicy(policy: ResponseInspectionPolicySummary): ResponseInspectionPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}

function cloneGatewayApiKeyRow(apiKey: NonNullable<DbServiceGatewayRuntime['apiKey']>): NonNullable<DbServiceGatewayRuntime['apiKey']> {
  return {
    ...apiKey,
    hybrid_routing_config: apiKey.hybrid_routing_config
      ? {
        ...apiKey.hybrid_routing_config,
        levelRoutes: apiKey.hybrid_routing_config.levelRoutes.map((route) => ({ ...route }))
      }
      : undefined,
    group_bindings: apiKey.group_bindings
      ? apiKey.group_bindings.map((binding) => ({ ...binding }))
      : undefined
  }
}

function isGatewayRuntimeCacheEntryFresh(entry: { revalidateAtMs?: number }, now = Date.now()): boolean {
  return entry.revalidateAtMs === undefined || entry.revalidateAtMs > now
}

function groupUsageAccessCacheEntry(value: GroupUsageAccessMetadata | false, now = Date.now()): GroupUsageAccessCacheEntry {
  return {
    value,
    revalidateAtMs: value ? now + groupUsageAccessCacheTtlMs(value, now) : now + invalidGatewayRuntimeTtlMs
  }
}

function cloneGroupUsageAccessCacheEntry(entry: GroupUsageAccessCacheEntry): GroupUsageAccessCacheEntry {
  return {
    value: entry.value ? cloneGroupUsageAccessMetadata(entry.value) : false,
    revalidateAtMs: entry.revalidateAtMs
  }
}

function cloneProviderModelRouteIndexCacheEntry(entry: ProviderModelRouteIndexCacheEntry): ProviderModelRouteIndexCacheEntry {
  return providerModelRouteIndexCacheEntry(entry.index)
}

function openAIAccountsCacheEntry(accounts: OpenAIAccountSecret[], now = Date.now()): OpenAIAccountsCacheEntry {
  return {
    accounts: accounts.map(cloneStaticOpenAIAccountSecret),
    revalidateAtMs: now + openAIAccountsCacheTtlMs(now, accounts)
  }
}

function responseInspectionPolicyCacheEntry(
  policies: ResponseInspectionPolicySummary[],
  now = Date.now(),
  revalidateAtMs = now + gatewayRuntimeTtlMs
): ResponseInspectionPolicyCacheEntry {
  return {
    policies: policies.map(cloneResponseInspectionPolicy),
    revalidateAtMs
  }
}

function groupUsageAccessFromCacheEntry(entry: GroupUsageAccessCacheEntry, now = Date.now()): GroupUsageAccessMetadata | undefined {
  if (!entry.value) return undefined
  if (!isGroupUsageAccessRuntimeUsableAt(entry.value, now)) return undefined
  return cloneGroupUsageAccessMetadata(entry.value)
}

function gatewayRuntimeCacheTtlMs(runtime: DbServiceGatewayRuntime, now = Date.now()): number {
  let ttlMs = gatewayRuntimeTtlMs
  for (const expiresAt of runtimeCacheExpiryCandidates(runtime)) {
    const expiresAtMs = Date.parse(expiresAt)
    if (!Number.isFinite(expiresAtMs)) {
      continue
    }
    ttlMs = Math.min(ttlMs, expiresAtMs - now)
  }
  return Math.max(1, ttlMs)
}

function groupUsageAccessCacheTtlMs(value: GroupUsageAccessMetadata, now = Date.now()): number {
  return ttlBoundedByIsoExpiries(groupUsageAccessTtlMs, [value.groupAuthorizationExpiresAt], now)
}

function openAIAccountsCacheTtlMs(now = Date.now(), accounts: OpenAIAccountSecret[] = []): number {
  const ttlMs = openAIAccountsTtlMs
  return ttlBoundedByIsoExpiries(ttlMs, accounts.flatMap((account) => [
    account.accountExpiresAt,
    account.expiresAt,
    account.accountAuthorizationExpiresAt,
    account.groupAuthorizationExpiresAt
  ]), now)
}

function ttlBoundedByIsoExpiries(baseTtlMs: number, expiresAtValues: Array<string | undefined>, now = Date.now()): number {
  let ttlMs = baseTtlMs
  for (const expiresAt of expiresAtValues) {
    if (!expiresAt) continue
    const expiresAtMs = Date.parse(expiresAt)
    if (!Number.isFinite(expiresAtMs)) continue
    ttlMs = Math.min(ttlMs, expiresAtMs - now)
  }
  return Math.max(1, ttlMs)
}

function isGatewayApiKeyRuntimeUsableAt(apiKey: DbServiceGatewayRuntime['apiKey'], now = Date.now()): boolean {
  if (!apiKey) return false
  if (apiKey.status !== 'active' || apiKey.availability_schedule_active !== 1) return false
  return !isoTimeExpired(apiKey.expires_at ?? undefined, now)
}

function isGroupUsageAccessRuntimeUsableAt(groupAccess: GroupUsageAccessMetadata, now = Date.now()): boolean {
  return !isoTimeExpired(groupAccess.groupAuthorizationExpiresAt, now)
}

function isOpenAIAccountRuntimeUsableAt(account: OpenAIAccountSecret, now = Date.now()): boolean {
  if (account.status !== 'active') return false
  return !isoTimeExpired(account.accountExpiresAt, now)
    && !isoTimeExpired(account.expiresAt, now)
    && !isoTimeExpired(account.accountAuthorizationExpiresAt, now)
    && !isoTimeExpired(account.groupAuthorizationExpiresAt, now)
}

function isoTimeExpired(value: string | undefined, now = Date.now()): boolean {
  if (!value) return false
  const expiresAtMs = Date.parse(value)
  return Number.isFinite(expiresAtMs) && expiresAtMs <= now
}

function runtimeCacheExpiryCandidates(runtime: DbServiceGatewayRuntime): string[] {
  const candidates = [
    runtime.apiKey?.expires_at ?? undefined,
    runtime.groupAccess?.groupAuthorizationExpiresAt
  ]
  for (const account of runtime.accounts) {
    candidates.push(
      account.accountExpiresAt,
      account.expiresAt,
      account.accountAuthorizationExpiresAt,
      account.groupAuthorizationExpiresAt
    )
  }
  return candidates.filter((value): value is string => Boolean(value))
}

function refreshGatewayRuntimeInBackground(apiKey: string, cacheKey: string): void {
  if (pendingGatewayRuntimeLoads.has(cacheKey)) {
    return
  }
  void loadGatewayRuntimeOnce(apiKey, cacheKey).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_runtime_stale_refresh_failed'
    }), '网关运行配置后台刷新失败，当前请求继续使用内存快照')
  })
}

function refreshGroupUsageAccessMetadataInBackground(groupId: string, systemAccountId: string, cacheKey: string): void {
  if (pendingGroupUsageAccessRefreshes.has(cacheKey)) {
    return
  }
  const refresh = (runtimeConfig.processRole === 'server'
    ? requestDbService({ type: 'resolve_group_usage_access', groupId, systemAccountId })
    : Promise.resolve(resolveGroupUsageAccessMetadata(groupId, systemAccountId)))
    .then((value) => {
      const entry = groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false)
      groupUsageAccessCache.set(cacheKey, entry, { ttlMs: groupUsageAccessRetainTtlMs })
      void setGroupUsageAccessSharedCacheEntry(cacheKey, entry)
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_group_access_stale_refresh_failed',
        groupId,
        systemAccountId
      }), '网关分组访问元数据后台刷新失败，当前请求继续使用内存快照')
    })
    .finally(() => {
      pendingGroupUsageAccessRefreshes.delete(cacheKey)
    })
  pendingGroupUsageAccessRefreshes.set(cacheKey, refresh)
}

function refreshOpenAIAccountsForGroupInBackground(
  groupId: string,
  systemAccountId: string,
  cacheKey: string,
  requestedModel?: string,
  requestedEndpointFamily?: GatewayRequestEndpointFamily
): void {
  if (pendingOpenAIAccountsRefreshes.has(cacheKey)) {
    return
  }
  const refresh = (runtimeConfig.processRole === 'server'
    ? requestDbService({ type: 'list_openai_accounts_for_group_result', groupId, systemAccountId, requestedModel, requestedEndpointFamily })
    : Promise.resolve(listOpenAIAccountsForGroupResult(groupId, systemAccountId, { requestedModel, requestedEndpointFamily })))
    .then((result) => {
      openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(result.accounts.map(cloneStaticOpenAIAccountSecret)), {
        ttlMs: openAIAccountsRetainTtlMs
      })
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_accounts_stale_refresh_failed',
        groupId,
        systemAccountId
      }), '网关候选账号后台刷新失败，当前请求继续使用内存快照')
    })
    .finally(() => {
      pendingOpenAIAccountsRefreshes.delete(cacheKey)
    })
  pendingOpenAIAccountsRefreshes.set(cacheKey, refresh)
}

async function getGatewaySettingsSharedCacheEntry(): Promise<GatewaySettings | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await gatewaySettingsSharedCache.get('current')
    return cached ? cloneGatewaySettings(cached) : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_settings_shared_cache_read_failed'
    }), '读取网关设置 Redis 共享缓存失败，继续读取 DB service')
    return undefined
  }
}

function setGatewaySettingsCacheEntry(settings: GatewaySettings): void {
  const cached = cloneGatewaySettings(settings)
  gatewaySettingsCache.set('current', cached)
  void setGatewaySettingsSharedCacheEntry(cached)
}

async function setGatewaySettingsCacheEntryAsync(settings: GatewaySettings): Promise<void> {
  const cached = cloneGatewaySettings(settings)
  gatewaySettingsCache.set('current', cached)
  await setGatewaySettingsSharedCacheEntry(cached)
}

async function setGatewaySettingsSharedCacheEntry(settings: GatewaySettings): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await gatewaySettingsSharedCache.set('current', cloneGatewaySettings(settings), { ttlMs: gatewaySettingsTtlMs })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_settings_shared_cache_write_failed'
    }), '写入网关设置 Redis 共享缓存失败')
  }
}

function clearGatewaySettingsSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void gatewaySettingsSharedCache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_settings_shared_cache_clear_failed'
    }), '清理网关设置 Redis 共享缓存失败')
  })
}

async function getGroupUsageAccessSharedCacheEntry(cacheKey: string): Promise<GroupUsageAccessCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await groupUsageAccessSharedCache.get(cacheKey)
    return cached ? cloneGroupUsageAccessCacheEntry(cached) : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_group_usage_access_shared_cache_read_failed'
    }), '读取网关分组访问 Redis 共享缓存失败，继续读取 DB service')
    return undefined
  }
}

async function setGroupUsageAccessCacheEntryAsync(cacheKey: string, entry: GroupUsageAccessCacheEntry): Promise<void> {
  const cachedEntry = cloneGroupUsageAccessCacheEntry(entry)
  groupUsageAccessCache.set(cacheKey, cachedEntry, { ttlMs: groupUsageAccessRetainTtlMs })
  await setGroupUsageAccessSharedCacheEntry(cacheKey, cachedEntry)
}

async function setGroupUsageAccessSharedCacheEntry(cacheKey: string, entry: GroupUsageAccessCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await groupUsageAccessSharedCache.set(cacheKey, cloneGroupUsageAccessCacheEntry(entry), {
      ttlMs: groupUsageAccessRetainTtlMs
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_group_usage_access_shared_cache_write_failed'
    }), '写入网关分组访问 Redis 共享缓存失败')
  }
}

function clearGroupUsageAccessSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void groupUsageAccessSharedCache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_group_usage_access_shared_cache_clear_failed'
    }), '清理网关分组访问 Redis 共享缓存失败')
  })
}

function refreshActiveResponseInspectionPoliciesInBackground(
  input: {
    protocolCode: string
    providerCode?: string
  },
  cacheKey: string
): void {
  if (pendingResponseInspectionPolicyRefreshes.has(cacheKey)) {
    return
  }
  const refresh = (runtimeConfig.processRole === 'server'
    ? requestDbService({
        type: 'list_active_response_inspection_policies',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      })
    : Promise.resolve(listActiveResponseInspectionPoliciesForGateway(input)))
    .then((value) => {
      const entry = responseInspectionPolicyCacheEntry(value.map(cloneResponseInspectionPolicy))
      responseInspectionPolicyCache.set(cacheKey, entry, { ttlMs: responseInspectionPolicyRetainTtlMs })
      void setResponseInspectionPolicySharedCacheEntry(cacheKey, entry)
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_response_inspection_policy_stale_refresh_failed',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      }), '网关响应检查策略后台刷新失败，当前请求继续使用内存快照')
    })
    .finally(() => {
      pendingResponseInspectionPolicyRefreshes.delete(cacheKey)
    })
  pendingResponseInspectionPolicyRefreshes.set(cacheKey, refresh)
}

async function getProviderModelCatalogSharedCacheEntry(cacheKey: string): Promise<ProviderModelCatalogItem[] | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await providerModelCatalogSharedCache.get(cacheKey)
    return cached ? cached.map((item) => ({ ...item })) : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_catalog_shared_cache_read_failed'
    }), '读取网关模型目录 Redis 共享缓存失败，继续读取 DB service')
    return undefined
  }
}

async function setProviderModelCatalogCacheEntryAsync(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  const cached = value.map((item) => ({ ...item }))
  providerModelCatalogCache.set(cacheKey, cached.map((item) => ({ ...item })))
  await setProviderModelCatalogSharedCacheEntry(cacheKey, cached)
}

async function setProviderModelCatalogSharedCacheEntry(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await providerModelCatalogSharedCache.set(cacheKey, value.map((item) => ({ ...item })), { ttlMs: providerModelCatalogTtlMs })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_catalog_shared_cache_write_failed'
    }), '写入网关模型目录 Redis 共享缓存失败')
  }
}

function clearProviderModelCatalogSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void providerModelCatalogSharedCache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_catalog_shared_cache_clear_failed'
    }), '清理网关模型目录 Redis 共享缓存失败')
  })
}

async function getProviderModelRouteIndexSharedCacheEntry(cacheKey: string): Promise<ProviderModelRouteIndexCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await providerModelRouteIndexSharedCache.get(cacheKey)
    return cached ? providerModelRouteIndexCacheEntryFromShared(cached) : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_route_index_shared_cache_read_failed'
    }), '读取网关模型路由索引 Redis 共享缓存失败，继续构建本地索引')
    return undefined
  }
}

async function setProviderModelRouteIndexSharedCacheEntry(cacheKey: string, entry: ProviderModelRouteIndexCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await providerModelRouteIndexSharedCache.set(cacheKey, providerModelRouteIndexCacheEntryToShared(entry), { ttlMs: providerModelCatalogTtlMs })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_route_index_shared_cache_write_failed'
    }), '写入网关模型路由索引 Redis 共享缓存失败')
  }
}

function clearProviderModelRouteIndexSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void providerModelRouteIndexSharedCache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_provider_model_route_index_shared_cache_clear_failed'
    }), '清理网关模型路由索引 Redis 共享缓存失败')
  })
}

function providerModelRouteIndexCacheEntryFromShared(entry: ProviderModelRouteIndexSharedCacheEntry): ProviderModelRouteIndexCacheEntry {
  return providerModelRouteIndexCacheEntry(new Map(entry.entries.map(([modelKey, providerCodes]) => [modelKey, providerCodes])))
}

function providerModelRouteIndexCacheEntryToShared(entry: ProviderModelRouteIndexCacheEntry): ProviderModelRouteIndexSharedCacheEntry {
  return {
    entries: [...entry.index.entries()].map(([modelKey, providerCodes]) => [modelKey, [...providerCodes]])
  }
}

async function getResponseInspectionPolicySharedCacheEntry(cacheKey: string): Promise<ResponseInspectionPolicyCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await responseInspectionPolicySharedCache.get(cacheKey)
    return cached
      ? responseInspectionPolicyCacheEntry(cached.policies, Date.now(), cached.revalidateAtMs)
      : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_response_inspection_policy_shared_cache_read_failed'
    }), '读取网关响应检查策略 Redis 共享缓存失败，继续读取 DB service')
    return undefined
  }
}

async function setResponseInspectionPolicyCacheEntryAsync(cacheKey: string, entry: ResponseInspectionPolicyCacheEntry): Promise<void> {
  const cachedEntry = responseInspectionPolicyCacheEntry(entry.policies, Date.now(), entry.revalidateAtMs)
  responseInspectionPolicyCache.set(cacheKey, cachedEntry, { ttlMs: responseInspectionPolicyRetainTtlMs })
  await setResponseInspectionPolicySharedCacheEntry(cacheKey, cachedEntry)
}

async function setResponseInspectionPolicySharedCacheEntry(cacheKey: string, entry: ResponseInspectionPolicyCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await responseInspectionPolicySharedCache.set(cacheKey, responseInspectionPolicyCacheEntry(entry.policies, Date.now(), entry.revalidateAtMs), {
      ttlMs: responseInspectionPolicyRetainTtlMs
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_response_inspection_policy_shared_cache_write_failed'
    }), '写入网关响应检查策略 Redis 共享缓存失败')
  }
}

function clearResponseInspectionPolicySharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void responseInspectionPolicySharedCache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_response_inspection_policy_shared_cache_clear_failed'
    }), '清理网关响应检查策略 Redis 共享缓存失败')
  })
}

registerGatewayRuntimeCacheInvalidator(clearGatewayRuntimeCache)
