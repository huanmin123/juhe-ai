import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import type { PoolClient } from 'pg'
import { requiredRfc3339Instant } from '../shared/rfc3339.js'
import { hourKey, nextCalendarDateKey, startOfZonedDateKeyIso } from './usage-stats-helpers.js'

export interface AccountHealthJobsPostgresOutcomePool {
  connect(): Promise<PoolClient>
  end(): Promise<void>
}

export interface AccountHealthJobsPostgresStoreSource {
  mode: 'postgres'
  postgresUrl: string
  /** Long-running consumers retain one bounded pool until their runtime stops. */
  persistent?: true
  pool?: AccountHealthJobsPostgresOutcomePool
}

export type AccountHealthJobsStoreSource =
  | { mode: 'sqlite'; databasePath: string }
  | AccountHealthJobsPostgresStoreSource

export interface AccountHealthJobsProjection {
  target_account_id: string
  transition_kind: string
  input_version: number
  config_revision: number
  dispatch_revision: number
  source_config_revision?: number
  // Includes `error` solely so a historical malformed outcome can reach the
  // projector and receive a durable rejection receipt. Projection validation
  // still rejects error for every currently allowed transition.
  expected_account_status: 'active' | 'pending_test' | 'temporary_unavailable' | 'rate_limited' | 'error'
  expected_cooldown_fence?: {
    observation_started_at: string
    generation: string
    source_config_revision?: number
  }
  values?: Record<string, unknown>
  cooldown_fence?: {
    observation_started_at: string
    generation: string
    source_config_revision?: number
  }
}

export interface AccountHealthJobsSourceFence {
  state_key: string
  account_id: string
  source_generation: number
  source_fence_id: string
  runtime_key: string
  probe_generation: number
  config_revision: number
}

export interface AccountHealthJobsOutcome {
  outcome_id: string
  request_id: string
  account_id: string
  outcome: 'complete_success' | 'framing_complete_neutral' | 'upstream_failure' | 'probe_task_failure' | 'stale'
  observed_at: string
  input_version: number
  config_revision: number
  dispatch_revision: number
  status_code?: number
  error_code?: string
  error_message?: string
  next_due_at?: string
  failure_count?: number
  failure_started_at?: string
  account_status?: string
  source_fence?: AccountHealthJobsSourceFence
  key_model_fence?: {
    capability_hash: string
    key_fingerprint: string
    dispatch_revision: number
    owner_id: string
  }
  winner_key_fingerprint?: string
  projection?: AccountHealthJobsProjection
  /** Durable-store ordering timestamp; not part of the Go payload contract. */
  storage_observed_at?: string
}

export interface AccountHealthJobsOutcomeCursor {
  observedAt: string
  outcomeId: string
}

const postgresOutcomeReaderIdleTimeoutMs = 30_000
const postgresOutcomeReaderMaxLifetimeSeconds = 600

export function createPostgresAccountHealthJobsStoreSource(postgresUrl: string): AccountHealthJobsPostgresStoreSource {
  const normalizedUrl = postgresUrl.trim()
  if (!normalizedUrl) throw new Error('J1 PostgreSQL outcome URL 不能为空')
  return { mode: 'postgres', postgresUrl: normalizedUrl, persistent: true }
}

export async function closeAccountHealthJobsStoreSource(source: AccountHealthJobsStoreSource): Promise<void> {
  if (source.mode !== 'postgres') return
  const pool = source.pool
  source.pool = undefined
  await pool?.end()
}

// This is a read-only adapter for the jobs-owned store. It intentionally does
// not expose INSERT/UPDATE/DELETE or a Node fallback executor. The DB-service
// projector consumes returned facts and writes only its own business receipt.
export async function listAccountHealthJobsOutcomes(
  source: AccountHealthJobsStoreSource,
  options: { after?: AccountHealthJobsOutcomeCursor; limit: number }
): Promise<AccountHealthJobsOutcome[]> {
  const limit = normalizedLimit(options.limit)
  const after = normalizeOutcomeCursor(options.after)
  if (source.mode === 'sqlite') {
    return readSqliteOutcomes(source.databasePath, after, limit)
  }
  return await readPostgresOutcomes(source, after, limit)
}

