import { createAppCache } from '../shared/cache.js'
import {
  apiKeyScheduleCacheTtlMs,
  evaluateApiKeyAvailabilitySchedule,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'
import { maxApiKeyGroupBindings } from './api-key-group-binding-limits.js'
import {
  normalizeApiKeyGroupBindingWeight,
  normalizeApiKeyGroupRouteStrategy
} from '../domain/api-key-routing.js'
import type { ApiKeyGroupRouteStrategy } from '../domain/types.js'
import { hashSecret } from './crypto.js'
import { getBusinessDatabase } from './database.js'

export interface GatewayApiKeyRow {
  id: string
  system_account_id: string
  selected_group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
  quota_limits_json: string | null
  group_route_strategy: ApiKeyGroupRouteStrategy
  availability_schedule_json?: string | null
  availability_schedule_active?: number
  system_account_image_generation_enabled: number
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
  scheduleRevalidateAtMs?: number
}

const GATEWAY_API_KEY_CACHE_TTL_MS = 60_000
const GATEWAY_API_KEY_CACHE_MAX_STALE_MS = 5 * 60_000
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
const gatewayApiKeyCacheKeysById = new Map<string, Set<string>>()

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
    && isGatewayApiKeyCacheEntryScheduleFresh(cached, now)
  ) {
    return cached.row
  }

  const row = getBusinessDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      api_keys.group_route_strategy,
      api_keys.availability_schedule_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    WHERE api_keys.key_hash = ?
      AND system_accounts.status = 'active'
  `).get(keyHash) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row, now)) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  applyGatewayApiKeyScheduleState(row, now)
  row.group_route_strategy = normalizeApiKeyGroupRouteStrategy(row.group_route_strategy)
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.system_account_id)
  if (!row.group_bindings.length) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  setGatewayApiKeyCacheEntry(keyHash, {
    row,
    forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS,
    scheduleRevalidateAtMs: row.availability_schedule_json
      ? now + apiKeyScheduleCacheTtlMs(now)
      : undefined
  }, { ttlMs: gatewayApiKeyCacheTtlMs(now, row) })
  return row
}

export function findActiveGatewayApiKeyById(id: string): GatewayApiKeyRow | undefined {
  const apiKeyId = id.trim()
  if (!apiKeyId) return undefined
  const row = getBusinessDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      '' AS selected_group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      api_keys.group_route_strategy,
      api_keys.availability_schedule_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    WHERE api_keys.id = ?
      AND system_accounts.status = 'active'
    LIMIT 1
  `).get(apiKeyId) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row)) {
    return undefined
  }
  applyGatewayApiKeyScheduleState(row)
  row.group_route_strategy = normalizeApiKeyGroupRouteStrategy(row.group_route_strategy)
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.system_account_id)
  if (!row.group_bindings.length) {
    return undefined
  }
  row.selected_group_id = row.group_bindings[0]?.group_id ?? row.selected_group_id
  return row
}

export function clearGatewayApiKeyValidationCache(): void {
  gatewayApiKeyCache.clear()
}

export function invalidateGatewayApiKeyCacheById(id: string): void {
  const keyHashes = gatewayApiKeyCacheKeysById.get(id)
  if (!keyHashes) return
  for (const keyHash of [...keyHashes]) {
    gatewayApiKeyCache.delete(keyHash)
  }
  gatewayApiKeyCacheKeysById.delete(id)
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
  if (row.availability_schedule_json) {
    ttlMs = Math.min(ttlMs, apiKeyScheduleCacheTtlMs(now))
  }
  return Math.max(1, ttlMs)
}

function isGatewayApiKeyCacheEntryScheduleFresh(entry: GatewayApiKeyCacheEntry, now: number): boolean {
  return entry.scheduleRevalidateAtMs === undefined || entry.scheduleRevalidateAtMs > now
}

export function isGatewayApiKeyScheduleInactive(row: GatewayApiKeyRow | undefined): boolean {
  return Boolean(row?.availability_schedule_json && row.availability_schedule_active === 0)
}

function applyGatewayApiKeyScheduleState(row: GatewayApiKeyRow, now = Date.now()): void {
  const schedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
  const decision = evaluateApiKeyAvailabilitySchedule(schedule, new Date(now))
  row.availability_schedule_active = decision.allowed ? 1 : 0
}

function setGatewayApiKeyCacheEntry(keyHash: string, entry: GatewayApiKeyCacheEntry, options?: { ttlMs?: number }): void {
  const previous = gatewayApiKeyCache.get(keyHash)
  if (previous) {
    removeGatewayApiKeyCacheIndex(previous.row.id, keyHash)
  }
  gatewayApiKeyCache.set(keyHash, entry, options)
  addGatewayApiKeyCacheIndex(entry.row.id, keyHash)
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

export function loadActiveGatewayApiKeyGroupBindings(apiKeyId: string, systemAccountId: string): GatewayApiKeyGroupBindingRow[] {
  return getBusinessDatabase().prepare(`
    SELECT
      api_key_group_bindings.id,
      api_key_group_bindings.api_key_id,
      api_key_group_bindings.system_account_id,
      api_key_group_bindings.group_id,
      api_key_group_bindings.priority,
      api_key_group_bindings.weight,
      api_key_group_bindings.status,
      groups.provider_code,
      groups.enabled AS group_enabled
    FROM api_key_group_bindings
    INNER JOIN groups
      ON groups.id = api_key_group_bindings.group_id
      AND groups.system_account_id = api_key_group_bindings.system_account_id
    WHERE api_key_group_bindings.api_key_id = ?
      AND api_key_group_bindings.system_account_id = ?
      AND api_key_group_bindings.status = 'active'
      AND groups.enabled = 1
    ORDER BY api_key_group_bindings.priority ASC, api_key_group_bindings.created_at ASC, api_key_group_bindings.id ASC
    LIMIT ?
  `).all(apiKeyId, systemAccountId, maxApiKeyGroupBindings)
    .map((row) => ({
      ...(row as unknown as GatewayApiKeyGroupBindingRow),
      weight: normalizeApiKeyGroupBindingWeight((row as unknown as GatewayApiKeyGroupBindingRow).weight)
    })) as GatewayApiKeyGroupBindingRow[]
}
