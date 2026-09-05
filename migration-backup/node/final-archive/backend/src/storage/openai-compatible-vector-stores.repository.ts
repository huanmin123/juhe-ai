import { randomUUID } from 'node:crypto'

import {
  getBusinessDatabase,
  nowIso,
  runInDatabaseTransaction
} from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import {
  findOpenAICompatibleFile,
  findOpenAICompatibleFileWithClient,
  type OpenAICompatibleFileRecord
} from './openai-compatible-files.repository.js'
import { getPostgresPool } from './postgres-client.js'

export type OpenAICompatibleVectorStoreStatus = 'active' | 'deleted'
export type OpenAICompatibleVectorStoreFileStatus = 'in_progress' | 'completed' | 'failed' | 'cancelled'

export interface OpenAICompatibleVectorStoreFileCounts {
  inProgress: number
  completed: number
  failed: number
  cancelled: number
  total: number
}

export interface OpenAICompatibleVectorStoreRecord {
  id: string
  systemAccountId: string
  apiKeyId: string
  name?: string
  description?: string
  metadata: Record<string, unknown>
  bytes: number
  status: OpenAICompatibleVectorStoreStatus
  createdAt: string
  updatedAt: string
  expiresAfterAnchor?: string
  expiresAfterDays?: number
  expiresAt?: string
  deletedAt?: string
  fileCounts: OpenAICompatibleVectorStoreFileCounts
}

export interface OpenAICompatibleVectorStoreCreateInput {
  id: string
  systemAccountId: string
  apiKeyId: string
  name?: string
  description?: string
  metadata?: Record<string, unknown>
  expiresAfterAnchor?: string
  expiresAfterDays?: number
  expiresAt?: string
}

export interface OpenAICompatibleVectorStoreListOptions {
  systemAccountId: string
  apiKeyId: string
  limit?: number
  order?: 'asc' | 'desc'
  after?: string
  before?: string
}

export interface OpenAICompatibleVectorStoreListResult {
  items: OpenAICompatibleVectorStoreRecord[]
  hasMore: boolean
}

export interface OpenAICompatibleVectorStoreFileRecord {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
  attributes: Record<string, unknown>
  chunkingStrategy: Record<string, unknown>
  status: OpenAICompatibleVectorStoreFileStatus
  usageBytes: number
  lastError?: Record<string, unknown>
  createdAt: string
  updatedAt: string
  deletedAt?: string
  file?: OpenAICompatibleFileRecord
}

export interface OpenAICompatibleVectorStoreFileCreateInput {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
  attributes?: Record<string, unknown>
  chunkingStrategy?: Record<string, unknown>
  status: OpenAICompatibleVectorStoreFileStatus
  usageBytes?: number
  lastError?: Record<string, unknown>
  chunks?: OpenAICompatibleVectorStoreChunkInput[]
}

export interface OpenAICompatibleVectorStoreFileListOptions {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
  limit?: number
  order?: 'asc' | 'desc'
  after?: string
}

export interface OpenAICompatibleVectorStoreFileListResult {
  items: OpenAICompatibleVectorStoreFileRecord[]
  hasMore: boolean
}

export interface OpenAICompatibleVectorStoreChunkInput {
  contentText: string
  contentPreview: string
  tokenEstimate: number
  keywordIndexText: string
}

export interface OpenAICompatibleVectorStoreSearchOptions {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
  query: string
  maxNumResults?: number
  filters?: Record<string, unknown>
  scoreThreshold?: number
}

export interface OpenAICompatibleVectorStoreSearchResult {
  chunkId: string
  vectorStoreId: string
  fileId: string
  filename: string
  attributes: Record<string, unknown>
  chunkIndex: number
  score: number
  contentText: string
  contentPreview: string
}

export interface OpenAICompatibleVectorStoreFileChunkRecord {
  chunkId: string
  vectorStoreId: string
  fileId: string
  filename: string
  chunkIndex: number
  contentText: string
  contentPreview: string
}

interface OpenAICompatibleVectorStoreRow {
  id: string
  system_account_id: string
  api_key_id: string
  name: string | null
  description: string | null
  metadata_json: string
  bytes: number
  status: string
  created_at: string
  updated_at: string
  expires_after_anchor: string | null
  expires_after_days: number | null
  expires_at: string | null
  deleted_at: string | null
}

interface OpenAICompatibleVectorStoreFileRow {
  vector_store_id: string
  file_id: string
  system_account_id: string
  api_key_id: string
  attributes_json: string
  chunking_strategy_json: string
  status: string
  usage_bytes: number
  last_error_json: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
}

interface OpenAICompatibleVectorStoreSearchRow {
  id: string
  vector_store_id: string
  file_id: string
  chunk_index: number
  content_text: string
  content_preview: string
  keyword_index_text: string
  filename: string
  attributes_json: string
}

const maxVectorStoreListLimit = 100
const defaultVectorStoreListLimit = 20
const maxSearchResults = 50
const defaultSearchResults = 10
const businessSchemaName = 'juhe_business'

export function newOpenAICompatibleVectorStoreId(): string {
  return `vs_${Date.now().toString(36)}_${randomUUID().replace(/-/g, '').slice(0, 20)}`
}