/**
 * Reads only the outcomes needed by one management-page account slice. This
 * stays read-only against the jobs-owned store and deliberately does not use
 * the drain cursor, which is owned by the DB-service projector.
 */
export function listAccountHealthJobsOutcomesForAccounts(
  source: AccountHealthJobsStoreSource,
  options: { accountIds: string[]; observedAfter: string; timezone?: string; hourBuckets?: string[] }
): AccountHealthJobsOutcome[] {
  const normalized = normalizeAccountOutcomeQuery(options)
  if (!normalized.accountIds.length) return []
  if (source.mode !== 'sqlite') {
    throw new Error('PostgreSQL J1 outcome 查询必须使用异步 reader')
  }
  return readSqliteOutcomesForAccounts(source.databasePath, normalized.accountIds, normalized.observedAfter, options.timezone, options.hourBuckets)
}

export async function listAccountHealthJobsOutcomesForAccountsAsync(
  source: AccountHealthJobsStoreSource,
  options: { accountIds: string[]; observedAfter: string; timezone?: string; hourBuckets?: string[] }
): Promise<AccountHealthJobsOutcome[]> {
  const normalized = normalizeAccountOutcomeQuery(options)
  if (!normalized.accountIds.length) return []
  if (source.mode === 'sqlite') {
    return readSqliteOutcomesForAccounts(source.databasePath, normalized.accountIds, normalized.observedAfter, options.timezone, options.hourBuckets)
  }
  return await readPostgresOutcomesForAccounts(source, normalized.accountIds, normalized.observedAfter, options.timezone, options.hourBuckets)
}

function readSqliteOutcomes(path: string, after: AccountHealthJobsOutcomeCursor | undefined, limit: number): AccountHealthJobsOutcome[] {
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => {
    exec(sql: string): void
    prepare(sql: string): { all(...values: unknown[]): unknown[], get(...values: unknown[]): unknown }
    close(): void
  }
  const database = new Constructor(resolve(path), { readOnly: true })
  try {
    database.exec('PRAGMA query_only = ON')
    const state = database.prepare('PRAGMA query_only').all()[0] as Record<string, unknown> | undefined
    if (Number(state?.query_only ?? state?.[0] ?? 0) !== 1) throw new Error('J1 jobs SQLite outcome 读取未进入 query_only')
    const effectiveAfter = after ? resolveSqliteCursor(database, after) : undefined
    const rows = effectiveAfter
      ? database.prepare(`SELECT payload, observed_at AS storage_observed_at FROM account_health_outcomes WHERE observed_at > ? OR (observed_at = ? AND outcome_id > ?) ORDER BY observed_at ASC, outcome_id ASC LIMIT ?`).all(effectiveAfter.observedAt, effectiveAfter.observedAt, effectiveAfter.outcomeId, limit)
      : database.prepare(`SELECT payload, observed_at AS storage_observed_at FROM account_health_outcomes ORDER BY observed_at ASC, outcome_id ASC LIMIT ?`).all(limit)
    return rows.map((row) => decodeOutcomeRow(row))
  } finally {
    database.close()
  }
}

