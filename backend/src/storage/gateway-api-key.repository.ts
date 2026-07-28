import { clearSharedJsonCacheInBackground, createAppCache, createProcessLocalResourceCache, createSharedJsonCache } from '../shared/cache.js'
import {
  registerGatewayApiKeyValidationCacheInvalidator,
  registerGatewayRuntimeCacheInvalidator,
  syncGatewayCacheInvalidationsFromRuntimeState
} from '../shared/gateway-cache-invalidation.js'
import { maxRouteStrategyGroupBindings } from './route-strategy-group-binding-limits.js'
import {
  normalizeApiKeyGroupBindingWeight
} from '../domain/api-key-routing.js'
import {
  normalizeRouteStrategyMode,
  parseRouteStrategyRuntimeConfigJson
} from '../domain/route-strategy.js'
import type { ApiKeyHybridRoutingConfig, RouteStrategyMode, RouteStrategyNormalRoutingConfig, UserRequestLimits } from '../domain/types.js'
import { parseUserRequestLimitsJson } from '../domain/user-request-limits.js'
import { runtimeConfig } from '../config/runtime.js'
import { hashSecret } from './crypto.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export interface GatewayApiKeyRow {
  id: string
  system_account_id: string
  route_strategy_id: string
  route_strategy_mode: RouteStrategyMode
  route_strategy_config_json: string | null
  selected_group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
  quota_limits_json: string | null
  normal_routing_config?: RouteStrategyNormalRoutingConfig
  hybrid_routing_config?: ApiKeyHybridRoutingConfig
  system_account_image_generation_enabled: number
  system_account_request_limits_json?: string | null
  system_account_request_limits?: UserRequestLimits
  group_bindings?: GatewayApiKeyGroupBindingRow[]
}

export interface GatewayApiKeyGroupBindingRow {
  id: string
  api_key_id: string
  system_account_id: string
  group_id: string
  priority: number
  weight: number
  status: 'active' | 'disabled'
  provider_code: string
  group_enabled: number
}

type GatewayApiKeyCacheEntry = {
  row: GatewayApiKeyRow
  forceRevalidateAtMs: number
}

type GatewayApiKeyPrewarmRow = GatewayApiKeyRow & {
  key_hash: string
}

const GATEWAY_API_KEY_CACHE_TTL_MS = 60_000
const GATEWAY_API_KEY_CACHE_MAX_STALE_MS = 5 * 60_000
const businessSchemaName = 'juhe_business'
const gatewayApiKeyCache = createAppCache<string, GatewayApiKeyCacheEntry>({
  name: 'gateway:api-key-validation',
  max: 10000,
  ttlMs: GATEWAY_API_KEY_CACHE_TTL_MS,
  updateAgeOnGet: true,
  dispose: (entry, keyHash) => {
    removeGatewayApiKeyCacheIndex(entry.row.id, keyHash)
  },
  onClear: () => {
    gatewayApiKeyCacheKeysById.clear()
  }
})
const gatewayApiKeySharedCache = createSharedJsonCache<GatewayApiKeyCacheEntry>({
  name: 'gateway:api-key-validation',
  max: 10000,
  ttlMs: GATEWAY_API_KEY_CACHE_TTL_MS
})
const gatewayApiKeyProcessCache = createProcessLocalResourceCache<string, GatewayApiKeyCacheEntry>({
  name: 'gateway:api-key-validation:local',
  max: 10_000,
  ttlMs: GATEWAY_API_KEY_CACHE_TTL_MS
})
const gatewayApiKeyCacheKeysById = new Map<string, Set<string>>()
const gatewayApiKeyValidationAttemptLimit = 3
let gatewayApiKeyValidationCacheGeneration = 0

