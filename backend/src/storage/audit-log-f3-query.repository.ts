import { readFile } from 'node:fs/promises'
import { join, resolve, sep } from 'node:path'
import { createRequire } from 'node:module'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'
import { StringDecoder } from 'node:string_decoder'

import {
  auditErrorGroupFromRow,
  auditLogAttemptFromRow,
  auditLogListItemFromRow,
  auditLogPayloadSummaryFromRow,
  auditLogSummaryFromRow,
  type AuditLogRow
} from './audit-log-f3-mappers.js'
import {
  auditLogDefaultPageSize,
  listColumns as auditLogListSelectColumns,
  auditLogMaxPageSize,
  errorGroupColumns as auditErrorGroupListSelectColumns,
  errorGroupDefaultPageSize,
  errorGroupMaxPageSize,
  normalizeAuditLogPage,
  normalizePage,
  normalizePageSize,
  persistedAuditTrafficSourceParams
} from './audit-log-f3-query-helpers.js'
import { auditPayloadBodyDetail, type AuditPayloadBlobStorageStatus, type AuditPayloadBlobWindow } from './audit-log-f3-query-helpers.js'
import type {
  AuditErrorGroupListOptions,
  AuditErrorGroupListResult,
  AuditLogAttemptSummary,
  AuditLogDetail,
  AuditLogListItem,
  AuditLogListOptions,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogPayloadReadOptions,
  AuditLogPayloadSummary,
  AuditErrorGroupSummary
} from './audit-log-f3-types.js'
import { pagedTotalUpperBound, takePageRows, textPrefixUpperBound } from './audit-log-f3-query-helpers.js'
import { convertQuestionPlaceholdersToPostgres } from './database-client.js'
const nonPersistedAuditTrafficSources = ['account_health_check', 'runtime_recovery_probe', 'cooldown_retest'] as const
const optionalString = (value: unknown): string | undefined => typeof value === 'string' && value.trim() ? value : undefined

const f3RequiredTables = [
  'audit_logs',
  'audit_log_attempts',
  'audit_payload_blobs',
  'audit_payload_refs',
  'audit_error_groups'
] as const

const f3BooleanColumns = new Set([
  'model_mapping_applied',
  'stream',
  'success',
  'attempt_model_mapping_applied'
])

const f3DefaultPayloadReadLimit = 256 * 1024
const f3MaxPayloadReadLimit = 1024 * 1024
const f3HotSearchMaxFiles = 2
const f3HotSearchMaxDirectoryEntries = 4096
const f3HotSearchMaxScanBytes = 4 * 1024 * 1024
const f3HotSearchMaxScanLines = 10_000
const f3HotSearchMaxLineBytes = 256 * 1024
const f3HotSearchReadChunkBytes = 64 * 1024
const f3HotSearchFileNamePattern = /^audit-hot-\d{10}\.ndjson$/

export interface AuditLogF3QueryOptions {
  /** Dedicated F3 SQLite fact file. Exactly one database source is required. */
  sqlitePath?: string
  /** Dedicated PostgreSQL connection string. Exactly one database source is required. */
  postgresUrl?: string
  /** PostgreSQL schema containing the F3 tables. */
  postgresSchema?: string
  /** F3 payload/blob root. Missing files are reported as file_missing. */
  payloadBlobDirectory?: string
  /** Optional PostgreSQL pool size for the read-only adapter. */
  postgresPoolMax?: number
  /** Dedicated F3 hot-search root. Read-only searches use this directory. */
  hotSearchDirectory?: string
}

export type AuditLogF3QueryMode = 'sqlite' | 'postgres'

export interface AuditLogF3Runtime {
  mode: AuditLogF3QueryMode
  readOnly: true
  queryOnly: true
  schemaReady: true
}

export class AuditLogF3SchemaError extends Error {
  readonly mode: AuditLogF3QueryMode
  readonly missingTables: readonly string[]

  constructor(mode: AuditLogF3QueryMode, missingTables: readonly string[]) {
    super(`F3 审计查询库缺少必需 schema：${missingTables.join(', ')}`)
    this.name = 'AuditLogF3SchemaError'
    this.mode = mode
    this.missingTables = [...missingTables]
  }
}