export function createOpenAICompatibleVectorStore(
  input: OpenAICompatibleVectorStoreCreateInput
): OpenAICompatibleVectorStoreRecord {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_vector_stores (
      id, system_account_id, api_key_id, name, description, metadata_json, bytes,
      status, created_at, updated_at, expires_after_anchor, expires_after_days, expires_at, deleted_at
    ) VALUES (?, ?, ?, ?, ?, ?, 0, 'active', ?, ?, ?, ?, ?, NULL)
  `).run(
    input.id,
    input.systemAccountId,
    input.apiKeyId,
    input.name ?? null,
    input.description ?? null,
    JSON.stringify(input.metadata ?? {}),
    now,
    now,
    input.expiresAfterAnchor ?? null,
    input.expiresAfterDays ?? null,
    input.expiresAt ?? null
  )
  const record = findOpenAICompatibleVectorStore({
    vectorStoreId: input.id,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  if (!record) {
    throw new Error(`OpenAI compatible vector store ${input.id} was not readable after insert`)
  }
  return record
}

export async function createOpenAICompatibleVectorStoreAsync(
  input: OpenAICompatibleVectorStoreCreateInput
): Promise<OpenAICompatibleVectorStoreRecord> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const now = nowIso()
  await client.execute(`
    INSERT INTO ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_stores')} (
      id, system_account_id, api_key_id, name, description, metadata_json, bytes,
      status, created_at, updated_at, expires_after_anchor, expires_after_days, expires_at, deleted_at
    ) VALUES (?, ?, ?, ?, ?, ?, 0, 'active', ?, ?, ?, ?, ?, NULL)
  `, [
    input.id,
    input.systemAccountId,
    input.apiKeyId,
    input.name ?? null,
    input.description ?? null,
    JSON.stringify(input.metadata ?? {}),
    now,
    now,
    input.expiresAfterAnchor ?? null,
    input.expiresAfterDays ?? null,
    input.expiresAt ?? null
  ])
  const record = await findOpenAICompatibleVectorStoreWithClient(client, {
    vectorStoreId: input.id,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  if (!record) {
    throw new Error(`OpenAI compatible vector store ${input.id} was not readable after insert`)
  }
  return record
}

export function listOpenAICompatibleVectorStores(
  options: OpenAICompatibleVectorStoreListOptions
): OpenAICompatibleVectorStoreListResult {
  const limit = normalizeListLimit(options.limit)
  const order = options.order === 'asc' ? 'asc' : 'desc'
  const params: Array<string | number> = [options.systemAccountId, options.apiKeyId]
  const clauses = [
    'system_account_id = ?',
    'api_key_id = ?',
    'deleted_at IS NULL'
  ]
  const afterCursor = options.after ? findOpenAICompatibleVectorStoreCursor(options.after, options.systemAccountId, options.apiKeyId) : undefined
  if (options.after && !afterCursor) return { items: [], hasMore: false }
  const beforeCursor = options.before ? findOpenAICompatibleVectorStoreCursor(options.before, options.systemAccountId, options.apiKeyId) : undefined
  if (options.before && !beforeCursor) return { items: [], hasMore: false }
  if (afterCursor) {
    clauses.push(order === 'asc'
      ? '(created_at > ? OR (created_at = ? AND id > ?))'
      : '(created_at < ? OR (created_at = ? AND id < ?))')
    params.push(afterCursor.created_at, afterCursor.created_at, afterCursor.id)
  }
  if (beforeCursor) {
    clauses.push(order === 'asc'
      ? '(created_at < ? OR (created_at = ? AND id < ?))'
      : '(created_at > ? OR (created_at = ? AND id > ?))')
    params.push(beforeCursor.created_at, beforeCursor.created_at, beforeCursor.id)
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT ${vectorStoreSelectColumns()}
    FROM openai_compatible_vector_stores
    WHERE ${clauses.join(' AND ')}
    ORDER BY created_at ${order.toUpperCase()}, id ${order.toUpperCase()}
    LIMIT ?
  `).all(...params, limit + 1) as unknown as OpenAICompatibleVectorStoreRow[]
  return {
    items: rows.slice(0, limit).map(vectorStoreFromRow),
    hasMore: rows.length > limit
  }
}

export async function listOpenAICompatibleVectorStoresAsync(
  options: OpenAICompatibleVectorStoreListOptions
): Promise<OpenAICompatibleVectorStoreListResult> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const limit = normalizeListLimit(options.limit)
  const order = options.order === 'asc' ? 'asc' : 'desc'
  const params: Array<string | number> = [options.systemAccountId, options.apiKeyId]
  const clauses = [
    'system_account_id = ?',
    'api_key_id = ?',
    'deleted_at IS NULL'
  ]
  const afterCursor = options.after ? await findOpenAICompatibleVectorStoreCursorWithClient(client, options.after, options.systemAccountId, options.apiKeyId) : undefined
  if (options.after && !afterCursor) return { items: [], hasMore: false }
  const beforeCursor = options.before ? await findOpenAICompatibleVectorStoreCursorWithClient(client, options.before, options.systemAccountId, options.apiKeyId) : undefined
  if (options.before && !beforeCursor) return { items: [], hasMore: false }
  if (afterCursor) {
    clauses.push(order === 'asc'
      ? '(created_at > ? OR (created_at = ? AND id > ?))'
      : '(created_at < ? OR (created_at = ? AND id < ?))')
    params.push(afterCursor.created_at, afterCursor.created_at, afterCursor.id)
  }
  if (beforeCursor) {
    clauses.push(order === 'asc'
      ? '(created_at < ? OR (created_at = ? AND id < ?))'
      : '(created_at > ? OR (created_at = ? AND id > ?))')
    params.push(beforeCursor.created_at, beforeCursor.created_at, beforeCursor.id)
  }
  const rows = await client.query<OpenAICompatibleVectorStoreRow>(`
    SELECT ${vectorStoreSelectColumns()}
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_stores')}
    WHERE ${clauses.join(' AND ')}
    ORDER BY created_at ${order.toUpperCase()}, id ${order.toUpperCase()}
    LIMIT ?
  `, [...params, limit + 1])
  return {
    items: await Promise.all(rows.slice(0, limit).map((row) => vectorStoreFromRowWithClient(client, row))),
    hasMore: rows.length > limit
  }
}

