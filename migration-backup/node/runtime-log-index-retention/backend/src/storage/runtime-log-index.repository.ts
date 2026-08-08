import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
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
  truncationGeneration: number
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
  truncationGeneration?: number
  fileMtimeMs?: number
  lastReadAt?: string
  lastErrorMessage?: string
}

export interface RuntimeLogFileCursorAsyncDependencies {
  getPostgresClient?: () => Promise<DatabaseClient>
  now?: () => string
}

type RuntimeLogRow = Record<string, unknown>
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

export const runtimeLogIndexRetentionDays = 14
export const runtimeLogIndexRetentionMaxDays = 90
const runtimeLogFacetBucketKey = 'current'
const runtimeLogFacetMaxEvents = 80

export function createRuntimeLogsBatch(inputs: RuntimeLogIndexInput[]): void {
  if (inputs.length === 0) return
  const database = getDatasetDatabase()
  const insertLog = database.prepare(`
    INSERT OR IGNORE INTO runtime_logs (
      id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const facetRows: RuntimeLogFacetInput[] = []
    for (const input of inputs.map(normalizeRuntimeLogIndexInput)) {
      const result = insertLog.run(input.id, input.logFile ?? null, input.logOffset, input.lineNumber, input.time, input.level, input.traceId ?? null, input.event ?? null, input.message ?? null, input.errorMessage ?? null, input.rawJson, input.createdAt)
      if (Number(result.changes ?? 0) > 0) facetRows.push({ time: input.time, level: input.level, event: input.event })
    }
    incrementRuntimeLogFacetSnapshots(database, facetRows)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try { rollbackDatabaseTransaction(database, transactionStarted) } catch {}
    throw error
  }
}

export async function createRuntimeLogsBatchAsync(inputs: RuntimeLogIndexInput[]): Promise<void> {
  if (inputs.length === 0) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    createRuntimeLogsBatch(inputs)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    const facetRows: RuntimeLogFacetInput[] = []
    for (const input of inputs.map(normalizeRuntimeLogIndexInput)) {
      const result = await tx.execute(`
        INSERT INTO juhe_dataset.runtime_logs (
          id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO NOTHING
      `, [input.id, input.logFile ?? null, input.logOffset, input.lineNumber, input.time, input.level, input.traceId ?? null, input.event ?? null, input.message ?? null, input.errorMessage ?? null, input.rawJson, input.createdAt])
      if (result.changes > 0) facetRows.push({ time: input.time, level: input.level, event: input.event })
    }
    await incrementRuntimeLogFacetSnapshotsPostgres(tx, facetRows)
  })
}

export function getRuntimeLogFacets(): RuntimeLogFacets {
  return getRuntimeLogFacetsReadOnly()
}

export function getRuntimeLogFacetsReadOnly(): RuntimeLogFacets {
  const database = getDatasetDatabase()
  const range = database.prepare('SELECT earliest_time, latest_time, total_count FROM runtime_log_facet_summary WHERE bucket_key = ?').get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  const levels = database.prepare(`SELECT level AS value, count FROM runtime_log_level_facets WHERE bucket_key = ? AND count > 0 ORDER BY count DESC, level ASC`).all(runtimeLogFacetBucketKey) as RuntimeLogRow[]
  const events = database.prepare(`SELECT event FROM runtime_log_event_facets WHERE bucket_key = ? AND count > 0 ORDER BY latest_time DESC, event ASC LIMIT ?`).all(runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents) as RuntimeLogRow[]
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

export function ensureRuntimeLogFacetSnapshots(cutoffIso = retentionCutoffIso(currentRuntimeLogIndexRetentionDays())): void {
  const database = getDatasetDatabase()
  const summary = database.prepare('SELECT bucket_key FROM runtime_log_facet_summary WHERE bucket_key = ? LIMIT 1').get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  if (summary?.bucket_key) return
  database.prepare('SELECT id FROM runtime_logs WHERE time >= ? LIMIT 1').get(cutoffIso)
}

export function cleanupRuntimeLogIndex(cutoffIso = retentionCutoffIso(currentRuntimeLogIndexRetentionDays()), limit = 10000): number {
  const database = getDatasetDatabase()
  const rows = database.prepare('SELECT id, time, level, event FROM runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?').all(cutoffIso, Math.max(1, Math.trunc(limit))) as RuntimeLogRow[]
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let deleted = 0
    for (const chunk of chunkValues(ids, 900)) deleted += Number(database.prepare(`DELETE FROM runtime_logs WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk).changes ?? 0)
    if (deleted > 0) decrementRuntimeLogFacetSnapshots(database, rows.map((row) => ({ time: String(row.time), level: String(row.level), event: optionalString(row.event) })), cutoffIso)
    commitDatabaseTransaction(database, transactionStarted)
    return deleted
  } catch (error) {
    try { rollbackDatabaseTransaction(database, transactionStarted) } catch {}
    throw error
  }
}