export interface AuditLogF3QueryRepository {
  readonly mode: AuditLogF3QueryMode
  listAuditLogs(options?: AuditLogListOptions): Promise<AuditLogListResult>
  listAuditLogsByIds(ids: readonly string[]): Promise<AuditLogListItem[]>
  getAuditLogDetail(id: string): Promise<AuditLogDetail | undefined>
  listAuditLogAttempts(auditLogId: string): Promise<AuditLogAttemptSummary[]>
  getAuditLogAttempts(auditLogId: string): Promise<AuditLogAttemptSummary[]>
  listAuditLogPayloads(auditLogId: string): Promise<AuditLogPayloadSummary[]>
  getAuditLogPayloads(auditLogId: string): Promise<AuditLogPayloadSummary[]>
  getAuditLogPayload(auditLogId: string, payloadId: string, options?: AuditLogPayloadReadOptions): Promise<AuditLogPayloadDetail | undefined>
  listAuditErrorGroups(options?: AuditErrorGroupListOptions): Promise<AuditErrorGroupListResult>
  listAuditErrorGroupEvents(errorGroupId: string, options?: AuditLogListOptions): Promise<AuditLogListResult>
  searchHot(options: AuditLogF3HotSearchOptions): Promise<AuditLogF3HotSearchResult>
  getRuntime(): AuditLogF3Runtime
  getAuditLogRuntime(): AuditLogF3Runtime
  runtime(): AuditLogF3Runtime
  close(): Promise<void>
}

export interface AuditLogF3HotSearchOptions {
  keywords: string[]
  limit?: number
  startAt?: string
  endAt?: string
}

export interface AuditLogF3HotSearchResult {
  available: boolean
  elapsedMs: number
  keywords: string[]
  startAt: string
  endAt: string
  limit: number
  auditLogIds: string[]
  truncated: boolean
  scannedFileCount: number
  message?: string
}

interface QueryBackend {
  readonly mode: AuditLogF3QueryMode
  query<T extends AuditLogRow = AuditLogRow>(sql: string, params?: readonly unknown[]): Promise<T[]>
  one<T extends AuditLogRow = AuditLogRow>(sql: string, params?: readonly unknown[]): Promise<T | undefined>
  close(): Promise<void>
}

/**
 * Open a dedicated F3 query repository. The adapter never creates or mutates
 * schema objects and never exposes a write operation.
 */
export async function createAuditLogF3QueryRepository(options: AuditLogF3QueryOptions): Promise<AuditLogF3QueryRepository> {
  const source = selectF3QuerySource(options)
  const backend = source.mode === 'sqlite'
    ? await createSqliteBackend(source.path)
    : await createPostgresBackend(source.url, source.schema, options.postgresPoolMax)
  try {
    await assertF3Schema(backend, source.schema)
    return new AuditLogF3QueryRepositoryImpl(backend, source.schema, options.payloadBlobDirectory, options.hotSearchDirectory)
  } catch (error) {
    await backend.close().catch(() => undefined)
    throw error
  }
}

export const openAuditLogF3QueryRepository = createAuditLogF3QueryRepository

class AuditLogF3QueryRepositoryImpl implements AuditLogF3QueryRepository {
  readonly mode: AuditLogF3QueryMode
  private readonly backend: QueryBackend
  private readonly schema: string
  private readonly payloadBlobDirectory?: string
  private readonly hotSearchDirectory?: string

  constructor(backend: QueryBackend, schema: string, payloadBlobDirectory?: string, hotSearchDirectory?: string) {
    this.backend = backend
    this.mode = backend.mode
    this.schema = schema
    this.payloadBlobDirectory = payloadBlobDirectory?.trim() ? resolve(payloadBlobDirectory) : undefined
    this.hotSearchDirectory = hotSearchDirectory?.trim() ? resolve(hotSearchDirectory) : undefined
  }