export function validateGatewayApiKey(key: string): GatewayApiKeyRow | undefined {
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  const now = Date.now()
  const cached = gatewayApiKeyCache.get(keyHash)
  if (
    cached
    && cached.forceRevalidateAtMs > now
    && !isGatewayApiKeyRowExpired(cached.row, now)
  ) {
    return cloneGatewayApiKeyRow(cached.row)
  }

  const row = getBusinessDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      route_strategies.id AS route_strategy_id,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.config_json AS route_strategy_config_json,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled,
      system_accounts.request_limits_json AS system_account_request_limits_json
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    WHERE api_keys.key_hash = ?
      AND system_accounts.status = 'active'
  `).get(keyHash) as unknown as GatewayApiKeyRow | undefined
  if (!row) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row, now)) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  if (row.status !== 'active') {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  normalizeGatewayApiKeyRouteFields(row)
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.route_strategy_id, row.system_account_id)
  if (!row.group_bindings.length) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  setGatewayApiKeyCacheEntry(keyHash, {
    row: cloneGatewayApiKeyRow(row),
    forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
  }, { ttlMs: gatewayApiKeyCacheTtlMs(now, row) })
  return cloneGatewayApiKeyRow(row)
}

export function loadGatewayApiKeyForValidationReadOnly(key: string): GatewayApiKeyRow | undefined {
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  const now = Date.now()
  const row = getBusinessDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      route_strategies.id AS route_strategy_id,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.config_json AS route_strategy_config_json,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled,
      system_accounts.request_limits_json AS system_account_request_limits_json
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    WHERE api_keys.key_hash = ?
      AND system_accounts.status = 'active'
  `).get(keyHash) as unknown as GatewayApiKeyRow | undefined
  if (!row) {
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row, now)) {
    return undefined
  }
  if (row.status !== 'active') {
    return undefined
  }
  normalizeGatewayApiKeyRouteFields(row)
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.route_strategy_id, row.system_account_id)
  if (!row.group_bindings.length) {
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  return row
}

export async function validateGatewayApiKeyAsync(key: string): Promise<GatewayApiKeyRow | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return await validateGatewayApiKeyWithSqliteReadWorker(key)
    }
    return validateGatewayApiKey(key)
  }
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    // 鉴权接受进程内热缓存前必须看到跨实例失效。异步同步可能让刚停用的 Key
    // 一直放行到后续请求恰好观察到失效版本为止。
    await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  }
  for (let attempt = 0; attempt < gatewayApiKeyValidationAttemptLimit; attempt += 1) {
    const generation = gatewayApiKeyValidationCacheGeneration
    const now = Date.now()
    const processCached = gatewayApiKeyProcessCache.get(keyHash)
    if (
      processCached
      && processCached.forceRevalidateAtMs > now
      && !isGatewayApiKeyRowExpired(processCached.row, now)
    ) {
      return cloneGatewayApiKeyRow(processCached.row)
    }
    if (runtimeConfig.cacheDriver !== 'redis') {
      const cached = gatewayApiKeyCache.get(keyHash)
      if (
        cached
        && cached.forceRevalidateAtMs > now
        && !isGatewayApiKeyRowExpired(cached.row, now)
      ) {
        return cloneGatewayApiKeyRow(cached.row)
      }
    }

    const sharedCached = await getGatewayApiKeySharedCacheEntry(keyHash)
    await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)
    if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue
    if (
      sharedCached
      && sharedCached.forceRevalidateAtMs > now
      && !isGatewayApiKeyRowExpired(sharedCached.row, now)
    ) {
      gatewayApiKeyProcessCache.set(keyHash, {
        row: cloneGatewayApiKeyRow(sharedCached.row),
        forceRevalidateAtMs: sharedCached.forceRevalidateAtMs
      }, { ttlMs: gatewayApiKeyCacheTtlMs(now, sharedCached.row) })
      setGatewayApiKeyCacheEntry(keyHash, {
        row: cloneGatewayApiKeyRow(sharedCached.row),
        forceRevalidateAtMs: sharedCached.forceRevalidateAtMs
      }, {
        ttlMs: gatewayApiKeyCacheTtlMs(now, sharedCached.row),
        skipSharedCache: true
      })
      return cloneGatewayApiKeyRow(sharedCached.row)
    }

    const client = await getGatewayApiKeyDatabaseClient()
    const row = await loadGatewayApiKeyBaseRowAsync(keyHash, client)
    if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue
    if (!row) {
      gatewayApiKeyCache.delete(keyHash)
      return undefined
    }
    if (isGatewayApiKeyRowExpired(row, now)) {
      gatewayApiKeyCache.delete(keyHash)
      return undefined
    }
    if (row.status !== 'active') {
      gatewayApiKeyCache.delete(keyHash)
      return undefined
    }
    normalizeGatewayApiKeyRouteFields(row)
    row.group_bindings = await loadActiveGatewayApiKeyGroupBindingsAsync(row.id, row.route_strategy_id, row.system_account_id, client)
    await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)
    if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue
    if (!row.group_bindings.length) {
      gatewayApiKeyCache.delete(keyHash)
      return undefined
    }
    row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
    const cached = await setGatewayApiKeyCacheEntryAsync(keyHash, {
      row: cloneGatewayApiKeyRow(row),
      forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
    }, {
      ttlMs: gatewayApiKeyCacheTtlMs(now, row),
      expectedGeneration: generation
    })
    if (!cached) continue
    return cloneGatewayApiKeyRow(row)
  }
  return await loadGatewayApiKeyForValidationAuthoritativelyAsync(keyHash)
}