function readSqliteOutcomesForAccounts(path: string, accountIds: string[], observedAfter: string, timezone?: string, hourBuckets?: string[]): AccountHealthJobsOutcome[] {
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => {
    exec(sql: string): void
    prepare(sql: string): { all(...values: unknown[]): unknown[], get(...values: unknown[]): unknown }
    close(): void
  }
  const database = new Constructor(resolve(path), { readOnly: true })
  try {
    database.exec('PRAGMA query_only = ON')
    const state = database.prepare('PRAGMA query_only').all()[0] as Record<string, unknown> | undefined
    if (Number(state?.query_only ?? state?.[0] ?? 0) !== 1) throw new Error('J1 jobs SQLite outcome 读取未进入 query_only')
    if (timezone && hourBuckets?.length) {
      const ranges = normalizeAiHealthHourBuckets(hourBuckets).map((statHour) => zonedHourRange(statHour, timezone))
      const rows: unknown[] = []
      for (const accountChunk of chunkValues(accountIds, 8)) {
        const values: unknown[] = []
        const tuples: string[] = []
        for (const accountId of accountChunk) {
          for (const range of ranges) {
            tuples.push('(?, ?, ?, ?)')
            values.push(accountId, range.statHour, range.startAt, range.endAt)
          }
        }
        const selected = database.prepare(`WITH requested(account_id, stat_hour, start_at, end_at) AS (VALUES ${tuples.join(', ')}) SELECT chosen.payload, chosen.observed_at AS storage_observed_at FROM requested JOIN account_health_outcomes chosen ON chosen.outcome_id = (SELECT candidate.outcome_id FROM account_health_outcomes candidate WHERE candidate.account_id = requested.account_id AND candidate.observed_at >= requested.start_at AND candidate.observed_at < requested.end_at AND candidate.observed_at >= ? AND COALESCE(json_extract(candidate.payload, '$.outcome'), '') <> 'stale' ORDER BY candidate.observed_at DESC, candidate.outcome_id DESC LIMIT 1)`).all(...values, observedAfter)
        rows.push(...selected)
      }
      return rows.map((row) => decodeOutcomeRow(row))
    }
    const placeholders = accountIds.map(() => '?').join(', ')
    const rows = database.prepare(`SELECT payload, observed_at AS storage_observed_at FROM account_health_outcomes WHERE account_id IN (${placeholders}) AND observed_at >= ? ORDER BY observed_at ASC, outcome_id ASC`).all(...accountIds, observedAfter)
    return rows.map((row) => decodeOutcomeRow(row))
  } finally {
    database.close()
  }
}

function zonedHourRange(statHour: string, timezone: string): { statHour: string; startAt: string; endAt: string } {
  const datePart = statHour.slice(0, 10)
  const midnight = startOfZonedDateKeyIso(datePart, timezone)
  const nextMidnight = startOfZonedDateKeyIso(nextCalendarDateKey(datePart), timezone)
  if (!midnight || !nextMidnight) throw new Error('AI 健康 J1 outcome 小时桶日期无法解析')
  const lowerBound = Date.parse(midnight)
  const upperBound = Date.parse(nextMidnight)
  let low = lowerBound
  let high = upperBound
  while (low < high) {
    const middle = Math.floor((low + high) / 2)
    if (hourKey(new Date(middle), timezone) >= statHour) high = middle
    else low = middle + 1
  }
  let startMs = low
  if (hourKey(new Date(startMs), timezone) !== statHour) {
    return { statHour, startAt: new Date(upperBound).toISOString(), endAt: new Date(upperBound).toISOString() }
  }
  let endMs = upperBound
  low = startMs
  high = upperBound
  while (low < high) {
    const middle = Math.floor((low + high) / 2)
    if (hourKey(new Date(middle), timezone) === statHour) low = middle + 1
    else high = middle
  }
  endMs = low
  if (endMs <= startMs) endMs = startMs + 60 * 60 * 1000
  return { statHour, startAt: new Date(startMs).toISOString(), endAt: new Date(endMs).toISOString() }
}

function chunkValues<T>(values: T[], size: number): T[][] {
  const chunks: T[][] = []
  for (let index = 0; index < values.length; index += size) chunks.push(values.slice(index, index + size))
  return chunks
}

