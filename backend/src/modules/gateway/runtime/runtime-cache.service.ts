import { clearSharedJsonCacheInBackground, createAppCache, createSharedJsonCache } from '../../../shared/cache.js'
import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync } from '../../../shared/account-concurrency.js'
import {
  gatewayAccountConcurrencyAccountId,
  gatewayAccountConcurrencyAccountIds
} from '../dispatch/account-concurrency-identity.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { registerGatewayRuntimeCacheInvalidator, syncGatewayCacheInvalidationsFromRuntimeState } from '../../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { GatewayRequestEndpointFamily } from '../../../domain/types.js'
import { isDynamicRouteStrategyMode } from '../../../domain/route-strategy.js'
import { hashSecret } from '../../../storage/crypto.js'
import {
  listOpenAIAccountsForGroupResult,
  listOpenAIAccountsForGroupResultAsync,
  listRecoverableUnavailableOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  resolveGroupUsageAccessMetadataAsync,
  type GroupUsageAccessMetadata,
  type OpenAIAccountsForGroupResult,
  type OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { clearSettingsRepositoryCache } from '../../../storage/settings.repository.js'
import { clearDbServiceGatewayRuntimeCache } from '../../db-service/db-service-ipc.js'
import type { DbServiceGatewayRuntime } from '../../db-service/db-service-types.js'
import { readGatewaySettings, readGatewaySettingsAsync, type GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  listActiveResponseInspectionPoliciesForGateway,
  listActiveResponseInspectionPoliciesForGatewayAsync,
  type ResponseInspectionPolicySummary
} from '../../../storage/response-inspection-policy.repository.js'
import { listProviderModelCatalog, listProviderModelCatalogAsync, type ProviderModelCatalogItem } from '../../model-pricing/model-catalog.service.js'
import { orderGatewayApiKeyGroupBindingsForDispatchAsync } from '../routing/api-key-group-route-selector.service.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'

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
  const useLocalPostgres = shouldUseLocalPostgresGatewayRuntimeDataAccess()
  if (!useLocalPostgres && !shouldUseGatewayRuntimeDbService()) {
    return { ...readCachedGatewaySettings() }
  }
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = gatewaySettingsCache.get('current')
    if (cached) {
      return { ...cached }
    }
  }
  const sharedCached = await getGatewaySettingsSharedCacheEntry()
  if (sharedCached) {
    gatewaySettingsCache.set('current', cloneGatewaySettings(sharedCached))
    return cloneGatewaySettings(sharedCached)
  }
  const value = useLocalPostgres
    ? await readGatewaySettingsAsync()
    : await requestGatewayDbService({ type: 'read_gateway_settings' })
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
  const useLocalPostgres = shouldUseLocalPostgresGatewayRuntimeDataAccess()
  if (!useLocalPostgres && !shouldUseGatewayRuntimeDbService()) {
    return resolveCachedGroupUsageAccessMetadata(groupId, systemAccountId)
  }
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = groupUsageAccessCache.get(cacheKey)
    if (cached !== undefined) {
      if (!isGatewayRuntimeCacheEntryFresh(cached)) {
        if (!shouldAllowStaleGatewayRuntimeFallback()) {
          return await loadGroupUsageAccessMetadataAndPopulateCache(groupId, systemAccountId, cacheKey)
        }
        refreshGroupUsageAccessMetadataInBackground(groupId, systemAccountId, cacheKey)
      }
      return groupUsageAccessFromCacheEntry(cached)
    }
  }
  const sharedCached = await getGroupUsageAccessSharedCacheEntry(cacheKey)
  if (sharedCached !== undefined) {
    groupUsageAccessCache.set(cacheKey, cloneGroupUsageAccessCacheEntry(sharedCached), {
      ttlMs: groupUsageAccessRetainTtlMs
    })
    if (!isGatewayRuntimeCacheEntryFresh(sharedCached)) {
      if (!shouldAllowStaleGatewayRuntimeFallback()) {
        return await loadGroupUsageAccessMetadataAndPopulateCache(groupId, systemAccountId, cacheKey)
      }
      refreshGroupUsageAccessMetadataInBackground(groupId, systemAccountId, cacheKey)
    }
    return groupUsageAccessFromCacheEntry(sharedCached)
  }
  return await loadGroupUsageAccessMetadataAndPopulateCache(groupId, systemAccountId, cacheKey)
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
  const useLocalPostgres = shouldUseLocalPostgresGatewayRuntimeDataAccess()
  if (!useLocalPostgres && !shouldUseGatewayRuntimeDbService()) {
    return listCachedOpenAIAccountsForGroup(groupId, systemAccountId, options)
  }
  const cacheKey = gatewayOpenAIAccountsCacheKey(groupId, systemAccountId, options.requestedModel, options.requestedEndpointFamily)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      if (!shouldAllowStaleGatewayRuntimeFallback()) {
        return await loadOpenAIAccountsForGroupAndPopulateCache(groupId, systemAccountId, cacheKey, options.requestedModel, options.requestedEndpointFamily)
      }
      refreshOpenAIAccountsForGroupInBackground(groupId, systemAccountId, cacheKey, options.requestedModel, options.requestedEndpointFamily)
    }
    return await cloneOpenAIAccountsWithCurrentConcurrencyAsync(cached.accounts)
  }
  return await loadOpenAIAccountsForGroupAndPopulateCache(groupId, systemAccountId, cacheKey, options.requestedModel, options.requestedEndpointFamily)
}