export async function prewarmGatewayApiKeyValidationCacheAsync(): Promise<number> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  }
  const generation = gatewayApiKeyValidationCacheGeneration
  const client = await getGatewayApiKeyDatabaseClient()
  const now = nowIso()
  const rows = await client.query<GatewayApiKeyPrewarmRow>(`
    SELECT
      api_keys.key_hash,
      api_keys.id,
      api_keys.system_account_id,
      route_strategies.id AS route_strategy_id,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.config_json AS route_strategy_config_json,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled,
      system_accounts.request_limits_json AS system_account_request_limits_json
    FROM ${gatewayApiKeyTable(client, 'api_keys')} api_keys
    INNER JOIN ${gatewayApiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
      AND system_accounts.status = 'active'
    INNER JOIN ${gatewayApiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    WHERE api_keys.status = 'active'
      AND (api_keys.expires_at IS NULL OR api_keys.expires_at > ?)
  `, [now])
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  }
  if (!isGatewayApiKeyValidationGenerationCurrent(generation)) return 0
  let warmed = 0
  for (let index = 0; index < rows.length; index += 8) {
    const results = await Promise.all(rows.slice(index, index + 8).map(async (row) => {
      const { key_hash: keyHash, ...apiKeyRow } = row
      normalizeGatewayApiKeyRouteFields(apiKeyRow)
      apiKeyRow.group_bindings = await loadActiveGatewayApiKeyGroupBindingsAsync(
        apiKeyRow.id,
        apiKeyRow.route_strategy_id,
        apiKeyRow.system_account_id,
        client
      )
      await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)
      if (!isGatewayApiKeyValidationGenerationCurrent(generation)) return false
      if (!apiKeyRow.group_bindings.length) return false
      apiKeyRow.selected_group_id = apiKeyRow.group_bindings[0]?.group_id ?? ''
      const cacheNow = Date.now()
      return await setGatewayApiKeyCacheEntryAsync(keyHash, {
        row: cloneGatewayApiKeyRow(apiKeyRow),
        forceRevalidateAtMs: cacheNow + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
      }, {
        ttlMs: gatewayApiKeyCacheTtlMs(cacheNow, apiKeyRow),
        expectedGeneration: generation
      })
    }))
    warmed += results.filter(Boolean).length
  }
  return warmed
}

async function validateGatewayApiKeyWithSqliteReadWorker(key: string): Promise<GatewayApiKeyRow | undefined> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  }
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  for (let attempt = 0; attempt < gatewayApiKeyValidationAttemptLimit; attempt += 1) {
    const generation = gatewayApiKeyValidationCacheGeneration
    const now = Date.now()
    if (runtimeConfig.cacheDriver !== 'redis') {
      const cached = gatewayApiKeyCache.get(keyHash)
      if (
        cached
        && cached.forceRevalidateAtMs > now
        && !isGatewayApiKeyRowExpired(cached.row, now)
      ) {
        return cloneGatewayApiKeyRow(cached.row)
      }
    }
    const sharedCached = await getGatewayApiKeySharedCacheEntry(keyHash)
    await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)
    if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue
    if (
      sharedCached
      && sharedCached.forceRevalidateAtMs > now
      && !isGatewayApiKeyRowExpired(sharedCached.row, now)
    ) {
      setGatewayApiKeyCacheEntry(keyHash, {
        row: cloneGatewayApiKeyRow(sharedCached.row),
        forceRevalidateAtMs: sharedCached.forceRevalidateAtMs
      }, {
        ttlMs: gatewayApiKeyCacheTtlMs(now, sharedCached.row),
        skipSharedCache: true
      })
      return cloneGatewayApiKeyRow(sharedCached.row)
    }

    const row = await requestSqliteReadWorker({
      type: 'load_gateway_api_key_for_validation_read_only',
      key
    })
    await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)
    if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue
    if (!row) {
      gatewayApiKeyCache.delete(keyHash)
      return undefined
    }
    const cached = await setGatewayApiKeyCacheEntryAsync(keyHash, {
      row: cloneGatewayApiKeyRow(row),
      forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
    }, {
      ttlMs: gatewayApiKeyCacheTtlMs(now, row),
      expectedGeneration: generation
    })
    if (!cached) continue
    return cloneGatewayApiKeyRow(row)
  }
  return await loadGatewayApiKeyForValidationWithSqliteReadWorkerAuthoritativelyAsync(key)
}

