import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, compatiblePagedTotal, sqlPlaceholders, takePageRows } from './query-utils.js'
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
  limit?: number
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

export interface RuntimeLogSearchBackfillResult {
  processed: number
  hasMore: boolean
  cursorCreatedAt?: string
  cursorId?: string
}

export interface RuntimeLogSummary {
  id: string
  time: string
  level: string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  createdAt: string
}

export type RuntimeLogDetail = RuntimeLogSummary

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

const runtimeLogDefaultPageSize = 100
const runtimeLogMaxPageSize = 100
export const runtimeLogIndexRetentionDays = 3
const runtimeLogFacetBucketKey = 'current'
const runtimeLogFacetMaxEvents = 80
const runtimeLogMaxRawJsonChars = 128 * 1024
const runtimeLogMinKeywordLength = 3
const runtimeLogSearchBackfillJobName = 'runtime_log_search_backfill'
const runtimeLogSearchBackfillMaxBatchSize = 5000

export function createRuntimeLogsBatch(inputs: RuntimeLogIndexInput[]): void {
  if (inputs.length === 0) return

  const database = getRecordDatabase()
  const insertLog = database.prepare(`
    INSERT OR IGNORE INTO runtime_logs (
      id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertSearch = database.prepare(`
    INSERT INTO runtime_log_search (
      log_id, trace_id, event, message, error_message, raw_json
    ) VALUES (?, ?, ?, ?, ?, ?)
  `)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const facetRows: RuntimeLogFacetInput[] = []
    for (const input of inputs) {
      const id = input.id ?? newId('rtlog')
      const createdAt = input.createdAt ?? nowIso()
      const level = normalizeLevel(input.level)
      const rawJson = truncateRuntimeLogRawJson(input.rawJson)
      const result = insertLog.run(
        id,
        input.logFile ?? null,
        integerOrNull(input.logOffset),
        integerOrNull(input.lineNumber),
        input.time,
        level,
        input.traceId ?? null,
        input.event ?? null,
        input.message ?? null,
        input.errorMessage ?? null,
        rawJson,
        createdAt
      )
      if (Number(result.changes ?? 0) === 0) {
        continue
      }
      facetRows.push({ time: input.time, level, event: input.event })
      insertSearch.run(
        id,
        input.traceId ?? '',
        input.event ?? '',
        input.message ?? '',
        input.errorMessage ?? '',
        rawJson
      )
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
  const filters = buildRuntimeLogFilters(options)
  const pageSize = normalizeRuntimeLogPageSize(options.pageSize ?? options.limit)
  const page = normalizeRuntimeLogPage(options.page)
  const offset = (page - 1) * pageSize
  const keywordFilter = buildRuntimeLogKeywordFilter(options.keyword)
  const keywordWhereClause = keywordFilter
    ? buildRuntimeLogKeywordWhereClause(filters.clause, keywordFilter.clause)
    : ''
  const database = getRecordDatabase()

  const rows = keywordFilter
    ? database
      .prepare(`
        SELECT ${runtimeLogListSelectColumns('rl')}
        FROM runtime_log_search
        INNER JOIN runtime_logs rl ON rl.id = runtime_log_search.log_id
        ${keywordWhereClause}
        ORDER BY rl.time DESC, rl.id DESC
        LIMIT ? OFFSET ?
      `)
      .all(...filters.params, ...keywordFilter.params, pageSize + 1, offset) as RuntimeLogRow[]
    : database
      .prepare(`
        SELECT ${runtimeLogListSelectColumns('rl')}
        FROM runtime_logs rl
        ${filters.clause}
        ORDER BY rl.time DESC, rl.id DESC
        LIMIT ? OFFSET ?
      `)
      .all(...filters.params, pageSize + 1, offset) as RuntimeLogRow[]

  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map(runtimeLogFromRow)
  return {
    items,
    total: compatiblePagedTotal(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function getRuntimeLogDetail(id: string): RuntimeLogDetail | undefined {
  const row = getRecordDatabase()
    .prepare(`
      SELECT ${runtimeLogDetailSelectColumns('rl')}
      FROM runtime_logs rl
      WHERE rl.id = ?
      LIMIT 1
    `)
    .get(id.trim()) as RuntimeLogRow | undefined
  return row ? runtimeLogFromRow(row) : undefined
}

export function getRuntimeLogFacets(): RuntimeLogFacets {
  const database = getRecordDatabase()
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
    retentionDays: runtimeLogIndexRetentionDays,
    earliestIndexedAt: optionalString(range?.earliest_time),
    latestIndexedAt: optionalString(range?.latest_time),
    totalIndexed: Number(range?.total_count ?? 0),
    levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })),
    events: events.map((row) => String(row.event))
  }
}

export function refreshRuntimeLogFacetSnapshots(cutoffIso = retentionCutoffIso()): void {
  const database = getRecordDatabase()
  const timestamp = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM runtime_log_event_facets WHERE bucket_key = ?').run(runtimeLogFacetBucketKey)
    database.prepare('DELETE FROM runtime_log_level_facets WHERE bucket_key = ?').run(runtimeLogFacetBucketKey)
    database.prepare('DELETE FROM runtime_log_facet_summary WHERE bucket_key = ?').run(runtimeLogFacetBucketKey)

    const range = database
      .prepare(`
        SELECT COUNT(*) AS total_count, MIN(time) AS earliest_time, MAX(time) AS latest_time
        FROM runtime_logs
        WHERE time >= ?
      `)
      .get(cutoffIso) as RuntimeLogRow | undefined
    const totalCount = Number(range?.total_count ?? 0)
    if (totalCount > 0) {
      database
        .prepare(`
          INSERT INTO runtime_log_facet_summary (
            bucket_key, total_count, earliest_time, latest_time, updated_at
          ) VALUES (?, ?, ?, ?, ?)
        `)
        .run(
          runtimeLogFacetBucketKey,
          totalCount,
          optionalString(range?.earliest_time) ?? null,
          optionalString(range?.latest_time) ?? null,
          timestamp
        )
    }

    const levels = database
      .prepare(`
        SELECT level, COUNT(*) AS count
        FROM runtime_logs
        WHERE time >= ?
        GROUP BY level
      `)
      .all(cutoffIso) as RuntimeLogRow[]
    const insertLevel = database.prepare(`
      INSERT INTO runtime_log_level_facets (
        bucket_key, level, count, updated_at
      ) VALUES (?, ?, ?, ?)
    `)
    for (const row of levels) {
      insertLevel.run(runtimeLogFacetBucketKey, String(row.level), Number(row.count ?? 0), timestamp)
    }

    const events = database
      .prepare(`
        SELECT event, COUNT(*) AS count, MAX(time) AS latest_time
        FROM runtime_logs
        WHERE time >= ? AND event IS NOT NULL AND event <> ''
        GROUP BY event
      `)
      .all(cutoffIso) as RuntimeLogRow[]
    const insertEvent = database.prepare(`
      INSERT INTO runtime_log_event_facets (
        bucket_key, event, count, latest_time, updated_at
      ) VALUES (?, ?, ?, ?, ?)
    `)
    for (const row of events) {
      insertEvent.run(
        runtimeLogFacetBucketKey,
        String(row.event),
        Number(row.count ?? 0),
        optionalString(row.latest_time) ?? null,
        timestamp
      )
    }

    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export function ensureRuntimeLogFacetSnapshots(cutoffIso = retentionCutoffIso()): void {
  const database = getRecordDatabase()
  const summary = database
    .prepare('SELECT bucket_key FROM runtime_log_facet_summary WHERE bucket_key = ? LIMIT 1')
    .get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  if (summary?.bucket_key) return

  const hasIndexedLogs = database
    .prepare('SELECT id FROM runtime_logs WHERE time >= ? LIMIT 1')
    .get(cutoffIso) as RuntimeLogRow | undefined
  if (!hasIndexedLogs?.id) return

  refreshRuntimeLogFacetSnapshots(cutoffIso)
}

export function cleanupRuntimeLogIndex(cutoffIso = retentionCutoffIso(), limit = 10000): number {
  const database = getRecordDatabase()
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
      deleteRuntimeLogSearchByIds(database, chunk)
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

export function backfillRuntimeLogSearchIndex(limit = 1000): RuntimeLogSearchBackfillResult {
  const database = getRecordDatabase()
  const batchLimit = positiveRuntimeLogSearchBackfillLimit(limit)
  const state = runtimeLogSearchBackfillState(database)
  const rows = database
    .prepare(`
      SELECT id, trace_id, event, message, error_message, raw_json, created_at
      FROM runtime_logs
      WHERE created_at > ? OR (created_at = ? AND id > ?)
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, batchLimit + 1) as RuntimeLogRow[]
  const batchRows = rows.slice(0, batchLimit)
  const hasMore = rows.length > batchLimit
  if (batchRows.length === 0) {
    updateRuntimeLogSearchBackfillState(database, {
      cursorCreatedAt: state.cursorCreatedAt || undefined,
      cursorId: state.cursorId || undefined,
      lastSuccessAt: nowIso(),
      lagSeconds: 0
    })
    return {
      processed: 0,
      hasMore: false,
      cursorCreatedAt: state.cursorCreatedAt || undefined,
      cursorId: state.cursorId || undefined
    }
  }

  const ids = batchRows.map((row) => String(row.id)).filter(Boolean)
  const insertSearch = database.prepare(`
    INSERT INTO runtime_log_search (
      log_id, trace_id, event, message, error_message, raw_json
    ) VALUES (?, ?, ?, ?, ?, ?)
  `)
  const updatedAt = nowIso()
  const last = batchRows[batchRows.length - 1]
  const cursorCreatedAt = String(last.created_at)
  const cursorId = String(last.id)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    deleteRuntimeLogSearchByIds(database, ids)
    for (const row of batchRows) {
      insertSearch.run(
        String(row.id),
        optionalString(row.trace_id) ?? '',
        optionalString(row.event) ?? '',
        optionalString(row.message) ?? '',
        optionalString(row.error_message) ?? '',
        truncateRuntimeLogRawJson(optionalString(row.raw_json) ?? '')
      )
    }
    updateRuntimeLogSearchBackfillState(database, {
      cursorCreatedAt,
      cursorId,
      lastSuccessAt: updatedAt,
      lagSeconds: hasMore ? searchBackfillLagSeconds(cursorCreatedAt) : 0
    })
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    markRuntimeLogSearchBackfillFailed(database, error)
    throw error
  }

  return {
    processed: batchRows.length,
    hasMore,
    cursorCreatedAt,
    cursorId
  }
}