export function findOpenAICompatibleVectorStore(input: {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleVectorStoreRecord | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT ${vectorStoreSelectColumns()}
    FROM openai_compatible_vector_stores
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(input.vectorStoreId, input.systemAccountId, input.apiKeyId) as unknown as OpenAICompatibleVectorStoreRow | undefined
  return row ? vectorStoreFromRow(row) : undefined
}

export async function findOpenAICompatibleVectorStoreAsync(input: {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
}): Promise<OpenAICompatibleVectorStoreRecord | undefined> {
  return await findOpenAICompatibleVectorStoreWithClient(await openAICompatibleVectorStorePostgresClient(), input)
}

async function findOpenAICompatibleVectorStoreWithClient(
  client: DatabaseClient,
  input: {
    vectorStoreId: string
    systemAccountId: string
    apiKeyId: string
  }
): Promise<OpenAICompatibleVectorStoreRecord | undefined> {
  const row = await client.one<OpenAICompatibleVectorStoreRow>(`
    SELECT ${vectorStoreSelectColumns()}
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_stores')}
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [input.vectorStoreId, input.systemAccountId, input.apiKeyId])
  return row ? await vectorStoreFromRowWithClient(client, row) : undefined
}

export function deleteOpenAICompatibleVectorStore(input: {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleVectorStoreRecord | undefined {
  const existing = findOpenAICompatibleVectorStore(input)
  if (!existing) return undefined
  const now = nowIso()
  runInDatabaseTransaction(() => {
    getBusinessDatabase().prepare(`
      UPDATE openai_compatible_vector_stores
      SET status = 'deleted',
          deleted_at = ?,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `).run(now, now, input.vectorStoreId, input.systemAccountId, input.apiKeyId)
    getBusinessDatabase().prepare(`
      UPDATE openai_compatible_vector_store_files
      SET deleted_at = ?,
          updated_at = ?
      WHERE vector_store_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `).run(now, now, input.vectorStoreId, input.systemAccountId, input.apiKeyId)
    getBusinessDatabase().prepare(`
      DELETE FROM openai_compatible_vector_store_chunks
      WHERE vector_store_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `).run(input.vectorStoreId, input.systemAccountId, input.apiKeyId)
  })
  return {
    ...existing,
    status: 'deleted',
    deletedAt: now,
    updatedAt: now
  }
}

export async function deleteOpenAICompatibleVectorStoreAsync(input: {
  vectorStoreId: string
  systemAccountId: string
  apiKeyId: string
}): Promise<OpenAICompatibleVectorStoreRecord | undefined> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const existing = await findOpenAICompatibleVectorStoreWithClient(client, input)
  if (!existing) return undefined
  const now = nowIso()
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_stores')}
      SET status = 'deleted',
          deleted_at = ?,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `, [now, now, input.vectorStoreId, input.systemAccountId, input.apiKeyId])
    await tx.execute(`
      UPDATE ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_files')}
      SET deleted_at = ?,
          updated_at = ?
      WHERE vector_store_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `, [now, now, input.vectorStoreId, input.systemAccountId, input.apiKeyId])
    await tx.execute(`
      DELETE FROM ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_chunks')}
      WHERE vector_store_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `, [input.vectorStoreId, input.systemAccountId, input.apiKeyId])
  })
  return {
    ...existing,
    status: 'deleted',
    deletedAt: now,
    updatedAt: now
  }
}

