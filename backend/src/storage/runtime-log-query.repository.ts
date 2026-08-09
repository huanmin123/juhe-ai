import { runtimeConfig } from '../config/runtime.js'
import { getRuntimeLogDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, takePageRows, textPrefixUpperBound } from './query-utils.js'
import { getSettings, getSettingsAsync } from './settings.repository.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalString } from './value-utils.js'

export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

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
  items: RuntimeLogListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface RuntimeLogListItem {
  id: string
  time: string
  level: string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
}

export interface RuntimeLogSummary extends RuntimeLogListItem {
  rawJson?: string
  createdAt: string
}

export type RuntimeLogDetail = RuntimeLogSummary & { rawJson: string }

export interface RuntimeLogDetailDelta {
  id: string
  rawJson: string
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
const runtimeLogMaxListWindowRows = 1001
const runtimeLogKeywordDefaultWindowHours = 6
const runtimeLogIndexRetentionDays = 14
const runtimeLogIndexRetentionMaxDays = 90
const runtimeLogFacetBucketKey = 'current'
const runtimeLogFacetMaxEvents = 80

export function getRuntimeLogFacets(): RuntimeLogFacets {
  return getRuntimeLogFacetsReadOnly()
}

export function getRuntimeLogFacetsReadOnly(): RuntimeLogFacets {
  const database = getRuntimeLogDatabase()
  const range = database.prepare('SELECT earliest_time, latest_time, total_count FROM runtime_log_facet_summary WHERE bucket_key = ?').get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  const levels = database.prepare('SELECT level AS value, count FROM runtime_log_level_facets WHERE bucket_key = ? AND count > 0 ORDER BY count DESC, level ASC').all(runtimeLogFacetBucketKey) as RuntimeLogRow[]
  const events = database.prepare('SELECT event FROM runtime_log_event_facets WHERE bucket_key = ? AND count > 0 ORDER BY latest_time DESC, event ASC LIMIT ?').all(runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents) as RuntimeLogRow[]
  return { retentionDays: currentRuntimeLogIndexRetentionDays(), earliestIndexedAt: optionalString(range?.earliest_time), latestIndexedAt: optionalString(range?.latest_time), totalIndexed: Number(range?.total_count ?? 0), levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })), events: events.map((row) => String(row.event)) }
}

export async function getRuntimeLogFacetsAsync(): Promise<RuntimeLogFacets> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) return requestSqliteReadWorker({ type: 'get_runtime_log_facets_read_only' })
    return getRuntimeLogFacetsReadOnly()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const [range, levels, events, retentionDays] = await Promise.all([
    client.one<RuntimeLogRow>('SELECT earliest_time, latest_time, total_count FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ?', [runtimeLogFacetBucketKey]),
    client.query<RuntimeLogRow>('SELECT level AS value, count FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = ? AND count > 0 ORDER BY count DESC, level ASC', [runtimeLogFacetBucketKey]),
    client.query<RuntimeLogRow>('SELECT event FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = ? AND count > 0 ORDER BY latest_time DESC, event ASC LIMIT ?', [runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents]),
    currentRuntimeLogIndexRetentionDaysAsync()
  ])
  return { retentionDays, earliestIndexedAt: optionalString(range?.earliest_time), latestIndexedAt: optionalString(range?.latest_time), totalIndexed: Number(range?.total_count ?? 0), levels: levels.map((row) => ({ value: String(row.value), count: Number(row.count ?? 0) })), events: events.map((row) => String(row.event)) }
}

export function listRuntimeLogs(options: RuntimeLogListOptions = {}): RuntimeLogListResult {
  return listRuntimeLogsReadOnly(options)
}