export async function listFreshOpenAIAccountsForGroupAsync(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions = {}
): Promise<OpenAIAccountSecret[]> {
  const result = shouldUseGatewayRuntimeDbService()
    ? await requestGatewayDbService({
        type: 'list_openai_accounts_for_group_result',
        groupId,
        systemAccountId,
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? await listOpenAIAccountsForGroupResultAsync(groupId, systemAccountId, {
          requestedModel: options.requestedModel,
          requestedEndpointFamily: options.requestedEndpointFamily
        })
      : listOpenAIAccountsForGroupResult(groupId, systemAccountId, {
          requestedModel: options.requestedModel,
          requestedEndpointFamily: options.requestedEndpointFamily
        })
  return await cloneOpenAIAccountsWithCurrentConcurrencyAsync(result.accounts)
}

export async function listRecoverableUnavailableOpenAIAccountsForGroupAsync(
  groupId: string,
  systemAccountId: string,
  options: CachedOpenAIAccountsForGroupOptions & { windowMs?: number } = {}
): Promise<OpenAIAccountSecret[]> {
  const accounts = shouldUseGatewayRuntimeDbService()
    ? await requestGatewayDbService({
        type: 'list_recoverable_unavailable_openai_accounts_for_group',
        groupId,
        systemAccountId,
        requestedModel: options.requestedModel,
        requestedEndpointFamily: options.requestedEndpointFamily,
        windowMs: options.windowMs
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? recoverableUnavailableOpenAIAccountsFromResult(await listOpenAIAccountsForGroupResultAsync(groupId, systemAccountId, {
          requestedModel: options.requestedModel,
          requestedEndpointFamily: options.requestedEndpointFamily,
          includeUnavailable: true
        }), options.windowMs)
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
  const useLocalPostgres = shouldUseLocalPostgresGatewayRuntimeDataAccess()
  if (!useLocalPostgres && !shouldUseGatewayRuntimeDbService()) {
    return listProviderModelCatalog(input)
  }
  const cacheKey = [
    input.providerCode,
    input.systemAccountId ?? '',
    input.includeInactive === true ? 'inactive' : 'active',
    input.includeUnpriced === true ? 'unpriced' : 'priced'
  ].join(':')
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = providerModelCatalogCache.get(cacheKey)
    if (cached) {
      return cached.map((item) => ({ ...item }))
    }
  }
  const sharedCached = await getProviderModelCatalogSharedCacheEntry(cacheKey)
  if (sharedCached) {
    providerModelCatalogCache.set(cacheKey, sharedCached.map((item) => ({ ...item })))
    return sharedCached.map((item) => ({ ...item }))
  }
  const value = useLocalPostgres
    ? await listProviderModelCatalogAsync(input)
    : await requestGatewayDbService({
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
  let cached = runtimeConfig.cacheDriver !== 'redis'
    ? providerModelRouteIndexCache.get(cacheKey)
    : undefined
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
    await setProviderModelRouteIndexSharedCacheEntry(cacheKey, cached)
    providerModelRouteIndexCache.set(cacheKey, cloneProviderModelRouteIndexCacheEntry(cached))
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
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = responseInspectionPolicyCache.get(cacheKey)
    if (cached) {
      if (!isGatewayRuntimeCacheEntryFresh(cached)) {
        if (!shouldAllowStaleGatewayRuntimeFallback()) {
          return await loadActiveResponseInspectionPoliciesAndPopulateCache(input, cacheKey)
        }
        refreshActiveResponseInspectionPoliciesInBackground(input, cacheKey)
      }
      return cached.policies.map(cloneResponseInspectionPolicy)
    }
  }
  const sharedCached = await getResponseInspectionPolicySharedCacheEntry(cacheKey)
  if (sharedCached) {
    responseInspectionPolicyCache.set(cacheKey, responseInspectionPolicyCacheEntry(sharedCached.policies, Date.now(), sharedCached.revalidateAtMs), {
      ttlMs: responseInspectionPolicyRetainTtlMs
    })
    if (!isGatewayRuntimeCacheEntryFresh(sharedCached)) {
      if (!shouldAllowStaleGatewayRuntimeFallback()) {
        return await loadActiveResponseInspectionPoliciesAndPopulateCache(input, cacheKey)
      }
      refreshActiveResponseInspectionPoliciesInBackground(input, cacheKey)
    }
    return sharedCached.policies.map(cloneResponseInspectionPolicy)
  }
  return await loadActiveResponseInspectionPoliciesAndPopulateCache(input, cacheKey)
}

export async function listCachedActiveResponseInspectionPoliciesForAccountsAsync(
  accounts: readonly Pick<OpenAIAccountSecret, 'protocolCode' | 'providerCode'>[]
): Promise<ResponseInspectionPolicySummary[]> {
  const profileKeys = new Set<string>()
  const profileScopes: Array<{ protocolCode: string; providerCode?: string }> = []
  const policiesById = new Map<string, ResponseInspectionPolicySummary>()
  for (const account of accounts) {
    const protocolCode = account.protocolCode?.trim()
    if (!protocolCode) {
      continue
    }
    const providerCode = account.providerCode?.trim() || undefined
    const key = `${protocolCode}:${providerCode ?? ''}`
    if (profileKeys.has(key)) {
      continue
    }
    profileKeys.add(key)
    profileScopes.push({
      protocolCode,
      providerCode
    })
  }
  const policyGroups = await Promise.all(profileScopes.map((scope) =>
    listCachedActiveResponseInspectionPoliciesAsync(scope)))
  for (const policies of policyGroups) {
    for (const policy of policies) {
      policiesById.set(policy.id, policy)
    }
  }
  return [...policiesById.values()].map(cloneResponseInspectionPolicy)
}

export async function readCachedGatewayRuntimeAsync(apiKey: string): Promise<DbServiceGatewayRuntime> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const cacheKey = hashSecret(apiKey)
  const cached = gatewayRuntimeCache.get(cacheKey)
  if (cached !== undefined) {
    if (isGatewayRuntimeCacheEntryFresh(cached)) {
      const runtime = sanitizedGatewayRuntimeForDispatch(cached.runtime)
      if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
        return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
      }
      return runtime.apiKey ? await cloneGatewayRuntimeForDispatchAsync(runtime) : cloneStaticGatewayRuntime(runtime)
    }
    if (!shouldAllowStaleGatewayRuntimeFallback()) {
      const runtime = await loadGatewayRuntimeOnce(apiKey, cacheKey)
      if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
        return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
      }
      return runtime.apiKey ? await cloneGatewayRuntimeForDispatchAsync(runtime) : cloneStaticGatewayRuntime(runtime)
    }
    refreshGatewayRuntimeInBackground(apiKey, cacheKey)
    const runtime = sanitizedGatewayRuntimeForDispatch(cached.runtime)
    if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
      return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
    }
    return runtime.apiKey ? await cloneGatewayRuntimeForDispatchAsync(runtime) : cloneStaticGatewayRuntime(runtime)
  }

  const runtime = await loadGatewayRuntimeOnce(apiKey, cacheKey)
  if (runtime.apiKey && isDynamicRouteStrategyMode(runtime.apiKey.route_strategy_mode)) {
    return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
  }
  return runtime.apiKey ? await cloneGatewayRuntimeForDispatchAsync(runtime) : cloneStaticGatewayRuntime(runtime)
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
  if (shouldUseGatewayRuntimeDbService()) {
    throw new Error(`${runtimeConfig.processRole} 角色在 ${runtimeConfig.databaseDriver} 模式禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}

function shouldUseLocalPostgresGatewayRuntimeDataAccess(): boolean {
  return runtimeConfig.databaseDriver === 'postgres' && runtimeConfig.processRole === 'db-service'
}

function shouldUseGatewayRuntimeDbService(): boolean {
  if (runtimeConfig.processRole === 'server') return true
  return runtimeConfig.databaseDriver === 'postgres' && runtimeConfig.processRole !== 'db-service'
}

function shouldAllowStaleGatewayRuntimeFallback(): boolean {
  return runtimeConfig.databaseDriver !== 'postgres'
}

async function loadGroupUsageAccessMetadataAndPopulateCache(
  groupId: string,
  systemAccountId: string,
  cacheKey: string
): Promise<GroupUsageAccessMetadata | undefined> {
  const value = shouldUseGatewayRuntimeDbService()
    ? await requestGatewayDbService({
        type: 'resolve_group_usage_access',
        groupId,
        systemAccountId
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? await resolveGroupUsageAccessMetadataAsync(groupId, systemAccountId)
      : resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  await setGroupUsageAccessCacheEntryAsync(cacheKey, groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false))
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

async function loadOpenAIAccountsForGroupAndPopulateCache(
  groupId: string,
  systemAccountId: string,
  cacheKey: string,
  requestedModel?: string,
  requestedEndpointFamily?: GatewayRequestEndpointFamily
): Promise<OpenAIAccountSecret[]> {
  const result = shouldUseGatewayRuntimeDbService()
    ? await requestGatewayDbService({
        type: 'list_openai_accounts_for_group_result',
        groupId,
        systemAccountId,
        requestedModel,
        requestedEndpointFamily
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? await listOpenAIAccountsForGroupResultAsync(groupId, systemAccountId, {
          requestedModel,
          requestedEndpointFamily
        })
      : listOpenAIAccountsForGroupResult(groupId, systemAccountId, {
          requestedModel,
          requestedEndpointFamily
        })
  const accounts = result.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(accounts), {
    ttlMs: openAIAccountsRetainTtlMs
  })
  return await cloneOpenAIAccountsWithCurrentConcurrencyAsync(result.accounts)
}

async function loadActiveResponseInspectionPoliciesAndPopulateCache(
  input: {
    protocolCode: string
    providerCode?: string
  },
  cacheKey: string
): Promise<ResponseInspectionPolicySummary[]> {
  const value = shouldUseGatewayRuntimeDbService()
    ? await requestGatewayDbService({
        type: 'list_active_response_inspection_policies',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? await listActiveResponseInspectionPoliciesForGatewayAsync(input)
      : listActiveResponseInspectionPoliciesForGateway(input)
  const policies = value.map(cloneResponseInspectionPolicy)
  await setResponseInspectionPolicyCacheEntryAsync(cacheKey, responseInspectionPolicyCacheEntry(policies))
  return policies.map(cloneResponseInspectionPolicy)
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
  const runtime = await requestGatewayDbService({
    type: 'read_gateway_runtime',
    key: apiKey,
    skipDynamicRouteSelection: true
  }, {
    timeoutMs: gatewayRuntimeDbServiceTimeoutMs
  })
  if (gatewayRuntimeCacheGeneration === generation) {
    await populateGatewayRuntimeCaches(cacheKey, runtime)
  }
  return runtime
}

async function populateGatewayRuntimeCaches(cacheKey: string, runtime: DbServiceGatewayRuntime): Promise<void> {
  if (!runtime.apiKey) {
    gatewayRuntimeCache.set(cacheKey, {
      runtime: cloneStaticGatewayRuntime(runtime),
      revalidateAtMs: Date.now() + invalidGatewayRuntimeTtlMs
    }, { ttlMs: gatewayRuntimeRetainTtlMs })
    await setGatewaySettingsCacheEntryAsync(runtime.settings)
    return
  }

  const nowMs = Date.now()
  const runtimeTtlMs = gatewayRuntimeCacheTtlMs(runtime, nowMs)
  gatewayRuntimeCache.set(cacheKey, {
    runtime: cloneStaticGatewayRuntime(runtime),
    revalidateAtMs: nowMs + runtimeTtlMs
  }, { ttlMs: gatewayRuntimeRetainTtlMs })
  await setGatewaySettingsCacheEntryAsync(runtime.settings)
  if (runtime.groupAccess) {
    const cacheKey = gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id)
    const entry = groupUsageAccessCacheEntry(cloneGroupUsageAccessMetadata(runtime.groupAccess), nowMs)
    await setGroupUsageAccessCacheEntryAsync(cacheKey, entry)
  }
  if (runtime.groupAccess) {
    const accounts = runtime.accounts.map(cloneStaticOpenAIAccountSecret)
    openAIAccountsCache.set(gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id), openAIAccountsCacheEntry(accounts, nowMs), {
      ttlMs: openAIAccountsRetainTtlMs
    })
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
  const concurrency = loadAccountCurrentConcurrencyByIds(gatewayAccountConcurrencyAccountIds(accounts))
  const output: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    const cloned = cloneOpenAIAccountSecretForDispatch(account)
    if (!cloned) {
      continue
    }
    output.push({
      ...cloned,
      currentConcurrency: concurrency.get(gatewayAccountConcurrencyAccountId(account)) ?? 0
    })
  }
  return output
}

async function cloneOpenAIAccountsWithCurrentConcurrencyAsync(accounts: OpenAIAccountSecret[]): Promise<OpenAIAccountSecret[]> {
  const concurrency = await loadAccountCurrentConcurrencyByIdsAsync(gatewayAccountConcurrencyAccountIds(accounts))
  const output: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    const cloned = cloneOpenAIAccountSecretForDispatch(account)
    if (!cloned) {
      continue
    }
    output.push({
      ...cloned,
      currentConcurrency: concurrency.get(gatewayAccountConcurrencyAccountId(account)) ?? 0
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
  const settings = cloneGatewaySettings(runtime.settings)
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
  return {
    ...settings,
    streamCircuitBreakerEnabled: true
  }
}

function cloneStaticGatewayRuntime(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    apiKey: runtime.apiKey ? cloneGatewayApiKeyRow(runtime.apiKey) : undefined,
    accountDispatchDiagnostics: runtime.accountDispatchDiagnostics ? { ...runtime.accountDispatchDiagnostics } : undefined,
    settings: cloneGatewaySettings(runtime.settings),
    groupAccess: runtime.groupAccess ? cloneGroupUsageAccessMetadata(runtime.groupAccess) : undefined,
    accounts: runtime.accounts.map(cloneStaticOpenAIAccountSecret),
    responseInspectionPolicies: runtime.responseInspectionPolicies ? runtime.responseInspectionPolicies.map(cloneResponseInspectionPolicy) : undefined
  }
}

async function cloneGatewayRuntimeForDispatchAsync(runtime: DbServiceGatewayRuntime): Promise<DbServiceGatewayRuntime> {
  return {
    ...cloneStaticGatewayRuntime(runtime),
    accounts: await cloneOpenAIAccountsWithCurrentConcurrencyAsync(runtime.accounts)
  }
}

async function routeCachedDynamicGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime): Promise<DbServiceGatewayRuntime> {
  if (!runtime.apiKey) {
    return cloneStaticGatewayRuntime(runtime)
  }
  const systemAccountId = runtime.apiKey.system_account_id
  const orderedBindings = await orderGatewayApiKeyGroupBindingsForDispatchAsync(runtime.apiKey)
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
    const responseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesForAccountsAsync(accounts)
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

function recoverableUnavailableOpenAIAccountsFromResult(
  result: OpenAIAccountsForGroupResult,
  windowMs: number | undefined
): OpenAIAccountSecret[] {
  const nowMs = Date.now()
  const latestRecoverableAtMs = nowMs + normalizeRecoverableUnavailableWindowMs(windowMs)
  return result.accounts.filter((account) => {
    const cooldownUntilMs = accountRecoverableCooldownUntilMs(account)
    if (cooldownUntilMs === undefined || cooldownUntilMs > latestRecoverableAtMs) {
      return false
    }
    if (account.status === 'active') {
      return cooldownUntilMs > nowMs
    }
    return account.status === 'temporary_unavailable' || account.status === 'rate_limited'
  })
}

function normalizeRecoverableUnavailableWindowMs(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, Math.trunc(value))
    : 30_000
}

function accountRecoverableCooldownUntilMs(account: OpenAIAccountSecret): number | undefined {
  if (!account.cooldownUntil) {
    return undefined
  }
  const cooldownUntilMs = Date.parse(account.cooldownUntil)
  return Number.isFinite(cooldownUntilMs) ? cooldownUntilMs : undefined
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
  if (apiKey.status !== 'active') return false
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
    }), '网关运行配置后台刷新失败，单机模式保留当前内存快照')
  })
}

function refreshGroupUsageAccessMetadataInBackground(groupId: string, systemAccountId: string, cacheKey: string): void {
  if (pendingGroupUsageAccessRefreshes.has(cacheKey)) {
    return
  }
  const refresh = (shouldUseGatewayRuntimeDbService()
    ? requestGatewayDbService({ type: 'resolve_group_usage_access', groupId, systemAccountId })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? resolveGroupUsageAccessMetadataAsync(groupId, systemAccountId)
      : Promise.resolve(resolveGroupUsageAccessMetadata(groupId, systemAccountId)))
    .then(async (value) => {
      const entry = groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false)
      groupUsageAccessCache.set(cacheKey, entry, { ttlMs: groupUsageAccessRetainTtlMs })
      await setGroupUsageAccessSharedCacheEntry(cacheKey, entry)
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_group_access_stale_refresh_failed',
        groupId,
        systemAccountId
      }), '网关分组访问元数据后台刷新失败，单机模式保留当前内存快照')
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
  const refresh = (shouldUseGatewayRuntimeDbService()
    ? requestGatewayDbService({ type: 'list_openai_accounts_for_group_result', groupId, systemAccountId, requestedModel, requestedEndpointFamily })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? listOpenAIAccountsForGroupResultAsync(groupId, systemAccountId, { requestedModel, requestedEndpointFamily })
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
      }), '网关候选账号后台刷新失败，单机模式保留当前内存快照')
    })
    .finally(() => {
      pendingOpenAIAccountsRefreshes.delete(cacheKey)
    })
  pendingOpenAIAccountsRefreshes.set(cacheKey, refresh)
}

async function getGatewaySettingsSharedCacheEntry(): Promise<GatewaySettings | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const cached = await gatewaySettingsSharedCache.get('current')
  return cached ? cloneGatewaySettings(cached) : undefined
}

async function setGatewaySettingsCacheEntryAsync(settings: GatewaySettings): Promise<void> {
  const cached = cloneGatewaySettings(settings)
  await setGatewaySettingsSharedCacheEntry(cached)
  gatewaySettingsCache.set('current', cached)
}

async function setGatewaySettingsSharedCacheEntry(settings: GatewaySettings): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await gatewaySettingsSharedCache.set('current', cloneGatewaySettings(settings), { ttlMs: gatewaySettingsTtlMs })
}

function clearGatewaySettingsSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    gatewaySettingsSharedCache,
    'gateway_settings_shared_cache_clear_failed',
    '网关设置 Redis shared cache 清理失败'
  )
}

async function getGroupUsageAccessSharedCacheEntry(cacheKey: string): Promise<GroupUsageAccessCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const cached = await groupUsageAccessSharedCache.get(cacheKey)
  return cached ? cloneGroupUsageAccessCacheEntry(cached) : undefined
}

async function setGroupUsageAccessCacheEntryAsync(cacheKey: string, entry: GroupUsageAccessCacheEntry): Promise<void> {
  const cachedEntry = cloneGroupUsageAccessCacheEntry(entry)
  await setGroupUsageAccessSharedCacheEntry(cacheKey, cachedEntry)
  groupUsageAccessCache.set(cacheKey, cachedEntry, { ttlMs: groupUsageAccessRetainTtlMs })
}

async function setGroupUsageAccessSharedCacheEntry(cacheKey: string, entry: GroupUsageAccessCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await groupUsageAccessSharedCache.set(cacheKey, cloneGroupUsageAccessCacheEntry(entry), {
    ttlMs: groupUsageAccessRetainTtlMs
  })
}

function clearGroupUsageAccessSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    groupUsageAccessSharedCache,
    'gateway_group_usage_access_shared_cache_clear_failed',
    '网关分组访问 Redis shared cache 清理失败'
  )
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
  const refresh = (shouldUseGatewayRuntimeDbService()
    ? requestGatewayDbService({
        type: 'list_active_response_inspection_policies',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      })
    : shouldUseLocalPostgresGatewayRuntimeDataAccess()
      ? listActiveResponseInspectionPoliciesForGatewayAsync(input)
      : Promise.resolve(listActiveResponseInspectionPoliciesForGateway(input)))
    .then(async (value) => {
      const entry = responseInspectionPolicyCacheEntry(value.map(cloneResponseInspectionPolicy))
      await setResponseInspectionPolicySharedCacheEntry(cacheKey, entry)
      responseInspectionPolicyCache.set(cacheKey, entry, { ttlMs: responseInspectionPolicyRetainTtlMs })
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_response_inspection_policy_stale_refresh_failed',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      }), '网关响应检查策略后台刷新失败，单机模式保留当前内存快照')
    })
    .finally(() => {
      pendingResponseInspectionPolicyRefreshes.delete(cacheKey)
    })
  pendingResponseInspectionPolicyRefreshes.set(cacheKey, refresh)
}

async function getProviderModelCatalogSharedCacheEntry(cacheKey: string): Promise<ProviderModelCatalogItem[] | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const cached = await providerModelCatalogSharedCache.get(cacheKey)
  return cached ? cached.map((item) => ({ ...item })) : undefined
}

async function setProviderModelCatalogCacheEntryAsync(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  const cached = value.map((item) => ({ ...item }))
  await setProviderModelCatalogSharedCacheEntry(cacheKey, cached)
  providerModelCatalogCache.set(cacheKey, cached.map((item) => ({ ...item })))
}

async function setProviderModelCatalogSharedCacheEntry(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await providerModelCatalogSharedCache.set(cacheKey, value.map((item) => ({ ...item })), { ttlMs: providerModelCatalogTtlMs })
}

function clearProviderModelCatalogSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    providerModelCatalogSharedCache,
    'gateway_provider_model_catalog_shared_cache_clear_failed',
    '网关模型目录 Redis shared cache 清理失败'
  )
}

async function getProviderModelRouteIndexSharedCacheEntry(cacheKey: string): Promise<ProviderModelRouteIndexCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const cached = await providerModelRouteIndexSharedCache.get(cacheKey)
  return cached ? providerModelRouteIndexCacheEntryFromShared(cached) : undefined
}

async function setProviderModelRouteIndexSharedCacheEntry(cacheKey: string, entry: ProviderModelRouteIndexCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await providerModelRouteIndexSharedCache.set(cacheKey, providerModelRouteIndexCacheEntryToShared(entry), { ttlMs: providerModelCatalogTtlMs })
}

function clearProviderModelRouteIndexSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    providerModelRouteIndexSharedCache,
    'gateway_provider_model_route_index_shared_cache_clear_failed',
    '网关模型路由索引 Redis shared cache 清理失败'
  )
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
  const cached = await responseInspectionPolicySharedCache.get(cacheKey)
  return cached
    ? responseInspectionPolicyCacheEntry(cached.policies, Date.now(), cached.revalidateAtMs)
    : undefined
}

async function setResponseInspectionPolicyCacheEntryAsync(cacheKey: string, entry: ResponseInspectionPolicyCacheEntry): Promise<void> {
  const cachedEntry = responseInspectionPolicyCacheEntry(entry.policies, Date.now(), entry.revalidateAtMs)
  await setResponseInspectionPolicySharedCacheEntry(cacheKey, cachedEntry)
  responseInspectionPolicyCache.set(cacheKey, cachedEntry, { ttlMs: responseInspectionPolicyRetainTtlMs })
}

async function setResponseInspectionPolicySharedCacheEntry(cacheKey: string, entry: ResponseInspectionPolicyCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await responseInspectionPolicySharedCache.set(cacheKey, responseInspectionPolicyCacheEntry(entry.policies, Date.now(), entry.revalidateAtMs), {
    ttlMs: responseInspectionPolicyRetainTtlMs
  })
}

function clearResponseInspectionPolicySharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    responseInspectionPolicySharedCache,
    'gateway_response_inspection_policy_shared_cache_clear_failed',
    '网关响应检查策略 Redis shared cache 清理失败'
  )
}

registerGatewayRuntimeCacheInvalidator(clearGatewayRuntimeCache)
