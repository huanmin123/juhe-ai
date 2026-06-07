import { createAppCache } from '../../shared/cache.js'
import { loadAccountCurrentConcurrencyByIds } from '../../shared/account-concurrency.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../config/runtime.js'
import { isDynamicApiKeyGroupRouteStrategy } from '../../domain/api-key-routing.js'
import { hashSecret } from '../../storage/crypto.js'
import {
  listOpenAIAccountsForGroupResult,
  resolveGroupUsageAccessMetadata,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { clearSettingsRepositoryCache } from '../../storage/settings.repository.js'
import { availabilityScheduleCacheTtlMs } from '../../storage/availability-schedule-cache.js'
import { clearDbServiceGatewayRuntimeCache, requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceGatewayRuntime } from '../db-service/db-service-types.js'
import { readGatewaySettings, type GatewaySettings } from './request-error-policy.service.js'
import type { ErrorPolicySummary } from '../../storage/error-policy.repository.js'
import type { StreamInterceptPolicySummary } from '../../storage/stream-intercept-policy.repository.js'
import { listProviderModelCatalog, type ProviderModelCatalogItem } from '../model-pricing/model-catalog.service.js'

const gatewayRuntimeTtlMs = 60_000
const invalidGatewayRuntimeTtlMs = 10_000
const gatewaySettingsTtlMs = 60_000
const groupUsageAccessTtlMs = 60_000
const openAIAccountsTtlMs = 60_000
const providerModelCatalogTtlMs = 60_000

interface GatewayRuntimeCacheEntry {
  runtime: DbServiceGatewayRuntime
  revalidateAtMs?: number
}

const gatewayRuntimeCache = createAppCache<string, GatewayRuntimeCacheEntry>({
  name: 'gateway:runtime',
  max: 10000,
  ttlMs: gatewayRuntimeTtlMs,
  updateAgeOnGet: true
})

const gatewaySettingsCache = createAppCache<string, GatewaySettings>({
  name: 'gateway:settings',
  max: 1,
  ttlMs: gatewaySettingsTtlMs
})

const groupUsageAccessCache = createAppCache<string, GroupUsageAccessMetadata | false>({
  name: 'gateway:group-usage-access',
  max: 1000,
  ttlMs: groupUsageAccessTtlMs
})

const openAIAccountsCache = createAppCache<string, OpenAIAccountSecret[]>({
  name: 'gateway:openai-accounts',
  max: 1000,
  ttlMs: openAIAccountsTtlMs
})

const providerModelCatalogCache = createAppCache<string, ProviderModelCatalogItem[]>({
  name: 'gateway:provider-model-catalog',
  max: 1000,
  ttlMs: providerModelCatalogTtlMs
})

const pendingGatewayRuntimeLoads = new Map<string, Promise<DbServiceGatewayRuntime>>()
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
    return cached ? cloneGroupUsageAccessMetadata(cached) : undefined
  }
  const value = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  groupUsageAccessCache.set(cacheKey, value ? cloneGroupUsageAccessMetadata(value) : false, value ? {
    ttlMs: groupUsageAccessCacheTtlMs(value)
  } : undefined)
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export async function resolveCachedGroupUsageAccessMetadataAsync(groupId: string, systemAccountId: string): Promise<GroupUsageAccessMetadata | undefined> {
  if (runtimeConfig.processRole !== 'server') {
    return resolveCachedGroupUsageAccessMetadata(groupId, systemAccountId)
  }
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = groupUsageAccessCache.get(cacheKey)
  if (cached !== undefined) {
    return cached ? cloneGroupUsageAccessMetadata(cached) : undefined
  }
  const value = await requestDbService({
    type: 'resolve_group_usage_access',
    groupId,
    systemAccountId
  })
  groupUsageAccessCache.set(cacheKey, value ? cloneGroupUsageAccessMetadata(value) : false, value ? {
    ttlMs: groupUsageAccessCacheTtlMs(value)
  } : undefined)
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export function listCachedOpenAIAccountsForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret[] {
  assertLocalGatewayDatabaseAccess('listCachedOpenAIAccountsForGroup')
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cloneOpenAIAccountsWithCurrentConcurrency(cached)
  }
  const value = listOpenAIAccountsForGroupResult(groupId, systemAccountId)
  const accounts = value.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, accounts, {
    ttlMs: openAIAccountsCacheTtlMs(value.hasAccountAvailabilitySchedule, Date.now(), accounts)
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
    return cloneOpenAIAccountsWithCurrentConcurrency(cached)
  }
  const result = await requestDbService({
    type: 'list_openai_accounts_for_group_result',
    groupId,
    systemAccountId
  })
  const accounts = result.accounts.map(cloneStaticOpenAIAccountSecret)
  openAIAccountsCache.set(cacheKey, accounts, {
    ttlMs: openAIAccountsCacheTtlMs(result.hasAccountAvailabilitySchedule, Date.now(), accounts)
  })
  return cloneOpenAIAccountsWithCurrentConcurrency(result.accounts)
}

