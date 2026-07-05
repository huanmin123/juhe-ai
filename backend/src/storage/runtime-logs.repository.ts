import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows, textPrefixUpperBound } from './query-utils.js'
import { getSettings, getSettingsAsync } from './settings.repository.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalString } from './value-utils.js'

export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface RuntimeLogIndexInput {
  id?: string
  logFile?: string
  logOffset?: number
  lineNumber?: number
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  createdAt?: string
}

export interface RuntimeLogListOptions {
  page?: number
  pageSize?: number
  traceId?: string
  level?: RuntimeLogLevel | 'all'
  event?: string
  keyword?: string
  startAt?: string
  endAt?: string
}

export interface RuntimeLogListResult {
  items: RuntimeLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface RuntimeLogSummary {
  id: string
  time: string
  level: string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson?: string
  createdAt: string
}

export type RuntimeLogDetail = RuntimeLogSummary & { rawJson: string }

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
}

export interface RuntimeLogFileCursor {
  logFile: string
  fileIdentity?: string
  cursorOffset: number
  lineNumber: number
  fileSize: number
  fileMtimeMs?: number
  lastReadAt?: string
  lastErrorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface RuntimeLogFileCursorInput {
  logFile: string
  fileIdentity?: string
  cursorOffset: number
  lineNumber: number
  fileSize: number
  fileMtimeMs?: number
  lastReadAt?: string
  lastErrorMessage?: string
}

type RuntimeLogRow = Record<string, unknown>
type RuntimeLogFilterValue = string | number
type RuntimeLogFacetInput = { time: string; level: string; event?: string }
interface NormalizedRuntimeLogIndexInput {
  id: string
  logFile?: string
  logOffset: number | null
  lineNumber: number | null
  time: string
  level: string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  createdAt: string
}

const runtimeLogDefaultPageSize = 100
const runtimeLogMaxPageSize = 100
const runtimeLogMaxListWindowRows = 1001
export const runtimeLogIndexRetentionDays = 14
export const runtimeLogIndexRetentionMaxDays = 90
const runtimeLogKeywordDefaultWindowHours = 6
const runtimeLogFacetBucketKey = 'current'
const runtimeLogFacetMaxEvents = 80
const runtimeLogMaxRawJsonChars = 128 * 1024

export function createRuntimeLogsBatch(inputs: RuntimeLogIndexInput[]): void {
  if (inputs.length === 0) return

  const database = getDatasetDatabase()
  const insertLog = database.prepare(`
    INSERT OR IGNORE INTO runtime_logs (
      id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)

  const normalizedInputs = inputs.map(normalizeRuntimeLogIndexInput)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const facetRows: RuntimeLogFacetInput[] = []
    for (const input of normalizedInputs) {
      const result = insertLog.run(
        input.id,
        input.logFile ?? null,
        input.logOffset,
        input.lineNumber,
        input.time,
        input.level,
        input.traceId ?? null,
        input.event ?? null,
        input.message ?? null,
        input.errorMessage ?? null,
        input.rawJson,
        input.createdAt
      )
      if (Number(result.changes ?? 0) === 0) {
        continue
      }
      facetRows.push({ time: input.time, level: input.level, event: input.event })
    }
    incrementRuntimeLogFacetSnapshots(database, facetRows)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export function listRuntimeLogs(options: RuntimeLogListOptions = {}): RuntimeLogListResult {
  return listRuntimeLogsReadOnly(options)
}

export function listRuntimeLogsReadOnly(options: RuntimeLogListOptions = {}): RuntimeLogListResult {
  const filters = buildRuntimeLogFilters(options)
  const pageSize = normalizeRuntimeLogPageSize(options.pageSize)
  const page = normalizeRuntimeLogPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()

  const rows = database
    .prepare(`
      SELECT ${runtimeLogListSelectColumns('rl')}
      FROM runtime_logs rl
      ${filters.clause}
      ORDER BY rl.time DESC, rl.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as RuntimeLogRow[]

  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map((row) => runtimeLogFromRow(row, { includeRawJson: false }))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function createRuntimeLogsBatchAsync(inputs: RuntimeLogIndexInput[]): Promise<void> {
  if (inputs.length === 0) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    createRuntimeLogsBatch(inputs)
    return
  }

  const normalizedInputs = inputs.map(normalizeRuntimeLogIndexInput)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    const facetRows: RuntimeLogFacetInput[] = []
    for (const input of normalizedInputs) {
      const result = await insertRuntimeLogPostgres(tx, input)
      if (result.changes === 0) {
        continue
      }
      facetRows.push({ time: input.time, level: input.level, event: input.event })
    }
    await incrementRuntimeLogFacetSnapshotsPostgres(tx, facetRows)
  })
}

