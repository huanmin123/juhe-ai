import { createAppCache } from '../shared/cache.js'
import { hashSecret } from './crypto.js'
import { getDatabase } from './database.js'

export interface GatewayApiKeyRow {
  id: string
  system_account_id: string
  group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
  quota_limits_json: string | null
  system_account_image_generation_enabled: number
  group_bindings?: GatewayApiKeyGroupBindingRow[]
}

export interface GatewayApiKeyGroupBindingRow {
  id: string
  api_key_id: string
  system_account_id: string
  group_id: string
  priority: number
  status: 'active' | 'disabled'
  provider_code: string
  group_enabled: number
}

type GatewayApiKeyCacheEntry = {
  row: GatewayApiKeyRow
  forceRevalidateAtMs: number
}

const GATEWAY_API_KEY_CACHE_TTL_MS = 60_000
const GATEWAY_API_KEY_CACHE_MAX_STALE_MS = 5 * 60_000
const gatewayApiKeyCache = createAppCache<string, GatewayApiKeyCacheEntry>({
  name: 'gateway:api-key-validation',
  max: 10000,
  ttlMs: GATEWAY_API_KEY_CACHE_TTL_MS,
  updateAgeOnGet: true
})

export function validateGatewayApiKey(key: string): GatewayApiKeyRow | undefined {
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  const now = Date.now()
  const cached = gatewayApiKeyCache.get(keyHash)
  if (cached && cached.forceRevalidateAtMs > now && !isGatewayApiKeyRowExpired(cached.row, now)) {
    return cached.row
  }

  const row = getDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      api_keys.group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN groups
      ON groups.id = api_keys.group_id
      AND groups.system_account_id = api_keys.system_account_id
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
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.system_account_id)
  if (!row.group_bindings.length) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  row.group_id = row.group_bindings[0]?.group_id ?? row.group_id
  gatewayApiKeyCache.set(keyHash, {
    row,
    forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
  }, { ttlMs: gatewayApiKeyCacheTtlMs(now, row) })
  return row
}

export function findActiveGatewayApiKeyById(id: string): GatewayApiKeyRow | undefined {
  const apiKeyId = id.trim()
  if (!apiKeyId) return undefined
  const row = getDatabase().prepare(`
    SELECT
      api_keys.id,
      api_keys.system_account_id,
      api_keys.group_id,
      api_keys.status,
      api_keys.expires_at,
      api_keys.quota_limits_json,
      system_accounts.image_generation_enabled AS system_account_image_generation_enabled
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN groups
      ON groups.id = api_keys.group_id
      AND groups.system_account_id = api_keys.system_account_id
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
  row.group_bindings = loadActiveGatewayApiKeyGroupBindings(row.id, row.system_account_id)
  if (!row.group_bindings.length) {
    return undefined
  }
  row.group_id = row.group_bindings[0]?.group_id ?? row.group_id
  return row
}

export function clearGatewayApiKeyValidationCache(): void {
  gatewayApiKeyCache.clear()
}

export function invalidateGatewayApiKeyCacheById(id: string): void {
  for (const [keyHash, entry] of gatewayApiKeyCache.entries()) {
    if (entry.row.id === id) {
      gatewayApiKeyCache.delete(keyHash)
    }
  }
}

function isGatewayApiKeyRowExpired(row: GatewayApiKeyRow, now = Date.now()): boolean {
  if (!row.expires_at) return false
  const expiresAt = Date.parse(row.expires_at)
  return Number.isFinite(expiresAt) && expiresAt <= now
}

function gatewayApiKeyCacheTtlMs(now: number, row: GatewayApiKeyRow): number {
  if (!row.expires_at) return GATEWAY_API_KEY_CACHE_TTL_MS
  const keyExpiresAt = Date.parse(row.expires_at)
  return Number.isFinite(keyExpiresAt) ? Math.max(1, Math.min(GATEWAY_API_KEY_CACHE_TTL_MS, keyExpiresAt - now)) : GATEWAY_API_KEY_CACHE_TTL_MS
}

export function loadActiveGatewayApiKeyGroupBindings(apiKeyId: string, systemAccountId: string): GatewayApiKeyGroupBindingRow[] {
  return getDatabase().prepare(`
    SELECT
      api_key_group_bindings.id,
      api_key_group_bindings.api_key_id,
      api_key_group_bindings.system_account_id,
      api_key_group_bindings.group_id,
      api_key_group_bindings.priority,
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
  `).all(apiKeyId, systemAccountId) as unknown as GatewayApiKeyGroupBindingRow[]
}