function resolveSqliteCursor(
  database: { prepare(sql: string): { get(...values: unknown[]): unknown } },
  after: AccountHealthJobsOutcomeCursor
): AccountHealthJobsOutcomeCursor {
  const row = database.prepare('SELECT observed_at AS storage_observed_at FROM account_health_outcomes WHERE outcome_id = ?').get(after.outcomeId)
  const storageObservedAt = row && typeof row === 'object' ? (row as Record<string, unknown>).storage_observed_at : undefined
  if (typeof storageObservedAt !== 'string' || !storageObservedAt.trim() || storageObservedAt === after.observedAt) {
    return after
  }
  return { observedAt: storageObservedAt.trim(), outcomeId: after.outcomeId }
}

async function readPostgresOutcomes(source: AccountHealthJobsPostgresStoreSource, after: AccountHealthJobsOutcomeCursor | undefined, limit: number): Promise<AccountHealthJobsOutcome[]> {
  const pool = await postgresOutcomePool(source)
  let connection: PoolClient | undefined
  let inTransaction = false
  let failed = false
  try {
    connection = await pool.connect()
    await connection.query('BEGIN READ ONLY')
    inTransaction = true
    await connection.query('SET LOCAL statement_timeout = 5000')
    const effectiveAfter = after ? await resolvePostgresCursor(connection, after) : undefined
    const result = effectiveAfter
      ? await connection.query(`SELECT payload, to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM juhe_jobs.account_health_outcomes WHERE observed_at > $1 OR (observed_at = $1 AND outcome_id > $2) ORDER BY observed_at ASC, outcome_id ASC LIMIT $3`, [effectiveAfter.observedAt, effectiveAfter.outcomeId, limit])
      : await connection.query(`SELECT payload, to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM juhe_jobs.account_health_outcomes ORDER BY observed_at ASC, outcome_id ASC LIMIT $1`, [limit])
    await connection.query('COMMIT')
    inTransaction = false
    return result.rows.map((row: Record<string, unknown>) => decodeOutcomeRow(row))
  } catch (error) {
    failed = true
    if (inTransaction) await connection?.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection?.release(failed ? new Error('J1 outcome reader connection failed') : undefined)
    if (failed || !source.persistent) await closeAccountHealthJobsStoreSource(source).catch(() => undefined)
  }
}

async function readPostgresOutcomesForAccounts(source: AccountHealthJobsPostgresStoreSource, accountIds: string[], observedAfter: string, timezone?: string, hourBuckets?: string[]): Promise<AccountHealthJobsOutcome[]> {
  const pool = await postgresOutcomePool(source)
  let connection: PoolClient | undefined
  let inTransaction = false
  let failed = false
  try {
    connection = await pool.connect()
    await connection.query('BEGIN READ ONLY')
    inTransaction = true
    await connection.query('SET LOCAL statement_timeout = 5000')
    const result = timezone && hourBuckets?.length
      ? await connection.query(`SELECT selected.payload, to_char(selected.observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM unnest($1::text[]) AS accounts(account_id) CROSS JOIN unnest($4::text[]) AS hours(stat_hour) CROSS JOIN LATERAL (SELECT payload, observed_at FROM juhe_jobs.account_health_outcomes WHERE account_id = accounts.account_id AND observed_at >= $2::timestamptz AND observed_at >= (hours.stat_hour || ':00:00')::timestamp AT TIME ZONE $3 AND observed_at < ((hours.stat_hour || ':00:00')::timestamp + INTERVAL '1 hour') AT TIME ZONE $3 AND outcome <> 'stale' ORDER BY observed_at DESC, outcome_id DESC LIMIT 1) AS selected`, [accountIds, observedAfter, timezone, normalizeAiHealthHourBuckets(hourBuckets)])
      : timezone
        ? await connection.query(`SELECT DISTINCT ON (account_id, date_trunc('hour', observed_at AT TIME ZONE $3)) payload, to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM juhe_jobs.account_health_outcomes WHERE account_id = ANY($1::text[]) AND observed_at >= $2::timestamptz AND outcome <> 'stale' ORDER BY account_id, date_trunc('hour', observed_at AT TIME ZONE $3), observed_at DESC, outcome_id DESC`, [accountIds, observedAfter, timezone])
      : await connection.query(`SELECT payload, to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM juhe_jobs.account_health_outcomes WHERE account_id = ANY($1::text[]) AND observed_at >= $2::timestamptz ORDER BY observed_at ASC, outcome_id ASC`, [accountIds, observedAfter])
    await connection.query('COMMIT')
    inTransaction = false
    return result.rows.map((row: Record<string, unknown>) => decodeOutcomeRow(row))
  } catch (error) {
    failed = true
    if (inTransaction) await connection?.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection?.release(failed ? new Error('J1 outcome reader connection failed') : undefined)
    if (failed || !source.persistent) await closeAccountHealthJobsStoreSource(source).catch(() => undefined)
  }
}

