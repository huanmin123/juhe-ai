import { Router, type NextFunction, type Request, type Response } from 'express'

import { requestDbService } from '../db-service/db-service-ipc.js'
import type { GatewayRuntimeRequest } from '../gateway/request/pre-auth.js'
import { gatewayErrorPayload } from '../gateway/response/responses.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { OpenAICompatibleFileRecord } from '../../storage/openai-compatible-files.repository.js'
import type {
  OpenAICompatibleVectorStoreFileChunkRecord,
  OpenAICompatibleVectorStoreFileRecord,
  OpenAICompatibleVectorStoreRecord,
  OpenAICompatibleVectorStoreSearchResult
} from '../../storage/openai-compatible-vector-stores.repository.js'
import { newOpenAICompatibleVectorStoreId } from '../../storage/openai-compatible-vector-stores.repository.js'
import {
  buildOpenAICompatibleVectorStoreChunks,
  OpenAICompatibleVectorStoreIndexingError
} from './text-indexer.js'

type JsonRecord = Record<string, unknown>

export const openAICompatibleVectorStoresRouter = Router()

class OpenAICompatibleVectorStoresRequestError extends Error {
  constructor(
    message: string,
    readonly statusCode = 400,
    readonly type = 'invalid_request_error',
    readonly code?: string
  ) {
    super(message)
  }
}

openAICompatibleVectorStoresRouter.get('/v1/vector_stores', handleOpenAICompatibleVectorStoresRoute(listOpenAICompatibleVectorStores))
openAICompatibleVectorStoresRouter.post('/v1/vector_stores', handleOpenAICompatibleVectorStoresRoute(createOpenAICompatibleVectorStoreRoute))
openAICompatibleVectorStoresRouter.get('/v1/vector_stores/:vectorStoreId/files/:fileId/content', handleOpenAICompatibleVectorStoresRoute(listOpenAICompatibleVectorStoreFileContent))
openAICompatibleVectorStoresRouter.get('/v1/vector_stores/:vectorStoreId/files/:fileId', handleOpenAICompatibleVectorStoresRoute(getOpenAICompatibleVectorStoreFile))
openAICompatibleVectorStoresRouter.delete('/v1/vector_stores/:vectorStoreId/files/:fileId', handleOpenAICompatibleVectorStoresRoute(deleteOpenAICompatibleVectorStoreFileRoute))
openAICompatibleVectorStoresRouter.get('/v1/vector_stores/:vectorStoreId/files', handleOpenAICompatibleVectorStoresRoute(listOpenAICompatibleVectorStoreFiles))
openAICompatibleVectorStoresRouter.post('/v1/vector_stores/:vectorStoreId/files', handleOpenAICompatibleVectorStoresRoute(createOpenAICompatibleVectorStoreFileRoute))
openAICompatibleVectorStoresRouter.post('/v1/vector_stores/:vectorStoreId/search', handleOpenAICompatibleVectorStoresRoute(searchOpenAICompatibleVectorStoreRoute))
openAICompatibleVectorStoresRouter.get('/v1/vector_stores/:vectorStoreId', handleOpenAICompatibleVectorStoresRoute(getOpenAICompatibleVectorStore))
openAICompatibleVectorStoresRouter.delete('/v1/vector_stores/:vectorStoreId', handleOpenAICompatibleVectorStoresRoute(deleteOpenAICompatibleVectorStoreRoute))

async function createOpenAICompatibleVectorStoreRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const body = await readJsonObjectBody(req)
  const expiresAfter = objectValue(body.expires_after)
  const expiresAfterDays = queryInteger(expiresAfter?.days)
  const created = await requestDbService({
    type: 'create_openai_compatible_vector_store',
    input: {
      id: newOpenAICompatibleVectorStoreId(),
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      name: stringValue(body.name),
      description: stringValue(body.description),
      metadata: objectValue(body.metadata),
      expiresAfterAnchor: stringValue(expiresAfter?.anchor),
      expiresAfterDays,
      expiresAt: expiresAtFromDays(expiresAfterDays)
    }
  })
  res.status(200).json(openAICompatibleVectorStoreObject(created))
}