export async function cleanupRuntimeLogIndexAsync(cutoffIso?: string, limit = 10000): Promise<number> {
  const effectiveCutoffIso = cutoffIso ?? retentionCutoffIso(await currentRuntimeLogIndexRetentionDaysAsync())
  if (runtimeConfig.databaseDriver !== 'postgres') return cleanupRuntimeLogIndex(effectiveCutoffIso, limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<RuntimeLogRow>('SELECT id, time, level, event FROM juhe_dataset.runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?', [effectiveCutoffIso, Math.max(1, Math.trunc(limit))])
  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  if (ids.length === 0) return 0
  let deleted = 0
  await client.transaction(async (tx) => {
    for (const chunk of chunkValues(ids, 10000)) deleted += Number((await tx.execute('DELETE FROM juhe_dataset.runtime_logs WHERE id = ANY(?::text[])', [chunk])).changes ?? 0)
    if (deleted > 0) await decrementRuntimeLogFacetSnapshotsAsync(tx, rows.map((row) => ({ time: String(row.time), level: String(row.level), event: optionalString(row.event) })), effectiveCutoffIso)
  })
  return deleted
}

export function cleanupRuntimeLogFileCursorsBefore(cutoffIso: string, limit = 10000): number {
  return Number(getDatasetDatabase().prepare(`DELETE FROM runtime_log_file_cursors WHERE rowid IN (SELECT rowid FROM runtime_log_file_cursors WHERE updated_at < ? AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, rowid ASC LIMIT ?)`).run(cutoffIso, Math.max(1, Math.trunc(limit))).changes ?? 0)
}

export async function cleanupRuntimeLogFileCursorsBeforeAsync(cutoffIso: string, limit = 10000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') return cleanupRuntimeLogFileCursorsBefore(cutoffIso, limit)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < ? AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT ?)`, [cutoffIso, Math.max(1, Math.trunc(limit))])
  return Number(result.changes ?? 0)
}

export function getRuntimeLogFileCursor(logFile: string): RuntimeLogFileCursor | undefined {
  const row = getDatasetDatabase().prepare('SELECT * FROM runtime_log_file_cursors WHERE log_file = ?').get(logFile) as RuntimeLogRow | undefined
  return row ? runtimeLogFileCursorFromRow(row) : undefined
}

export async function getRuntimeLogFileCursorAsync(logFile: string, dependencies: RuntimeLogFileCursorAsyncDependencies = {}): Promise<RuntimeLogFileCursor | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') return getRuntimeLogFileCursor(logFile)
  const rows = await (await runtimeLogFileCursorPostgresClient(dependencies)).query<RuntimeLogRow>('SELECT * FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = ?', [logFile])
  return rows[0] ? runtimeLogFileCursorFromRow(rows[0]) : undefined
}

export function getRuntimeLogFileCursorByIdentity(fileIdentity: string): RuntimeLogFileCursor | undefined {
  const row = getDatasetDatabase().prepare('SELECT * FROM runtime_log_file_cursors WHERE file_identity = ? ORDER BY updated_at DESC LIMIT 1').get(fileIdentity) as RuntimeLogRow | undefined
  return row ? runtimeLogFileCursorFromRow(row) : undefined
}

export async function getRuntimeLogFileCursorByIdentityAsync(fileIdentity: string, dependencies: RuntimeLogFileCursorAsyncDependencies = {}): Promise<RuntimeLogFileCursor | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') return getRuntimeLogFileCursorByIdentity(fileIdentity)
  const rows = await (await runtimeLogFileCursorPostgresClient(dependencies)).query<RuntimeLogRow>('SELECT * FROM juhe_dataset.runtime_log_file_cursors WHERE file_identity = ? ORDER BY updated_at DESC LIMIT 1', [fileIdentity])
  return rows[0] ? runtimeLogFileCursorFromRow(rows[0]) : undefined
}