async function postgresOutcomePool(source: AccountHealthJobsPostgresStoreSource): Promise<AccountHealthJobsPostgresOutcomePool> {
  if (source.pool) return source.pool
  const { Pool } = await import('pg')
  const pool = new Pool({
    connectionString: source.postgresUrl,
    max: 1,
    idleTimeoutMillis: postgresOutcomeReaderIdleTimeoutMs,
    maxLifetimeSeconds: postgresOutcomeReaderMaxLifetimeSeconds,
    connectionTimeoutMillis: 5_000,
    query_timeout: 5_000,
    application_name: 'juhe-ai:j1-outcome-reader'
  }) as unknown as AccountHealthJobsPostgresOutcomePool
  source.pool = pool
  return pool
}

function decodeOutcomeRow(row: unknown): AccountHealthJobsOutcome {
  if (!row || typeof row !== 'object') throw new Error('J1 jobs outcome 行格式无效')
  const record = row as Record<string, unknown>
  const storageObservedAt = record.storage_observed_at
  if (typeof storageObservedAt !== 'string' || !storageObservedAt.trim()) {
    throw new Error('J1 jobs outcome storage observed_at 无效')
  }
  return {
    ...decodeAccountHealthJobsOutcomePayload(rowPayload(record)),
    storage_observed_at: storageObservedAt.trim()
  }
}

async function resolvePostgresCursor(
  connection: { query(text: string, values?: unknown[]): Promise<{ rows: Array<Record<string, unknown>> }> },
  after: AccountHealthJobsOutcomeCursor
): Promise<AccountHealthJobsOutcomeCursor> {
  const result = await connection.query(`SELECT to_char(observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at FROM juhe_jobs.account_health_outcomes WHERE outcome_id = $1`, [after.outcomeId])
  const storageObservedAt = result.rows[0]?.storage_observed_at
  if (typeof storageObservedAt !== 'string' || !storageObservedAt.trim() || storageObservedAt === after.observedAt) {
    return after
  }
  return { observedAt: storageObservedAt.trim(), outcomeId: after.outcomeId }
}

function rowPayload(row: unknown): unknown {
  if (!row || typeof row !== 'object') throw new Error('J1 jobs outcome 行格式无效')
  const payload = (row as Record<string, unknown>).payload
  if (payload && typeof payload === 'object') return payload
  if (typeof payload !== 'string') throw new Error('J1 jobs outcome payload 无效')
  try {
    return JSON.parse(payload) as unknown
  } catch {
    throw new Error('J1 jobs SQLite outcome payload 不是 JSON')
  }
}