async function loadGatewayApiKeyForValidationAuthoritativelyAsync(keyHash: string): Promise<GatewayApiKeyRow | undefined> {
  const client = await getGatewayApiKeyDatabaseClient()
  const row = await loadGatewayApiKeyBaseRowAsync(keyHash, client)
  const now = Date.now()
  if (!row || isGatewayApiKeyRowExpired(row, now) || row.status !== 'active') {
    return undefined
  }
  normalizeGatewayApiKeyRouteFields(row)
  row.group_bindings = await loadActiveGatewayApiKeyGroupBindingsAsync(
    row.id,
    row.route_strategy_id,
    row.system_account_id,
    client
  )
  if (!row.group_bindings.length) {
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  return cloneGatewayApiKeyRow(row)
}

async function loadGatewayApiKeyForValidationWithSqliteReadWorkerAuthoritativelyAsync(
  key: string
): Promise<GatewayApiKeyRow | undefined> {
  const row = await requestSqliteReadWorker({
    type: 'load_gateway_api_key_for_validation_read_only',
    key
  })
  return row ? cloneGatewayApiKeyRow(row) : undefined
}

async function loadGatewayApiKeyBaseRowAsync(
  keyHash: string,
  client: DatabaseClient
): Promise<GatewayApiKeyRow | undefined> {
  return await client.one<GatewayApiKeyRow>(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      route_strategies.id AS route_strategy_id,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.config_json AS route_strategy_config_json,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled,
      system_accounts.request_limits_json AS system_account_request_limits_json
    FROM ${gatewayApiKeyTable(client, 'api_keys')} api_keys
    INNER JOIN ${gatewayApiKeyTable(client, 'system_accounts')} system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN ${gatewayApiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    WHERE api_keys.key_hash = ?
      AND system_accounts.status = 'active'
  `, [keyHash])
}

export function findActiveGatewayApiKeyById(id: string): GatewayApiKeyRow | undefined {
  const apiKeyId = id.trim()
  if (!apiKeyId) return undefined
  const row = getBusinessDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      route_strategies.id AS route_strategy_id,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.config_json AS route_strategy_config_json,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled,
      system_accounts.request_limits_json AS system_account_request_limits_json
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    WHERE api_keys.id = ?
      AND system_accounts.status = 'active'
    LIMIT 1
  `).get(apiKeyId) as unknown as GatewayApiKeyRow | undefined
  if (!row) {
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row)) {
    return undefined
  }
  if (row.status !== 'active') {
    return undefined
  }
  normalizeGatewayApiKeyRouteFields(row)
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.route_strategy_id, row.system_account_id)
  if (!row.group_bindings.length) {
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  return row
}

function normalizeGatewayApiKeyRouteFields(row: GatewayApiKeyRow): void {
  row.system_account_request_limits = parseUserRequestLimitsJson(row.system_account_request_limits_json)
  row.route_strategy_mode = normalizeRouteStrategyMode(row.route_strategy_mode)
  const routeStrategyConfig = parseRouteStrategyRuntimeConfigJson(row.route_strategy_config_json)
  row.normal_routing_config = row.route_strategy_mode === 'normal'
    ? routeStrategyConfig.normalRoutingConfig
    : undefined
  row.hybrid_routing_config = row.route_strategy_mode === 'hybrid_smart'
    ? routeStrategyConfig.hybridRoutingConfig
    : undefined
}

export function clearGatewayApiKeyValidationCache(): void {
  advanceGatewayApiKeyValidationCacheGeneration()
  gatewayApiKeyCache.clear()
  gatewayApiKeyProcessCache.clear()
  clearGatewayApiKeySharedCache()
}

export async function clearGatewayApiKeyValidationCacheAsync(): Promise<void> {
  advanceGatewayApiKeyValidationCacheGeneration()
  gatewayApiKeyCache.clear()
  gatewayApiKeyProcessCache.clear()
  await clearGatewayApiKeySharedCacheAsync()
}

export function invalidateGatewayApiKeyCacheById(id: string): void {
  advanceGatewayApiKeyValidationCacheGeneration()
  gatewayApiKeyProcessCache.clear()
  if (runtimeConfig.cacheDriver === 'redis') {
    gatewayApiKeyCacheKeysById.clear()
    clearGatewayApiKeySharedCache()
    return
  }
  const keyHashes = gatewayApiKeyCacheKeysById.get(id)
  if (keyHashes) {
    for (const keyHash of [...keyHashes]) {
      gatewayApiKeyCache.delete(keyHash)
    }
    gatewayApiKeyCacheKeysById.delete(id)
  }
  clearGatewayApiKeySharedCache()
}

export async function invalidateGatewayApiKeyCacheByIdAsync(id: string): Promise<void> {
  advanceGatewayApiKeyValidationCacheGeneration()
  gatewayApiKeyProcessCache.clear()
  if (runtimeConfig.cacheDriver === 'redis') {
    gatewayApiKeyCacheKeysById.clear()
    await clearGatewayApiKeySharedCacheAsync()
    return
  }
  const keyHashes = gatewayApiKeyCacheKeysById.get(id)
  if (keyHashes) {
    for (const keyHash of [...keyHashes]) {
      gatewayApiKeyCache.delete(keyHash)
    }
    gatewayApiKeyCacheKeysById.delete(id)
  }
  await clearGatewayApiKeySharedCacheAsync()
}

function advanceGatewayApiKeyValidationCacheGeneration(): void {
  gatewayApiKeyValidationCacheGeneration += 1
}

function isGatewayApiKeyValidationGenerationCurrent(generation: number): boolean {
  return generation === gatewayApiKeyValidationCacheGeneration
}

async function syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation: number): Promise<void> {
  if (
    runtimeConfig.runtimeStateDriver === 'redis'
    && isGatewayApiKeyValidationGenerationCurrent(generation)
  ) {
    await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  }
}

