import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
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
  rawJson: string
  createdAt: string
}

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

const runtimeLogDefaultPageSize = 100
const runtimeLogMaxPageSize = 100
export const runtimeLogIndexRetentionDays = 3

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
    for (const input of inputs) {
      const id = input.id ?? newId('rtlog')
      const createdAt = input.createdAt ?? nowIso()
      const result = insertLog.run(
        id,
        input.logFile ?? null,
        integerOrNull(input.logOffset),
        integerOrNull(input.lineNumber),
        input.time,
        normalizeLevel(input.level),
        input.traceId ?? null,
        input.event ?? null,
        input.message ?? null,
        input.errorMessage ?? null,
        input.rawJson,
        createdAt
      )
      if (Number(result.changes ?? 0) === 0) {
        continue
      }
      insertSearch.run(
        id,
        input.traceId ?? '',
        input.event ?? '',
        input.message ?? '',
        input.errorMessage ?? '',
        input.rawJson
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

export function listRuntimeLogs(options: RuntimeLogListOptions = {}): RuntimeLogListResult {
  const filters = buildRuntimeLogFilters(options)
  const pageSize = normalizeRuntimeLogPageSize(options.pageSize ?? options.limit)
  const page = normalizeRuntimeLogPage(options.page)
  const offset = (page - 1) * pageSize
  const keywordFilter = buildRuntimeLogKeywordFilter(options.keyword)
  const database = getRecordDatabase()

  const totalRow = keywordFilter
    ? database
      .prepare(`
        SELECT COUNT(*) AS total
        FROM runtime_log_search
        INNER JOIN runtime_logs rl ON rl.id = runtime_log_search.log_id
        ${filters.clause}
        AND ${keywordFilter.clause}
      `)
      .get(...filters.params, ...keywordFilter.params) as RuntimeLogRow | undefined
    : database
      .prepare(`
        SELECT COUNT(*) AS total
        FROM runtime_logs rl
        ${filters.clause}
      `)
      .get(...filters.params) as RuntimeLogRow | undefined

  const rows = keywordFilter
    ? database
      .prepare(`
        SELECT rl.*
        FROM runtime_log_search
        INNER JOIN runtime_logs rl ON rl.id = runtime_log_search.log_id
        ${filters.clause}
        AND ${keywordFilter.clause}
        ORDER BY rl.time DESC, rl.id DESC
        LIMIT ? OFFSET ?
      `)
      .all(...filters.params, ...keywordFilter.params, pageSize, offset) as RuntimeLogRow[]
    : database
      .prepare(`
        SELECT rl.*
        FROM runtime_logs rl
        ${filters.clause}
        ORDER BY rl.time DESC, rl.id DESC
        LIMIT ? OFFSET ?
      `)
      .all(...filters.params, pageSize, offset) as RuntimeLogRow[]

  return {
    items: rows.map(runtimeLogFromRow),
    total: Number(totalRow?.total ?? 0),
    page,
    pageSize
  }
}

export function getRuntimeLogFacets(): RuntimeLogFacets {
  const cutoff = retentionCutoffIso()
  const database = getRecordDatabase()
  const range = database
    .prepare(`
      SELECT MIN(time) AS earliest_indexed_at, MAX(time) AS latest_indexed_at, COUNT(*) AS total_indexed
      FROM runtime_logs
      WHERE time >= ?
    `)
    .get(cutoff) as RuntimeLogRow | undefined
  const levels = database
    .prepare(`
      SELECT level AS value, COUNT(*) AS count
      FROM runtime_logs
      WHERE time >= ?
      GROUP BY level
      ORDER BY count DESC, level ASC
    `)
    .all(cutoff) as RuntimeLogRow[]
  const events = database
    .prepare(`
      SELECT event
      FROM runtime_logs
      WHERE time >= ? AND event IS NOT NULL AND event <> ''
      GROUP BY event
      ORDER BY MAX(time) DESC, event ASC
      LIMIT 80
    `)
    .all(cutoff) as RuntimeLogRow[]
  return {
    retentionDays: runtimeLogIndexRetentionDays,
    earliestIndexedAt: optionalString(range?.earliest_indexed_at),
    latestIndexedAt: optionalString(range?.latest_indexed_at),
    totalIndexed: Number(range?.total_indexed ?? 0),
    levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })),
    events: events.map((row) => String(row.event))
  }
}

export function cleanupRuntimeLogIndex(cutoffIso = retentionCutoffIso(), limit = 10000): number {
  const database = getRecordDatabase()
  const rows = database
    .prepare('SELECT id FROM runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?')
    .all(cutoffIso, Math.max(1, Math.trunc(limit))) as RuntimeLogRow[]
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare(`DELETE FROM runtime_log_search WHERE log_id IN (${placeholders})`).run(...ids)
    const result = database.prepare(`DELETE FROM runtime_logs WHERE id IN (${placeholders})`).run(...ids)
    commitDatabaseTransaction(database, transactionStarted)
    return Number(result.changes ?? 0)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
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

  pushExactTextFilter(clauses, params, 'rl.trace_id', options.traceId)
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

function pushExactTextFilter(clauses: string[], params: RuntimeLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function buildRuntimeLogKeywordFilter(value?: string): { clause: string; params: string[] } | undefined {
  const text = value?.trim()
  if (!text) return undefined
  const terms = splitKeywordTerms(text)
  if (!terms.length) return undefined
  if (terms.every((term) => [...term].length >= 3)) {
    return {
      clause: 'runtime_log_search MATCH ?',
      params: [terms.map(quoteFts5Term).join(' AND ')]
    }
  }
  return {
    clause: terms.map(() => 'runtime_log_search.raw_json LIKE ?').join(' AND '),
    params: terms.map((term) => `%${term}%`)
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
    rawJson: String(row.raw_json),
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

function normalizeLevel(value: string): string {
  const text = value.trim().toLowerCase()
  return text || 'info'
}

function retentionCutoffIso(): string {
  return new Date(Date.now() - runtimeLogIndexRetentionDays * 24 * 60 * 60 * 1000).toISOString()
}