  async listAuditLogs(options: AuditLogListOptions = {}): Promise<AuditLogListResult> {
    const filters = buildF3AuditLogFilters(options, this.mode)
    const pageSize = normalizePageSize(options.pageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
    const page = normalizeAuditLogPage(options.page, pageSize, options.sessionId)
    const offset = (page - 1) * pageSize
    const table = this.table('audit_logs')
    const rows = await this.backend.query(`
      SELECT ${auditLogListSelectColumns('al')}
      FROM ${table} al
      ${filters.clause}
      ORDER BY al.created_at DESC, al.id DESC
      LIMIT ? OFFSET ?
    `, [...filters.params, pageSize + 1, offset])
    const pageRows = takePageRows(rows, pageSize)
    const items = pageRows.rows.map((row) => auditLogListItemFromRow(normalizeF3Row(row), new Map()))
    return {
      items,
      total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
      hasMore: pageRows.hasMore,
      page,
      pageSize
    }
  }

  async listAuditLogsByIds(ids: readonly string[]): Promise<AuditLogListItem[]> {
    const uniqueIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (uniqueIds.length === 0) return []
    const table = this.table('audit_logs')
    const rows: AuditLogRow[] = []
    for (let offset = 0; offset < uniqueIds.length; offset += 900) {
      const chunk = uniqueIds.slice(offset, offset + 900)
      const placeholders = chunk.map(() => '?').join(', ')
      rows.push(...await this.backend.query(`
        SELECT ${auditLogListSelectColumns('al')}
        FROM ${table} al
        WHERE al.id IN (${placeholders})
          AND ${f3PersistedTrafficClause('al')}
      `, [...chunk, ...persistedAuditTrafficSourceParams()]))
    }
    const order = new Map(uniqueIds.map((id, index) => [id, index]))
    rows.sort((left, right) => (order.get(String(left.id ?? '')) ?? Number.MAX_SAFE_INTEGER) - (order.get(String(right.id ?? '')) ?? Number.MAX_SAFE_INTEGER))
    return rows.map((row) => auditLogListItemFromRow(normalizeF3Row(row), new Map()))
  }

  async getAuditLogDetail(id: string): Promise<AuditLogDetail | undefined> {
    const logId = id.trim()
    if (!logId) return undefined
    const table = this.table('audit_logs')
    const row = await this.backend.one(`
      SELECT al.*
      FROM ${table} al
      WHERE al.id = ?
        AND ${f3PersistedTrafficClause('al')}
    `, [logId, ...persistedAuditTrafficSourceParams()])
    if (!row) return undefined
    const normalizedRow = normalizeF3Row(row)
    const [attempts, payloads] = await Promise.all([
      this.listAuditLogAttempts(logId),
      this.listAuditLogPayloads(logId)
    ])
    const errorGroupId = optionalString(normalizedRow.error_group_id)
    const errorGroup = errorGroupId ? await this.getAuditErrorGroupById(errorGroupId) : undefined
    return {
      ...auditLogSummaryFromRow(normalizedRow, new Map()),
      conversationKey: optionalString(normalizedRow.conversation_key),
      attempts,
      errorGroup,
      payloads
    }
  }

  async listAuditLogAttempts(auditLogId: string): Promise<AuditLogAttemptSummary[]> {
    const table = this.table('audit_log_attempts')
    const rows = await this.backend.query(`
      SELECT *
      FROM ${table}
      WHERE audit_log_id = ?
      ORDER BY attempt_index ASC, id ASC
    `, [auditLogId.trim()])
    return rows.map((row) => auditLogAttemptFromRow(normalizeF3Row(row), new Map(), new Map()))
  }

  async getAuditLogAttempts(auditLogId: string): Promise<AuditLogAttemptSummary[]> {
    return this.listAuditLogAttempts(auditLogId)
  }

  async listAuditLogPayloads(auditLogId: string): Promise<AuditLogPayloadSummary[]> {
    const table = this.table('audit_payload_refs')
    const rows = await this.backend.query(`
      SELECT *
      FROM ${table}
      WHERE audit_log_id = ?
      ORDER BY sequence_index ASC, id ASC
    `, [auditLogId.trim()])
    return rows.map((row) => auditLogPayloadSummaryFromRow(normalizeF3Row(row)))
  }

  async getAuditLogPayloads(auditLogId: string): Promise<AuditLogPayloadSummary[]> {
    return this.listAuditLogPayloads(auditLogId)
  }

  async getAuditLogPayload(auditLogId: string, payloadId: string, options: AuditLogPayloadReadOptions = {}): Promise<AuditLogPayloadDetail | undefined> {
    const refs = this.table('audit_payload_refs')
    const logs = this.table('audit_logs')
    const row = await this.backend.one(`
      SELECT refs.*
      FROM ${refs} refs
      WHERE refs.audit_log_id = ?
        AND refs.id = ?
        AND EXISTS (
          SELECT 1
          FROM ${logs} al
          WHERE al.id = refs.audit_log_id
            AND ${f3PersistedTrafficClause('al')}
        )
    `, [auditLogId.trim(), payloadId.trim(), ...persistedAuditTrafficSourceParams()])
    if (!row) return undefined
    const normalizedRow = normalizeF3Row(row)
    const summary = auditLogPayloadSummaryFromRow(normalizedRow)
    const includeHeaders = shouldIncludeAuditPayloadHeaders(options)
    const headers = includeHeaders
      ? await this.readHeadersBlob(optionalString(normalizedRow.headers_blob_id))
      : undefined
    const bodyWindow = await this.readBlobWindow(optionalString(normalizedRow.body_blob_id), options)
    return {
      ...summary,
      headers: headers?.headers,
      headersIncluded: headers !== undefined,
      ...auditPayloadBodyDetail(bodyWindow.bytes),
      headersStorageStatus: headers?.storageStatus ?? 'not_saved',
      bodyStorageStatus: bodyWindow.storageStatus,
      bodyOffset: bodyWindow.offset,
      bodyLimit: bodyWindow.limit,
      bodyBytesReturned: bodyWindow.bytes?.byteLength ?? 0,
      bodyTotalBytes: bodyWindow.totalBytes,
      bodyNextOffset: bodyWindow.nextOffset,
      bodyTruncated: bodyWindow.truncated
    }
  }

  async listAuditErrorGroups(options: AuditErrorGroupListOptions = {}): Promise<AuditErrorGroupListResult> {
    const filters = buildF3ErrorGroupFilters(options, this.mode)
    const pageSize = normalizePageSize(options.pageSize, errorGroupDefaultPageSize, errorGroupMaxPageSize)
    const page = normalizePage(options.page, pageSize)
    const offset = (page - 1) * pageSize
    const table = this.table('audit_error_groups')
    const rows = await this.backend.query(`
      SELECT ${auditErrorGroupListSelectColumns('aeg')}
      FROM ${table} aeg
      ${filters.clause}
      ORDER BY aeg.updated_at DESC, aeg.id DESC
      LIMIT ? OFFSET ?
    `, [...filters.params, pageSize + 1, offset])
    const pageRows = takePageRows(rows, pageSize)
    const items = pageRows.rows.map((row) => auditErrorGroupFromRow(normalizeF3Row(row), new Map()))
    return {
      items,
      total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
      hasMore: pageRows.hasMore,
      page,
      pageSize
    }
  }

  async listAuditErrorGroupEvents(errorGroupId: string, options: AuditLogListOptions = {}): Promise<AuditLogListResult> {
    return this.listAuditLogs({ ...options, errorGroupId: errorGroupId.trim() })
  }

  async searchHot(options: AuditLogF3HotSearchOptions): Promise<AuditLogF3HotSearchResult> {
    const startedAt = performance.now()
    const keywords = normalizeHotKeywords(options.keywords)
    const limit = normalizeHotLimit(options.limit)
    const now = Date.now()
    const endMs = clampHotTime(Date.parse(options.endAt ?? ''), now)
    const startMs = Math.max(endMs - 60 * 60 * 1000, clampHotTime(Date.parse(options.startAt ?? ''), endMs - 60 * 60 * 1000))
    const range = { startAt: new Date(startMs).toISOString(), endAt: new Date(endMs).toISOString() }
    const base = { available: true, elapsedMs: 0, keywords, ...range, limit, auditLogIds: [] as string[], truncated: false, scannedFileCount: 0 }
    if (keywords.length === 0) return { ...base, elapsedMs: Math.round(performance.now() - startedAt), message: '请输入要搜索的审计内容关键字' }
    if (!this.hotSearchDirectory) return { ...base, available: false, elapsedMs: Math.round(performance.now() - startedAt), message: 'F3 审计内容搜索目录未配置' }
    const files = await listF3HotSearchFiles(this.hotSearchDirectory, startMs, endMs)
    if (files.directoryMissing) return { ...base, elapsedMs: Math.round(performance.now() - startedAt), message: '最近 1 小时没有可搜索的审计内容' }
    const seen = new Map<string, number>()
    let remainingBytes = f3HotSearchMaxScanBytes
    let remainingLines = f3HotSearchMaxScanLines
    let contentTruncated = false
    let scannedFileCount = 0
    for (const filePath of files.paths) {
      if (remainingBytes <= 0 || remainingLines <= 0) {
        contentTruncated = true
        break
      }
      const scan = await scanF3HotSearchFile({ filePath, keywords, startMs, endMs, maxBytes: remainingBytes, maxLines: remainingLines, seen })
      scannedFileCount += 1
      remainingBytes -= scan.bytesRead
      remainingLines -= scan.linesRead
      contentTruncated ||= scan.truncated
    }
    const ids = [...seen.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([id]) => id)
    const resultTruncated = ids.length > limit
    const truncated = files.truncated || contentTruncated || resultTruncated
    const auditLogIds = ids.slice(0, limit)
    const messages = [
      files.truncated ? '热搜索文件范围超过读取上限，结果可能不完整' : undefined,
      contentTruncated ? '热搜索内容超过读取上限，结果可能不完整' : undefined,
      resultTruncated ? `结果超过 ${limit} 条，已按最新优先截断显示` : undefined
    ].filter((message): message is string => Boolean(message))
    return { ...base, elapsedMs: Math.round(performance.now() - startedAt), auditLogIds, truncated, scannedFileCount, message: messages.join('；') || undefined }
  }

  getRuntime(): AuditLogF3Runtime {
    return { mode: this.mode, readOnly: true, queryOnly: true, schemaReady: true }
  }

  getAuditLogRuntime(): AuditLogF3Runtime {
    return this.getRuntime()
  }

  runtime(): AuditLogF3Runtime {
    return this.getRuntime()
  }

  async close(): Promise<void> {
    await this.backend.close()
  }

  private async getAuditErrorGroupById(id: string): Promise<AuditErrorGroupSummary | undefined> {
    const table = this.table('audit_error_groups')
    const row = await this.backend.one(`
      SELECT ${auditErrorGroupListSelectColumns('aeg')}
      FROM ${table} aeg
      WHERE aeg.id = ?
    `, [id])
    return row ? auditErrorGroupFromRow(normalizeF3Row(row), new Map()) : undefined
  }

  private table(name: typeof f3RequiredTables[number]): string {
    return this.mode === 'postgres'
      ? `${quoteIdentifier(this.schema)}.${quoteIdentifier(name)}`
      : quoteIdentifier(name)
  }

  private async readHeadersBlob(blobId: string | undefined): Promise<{ headers?: Record<string, string | string[]>; storageStatus: AuditPayloadBlobStorageStatus } | undefined> {
    if (!blobId) return undefined
    const window = await this.readBlobWindow(blobId, { offset: 0, limit: f3MaxPayloadReadLimit })
    if (!window.bytes) return { storageStatus: window.storageStatus }
    try {
      return { headers: JSON.parse(window.bytes.toString('utf8')) as Record<string, string | string[]>, storageStatus: window.storageStatus }
    } catch {
      return { storageStatus: window.storageStatus }
    }
  }

  private async readBlobWindow(blobId: string | undefined, options: { offset?: number; limit?: number; full?: boolean }): Promise<AuditPayloadBlobWindow> {
    const full = options.full === true
    const requestedOffset = normalizePayloadOffset(options.offset)
    const requestedLimit = full ? 0 : normalizePayloadLimit(options.limit)
    if (!blobId) return emptyBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'not_saved')
    const row = await this.backend.one(`
      SELECT storage_key, compression, raw_size_bytes, compressed_size_bytes
      FROM ${this.table('audit_payload_blobs')}
      WHERE id = ?
    `, [blobId])
    if (!row) return emptyBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'metadata_missing')
    const storageKey = optionalString(row.storage_key)
    const rawSize = finiteNonNegativeNumber(row.raw_size_bytes)
    const compressedSize = finiteNonNegativeNumber(row.compressed_size_bytes)
    if (!storageKey || !this.payloadBlobDirectory) {
      return emptyBlobWindow(full ? 0 : requestedOffset, full ? rawSize : requestedLimit, rawSize, 'file_missing')
    }
    const filePath = resolveBlobPath(this.payloadBlobDirectory, storageKey)
    let bytes: Buffer
    try {
      bytes = await readFile(filePath)
    } catch (error) {
      if (isFileNotFound(error)) return emptyBlobWindow(full ? 0 : requestedOffset, full ? rawSize : requestedLimit, rawSize, 'file_missing')
      throw error
    }
    if (compressedSize > 0 && bytes.byteLength !== compressedSize) {
      throw new Error(`F3 审计 payload blob 文件尺寸与 metadata 不一致：id=${blobId}, expected=${compressedSize}, actual=${bytes.byteLength}`)
    }
    const compression = optionalString(row.compression) ?? 'none'
    if (compression === 'gzip') {
      const { gunzip } = await import('node:zlib')
      bytes = await new Promise<Buffer>((resolvePromise, reject) => gunzip(bytes, (error, value) => error ? reject(error) : resolvePromise(value)))
    } else if (compression !== 'none') {
      throw new Error(`F3 审计 payload 使用未知压缩方式：${compression}`)
    }
    if (rawSize > 0 && bytes.byteLength !== rawSize) {
      throw new Error(`F3 审计 payload 解压后尺寸与 metadata 不一致：id=${blobId}, expected=${rawSize}, actual=${bytes.byteLength}`)
    }
    const offset = full ? 0 : requestedOffset
    const limit = full ? rawSize : requestedLimit
    const windowBytes = bytes.subarray(offset, Math.min(bytes.byteLength, offset + limit))
    const nextOffset = offset + windowBytes.byteLength
    const truncated = nextOffset < rawSize
    return {
      bytes: windowBytes,
      offset,
      limit,
      totalBytes: rawSize,
      nextOffset: !full && truncated && windowBytes.byteLength > 0 ? nextOffset : undefined,
      truncated,
      storageStatus: 'available'
    }
  }
}

