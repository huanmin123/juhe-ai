import { clearSharedJsonCacheInBackground, createAppCache, createSharedJsonCache } from '../../../shared/cache.js'
import { registerApiKeyQuotaCacheInvalidator, syncGatewayCacheInvalidationsFromRuntimeState } from '../../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { getStatsDatabase } from '../../../storage/database.js'
import { createPostgresDatabaseClient } from '../../../storage/database-client.js'
import { getPostgresPool } from '../../../storage/postgres-client.js'
import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../../storage/request-quota-limits.js'
import {
  isRequestQuotaExceeded,
  loadRequestQuotaCosts,
  loadRequestQuotaCostsBatchAsync,
  requestQuotaCostKey,
  requestQuotaCostKeyAsync
} from './request-quota-checker.js'
import { isGatewayQuotaCostSnapshotIncomplete, readGatewayQuotaCostsSnapshot } from './quota-snapshot-cache.service.js'

export const API_KEY_QUOTA_EXCEEDED_MESSAGE = '额度已用完，请联系管理员提升额度'

export interface ApiKeyQuotaDecision {
  allowed: boolean
  message?: string
}

type ApiKeyQuotaCacheEntry = ApiKeyQuotaDecision & {
  checkedAtMs: number
}

const API_KEY_QUOTA_CACHE_TTL_MS = 5_000
const apiKeyQuotaCache = createAppCache<string, ApiKeyQuotaCacheEntry>({
  name: 'gateway:api-key-quota',
  max: 10000,
  ttlMs: API_KEY_QUOTA_CACHE_TTL_MS,
  updateAgeOnGet: false,
  dispose: (_entry, cacheKey) => {
    removeApiKeyQuotaCacheIndex(apiKeyIdFromQuotaCacheKey(cacheKey), cacheKey)
  },
  onClear: () => {
    apiKeyQuotaCacheKeysById.clear()
  }
})
const apiKeyQuotaSharedCache = createSharedJsonCache<ApiKeyQuotaCacheEntry>({
  name: 'gateway:api-key-quota',
  max: 10000,
  ttlMs: API_KEY_QUOTA_CACHE_TTL_MS
})
const apiKeyQuotaCacheKeysById = new Map<string, Set<string>>()

export function checkGatewayApiKeyQuota(apiKey: GatewayApiKeyRow, now = new Date()): ApiKeyQuotaDecision {
  assertLocalGatewayDatabaseAccess('checkGatewayApiKeyQuota')
  const quotaLimits = parseRequestQuotaLimitsJson(apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(quotaLimits)) {
    return { allowed: true }
  }

  const cacheKey = apiKeyQuotaCacheKey(apiKey, now, quotaLimits.hourly?.hours)
  const cached = apiKeyQuotaCache.get(cacheKey)
  if (cached) {
    return cached
  }

  const quotaCosts = loadRequestQuotaCosts(getStatsDatabase(), {
    systemAccountId: apiKey.system_account_id,
    scopeType: 'api_key',
    scopeId: apiKey.id,
    now,
    hourlyWindowHours: quotaLimits.hourly?.hours
  })
  const allowed = !isRequestQuotaExceeded(quotaLimits, quotaCosts)
  const decision: ApiKeyQuotaCacheEntry = {
    allowed,
    message: allowed ? undefined : API_KEY_QUOTA_EXCEEDED_MESSAGE,
    checkedAtMs: Date.now()
  }
  setApiKeyQuotaCacheEntry(apiKey.id, cacheKey, decision)
  return decision
}

export async function checkGatewayApiKeyQuotaExactAsync(apiKey: GatewayApiKeyRow, now = new Date()): Promise<ApiKeyQuotaDecision> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return checkGatewayApiKeyQuota(apiKey, now)
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const quotaLimits = parseRequestQuotaLimitsJson(apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(quotaLimits)) {
    return { allowed: true }
  }

  const cacheKey = await apiKeyQuotaCacheKeyAsync(apiKey, now, quotaLimits.hourly?.hours)
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = apiKeyQuotaCache.get(cacheKey)
    if (cached) {
      return cached
    }
  }
  const sharedCached = await getApiKeyQuotaSharedCacheEntry(cacheKey)
  if (sharedCached) {
    setApiKeyQuotaCacheEntry(apiKey.id, cacheKey, sharedCached, { skipSharedCache: true })
    return sharedCached
  }

  const costInput = {
    systemAccountId: apiKey.system_account_id,
    scopeType: 'api_key',
    scopeId: apiKey.id,
    now,
    hourlyWindowHours: quotaLimits.hourly?.hours
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const costsByKey = await loadRequestQuotaCostsBatchAsync(client, [costInput])
  const allowed = !isRequestQuotaExceeded(
    quotaLimits,
    costsByKey.get(await requestQuotaCostKeyAsync(costInput)) ?? emptyRequestQuotaCosts()
  )
  const decision: ApiKeyQuotaCacheEntry = {
    allowed,
    message: allowed ? undefined : API_KEY_QUOTA_EXCEEDED_MESSAGE,
    checkedAtMs: Date.now()
  }
  await setApiKeyQuotaCacheEntryAsync(apiKey.id, cacheKey, decision)
  return decision
}