async function listOpenAICompatibleVectorStores(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const result = await requestDbService({
    type: 'list_openai_compatible_vector_stores',
    options: {
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      limit: queryInteger(req.query.limit),
      order: queryString(req.query.order) === 'asc' ? 'asc' : 'desc',
      after: queryString(req.query.after),
      before: queryString(req.query.before)
    }
  })
  const data = result.items.map(openAICompatibleVectorStoreObject)
  res.json({
    object: 'list',
    data,
    first_id: data[0]?.id,
    last_id: data[data.length - 1]?.id,
    has_more: result.hasMore
  })
}

async function getOpenAICompatibleVectorStore(req: Request, res: Response): Promise<void> {
  const record = await findOpenAICompatibleVectorStoreForRequest(req)
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储不存在', 404, 'invalid_request_error', 'vector_store_not_found')
  }
  res.json(openAICompatibleVectorStoreObject(record))
}

async function deleteOpenAICompatibleVectorStoreRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const vectorStoreId = pathVectorStoreId(req)
  const record = await requestDbService({
    type: 'delete_openai_compatible_vector_store',
    vectorStoreId,
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储不存在', 404, 'invalid_request_error', 'vector_store_not_found')
  }
  res.json({
    id: record.id,
    object: 'vector_store.deleted',
    deleted: true
  })
}

async function createOpenAICompatibleVectorStoreFileRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const vectorStoreId = pathVectorStoreId(req)
  const body = await readJsonObjectBody(req)
  const fileId = stringValue(body.file_id)
  if (!fileId) {
    throw new OpenAICompatibleVectorStoresRequestError('缺少必填字段：file_id', 400, 'invalid_request_error', 'missing_file_id')
  }
  const store = await findOpenAICompatibleVectorStoreForRequest(req)
  if (!store) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储不存在', 404, 'invalid_request_error', 'vector_store_not_found')
  }
  const file = await requestDbService({
    type: 'get_openai_compatible_file',
    fileId,
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
  if (!file) {
    throw new OpenAICompatibleVectorStoresRequestError('文件不存在', 404, 'invalid_request_error', 'file_not_found')
  }
  const created = await requestDbService({
    type: 'create_openai_compatible_vector_store_file',
    input: {
      vectorStoreId,
      fileId,
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      attributes: objectValue(body.attributes),
      chunkingStrategy: objectValue(body.chunking_strategy),
      status: 'in_progress'
    }
  }, { timeoutMs: 10_000 })
  if (!created) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储或文件不存在', 404, 'invalid_request_error', 'vector_store_file_not_found')
  }
  queueOpenAICompatibleVectorStoreFileIndexing({
    vectorStoreId,
    file,
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id,
    attributes: objectValue(body.attributes),
    chunkingStrategy: objectValue(body.chunking_strategy)
  })
  res.status(200).json(openAICompatibleVectorStoreFileObject(created))
}

async function listOpenAICompatibleVectorStoreFiles(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  await requireOpenAICompatibleVectorStoreForRequest(req)
  const result = await requestDbService({
    type: 'list_openai_compatible_vector_store_files',
    options: {
      vectorStoreId: pathVectorStoreId(req),
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      limit: queryInteger(req.query.limit),
      order: queryString(req.query.order) === 'asc' ? 'asc' : 'desc',
      after: queryString(req.query.after)
    }
  })
  const data = result.items.map(openAICompatibleVectorStoreFileObject)
  res.json({
    object: 'list',
    data,
    first_id: data[0]?.id,
    last_id: data[data.length - 1]?.id,
    has_more: result.hasMore
  })
}

async function getOpenAICompatibleVectorStoreFile(req: Request, res: Response): Promise<void> {
  const record = await findOpenAICompatibleVectorStoreFileForRequest(req)
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储文件不存在', 404, 'invalid_request_error', 'vector_store_file_not_found')
  }
  res.json(openAICompatibleVectorStoreFileObject(record))
}

async function deleteOpenAICompatibleVectorStoreFileRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const vectorStoreId = pathVectorStoreId(req)
  const fileId = pathFileId(req)
  const record = await requestDbService({
    type: 'delete_openai_compatible_vector_store_file',
    vectorStoreId,
    fileId,
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储文件不存在', 404, 'invalid_request_error', 'vector_store_file_not_found')
  }
  res.json({
    id: fileId,
    object: 'vector_store.file.deleted',
    deleted: true
  })
}

