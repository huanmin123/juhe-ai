import { errorLogFields, logger } from './logger.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, runAfterDatabaseCommit } from '../storage/database.js'

type CacheInvalidationHandler = () => void
type ApiKeyQuotaInvalidationHandler = (apiKeyId?: string) => void

const gatewayRuntimeCacheInvalidators = new Set<CacheInvalidationHandler>()
const authorizationQuotaCacheInvalidators = new Set<CacheInvalidationHandler>()
const apiKeyQuotaCacheInvalidators = new Set<ApiKeyQuotaInvalidationHandler>()

export function registerGatewayRuntimeCacheInvalidator(handler: CacheInvalidationHandler): () => void {
  gatewayRuntimeCacheInvalidators.add(handler)
  return () => {
    gatewayRuntimeCacheInvalidators.delete(handler)
  }
}

export function registerAuthorizationQuotaCacheInvalidator(handler: CacheInvalidationHandler): () => void {
  authorizationQuotaCacheInvalidators.add(handler)
  return () => {
    authorizationQuotaCacheInvalidators.delete(handler)
  }
}

export function registerApiKeyQuotaCacheInvalidator(handler: ApiKeyQuotaInvalidationHandler): () => void {
  apiKeyQuotaCacheInvalidators.add(handler)
  return () => {
    apiKeyQuotaCacheInvalidators.delete(handler)
  }
}

export function notifyGatewayRuntimeCacheInvalidation(reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    runCacheInvalidators('gateway_runtime_cache', reason, gatewayRuntimeCacheInvalidators, (handler) => handler())
  })
}

export function notifyAuthorizationQuotaCacheInvalidation(reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    runCacheInvalidators('authorization_quota_cache', reason, authorizationQuotaCacheInvalidators, (handler) => handler())
  })
}

export function notifyApiKeyQuotaCacheInvalidation(apiKeyId: string | undefined, reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    runCacheInvalidators('api_key_quota_cache', reason, apiKeyQuotaCacheInvalidators, (handler) => handler(apiKeyId), { apiKeyId })
  })
}

export function runGatewayCacheInvalidatorsAfterCommit(effect: () => void): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    effect()
    return
  }
  runAfterDatabaseCommit(effect, getBusinessDatabase())
}

function runCacheInvalidators<THandler>(
  cacheName: string,
  reason: string,
  handlers: Set<THandler>,
  invoke: (handler: THandler) => void,
  fields: Record<string, unknown> = {}
): void {
  for (const handler of handlers) {
    try {
      invoke(handler)
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'gateway_cache_invalidation_failed',
        cacheName,
        reason,
        ...fields
      }), '网关缓存失效通知失败')
    }
  }
}
