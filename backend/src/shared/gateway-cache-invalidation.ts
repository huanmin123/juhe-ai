import { errorLogFields, logger } from './logger.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, runAfterDatabaseCommit } from '../storage/database.js'
import { createRuntimeStateStore } from './runtime-state-store.js'
import { clearLocalApiKeyLookupCache } from '../storage/repository-lookups.js'

export type GatewayRuntimeCacheInvalidationMetadata =
  | {
      source: 'local'
      keyHashes?: readonly string[]
    }
  | {
      source: 'runtime_state'
      version: string
      publishedAt: string
    }

type GatewayRuntimeCacheInvalidationResult = void | false
type GatewayRuntimeCacheInvalidationHandler = (
  reason: string,
  metadata: GatewayRuntimeCacheInvalidationMetadata
) => GatewayRuntimeCacheInvalidationResult | Promise<GatewayRuntimeCacheInvalidationResult>
type GatewayApiKeyValidationCacheInvalidationHandler = (
  apiKeyId: string | undefined,
  metadata: GatewayRuntimeCacheInvalidationMetadata
) => GatewayRuntimeCacheInvalidationResult | Promise<GatewayRuntimeCacheInvalidationResult>
type CacheInvalidationMetadata = {
  publishedAt?: string
}
type CacheInvalidationHandler = (metadata?: CacheInvalidationMetadata) => void
type ApiKeyQuotaInvalidationHandler = (apiKeyId?: string) => void
type GatewayApiKeyValidationServerInvalidator = (
  apiKeyId: string | undefined,
  keyHashes: readonly string[]
) => Promise<void>
type GatewayCacheInvalidationTopic =
  | 'gateway_runtime_cache'
  | 'gateway_api_key_validation_cache'
  | 'authorization_quota_cache'
  | 'api_key_quota_cache'

interface GatewayCacheInvalidationState {
  version: string
  reason: string
  apiKeyId?: string
  publishedAt: string
}

const gatewayRuntimeCacheInvalidators = new Set<GatewayRuntimeCacheInvalidationHandler>()
const gatewayApiKeyValidationCacheInvalidators = new Set<GatewayApiKeyValidationCacheInvalidationHandler>()
const authorizationQuotaCacheInvalidators = new Set<CacheInvalidationHandler>()
const apiKeyQuotaCacheInvalidators = new Set<ApiKeyQuotaInvalidationHandler>()
const gatewayCacheInvalidationState = createRuntimeStateStore('gateway_cache_invalidation')
const gatewayCacheInvalidationStateTtlMs = 24 * 60 * 60 * 1000
const gatewayCacheInvalidationSyncIntervalMs = 1000
const gatewayCacheInvalidationTopics: GatewayCacheInvalidationTopic[] = [
  'gateway_runtime_cache',
  'gateway_api_key_validation_cache',
  'authorization_quota_cache',
  'api_key_quota_cache'
]

const lastSeenGatewayCacheInvalidationVersions = new Map<GatewayCacheInvalidationTopic, string>()
const deferredGatewayCacheInvalidationTopics = new Set<GatewayCacheInvalidationTopic>()
let lastGatewayCacheInvalidationSyncAt = 0
let gatewayCacheInvalidationSyncPromise: Promise<void> | undefined
let gatewayApiKeyValidationServerInvalidator: GatewayApiKeyValidationServerInvalidator | undefined

export function registerGatewayRuntimeCacheInvalidator(handler: GatewayRuntimeCacheInvalidationHandler): () => void {
  gatewayRuntimeCacheInvalidators.add(handler)
  return () => {
    gatewayRuntimeCacheInvalidators.delete(handler)
  }
}

