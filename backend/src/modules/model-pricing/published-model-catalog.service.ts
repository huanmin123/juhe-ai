import { randomUUID } from 'node:crypto'

import { createProcessLocalResourceCache, createSharedJsonCache } from '../../shared/cache.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  findGatewayModelCatalogSnapshotAsync,
  listGatewayModelCatalogSnapshotsAsync,
  listGatewayModelCatalogSystemAccountIdsAsync,
  pruneGatewayModelCatalogSnapshotsAsync,
  replaceGatewayModelCatalogSnapshotsAsync,
  type GatewayModelCatalogProtocol,
  type GatewayModelCatalogSnapshot,
  type GatewayModelCatalogVariant
} from '../../storage/gateway-model-catalog-snapshot.repository.js'
import { requestGatewayDbService } from '../gateway/runtime/gateway-db-service-request.js'
import { buildAnthropicModelsResponse } from '../gateway/protocols/anthropic-v1/route-helpers.js'
import { buildGeminiModelsResponse } from '../gateway/protocols/gemini-v1beta/route-helpers.js'
import { buildChatModelOptions, chatModelCapabilities, chatModelListOptions, type ChatModelCapabilities, type ChatModelListOption } from '../chat/chat-model-options.js'
import { normalizeProviderToken, OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import {
  buildCodexModelsResponseFromCatalog,
  buildOpenAIModelsResponseFromCatalog,
  listProviderModelCatalogAsync,
  type ProviderModelCatalogItem
} from './model-catalog.service.js'

interface PublishedModelCatalogCacheEntry {
  payload: object
  revision: string
  modelCount: number
}

const publishedModelCatalogCache = createSharedJsonCache<PublishedModelCatalogCacheEntry>({
  name: 'gateway:published-model-catalog',
  max: 50_000,
  ttlMs: 10 * 60_000,
  version: 'v1'
})
// The published snapshot is immutable between explicit catalog rebuilds. Keep the
// response object in-process so the hot path does not pay a Redis round trip or
// JSON parse for every /v1/models request.
const publishedModelCatalogLocalCache = createProcessLocalResourceCache<string, PublishedModelCatalogCacheEntry>({
  name: 'gateway:published-model-catalog:local',
  max: 50_000,
  ttlMs: 10 * 60_000
})
const pendingRebuildRetries = new Map<string, { timer: NodeJS.Timeout; attempt: number }>()

export async function readPublishedModelCatalogResponseAsync(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): Promise<object> {
  const cacheKey = publishedModelCatalogCacheKey(input)
  const localCached = publishedModelCatalogLocalCache.get(cacheKey)
  if (localCached?.payload) return localCached.payload
  let cached: PublishedModelCatalogCacheEntry | undefined
  try {
    cached = await publishedModelCatalogCache.get(cacheKey)
  } catch (error) {
    logger.warn({ event: 'published_model_catalog_cache_read_failed', cacheKey, error }, '模型目录缓存读取失败，回源持久化快照')
  }
  if (cached?.payload) {
    publishedModelCatalogLocalCache.set(cacheKey, cached)
    return cached.payload
  }

  return enqueueSnapshotRebuild(async () => {
    let current: PublishedModelCatalogCacheEntry | undefined
    try {
      current = await publishedModelCatalogCache.get(cacheKey)
    } catch (error) {
      logger.warn({ event: 'published_model_catalog_cache_read_failed', cacheKey, error }, '模型目录缓存读取失败，回源持久化快照')
    }
    if (current?.payload) return current.payload
    const snapshot = await readPersistedSnapshotAsync(input)
      ?? await readPersistedSnapshotAsync({ ...input, systemAccountId: '' })
    if (!snapshot) {
      throw new Error('网关发布模型目录尚未生成')
    }
    await cachePublishedSnapshot(snapshot)
    return snapshot.payload
  })
}

let snapshotRebuildTail: Promise<void> = Promise.resolve()
let allRebuildInFlight: Promise<number> | undefined
let allRebuildAgain = false
const personalRebuildInFlight = new Map<string, Promise<GatewayModelCatalogSnapshot[]>>()
const personalRebuildAgain = new Set<string>()
const maxPersonalRebuildsInFlight = 4

export function rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(
  systemAccountId: string
): Promise<GatewayModelCatalogSnapshot[]> {
  const current = personalRebuildInFlight.get(systemAccountId)
  if (current) {
    personalRebuildAgain.add(systemAccountId)
    return current
  }
  if (personalRebuildInFlight.size >= maxPersonalRebuildsInFlight) {
    return Promise.race(personalRebuildInFlight.values())
      .catch(() => undefined)
      .then(() => rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(systemAccountId))
  }
  const run = (async () => {
    let snapshots: GatewayModelCatalogSnapshot[] = []
    do {
      personalRebuildAgain.delete(systemAccountId)
      snapshots = await enqueueSnapshotRebuild(() => rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync(systemAccountId))
    } while (personalRebuildAgain.has(systemAccountId))
    return snapshots
  })()
  const settled = run.finally(() => {
    if (personalRebuildInFlight.get(systemAccountId) === settled) personalRebuildInFlight.delete(systemAccountId)
  })
  personalRebuildInFlight.set(systemAccountId, settled)
  return settled
}

async function rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync(
  systemAccountId: string
): Promise<GatewayModelCatalogSnapshot[]> {
  return rebuildPublishedModelCatalogSnapshotsForSystemAccountImplAsync(systemAccountId)
}

async function rebuildPublishedModelCatalogSnapshotsForSystemAccountImplAsync(
  systemAccountId: string
): Promise<GatewayModelCatalogSnapshot[]> {
  const [openaiCatalog, anthropicCatalog, geminiCatalog] = await Promise.all([
    publishedCatalog(OPENAI_COMPATIBLE_PROVIDER_CODE, systemAccountId),
    publishedCatalog('anthropic', systemAccountId),
    publishedCatalog('gemini', systemAccountId)
  ])
  const revision = `${Date.now()}-${Math.random().toString(16).slice(2)}`
  const chatCatalogsByProvider = new Map<string, ProviderModelCatalogItem[]>()
  for (const item of openaiCatalog) {
    const providerCode = normalizeProviderToken(item.providerCode) || item.providerCode
    const catalog = chatCatalogsByProvider.get(providerCode)
    if (catalog) catalog.push(item)
    else chatCatalogsByProvider.set(providerCode, [item])
  }
  const chatSnapshots = [...chatCatalogsByProvider.entries()].flatMap(([providerCode, providerCatalog]) => {
    const models = buildChatModelOptions(providerCatalog.map((item) => item.model), providerCatalog)
    const list = chatModelListOptions(models)
    return [
      snapshotInput('openai', chatModelListSnapshotVariant(providerCode), { defaultModel: list[0], data: list }, list.length, revision),
      ...models.map((model) => snapshotInput('openai', chatModelSnapshotVariant(providerCode, model.id), { data: chatModelCapabilities(model) }, 1, revision))
    ]
  })
  // Invalidate before replacing the durable rows. If Redis is unavailable, the
  // rebuild fails before the database changes and the retry can invalidate the
  // same old dynamic chat-model keys again.
  await publishedModelCatalogCache.clear()
  publishedModelCatalogLocalCache.clear()
  const snapshots = await replaceGatewayModelCatalogSnapshotsAsync(systemAccountId, [
    snapshotInput('openai', 'default', buildOpenAIModelsResponseFromCatalog(openaiCatalog), openaiCatalog.length, revision),
    snapshotInput('openai', 'codex', buildCodexModelsResponseFromCatalog(openaiCatalog), openaiCatalog.length, revision),
    snapshotInput('anthropic', 'default', buildAnthropicModelsResponse(anthropicCatalog), anthropicCatalog.length, revision),
    snapshotInput('gemini', 'default', buildGeminiModelsResponse(geminiCatalog), geminiCatalog.length, revision),
    ...chatSnapshots
  ])
  await Promise.all(snapshots.filter((snapshot) => !isChatModelSnapshot(snapshot.variant)).map((snapshot) => cachePublishedSnapshot(snapshot, true)))
  return snapshots
}

export interface PublishedChatModelList {
  defaultModel?: ChatModelListOption
  models: ChatModelListOption[]
}

export async function readPublishedChatModelListAsync(systemAccountId: string, providerCode: string): Promise<PublishedChatModelList> {
  let payload: object
  try {
    payload = await readPublishedModelCatalogResponseAsync({
      systemAccountId,
      protocol: 'openai',
      variant: chatModelListSnapshotVariant(providerCode)
    })
  } catch (error) {
    if (error instanceof Error && error.message === '网关发布模型目录尚未生成') return { models: [] }
    throw error
  }
  const data = 'data' in payload ? payload.data : undefined
  const models = Array.isArray(data) ? data as ChatModelListOption[] : []
  const defaultModel = 'defaultModel' in payload && payload.defaultModel && typeof payload.defaultModel === 'object'
    ? payload.defaultModel as ChatModelListOption
    : models[0]
  return { ...(defaultModel ? { defaultModel } : {}), models }
}

export async function readPublishedChatModelCapabilitiesAsync(systemAccountId: string, providerCode: string, modelId: string): Promise<ChatModelCapabilities | undefined> {
  try {
    const payload = await readPublishedModelCatalogResponseAsync({
      systemAccountId,
      protocol: 'openai',
      variant: chatModelSnapshotVariant(providerCode, modelId)
    })
    const data = 'data' in payload ? payload.data : undefined
    return data && typeof data === 'object' && !Array.isArray(data) && 'id' in data && data.id === modelId
      ? data as ChatModelCapabilities
      : undefined
  } catch (error) {
    if (error instanceof Error && error.message === '网关发布模型目录尚未生成') return undefined
    throw error
  }
}

export function rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync(
  systemAccountId?: string
): Promise<number> {
  if (systemAccountId) {
    return rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(systemAccountId)
      .then((snapshots) => snapshots.length)
  }
  if (allRebuildInFlight) {
    allRebuildAgain = true
    return allRebuildInFlight
  }
  const run = (async () => {
    let rebuilt = 0
    do {
      allRebuildAgain = false
      rebuilt = await enqueueSnapshotRebuild(() => rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync())
    } while (allRebuildAgain)
    return rebuilt
  })()
  const settled = run.finally(() => {
    if (allRebuildInFlight === settled) allRebuildInFlight = undefined
  })
  allRebuildInFlight = settled
  return settled
}

export interface EnsurePublishedModelCatalogSnapshotsResult {
  action: 'unchanged' | 'initialized'
  modelCount: number
  snapshotOwners: number
}

interface EnsurePublishedModelCatalogSnapshotsDependencies {
  findInitialSnapshot: () => Promise<GatewayModelCatalogSnapshot | undefined>
  rebuildAll: () => Promise<number>
  acquireInitializationLease?: (token: string, ttlMs: number) => Promise<boolean>
  releaseInitializationLease?: (token: string) => Promise<void>
  delay?: (milliseconds: number) => Promise<void>
  now?: () => number
  leaseCommandTimeoutMs?: number
  initializationWaitTimeoutMs?: number
  initializationWaitIntervalMs?: number
}

const publishedModelCatalogInitializationLeaseKey = 'initialization:v1'
const publishedModelCatalogInitializationLeaseTtlMs = 10 * 60_000
const publishedModelCatalogInitializationLeaseCommandTimeoutMs = 2_000
const publishedModelCatalogInitializationWaitTimeoutMs = publishedModelCatalogInitializationLeaseTtlMs
const publishedModelCatalogInitializationWaitIntervalMs = 100
const ensurePublishedModelCatalogSnapshotsInFlight = new WeakMap<
  EnsurePublishedModelCatalogSnapshotsDependencies,
  Promise<EnsurePublishedModelCatalogSnapshotsResult>
>()
const findGlobalOpenAIPublishedModelCatalogSnapshotAsync = () => findGatewayModelCatalogSnapshotAsync({
  systemAccountId: '',
  protocol: 'openai',
  variant: 'default'
})
const defaultEnsurePublishedModelCatalogSnapshotsDependencies: EnsurePublishedModelCatalogSnapshotsDependencies = {
  findInitialSnapshot: findGlobalOpenAIPublishedModelCatalogSnapshotAsync,
  // Initialization must not enter the public full-rebuild coalescer. Doing so
  // while another full rebuild is active would set allRebuildAgain and rebuild
  // the whole catalog a second time after the first initialization succeeds.
  rebuildAll: () => enqueueSnapshotRebuild(async () => {
    if (await findGlobalOpenAIPublishedModelCatalogSnapshotAsync()) return 0
    return rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync()
  })
}

export async function ensurePublishedModelCatalogSnapshotsInitializedAsync(
  dependencies: EnsurePublishedModelCatalogSnapshotsDependencies = defaultEnsurePublishedModelCatalogSnapshotsDependencies
): Promise<EnsurePublishedModelCatalogSnapshotsResult> {
  const existing = await dependencies.findInitialSnapshot()
  if (existing) return unchangedPublishedModelCatalogInitializationResult(existing)

  const current = ensurePublishedModelCatalogSnapshotsInFlight.get(dependencies)
  if (current) return current

  const run = ensurePublishedModelCatalogSnapshotsInitializedInternalAsync(dependencies)
  const settled = run.finally(() => {
    if (ensurePublishedModelCatalogSnapshotsInFlight.get(dependencies) === settled) {
      ensurePublishedModelCatalogSnapshotsInFlight.delete(dependencies)
    }
  })
  ensurePublishedModelCatalogSnapshotsInFlight.set(dependencies, settled)
  return settled
}

async function ensurePublishedModelCatalogSnapshotsInitializedInternalAsync(
  dependencies: EnsurePublishedModelCatalogSnapshotsDependencies
): Promise<EnsurePublishedModelCatalogSnapshotsResult> {
  const leaseToken = randomUUID()
  const leaseCommandTimeoutMs = dependencies.leaseCommandTimeoutMs
    ?? publishedModelCatalogInitializationLeaseCommandTimeoutMs
  const acquired = await withPublishedModelCatalogInitializationTimeoutAsync(
    (dependencies.acquireInitializationLease
      ? dependencies.acquireInitializationLease(leaseToken, publishedModelCatalogInitializationLeaseTtlMs)
      : publishedModelCatalogCache.acquireLease(publishedModelCatalogInitializationLeaseKey, {
          ttlMs: publishedModelCatalogInitializationLeaseTtlMs,
          token: leaseToken
        })),
    leaseCommandTimeoutMs,
    '发布模型目录首次初始化失败：初始化租约获取超时'
  )

  if (!acquired) {
    const initialized = await waitForPublishedModelCatalogInitializationAsync(dependencies)
    return unchangedPublishedModelCatalogInitializationResult(initialized)
  }

  try {
    const existing = await dependencies.findInitialSnapshot()
    if (existing) return unchangedPublishedModelCatalogInitializationResult(existing)

    const snapshotOwners = await dependencies.rebuildAll()
    const initialized = await dependencies.findInitialSnapshot()
    if (!initialized) {
      throw new Error('发布模型目录首次初始化失败：全局 OpenAI 快照仍不存在')
    }
    if (snapshotOwners === 0) return unchangedPublishedModelCatalogInitializationResult(initialized)
    return {
      action: 'initialized',
      modelCount: initialized.modelCount,
      snapshotOwners
    }
  } finally {
    await withPublishedModelCatalogInitializationTimeoutAsync(
      dependencies.releaseInitializationLease
        ? dependencies.releaseInitializationLease(leaseToken)
        : publishedModelCatalogCache.releaseLease(publishedModelCatalogInitializationLeaseKey, leaseToken),
      leaseCommandTimeoutMs,
      '发布模型目录首次初始化失败：初始化租约释放超时'
    )
  }
}

async function waitForPublishedModelCatalogInitializationAsync(
  dependencies: EnsurePublishedModelCatalogSnapshotsDependencies
): Promise<GatewayModelCatalogSnapshot> {
  const now = dependencies.now ?? Date.now
  const delay = dependencies.delay ?? delayAsync
  const timeoutMs = dependencies.initializationWaitTimeoutMs
    ?? publishedModelCatalogInitializationWaitTimeoutMs
  const intervalMs = dependencies.initializationWaitIntervalMs
    ?? publishedModelCatalogInitializationWaitIntervalMs
  const deadline = now() + timeoutMs

  while (true) {
    const initialized = await dependencies.findInitialSnapshot()
    if (initialized) return initialized
    const remainingMs = deadline - now()
    if (remainingMs <= 0) {
      throw new Error('发布模型目录首次初始化失败：等待持锁进程生成全局 OpenAI 快照超时')
    }
    await delay(Math.min(intervalMs, remainingMs))
  }
}

function unchangedPublishedModelCatalogInitializationResult(
  snapshot: GatewayModelCatalogSnapshot
): EnsurePublishedModelCatalogSnapshotsResult {
  return {
    action: 'unchanged',
    modelCount: snapshot.modelCount,
    snapshotOwners: 0
  }
}

async function withPublishedModelCatalogInitializationTimeoutAsync<T>(
  operation: Promise<T>,
  timeoutMs: number,
  timeoutMessage: string
): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  const timeout = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => reject(new Error(timeoutMessage)), timeoutMs)
  })
  try {
    return await Promise.race([operation, timeout])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

function delayAsync(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

async function rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync(
  systemAccountId?: string
): Promise<number> {
  return rebuildPublishedModelCatalogSnapshotsAfterModelChangeImplAsync(systemAccountId)
}

async function rebuildPublishedModelCatalogSnapshotsAfterModelChangeImplAsync(
  systemAccountId?: string
): Promise<number> {
  const activeSystemAccountIds = systemAccountId ? undefined : await listGatewayModelCatalogSystemAccountIdsAsync()
  const accountIds = systemAccountId
    ? [systemAccountId]
    : ['', ...(activeSystemAccountIds ?? [])]
  if (!systemAccountId) {
    await pruneGatewayModelCatalogSnapshotsAsync(activeSystemAccountIds ?? [])
    await publishedModelCatalogCache.clear()
    publishedModelCatalogLocalCache.clear()
  }
  let rebuilt = 0
  for (let index = 0; index < accountIds.length; index += 4) {
    const batch = accountIds.slice(index, index + 4)
    await Promise.all(batch.map((accountId) => rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync(accountId)))
    rebuilt += batch.length
  }
  return rebuilt
}

function enqueueSnapshotRebuild<T>(operation: () => Promise<T>): Promise<T> {
  const result = snapshotRebuildTail.then(operation, operation)
  snapshotRebuildTail = result.then(() => undefined, () => undefined)
  return result
}

export function prewarmPublishedModelCatalogSnapshotsAsync(): Promise<number> {
  return enqueueSnapshotRebuild(async () => {
    const snapshots = runtimeConfig.processRole === 'server'
      ? await requestGatewayDbService({ type: 'list_gateway_model_catalog_snapshots' })
      : await listGatewayModelCatalogSnapshotsAsync()
    await Promise.all(snapshots.filter((snapshot) => !isChatModelSnapshot(snapshot.variant)).map((snapshot) => cachePublishedSnapshot(snapshot)))
    return snapshots.length
  })
}

export async function rebuildPublishedModelCatalogSnapshotsBestEffortAsync(systemAccountId?: string, attempt = 0): Promise<boolean> {
  const retryKey = systemAccountId ?? '*'
  try {
    await rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync(systemAccountId)
    const pending = pendingRebuildRetries.get(retryKey)
    if (pending) clearTimeout(pending.timer)
    pendingRebuildRetries.delete(retryKey)
    return true
  } catch (error) {
    logger.error({ event: 'published_model_catalog_rebuild_failed', systemAccountId, error }, '模型事实已保存，发布快照将后台重试')
    if (!pendingRebuildRetries.has(retryKey) && attempt < 6) {
      const nextAttempt = attempt + 1
      const timer = setTimeout(() => {
        pendingRebuildRetries.delete(retryKey)
        void rebuildPublishedModelCatalogSnapshotsBestEffortAsync(systemAccountId, nextAttempt)
      }, Math.min(60_000, 2_000 * (2 ** attempt)))
      timer.unref()
      pendingRebuildRetries.set(retryKey, { timer, attempt: nextAttempt })
    }
    return false
  }
}

async function publishedCatalog(providerCode: string, systemAccountId: string): Promise<ProviderModelCatalogItem[]> {
  return (await listProviderModelCatalogAsync({
    providerCode,
    systemAccountId: systemAccountId || undefined
  })).filter((item) => item.catalogVisible !== false)
}

async function readPersistedSnapshotAsync(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): Promise<GatewayModelCatalogSnapshot | undefined> {
  if (runtimeConfig.processRole === 'server') {
    return await requestGatewayDbService({
      type: 'find_gateway_model_catalog_snapshot',
      ...input
    })
  }
  return await findGatewayModelCatalogSnapshotAsync(input)
}

async function cachePublishedSnapshot(snapshot: GatewayModelCatalogSnapshot, strict = false): Promise<void> {
  const entry = {
    payload: snapshot.payload,
    revision: snapshot.revision,
    modelCount: snapshot.modelCount
  }
  publishedModelCatalogLocalCache.set(publishedModelCatalogCacheKey(snapshot), entry)
  try {
    await publishedModelCatalogCache.set(publishedModelCatalogCacheKey(snapshot), entry)
  } catch (error) {
    logger.warn({ event: 'published_model_catalog_cache_write_failed', systemAccountId: snapshot.systemAccountId, protocol: snapshot.protocol, variant: snapshot.variant, error }, '模型目录 Redis 缓存写入失败，保留数据库快照')
    if (strict) throw error
  }
}

function snapshotInput(
  protocol: GatewayModelCatalogProtocol,
  variant: GatewayModelCatalogVariant,
  payload: object,
  modelCount: number,
  revision: string
) {
  return { protocol, variant, payload, modelCount, revision }
}

function chatModelListSnapshotVariant(providerCode: string): GatewayModelCatalogVariant {
  return `chat_list:${encodeURIComponent(normalizeProviderToken(providerCode) || providerCode)}`
}

function chatModelSnapshotVariant(providerCode: string, modelId: string): GatewayModelCatalogVariant {
  return `chat_model:${encodeURIComponent(normalizeProviderToken(providerCode) || providerCode)}:${encodeURIComponent(modelId)}`
}

function isChatModelSnapshot(variant: GatewayModelCatalogVariant): boolean {
  return variant.startsWith('chat_model:')
}

function publishedModelCatalogCacheKey(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): string {
  return JSON.stringify([input.systemAccountId, input.protocol, input.variant])
}
