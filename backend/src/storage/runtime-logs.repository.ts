import { getDatabase, newId, nowIso } from './database.js'
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
  startedAt?: string
  endedAt?: string
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

type RuntimeLogRow = Record<string, unknown>
type RuntimeLogFilterValue = string | number

const runtimeLogDefaultPageSize = 100
const runtimeLogMaxPageSize = 100
export const runtimeLogIndexRetentionDays = 3

export function createRuntimeLogsBatch(inputs: RuntimeLogIndexInput[]): void {
  if (inputs.length === 0) return

  const database = getDatabase()
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

  database.exec('BEGIN')
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
    database.exec('COMMIT')
  } catch (error) {
    try {
      database.exec('ROLLBACK')
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
  const database = getDatabase()

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
  const database = getDatabase()
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
      ORDER BY MAX(time) DESC
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
  const database = getDatabase()
  const rows = database
    .prepare('SELECT id FROM runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?')
    .all(cutoffIso, Math.max(1, Math.trunc(limit))) as RuntimeLogRow[]
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  database.exec('BEGIN')
  try {
    database.prepare(`DELETE FROM runtime_log_search WHERE log_id IN (${placeholders})`).run(...ids)
    const result = database.prepare(`DELETE FROM runtime_logs WHERE id IN (${placeholders})`).run(...ids)
    database.exec('COMMIT')
    return Number(result.changes ?? 0)
  } catch (error) {
    try {
      database.exec('ROLLBACK')
    } catch {
    }
    throw error
  }
}

function buildRuntimeLogFilters(options: RuntimeLogListOptions): { clause: string; params: RuntimeLogFilterValue[] } {
  const clauses: string[] = ['rl.time >= ?']
  const params: RuntimeLogFilterValue[] = [normalizeStartedAt(options.startedAt)]
  const endedAt = normalizeIsoDateTime(options.endedAt)

  if (endedAt) {
    clauses.push('rl.time <= ?')
    params.push(endedAt)
  }

  pushExactTextFilter(clauses, params, 'rl.trace_id', options.traceId)
  pushExactTextFilter(clauses, params, 'rl.event', options.event)

  const level = options.level?.trim().toLowerCase()
  if (level && level !== 'all') {
    clauses.push('rl.level = ?')
    params.push(level)
  }

  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function pushExactTextFilter(clauses: string[], params: RuntimeLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function normalizeStartedAt(value?: string): string {
  const parsed = normalizeIsoDateTime(value)
  const cutoff = retentionCutoffIso()
  if (!parsed) return cutoff
  return parsed > cutoff ? parsed : cutoff
}

function normalizeIsoDateTime(value?: string): string | undefined {
  const text = value?.trim()
  if (!text) return undefined
  const timestamp = Date.parse(text)
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : undefined
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

function normalizeLevel(value: string): string {
  const text = value.trim().toLowerCase()
  return text || 'info'
}

function retentionCutoffIso(): string {
  return new Date(Date.now() - runtimeLogIndexRetentionDays * 24 * 60 * 60 * 1000).toISOString()
}
