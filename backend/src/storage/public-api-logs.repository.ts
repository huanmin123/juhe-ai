import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getDatasetDatabase,
  newId,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalString, optionalServerDateTimeIso } from './value-utils.js'

export type PublicApiLogCaptureStatus = 'complete' | 'truncated' | 'empty' | 'dropped'
export type PublicApiLogResultFilter = 'success' | 'failed' | 'all'

export interface PublicApiLogInput {
  id?: string
  traceId?: string
  sourceRefId?: string
  sourceName?: string
  tokenId?: string
  tokenName?: string
  tokenPrefix?: string
  isTestToken?: boolean
  method: string
  path: string
  queryString?: string
  clientIp?: string
  userAgent?: string
  statusCode?: number
  success?: boolean
  durationMs?: number
  requestSizeBytes?: number
  responseSizeBytes?: number
  requestCaptureStatus?: PublicApiLogCaptureStatus
  responseCaptureStatus?: PublicApiLogCaptureStatus
  requestData?: Record<string, unknown>
  responseData?: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt: string
  createdAt?: string
}

export interface PublicApiLogSummary {
  id: string
  traceId?: string
  sourceRefId?: string
  sourceName?: string
  tokenId?: string
  tokenName?: string
  tokenPrefix?: string
  isTestToken: boolean
  method: string
  path: string
  queryString?: string
  clientIp?: string
  userAgent?: string
  statusCode?: number
  success: boolean
  durationMs?: number
  requestSizeBytes: number
  responseSizeBytes: number
  requestCaptureStatus: PublicApiLogCaptureStatus
  responseCaptureStatus: PublicApiLogCaptureStatus
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt: string
  createdAt: string
}

export interface PublicApiLogDetail extends PublicApiLogSummary {
  requestData: Record<string, unknown>
  responseData: Record<string, unknown>
}

export interface PublicApiLogListOptions {
  page?: number
  pageSize?: number
  traceId?: string
  sourceRefId?: string
  path?: string
  result?: PublicApiLogResultFilter
  statusCode?: number
  clientIp?: string
  startAt?: string
  endAt?: string
}

export interface PublicApiLogListResult {
  items: PublicApiLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

type PublicApiLogRow = Record<string, unknown>
type PublicApiLogFilterValue = string | number

const publicApiLogDefaultPageSize = 100
const publicApiLogMaxPageSize = 100
const publicApiLogMaxListWindowRows = 1001
const publicApiLogPostgresRowsPerInsert = 1000

export function createPublicApiLog(input: PublicApiLogInput): PublicApiLogSummary {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式下公开接口日志写入必须使用 createPublicApiLogAsync')
  }
  return createPublicApiLogsBatch([input])[0]
}

export async function createPublicApiLogAsync(input: PublicApiLogInput): Promise<PublicApiLogSummary> {
  const [summary] = await createPublicApiLogsBatchAsync([input])
  return summary
}