async function insertRuntimeLogPostgres(client: DatabaseClient, input: NormalizedRuntimeLogIndexInput): Promise<{ changes: number }> {
  return await client.execute(`
    INSERT INTO juhe_dataset.runtime_logs (
      id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `, [
    input.id,
    input.logFile ?? null,
    input.logOffset,
    input.lineNumber,
    input.time,
    input.level,
    input.traceId ?? null,
    input.event ?? null,
    input.message ?? null,
    input.errorMessage ?? null,
    input.rawJson,
    input.createdAt
  ])
}

export async function listRuntimeLogsAsync(options: RuntimeLogListOptions = {}): Promise<RuntimeLogListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_runtime_logs_read_only',
        options
      })
    }
    return listRuntimeLogsReadOnly(options)
  }
  const filters = buildRuntimeLogFilters(options)
  const pageSize = normalizeRuntimeLogPageSize(options.pageSize)
  const page = normalizeRuntimeLogPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const client = createPostgresDatabaseClient(await getPostgresPool())

  const rows = await client.query<RuntimeLogRow>(`
    SELECT ${runtimeLogListSelectColumns('rl')}
    FROM juhe_dataset.runtime_logs rl
    ${filters.clause}
    ORDER BY rl.time DESC, rl.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, pageSize + 1, offset])

  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map((row) => runtimeLogFromRow(row, { includeRawJson: false }))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function getRuntimeLogDetail(id: string): RuntimeLogDetail | undefined {
  return getRuntimeLogDetailReadOnly(id)
}

export function getRuntimeLogDetailReadOnly(id: string): RuntimeLogDetail | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT ${runtimeLogDetailSelectColumns('rl')}
      FROM runtime_logs rl
      WHERE rl.id = ?
      LIMIT 1
    `)
    .get(id.trim()) as RuntimeLogRow | undefined
  return row ? runtimeLogDetailFromRow(row) : undefined
}

export async function getRuntimeLogDetailAsync(id: string): Promise<RuntimeLogDetail | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'get_runtime_log_detail_read_only',
        id
      })
    }
    return getRuntimeLogDetailReadOnly(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<RuntimeLogRow>(`
    SELECT ${runtimeLogDetailSelectColumns('rl')}
    FROM juhe_dataset.runtime_logs rl
    WHERE rl.id = ?
    LIMIT 1
  `, [id.trim()])
  return row ? runtimeLogDetailFromRow(row) : undefined
}

export function getRuntimeLogFacets(): RuntimeLogFacets {
  return getRuntimeLogFacetsReadOnly()
}

export function getRuntimeLogFacetsReadOnly(): RuntimeLogFacets {
  const database = getDatasetDatabase()
  const range = database
    .prepare('SELECT earliest_time, latest_time, total_count FROM runtime_log_facet_summary WHERE bucket_key = ?')
    .get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  const levels = database
    .prepare(`
      SELECT level AS value, count
      FROM runtime_log_level_facets
      WHERE bucket_key = ? AND count > 0
      ORDER BY count DESC, level ASC
    `)
    .all(runtimeLogFacetBucketKey) as RuntimeLogRow[]
  const events = database
    .prepare(`
      SELECT event
      FROM runtime_log_event_facets
      WHERE bucket_key = ? AND count > 0
      ORDER BY latest_time DESC, event ASC
      LIMIT ?
    `)
    .all(runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents) as RuntimeLogRow[]
  return {
    retentionDays: currentRuntimeLogIndexRetentionDays(),
    earliestIndexedAt: optionalString(range?.earliest_time),
    latestIndexedAt: optionalString(range?.latest_time),
    totalIndexed: Number(range?.total_count ?? 0),
    levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })),
    events: events.map((row) => String(row.event))
  }
}