function isGatewayApiKeyRowExpired(row: GatewayApiKeyRow, now = Date.now()): boolean {
  if (!row.expires_at) return false
  const expiresAt = Date.parse(row.expires_at)
  return Number.isFinite(expiresAt) && expiresAt <= now
}

function gatewayApiKeyCacheTtlMs(now: number, row: GatewayApiKeyRow): number {
  let ttlMs = GATEWAY_API_KEY_CACHE_TTL_MS
  if (row.expires_at) {
    const keyExpiresAt = Date.parse(row.expires_at)
    if (Number.isFinite(keyExpiresAt)) {
      ttlMs = Math.min(ttlMs, keyExpiresAt - now)
    }
  }
  return Math.max(1, ttlMs)
}

function setGatewayApiKeyCacheEntry(
  keyHash: string,
  entry: GatewayApiKeyCacheEntry,
  options: { ttlMs?: number; skipSharedCache?: boolean } = {}
): void {
  if (runtimeConfig.cacheDriver === 'redis') {
    gatewayApiKeyCacheKeysById.clear()
    if (!options.skipSharedCache) {
      throw new Error('高性能模式禁止同步写入 API Key Redis 共享缓存，必须使用 setGatewayApiKeyCacheEntryAsync')
    }
    return
  }
  const previous = gatewayApiKeyCache.get(keyHash)
  if (previous) {
    removeGatewayApiKeyCacheIndex(previous.row.id, keyHash)
  }
  gatewayApiKeyCache.set(keyHash, {
    row: cloneGatewayApiKeyRow(entry.row),
    forceRevalidateAtMs: entry.forceRevalidateAtMs
  }, options)
  addGatewayApiKeyCacheIndex(entry.row.id, keyHash)
}

