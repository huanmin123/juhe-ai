import { createSharedJsonCache } from '../../shared/cache.js'
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
import { buildChatModelOptions, type ChatModelOption } from '../chat/chat-model-options.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
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
const pendingRebuildRetries = new Map<string, { timer: NodeJS.Timeout; attempt: number }>()
let publishedCatalogRebuildChain: Promise<unknown> = Promise.resolve()

export async function readPublishedModelCatalogResponseAsync(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): Promise<object> {
  const cacheKey = publishedModelCatalogCacheKey(input)
  let cached: PublishedModelCatalogCacheEntry | undefined
  try {
    cached = await publishedModelCatalogCache.get(cacheKey)
  } catch (error) {
    logger.warn({ event: 'published_model_catalog_cache_read_failed', cacheKey, error }, '模型目录缓存读取失败，回源持久化快照')
  }
  if (cached?.payload) return cached.payload

  const snapshot = await readPersistedSnapshotAsync(input)
    ?? await readPersistedSnapshotAsync({ ...input, systemAccountId: '' })
  if (!snapshot) {
    throw new Error('网关发布模型目录尚未生成')
  }
  await cachePublishedSnapshot(snapshot)
  return snapshot.payload
}

export async function rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(
  systemAccountId: string
): Promise<GatewayModelCatalogSnapshot[]> {
  const run = publishedCatalogRebuildChain.then(() => rebuildPublishedModelCatalogSnapshotsForSystemAccountImplAsync(systemAccountId))
  publishedCatalogRebuildChain = run.catch(() => undefined)
  return run
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
  const snapshots = await replaceGatewayModelCatalogSnapshotsAsync(systemAccountId, [
    snapshotInput('openai', 'default', buildOpenAIModelsResponseFromCatalog(openaiCatalog), openaiCatalog.length, revision),
    snapshotInput('openai', 'codex', buildCodexModelsResponseFromCatalog(openaiCatalog), openaiCatalog.length, revision),
    snapshotInput('anthropic', 'default', buildAnthropicModelsResponse(anthropicCatalog), anthropicCatalog.length, revision),
    snapshotInput('gemini', 'default', buildGeminiModelsResponse(geminiCatalog), geminiCatalog.length, revision),
    snapshotInput('openai', 'chat', { data: buildChatModelOptions(openaiCatalog.map((item) => item.model), openaiCatalog) }, openaiCatalog.length, revision)
  ])
  await clearPublishedModelCatalogOwnerCacheAsync(systemAccountId)
  await Promise.all(snapshots.map((snapshot) => cachePublishedSnapshot(snapshot, true)))
  return snapshots
}

export async function readPublishedChatModelOptionsAsync(systemAccountId: string): Promise<ChatModelOption[]> {
  const payload = await readPublishedModelCatalogResponseAsync({ systemAccountId, protocol: 'openai', variant: 'chat' })
  const data = 'data' in payload ? payload.data : undefined
  return Array.isArray(data) ? data as ChatModelOption[] : []
}

export async function rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync(
  systemAccountId?: string
): Promise<number> {
  const run = publishedCatalogRebuildChain.then(() => rebuildPublishedModelCatalogSnapshotsAfterModelChangeImplAsync(systemAccountId))
  publishedCatalogRebuildChain = run.catch(() => undefined)
  return run
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
  }
  let rebuilt = 0
  for (let index = 0; index < accountIds.length; index += 4) {
    const batch = accountIds.slice(index, index + 4)
    await Promise.all(batch.map((accountId) => rebuildPublishedModelCatalogSnapshotsForSystemAccountImplAsync(accountId)))
    rebuilt += batch.length
  }
  return rebuilt
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

export async function prewarmPublishedModelCatalogSnapshotsAsync(): Promise<number> {
  const snapshots = runtimeConfig.processRole === 'server'
    ? await requestGatewayDbService({ type: 'list_gateway_model_catalog_snapshots' })
    : await listGatewayModelCatalogSnapshotsAsync()
  await Promise.all(snapshots.map((snapshot) => cachePublishedSnapshot(snapshot)))
  return snapshots.length
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
  try {
    await publishedModelCatalogCache.set(publishedModelCatalogCacheKey(snapshot), {
      payload: snapshot.payload,
      revision: snapshot.revision,
      modelCount: snapshot.modelCount
    })
  } catch (error) {
    logger.warn({ event: 'published_model_catalog_cache_write_failed', systemAccountId: snapshot.systemAccountId, protocol: snapshot.protocol, variant: snapshot.variant, error }, '模型目录 Redis 缓存写入失败，保留数据库快照')
    if (strict) throw error
  }
}

async function clearPublishedModelCatalogOwnerCacheAsync(systemAccountId: string): Promise<void> {
  const protocols: GatewayModelCatalogProtocol[] = ['openai', 'anthropic', 'gemini']
  const variants: GatewayModelCatalogVariant[] = ['default', 'codex', 'chat']
  await Promise.all(protocols.flatMap((protocol) => variants.map((variant) =>
    publishedModelCatalogCache.delete(publishedModelCatalogCacheKey({ systemAccountId, protocol, variant }))
  )))
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

function publishedModelCatalogCacheKey(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): string {
  return JSON.stringify([input.systemAccountId, input.protocol, input.variant])
}
