import { createAppCache } from '../../shared/cache.js'
import { loadAccountCurrentConcurrencyByIds } from '../../shared/account-concurrency.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../config/runtime.js'
import { hashSecret } from '../../storage/crypto.js'
import {
  listOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { clearSettingsRepositoryCache } from '../../storage/settings.repository.js'
import { clearDbServiceGatewayRuntimeCache, requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceGatewayRuntime } from '../db-service/db-service-types.js'
import { readGatewaySettings, type GatewaySettings } from './account-error-policy.service.js'

const gatewayRuntimeTtlMs = 60_000
const gatewaySettingsTtlMs = 60_000
const groupUsageAccessTtlMs = 60_000
const openAIAccountsTtlMs = 60_000

const gatewayRuntimeCache = createAppCache<string, DbServiceGatewayRuntime>({
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
  groupUsageAccessCache.set(cacheKey, value ? cloneGroupUsageAccessMetadata(value) : false)
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export async function resolveCachedGroupUsageAccessMetadataAsync(groupId: string, systemAccountId: string): Promise<GroupUsageAccessMetadata | undefined> {
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
  groupUsageAccessCache.set(cacheKey, value ? cloneGroupUsageAccessMetadata(value) : false)
  return value ? cloneGroupUsageAccessMetadata(value) : undefined
}

export function listCachedOpenAIAccountsForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret[] {
  assertLocalGatewayDatabaseAccess('listCachedOpenAIAccountsForGroup')
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cloneOpenAIAccountsWithCurrentConcurrency(cached)
  }
  const value = listOpenAIAccountsForGroup(groupId, systemAccountId)
  openAIAccountsCache.set(cacheKey, value.map(cloneStaticOpenAIAccountSecret))
  return cloneOpenAIAccountsWithCurrentConcurrency(value)
}

export async function listCachedOpenAIAccountsForGroupAsync(groupId: string, systemAccountId: string): Promise<OpenAIAccountSecret[]> {
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cloneOpenAIAccountsWithCurrentConcurrency(cached)
  }
  const accounts = await requestDbService({
    type: 'list_openai_accounts_for_group',
    groupId,
    systemAccountId
  })
  openAIAccountsCache.set(cacheKey, accounts.map(cloneStaticOpenAIAccountSecret))
  return cloneOpenAIAccountsWithCurrentConcurrency(accounts)
}

export async function readCachedGatewayRuntimeAsync(apiKey: string): Promise<DbServiceGatewayRuntime> {
  const cacheKey = hashSecret(apiKey)
  const cached = gatewayRuntimeCache.get(cacheKey)
  if (cached && cached.apiKey) {
    return cloneGatewayRuntimeForDispatch(cached)
  }

  const runtime = await requestDbService({
    type: 'read_gateway_runtime',
    key: apiKey
  })
  if (!runtime.apiKey) {
    return cloneStaticGatewayRuntime(runtime)
  }

  gatewayRuntimeCache.set(cacheKey, cloneStaticGatewayRuntime(runtime), { ttlMs: gatewayRuntimeCacheTtlMs(runtime) })
  gatewaySettingsCache.set('current', runtime.settings)
  if (runtime.groupAccess) {
    groupUsageAccessCache.set(gatewayCacheKey(runtime.apiKey.group_id, runtime.apiKey.system_account_id), cloneGroupUsageAccessMetadata(runtime.groupAccess))
  }
  openAIAccountsCache.set(gatewayCacheKey(runtime.apiKey.group_id, runtime.apiKey.system_account_id), runtime.accounts.map(cloneStaticOpenAIAccountSecret))
  return cloneGatewayRuntimeForDispatch(runtime)
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
  gatewayRuntimeCache.clear()
  gatewaySettingsCache.clear()
  groupUsageAccessCache.clear()
  openAIAccountsCache.clear()
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

function cloneStaticOpenAIAccountSecret(account: OpenAIAccountSecret): OpenAIAccountSecret {
  return {
    ...account,
    currentConcurrency: undefined,
    supportedModels: [...(account.supportedModels ?? [])],
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
    apiKey: runtime.apiKey ? { ...runtime.apiKey } : undefined,
    settings: { ...runtime.settings },
    groupAccess: runtime.groupAccess ? cloneGroupUsageAccessMetadata(runtime.groupAccess) : undefined,
    accounts: runtime.accounts.map(cloneStaticOpenAIAccountSecret)
  }
}

function cloneGatewayRuntimeForDispatch(runtime: DbServiceGatewayRuntime): DbServiceGatewayRuntime {
  return {
    ...cloneStaticGatewayRuntime(runtime),
    accounts: cloneOpenAIAccountsWithCurrentConcurrency(runtime.accounts)
  }
}

function gatewayRuntimeCacheTtlMs(runtime: DbServiceGatewayRuntime): number {
  let ttlMs = gatewayRuntimeTtlMs
  for (const expiresAt of runtimeCacheExpiryCandidates(runtime)) {
    const expiresAtMs = Date.parse(expiresAt)
    if (!Number.isFinite(expiresAtMs)) {
      continue
    }
    ttlMs = Math.min(ttlMs, expiresAtMs - Date.now())
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