async function setGatewayApiKeyCacheEntryAsync(
  keyHash: string,
  entry: GatewayApiKeyCacheEntry,
  options: { ttlMs?: number; expectedGeneration?: number } = {}
): Promise<boolean> {
  if (
    options.expectedGeneration !== undefined
    && !isGatewayApiKeyValidationGenerationCurrent(options.expectedGeneration)
  ) {
    return false
  }
  await setGatewayApiKeySharedCacheEntry(keyHash, entry, options)
  if (options.expectedGeneration !== undefined) {
    await syncGatewayApiKeyValidationGenerationAfterAsyncRead(options.expectedGeneration)
  }
  if (
    options.expectedGeneration !== undefined
    && !isGatewayApiKeyValidationGenerationCurrent(options.expectedGeneration)
  ) {
    await clearGatewayApiKeySharedCacheAsync()
    return false
  }
  gatewayApiKeyProcessCache.set(keyHash, {
    row: cloneGatewayApiKeyRow(entry.row),
    forceRevalidateAtMs: entry.forceRevalidateAtMs
  }, options)
  setGatewayApiKeyCacheEntry(keyHash, entry, {
    ...options,
    skipSharedCache: true
  })
  return true
}

registerGatewayRuntimeCacheInvalidator((reason) => {
  if (shouldInvalidateGatewayApiKeyProcessCache(reason)) {
    advanceGatewayApiKeyValidationCacheGeneration()
    gatewayApiKeyProcessCache.clear()
  }
})

registerGatewayApiKeyValidationCacheInvalidator(async (apiKeyId, metadata) => {
  if (metadata.source === 'local' && apiKeyId) {
    await invalidateGatewayApiKeyCacheByIdAsync(apiKeyId)
    return
  }
  await clearGatewayApiKeyValidationCacheAsync()
})

function shouldInvalidateGatewayApiKeyProcessCache(reason: string): boolean {
  return new Set([
    'api_key_created',
    'api_key_updated',
    'api_key_secret_refreshed',
    'api_key_deleted',
    'route_strategy_created',
    'route_strategy_updated',
    'route_strategy_deleted',
    'group_created',
    'group_updated',
    'group_deleted',
    'group_authorization_settings_updated',
    'resource_authorization_created',
    'resource_authorization_updated',
    'resource_authorization_revoked',
    'resource_authorization_returned',
    'authorization_expired',
    'team_authorization_changed',
    'team_members_changed',
    'system_account_status_changed',
    'system_account_image_generation_changed',
    'system_account_request_limits_changed'
  ]).has(reason)
}

function addGatewayApiKeyCacheIndex(apiKeyId: string, keyHash: string): void {
  const keyHashes = gatewayApiKeyCacheKeysById.get(apiKeyId) ?? new Set<string>()
  keyHashes.add(keyHash)
  gatewayApiKeyCacheKeysById.set(apiKeyId, keyHashes)
}

function removeGatewayApiKeyCacheIndex(apiKeyId: string, keyHash: string): void {
  const keyHashes = gatewayApiKeyCacheKeysById.get(apiKeyId)
  if (!keyHashes) return
  keyHashes.delete(keyHash)
  if (!keyHashes.size) {
    gatewayApiKeyCacheKeysById.delete(apiKeyId)
  }
}

export function loadActiveGatewayApiKeyGroupBindings(apiKeyId: string, routeStrategyId: string, systemAccountId: string): GatewayApiKeyGroupBindingRow[] {
  const now = nowIso()
  return getBusinessDatabase().prepare(`
    SELECT
      route_strategy_groups.id,
      ? AS api_key_id,
      route_strategy_groups.system_account_id,
      route_strategy_groups.group_id,
      route_strategy_groups.priority,
      route_strategy_groups.weight,
      route_strategy_groups.status,
      groups.provider_code,
      groups.enabled AS group_enabled
    FROM route_strategies
    INNER JOIN route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
    INNER JOIN groups
      ON groups.id = route_strategy_groups.group_id
      LEFT JOIN resource_authorizations group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
        AND group_authorization_settings.group_id = groups.id
    WHERE route_strategies.id = ?
      AND route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategy_groups.status = 'active'
      AND groups.enabled = 1
      AND (
        groups.system_account_id = route_strategy_groups.system_account_id
        OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
      )
    ORDER BY route_strategy_groups.priority ASC, route_strategy_groups.created_at ASC, route_strategy_groups.id ASC
    LIMIT ?
  `).all(apiKeyId, now, routeStrategyId, systemAccountId, maxRouteStrategyGroupBindings)
    .map((row) => ({
      ...(row as unknown as GatewayApiKeyGroupBindingRow),
      weight: normalizeApiKeyGroupBindingWeight((row as unknown as GatewayApiKeyGroupBindingRow).weight)
    })) as GatewayApiKeyGroupBindingRow[]
}