export function registerGatewayApiKeyValidationCacheInvalidator(
  handler: GatewayApiKeyValidationCacheInvalidationHandler
): () => void {
  gatewayApiKeyValidationCacheInvalidators.add(handler)
  return () => {
    gatewayApiKeyValidationCacheInvalidators.delete(handler)
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

export function registerGatewayApiKeyValidationServerInvalidator(
  handler: GatewayApiKeyValidationServerInvalidator
): () => void {
  gatewayApiKeyValidationServerInvalidator = handler
  return () => {
    if (gatewayApiKeyValidationServerInvalidator === handler) {
      gatewayApiKeyValidationServerInvalidator = undefined
    }
  }
}

export function notifyGatewayRuntimeCacheInvalidation(reason: string): void {
  runGatewayCacheInvalidatorsAfterCommit(() => {
    runGatewayRuntimeCacheInvalidators(reason)
    publishGatewayCacheInvalidationToRuntimeState('gateway_runtime_cache', reason)
  })
}

export async function notifyGatewayRuntimeCacheInvalidationAsync(reason: string): Promise<void> {
  if (shouldInvalidateApiKeyLookupCache(reason)) {
    clearLocalApiKeyLookupCache()
  }
  await runCacheInvalidatorsAsync(
    'gateway_runtime_cache',
    reason,
    gatewayRuntimeCacheInvalidators,
    (handler) => handler(reason, { source: 'local' })
  )
  await publishGatewayCacheInvalidationToRuntimeStateAsync('gateway_runtime_cache', reason)
}

export async function notifyGatewayApiKeyValidationCacheInvalidationAsync(
  apiKeyId: string | undefined,
  reason: string,
  keyHashes: readonly string[] = []
): Promise<void> {
  const errors: unknown[] = []
  try {
    const applied = await runCacheInvalidatorsAsync(
      'gateway_api_key_validation_cache',
      reason,
      gatewayApiKeyValidationCacheInvalidators,
      (handler) => handler(apiKeyId, { source: 'local', keyHashes }),
      { apiKeyId }
    )
    if (!applied) {
      errors.push(new Error('gateway_api_key_validation_cache 本地失效未完成'))
    }
  } catch (error) {
    errors.push(error)
  }
  try {
    await publishGatewayCacheInvalidationToRuntimeStateAsync(
      'gateway_api_key_validation_cache',
      reason,
      { apiKeyId }
    )
  } catch (error) {
    errors.push(error)
  }
  if (
    runtimeConfig.runtimeStateDriver !== 'redis'
    && runtimeConfig.processRole === 'db-service'
  ) {
    if (!gatewayApiKeyValidationServerInvalidator) {
      errors.push(new Error('gateway_api_key_validation_cache server 失效发布器未注册'))
    } else {
      try {
        await gatewayApiKeyValidationServerInvalidator(apiKeyId, keyHashes)
      } catch (error) {
        errors.push(error)
      }
    }
  }
  if (errors.length > 0) {
    throw new AggregateError(errors, `gateway_api_key_validation_cache 失效存在 ${errors.length} 个失败`)
  }
}

export async function applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync(
  apiKeyId: string | undefined,
  keyHashes: readonly string[] = []
): Promise<void> {
  const applied = await runCacheInvalidatorsAsync(
    'gateway_api_key_validation_cache',
    'api_key_ipc_invalidation',
    gatewayApiKeyValidationCacheInvalidators,
    (handler) => handler(apiKeyId, { source: 'local', keyHashes }),
    { apiKeyId }
  )
  if (!applied) {
    throw new Error('gateway_api_key_validation_cache server IPC 失效未完成')
  }
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
  invoke: (handler: THandler) => GatewayRuntimeCacheInvalidationResult | Promise<GatewayRuntimeCacheInvalidationResult>,
  fields: Record<string, unknown> = {}
): void {
  for (const handler of handlers) {
    try {
      const result = invoke(handler)
      if (result) {
        void result.catch((error) => {
          logCacheInvalidationFailure(error, cacheName, reason, fields)
        })
      }
    } catch (error) {
      logCacheInvalidationFailure(error, cacheName, reason, fields)
    }
  }
}

async function runCacheInvalidatorsAsync<THandler>(
  cacheName: string,
  reason: string,
  handlers: Set<THandler>,
  invoke: (handler: THandler) => GatewayRuntimeCacheInvalidationResult | Promise<GatewayRuntimeCacheInvalidationResult>,
  fields: Record<string, unknown> = {}
): Promise<boolean> {
  const errors: unknown[] = []
  let applied = true
  for (const handler of handlers) {
    try {
      if (await invoke(handler) === false) {
        applied = false
      }
    } catch (error) {
      logCacheInvalidationFailure(error, cacheName, reason, fields)
      errors.push(error)
    }
  }
  if (errors.length > 0) {
    throw new AggregateError(errors, `${cacheName} 缓存失效存在 ${errors.length} 个 handler 失败`)
  }
  return applied
}

function logCacheInvalidationFailure(
  error: unknown,
  cacheName: string,
  reason: string,
  fields: Record<string, unknown>
): void {
  logger.warn(errorLogFields(error, {
    event: 'gateway_cache_invalidation_failed',
    cacheName,
    reason,
    ...fields
  }), '网关缓存失效通知失败')
}

function publishGatewayCacheInvalidationToRuntimeState(
  topic: GatewayCacheInvalidationTopic,
  reason: string,
  fields: { apiKeyId?: string; publishedAt?: string } = {}
): void {
  void publishGatewayCacheInvalidationToRuntimeStateAsync(topic, reason, fields).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_cache_invalidation_runtime_state_publish_failed',
      cacheName: topic,
      reason,
      apiKeyId: fields.apiKeyId
    }), '发布 Redis runtime state 网关缓存失效版本失败')
  })
}