export async function listCachedProviderModelCatalogAsync(input: {
  providerCode: string
  systemAccountId?: string
  includeMappingTargets?: boolean
  includeInactive?: boolean
  includeUnpriced?: boolean
}): Promise<ProviderModelCatalogItem[]> {
  if (runtimeConfig.processRole !== 'server') {
    return listProviderModelCatalog(input)
  }
  const cacheKey = [
    input.providerCode,
    input.systemAccountId ?? '',
    input.includeMappingTargets === true ? 'all' : 'public',
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
    includeMappingTargets: input.includeMappingTargets,
    includeInactive: input.includeInactive,
    includeUnpriced: input.includeUnpriced
  })
  providerModelCatalogCache.set(cacheKey, value.map((item) => ({ ...item })))
  return value.map((item) => ({ ...item }))
}

export async function readCachedGatewayRuntimeAsync(apiKey: string): Promise<DbServiceGatewayRuntime> {
  const cacheKey = hashSecret(apiKey)
  const cached = gatewayRuntimeCache.get(cacheKey)
  if (cached !== undefined) {
    if (isGatewayRuntimeCacheEntryFresh(cached) && !isGatewayRuntimeCacheEntryDynamic(cached)) {
      return cloneGatewayRuntimeForDispatch(cached.runtime)
    }
    gatewayRuntimeCache.delete(cacheKey)
  }

  const runtime = await loadGatewayRuntimeOnce(apiKey, cacheKey)
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
  gatewayRuntimeCache.clear()
  gatewaySettingsCache.clear()
  groupUsageAccessCache.clear()
  openAIAccountsCache.clear()
  providerModelCatalogCache.clear()
  clearSettingsRepositoryCache()
}

function gatewayCacheKey(groupId: string, systemAccountId: string): string {
  return `${groupId}:${systemAccountId}`
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
    key: apiKey
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
    }, { ttlMs: invalidGatewayRuntimeTtlMs })
    gatewaySettingsCache.set('current', runtime.settings)
    return
  }

  const nowMs = Date.now()
  if (!isDynamicApiKeyGroupRouteStrategy(runtime.apiKey.group_route_strategy)) {
    const runtimeTtlMs = gatewayRuntimeCacheTtlMs(runtime, nowMs)
    gatewayRuntimeCache.set(cacheKey, {
      runtime: cloneStaticGatewayRuntime(runtime),
      revalidateAtMs: nowMs + runtimeTtlMs
    }, { ttlMs: runtimeTtlMs })
  }
  gatewaySettingsCache.set('current', runtime.settings)
  if (runtime.groupAccess) {
    groupUsageAccessCache.set(gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id), cloneGroupUsageAccessMetadata(runtime.groupAccess), {
      ttlMs: groupUsageAccessCacheTtlMs(runtime.groupAccess, nowMs)
    })
  }
  if (runtime.groupAccess && !isRuntimeApiKeyScheduleInactive(runtime)) {
    const accounts = runtime.accounts.map(cloneStaticOpenAIAccountSecret)
    openAIAccountsCache.set(gatewayCacheKey(runtime.apiKey.selected_group_id, runtime.apiKey.system_account_id), accounts, {
      ttlMs: openAIAccountsCacheTtlMs(runtime.hasAccountAvailabilitySchedule === true, nowMs, accounts)
    })
  }
}