export async function loadActiveGatewayApiKeyGroupBindingsAsync(
  apiKeyId: string,
  routeStrategyId: string,
  systemAccountId: string,
  client?: DatabaseClient
): Promise<GatewayApiKeyGroupBindingRow[]> {
  const activeClient = client ?? await getGatewayApiKeyDatabaseClient()
  if (activeClient.driver === 'sqlite') {
    return loadActiveGatewayApiKeyGroupBindings(apiKeyId, routeStrategyId, systemAccountId)
  }
  const now = nowIso()
  const rows = await activeClient.query<GatewayApiKeyGroupBindingRow>(`
    SELECT
      route_strategy_groups.id,
      ? AS api_key_id,
      route_strategy_groups.system_account_id,
      route_strategy_groups.group_id,
      route_strategy_groups.priority,
      route_strategy_groups.weight,
      route_strategy_groups.status,
      groups.provider_code,
      groups.enabled AS group_enabled
    FROM ${gatewayApiKeyTable(activeClient, 'route_strategies')} route_strategies
    INNER JOIN ${gatewayApiKeyTable(activeClient, 'route_strategy_groups')} route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
    INNER JOIN ${gatewayApiKeyTable(activeClient, 'groups')} groups
      ON groups.id = route_strategy_groups.group_id
      LEFT JOIN ${gatewayApiKeyTable(activeClient, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${gatewayApiKeyTable(activeClient, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
        AND group_authorization_settings.group_id = groups.id
    WHERE route_strategies.id = ?
      AND route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategy_groups.status = 'active'
      AND groups.enabled = 1
      AND (
        groups.system_account_id = route_strategy_groups.system_account_id
        OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
      )
    ORDER BY route_strategy_groups.priority ASC, route_strategy_groups.created_at ASC, route_strategy_groups.id ASC
    LIMIT ?
  `, [apiKeyId, now, routeStrategyId, systemAccountId, maxRouteStrategyGroupBindings])
  return rows.map((row) => ({
    ...row,
    weight: normalizeApiKeyGroupBindingWeight(row.weight)
  }))
}

async function getGatewayApiKeyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function gatewayApiKeyTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function cloneGatewayApiKeyRow(row: GatewayApiKeyRow): GatewayApiKeyRow {
  return {
    ...row,
    normal_routing_config: row.normal_routing_config
      ? {
        ...row.normal_routing_config,
        speedFirstConfig: row.normal_routing_config.speedFirstConfig
          ? { ...row.normal_routing_config.speedFirstConfig }
          : undefined
      }
      : undefined,
    hybrid_routing_config: row.hybrid_routing_config
      ? {
        ...row.hybrid_routing_config,
        levelRoutes: row.hybrid_routing_config.levelRoutes.map((route) => ({ ...route }))
      }
      : undefined,
    group_bindings: row.group_bindings?.map((binding) => ({ ...binding }))
  }
}

async function getGatewayApiKeySharedCacheEntry(keyHash: string): Promise<GatewayApiKeyCacheEntry | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const entry = await gatewayApiKeySharedCache.get(keyHash)
  return entry
    ? {
        row: cloneGatewayApiKeyRow(entry.row),
        forceRevalidateAtMs: entry.forceRevalidateAtMs
      }
    : undefined
}

async function setGatewayApiKeySharedCacheEntry(
  keyHash: string,
  entry: GatewayApiKeyCacheEntry,
  options: { ttlMs?: number } = {}
): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await gatewayApiKeySharedCache.set(keyHash, {
    row: cloneGatewayApiKeyRow(entry.row),
    forceRevalidateAtMs: entry.forceRevalidateAtMs
  }, { ttlMs: options.ttlMs ?? GATEWAY_API_KEY_CACHE_TTL_MS })
}

function clearGatewayApiKeySharedCache(): void {
  clearSharedJsonCacheInBackground(
    gatewayApiKeySharedCache,
    'gateway_api_key_shared_cache_clear_failed',
    '清理 Redis shared API Key 缓存失败'
  )
}

async function clearGatewayApiKeySharedCacheAsync(): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await gatewayApiKeySharedCache.clear()
}