export function upsertRuntimeLogFileCursor(input: RuntimeLogFileCursorInput): void {
  const now = nowIso()
  getDatasetDatabase().prepare(cursorUpsertSql('runtime_log_file_cursors')).run(...cursorValues(input, now))
}

export async function upsertRuntimeLogFileCursorAsync(input: RuntimeLogFileCursorInput, dependencies: RuntimeLogFileCursorAsyncDependencies = {}): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') return upsertRuntimeLogFileCursor(input)
  const now = dependencies.now?.() ?? nowIso()
  await (await runtimeLogFileCursorPostgresClient(dependencies)).execute(cursorUpsertSql('juhe_dataset.runtime_log_file_cursors'), cursorValues(input, now))
}

function cursorUpsertSql(tableName: string): string {
  return `INSERT INTO ${tableName} (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(log_file) DO UPDATE SET file_identity = excluded.file_identity, cursor_offset = excluded.cursor_offset, line_number = excluded.line_number, file_size = excluded.file_size, truncation_generation = excluded.truncation_generation, file_mtime_ms = excluded.file_mtime_ms, last_read_at = excluded.last_read_at, last_error_message = excluded.last_error_message, updated_at = excluded.updated_at`
}

function cursorValues(input: RuntimeLogFileCursorInput, now: string): Array<string | number | null> {
  return [input.logFile, input.fileIdentity ?? null, positiveInteger(input.cursorOffset), positiveInteger(input.lineNumber), positiveInteger(input.fileSize), positiveInteger(input.truncationGeneration), integerOrNull(input.fileMtimeMs), input.lastReadAt ?? now, input.lastErrorMessage ?? null, now, now]
}

async function runtimeLogFileCursorPostgresClient(dependencies: RuntimeLogFileCursorAsyncDependencies): Promise<DatabaseClient> {
  return dependencies.getPostgresClient ? dependencies.getPostgresClient() : createPostgresDatabaseClient(await getPostgresPool())
}

function runtimeLogFileCursorFromRow(row: RuntimeLogRow): RuntimeLogFileCursor {
  return { logFile: String(row.log_file), fileIdentity: optionalString(row.file_identity), cursorOffset: positiveInteger(row.cursor_offset), lineNumber: positiveInteger(row.line_number), fileSize: positiveInteger(row.file_size), truncationGeneration: positiveInteger(row.truncation_generation), fileMtimeMs: integerOrNull(row.file_mtime_ms) ?? undefined, lastReadAt: optionalString(row.last_read_at), lastErrorMessage: optionalString(row.last_error_message), createdAt: String(row.created_at), updatedAt: String(row.updated_at) }
}

function normalizeRuntimeLogIndexInput(input: RuntimeLogIndexInput): NormalizedRuntimeLogIndexInput {
  const fallbackNowIso = nowIso()
  return { id: input.id ?? newId('rtlog'), logFile: input.logFile, logOffset: integerOrNull(input.logOffset), lineNumber: integerOrNull(input.lineNumber), time: normalizeRuntimeLogTimestamp(input.time) ?? fallbackNowIso, level: normalizeLevel(input.level), traceId: input.traceId, event: input.event, message: input.message, errorMessage: input.errorMessage, rawJson: input.rawJson, createdAt: normalizeRuntimeLogTimestamp(input.createdAt) ?? fallbackNowIso }
}

function normalizeRuntimeLogTimestamp(value: string | undefined): string | undefined {
  if (typeof value !== 'string') return undefined
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : undefined
}

function incrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getDatasetDatabase>, rows: RuntimeLogFacetInput[]): void {
  const retainedRows = rows.filter((row) => row.time >= retentionCutoffIso(currentRuntimeLogIndexRetentionDays()))
  if (retainedRows.length === 0) return
  const timestamp = nowIso()
  const [earliestTime, latestTime] = rangeTimes(retainedRows)
  database.prepare(`INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bucket_key) DO UPDATE SET total_count = total_count + excluded.total_count, earliest_time = CASE WHEN runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at`).run(runtimeLogFacetBucketKey, retainedRows.length, earliestTime, latestTime, timestamp)
  const levels = new Map<string, number>()
  const events = new Map<string, { count: number; latestTime: string }>()
  for (const row of retainedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      const current = events.get(event)
      events.set(event, { count: (current?.count ?? 0) + 1, latestTime: current && current.latestTime > row.time ? current.latestTime : row.time })
    }
  }
  const upsertLevel = database.prepare('INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(bucket_key, level) DO UPDATE SET count = count + excluded.count, updated_at = excluded.updated_at')
  for (const [level, count] of levels) upsertLevel.run(runtimeLogFacetBucketKey, level, count, timestamp)
  const upsertEvent = database.prepare(`INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bucket_key, event) DO UPDATE SET count = count + excluded.count, latest_time = CASE WHEN runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at`)
  for (const [event, summary] of events) upsertEvent.run(runtimeLogFacetBucketKey, event, summary.count, summary.latestTime, timestamp)
}