export function decodeAccountHealthJobsOutcomePayload(value: unknown): AccountHealthJobsOutcome {
  const record = objectRecord(value, 'J1 jobs outcome')
  const outcome = requiredEnum(record.outcome, 'outcome', ['complete_success', 'framing_complete_neutral', 'upstream_failure', 'probe_task_failure', 'stale'] as const)
  const result: AccountHealthJobsOutcome = {
    outcome_id: requiredText(record.outcome_id, 'outcome_id'),
    request_id: requiredText(record.request_id, 'request_id'),
    account_id: requiredText(record.account_id, 'account_id'),
    outcome,
    observed_at: requiredIso(record.observed_at, 'observed_at'),
    input_version: requiredPositiveInteger(record.input_version, 'input_version'),
    config_revision: requiredPositiveInteger(record.config_revision, 'config_revision'),
    dispatch_revision: requiredPositiveInteger(record.dispatch_revision, 'dispatch_revision')
  }
  if (record.status_code !== undefined) result.status_code = boundedInteger(record.status_code, 'status_code', 100, 599)
  if (record.error_code !== undefined) result.error_code = boundedText(record.error_code, 'error_code', 200)
  if (record.error_message !== undefined) result.error_message = boundedText(record.error_message, 'error_message', 2_000)
  if (record.next_due_at !== undefined) result.next_due_at = requiredIso(record.next_due_at, 'next_due_at')
  if (record.failure_count !== undefined) result.failure_count = boundedInteger(record.failure_count, 'failure_count', 0, 1_000_000)
  if (record.failure_started_at !== undefined) result.failure_started_at = requiredIso(record.failure_started_at, 'failure_started_at')
  if (record.account_status !== undefined) result.account_status = requiredEnum(record.account_status, 'account_status', ['active', 'pending_test', 'temporary_unavailable', 'rate_limited', 'error'] as const)
  if (record.source_fence !== undefined) result.source_fence = normalizeSourceFence(record.source_fence, result.account_id, result.config_revision)
  if (record.projection !== undefined) {
    result.projection = normalizeProjection(record.projection)
    if (result.projection.target_account_id !== result.account_id
      || result.projection.input_version !== result.input_version
      || result.projection.config_revision !== result.config_revision
      || result.projection.dispatch_revision !== result.dispatch_revision) {
      throw new Error('projection 与 outcome account/revision fence 不一致')
    }
  }
  return result
}

function normalizeSourceFence(value: unknown, accountID: string, configRevision: number): AccountHealthJobsSourceFence {
  const record = objectRecord(value, 'J1 jobs source_fence')
  const fence: AccountHealthJobsSourceFence = {
    state_key: requiredText(record.state_key, 'source_fence.state_key'),
    account_id: requiredText(record.account_id, 'source_fence.account_id'),
    source_generation: requiredPositiveInteger(record.source_generation, 'source_fence.source_generation'),
    source_fence_id: requiredText(record.source_fence_id, 'source_fence.source_fence_id'),
    runtime_key: requiredText(record.runtime_key, 'source_fence.runtime_key'),
    probe_generation: requiredPositiveInteger(record.probe_generation, 'source_fence.probe_generation'),
    config_revision: requiredPositiveInteger(record.config_revision, 'source_fence.config_revision')
  }
  if (fence.account_id !== accountID || fence.config_revision !== configRevision) {
    throw new Error('source_fence 与 outcome account/config fence 不一致')
  }
  return fence
}

function normalizeProjection(value: unknown): AccountHealthJobsProjection {
  const record = objectRecord(value, 'J1 jobs projection')
  const projection: AccountHealthJobsProjection = {
    target_account_id: requiredText(record.target_account_id, 'projection.target_account_id'),
    transition_kind: requiredText(record.transition_kind, 'projection.transition_kind'),
    input_version: requiredPositiveInteger(record.input_version, 'projection.input_version'),
    config_revision: requiredPositiveInteger(record.config_revision, 'projection.config_revision'),
    dispatch_revision: requiredPositiveInteger(record.dispatch_revision, 'projection.dispatch_revision'),
    // Historical health_failure outcomes may contain `error`. Keep them
    // decodable so the projector can record a durable rejection receipt.
    expected_account_status: requiredEnum(record.expected_account_status, 'projection.expected_account_status', ['active', 'pending_test', 'temporary_unavailable', 'rate_limited', 'error'] as const)
  }
  if (record.source_config_revision !== undefined) projection.source_config_revision = requiredPositiveInteger(record.source_config_revision, 'projection.source_config_revision')
  if (record.values !== undefined) projection.values = objectRecord(record.values, 'projection.values')
  if (record.expected_cooldown_fence !== undefined) {
    projection.expected_cooldown_fence = normalizeCooldownFence(record.expected_cooldown_fence, 'projection.expected_cooldown_fence')
  }
  if (record.cooldown_fence !== undefined) {
    projection.cooldown_fence = normalizeCooldownFence(record.cooldown_fence, 'projection.cooldown_fence')
  }
  return projection
}

