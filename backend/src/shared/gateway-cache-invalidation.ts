import { errorLogFields, logger } from './logger.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, runAfterDatabaseCommit } from '../storage/database.js'
import { scheduleProcessFatalError } from './process-fatal.js'
import { createRuntimeStateStore } from './runtime-state-store.js'

type GatewayRuntimeCacheInvalidationHandler = (reason: string) => void
type CacheInvalidationMetadata = {
  publishedAt?: string
}
type CacheInvalidationHandler = (metadata?: CacheInvalidationMetadata) => void
type ApiKeyQuotaInvalidationHandler = (apiKeyId?: string) => void
type GatewayCacheInvalidationTopic = 'gateway_runtime_cache' | 'authorization_quota_cache' | 'api_key_quota_cache'

interface GatewayCacheInvalidationState {
  version: string
  reason: string
  apiKeyId?: string
  publishedAt: string
}

const gatewayRuntimeCacheInvalidators = new Set<GatewayRuntimeCacheInvalidationHandler>()
const authorizationQuotaCacheInvalidators = new Set<CacheInvalidationHandler>()
const apiKeyQuotaCacheInvalidators = new Set<ApiKeyQuotaInvalidationHandler>()
const gatewayCacheInvalidationState = createRuntimeStateStore('gateway_cache_invalidation')
const gatewayCacheInvalidationStateTtlMs = 24 * 60 * 60 * 1000
const gatewayCacheInvalidationSyncIntervalMs = 1000
const gatewayCacheInvalidationTopics: GatewayCacheInvalidationTopic[] = [
  'gateway_runtime_cache',
  'authorization_quota_cache',
  'api_key_quota_cache'
]

const lastSeenGatewayCacheInvalidationVersions = new Map<GatewayCacheInvalidationTopic, string>()
let lastGatewayCacheInvalidationSyncAt = 0
let gatewayCacheInvalidationSyncPromise: Promise<void> | undefined

export function registerGatewayRuntimeCacheInvalidator(handler: GatewayRuntimeCacheInvalidationHandler): () => void {
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
    runCacheInvalidators('gateway_runtime_cache', reason, gatewayRuntimeCacheInvalidators, (handler) => handler(reason))
    publishGatewayCacheInvalidationToRuntimeState('gateway_runtime_cache', reason)
  })
}

export function notifyAuthorizationQuotaCacheInvalidation(reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    const publishedAt = new Date().toISOString()
    runCacheInvalidators('authorization_quota_cache', reason, authorizationQuotaCacheInvalidators, (handler) => handler({ publishedAt }))
    publishGatewayCacheInvalidationToRuntimeState('authorization_quota_cache', reason, { publishedAt })
  })
}

export function notifyApiKeyQuotaCacheInvalidation(apiKeyId: string | undefined, reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    runCacheInvalidators('api_key_quota_cache', reason, apiKeyQuotaCacheInvalidators, (handler) => handler(apiKeyId), { apiKeyId })
    publishGatewayCacheInvalidationToRuntimeState('api_key_quota_cache', reason, { apiKeyId })
  })
}

export function runGatewayCacheInvalidatorsAfterCommit(effect: () => void): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    effect()
    return
  }
  runAfterDatabaseCommit(effect, getBusinessDatabase())
}

export async function syncGatewayCacheInvalidationsFromRuntimeState(options: { force?: boolean } = {}): Promise<void> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  const now = Date.now()
  if (!options.force && now - lastGatewayCacheInvalidationSyncAt < gatewayCacheInvalidationSyncIntervalMs) {
    return gatewayCacheInvalidationSyncPromise
  }
  lastGatewayCacheInvalidationSyncAt = now
  if (!gatewayCacheInvalidationSyncPromise) {
    gatewayCacheInvalidationSyncPromise = syncGatewayCacheInvalidationsFromRuntimeStateUnsafe()
      .catch((error) => {
        logger.warn(errorLogFields(error, {
          event: 'gateway_cache_invalidation_runtime_state_sync_failed'
        }), '同步 Redis runtime state 网关缓存失效版本失败')
        throw error
      })
      .finally(() => {
        gatewayCacheInvalidationSyncPromise = undefined
      })
  }
  return gatewayCacheInvalidationSyncPromise
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

function publishGatewayCacheInvalidationToRuntimeState(
  topic: GatewayCacheInvalidationTopic,
  reason: string,
  fields: { apiKeyId?: string; publishedAt?: string } = {}
): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  const state: GatewayCacheInvalidationState = {
    version: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    reason,
    apiKeyId: fields.apiKeyId,
    publishedAt: fields.publishedAt ?? new Date().toISOString()
  }
  lastSeenGatewayCacheInvalidationVersions.set(topic, state.version)
  void gatewayCacheInvalidationState
    .setJson(gatewayCacheInvalidationStateKey(topic), state, gatewayCacheInvalidationStateTtlMs)
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_cache_invalidation_runtime_state_publish_failed',
        cacheName: topic,
        reason,
        apiKeyId: fields.apiKeyId
      }), '发布 Redis runtime state 网关缓存失效版本失败')
      if (runtimeConfig.runtimeMode === 'performance') {
        scheduleProcessFatalError(error)
      }
    })
}

async function syncGatewayCacheInvalidationsFromRuntimeStateUnsafe(): Promise<void> {
  for (const topic of gatewayCacheInvalidationTopics) {
    const state = await gatewayCacheInvalidationState.getJson<GatewayCacheInvalidationState>(gatewayCacheInvalidationStateKey(topic))
    if (!state?.version) continue
    if (lastSeenGatewayCacheInvalidationVersions.get(topic) === state.version) continue
    lastSeenGatewayCacheInvalidationVersions.set(topic, state.version)
    applyRuntimeStateCacheInvalidation(topic, state)
  }
}

function applyRuntimeStateCacheInvalidation(topic: GatewayCacheInvalidationTopic, state: GatewayCacheInvalidationState): void {
  if (topic === 'gateway_runtime_cache') {
    runCacheInvalidators(topic, state.reason, gatewayRuntimeCacheInvalidators, (handler) => handler(state.reason))
    return
  }
  if (topic === 'authorization_quota_cache') {
    runCacheInvalidators(topic, state.reason, authorizationQuotaCacheInvalidators, (handler) => handler({ publishedAt: state.publishedAt }))
    return
  }
  runCacheInvalidators(topic, state.reason, apiKeyQuotaCacheInvalidators, (handler) => handler(state.apiKeyId), {
    apiKeyId: state.apiKeyId
  })
}

function gatewayCacheInvalidationStateKey(topic: GatewayCacheInvalidationTopic): string {
  return `topic:${topic}`
}