export function getRuntimeLogFileCursor(logFile: string): RuntimeLogFileCursor | undefined {
  const row = getRecordDatabase()
    .prepare('SELECT * FROM runtime_log_file_cursors WHERE log_file = ?')
    .get(logFile) as RuntimeLogRow | undefined
  return row ? runtimeLogFileCursorFromRow(row) : undefined
}

export function upsertRuntimeLogFileCursor(input: RuntimeLogFileCursorInput): void {
  const now = nowIso()
  getRecordDatabase()
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

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function buildRuntimeLogKeywordWhereClause(filterClause: string, keywordClause: string): string {
  return filterClause
    ? `${filterClause} AND ${keywordClause}`
    : `WHERE ${keywordClause}`
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
  clauses.push(`${column} >= ? AND ${column} < ?`)
  params.push(text, `${text}\uffff`)
}

function buildRuntimeLogKeywordFilter(value?: string): { clause: string; params: string[] } | undefined {
  const text = value?.trim()
  if (!text) return undefined
  const terms = splitKeywordTerms(text)
  if (!terms.length) return undefined
  if (terms.some((term) => [...term].length < runtimeLogMinKeywordLength)) {
    return {
      clause: '0 = 1',
      params: []
    }
  }
  return {
    clause: 'runtime_log_search MATCH ?',
    params: [terms.map(quoteFts5Term).join(' AND ')]
  }
}

function splitKeywordTerms(value: string): string[] {
  const seen = new Set<string>()
  const terms: string[] = []
  for (const part of value.split(/[\s,;，；]+/)) {
    const term = part.trim()
    if (!term) continue
    const key = term.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    terms.push(term)
  }
  return terms
}

function quoteFts5Term(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

function deleteRuntimeLogSearchByIds(database: ReturnType<typeof getRecordDatabase>, ids: string[]): void {
  for (const chunk of chunkValues(ids, 900)) {
    if (chunk.length === 0) continue
    database.prepare(`DELETE FROM runtime_log_search WHERE log_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
  }
}

function runtimeLogSearchBackfillState(database: ReturnType<typeof getRecordDatabase>): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(runtimeLogSearchBackfillJobName) as RuntimeLogRow | undefined
  return {
    cursorCreatedAt: optionalString(row?.cursor_created_at) ?? '',
    cursorId: optionalString(row?.cursor_id) ?? ''
  }
}

function updateRuntimeLogSearchBackfillState(
  database: ReturnType<typeof getRecordDatabase>,
  input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }
): void {
  const updatedAt = nowIso()
  database.prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
    ) VALUES ('global', '', ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(
    runtimeLogSearchBackfillJobName,
    input.cursorCreatedAt ?? null,
    input.cursorId ?? null,
    input.lastSuccessAt ?? null,
    input.lastErrorMessage ?? null,
    input.lagSeconds ?? null,
    updatedAt
  )
}

function markRuntimeLogSearchBackfillFailed(database: ReturnType<typeof getRecordDatabase>, error: unknown): void {
  updateRuntimeLogSearchBackfillState(database, {
    lastErrorMessage: error instanceof Error ? error.message : '运行日志搜索索引回填失败'
  })
}

function positiveRuntimeLogSearchBackfillLimit(value: number): number {
  return Number.isFinite(value)
    ? Math.min(runtimeLogSearchBackfillMaxBatchSize, Math.max(1, Math.trunc(value)))
    : 1000
}

function searchBackfillLagSeconds(cursorTime: string): number {
  const cursorMs = Date.parse(cursorTime)
  return Number.isFinite(cursorMs) ? Math.max(0, Math.floor((Date.now() - cursorMs) / 1000)) : 0
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

function normalizeRuntimeLogPage(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.max(1, value)
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

function runtimeLogFromRow(row: RuntimeLogRow): RuntimeLogSummary {
  return {
    id: String(row.id),
    time: String(row.time),
    level: String(row.level),
    traceId: optionalString(row.trace_id),
    event: optionalString(row.event),
    message: optionalString(row.message),
    errorMessage: optionalString(row.error_message),
    rawJson: optionalString(row.raw_json) ?? '',
    createdAt: String(row.created_at)
  }
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

function incrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getRecordDatabase>, rows: RuntimeLogFacetInput[]): void {
  const cutoff = retentionCutoffIso()
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

function decrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getRecordDatabase>, rows: RuntimeLogFacetInput[], cutoffIso: string): void {
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

  const range = database
    .prepare('SELECT MIN(time) AS earliest_time, MAX(time) AS latest_time FROM runtime_logs WHERE time >= ?')
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
    optionalString(range?.earliest_time) ?? null,
    optionalString(range?.latest_time) ?? null,
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

function normalizeLevel(value: string): string {
  const text = value.trim().toLowerCase()
  return text || 'info'
}

function retentionCutoffIso(): string {
  return new Date(Date.now() - runtimeLogIndexRetentionDays * 24 * 60 * 60 * 1000).toISOString()
}