export function createPublicApiLogsBatch(inputs: PublicApiLogInput[]): PublicApiLogSummary[] {
  if (inputs.length === 0) return []
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式下公开接口日志批量写入必须使用 createPublicApiLogsBatchAsync')
  }
  const database = getDatasetDatabase()
  const insert = database.prepare(`
    INSERT INTO public_api_logs (
      id, trace_id, source_ref_id, source_name, token_id, token_name, token_prefix, is_test_token,
      method, path, query_string, client_ip, user_agent, status_code, success, duration_ms,
      request_size_bytes, response_size_bytes, request_capture_status, response_capture_status,
      request_data_json, response_data_json, error_code, error_message, started_at, ended_at, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const normalizedLogs = inputs.map(normalizePublicApiLogInput)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const log of normalizedLogs) {
      insert.run(
        log.id,
        log.traceId ?? null,
        log.sourceRefId ?? null,
        log.sourceName ?? null,
        log.tokenId ?? null,
        log.tokenName ?? null,
        log.tokenPrefix ?? null,
        log.isTestToken ? 1 : 0,
        log.method,
        log.path,
        log.queryString ?? null,
        log.clientIp ?? null,
        log.userAgent ?? null,
        log.statusCode,
        log.success,
        log.durationMs,
        log.requestSizeBytes,
        log.responseSizeBytes,
        log.requestCaptureStatus,
        log.responseCaptureStatus,
        log.requestDataJson,
        log.responseDataJson,
        log.errorCode ?? null,
        log.errorMessage ?? null,
        log.startedAt,
        log.endedAt,
        log.createdAt
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return normalizedLogs.map(publicApiLogSummaryFromNormalizedInput)
}

export async function createPublicApiLogsBatchAsync(inputs: PublicApiLogInput[]): Promise<PublicApiLogSummary[]> {
  if (inputs.length === 0) return []
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createPublicApiLogsBatch(inputs)
  }
  const normalizedLogs = inputs.map(normalizePublicApiLogInput)
  await createPublicApiLogsBatchPostgres(normalizedLogs)
  return normalizedLogs.map(publicApiLogSummaryFromNormalizedInput)
}

async function createPublicApiLogsBatchPostgres(normalizedLogs: NormalizedPublicApiLogInput[]): Promise<void> {
  if (normalizedLogs.length === 0) return
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    for (const chunk of chunkValues(normalizedLogs, publicApiLogPostgresRowsPerInsert)) {
      await insertPublicApiLogsPostgres(tx, chunk)
    }
  })
}

async function insertPublicApiLogsPostgres(client: DatabaseClient, logs: NormalizedPublicApiLogInput[]): Promise<void> {
  if (logs.length === 0) return
  await client.execute(`
    INSERT INTO juhe_dataset.public_api_logs (
      id, trace_id, source_ref_id, source_name, token_id, token_name, token_prefix, is_test_token,
      method, path, query_string, client_ip, user_agent, status_code, success, duration_ms,
      request_size_bytes, response_size_bytes, request_capture_status, response_capture_status,
      request_data_json, response_data_json, error_code, error_message, started_at, ended_at, created_at
    ) VALUES ${multiRowPlaceholders(logs.length, 27)}
    ON CONFLICT(id) DO NOTHING
  `, logs.flatMap((log) => [
    log.id,
    log.traceId ?? null,
    log.sourceRefId ?? null,
    log.sourceName ?? null,
    log.tokenId ?? null,
    log.tokenName ?? null,
    log.tokenPrefix ?? null,
    log.isTestToken ? 1 : 0,
    log.method,
    log.path,
    log.queryString ?? null,
    log.clientIp ?? null,
    log.userAgent ?? null,
    log.statusCode,
    log.success,
    log.durationMs,
    log.requestSizeBytes,
    log.responseSizeBytes,
    log.requestCaptureStatus,
    log.responseCaptureStatus,
    log.requestDataJson,
    log.responseDataJson,
    log.errorCode ?? null,
    log.errorMessage ?? null,
    log.startedAt,
    log.endedAt,
    log.createdAt
  ]))
}

interface NormalizedPublicApiLogInput {
  id: string
  traceId?: string
  sourceRefId?: string
  sourceName?: string
  tokenId?: string
  tokenName?: string
  tokenPrefix?: string
  isTestToken: boolean
  method: string
  path: string
  queryString?: string
  clientIp?: string
  userAgent?: string
  statusCode: number | null
  success: 0 | 1
  durationMs: number | null
  requestSizeBytes: number
  responseSizeBytes: number
  requestCaptureStatus: PublicApiLogCaptureStatus
  responseCaptureStatus: PublicApiLogCaptureStatus
  requestDataJson: string
  responseDataJson: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt: string
  createdAt: string
}

function normalizePublicApiLogInput(input: PublicApiLogInput): NormalizedPublicApiLogInput {
  const id = input.id ?? newId('publog')
  const createdAt = input.createdAt ?? nowIso()
  const statusCode = integerOrNull(input.statusCode)
  const durationMs = integerOrNull(input.durationMs)
  const requestSizeBytes = normalizeNonNegativeInteger(input.requestSizeBytes)
  const responseSizeBytes = normalizeNonNegativeInteger(input.responseSizeBytes)
  const requestCaptureStatus = normalizeCaptureStatus(input.requestCaptureStatus)
  const responseCaptureStatus = normalizeCaptureStatus(input.responseCaptureStatus)
  return {
    id,
    traceId: input.traceId,
    sourceRefId: input.sourceRefId,
    sourceName: input.sourceName,
    tokenId: input.tokenId,
    tokenName: input.tokenName,
    tokenPrefix: input.tokenPrefix,
    isTestToken: input.isTestToken === true,
    method: input.method,
    path: input.path,
    queryString: input.queryString,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    statusCode,
    success: input.success ? 1 : 0,
    durationMs,
    requestSizeBytes,
    responseSizeBytes,
    requestCaptureStatus,
    responseCaptureStatus,
    requestDataJson: safeJsonObjectStringify(input.requestData),
    responseDataJson: safeJsonObjectStringify(input.responseData),
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    startedAt: input.startedAt,
    endedAt: input.endedAt,
    createdAt
  }
}

function publicApiLogSummaryFromNormalizedInput(input: NormalizedPublicApiLogInput): PublicApiLogSummary {
  return publicApiLogSummaryFromRow({
    id: input.id,
    trace_id: input.traceId,
    source_ref_id: input.sourceRefId,
    source_name: input.sourceName,
    token_id: input.tokenId,
    token_name: input.tokenName,
    token_prefix: input.tokenPrefix,
    is_test_token: input.isTestToken ? 1 : 0,
    method: input.method,
    path: input.path,
    query_string: input.queryString,
    client_ip: input.clientIp,
    user_agent: input.userAgent,
    status_code: input.statusCode,
    success: input.success,
    duration_ms: input.durationMs,
    request_size_bytes: input.requestSizeBytes,
    response_size_bytes: input.responseSizeBytes,
    request_capture_status: input.requestCaptureStatus,
    response_capture_status: input.responseCaptureStatus,
    error_code: input.errorCode,
    error_message: input.errorMessage,
    started_at: input.startedAt,
    ended_at: input.endedAt,
    created_at: input.createdAt
  })
}

export function listPublicApiLogs(options: PublicApiLogListOptions = {}): PublicApiLogListResult {
  const pageSize = normalizePublicApiLogPageSize(options.pageSize)
  const page = normalizeListPage(options.page, pageSize, publicApiLogMaxListWindowRows)
  const offset = (page - 1) * pageSize
  const filters = buildPublicApiLogFilters(options)
  const whereClause = filters.clauses.length ? `WHERE ${filters.clauses.join(' AND ')}` : ''
  const rows = getDatasetDatabase().prepare(`
    SELECT ${publicApiLogSummarySelectColumns('pal')}
    FROM public_api_logs pal
    ${whereClause}
    ORDER BY pal.created_at DESC, pal.id DESC
    LIMIT ? OFFSET ?
  `).all(...filters.params, pageSize + 1, offset) as PublicApiLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map(publicApiLogSummaryFromRow)
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function listPublicApiLogsAsync(options: PublicApiLogListOptions = {}): Promise<PublicApiLogListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_public_api_logs_read_only',
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listPublicApiLogs(options)
  }
  const pageSize = normalizePublicApiLogPageSize(options.pageSize)
  const page = normalizeListPage(options.page, pageSize, publicApiLogMaxListWindowRows)
  const offset = (page - 1) * pageSize
  const filters = buildPublicApiLogFilters(options)
  const whereClause = filters.clauses.length ? `WHERE ${filters.clauses.join(' AND ')}` : ''
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<PublicApiLogRow>(`
    SELECT ${publicApiLogSummarySelectColumns('pal')}
    FROM juhe_dataset.public_api_logs pal
    ${whereClause}
    ORDER BY pal.created_at DESC, pal.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, pageSize + 1, offset])
  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map(publicApiLogSummaryFromRow)
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function getPublicApiLogDetail(id: string): PublicApiLogDetail | undefined {
  const row = getDatasetDatabase()
    .prepare('SELECT * FROM public_api_logs WHERE id = ?')
    .get(id) as PublicApiLogRow | undefined
  return row ? publicApiLogDetailFromRow(row) : undefined
}

