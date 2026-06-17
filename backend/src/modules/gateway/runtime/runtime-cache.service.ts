import { createAppCache } from '../../../shared/cache.js'
import { loadAccountCurrentConcurrencyByIds } from '../../../shared/account-concurrency.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import {
  isAccountApiKeyPoolIsolationEnabled,
  selectAccountRuntimeApiKeyEntry
} from '../../../storage/account-api-key-rotation.js'
import { localAccountApiKeyRuntimeStatesForDispatch } from './account-api-key-failure-guard.service.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { isDynamicApiKeyGroupRouteStrategy } from '../../../domain/api-key-routing.js'
import { hashSecret } from '../../../storage/crypto.js'
import {
  listOpenAIAccountsForGroupResult,
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

const groupUsageAccessCache = createAppCache<string, GroupUsageAccessCacheEntry>({
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

const responseInspectionPolicyCache = createAppCache<string, ResponseInspectionPolicyCacheEntry>({
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
  if (runtimeConfig.processRole !== 'server') {
    return { ...readCachedGatewaySettings() }
  }
  const cached = gatewaySettingsCache.get('current')
  if (cached) {
    return { ...cached }
  }
  const value = await requestDbService({ type: 'read_gateway_settings' })
  gatewaySettingsCache.set('current', value)
  return { ...value }
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
  const value = await requestDbService({
    type: 'resolve_group_usage_access',
    groupId,
    systemAccountId
  })
  groupUsageAccessCache.set(cacheKey, groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false), {
    ttlMs: groupUsageAccessRetainTtlMs
  })
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export function listCachedOpenAIAccountsForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret[] {
  assertLocalGatewayDatabaseAccess('listCachedOpenAIAccountsForGroup')
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cloneOpenAIAccountsWithCurrentConcurrency(cached.accounts)
  }
  const value = listOpenAIAccountsForGroupResult(groupId, systemAccountId)
  const accounts = value.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(accounts), {
    ttlMs: openAIAccountsRetainTtlMs
  })
  return cloneOpenAIAccountsWithCurrentConcurrency(value.accounts)
}

export async function listCachedOpenAIAccountsForGroupAsync(groupId: string, systemAccountId: string): Promise<OpenAIAccountSecret[]> {
  if (runtimeConfig.processRole !== 'server') {
    return listCachedOpenAIAccountsForGroup(groupId, systemAccountId)
  }
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      refreshOpenAIAccountsForGroupInBackground(groupId, systemAccountId, cacheKey)
    }
    return cloneOpenAIAccountsWithCurrentConcurrency(cached.accounts)
  }
  const result = await requestDbService({
    type: 'list_openai_accounts_for_group_result',
    groupId,
    systemAccountId
  })
  const accounts = result.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, openAIAccountsCacheEntry(accounts), {
    ttlMs: openAIAccountsRetainTtlMs
  })
  return cloneOpenAIAccountsWithCurrentConcurrency(result.accounts)
}

export async function listCachedProviderModelCatalogAsync(input: {
  providerCode: string
  systemAccountId?: string
  includeInactive?: boolean
  includeUnpriced?: boolean
}): Promise<ProviderModelCatalogItem[]> {
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
  const value = await requestDbService({
    type: 'list_provider_model_catalog',
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeInactive: input.includeInactive,
    includeUnpriced: input.includeUnpriced
  })
  providerModelCatalogCache.set(cacheKey, value.map((item) => ({ ...item })))
  return value.map((item) => ({ ...item }))
}

export async function listCachedActiveResponseInspectionPoliciesAsync(input: {
  protocolCode: string
  providerCode?: string
}): Promise<ResponseInspectionPolicySummary[]> {
  const cacheKey = responseInspectionPolicyCacheKey(input.protocolCode, input.providerCode)
  const cached = responseInspectionPolicyCache.get(cacheKey)
  if (cached) {
    if (!isGatewayRuntimeCacheEntryFresh(cached)) {
      refreshActiveResponseInspectionPoliciesInBackground(input, cacheKey)
    }
    return cached.policies.map(cloneResponseInspectionPolicy)
  }
  const value = runtimeConfig.processRole !== 'server'
    ? listActiveResponseInspectionPoliciesForGateway(input)
    : await requestDbService({
        type: 'list_active_response_inspection_policies',
        protocolCode: input.protocolCode,
        providerCode: input.providerCode
      })
  responseInspectionPolicyCache.set(cacheKey, responseInspectionPolicyCacheEntry(value.map(cloneResponseInspectionPolicy)), {
    ttlMs: responseInspectionPolicyRetainTtlMs
  })
  return value.map(cloneResponseInspectionPolicy)
}

export async function readCachedGatewayRuntimeAsync(apiKey: string): Promise<DbServiceGatewayRuntime> {
  const cacheKey = hashSecret(apiKey)
  const cached = gatewayRuntimeCache.get(cacheKey)
  if (cached !== undefined) {
    if (isGatewayRuntimeCacheEntryFresh(cached)) {
      if (isDynamicApiKeyGroupRouteStrategy(cached.runtime.apiKey?.group_route_strategy)) {
        return await routeCachedDynamicGatewayRuntimeForDispatch(cached.runtime)
      }
      return cloneGatewayRuntimeForDispatch(cached.runtime)
    }
    refreshGatewayRuntimeInBackground(apiKey, cacheKey)
    const runtime = sanitizedGatewayRuntimeForDispatch(cached.runtime)
    if (runtime.apiKey && isDynamicApiKeyGroupRouteStrategy(runtime.apiKey.group_route_strategy)) {
      return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
    }
    return runtime.apiKey ? cloneGatewayRuntimeForDispatch(runtime) : cloneStaticGatewayRuntime(runtime)
  }

  const runtime = await loadGatewayRuntimeOnce(apiKey, cacheKey)
  if (runtime.apiKey && isDynamicApiKeyGroupRouteStrategy(runtime.apiKey.group_route_strategy)) {
    return await routeCachedDynamicGatewayRuntimeForDispatch(runtime)
  }
  return runtime.apiKey ? cloneGatewayRuntimeForDispatch(runtime) : cloneStaticGatewayRuntime(runtime)
}