async function listOpenAICompatibleVectorStoreFileContent(req: Request, res: Response): Promise<void> {
  const record = await findOpenAICompatibleVectorStoreFileForRequest(req)
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储文件不存在', 404, 'invalid_request_error', 'vector_store_file_not_found')
  }
  const runtime = requireGatewayRuntime(req)
  const chunks = await requestDbService({
    type: 'list_openai_compatible_vector_store_file_chunks',
    vectorStoreId: pathVectorStoreId(req),
    fileId: pathFileId(req),
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id,
    limit: queryInteger(req.query.limit)
  })
  const data = chunks.map(openAICompatibleVectorStoreFileContentObject)
  res.json({
    object: 'list',
    data,
    first_id: data[0]?.id,
    last_id: data[data.length - 1]?.id,
    has_more: false
  })
}

async function searchOpenAICompatibleVectorStoreRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const store = await requireOpenAICompatibleVectorStoreForRequest(req)
  requireOpenAICompatibleVectorStoreReadyForSearch(store)
  const body = await readJsonObjectBody(req)
  const query = stringValue(body.query)
  if (!query) {
    throw new OpenAICompatibleVectorStoresRequestError('缺少必填字段：query', 400, 'invalid_request_error', 'missing_query')
  }
  const rankingOptions = objectValue(body.ranking_options)
  const results = await requestDbService({
    type: 'search_openai_compatible_vector_store',
    options: {
      vectorStoreId: pathVectorStoreId(req),
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      query,
      maxNumResults: queryInteger(body.max_num_results),
      filters: objectValue(body.filters) ?? objectValue(body.attribute_filter),
      scoreThreshold: queryNumber(rankingOptions?.score_threshold)
    }
  })
  res.json(openAICompatibleVectorStoreSearchResponse(query, results))
}

function queueOpenAICompatibleVectorStoreFileIndexing(input: {
  vectorStoreId: string
  file: OpenAICompatibleFileRecord
  systemAccountId: string
  apiKeyId: string
  attributes?: JsonRecord
  chunkingStrategy?: JsonRecord
}): void {
  void indexOpenAICompatibleVectorStoreFile(input).catch((error: unknown) => {
    logger.warn(errorLogFields(error, {
      event: 'openai_compatible_vector_store_file_indexing_failed',
      vectorStoreId: input.vectorStoreId,
      fileId: input.file.id,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }))
  })
}

async function indexOpenAICompatibleVectorStoreFile(input: {
  vectorStoreId: string
  file: OpenAICompatibleFileRecord
  systemAccountId: string
  apiKeyId: string
  attributes?: JsonRecord
  chunkingStrategy?: JsonRecord
}): Promise<void> {
  try {
    const chunks = await buildOpenAICompatibleVectorStoreChunks(input.file)
    await requestDbService({
      type: 'create_openai_compatible_vector_store_file',
      input: {
        vectorStoreId: input.vectorStoreId,
        fileId: input.file.id,
        systemAccountId: input.systemAccountId,
        apiKeyId: input.apiKeyId,
        attributes: input.attributes,
        chunkingStrategy: input.chunkingStrategy,
        status: 'completed',
        chunks
      }
    }, { timeoutMs: 10_000 })
  } catch (error) {
    const lastError = openAICompatibleVectorStoreFileLastError(error)
    await requestDbService({
      type: 'create_openai_compatible_vector_store_file',
      input: {
        vectorStoreId: input.vectorStoreId,
        fileId: input.file.id,
        systemAccountId: input.systemAccountId,
        apiKeyId: input.apiKeyId,
        attributes: input.attributes,
        chunkingStrategy: input.chunkingStrategy,
        status: 'failed',
        lastError
      }
    }, { timeoutMs: 10_000 })
  }
}

function openAICompatibleVectorStoreFileLastError(error: unknown): JsonRecord {
  if (error instanceof OpenAICompatibleVectorStoreIndexingError) {
    return {
      code: error.code,
      type: error.type,
      message: error.message
    }
  }
  return {
    code: 'openai_compatible_vector_store_index_failed',
    type: 'invalid_request_error',
    message: '向量存储文件建立索引失败'
  }
}