export function listRuntimeLogsReadOnly(options: RuntimeLogListOptions = {}): RuntimeLogListResult {
  const filters = buildRuntimeLogFilters(options)
  const pageSize = normalizeRuntimeLogPageSize(options.pageSize)
  const page = normalizeRuntimeLogPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const rows = getRuntimeLogDatabase()
    .prepare(`
      SELECT ${runtimeLogListSelectColumns('rl')}
      FROM runtime_logs rl
      ${filters.clause}
      ORDER BY rl.time DESC, rl.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as RuntimeLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const items = pageRows.rows.map(runtimeLogListItemFromRow)
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function listRuntimeLogsAsync(options: RuntimeLogListOptions = {}): Promise<RuntimeLogListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({ type: 'list_runtime_logs_read_only', options })
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
  const items = pageRows.rows.map(runtimeLogListItemFromRow)
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
  const row = getRuntimeLogDatabase().prepare(`
    SELECT ${runtimeLogDetailSelectColumns('rl')}
    FROM runtime_logs rl
    WHERE rl.id = ?
    LIMIT 1
  `).get(id.trim()) as RuntimeLogRow | undefined
  return row ? runtimeLogDetailFromRow(row) : undefined
}

export async function getRuntimeLogDetailAsync(id: string): Promise<RuntimeLogDetail | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({ type: 'get_runtime_log_detail_read_only', id })
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

export function getRuntimeLogDetailDeltaReadOnly(id: string): RuntimeLogDetailDelta | undefined {
  const row = getRuntimeLogDatabase().prepare(`
    SELECT id, raw_json
    FROM runtime_logs
    WHERE id = ?
    LIMIT 1
  `).get(id.trim()) as RuntimeLogRow | undefined
  return row ? runtimeLogDetailDeltaFromRow(row) : undefined
}

export async function getRuntimeLogDetailDeltaAsync(id: string): Promise<RuntimeLogDetailDelta | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({ type: 'get_runtime_log_detail_delta_read_only', id })
    }
    return getRuntimeLogDetailDeltaReadOnly(id)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<RuntimeLogRow>(`
    SELECT id, raw_json
    FROM juhe_dataset.runtime_logs
    WHERE id = ?
    LIMIT 1
  `, [id.trim()])
  return row ? runtimeLogDetailDeltaFromRow(row) : undefined
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
  return { clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '', params }
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
  const expression = runtimeConfig.databaseDriver === 'postgres' ? `${column} COLLATE "C"` : column
  clauses.push(`${expression} >= ? AND ${expression} < ?`)
  params.push(text, textPrefixUpperBound(text))
}

function pushMessageKeywordFilter(clauses: string[], params: RuntimeLogFilterValue[], value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push("rl.message LIKE ? ESCAPE '\\'")
  params.push(`%${text.replace(/[\\%_]/g, (char) => `\\${char}`)}%`)
}

function runtimeLogListSelectColumns(alias: string): string {
  return [
    `${alias}.id`, `${alias}.time`, `${alias}.level`,
    `SUBSTR(${alias}.trace_id, 1, 256) AS trace_id`,
    `SUBSTR(${alias}.event, 1, 256) AS event`,
    `SUBSTR(${alias}.message, 1, 1000) AS message`,
    `SUBSTR(${alias}.error_message, 1, 1000) AS error_message`
  ].join(', ')
}

function runtimeLogDetailSelectColumns(alias: string): string {
  return ['id', 'time', 'level', 'trace_id', 'event', 'message', 'error_message', 'raw_json', 'created_at']
    .map((column) => `${alias}.${column}`).join(', ')
}

function normalizeRuntimeLogPage(value: unknown, pageSize: number): number {
  const maxPage = Math.max(1, Math.floor((runtimeLogMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return typeof value === 'number' && Number.isInteger(value) ? Math.min(maxPage, Math.max(1, value)) : 1
}

function normalizeRuntimeLogPageSize(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(runtimeLogMaxPageSize, Math.max(1, value))
    : runtimeLogDefaultPageSize
}

function runtimeLogFromRow(row: RuntimeLogRow): RuntimeLogSummary {
  return {
    id: String(row.id), time: String(row.time), level: String(row.level),
    traceId: optionalString(row.trace_id), event: optionalString(row.event),
    message: optionalString(row.message), errorMessage: optionalString(row.error_message),
    rawJson: optionalString(row.raw_json) ?? '', createdAt: String(row.created_at)
  }
}

function runtimeLogListItemFromRow(row: RuntimeLogRow): RuntimeLogListItem {
  return {
    id: String(row.id), time: String(row.time), level: String(row.level),
    traceId: optionalString(row.trace_id), event: optionalString(row.event),
    message: optionalString(row.message), errorMessage: optionalString(row.error_message)
  }
}

function runtimeLogDetailFromRow(row: RuntimeLogRow): RuntimeLogDetail {
  return runtimeLogFromRow(row) as RuntimeLogDetail
}

function runtimeLogDetailDeltaFromRow(row: RuntimeLogRow): RuntimeLogDetailDelta {
  return { id: String(row.id), rawJson: optionalString(row.raw_json) ?? '' }
}

function currentRuntimeLogIndexRetentionDays(): number {
  try { return runtimeLogIndexRetentionDaysFromSettings(getSettings()) } catch { return runtimeLogIndexRetentionDays }
}

async function currentRuntimeLogIndexRetentionDaysAsync(): Promise<number> {
  try { return runtimeLogIndexRetentionDaysFromSettings(await getSettingsAsync()) } catch { return runtimeLogIndexRetentionDays }
}

function runtimeLogIndexRetentionDaysFromSettings(settings: Record<string, unknown>): number {
  const value = settings.runtimeLogIndexRetentionDays
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value)
    ? Math.min(Math.max(1, value), runtimeLogIndexRetentionMaxDays)
    : runtimeLogIndexRetentionDays
}