async function publishGatewayCacheInvalidationToRuntimeStateAsync(
  topic: GatewayCacheInvalidationTopic,
  reason: string,
  fields: { apiKeyId?: string; publishedAt?: string } = {}
): Promise<void> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  const state: GatewayCacheInvalidationState = {
    version: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    reason,
    apiKeyId: fields.apiKeyId,
    publishedAt: fields.publishedAt ?? new Date().toISOString()
  }
  if (!deferredGatewayCacheInvalidationTopics.has(topic)) {
    lastSeenGatewayCacheInvalidationVersions.set(topic, state.version)
  }
  await gatewayCacheInvalidationState.setJson(gatewayCacheInvalidationStateKey(topic), state, gatewayCacheInvalidationStateTtlMs)
}

async function syncGatewayCacheInvalidationsFromRuntimeStateUnsafe(): Promise<void> {
  const states = await gatewayCacheInvalidationState.getJsonMany<GatewayCacheInvalidationState>(
    gatewayCacheInvalidationTopics.map(gatewayCacheInvalidationStateKey)
  )
  for (const [index, topic] of gatewayCacheInvalidationTopics.entries()) {
    const state = states[index]
    if (!state?.version) continue
    if (lastSeenGatewayCacheInvalidationVersions.get(topic) === state.version) continue
    try {
      const applied = await applyRuntimeStateCacheInvalidation(topic, state)
      if (applied) {
        deferredGatewayCacheInvalidationTopics.delete(topic)
        lastSeenGatewayCacheInvalidationVersions.set(topic, state.version)
      } else {
        deferredGatewayCacheInvalidationTopics.add(topic)
      }
    } catch (error) {
      deferredGatewayCacheInvalidationTopics.add(topic)
      throw error
    }
  }
}

async function applyRuntimeStateCacheInvalidation(
  topic: GatewayCacheInvalidationTopic,
  state: GatewayCacheInvalidationState
): Promise<boolean> {
  if (topic === 'gateway_runtime_cache') {
    return runGatewayRuntimeCacheInvalidatorsAsync(state.reason, {
      source: 'runtime_state',
      version: state.version,
      publishedAt: state.publishedAt
    })
  }
  if (topic === 'gateway_api_key_validation_cache') {
    return runCacheInvalidatorsAsync(
      topic,
      state.reason,
      gatewayApiKeyValidationCacheInvalidators,
      (handler) => handler(undefined, {
        source: 'runtime_state',
        version: state.version,
        publishedAt: state.publishedAt
      }),
      { apiKeyId: state.apiKeyId }
    )
  }
  if (topic === 'authorization_quota_cache') {
    runCacheInvalidators(topic, state.reason, authorizationQuotaCacheInvalidators, (handler) => handler({ publishedAt: state.publishedAt }))
    return true
  }
  runCacheInvalidators(topic, state.reason, apiKeyQuotaCacheInvalidators, (handler) => handler(state.apiKeyId), {
    apiKeyId: state.apiKeyId
  })
  return true
}

function runGatewayRuntimeCacheInvalidators(reason: string): void {
  if (shouldInvalidateApiKeyLookupCache(reason)) {
    clearLocalApiKeyLookupCache()
  }
  runCacheInvalidators(
    'gateway_runtime_cache',
    reason,
    gatewayRuntimeCacheInvalidators,
    (handler) => handler(reason, { source: 'local' })
  )
}

async function runGatewayRuntimeCacheInvalidatorsAsync(
  reason: string,
  metadata: GatewayRuntimeCacheInvalidationMetadata
): Promise<boolean> {
  if (shouldInvalidateApiKeyLookupCache(reason)) {
    clearLocalApiKeyLookupCache()
  }
  return runCacheInvalidatorsAsync(
    'gateway_runtime_cache',
    reason,
    gatewayRuntimeCacheInvalidators,
    (handler) => handler(reason, metadata)
  )
}

function shouldInvalidateApiKeyLookupCache(reason: string): boolean {
  return reason === 'api_key_created'
    || reason === 'api_key_updated'
    || reason === 'api_key_secret_refreshed'
    || reason === 'api_key_deleted'
}

function gatewayCacheInvalidationStateKey(topic: GatewayCacheInvalidationTopic): string {
  return `topic:${topic}`
}