export async function getPublicApiLogDetailAsync(id: string): Promise<PublicApiLogDetail | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_public_api_log_detail_read_only',
      id
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getPublicApiLogDetail(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<PublicApiLogRow>(
    'SELECT * FROM juhe_dataset.public_api_logs WHERE id = ?',
    [id]
  )
  return row ? publicApiLogDetailFromRow(row) : undefined
}

export function cleanupPublicApiLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  const rows = getDatasetDatabase()
    .prepare('SELECT id FROM public_api_logs WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?')
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as PublicApiLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0
  const result = getDatasetDatabase()
    .prepare(`DELETE FROM public_api_logs WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .run(...ids)
  return Number(result.changes ?? 0)
}

function buildPublicApiLogFilters(options: PublicApiLogListOptions): { clauses: string[]; params: PublicApiLogFilterValue[] } {
  const clauses: string[] = []
  const params: PublicApiLogFilterValue[] = []
  pushPrefixFilter(clauses, params, 'pal.trace_id', options.traceId)
  pushExactFilter(clauses, params, 'pal.source_ref_id', options.sourceRefId)
  pushExactFilter(clauses, params, 'pal.path', options.path)
  pushPrefixFilter(clauses, params, 'pal.client_ip', options.clientIp)
  if (options.result === 'success') {
    clauses.push('pal.success = 1')
  } else if (options.result === 'failed') {
    clauses.push('pal.success = 0')
  }
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('pal.status_code = ?')
    params.push(options.statusCode)
  }
  const startAt = normalizeDateTimeFilter(options.startAt)
  if (startAt) {
    clauses.push('pal.created_at >= ?')
    params.push(startAt)
  }
  const endAt = normalizeDateTimeFilter(options.endAt)
  if (endAt) {
    clauses.push('pal.created_at <= ?')
    params.push(endAt)
  }
  return { clauses, params }
}

function publicApiLogSummarySelectColumns(alias: string): string {
  return [
    'id',
    'trace_id',
    'source_ref_id',
    'source_name',
    'token_id',
    'token_name',
    'token_prefix',
    'is_test_token',
    'method',
    'path',
    'query_string',
    'client_ip',
    'user_agent',
    'status_code',
    'success',
    'duration_ms',
    'request_size_bytes',
    'response_size_bytes',
    'request_capture_status',
    'response_capture_status',
    "'{}' AS request_data_json",
    "'{}' AS response_data_json",
    'error_code',
    'error_message',
    'started_at',
    'ended_at',
    'created_at'
  ].map((column) => column.includes(' AS ') ? column : `${alias}.${column}`).join(', ')
}

function publicApiLogSummaryFromRow(row: PublicApiLogRow): PublicApiLogSummary {
  return {
    id: String(row.id),
    traceId: optionalString(row.trace_id),
    sourceRefId: optionalString(row.source_ref_id),
    sourceName: optionalString(row.source_name),
    tokenId: optionalString(row.token_id),
    tokenName: optionalString(row.token_name),
    tokenPrefix: optionalString(row.token_prefix),
    isTestToken: Number(row.is_test_token ?? 0) === 1,
    method: String(row.method),
    path: String(row.path),
    queryString: optionalString(row.query_string),
    clientIp: optionalString(row.client_ip),
    userAgent: optionalString(row.user_agent),
    statusCode: numberValue(row.status_code),
    success: Number(row.success ?? 0) === 1,
    durationMs: numberValue(row.duration_ms),
    requestSizeBytes: nonNegativeNumberValue(row.request_size_bytes),
    responseSizeBytes: nonNegativeNumberValue(row.response_size_bytes),
    requestCaptureStatus: normalizeCaptureStatus(row.request_capture_status),
    responseCaptureStatus: normalizeCaptureStatus(row.response_capture_status),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    startedAt: String(row.started_at),
    endedAt: String(row.ended_at),
    createdAt: String(row.created_at)
  }
}

function publicApiLogDetailFromRow(row: PublicApiLogRow): PublicApiLogDetail {
  return {
    ...publicApiLogSummaryFromRow(row),
    requestData: parseJsonObject(row.request_data_json),
    responseData: parseJsonObject(row.response_data_json)
  }
}

function normalizePublicApiLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(publicApiLogMaxPageSize, Math.max(1, value))
    : publicApiLogDefaultPageSize
}

function pushExactFilter(clauses: string[], params: PublicApiLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text || text === 'all') return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPrefixFilter(clauses: string[], params: PublicApiLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} >= ? AND ${column} < ?`)
  params.push(text, prefixUpperBound(text))
}

function normalizeCaptureStatus(value: unknown): PublicApiLogCaptureStatus {
  if (value === 'complete' || value === 'truncated' || value === 'empty' || value === 'dropped') {
    return value
  }
  return 'empty'
}

function safeJsonObjectStringify(value: unknown): string {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return '{}'
  }
  try {
    return JSON.stringify(value) ?? '{}'
  } catch {
    return '{}'
  }
}

function parseJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string' || !value.trim()) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function normalizeDateTimeFilter(value: unknown): string | undefined {
  return optionalServerDateTimeIso(value)
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function integerOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : null
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function nonNegativeNumberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function normalizeNonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function multiRowPlaceholders(rowCount: number, columnCount: number): string {
  const row = `(${Array.from({ length: columnCount }, () => '?').join(', ')})`
  return Array.from({ length: rowCount }, () => row).join(', ')
}

function prefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\uffff`
}