export async function getRuntimeLogFacetsAsync(): Promise<RuntimeLogFacets> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'get_runtime_log_facets_read_only'
      })
    }
    return getRuntimeLogFacetsReadOnly()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const [range, levels, events] = await Promise.all([
    client.one<RuntimeLogRow>(
      'SELECT earliest_time, latest_time, total_count FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ?',
      [runtimeLogFacetBucketKey]
    ),
    client.query<RuntimeLogRow>(`
      SELECT level AS value, count
      FROM juhe_dataset.runtime_log_level_facets
      WHERE bucket_key = ? AND count > 0
      ORDER BY count DESC, level ASC
    `, [runtimeLogFacetBucketKey]),
    client.query<RuntimeLogRow>(`
      SELECT event
      FROM juhe_dataset.runtime_log_event_facets
      WHERE bucket_key = ? AND count > 0
      ORDER BY latest_time DESC, event ASC
      LIMIT ?
    `, [runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents])
  ])
  const retentionDays = await currentRuntimeLogIndexRetentionDaysAsync()
  return {
    retentionDays,
    earliestIndexedAt: optionalString(range?.earliest_time),
    latestIndexedAt: optionalString(range?.latest_time),
    totalIndexed: Number(range?.total_count ?? 0),
    levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })),
    events: events.map((row) => String(row.event))
  }
}

export function ensureRuntimeLogFacetSnapshots(cutoffIso = retentionCutoffIso(currentRuntimeLogIndexRetentionDays())): void {
  const database = getDatasetDatabase()
  const summary = database
    .prepare('SELECT bucket_key FROM runtime_log_facet_summary WHERE bucket_key = ? LIMIT 1')
    .get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  if (summary?.bucket_key) return

  const hasIndexedLogs = database
    .prepare('SELECT id FROM runtime_logs WHERE time >= ? LIMIT 1')
    .get(cutoffIso) as RuntimeLogRow | undefined
  if (!hasIndexedLogs?.id) return
}