function requireOpenAICompatibleVectorStoreReadyForSearch(store: OpenAICompatibleVectorStoreRecord): void {
  if (store.fileCounts.inProgress > 0) {
    throw new OpenAICompatibleVectorStoresRequestError(
      '向量存储文件仍在建立索引',
      409,
      'invalid_request_error',
      'openai_compatible_vector_store_not_ready'
    )
  }
  if (store.fileCounts.completed <= 0 && store.fileCounts.failed > 0) {
    throw new OpenAICompatibleVectorStoresRequestError(
      '向量存储没有可检索的已完成文件',
      400,
      'invalid_request_error',
      'openai_compatible_vector_store_file_failed'
    )
  }
}

async function findOpenAICompatibleVectorStoreForRequest(req: Request): Promise<OpenAICompatibleVectorStoreRecord | undefined> {
  const runtime = requireGatewayRuntime(req)
  return await requestDbService({
    type: 'get_openai_compatible_vector_store',
    vectorStoreId: pathVectorStoreId(req),
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
}

async function requireOpenAICompatibleVectorStoreForRequest(req: Request): Promise<OpenAICompatibleVectorStoreRecord> {
  const record = await findOpenAICompatibleVectorStoreForRequest(req)
  if (!record) {
    throw new OpenAICompatibleVectorStoresRequestError('向量存储不存在', 404, 'invalid_request_error', 'vector_store_not_found')
  }
  return record
}

async function findOpenAICompatibleVectorStoreFileForRequest(req: Request): Promise<OpenAICompatibleVectorStoreFileRecord | undefined> {
  const runtime = requireGatewayRuntime(req)
  return await requestDbService({
    type: 'get_openai_compatible_vector_store_file',
    vectorStoreId: pathVectorStoreId(req),
    fileId: pathFileId(req),
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
}

function openAICompatibleVectorStoreObject(record: OpenAICompatibleVectorStoreRecord): JsonRecord {
  return {
    id: record.id,
    object: 'vector_store',
    created_at: openAITimestamp(record.createdAt),
    name: record.name ?? null,
    description: record.description ?? null,
    bytes: record.bytes,
    file_counts: {
      in_progress: record.fileCounts.inProgress,
      completed: record.fileCounts.completed,
      failed: record.fileCounts.failed,
      cancelled: record.fileCounts.cancelled,
      total: record.fileCounts.total
    },
    status: record.fileCounts.inProgress > 0 ? 'in_progress' : 'completed',
    metadata: record.metadata,
    ...(record.expiresAfterAnchor || record.expiresAfterDays ? {
      expires_after: {
        anchor: record.expiresAfterAnchor ?? 'last_active_at',
        days: record.expiresAfterDays ?? 0
      }
    } : {}),
    ...(record.expiresAt ? { expires_at: openAITimestamp(record.expiresAt) } : {})
  }
}

function openAICompatibleVectorStoreFileObject(record: OpenAICompatibleVectorStoreFileRecord): JsonRecord {
  return {
    id: record.fileId,
    object: 'vector_store.file',
    usage_bytes: record.usageBytes,
    created_at: openAITimestamp(record.createdAt),
    vector_store_id: record.vectorStoreId,
    status: record.status,
    last_error: record.lastError ?? null,
    chunking_strategy: Object.keys(record.chunkingStrategy).length
      ? record.chunkingStrategy
      : {
        type: 'static',
        static: {
          max_chunk_size_tokens: 800,
          chunk_overlap_tokens: 400
        }
      },
    attributes: record.attributes
  }
}

function openAICompatibleVectorStoreFileContentObject(record: OpenAICompatibleVectorStoreFileChunkRecord): JsonRecord {
  return {
    id: record.chunkId,
    object: 'vector_store.file_content',
    type: 'text',
    text: record.contentText,
    file_id: record.fileId,
    filename: record.filename,
    chunk_index: record.chunkIndex
  }
}

function openAICompatibleVectorStoreSearchResponse(
  query: string,
  results: OpenAICompatibleVectorStoreSearchResult[]
): JsonRecord {
  return {
    object: 'vector_store.search_results.page',
    search_query: query,
    data: results.map((result) => ({
      file_id: result.fileId,
      filename: result.filename,
      score: result.score,
      attributes: result.attributes,
      content: [{
        type: 'text',
        text: result.contentText
      }]
    })),
    has_more: false,
    next_page: null
  }
}

function handleOpenAICompatibleVectorStoresRoute(
  handler: (req: Request, res: Response) => Promise<void>
): (req: Request, res: Response, next: NextFunction) => void {
  return (req, res, next) => {
    handler(req, res).catch((error: unknown) => {
      if (res.headersSent) {
        next(error)
        return
      }
      if (error instanceof OpenAICompatibleVectorStoresRequestError) {
        res.status(error.statusCode).json(gatewayErrorPayload(error.message, error.type, error.code))
        return
      }
      if (error instanceof OpenAICompatibleVectorStoreIndexingError) {
        res.status(error.statusCode).json(gatewayErrorPayload(error.message, error.type, error.code))
        return
      }
      next(error)
    })
  }
}

function requireGatewayRuntime(req: Request): Required<Pick<NonNullable<GatewayRuntimeRequest['gatewayRuntime']>, 'apiKey'>> {
  const runtime = (req as GatewayRuntimeRequest).gatewayRuntime
  if (!runtime?.apiKey) {
    throw new OpenAICompatibleVectorStoresRequestError('缺少或无效的 API Key', 401, 'invalid_request_error', 'invalid_api_key')
  }
  return { apiKey: runtime.apiKey }
}

async function readJsonObjectBody(req: Request): Promise<JsonRecord> {
  if (isPlainObject(req.body)) return req.body
  const chunks: Buffer[] = []
  let total = 0
  for await (const chunk of req) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    total += buffer.length
    if (total > 1024 * 1024) {
      throw new OpenAICompatibleVectorStoresRequestError('JSON 请求体过大', 413, 'request_too_large', 'request_body_too_large')
    }
    chunks.push(buffer)
  }
  const text = Buffer.concat(chunks).toString('utf8').trim()
  if (!text) return {}
  try {
    const parsed = JSON.parse(text) as unknown
    if (!isPlainObject(parsed)) {
      throw new OpenAICompatibleVectorStoresRequestError('JSON 请求体必须是对象', 400, 'invalid_request_error', 'invalid_json_body')
    }
    return parsed
  } catch (error) {
    if (error instanceof OpenAICompatibleVectorStoresRequestError) throw error
    throw new OpenAICompatibleVectorStoresRequestError('JSON 请求体无效', 400, 'invalid_request_error', 'invalid_json_body')
  }
}

function pathVectorStoreId(req: Request): string {
  const value = typeof req.params.vectorStoreId === 'string' ? req.params.vectorStoreId.trim() : ''
  if (!value) {
    throw new OpenAICompatibleVectorStoresRequestError('缺少向量存储 ID', 400, 'invalid_request_error', 'missing_vector_store_id')
  }
  return value
}

function pathFileId(req: Request): string {
  const value = typeof req.params.fileId === 'string' ? req.params.fileId.trim() : ''
  if (!value) {
    throw new OpenAICompatibleVectorStoresRequestError('缺少文件 ID', 400, 'invalid_request_error', 'missing_file_id')
  }
  return value
}

function queryString(value: unknown): string | undefined {
  if (typeof value === 'string') {
    const text = value.trim()
    return text || undefined
  }
  return undefined
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'string') {
    const text = value.trim()
    return text || undefined
  }
  return undefined
}

function queryInteger(value: unknown): number | undefined {
  const text = typeof value === 'number' ? String(value) : queryString(value)
  if (!text) return undefined
  const number = Number(text)
  return Number.isFinite(number) ? Math.trunc(number) : undefined
}

function queryNumber(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function openAITimestamp(value: string): number {
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : Math.floor(Date.now() / 1000)
}

function expiresAtFromDays(days: number | undefined): string | undefined {
  if (!Number.isFinite(days) || !days || days <= 0) return undefined
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString()
}