function cloneStaticOpenAIAccountSecret(account: OpenAIAccountSecret): OpenAIAccountSecret {
  return {
    ...account,
    currentConcurrency: undefined,
    supportedModels: [...(account.supportedModels ?? [])],
    modelMappings: (account.modelMappings ?? []).map((mapping) => ({ ...mapping })),
    credentials: { ...account.credentials }
  }
}

function cloneOpenAIAccountsWithCurrentConcurrency(accounts: OpenAIAccountSecret[]): OpenAIAccountSecret[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  return accounts.map((account) => ({
    ...cloneStaticOpenAIAccountSecret(account),
    currentConcurrency: concurrency.get(account.id) ?? 0
  }))
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
    hasAccountAvailabilitySchedule: runtime.hasAccountAvailabilitySchedule,
    accountDispatchDiagnostics: runtime.accountDispatchDiagnostics ? { ...runtime.accountDispatchDiagnostics } : undefined,
    settings: { ...runtime.settings },
    groupAccess: runtime.groupAccess ? cloneGroupUsageAccessMetadata(runtime.groupAccess) : undefined,
    accounts: runtime.accounts.map(cloneStaticOpenAIAccountSecret),
    errorPolicies: runtime.errorPolicies ? runtime.errorPolicies.map(cloneErrorPolicy) : undefined,
    streamInterceptPolicies: runtime.streamInterceptPolicies ? runtime.streamInterceptPolicies.map(cloneStreamInterceptPolicy) : undefined
  }
}

function cloneGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    ...cloneStaticGatewayRuntime(runtime),
    accounts: cloneOpenAIAccountsWithCurrentConcurrency(runtime.accounts)
  }
}

function cloneStreamInterceptPolicy(policy: StreamInterceptPolicySummary): StreamInterceptPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}

function cloneErrorPolicy(policy: ErrorPolicySummary): ErrorPolicySummary {
  return {
    ...policy,
    match: { ...policy.match }
  }
}

function isGatewayRuntimeCacheEntryFresh(entry: GatewayRuntimeCacheEntry, now = Date.now()): boolean {
  return entry.revalidateAtMs === undefined || entry.revalidateAtMs > now
}

function isGatewayRuntimeCacheEntryDynamic(entry: GatewayRuntimeCacheEntry): boolean {
  return isDynamicApiKeyGroupRouteStrategy(entry.runtime.apiKey?.group_route_strategy)
}

function isRuntimeApiKeyScheduleInactive(runtime: DbServiceGatewayRuntime): boolean {
  return Boolean(runtime.apiKey?.availability_schedule_json && runtime.apiKey.availability_schedule_active === 0)
}

function gatewayRuntimeCacheTtlMs(runtime: DbServiceGatewayRuntime, now = Date.now()): number {
  let ttlMs = gatewayRuntimeTtlMs
  if (runtime.apiKey?.availability_schedule_json || runtime.hasAccountAvailabilitySchedule) {
    ttlMs = Math.min(ttlMs, availabilityScheduleCacheTtlMs(now))
  }
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

function openAIAccountsCacheTtlMs(hasAccountAvailabilitySchedule: boolean, now = Date.now(), accounts: OpenAIAccountSecret[] = []): number {
  const ttlMs = hasAccountAvailabilitySchedule ? availabilityScheduleCacheTtlMs(now) : openAIAccountsTtlMs
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

registerGatewayRuntimeCacheInvalidator(clearGatewayRuntimeCache)