export async function checkGatewayApiKeyQuotaAsync(apiKey: GatewayApiKeyRow): Promise<ApiKeyQuotaDecision> {
  if (runtimeConfig.runtimeStateDriver === 'redis') await syncGatewayCacheInvalidationsFromRuntimeState()
  const now = new Date()
  const quotaLimits = parseRequestQuotaLimitsJson(apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(quotaLimits)) {
    return { allowed: true }
  }
  const cacheKey = runtimeConfig.databaseDriver === 'postgres'
    ? await apiKeyQuotaCacheKeyAsync(apiKey, now, quotaLimits.hourly?.hours)
    : apiKeyQuotaCacheKey(apiKey, now, quotaLimits.hourly?.hours)
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = apiKeyQuotaCache.get(cacheKey)
    if (cached) {
      return cached
    }
  }
  const sharedCached = await getApiKeyQuotaSharedCacheEntry(cacheKey)
  if (sharedCached) {
    setApiKeyQuotaCacheEntry(apiKey.id, cacheKey, sharedCached, { skipSharedCache: true })
    return sharedCached
  }
  if (runtimeConfig.cacheDriver === 'redis' && runtimeConfig.processRole === 'server') {
    try {
      const dbService = await import('../../db-service/db-service-ipc.js')
      const decision = await dbService.requestDbService({ type: 'check_api_key_quota', apiKey }, { timeoutMs: 1000 })
      await setApiKeyQuotaCacheEntryAsync(apiKey.id, cacheKey, {
        ...decision,
        checkedAtMs: Date.now()
      })
      return decision
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'gateway_api_key_quota_redis_exact_check_failed',
        apiKeyId: apiKey.id,
        systemAccountId: apiKey.system_account_id
      }), 'Redis 模式 API Key 配额精确补判失败，按保护策略拒绝请求')
      return { allowed: false, message: API_KEY_QUOTA_EXCEEDED_MESSAGE }
    }
  }
  if (runtimeConfig.processRole === 'server') {
    const costs = readGatewayQuotaCostsSnapshot({
      systemAccountId: apiKey.system_account_id,
      scopeType: 'api_key',
      scopeId: apiKey.id,
      hourlyWindowHours: quotaLimits.hourly?.hours
    })
    let allowed = costs ? !isRequestQuotaExceeded(quotaLimits, costs) : true
    if (!costs && isGatewayQuotaCostSnapshotIncomplete()) {
      try {
        const dbService = await import('../../db-service/db-service-ipc.js')
        const decision = await dbService.requestDbService({ type: 'check_api_key_quota', apiKey }, { timeoutMs: 1000 })
        await setApiKeyQuotaCacheEntryAsync(apiKey.id, cacheKey, {
          ...decision,
          checkedAtMs: Date.now()
        })
        return decision
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'gateway_api_key_quota_snapshot_fallback_failed',
          apiKeyId: apiKey.id,
          systemAccountId: apiKey.system_account_id
        }), 'API Key 配额快照不完整且 DB service 精确补判失败，按保护策略拒绝请求')
        allowed = false
      }
    }
    const passiveDecision: ApiKeyQuotaCacheEntry = {
      allowed,
      message: allowed ? undefined : API_KEY_QUOTA_EXCEEDED_MESSAGE,
      checkedAtMs: Date.now()
    }
    await setApiKeyQuotaCacheEntryAsync(apiKey.id, cacheKey, passiveDecision)
    return passiveDecision
  }
  const decision = runtimeConfig.databaseDriver === 'postgres'
    ? await checkGatewayApiKeyQuotaExactAsync(apiKey, now)
    : checkGatewayApiKeyQuota(apiKey, now)
  await setApiKeyQuotaCacheEntryAsync(apiKey.id, cacheKey, {
    ...decision,
    checkedAtMs: Date.now()
  })
  return decision
}

export function clearApiKeyQuotaCache(): void {
  apiKeyQuotaCache.clear()
  clearApiKeyQuotaSharedCache()
}

