import { createSharedJsonCache } from '../../shared/cache.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  findGatewayModelCatalogSnapshotAsync,
  listGatewayModelCatalogSnapshotsAsync,
  listGatewayModelCatalogSystemAccountIdsAsync,
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
  ttlMs: 30 * 24 * 60 * 60_000,
  version: 'v1'
})

export async function readPublishedModelCatalogResponseAsync(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): Promise<object> {
  const cacheKey = publishedModelCatalogCacheKey(input)
  const cached = await publishedModelCatalogCache.get(cacheKey)
  if (cached?.payload) return cached.payload

  return enqueueSnapshotRebuild(async () => {
    const current = await publishedModelCatalogCache.get(cacheKey)
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
  await Promise.all(snapshots.map(cachePublishedSnapshot))
  return snapshots
}

export async function readPublishedChatModelOptionsAsync(systemAccountId: string): Promise<ChatModelOption[]> {
  const payload = await readPublishedModelCatalogResponseAsync({ systemAccountId, protocol: 'openai', variant: 'chat' })
  const data = 'data' in payload ? payload.data : undefined
  return Array.isArray(data) ? data as ChatModelOption[] : []
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

async function rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync(
  systemAccountId?: string
): Promise<number> {
  const accountIds = systemAccountId
    ? [systemAccountId]
    : ['', ...await listGatewayModelCatalogSystemAccountIdsAsync()]
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
    await Promise.all(snapshots.map(cachePublishedSnapshot))
    return snapshots.length
  })
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

async function cachePublishedSnapshot(snapshot: GatewayModelCatalogSnapshot): Promise<void> {
  await publishedModelCatalogCache.set(publishedModelCatalogCacheKey(snapshot), {
    payload: snapshot.payload,
    revision: snapshot.revision,
    modelCount: snapshot.modelCount
  })
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
