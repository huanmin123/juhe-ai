import { createAppCache } from '../../shared/cache.js'
import { registerApiKeyQuotaCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../config/runtime.js'
import { getStatsDatabase } from '../../storage/database.js'
import type { GatewayApiKeyRow } from '../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../storage/request-quota-limits.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { isRequestQuotaExceeded, loadRequestQuotaCosts } from './request-quota-checker.js'
import { readGatewayQuotaCostsSnapshot } from './gateway-quota-snapshot-cache.service.js'

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
  updateAgeOnGet: true
})

export function checkGatewayApiKeyQuota(apiKey: GatewayApiKeyRow, now = new Date()): ApiKeyQuotaDecision {
  assertLocalGatewayDatabaseAccess('checkGatewayApiKeyQuota')
  const quotaLimits = parseRequestQuotaLimitsJson(apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(quotaLimits)) {
    return { allowed: true }
  }

  const cacheKey = apiKeyQuotaCacheKey(apiKey)
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
  apiKeyQuotaCache.set(cacheKey, decision)
  return decision
}

export async function checkGatewayApiKeyQuotaAsync(apiKey: GatewayApiKeyRow): Promise<ApiKeyQuotaDecision> {
  const quotaLimits = parseRequestQuotaLimitsJson(apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(quotaLimits)) {
    return { allowed: true }
  }
  const cacheKey = apiKeyQuotaCacheKey(apiKey)
  const cached = apiKeyQuotaCache.get(cacheKey)
  if (cached) {
    return cached
  }
  if (runtimeConfig.processRole === 'server') {
    const costs = readGatewayQuotaCostsSnapshot({
      systemAccountId: apiKey.system_account_id,
      scopeType: 'api_key',
      scopeId: apiKey.id,
      hourlyWindowHours: quotaLimits.hourly?.hours
    })
    const allowed = costs ? !isRequestQuotaExceeded(quotaLimits, costs) : true
    const passiveDecision: ApiKeyQuotaCacheEntry = {
      allowed,
      message: allowed ? undefined : API_KEY_QUOTA_EXCEEDED_MESSAGE,
      checkedAtMs: Date.now()
    }
    apiKeyQuotaCache.set(cacheKey, passiveDecision)
    return passiveDecision
  }
  const decision = await requestDbService({
    type: 'check_api_key_quota',
    apiKey
  })
  apiKeyQuotaCache.set(cacheKey, {
    ...decision,
    checkedAtMs: Date.now()
  })
  return decision
}

export function clearApiKeyQuotaCache(): void {
  apiKeyQuotaCache.clear()
}

export function invalidateApiKeyQuotaCacheById(id: string): void {
  for (const [cacheKey] of apiKeyQuotaCache.entries()) {
    if (cacheKey.includes(`\u0000${id}\u0000`)) {
      apiKeyQuotaCache.delete(cacheKey)
    }
  }
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}

function apiKeyQuotaCacheKey(apiKey: GatewayApiKeyRow): string {
  return `${apiKey.system_account_id}\u0000${apiKey.id}\u0000${apiKey.quota_limits_json ?? ''}`
}

registerApiKeyQuotaCacheInvalidator((apiKeyId) => {
  if (apiKeyId) {
    invalidateApiKeyQuotaCacheById(apiKeyId)
    return
  }
  clearApiKeyQuotaCache()
})