export function invalidateApiKeyQuotaCacheById(id: string): void {
  if (runtimeConfig.cacheDriver === 'redis') {
    apiKeyQuotaCacheKeysById.clear()
    clearApiKeyQuotaSharedCache()
    return
  }
  const cacheKeys = apiKeyQuotaCacheKeysById.get(id)
  if (!cacheKeys) return
  for (const cacheKey of [...cacheKeys]) {
    apiKeyQuotaCache.delete(cacheKey)
  }
  apiKeyQuotaCacheKeysById.delete(id)
  clearApiKeyQuotaSharedCache()
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}

function apiKeyQuotaCacheKey(apiKey: GatewayApiKeyRow, now: Date, hourlyWindowHours?: number): string {
  const windowKey = requestQuotaCostKey({
    systemAccountId: apiKey.system_account_id,
    scopeType: 'api_key',
    scopeId: apiKey.id,
    now,
    hourlyWindowHours
  })
  return `${apiKey.system_account_id}\u0000${apiKey.id}\u0000${windowKey}\u0000${apiKey.quota_limits_json ?? ''}`
}

async function apiKeyQuotaCacheKeyAsync(apiKey: GatewayApiKeyRow, now: Date, hourlyWindowHours?: number): Promise<string> {
  const windowKey = await requestQuotaCostKeyAsync({
    systemAccountId: apiKey.system_account_id,
    scopeType: 'api_key',
    scopeId: apiKey.id,
    now,
    hourlyWindowHours
  })
  return `${apiKey.system_account_id}\u0000${apiKey.id}\u0000${windowKey}\u0000${apiKey.quota_limits_json ?? ''}`
}

function apiKeyIdFromQuotaCacheKey(cacheKey: string): string {
  return cacheKey.split('\u0000')[1] ?? ''
}

function setApiKeyQuotaCacheEntry(apiKeyId: string, cacheKey: string, entry: ApiKeyQuotaCacheEntry, options: { skipSharedCache?: boolean } = {}): void {
  if (runtimeConfig.cacheDriver === 'redis') {
    apiKeyQuotaCacheKeysById.clear()
    if (!options.skipSharedCache) {
      void setApiKeyQuotaSharedCacheEntry(cacheKey, entry)
    }
    return
  }
  const previousApiKeyId = apiKeyIdFromQuotaCacheKey(cacheKey)
  if (previousApiKeyId) {
    removeApiKeyQuotaCacheIndex(previousApiKeyId, cacheKey)
  }
  apiKeyQuotaCache.set(cacheKey, entry)
  addApiKeyQuotaCacheIndex(apiKeyId, cacheKey)
  if (!options.skipSharedCache) {
    void setApiKeyQuotaSharedCacheEntry(cacheKey, entry)
  }
}

async function setApiKeyQuotaCacheEntryAsync(apiKeyId: string, cacheKey: string, entry: ApiKeyQuotaCacheEntry): Promise<void> {
  await setApiKeyQuotaSharedCacheEntry(cacheKey, entry)
  setApiKeyQuotaCacheEntry(apiKeyId, cacheKey, entry, { skipSharedCache: true })
}

function addApiKeyQuotaCacheIndex(apiKeyId: string, cacheKey: string): void {
  const cacheKeys = apiKeyQuotaCacheKeysById.get(apiKeyId) ?? new Set<string>()
  cacheKeys.add(cacheKey)
  apiKeyQuotaCacheKeysById.set(apiKeyId, cacheKeys)
}

function removeApiKeyQuotaCacheIndex(apiKeyId: string, cacheKey: string): void {
  const cacheKeys = apiKeyQuotaCacheKeysById.get(apiKeyId)
  if (!cacheKeys) return
  cacheKeys.delete(cacheKey)
  if (!cacheKeys.size) {
    apiKeyQuotaCacheKeysById.delete(apiKeyId)
  }
}

registerApiKeyQuotaCacheInvalidator((apiKeyId) => {
  if (apiKeyId) {
    invalidateApiKeyQuotaCacheById(apiKeyId)
    return
  }
  clearApiKeyQuotaCache()
})

async function getApiKeyQuotaSharedCacheEntry(cacheKey: string): Promise<ApiKeyQuotaCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  return await apiKeyQuotaSharedCache.get(sharedQuotaCacheKey(cacheKey))
}

async function setApiKeyQuotaSharedCacheEntry(cacheKey: string, entry: ApiKeyQuotaCacheEntry): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await apiKeyQuotaSharedCache.set(sharedQuotaCacheKey(cacheKey), entry, { ttlMs: API_KEY_QUOTA_CACHE_TTL_MS })
}

function clearApiKeyQuotaSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    apiKeyQuotaSharedCache,
    'api_key_quota_shared_cache_clear_failed',
    'API Key 额度 Redis shared cache 清理失败'
  )
}

function sharedQuotaCacheKey(cacheKey: string): string {
  return Buffer.from(cacheKey).toString('base64url')
}

function emptyRequestQuotaCosts() {
  return { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
}