async function incrementRuntimeLogFacetSnapshotsPostgres(client: DatabaseClient, rows: RuntimeLogFacetInput[]): Promise<void> {
  const cutoff = retentionCutoffIso(await currentRuntimeLogIndexRetentionDaysAsync())
  const retainedRows = rows.filter((row) => row.time >= cutoff)
  if (retainedRows.length === 0) return
  const timestamp = nowIso()
  const [earliestTime, latestTime] = rangeTimes(retainedRows)
  await client.execute(`INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bucket_key) DO UPDATE SET total_count = runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at`, [runtimeLogFacetBucketKey, retainedRows.length, earliestTime, latestTime, timestamp])
  const levels = new Map<string, number>()
  const events = new Map<string, { count: number; latestTime: string }>()
  for (const row of retainedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      const current = events.get(event)
      events.set(event, { count: (current?.count ?? 0) + 1, latestTime: current && current.latestTime > row.time ? current.latestTime : row.time })
    }
  }
  if (levels.size > 0) {
    const params: unknown[] = []
    const values = Array.from(levels.entries()).map(([level, count]) => { params.push(runtimeLogFacetBucketKey, level, count, timestamp); return '(?, ?, ?, ?)' })
    await client.execute(`INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ${values.join(', ')} ON CONFLICT(bucket_key, level) DO UPDATE SET count = runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at`, params)
  }
  if (events.size > 0) {
    const params: unknown[] = []
    const values = Array.from(events.entries()).map(([event, summary]) => { params.push(runtimeLogFacetBucketKey, event, summary.count, summary.latestTime, timestamp); return '(?, ?, ?, ?, ?)' })
    await client.execute(`INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ${values.join(', ')} ON CONFLICT(bucket_key, event) DO UPDATE SET count = runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at`, params)
  }
}

function decrementRuntimeLogFacetSnapshots(database: ReturnType<typeof getDatasetDatabase>, rows: RuntimeLogFacetInput[], cutoffIso: string): void {
  if (rows.length === 0) return
  const timestamp = nowIso()
  const summary = database.prepare('SELECT earliest_time FROM runtime_log_facet_summary WHERE bucket_key = ?').get(runtimeLogFacetBucketKey) as RuntimeLogRow | undefined
  const earliestCountedTime = optionalString(summary?.earliest_time)
  const countedRows = earliestCountedTime ? rows.filter((row) => row.time >= earliestCountedTime) : rows
  if (countedRows.length === 0) return
  const earliestRow = database.prepare('SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time ASC, id ASC LIMIT 1').get(cutoffIso) as RuntimeLogRow | undefined
  const latestRow = database.prepare('SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time DESC, id DESC LIMIT 1').get(cutoffIso) as RuntimeLogRow | undefined
  database.prepare('UPDATE runtime_log_facet_summary SET total_count = MAX(0, total_count - ?), earliest_time = ?, latest_time = ?, updated_at = ? WHERE bucket_key = ?').run(countedRows.length, optionalString(earliestRow?.time) ?? null, optionalString(latestRow?.time) ?? null, timestamp, runtimeLogFacetBucketKey)
  database.prepare('DELETE FROM runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0').run(runtimeLogFacetBucketKey)
  decrementFacetMaps(countedRows, (level, count) => database.prepare('UPDATE runtime_log_level_facets SET count = MAX(0, count - ?), updated_at = ? WHERE bucket_key = ? AND level = ?').run(count, timestamp, runtimeLogFacetBucketKey, level), (event, count) => database.prepare('UPDATE runtime_log_event_facets SET count = MAX(0, count - ?), updated_at = ? WHERE bucket_key = ? AND event = ?').run(count, timestamp, runtimeLogFacetBucketKey, event))
  database.prepare('DELETE FROM runtime_log_level_facets WHERE bucket_key = ? AND count <= 0').run(runtimeLogFacetBucketKey)
  database.prepare('DELETE FROM runtime_log_event_facets WHERE bucket_key = ? AND count <= 0').run(runtimeLogFacetBucketKey)
}