export function clearGatewayRuntimeCache(): void {
  clearGatewayRuntimeCacheLocal()
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

export function clearGatewayRuntimeCacheLocal(): void {
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
  responseInspectionPolicyCache.clear()
  clearSettingsRepositoryCache()
}

function gatewayCacheKey(groupId: string, systemAccountId: string): string {
  return `${groupId}:${systemAccountId}`
}

function responseInspectionPolicyCacheKey(protocolCode: string, providerCode?: string): string {
  return `${protocolCode}:${providerCode ?? ''}`
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
    gatewaySettingsCache.set('current', runtime.settings)
    return
  }

  const nowMs = Date.now()
  const runtimeTtlMs = gatewayRuntimeCacheTtlMs(runtime, nowMs)
  gatewayRuntimeCache.set(cacheKey, {
    runtime: cloneStaticGatewayRuntime(runtime),
    revalidateAtMs: nowMs + runtimeTtlMs
  }, { ttlMs: gatewayRuntimeRetainTtlMs })
  gatewaySettingsCache.set('current', runtime.settings)
  if (runtime.groupAccess) {
    groupUsageAccessCache.set(gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id), groupUsageAccessCacheEntry(cloneGroupUsageAccessMetadata(runtime.groupAccess), nowMs), {
      ttlMs: groupUsageAccessRetainTtlMs
    })
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
  const cloned = cloneStaticOpenAIAccountSecret(account)
  if (cloned.type === 'api_key') {
    const accountId = cloned.credentialSourceAccountId ?? cloned.id
    const credentials = {
      ...cloned.credentials,
      api_key: cloned.apiKey,
      ...(cloned.apiKeys?.length ? { api_keys: cloned.apiKeys } : {})
    }
    const runtimeStates = [
      ...(cloned.apiKeyRuntimeStates ?? []),
      ...localAccountApiKeyRuntimeStatesForDispatch(accountId)
    ]
    const selected = selectAccountRuntimeApiKeyEntry({
      accountId,
      credentials,
      runtimeStates
    })
    if (!selected && isAccountApiKeyPoolIsolationEnabled({
      providerCode: cloned.providerCode,
      type: cloned.type,
      credentials
    })) {
      return undefined
    }
    if (selected) {
      cloned.apiKey = selected.key
      if (isAccountApiKeyPoolIsolationEnabled({
        providerCode: cloned.providerCode,
        type: cloned.type,
        credentials
      })) {
        cloned.selectedApiKeyFingerprint = selected.fingerprint
        cloned.selectedApiKeyIndex = selected.index
      }
    }
  }
  return cloned
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
    apiKey: {
      ...runtime.apiKey,
      group_bindings: runtime.apiKey.group_bindings
        ? runtime.apiKey.group_bindings.map((binding) => ({ ...binding }))
        : undefined
    },
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

function cloneStaticGatewayRuntime(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    apiKey: runtime.apiKey
      ? {
        ...runtime.apiKey,
        group_bindings: runtime.apiKey.group_bindings
          ? runtime.apiKey.group_bindings.map((binding) => ({ ...binding }))
          : undefined
      }
      : undefined,
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
    ...runtime.apiKey,
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

function isGatewayRuntimeCacheEntryFresh(entry: { revalidateAtMs?: number }, now = Date.now()): boolean {
  return entry.revalidateAtMs === undefined || entry.revalidateAtMs > now
}

function groupUsageAccessCacheEntry(value: GroupUsageAccessMetadata | false, now = Date.now()): GroupUsageAccessCacheEntry {
  return {
    value,
    revalidateAtMs: value ? now + groupUsageAccessCacheTtlMs(value, now) : now + invalidGatewayRuntimeTtlMs
  }
}

function openAIAccountsCacheEntry(accounts: OpenAIAccountSecret[], now = Date.now()): OpenAIAccountsCacheEntry {
  return {
    accounts: accounts.map(cloneStaticOpenAIAccountSecret),
    revalidateAtMs: now + openAIAccountsCacheTtlMs(now, accounts)
  }
}

function responseInspectionPolicyCacheEntry(policies: ResponseInspectionPolicySummary[], now = Date.now()): ResponseInspectionPolicyCacheEntry {
  return {
    policies: policies.map(cloneResponseInspectionPolicy),
    revalidateAtMs: now + gatewayRuntimeTtlMs
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
      groupUsageAccessCache.set(cacheKey, groupUsageAccessCacheEntry(value ? cloneGroupUsageAccessMetadata(value) : false), {
        ttlMs: groupUsageAccessRetainTtlMs
      })
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

function refreshOpenAIAccountsForGroupInBackground(groupId: string, systemAccountId: string, cacheKey: string): void {
  if (pendingOpenAIAccountsRefreshes.has(cacheKey)) {
    return
  }
  const refresh = (runtimeConfig.processRole === 'server'
    ? requestDbService({ type: 'list_openai_accounts_for_group_result', groupId, systemAccountId })
    : Promise.resolve(listOpenAIAccountsForGroupResult(groupId, systemAccountId)))
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
      responseInspectionPolicyCache.set(cacheKey, responseInspectionPolicyCacheEntry(value.map(cloneResponseInspectionPolicy)), {
        ttlMs: responseInspectionPolicyRetainTtlMs
      })
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

registerGatewayRuntimeCacheInvalidator(clearGatewayRuntimeCache)
