import { createAppCache } from '../shared/cache.js'
import { hashSecret } from './crypto.js'
import { getDatabase, nowIso } from './database.js'

export interface GatewayApiKeyRow {
  id: string
  system_account_id: string
  group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
  quota_limits_json: string | null
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
    SELECT api_keys.id, api_keys.system_account_id, api_keys.group_id, api_keys.status, api_keys.expires_at, api_keys.quota_limits_json
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    LEFT JOIN resource_authorizations group_authorizations
      ON group_authorizations.id = api_keys.group_authorization_id
    WHERE api_keys.key_hash = ?
      AND system_accounts.status = 'active'
      AND (
        api_keys.group_authorization_id IS NULL
        OR (
          group_authorizations.status = 'active'
          AND (group_authorizations.expires_at IS NULL OR group_authorizations.expires_at > ?)
        )
      )
  `).get(keyHash, nowIso()) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row, now)) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
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
    SELECT api_keys.id, api_keys.system_account_id, api_keys.group_id, api_keys.status, api_keys.expires_at, api_keys.quota_limits_json
    FROM api_keys
    INNER JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    LEFT JOIN resource_authorizations group_authorizations
      ON group_authorizations.id = api_keys.group_authorization_id
    WHERE api_keys.id = ?
      AND system_accounts.status = 'active'
      AND (
        api_keys.group_authorization_id IS NULL
        OR (
          group_authorizations.status = 'active'
          AND (group_authorizations.expires_at IS NULL OR group_authorizations.expires_at > ?)
        )
      )
    LIMIT 1
  `).get(apiKeyId, nowIso()) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    return undefined
  }
  return isGatewayApiKeyRowExpired(row) ? undefined : row
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