async function decrementRuntimeLogFacetSnapshotsAsync(client: DatabaseClient, rows: RuntimeLogFacetInput[], cutoffIso: string): Promise<void> {
  if (rows.length === 0) return
  const timestamp = nowIso()
  const summary = await client.one<RuntimeLogRow>('SELECT earliest_time FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ?', [runtimeLogFacetBucketKey])
  const earliestCountedTime = optionalString(summary?.earliest_time)
  const countedRows = earliestCountedTime ? rows.filter((row) => row.time >= earliestCountedTime) : rows
  if (countedRows.length === 0) return
  const [earliestRow, latestRow] = await Promise.all([
    client.one<RuntimeLogRow>('SELECT time FROM juhe_dataset.runtime_logs WHERE time >= ? ORDER BY time ASC, id ASC LIMIT 1', [cutoffIso]),
    client.one<RuntimeLogRow>('SELECT time FROM juhe_dataset.runtime_logs WHERE time >= ? ORDER BY time DESC, id DESC LIMIT 1', [cutoffIso])
  ])
  await client.execute('UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - ?), earliest_time = ?, latest_time = ?, updated_at = ? WHERE bucket_key = ?', [countedRows.length, optionalString(earliestRow?.time) ?? null, optionalString(latestRow?.time) ?? null, timestamp, runtimeLogFacetBucketKey])
  await client.execute('DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0', [runtimeLogFacetBucketKey])
  const levels = new Map<string, number>()
  const events = new Map<string, number>()
  collectFacetCounts(countedRows, levels, events)
  for (const [level, count] of levels) await client.execute('UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - ?), updated_at = ? WHERE bucket_key = ? AND level = ?', [count, timestamp, runtimeLogFacetBucketKey, level])
  await client.execute('DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = ? AND count <= 0', [runtimeLogFacetBucketKey])
  for (const [event, count] of events) await client.execute('UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - ?), updated_at = ? WHERE bucket_key = ? AND event = ?', [count, timestamp, runtimeLogFacetBucketKey, event])
  await client.execute('DELETE FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = ? AND count <= 0', [runtimeLogFacetBucketKey])
}

function decrementFacetMaps(rows: RuntimeLogFacetInput[], updateLevel: (level: string, count: number) => void, updateEvent: (event: string, count: number) => void): void {
  const levels = new Map<string, number>()
  const events = new Map<string, number>()
  collectFacetCounts(rows, levels, events)
  for (const [level, count] of levels) updateLevel(level, count)
  for (const [event, count] of events) updateEvent(event, count)
}

function collectFacetCounts(rows: RuntimeLogFacetInput[], levels: Map<string, number>, events: Map<string, number>): void {
  for (const row of rows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) events.set(event, (events.get(event) ?? 0) + 1)
  }
}

function rangeTimes(rows: RuntimeLogFacetInput[]): [string, string] {
  const sorted = rows.map((row) => row.time).sort()
  return [sorted[0], sorted[sorted.length - 1]]
}

function normalizeLevel(value: string): string {
  return value.trim().toLowerCase() || 'info'
}

function positiveInteger(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : 0
}

function integerOrNull(value: unknown): number | null {
  if (value === null || value === undefined) return null
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.trunc(numeric) : null
}

export function runtimeLogIndexRetentionDaysFromSettings(settings: Record<string, unknown>): number {
  const value = settings.runtimeLogIndexRetentionDays
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) ? Math.min(Math.max(1, value), runtimeLogIndexRetentionMaxDays) : runtimeLogIndexRetentionDays
}

function currentRuntimeLogIndexRetentionDays(): number {
  try { return runtimeLogIndexRetentionDaysFromSettings(getSettings()) } catch { return runtimeLogIndexRetentionDays }
}

async function currentRuntimeLogIndexRetentionDaysAsync(): Promise<number> {
  try { return runtimeLogIndexRetentionDaysFromSettings(await getSettingsAsync()) } catch { return runtimeLogIndexRetentionDays }
}

function retentionCutoffIso(retentionDays = runtimeLogIndexRetentionDays): string {
  return new Date(Date.now() - retentionDays * 24 * 60 * 60 * 1000).toISOString()
}