function selectF3QuerySource(options: AuditLogF3QueryOptions): { mode: 'sqlite'; path: string; schema: string } | { mode: 'postgres'; url: string; schema: string } {
  const sqlitePath = options.sqlitePath?.trim()
  const postgresUrl = options.postgresUrl?.trim()
  if (Boolean(sqlitePath) === Boolean(postgresUrl)) {
    throw new Error('F3 审计查询必须且只能配置 sqlitePath 或 postgresUrl')
  }
  const schema = options.postgresSchema?.trim() || 'juhe_dataset'
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(schema)) {
    throw new Error(`F3 PostgreSQL schema 名称非法：${schema}`)
  }
  return sqlitePath
    ? { mode: 'sqlite', path: sqlitePath, schema: '' }
    : { mode: 'postgres', url: postgresUrl as string, schema }
}

async function createSqliteBackend(path: string): Promise<QueryBackend> {
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => DatabaseSync
  const database = new Constructor(resolve(path), { readOnly: true })
  database.exec('PRAGMA query_only = ON')
  const row = database.prepare('PRAGMA query_only').get()
  if (Number(row?.query_only ?? row?.[0] ?? 0) !== 1) {
    database.close()
    throw new Error('F3 SQLite 查询库未启用 PRAGMA query_only=1')
  }
  return {
    mode: 'sqlite',
    async query<T extends AuditLogRow = AuditLogRow>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      return database.prepare(sql).all(...params as SQLInputValue[]) as T[]
    },
    async one<T extends AuditLogRow = AuditLogRow>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      return database.prepare(sql).get(...params as SQLInputValue[]) as T | undefined
    },
    async close(): Promise<void> { database.close() }
  }
}