export function cleanupRuntimeLogIndex(cutoffIso = retentionCutoffIso(currentRuntimeLogIndexRetentionDays()), limit = 10000): number {
  const database = getDatasetDatabase()
  const rows = database
    .prepare('SELECT id, time, level, event FROM runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?')
    .all(cutoffIso, Math.max(1, Math.trunc(limit))) as RuntimeLogRow[]
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let deleted = 0
    for (const chunk of chunkValues(ids, 900)) {
      const placeholders = sqlPlaceholders(chunk.length)
      const result = database.prepare(`DELETE FROM runtime_logs WHERE id IN (${placeholders})`).run(...chunk)
      deleted += Number(result.changes ?? 0)
    }
    if (deleted > 0) {
      decrementRuntimeLogFacetSnapshots(database, rows.map((row) => ({
        time: String(row.time),
        level: String(row.level),
        event: optionalString(row.event)
      })), cutoffIso)
    }
    commitDatabaseTransaction(database, transactionStarted)
    return deleted
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export async function cleanupRuntimeLogIndexAsync(cutoffIso?: string, limit = 10000): Promise<number> {
  const effectiveCutoffIso = cutoffIso ?? retentionCutoffIso(await currentRuntimeLogIndexRetentionDaysAsync())
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupRuntimeLogIndex(effectiveCutoffIso, limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<RuntimeLogRow>(`
    SELECT id, time, level, event
    FROM juhe_dataset.runtime_logs
    WHERE time < ?
    ORDER BY time ASC, id ASC
    LIMIT ?
  `, [effectiveCutoffIso, Math.max(1, Math.trunc(limit))])
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0

  let deleted = 0
  await client.transaction(async (tx) => {
    for (const chunk of chunkValues(ids, 10000)) {
      deleted += Number((await tx.execute('DELETE FROM juhe_dataset.runtime_logs WHERE id = ANY(?::text[])', [chunk])).changes ?? 0)
    }
    if (deleted > 0) {
      await decrementRuntimeLogFacetSnapshotsAsync(tx, rows.map((row) => ({
        time: String(row.time),
        level: String(row.level),
        event: optionalString(row.event)
      })), effectiveCutoffIso)
    }
  })
  return deleted
}

export function cleanupRuntimeLogFileCursorsBefore(cutoffIso: string, limit = 10000): number {
  const database = getDatasetDatabase()
  const result = database.prepare(`
    DELETE FROM runtime_log_file_cursors
    WHERE rowid IN (
      SELECT rowid
      FROM runtime_log_file_cursors
      WHERE updated_at < ?
      ORDER BY updated_at ASC, rowid ASC
      LIMIT ?
    )
  `).run(cutoffIso, Math.max(1, Math.trunc(limit)))
  return Number(result.changes ?? 0)
}

export async function cleanupRuntimeLogFileCursorsBeforeAsync(cutoffIso: string, limit = 10000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupRuntimeLogFileCursorsBefore(cutoffIso, limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    DELETE FROM juhe_dataset.runtime_log_file_cursors
    WHERE ctid IN (
      SELECT ctid
      FROM juhe_dataset.runtime_log_file_cursors
      WHERE updated_at < ?
      ORDER BY updated_at ASC, ctid ASC
      LIMIT ?
    )
  `, [cutoffIso, Math.max(1, Math.trunc(limit))])
  return Number(result.changes ?? 0)
}

export function getRuntimeLogFileCursor(logFile: string): RuntimeLogFileCursor | undefined {
  const row = getDatasetDatabase()
    .prepare('SELECT * FROM runtime_log_file_cursors WHERE log_file = ?')
    .get(logFile) as RuntimeLogRow | undefined
  return row ? runtimeLogFileCursorFromRow(row) : undefined
}

export function upsertRuntimeLogFileCursor(input: RuntimeLogFileCursorInput): void {
  const now = nowIso()
  getDatasetDatabase()
    .prepare(`
      INSERT INTO runtime_log_file_cursors (
        log_file, file_identity, cursor_offset, line_number, file_size, file_mtime_ms,
        last_read_at, last_error_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(log_file) DO UPDATE SET
        file_identity = excluded.file_identity,
        cursor_offset = excluded.cursor_offset,
        line_number = excluded.line_number,
        file_size = excluded.file_size,
        file_mtime_ms = excluded.file_mtime_ms,
        last_read_at = excluded.last_read_at,
        last_error_message = excluded.last_error_message,
        updated_at = excluded.updated_at
    `)
    .run(
      input.logFile,
      input.fileIdentity ?? null,
      positiveInteger(input.cursorOffset),
      positiveInteger(input.lineNumber),
      positiveInteger(input.fileSize),
      integerOrNull(input.fileMtimeMs),
      input.lastReadAt ?? now,
      input.lastErrorMessage ?? null,
      now,
      now
    )
}

function buildRuntimeLogFilters(options: RuntimeLogListOptions): { clause: string; params: RuntimeLogFilterValue[] } {
  const clauses: string[] = []
  const params: RuntimeLogFilterValue[] = []

  pushPrefixTextFilter(clauses, params, 'rl.trace_id', options.traceId)
  pushExactTextFilter(clauses, params, 'rl.event', options.event)

  const level = options.level?.trim().toLowerCase()
  if (level && level !== 'all') {
    clauses.push('rl.level = ?')
    params.push(level)
  }

  const startAt = options.startAt?.trim()
  if (startAt) {
    clauses.push('rl.time >= ?')
    params.push(startAt)
  }

  const endAt = options.endAt?.trim()
  if (endAt) {
    clauses.push('rl.time <= ?')
    params.push(endAt)
  }

  if (options.keyword?.trim() && !startAt && !endAt) {
    clauses.push('rl.time >= ?')
    params.push(new Date(Date.now() - runtimeLogKeywordDefaultWindowHours * 60 * 60 * 1000).toISOString())
  }

  pushMessageKeywordFilter(clauses, params, options.keyword)

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function pushExactTextFilter(clauses: string[], params: RuntimeLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPrefixTextFilter(clauses: string[], params: RuntimeLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  const columnExpression = runtimeConfig.databaseDriver === 'postgres' ? `${column} COLLATE "C"` : column
  clauses.push(`${columnExpression} >= ? AND ${columnExpression} < ?`)
  params.push(text, textPrefixUpperBound(text))
}

function pushMessageKeywordFilter(clauses: string[], params: RuntimeLogFilterValue[], value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push("rl.message LIKE ? ESCAPE '\\'")
  params.push(`%${escapeSqlLikePattern(text)}%`)
}

function escapeSqlLikePattern(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function truncateRuntimeLogRawJson(value: string): string {
  if (value.length <= runtimeLogMaxRawJsonChars) return value
  return `${value.slice(0, runtimeLogMaxRawJsonChars)}...[truncated]`
}

function runtimeLogListSelectColumns(alias: string): string {
  return [
    'id',
    'time',
    'level',
    'trace_id',
    'event',
    'message',
    'error_message',
    'created_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

function runtimeLogDetailSelectColumns(alias: string): string {
  return `${runtimeLogListSelectColumns(alias)}, ${alias}.raw_json`
}

function normalizeRuntimeLogPage(value: unknown, pageSize: number): number {
  const maxPage = Math.max(1, Math.floor((runtimeLogMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(maxPage, Math.max(1, value))
    : 1
}

function normalizeRuntimeLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(runtimeLogMaxPageSize, Math.max(1, value))
    : runtimeLogDefaultPageSize
}

function integerOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : null
}

function positiveInteger(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function runtimeLogFromRow(row: RuntimeLogRow, options: { includeRawJson?: boolean } = { includeRawJson: true }): RuntimeLogSummary {
  return {
    id: String(row.id),
    time: String(row.time),
    level: String(row.level),
    traceId: optionalString(row.trace_id),
    event: optionalString(row.event),
    message: optionalString(row.message),
    errorMessage: optionalString(row.error_message),
    ...(options.includeRawJson === false ? {} : { rawJson: optionalString(row.raw_json) ?? '' }),
    createdAt: String(row.created_at)
  }
}

function runtimeLogDetailFromRow(row: RuntimeLogRow): RuntimeLogDetail {
  return runtimeLogFromRow(row) as RuntimeLogDetail
}

function runtimeLogFileCursorFromRow(row: RuntimeLogRow): RuntimeLogFileCursor {
  return {
    logFile: String(row.log_file),
    fileIdentity: optionalString(row.file_identity),
    cursorOffset: positiveInteger(row.cursor_offset),
    lineNumber: positiveInteger(row.line_number),
    fileSize: positiveInteger(row.file_size),
    fileMtimeMs: integerOrNull(row.file_mtime_ms) ?? undefined,
    lastReadAt: optionalString(row.last_read_at),
    lastErrorMessage: optionalString(row.last_error_message),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at)
  }
}

function normalizeRuntimeLogIndexInput(input: RuntimeLogIndexInput): NormalizedRuntimeLogIndexInput {
  return {
    id: input.id ?? newId('rtlog'),
    logFile: input.logFile,
    logOffset: integerOrNull(input.logOffset),
    lineNumber: integerOrNull(input.lineNumber),
    time: input.time,
    level: normalizeLevel(input.level),
    traceId: input.traceId,
    event: input.event,
    message: input.message,
    errorMessage: input.errorMessage,
    rawJson: truncateRuntimeLogRawJson(input.rawJson),
    createdAt: input.createdAt ?? nowIso()
  }
}

function incrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getDatasetDatabase>, rows: RuntimeLogFacetInput[]): void {
  const cutoff = retentionCutoffIso(currentRuntimeLogIndexRetentionDays())
  const retainedRows = rows.filter((row) => row.time >= cutoff)
  if (retainedRows.length === 0) return

  const timestamp = nowIso()
  const sortedTimes = retainedRows.map((row) => row.time).sort()
  const earliestTime = sortedTimes[0]
  const latestTime = sortedTimes[sortedTimes.length - 1]
  database
    .prepare(`
      INSERT INTO runtime_log_facet_summary (
        bucket_key, total_count, earliest_time, latest_time, updated_at
      ) VALUES (?, ?, ?, ?, ?)
      ON CONFLICT(bucket_key) DO UPDATE SET
        total_count = total_count + excluded.total_count,
        earliest_time = CASE
          WHEN runtime_log_facet_summary.earliest_time IS NULL THEN excluded.earliest_time
          WHEN excluded.earliest_time < runtime_log_facet_summary.earliest_time THEN excluded.earliest_time
          ELSE runtime_log_facet_summary.earliest_time
        END,
        latest_time = CASE
          WHEN runtime_log_facet_summary.latest_time IS NULL THEN excluded.latest_time
          WHEN excluded.latest_time > runtime_log_facet_summary.latest_time THEN excluded.latest_time
          ELSE runtime_log_facet_summary.latest_time
        END,
        updated_at = excluded.updated_at
    `)
    .run(runtimeLogFacetBucketKey, retainedRows.length, earliestTime, latestTime, timestamp)

  const levels = new Map<string, number>()
  const events = new Map<string, { count: number; latestTime: string }>()
  for (const row of retainedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      const current = events.get(event)
      events.set(event, {
        count: (current?.count ?? 0) + 1,
        latestTime: current && current.latestTime > row.time ? current.latestTime : row.time
      })
    }
  }

  const upsertLevel = database.prepare(`
    INSERT INTO runtime_log_level_facets (
      bucket_key, level, count, updated_at
    ) VALUES (?, ?, ?, ?)
    ON CONFLICT(bucket_key, level) DO UPDATE SET
      count = count + excluded.count,
      updated_at = excluded.updated_at
  `)
  for (const [level, count] of levels) {
    upsertLevel.run(runtimeLogFacetBucketKey, level, count, timestamp)
  }

  const upsertEvent = database.prepare(`
    INSERT INTO runtime_log_event_facets (
      bucket_key, event, count, latest_time, updated_at
    ) VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(bucket_key, event) DO UPDATE SET
      count = count + excluded.count,
      latest_time = CASE
        WHEN runtime_log_event_facets.latest_time IS NULL THEN excluded.latest_time
        WHEN excluded.latest_time > runtime_log_event_facets.latest_time THEN excluded.latest_time
        ELSE runtime_log_event_facets.latest_time
      END,
      updated_at = excluded.updated_at
  `)
  for (const [event, summary] of events) {
    upsertEvent.run(runtimeLogFacetBucketKey, event, summary.count, summary.latestTime, timestamp)
  }
}

async function incrementRuntimeLogFacetSnapshotsPostgres(client: DatabaseClient, rows: RuntimeLogFacetInput[]): Promise<void> {
  const cutoff = retentionCutoffIso(await currentRuntimeLogIndexRetentionDaysAsync())
  const retainedRows = rows.filter((row) => row.time >= cutoff)
  if (retainedRows.length === 0) return

  const timestamp = nowIso()
  const sortedTimes = retainedRows.map((row) => row.time).sort()
  const earliestTime = sortedTimes[0]
  const latestTime = sortedTimes[sortedTimes.length - 1]
  await client.execute(`
    INSERT INTO juhe_dataset.runtime_log_facet_summary (
      bucket_key, total_count, earliest_time, latest_time, updated_at
    ) VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(bucket_key) DO UPDATE SET
      total_count = runtime_log_facet_summary.total_count + excluded.total_count,
      earliest_time = CASE
        WHEN runtime_log_facet_summary.earliest_time IS NULL THEN excluded.earliest_time
        WHEN excluded.earliest_time < runtime_log_facet_summary.earliest_time THEN excluded.earliest_time
        ELSE runtime_log_facet_summary.earliest_time
      END,
      latest_time = CASE
        WHEN runtime_log_facet_summary.latest_time IS NULL THEN excluded.latest_time
        WHEN excluded.latest_time > runtime_log_facet_summary.latest_time THEN excluded.latest_time
        ELSE runtime_log_facet_summary.latest_time
      END,
      updated_at = excluded.updated_at
  `, [runtimeLogFacetBucketKey, retainedRows.length, earliestTime, latestTime, timestamp])

  const levels = new Map<string, number>()
  const events = new Map<string, { count: number; latestTime: string }>()
  for (const row of retainedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      const current = events.get(event)
      events.set(event, {
        count: (current?.count ?? 0) + 1,
        latestTime: current && current.latestTime > row.time ? current.latestTime : row.time
      })
    }
  }

  if (levels.size > 0) {
    const params: unknown[] = []
    const values = Array.from(levels.entries()).map(([level, count]) => {
      params.push(runtimeLogFacetBucketKey, level, count, timestamp)
      return '(?, ?, ?, ?)'
    })
    await client.execute(`
      INSERT INTO juhe_dataset.runtime_log_level_facets (
        bucket_key, level, count, updated_at
      ) VALUES ${values.join(', ')}
      ON CONFLICT(bucket_key, level) DO UPDATE SET
        count = runtime_log_level_facets.count + excluded.count,
        updated_at = excluded.updated_at
    `, params)
  }

  if (events.size > 0) {
    const params: unknown[] = []
    const values = Array.from(events.entries()).map(([event, summary]) => {
      params.push(runtimeLogFacetBucketKey, event, summary.count, summary.latestTime, timestamp)
      return '(?, ?, ?, ?, ?)'
    })
    await client.execute(`
      INSERT INTO juhe_dataset.runtime_log_event_facets (
        bucket_key, event, count, latest_time, updated_at
      ) VALUES ${values.join(', ')}
      ON CONFLICT(bucket_key, event) DO UPDATE SET
        count = runtime_log_event_facets.count + excluded.count,
        latest_time = CASE
          WHEN runtime_log_event_facets.latest_time IS NULL THEN excluded.latest_time
          WHEN excluded.latest_time > runtime_log_event_facets.latest_time THEN excluded.latest_time
          ELSE runtime_log_event_facets.latest_time
        END,
        updated_at = excluded.updated_at
    `, params)
  }
}

function decrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getDatasetDatabase>, rows: RuntimeLogFacetInput[], cutoffIso: string): void {
  if (rows.length === 0) return

  const timestamp = nowIso()
  const summary = database
    .prepare('SELECT earliest_time FROM runtime_log_facet_summary WHERE bucket_key = ?')
    .get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  const earliestCountedTime = optionalString(summary?.earliest_time)
  const countedRows = earliestCountedTime
    ? rows.filter((row) => row.time >= earliestCountedTime)
    : rows
  if (countedRows.length === 0) return

  const earliestRow = database
    .prepare('SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time ASC, id ASC LIMIT 1')
    .get(cutoffIso) as RuntimeLogRow | undefined
  const latestRow = database
    .prepare('SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time DESC, id DESC LIMIT 1')
    .get(cutoffIso) as RuntimeLogRow | undefined
  database.prepare(`
    UPDATE runtime_log_facet_summary
    SET total_count = MAX(0, total_count - ?),
        earliest_time = ?,
        latest_time = ?,
        updated_at = ?
    WHERE bucket_key = ?
  `).run(
    countedRows.length,
    optionalString(earliestRow?.time) ?? null,
    optionalString(latestRow?.time) ?? null,
    timestamp,
    runtimeLogFacetBucketKey
  )
  database.prepare('DELETE FROM runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0').run(runtimeLogFacetBucketKey)

  const levels = new Map<string, number>()
  const events = new Map<string, number>()
  for (const row of countedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      events.set(event, (events.get(event) ?? 0) + 1)
    }
  }

  const updateLevel = database.prepare(`
    UPDATE runtime_log_level_facets
    SET count = MAX(0, count - ?),
        updated_at = ?
    WHERE bucket_key = ? AND level = ?
  `)
  for (const [level, count] of levels) {
    updateLevel.run(count, timestamp, runtimeLogFacetBucketKey, level)
  }
  database.prepare('DELETE FROM runtime_log_level_facets WHERE bucket_key = ? AND count <= 0').run(runtimeLogFacetBucketKey)

  const updateEvent = database.prepare(`
    UPDATE runtime_log_event_facets
    SET count = MAX(0, count - ?),
        updated_at = ?
    WHERE bucket_key = ? AND event = ?
  `)
  for (const [event, count] of events) {
    updateEvent.run(count, timestamp, runtimeLogFacetBucketKey, event)
  }
  database.prepare('DELETE FROM runtime_log_event_facets WHERE bucket_key = ? AND count <= 0').run(runtimeLogFacetBucketKey)
}

async function decrementRuntimeLogFacetSnapshotsAsync(client: DatabaseClient, rows: RuntimeLogFacetInput[], cutoffIso: string): Promise<void> {
  if (rows.length === 0) return

  const timestamp = nowIso()
  const summary = await client.one<RuntimeLogRow>(
    'SELECT earliest_time FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ?',
    [runtimeLogFacetBucketKey]
  )
  const earliestCountedTime = optionalString(summary?.earliest_time)
  const countedRows = earliestCountedTime
    ? rows.filter((row) => row.time >= earliestCountedTime)
    : rows
  if (countedRows.length === 0) return

  const earliestRow = await client.one<RuntimeLogRow>(`
    SELECT time
    FROM juhe_dataset.runtime_logs
    WHERE time >= ?
    ORDER BY time ASC, id ASC
    LIMIT 1
  `, [cutoffIso])
  const latestRow = await client.one<RuntimeLogRow>(`
    SELECT time
    FROM juhe_dataset.runtime_logs
    WHERE time >= ?
    ORDER BY time DESC, id DESC
    LIMIT 1
  `, [cutoffIso])
  await client.execute(`
    UPDATE juhe_dataset.runtime_log_facet_summary
    SET total_count = GREATEST(0, total_count - ?),
        earliest_time = ?,
        latest_time = ?,
        updated_at = ?
    WHERE bucket_key = ?
  `, [
    countedRows.length,
    optionalString(earliestRow?.time) ?? null,
    optionalString(latestRow?.time) ?? null,
    timestamp,
    runtimeLogFacetBucketKey
  ])
  await client.execute('DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0', [runtimeLogFacetBucketKey])

  const levels = new Map<string, number>()
  const events = new Map<string, number>()
  for (const row of countedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      events.set(event, (events.get(event) ?? 0) + 1)
    }
  }

  for (const [level, count] of levels) {
    await client.execute(`
      UPDATE juhe_dataset.runtime_log_level_facets
      SET count = GREATEST(0, count - ?),
          updated_at = ?
      WHERE bucket_key = ? AND level = ?
    `, [count, timestamp, runtimeLogFacetBucketKey, level])
  }
  await client.execute('DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = ? AND count <= 0', [runtimeLogFacetBucketKey])

  for (const [event, count] of events) {
    await client.execute(`
      UPDATE juhe_dataset.runtime_log_event_facets
      SET count = GREATEST(0, count - ?),
          updated_at = ?
      WHERE bucket_key = ? AND event = ?
    `, [count, timestamp, runtimeLogFacetBucketKey, event])
  }
  await client.execute('DELETE FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = ? AND count <= 0', [runtimeLogFacetBucketKey])
}

function normalizeLevel(value: string): string {
  const text = value.trim().toLowerCase()
  return text || 'info'
}

export function runtimeLogIndexRetentionDaysFromSettings(settings: Record<string, unknown>): number {
  const value = settings.runtimeLogIndexRetentionDays
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    return runtimeLogIndexRetentionDays
  }
  return Math.min(Math.max(1, value), runtimeLogIndexRetentionMaxDays)
}

function currentRuntimeLogIndexRetentionDays(): number {
  try {
    return runtimeLogIndexRetentionDaysFromSettings(getSettings())
  } catch {
    return runtimeLogIndexRetentionDays
  }
}

async function currentRuntimeLogIndexRetentionDaysAsync(): Promise<number> {
  try {
    return runtimeLogIndexRetentionDaysFromSettings(await getSettingsAsync())
  } catch {
    return runtimeLogIndexRetentionDays
  }
}

function retentionCutoffIso(retentionDays = runtimeLogIndexRetentionDays): string {
  return new Date(Date.now() - retentionDays * 24 * 60 * 60 * 1000).toISOString()
}
