import { getBusinessDatabase, nowIso } from './database.js'

export interface OpenAICompatibleFileRecord {
  id: string
  systemAccountId: string
  apiKeyId: string
  purpose: string
  filename: string
  bytes: number
  mediaType?: string
  storageKey: string
  sha256: string
  status: 'processed' | 'deleted'
  createdAt: string
  updatedAt: string
  expiresAt?: string
  deletedAt?: string
}

export interface OpenAICompatibleFileCreateInput {
  id: string
  systemAccountId: string
  apiKeyId: string
  purpose: string
  filename: string
  bytes: number
  mediaType?: string
  storageKey: string
  sha256: string
  expiresAt?: string
}

export interface OpenAICompatibleFileListOptions {
  systemAccountId: string
  apiKeyId: string
  purpose?: string
  limit?: number
  order?: 'asc' | 'desc'
  after?: string
}

export interface OpenAICompatibleFileListResult {
  items: OpenAICompatibleFileRecord[]
  hasMore: boolean
}

interface OpenAICompatibleFileRow {
  id: string
  system_account_id: string
  api_key_id: string
  purpose: string
  filename: string
  bytes: number
  media_type: string | null
  storage_key: string
  sha256: string
  status: string
  created_at: string
  updated_at: string
  expires_at: string | null
  deleted_at: string | null
}

const maxOpenAICompatibleFilesListLimit = 100
const defaultOpenAICompatibleFilesListLimit = 20

export function createOpenAICompatibleFile(input: OpenAICompatibleFileCreateInput): OpenAICompatibleFileRecord {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO openai_compatible_files (
      id, system_account_id, api_key_id, purpose, filename, bytes, media_type,
      storage_key, sha256, status, created_at, updated_at, expires_at, deleted_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'processed', ?, ?, ?, NULL)
  `).run(
    input.id,
    input.systemAccountId,
    input.apiKeyId,
    input.purpose,
    input.filename,
    input.bytes,
    input.mediaType ?? null,
    input.storageKey,
    input.sha256,
    now,
    now,
    input.expiresAt ?? null
  )
  const record = findOpenAICompatibleFile({
    fileId: input.id,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId
  })
  if (!record) {
    throw new Error(`OpenAI compatible file ${input.id} was not readable after insert`)
  }
  return record
}

export function listOpenAICompatibleFiles(options: OpenAICompatibleFileListOptions): OpenAICompatibleFileListResult {
  const limit = normalizeListLimit(options.limit)
  const order = options.order === 'asc' ? 'asc' : 'desc'
  const params: Array<string | number | null> = [options.systemAccountId, options.apiKeyId]
  const clauses = [
    'system_account_id = ?',
    'api_key_id = ?',
    'deleted_at IS NULL'
  ]
  const purpose = options.purpose?.trim()
  if (purpose) {
    clauses.push('purpose = ?')
    params.push(purpose)
  }
  const cursor = options.after ? findOpenAICompatibleFileCursor(options.after, options.systemAccountId, options.apiKeyId) : undefined
  if (options.after && !cursor) {
    return { items: [], hasMore: false }
  }
  if (cursor) {
    clauses.push(order === 'asc'
      ? '(created_at > ? OR (created_at = ? AND id > ?))'
      : '(created_at < ? OR (created_at = ? AND id < ?))')
    params.push(cursor.created_at, cursor.created_at, cursor.id)
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleFileSelectColumns()}
    FROM openai_compatible_files
    WHERE ${clauses.join(' AND ')}
    ORDER BY created_at ${order.toUpperCase()}, id ${order.toUpperCase()}
    LIMIT ?
  `).all(...params, limit + 1) as unknown as OpenAICompatibleFileRow[]
  return {
    items: rows.slice(0, limit).map(openAICompatibleFileFromRow),
    hasMore: rows.length > limit
  }
}

export function findOpenAICompatibleFile(input: {
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleFileRecord | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT ${openAICompatibleFileSelectColumns()}
    FROM openai_compatible_files
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(input.fileId, input.systemAccountId, input.apiKeyId) as unknown as OpenAICompatibleFileRow | undefined
  return row ? openAICompatibleFileFromRow(row) : undefined
}

export function deleteOpenAICompatibleFile(input: {
  fileId: string
  systemAccountId: string
  apiKeyId: string
}): OpenAICompatibleFileRecord | undefined {
  const existing = findOpenAICompatibleFile(input)
  if (!existing) return undefined
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE openai_compatible_files
    SET status = 'deleted',
        deleted_at = ?,
        updated_at = ?
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
  `).run(now, now, input.fileId, input.systemAccountId, input.apiKeyId)
  return {
    ...existing,
    status: 'deleted',
    deletedAt: now,
    updatedAt: now
  }
}

function findOpenAICompatibleFileCursor(
  fileId: string,
  systemAccountId: string,
  apiKeyId: string
): { id: string; created_at: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, created_at
    FROM openai_compatible_files
    WHERE id = ?
      AND system_account_id = ?
      AND api_key_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(fileId, systemAccountId, apiKeyId) as unknown as { id: string; created_at: string } | undefined
}

function openAICompatibleFileSelectColumns(): string {
  return [
    'id',
    'system_account_id',
    'api_key_id',
    'purpose',
    'filename',
    'bytes',
    'media_type',
    'storage_key',
    'sha256',
    'status',
    'created_at',
    'updated_at',
    'expires_at',
    'deleted_at'
  ].join(', ')
}

function openAICompatibleFileFromRow(row: OpenAICompatibleFileRow): OpenAICompatibleFileRecord {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    apiKeyId: row.api_key_id,
    purpose: row.purpose,
    filename: row.filename,
    bytes: Number(row.bytes) || 0,
    mediaType: row.media_type ?? undefined,
    storageKey: row.storage_key,
    sha256: row.sha256,
    status: row.status === 'deleted' ? 'deleted' : 'processed',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    expiresAt: row.expires_at ?? undefined,
    deletedAt: row.deleted_at ?? undefined
  }
}

function normalizeListLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultOpenAICompatibleFilesListLimit
  return Math.max(1, Math.min(Math.trunc(value ?? defaultOpenAICompatibleFilesListLimit), maxOpenAICompatibleFilesListLimit))
}