async function createPostgresBackend(url: string, schema: string, poolMax = 4): Promise<QueryBackend> {
  const { Pool } = await import('pg')
  const pool = new Pool({ connectionString: url, max: Math.max(1, Math.min(32, Math.trunc(poolMax))) })
  return {
    mode: 'postgres',
    async query<T extends AuditLogRow = AuditLogRow>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const connection = await pool.connect()
      let inTransaction = false
      try {
        await connection.query('BEGIN READ ONLY')
        inTransaction = true
        const result = await connection.query(convertQuestionPlaceholdersToPostgres(sql), params as any[]) as { rows: Array<Record<string, unknown>> }
        await connection.query('COMMIT')
        inTransaction = false
        return result.rows.map((row) => normalizeF3Row(row)) as T[]
      } catch (error) {
        if (inTransaction) await connection.query('ROLLBACK').catch(() => undefined)
        throw error
      } finally {
        connection.release()
      }
    },
    async one<T extends AuditLogRow = AuditLogRow>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      const connection = await pool.connect()
      let inTransaction = false
      try {
        await connection.query('BEGIN READ ONLY')
        inTransaction = true
        const result = await connection.query(convertQuestionPlaceholdersToPostgres(sql), params as any[]) as { rows: Array<Record<string, unknown>> }
        await connection.query('COMMIT')
        inTransaction = false
        const row = result.rows[0]
        return row ? normalizeF3Row(row) as T : undefined
      } catch (error) {
        if (inTransaction) await connection.query('ROLLBACK').catch(() => undefined)
        throw error
      } finally {
        connection.release()
      }
    },
    async close(): Promise<void> {
      await pool.end()
    }
  }
}