function normalizeCooldownFence(value: unknown, field: string): NonNullable<AccountHealthJobsProjection['cooldown_fence']> {
  const fence = objectRecord(value, field)
  const result: NonNullable<AccountHealthJobsProjection['cooldown_fence']> = {
    observation_started_at: requiredIso(fence.observation_started_at, `${field}.observation_started_at`),
    generation: requiredText(fence.generation, `${field}.generation`)
  }
  if (fence.source_config_revision !== undefined) result.source_config_revision = requiredPositiveInteger(fence.source_config_revision, `${field}.source_config_revision`)
  return result
}

function normalizedLimit(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > 1_000) throw new Error('J1 outcome 查询 limit 必须在 1..1000')
  return value
}

function normalizeAiHealthHourBuckets(value: string[]): string[] {
  if (value.length > 31 * 24) throw new Error('AI 健康 J1 outcome 查询小时桶超出允许范围')
  const buckets = [...new Set(value)]
  if (buckets.some((bucket) => !/^\d{4}-\d{2}-\d{2}T\d{2}$/.test(bucket))) {
    throw new Error('AI 健康 J1 outcome 查询小时桶无效')
  }
  return buckets
}

function normalizeAccountOutcomeQuery(value: { accountIds: string[]; observedAfter: string }): { accountIds: string[]; observedAfter: string } {
  if (!Array.isArray(value.accountIds) || value.accountIds.length > 50) throw new Error('J1 outcome 账户查询最多允许 50 个账户')
  const accountIds = [...new Set(value.accountIds.map((accountId) => requiredText(accountId, 'outcome accountId')))]
  return { accountIds, observedAfter: requiredIso(value.observedAfter, 'outcome observedAfter') }
}

function normalizeOutcomeCursor(value: AccountHealthJobsOutcomeCursor | undefined): AccountHealthJobsOutcomeCursor | undefined {
  if (value === undefined) return undefined
  return {
    observedAt: requiredIso(value.observedAt, 'outcome cursor observedAt'),
    outcomeId: requiredText(value.outcomeId, 'outcome cursor outcomeId')
  }
}

function objectRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} 必须是对象`)
  return value as Record<string, unknown>
}

function requiredText(value: unknown, field: string): string {
  if (typeof value !== 'string' || !value.trim() || value.length > 4_096) throw new Error(`${field} 必须是非空文本`)
  return value.trim()
}

function requiredEnum<const T extends readonly string[]>(value: unknown, field: string, allowed: T): T[number] {
  if (typeof value !== 'string' || !(allowed as readonly string[]).includes(value)) {
    throw new Error(`${field} 不是允许的枚举值`)
  }
  return value as T[number]
}

function boundedText(value: unknown, field: string, maximum: number): string {
  const text = requiredText(value, field)
  if (text.length > maximum) throw new Error(`${field} 超过最大长度`)
  return text
}

function requiredPositiveInteger(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 1) throw new Error(`${field} 必须是正安全整数`)
  return value
}

function boundedInteger(value: unknown, field: string, minimum: number, maximum: number): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < minimum || value > maximum) throw new Error(`${field} 必须在 ${minimum}..${maximum}`)
  return value
}

function requiredIso(value: unknown, field: string): string {
  const text = requiredText(value, field)
  return requiredRfc3339Instant(text, field)
}