export function createOpenAICompatibleVectorStoreFile(
  input: OpenAICompatibleVectorStoreFileCreateInput
): OpenAICompatibleVectorStoreFileRecord | undefined {
  const store = findOpenAICompatibleVectorStore({
    vectorStoreId: input.vectorStoreId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  const file = findOpenAICompatibleFile({
    fileId: input.fileId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  if (!store || !file) return undefined
  const chunks = input.chunks ?? []
  const now = nowIso()
  const usageBytes = input.usageBytes ?? chunks.reduce((total, chunk) => total + Buffer.byteLength(chunk.contentText, 'utf8'), 0)
  runInDatabaseTransaction(() => {
    getBusinessDatabase().prepare(`
      INSERT INTO openai_compatible_vector_store_files (
        vector_store_id, file_id, system_account_id, api_key_id, attributes_json,
        chunking_strategy_json, status, usage_bytes, last_error_json, created_at, updated_at, deleted_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
      ON CONFLICT(vector_store_id, file_id) DO UPDATE SET
        attributes_json = excluded.attributes_json,
        chunking_strategy_json = excluded.chunking_strategy_json,
        status = excluded.status,
        usage_bytes = excluded.usage_bytes,
        last_error_json = excluded.last_error_json,
        updated_at = excluded.updated_at,
        deleted_at = NULL
    `).run(
      input.vectorStoreId,
      input.fileId,
      input.systemAccountId,
      input.apiKeyId,
      JSON.stringify(input.attributes ?? {}),
      JSON.stringify(input.chunkingStrategy ?? {}),
      input.status,
      usageBytes,
      input.lastError ? JSON.stringify(input.lastError) : null,
      now,
      now
    )
    getBusinessDatabase().prepare(`
      DELETE FROM openai_compatible_vector_store_chunks
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `).run(input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId)
    const insertChunk = getBusinessDatabase().prepare(`
      INSERT INTO openai_compatible_vector_store_chunks (
        id, vector_store_id, file_id, system_account_id, api_key_id, chunk_index,
        content_text, content_preview, token_estimate, keyword_index_text, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    chunks.forEach((chunk, index) => {
      insertChunk.run(
        `vschunk_${randomUUID().replace(/-/g, '')}`,
        input.vectorStoreId,
        input.fileId,
        input.systemAccountId,
        input.apiKeyId,
        index,
        chunk.contentText,
        chunk.contentPreview,
        chunk.tokenEstimate,
        chunk.keywordIndexText,
        now
      )
    })
    refreshVectorStoreBytes(input.vectorStoreId, input.systemAccountId, input.apiKeyId)
  })
  return findOpenAICompatibleVectorStoreFile({
    vectorStoreId: input.vectorStoreId,
    fileId: input.fileId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
}

export async function createOpenAICompatibleVectorStoreFileAsync(
  input: OpenAICompatibleVectorStoreFileCreateInput
): Promise<OpenAICompatibleVectorStoreFileRecord | undefined> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const store = await findOpenAICompatibleVectorStoreWithClient(client, {
    vectorStoreId: input.vectorStoreId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  const file = await findOpenAICompatibleFileWithClient(client, {
    fileId: input.fileId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  if (!store || !file) return undefined
  const chunks = input.chunks ?? []
  const now = nowIso()
  const usageBytes = input.usageBytes ?? chunks.reduce((total, chunk) => total + Buffer.byteLength(chunk.contentText, 'utf8'), 0)
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_files')} (
        vector_store_id, file_id, system_account_id, api_key_id, attributes_json,
        chunking_strategy_json, status, usage_bytes, last_error_json, created_at, updated_at, deleted_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
      ON CONFLICT(vector_store_id, file_id) DO UPDATE SET
        attributes_json = excluded.attributes_json,
        chunking_strategy_json = excluded.chunking_strategy_json,
        status = excluded.status,
        usage_bytes = excluded.usage_bytes,
        last_error_json = excluded.last_error_json,
        updated_at = excluded.updated_at,
        deleted_at = NULL
    `, [
      input.vectorStoreId,
      input.fileId,
      input.systemAccountId,
      input.apiKeyId,
      JSON.stringify(input.attributes ?? {}),
      JSON.stringify(input.chunkingStrategy ?? {}),
      input.status,
      usageBytes,
      input.lastError ? JSON.stringify(input.lastError) : null,
      now,
      now
    ])
    await tx.execute(`
      DELETE FROM ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_chunks')}
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `, [input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId])
    for (const [index, chunk] of chunks.entries()) {
      await tx.execute(`
        INSERT INTO ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_chunks')} (
          id, vector_store_id, file_id, system_account_id, api_key_id, chunk_index,
          content_text, content_preview, token_estimate, keyword_index_text, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, [
        `vschunk_${randomUUID().replace(/-/g, '')}`,
        input.vectorStoreId,
        input.fileId,
        input.systemAccountId,
        input.apiKeyId,
        index,
        chunk.contentText,
        chunk.contentPreview,
        chunk.tokenEstimate,
        chunk.keywordIndexText,
        now
      ])
    }
    await refreshVectorStoreBytesWithClient(tx, input.vectorStoreId, input.systemAccountId, input.apiKeyId)
  })
  return await findOpenAICompatibleVectorStoreFileWithClient(client, {
    vectorStoreId: input.vectorStoreId,
    fileId: input.fileId,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
}

export function listOpenAICompatibleVectorStoreFiles(
  options: OpenAICompatibleVectorStoreFileListOptions
): OpenAICompatibleVectorStoreFileListResult {
  const limit = normalizeListLimit(options.limit)
  const order = options.order === 'asc' ? 'asc' : 'desc'
  const params: Array<string | number> = [options.vectorStoreId, options.systemAccountId, options.apiKeyId]
  const clauses = [
    'vector_store_id = ?',
    'system_account_id = ?',
    'api_key_id = ?',
    'deleted_at IS NULL'
  ]
  const cursor = options.after ? findOpenAICompatibleVectorStoreFileCursor(options.after, options.vectorStoreId, options.systemAccountId, options.apiKeyId) : undefined
  if (options.after && !cursor) return { items: [], hasMore: false }
  if (cursor) {
    clauses.push(order === 'asc'
      ? '(created_at > ? OR (created_at = ? AND file_id > ?))'
      : '(created_at < ? OR (created_at = ? AND file_id < ?))')
    params.push(cursor.created_at, cursor.created_at, cursor.file_id)
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT ${vectorStoreFileSelectColumns()}
    FROM openai_compatible_vector_store_files
    WHERE ${clauses.join(' AND ')}
    ORDER BY created_at ${order.toUpperCase()}, file_id ${order.toUpperCase()}
    LIMIT ?
  `).all(...params, limit + 1) as unknown as OpenAICompatibleVectorStoreFileRow[]
  return {
    items: rows.slice(0, limit).map(vectorStoreFileFromRow),
    hasMore: rows.length > limit
  }
}

export async function listOpenAICompatibleVectorStoreFilesAsync(
  options: OpenAICompatibleVectorStoreFileListOptions
): Promise<OpenAICompatibleVectorStoreFileListResult> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const limit = normalizeListLimit(options.limit)
  const order = options.order === 'asc' ? 'asc' : 'desc'
  const params: Array<string | number> = [options.vectorStoreId, options.systemAccountId, options.apiKeyId]
  const clauses = [
    'vector_store_id = ?',
    'system_account_id = ?',
    'api_key_id = ?',
    'deleted_at IS NULL'
  ]
  const cursor = options.after ? await findOpenAICompatibleVectorStoreFileCursorWithClient(client, options.after, options.vectorStoreId, options.systemAccountId, options.apiKeyId) : undefined
  if (options.after && !cursor) return { items: [], hasMore: false }
  if (cursor) {
    clauses.push(order === 'asc'
      ? '(created_at > ? OR (created_at = ? AND file_id > ?))'
      : '(created_at < ? OR (created_at = ? AND file_id < ?))')
    params.push(cursor.created_at, cursor.created_at, cursor.file_id)
  }
  const rows = await client.query<OpenAICompatibleVectorStoreFileRow>(`
    SELECT ${vectorStoreFileSelectColumns()}
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')}
    WHERE ${clauses.join(' AND ')}
    ORDER BY created_at ${order.toUpperCase()}, file_id ${order.toUpperCase()}
    LIMIT ?
  `, [...params, limit + 1])
  return {
    items: rows.slice(0, limit).map(vectorStoreFileFromRow),
    hasMore: rows.length > limit
  }
}

export function findOpenAICompatibleVectorStoreFile(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleVectorStoreFileRecord | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT ${vectorStoreFileSelectColumns()}
    FROM openai_compatible_vector_store_files
    WHERE vector_store_id = ?
      AND file_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId) as unknown as OpenAICompatibleVectorStoreFileRow | undefined
  if (!row) return undefined
  const file = findOpenAICompatibleFile(input)
  return {
    ...vectorStoreFileFromRow(row),
    file
  }
}

export async function findOpenAICompatibleVectorStoreFileAsync(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): Promise<OpenAICompatibleVectorStoreFileRecord | undefined> {
  return await findOpenAICompatibleVectorStoreFileWithClient(await openAICompatibleVectorStorePostgresClient(), input)
}

async function findOpenAICompatibleVectorStoreFileWithClient(
  client: DatabaseClient,
  input: {
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
): Promise<OpenAICompatibleVectorStoreFileRecord | undefined> {
  const row = await client.one<OpenAICompatibleVectorStoreFileRow>(`
    SELECT ${vectorStoreFileSelectColumns()}
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')}
    WHERE vector_store_id = ?
      AND file_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId])
  if (!row) return undefined
  const file = await findOpenAICompatibleFileWithClient(client, input)
  return {
    ...vectorStoreFileFromRow(row),
    file
  }
}

export function deleteOpenAICompatibleVectorStoreFile(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleVectorStoreFileRecord | undefined {
  const existing = findOpenAICompatibleVectorStoreFile(input)
  if (!existing) return undefined
  const now = nowIso()
  runInDatabaseTransaction(() => {
    getBusinessDatabase().prepare(`
      UPDATE openai_compatible_vector_store_files
      SET deleted_at = ?,
          updated_at = ?
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `).run(now, now, input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId)
    getBusinessDatabase().prepare(`
      DELETE FROM openai_compatible_vector_store_chunks
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `).run(input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId)
    refreshVectorStoreBytes(input.vectorStoreId, input.systemAccountId, input.apiKeyId)
  })
  return {
    ...existing,
    deletedAt: now,
    updatedAt: now
  }
}

export async function deleteOpenAICompatibleVectorStoreFileAsync(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): Promise<OpenAICompatibleVectorStoreFileRecord | undefined> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const existing = await findOpenAICompatibleVectorStoreFileWithClient(client, input)
  if (!existing) return undefined
  const now = nowIso()
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_files')}
      SET deleted_at = ?,
          updated_at = ?
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
        AND deleted_at IS NULL
    `, [now, now, input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId])
    await tx.execute(`
      DELETE FROM ${openAICompatibleVectorStoreTable(tx, 'openai_compatible_vector_store_chunks')}
      WHERE vector_store_id = ?
        AND file_id = ?
        AND system_account_id = ?
        AND api_key_id = ?
    `, [input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId])
    await refreshVectorStoreBytesWithClient(tx, input.vectorStoreId, input.systemAccountId, input.apiKeyId)
  })
  return {
    ...existing,
    deletedAt: now,
    updatedAt: now
  }
}

export function searchOpenAICompatibleVectorStore(
  options: OpenAICompatibleVectorStoreSearchOptions
): OpenAICompatibleVectorStoreSearchResult[] {
  const maxResults = normalizeSearchLimit(options.maxNumResults)
  const terms = uniqueSearchTerms(options.query)
  const params: Array<string | number> = [
    options.vectorStoreId,
    options.systemAccountId,
    options.apiKeyId
  ]
  const where = [
    'c.vector_store_id = ?',
    'c.system_account_id = ?',
    'c.api_key_id = ?',
    'vsf.deleted_at IS NULL',
    "vsf.status = 'completed'"
  ]
  if (terms.length) {
    where.push(`(${terms.map(() => 'c.keyword_index_text LIKE ?').join(' OR ')})`)
    params.push(...terms.map((term) => `%${escapeSqlLike(term)}%`))
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT
      c.id,
      c.vector_store_id,
      c.file_id,
      c.chunk_index,
      c.content_text,
      c.content_preview,
      c.keyword_index_text,
      f.filename,
      vsf.attributes_json
    FROM openai_compatible_vector_store_chunks c
    JOIN openai_compatible_vector_store_files vsf
      ON vsf.vector_store_id = c.vector_store_id
      AND vsf.file_id = c.file_id
      AND vsf.system_account_id = c.system_account_id
      AND vsf.api_key_id = c.api_key_id
    JOIN openai_compatible_files f
      ON f.id = c.file_id
      AND f.system_account_id = c.system_account_id
      AND f.api_key_id = c.api_key_id
      AND f.deleted_at IS NULL
    WHERE ${where.join(' AND ')}
    ORDER BY c.file_id ASC, c.chunk_index ASC
    LIMIT ?
  `).all(...params, Math.max(maxResults * 20, 100)) as unknown as OpenAICompatibleVectorStoreSearchRow[]
  const scored = rows
    .map((row) => searchResultFromRow(row, terms))
    .filter((result) => matchesAttributeFilter(result.attributes, options.filters))
    .filter((result) => result.score >= (options.scoreThreshold ?? 0))
    .sort((left, right) => right.score - left.score || left.fileId.localeCompare(right.fileId) || left.chunkIndex - right.chunkIndex)
  return scored.slice(0, maxResults)
}

export async function searchOpenAICompatibleVectorStoreAsync(
  options: OpenAICompatibleVectorStoreSearchOptions
): Promise<OpenAICompatibleVectorStoreSearchResult[]> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const maxResults = normalizeSearchLimit(options.maxNumResults)
  const terms = uniqueSearchTerms(options.query)
  const params: Array<string | number> = [
    options.vectorStoreId,
    options.systemAccountId,
    options.apiKeyId
  ]
  const where = [
    'c.vector_store_id = ?',
    'c.system_account_id = ?',
    'c.api_key_id = ?',
    'vsf.deleted_at IS NULL',
    "vsf.status = 'completed'"
  ]
  if (terms.length) {
    where.push(`(${terms.map(() => 'c.keyword_index_text LIKE ?').join(' OR ')})`)
    params.push(...terms.map((term) => `%${escapeSqlLike(term)}%`))
  }
  const rows = await client.query<OpenAICompatibleVectorStoreSearchRow>(`
    SELECT
      c.id,
      c.vector_store_id,
      c.file_id,
      c.chunk_index,
      c.content_text,
      c.content_preview,
      c.keyword_index_text,
      f.filename,
      vsf.attributes_json
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_chunks')} c
    JOIN ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')} vsf
      ON vsf.vector_store_id = c.vector_store_id
      AND vsf.file_id = c.file_id
      AND vsf.system_account_id = c.system_account_id
      AND vsf.api_key_id = c.api_key_id
    JOIN ${openAICompatibleVectorStoreTable(client, 'openai_compatible_files')} f
      ON f.id = c.file_id
      AND f.system_account_id = c.system_account_id
      AND f.api_key_id = c.api_key_id
      AND f.deleted_at IS NULL
    WHERE ${where.join(' AND ')}
    ORDER BY c.file_id ASC, c.chunk_index ASC
    LIMIT ?
  `, [...params, Math.max(maxResults * 20, 100)])
  const scored = rows
    .map((row) => searchResultFromRow(row, terms))
    .filter((result) => matchesAttributeFilter(result.attributes, options.filters))
    .filter((result) => result.score >= (options.scoreThreshold ?? 0))
    .sort((left, right) => right.score - left.score || left.fileId.localeCompare(right.fileId) || left.chunkIndex - right.chunkIndex)
  return scored.slice(0, maxResults)
}

export function listOpenAICompatibleVectorStoreFileChunks(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
  limit?: number
}): OpenAICompatibleVectorStoreFileChunkRecord[] {
  const limit = normalizeSearchLimit(input.limit)
  const rows = getBusinessDatabase().prepare(`
    SELECT
      c.id,
      c.vector_store_id,
      c.file_id,
      c.chunk_index,
      c.content_text,
      c.content_preview,
      c.keyword_index_text,
      f.filename,
      vsf.attributes_json
    FROM openai_compatible_vector_store_chunks c
    JOIN openai_compatible_vector_store_files vsf
      ON vsf.vector_store_id = c.vector_store_id
      AND vsf.file_id = c.file_id
      AND vsf.system_account_id = c.system_account_id
      AND vsf.api_key_id = c.api_key_id
    JOIN openai_compatible_files f
      ON f.id = c.file_id
      AND f.system_account_id = c.system_account_id
      AND f.api_key_id = c.api_key_id
      AND f.deleted_at IS NULL
    WHERE c.vector_store_id = ?
      AND c.file_id = ?
      AND c.system_account_id = ?
      AND c.api_key_id = ?
      AND vsf.deleted_at IS NULL
    ORDER BY c.chunk_index ASC
    LIMIT ?
  `).all(input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId, limit) as unknown as OpenAICompatibleVectorStoreSearchRow[]
  return rows.map((row) => ({
    chunkId: row.id,
    vectorStoreId: row.vector_store_id,
    fileId: row.file_id,
    filename: row.filename,
    chunkIndex: Number(row.chunk_index) || 0,
    contentText: row.content_text,
    contentPreview: row.content_preview
  }))
}

export async function listOpenAICompatibleVectorStoreFileChunksAsync(input: {
  vectorStoreId: string
  fileId: string
  systemAccountId: string
  apiKeyId: string
  limit?: number
}): Promise<OpenAICompatibleVectorStoreFileChunkRecord[]> {
  const client = await openAICompatibleVectorStorePostgresClient()
  const limit = normalizeSearchLimit(input.limit)
  const rows = await client.query<OpenAICompatibleVectorStoreSearchRow>(`
    SELECT
      c.id,
      c.vector_store_id,
      c.file_id,
      c.chunk_index,
      c.content_text,
      c.content_preview,
      c.keyword_index_text,
      f.filename,
      vsf.attributes_json
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_chunks')} c
    JOIN ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')} vsf
      ON vsf.vector_store_id = c.vector_store_id
      AND vsf.file_id = c.file_id
      AND vsf.system_account_id = c.system_account_id
      AND vsf.api_key_id = c.api_key_id
    JOIN ${openAICompatibleVectorStoreTable(client, 'openai_compatible_files')} f
      ON f.id = c.file_id
      AND f.system_account_id = c.system_account_id
      AND f.api_key_id = c.api_key_id
      AND f.deleted_at IS NULL
    WHERE c.vector_store_id = ?
      AND c.file_id = ?
      AND c.system_account_id = ?
      AND c.api_key_id = ?
      AND vsf.deleted_at IS NULL
    ORDER BY c.chunk_index ASC
    LIMIT ?
  `, [input.vectorStoreId, input.fileId, input.systemAccountId, input.apiKeyId, limit])
  return rows.map((row) => ({
    chunkId: row.id,
    vectorStoreId: row.vector_store_id,
    fileId: row.file_id,
    filename: row.filename,
    chunkIndex: Number(row.chunk_index) || 0,
    contentText: row.content_text,
    contentPreview: row.content_preview
  }))
}

function refreshVectorStoreBytes(vectorStoreId: string, systemAccountId: string, apiKeyId: string): void {
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_vector_stores
    SET bytes = COALESCE((
          SELECT SUM(usage_bytes)
          FROM openai_compatible_vector_store_files
          WHERE vector_store_id = ?
            AND system_account_id = ?
            AND api_key_id = ?
            AND deleted_at IS NULL
            AND status = 'completed'
        ), 0),
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
  `).run(vectorStoreId, systemAccountId, apiKeyId, nowIso(), vectorStoreId, systemAccountId, apiKeyId)
}

async function refreshVectorStoreBytesWithClient(
  client: DatabaseClient,
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): Promise<void> {
  await client.execute(`
    UPDATE ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_stores')}
    SET bytes = COALESCE((
          SELECT SUM(usage_bytes)
          FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')}
          WHERE vector_store_id = ?
            AND system_account_id = ?
            AND api_key_id = ?
            AND deleted_at IS NULL
            AND status = 'completed'
        ), 0),
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
  `, [vectorStoreId, systemAccountId, apiKeyId, nowIso(), vectorStoreId, systemAccountId, apiKeyId])
}

function vectorStoreFromRow(row: OpenAICompatibleVectorStoreRow): OpenAICompatibleVectorStoreRecord {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    name: row.name ?? undefined,
    description: row.description ?? undefined,
    metadata: parseJsonObject(row.metadata_json),
    bytes: Number(row.bytes) || 0,
    status: row.status === 'deleted' ? 'deleted' : 'active',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    expiresAfterAnchor: row.expires_after_anchor ?? undefined,
    expiresAfterDays: row.expires_after_days ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    deletedAt: row.deleted_at ?? undefined,
    fileCounts: vectorStoreFileCounts(row.id, row.system_account_id, row.api_key_id)
  }
}

async function vectorStoreFromRowWithClient(
  client: DatabaseClient,
  row: OpenAICompatibleVectorStoreRow
): Promise<OpenAICompatibleVectorStoreRecord> {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    name: row.name ?? undefined,
    description: row.description ?? undefined,
    metadata: parseJsonObject(row.metadata_json),
    bytes: Number(row.bytes) || 0,
    status: row.status === 'deleted' ? 'deleted' : 'active',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    expiresAfterAnchor: row.expires_after_anchor ?? undefined,
    expiresAfterDays: row.expires_after_days ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    deletedAt: row.deleted_at ?? undefined,
    fileCounts: await vectorStoreFileCountsWithClient(client, row.id, row.system_account_id, row.api_key_id)
  }
}

function vectorStoreFileFromRow(row: OpenAICompatibleVectorStoreFileRow): OpenAICompatibleVectorStoreFileRecord {
  const status = vectorStoreFileStatus(row.status)
  return {
    vectorStoreId: row.vector_store_id,
    fileId: row.file_id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    attributes: parseJsonObject(row.attributes_json),
    chunkingStrategy: parseJsonObject(row.chunking_strategy_json),
    status,
    usageBytes: Number(row.usage_bytes) || 0,
    lastError: row.last_error_json ? parseJsonObject(row.last_error_json) : undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    deletedAt: row.deleted_at ?? undefined
  }
}

function vectorStoreFileCounts(
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): OpenAICompatibleVectorStoreFileCounts {
  const rows = getBusinessDatabase().prepare(`
    SELECT status, COUNT(*) AS count
    FROM openai_compatible_vector_store_files
    WHERE vector_store_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    GROUP BY status
  `).all(vectorStoreId, systemAccountId, apiKeyId) as unknown as Array<{ status: string; count: number }>
  const counts: OpenAICompatibleVectorStoreFileCounts = {
    inProgress: 0,
    completed: 0,
    failed: 0,
    cancelled: 0,
    total: 0
  }
  for (const row of rows) {
    const count = Number(row.count) || 0
    counts.total += count
    const status = vectorStoreFileStatus(row.status)
    if (status === 'in_progress') counts.inProgress += count
    else if (status === 'completed') counts.completed += count
    else if (status === 'failed') counts.failed += count
    else if (status === 'cancelled') counts.cancelled += count
  }
  return counts
}

async function vectorStoreFileCountsWithClient(
  client: DatabaseClient,
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): Promise<OpenAICompatibleVectorStoreFileCounts> {
  const rows = await client.query<{ status: string; count: number }>(`
    SELECT status, COUNT(*) AS count
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')}
    WHERE vector_store_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    GROUP BY status
  `, [vectorStoreId, systemAccountId, apiKeyId])
  const counts: OpenAICompatibleVectorStoreFileCounts = {
    inProgress: 0,
    completed: 0,
    failed: 0,
    cancelled: 0,
    total: 0
  }
  for (const row of rows) {
    const count = Number(row.count) || 0
    counts.total += count
    const status = vectorStoreFileStatus(row.status)
    if (status === 'in_progress') counts.inProgress += count
    else if (status === 'completed') counts.completed += count
    else if (status === 'failed') counts.failed += count
    else if (status === 'cancelled') counts.cancelled += count
  }
  return counts
}

function searchResultFromRow(
  row: OpenAICompatibleVectorStoreSearchRow,
  terms: string[]
): OpenAICompatibleVectorStoreSearchResult {
  return {
    chunkId: row.id,
    vectorStoreId: row.vector_store_id,
    fileId: row.file_id,
    filename: row.filename,
    attributes: parseJsonObject(row.attributes_json),
    chunkIndex: Number(row.chunk_index) || 0,
    score: scoreKeywordMatch(row.keyword_index_text, terms),
    contentText: row.content_text,
    contentPreview: row.content_preview
  }
}

function scoreKeywordMatch(text: string, terms: string[]): number {
  if (!terms.length) return 0.01
  const haystack = text.toLowerCase()
  let score = 0
  for (const term of terms) {
    let from = 0
    let count = 0
    while (from < haystack.length) {
      const index = haystack.indexOf(term, from)
      if (index < 0) break
      count += 1
      from = index + term.length
    }
    if (count > 0) score += 1 + Math.log2(count)
  }
  return Math.min(1, score / Math.max(terms.length, 1))
}

function uniqueSearchTerms(query: string): string[] {
  const terms = query
    .toLowerCase()
    .split(/[^\p{L}\p{N}_-]+/u)
    .map((term) => term.trim())
    .filter((term) => term.length >= 2)
  return [...new Set(terms)].slice(0, 20)
}

function matchesAttributeFilter(attributes: Record<string, unknown>, filter: Record<string, unknown> | undefined): boolean {
  if (!filter || Object.keys(filter).length === 0) return true
  const type = typeof filter.type === 'string' ? filter.type : undefined
  if (type === 'and') {
    const filters = Array.isArray(filter.filters) ? filter.filters : []
    return filters.every((item) => matchesAttributeFilter(attributes, isRecord(item) ? item : undefined))
  }
  if (type === 'or') {
    const filters = Array.isArray(filter.filters) ? filter.filters : []
    return filters.some((item) => matchesAttributeFilter(attributes, isRecord(item) ? item : undefined))
  }
  const key = typeof filter.key === 'string' ? filter.key : undefined
  if (!key) return true
  const actual = attributes[key]
  const expected = filter.value
  if (type === 'ne') return !jsonScalarEqual(actual, expected)
  if (type === 'in') return Array.isArray(expected) && expected.some((item) => jsonScalarEqual(actual, item))
  if (type === 'nin') return Array.isArray(expected) && !expected.some((item) => jsonScalarEqual(actual, item))
  if (type === 'gt' || type === 'gte' || type === 'lt' || type === 'lte') {
    const left = typeof actual === 'number' ? actual : typeof actual === 'string' ? Number(actual) : NaN
    const right = typeof expected === 'number' ? expected : typeof expected === 'string' ? Number(expected) : NaN
    if (!Number.isFinite(left) || !Number.isFinite(right)) return false
    if (type === 'gt') return left > right
    if (type === 'gte') return left >= right
    if (type === 'lt') return left < right
    return left <= right
  }
  return jsonScalarEqual(actual, expected)
}

function jsonScalarEqual(left: unknown, right: unknown): boolean {
  return left === right
}

function vectorStoreSelectColumns(): string {
  return [
    'id',
    'system_account_id',
    'api_key_id',
    'name',
    'description',
    'metadata_json',
    'bytes',
    'status',
    'created_at',
    'updated_at',
    'expires_after_anchor',
    'expires_after_days',
    'expires_at',
    'deleted_at'
  ].join(', ')
}

function vectorStoreFileSelectColumns(): string {
  return [
    'vector_store_id',
    'file_id',
    'system_account_id',
    'api_key_id',
    'attributes_json',
    'chunking_strategy_json',
    'status',
    'usage_bytes',
    'last_error_json',
    'created_at',
    'updated_at',
    'deleted_at'
  ].join(', ')
}

function findOpenAICompatibleVectorStoreCursor(
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): { id: string; created_at: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, created_at
    FROM openai_compatible_vector_stores
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(vectorStoreId, systemAccountId, apiKeyId) as unknown as { id: string; created_at: string } | undefined
}

async function findOpenAICompatibleVectorStoreCursorWithClient(
  client: DatabaseClient,
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): Promise<{ id: string; created_at: string } | undefined> {
  return await client.one<{ id: string; created_at: string }>(`
    SELECT id, created_at
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_stores')}
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [vectorStoreId, systemAccountId, apiKeyId])
}

function findOpenAICompatibleVectorStoreFileCursor(
  fileId: string,
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): { file_id: string; created_at: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT file_id, created_at
    FROM openai_compatible_vector_store_files
    WHERE file_id = ?
      AND vector_store_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(fileId, vectorStoreId, systemAccountId, apiKeyId) as unknown as { file_id: string; created_at: string } | undefined
}

async function findOpenAICompatibleVectorStoreFileCursorWithClient(
  client: DatabaseClient,
  fileId: string,
  vectorStoreId: string,
  systemAccountId: string,
  apiKeyId: string
): Promise<{ file_id: string; created_at: string } | undefined> {
  return await client.one<{ file_id: string; created_at: string }>(`
    SELECT file_id, created_at
    FROM ${openAICompatibleVectorStoreTable(client, 'openai_compatible_vector_store_files')}
    WHERE file_id = ?
      AND vector_store_id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [fileId, vectorStoreId, systemAccountId, apiKeyId])
}

function vectorStoreFileStatus(value: string): OpenAICompatibleVectorStoreFileStatus {
  if (value === 'completed' || value === 'failed' || value === 'cancelled') return value
  return 'in_progress'
}

function normalizeListLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultVectorStoreListLimit
  return Math.max(1, Math.min(Math.trunc(value ?? defaultVectorStoreListLimit), maxVectorStoreListLimit))
}

function normalizeSearchLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultSearchResults
  return Math.max(1, Math.min(Math.trunc(value ?? defaultSearchResults), maxSearchResults))
}

function parseJsonObject(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value) as unknown
    return isRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function escapeSqlLike(value: string): string {
  return value.replace(/[%_]/g, (match) => `\\${match}`)
}

async function openAICompatibleVectorStorePostgresClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function openAICompatibleVectorStoreTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