async function assertF3Schema(backend: QueryBackend, schema: string): Promise<void> {
  if (backend.mode === 'sqlite') {
    const rows = await backend.query<{ name?: unknown }>(
      `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN (${f3RequiredTables.map(() => '?').join(', ')})`,
      f3RequiredTables
    )
    const found = new Set(rows.map((row) => String(row.name ?? '')))
    const missing = f3RequiredTables.filter((table) => !found.has(table))
    if (missing.length > 0) throw new AuditLogF3SchemaError('sqlite', missing)
    return
  }
  const rows = await backend.query<{ table_name?: unknown }>(
    `SELECT table_name
     FROM information_schema.tables
     WHERE table_schema = ?
       AND table_name IN (${f3RequiredTables.map(() => '?').join(', ')})`,
    [schema, ...f3RequiredTables]
  )
  const found = new Set(rows.map((row) => String(row.table_name ?? '')))
  const missing = f3RequiredTables.filter((table) => !found.has(table))
  if (missing.length > 0) throw new AuditLogF3SchemaError('postgres', missing)
}

function buildF3AuditLogFilters(options: AuditLogListOptions, mode: AuditLogF3QueryMode): { clause: string; params: Array<string | number> } {
  const clauses: string[] = [f3PersistedTrafficClause('al')]
  const params: Array<string | number> = [...persistedAuditTrafficSourceParams()]
  pushPrefixFilter(clauses, params, 'al.trace_id', options.traceId, mode)
  pushExactFilter(clauses, params, 'al.session_id', options.sessionId)
  pushExactFilter(clauses, params, 'al.session_client_type', options.sessionClientType)
  pushPathFilter(clauses, params, 'al.path', options.path)
  pushExactFilter(clauses, params, 'al.model', options.model)
  pushPrefixFilter(clauses, params, 'al.client_ip', options.clientIp, mode)
  if (options.outcome && options.outcome !== 'all') {
    clauses.push('al.audit_outcome = ?')
    params.push(options.outcome)
  }
  if (typeof options.statusCode === 'number' && Number.isInteger(options.statusCode) && options.statusCode >= 100 && options.statusCode <= 599) {
    clauses.push('al.final_status_code = ?')
    params.push(options.statusCode)
  }
  if (options.trafficSource) {
    clauses.push('al.traffic_source = ?')
    params.push(options.trafficSource)
  }
  const startAt = options.startAt?.trim()
  if (startAt) {
    clauses.push('al.created_at >= ?')
    params.push(startAt)
  }
  const endAt = options.endAt?.trim()
  if (endAt) {
    clauses.push('al.created_at <= ?')
    params.push(endAt)
  }
  for (const [column, value] of [
    ['al.system_account_id', options.systemAccountId],
    ['al.api_key_id', options.apiKeyId],
    ['al.group_id', options.groupId],
    ['al.account_id', options.accountId],
    ['al.error_group_id', options.errorGroupId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }
  return { clause: `WHERE ${clauses.join(' AND ')}`, params }
}

function buildF3ErrorGroupFilters(options: AuditErrorGroupListOptions, _mode: AuditLogF3QueryMode): { clause: string; params: Array<string | number> } {
  const clauses: string[] = []
  const params: Array<string | number> = []
  pushPathFilter(clauses, params, 'aeg.path', options.path)
  pushExactFilter(clauses, params, 'aeg.model', options.model)
  if (typeof options.statusCode === 'number' && Number.isInteger(options.statusCode) && options.statusCode >= 100 && options.statusCode <= 599) {
    clauses.push('aeg.status_code = ?')
    params.push(options.statusCode)
  }
  for (const [column, value] of [
    ['aeg.system_account_id', options.systemAccountId],
    ['aeg.api_key_id', options.apiKeyId],
    ['aeg.group_id', options.groupId],
    ['aeg.account_id', options.accountId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }
  return { clause: clauses.length > 0 ? `WHERE ${clauses.join(' AND ')}` : '', params }
}

function f3PersistedTrafficClause(alias: string): string {
  return `${alias}.traffic_source NOT IN (${nonPersistedAuditTrafficSources.map(() => '?').join(', ')})`
}

function pushExactFilter(clauses: string[], params: Array<string | number>, column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPrefixFilter(clauses: string[], params: Array<string | number>, column: string, value: string | undefined, mode: AuditLogF3QueryMode): void {
  const text = value?.trim()
  if (!text) return
  const expression = mode === 'postgres' ? `${column} COLLATE "C"` : column
  clauses.push(`${expression} >= ? AND ${expression} < ?`)
  params.push(text, textPrefixUpperBound(text))
}

function pushPathFilter(clauses: string[], params: Array<string | number>, column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  const path = text.replace(/^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+/i, '').split('?')[0]?.trim()
  if (!path) return
  clauses.push(`${column} = ?`)
  params.push(path)
}

function normalizeF3Row<T extends AuditLogRow>(row: T): T {
  let normalized: AuditLogRow | undefined
  for (const [key, value] of Object.entries(row)) {
    if (value instanceof Date && Number.isFinite(value.getTime())) {
      normalized ??= { ...row }
      normalized[key] = value.toISOString()
      continue
    }
    if (typeof value === 'boolean' && f3BooleanColumns.has(key)) {
      normalized ??= { ...row }
      normalized[key] = value ? 1 : 0
    }
  }
  return (normalized ?? row) as T
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}

function normalizePayloadOffset(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function normalizePayloadLimit(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.min(f3MaxPayloadReadLimit, Math.max(1, Math.trunc(value)))
    : f3DefaultPayloadReadLimit
}

function finiteNonNegativeNumber(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : 0
}

function emptyBlobWindow(offset: number, limit: number, totalBytes: number, storageStatus: AuditPayloadBlobStorageStatus): AuditPayloadBlobWindow {
  return { offset, limit, totalBytes, truncated: false, storageStatus }
}

function resolveBlobPath(root: string, storageKey: string): string {
  const normalizedRoot = resolve(root)
  const target = resolve(normalizedRoot, storageKey)
  if (target !== normalizedRoot && !target.startsWith(`${normalizedRoot}${sep}`)) {
    throw new Error('F3 审计 payload storage_key 越出专用 blob 目录')
  }
  return target
}

function isFileNotFound(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && String((error as { code?: unknown }).code) === 'ENOENT'
}

interface F3HotSearchFileList {
  directoryMissing: boolean
  paths: string[]
  truncated: boolean
}

interface F3HotSearchFileScanOptions {
  filePath: string
  keywords: readonly string[]
  startMs: number
  endMs: number
  maxBytes: number
  maxLines: number
  seen: Map<string, number>
}

interface F3HotSearchFileScanResult {
  bytesRead: number
  linesRead: number
  truncated: boolean
}

async function listF3HotSearchFiles(directoryPath: string, startMs: number, endMs: number): Promise<F3HotSearchFileList> {
  const { opendir } = await import('node:fs/promises')
  let directory: Awaited<ReturnType<typeof opendir>>
  try {
    directory = await opendir(directoryPath)
  } catch (error) {
    if (isFileNotFound(error)) return { directoryMissing: true, paths: [], truncated: false }
    throw error
  }

  const candidates: Array<{ name: string; bucket: number }> = []
  let scannedDirectoryEntries = 0
  let directoryTruncated = false
  for await (const entry of directory) {
    scannedDirectoryEntries += 1
    if (scannedDirectoryEntries > f3HotSearchMaxDirectoryEntries) {
      directoryTruncated = true
      break
    }
    if (!f3HotSearchFileNamePattern.test(entry.name)) continue
    const bucket = parseF3HotSearchBucket(entry.name)
    if (bucket === undefined || bucket + 3_600_000 < startMs || bucket > endMs) continue
    candidates.push({ name: entry.name, bucket })
  }
  candidates.sort((left, right) => right.bucket - left.bucket || right.name.localeCompare(left.name))
  const fileTruncated = directoryTruncated || candidates.length > f3HotSearchMaxFiles
  return {
    directoryMissing: false,
    paths: candidates.slice(0, f3HotSearchMaxFiles).map((candidate) => join(directoryPath, candidate.name)),
    truncated: fileTruncated
  }
}

function parseF3HotSearchBucket(name: string): number | undefined {
  const bucket = Date.parse(`${name.slice(10, 14)}-${name.slice(14, 16)}-${name.slice(16, 18)}T${name.slice(18, 20)}:00:00.000Z`)
  return Number.isFinite(bucket) ? bucket : undefined
}

async function scanF3HotSearchFile(options: F3HotSearchFileScanOptions): Promise<F3HotSearchFileScanResult> {
  const { open } = await import('node:fs/promises')
  const file = await open(options.filePath, 'r')
  const buffer = Buffer.allocUnsafe(f3HotSearchReadChunkBytes)
  const decoder = new StringDecoder('utf8')
  let bytesRead = 0
  let linesRead = 0
  let truncated = false
  let reachedEndOfFile = false
  let pendingLine = ''
  let pendingLineBytes = 0
  let discardingLongLine = false

  const processPendingLine = (): void => {
    linesRead += 1
    if (discardingLongLine) {
      truncated = true
    } else {
      collectF3HotSearchMatch(pendingLine.endsWith('\r') ? pendingLine.slice(0, -1) : pendingLine, options)
    }
    pendingLine = ''
    pendingLineBytes = 0
    discardingLongLine = false
  }

  const appendLineSegment = (segment: string): void => {
    if (discardingLongLine) return
    pendingLineBytes += Buffer.byteLength(segment)
    if (pendingLineBytes > f3HotSearchMaxLineBytes) {
      pendingLine = ''
      discardingLongLine = true
      truncated = true
      return
    }
    pendingLine += segment
  }

  const consumeText = (text: string): boolean => {
    let offset = 0
    while (offset < text.length) {
      const newline = text.indexOf('\n', offset)
      const segmentEnd = newline === -1 ? text.length : newline
      appendLineSegment(text.slice(offset, segmentEnd))
      if (newline === -1) return false
      processPendingLine()
      if (linesRead >= options.maxLines) {
        truncated = true
        return true
      }
      offset = newline + 1
    }
    return false
  }

  try {
    while (bytesRead < options.maxBytes && linesRead < options.maxLines) {
      const remainingBytes = options.maxBytes - bytesRead
      const read = await file.read(buffer, 0, Math.min(buffer.length, remainingBytes), null)
      if (read.bytesRead === 0) {
        reachedEndOfFile = true
        break
      }
      bytesRead += read.bytesRead
      if (consumeText(decoder.write(buffer.subarray(0, read.bytesRead)))) break
      if (bytesRead >= options.maxBytes) {
        truncated = true
        break
      }
    }
    if (!reachedEndOfFile) return { bytesRead, linesRead, truncated: true }
    if (consumeText(decoder.end())) return { bytesRead, linesRead, truncated: true }
    if (pendingLine.length > 0 || discardingLongLine) {
      if (linesRead >= options.maxLines) return { bytesRead, linesRead, truncated: true }
      processPendingLine()
    }
    return { bytesRead, linesRead, truncated }
  } finally {
    await file.close()
  }
}

function collectF3HotSearchMatch(line: string, options: F3HotSearchFileScanOptions): void {
  try {
    const row = JSON.parse(line) as { auditLogId?: string; createdAt?: string; text?: string }
    const when = Date.parse(row.createdAt ?? '')
    if (!row.auditLogId || !Number.isFinite(when) || when < options.startMs || when > options.endMs) return
    if (!options.keywords.some((keyword) => (row.text ?? '').toLowerCase().includes(keyword))) return
    const previous = options.seen.get(row.auditLogId)
    if (previous === undefined || when > previous) options.seen.set(row.auditLogId, when)
  } catch { /* malformed lines are ignored */ }
}

function normalizeHotKeywords(values: string[]): string[] {
  const seen = new Set<string>()
  const result: string[] = []
  for (const value of values) {
    const keyword = value.trim().toLowerCase()
    if (keyword.length < 2 || seen.has(keyword)) continue
    seen.add(keyword)
    result.push(keyword)
    if (result.length >= 10) break
  }
  return result
}

function normalizeHotLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return 100
  return Math.min(100, Math.max(1, Math.trunc(value as number)))
}

function clampHotTime(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.min(value, fallback) : fallback
}

function shouldIncludeAuditPayloadHeaders(options: AuditLogPayloadReadOptions): boolean {
  if (options.includeHeaders !== undefined) return options.includeHeaders
  if (options.full) return true
  const offset = typeof options.offset === 'number' ? options.offset : Number(options.offset ?? 0)
  return !Number.isFinite(offset) || offset <= 0
}
