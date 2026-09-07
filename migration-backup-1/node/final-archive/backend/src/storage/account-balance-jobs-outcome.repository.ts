import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import type { PoolClient } from 'pg'
import { parseRfc3339Instant, requiredRfc3339Instant } from '../shared/rfc3339.js'

export interface AccountBalanceJobsPostgresOutcomePool {
  connect(): Promise<PoolClient>
  end(): Promise<void>
}

export interface AccountBalanceJobsPostgresStoreSource {
  mode: 'postgres'
  postgresUrl: string
  pool?: AccountBalanceJobsPostgresOutcomePool
  readerRoleVerified?: boolean
}

export type AccountBalanceJobsStoreSource =
  | { mode: 'sqlite'; databasePath: string }
  | AccountBalanceJobsPostgresStoreSource

export type AccountBalanceJobsOutcomeCursor = { observedAt: string; outcomeId: string }
export interface AccountBalanceJobsOutcome {
  outcomeId: string
  requestId: string
  accountId: string
  systemAccountId: string
  inputVersion: number
  configRevision: number
  trigger: 'periodic' | 'first_probe' | 'manual'
  observedAt: string
  snapshot: Record<string, unknown>
  adapter?: string
  nextRefreshAt: string | null
  expectedNextRefreshAt?: string | null
  expectedInput?: number
  expectedConfig?: number
  errorCode?: string
  errorMessage?: string
  storageObservedAt: string
}

const postgresOutcomeReaderRole = 'juhe_ai_j2_outcome_reader'
const postgresOutcomeReaderIdleTimeoutMs = 30_000
const postgresOutcomeReaderMaxLifetimeSeconds = 600

export function createPostgresAccountBalanceJobsStoreSource(postgresUrl: string): AccountBalanceJobsPostgresStoreSource {
  const normalizedUrl = postgresUrl.trim()
  if (!normalizedUrl) throw new Error('J2 PostgreSQL outcome URL 不能为空')
  return { mode: 'postgres', postgresUrl: normalizedUrl }
}

export async function closeAccountBalanceJobsStoreSource(source: AccountBalanceJobsStoreSource): Promise<void> {
  if (source.mode !== 'postgres') return
  const pool = source.pool
  source.pool = undefined
  source.readerRoleVerified = false
  await pool?.end()
}

export function decodeAccountBalanceJobsOutcome(value: unknown, storageObservedAt: string): AccountBalanceJobsOutcome {
  const record = object(value, 'J2 outcome')
  exact(record, ['outcome_id', 'request_id', 'account_id', 'system_account_id', 'input_version', 'config_revision', 'trigger', 'observed_at', 'snapshot', 'adapter', 'next_refresh_at', 'expected_next_refresh_at', 'expected_input', 'expected_config', 'error_code', 'error_message'])
  const outcomeId = text(record.outcome_id, 'outcome_id')
  const result: AccountBalanceJobsOutcome = {
    outcomeId,
    requestId: text(record.request_id, 'request_id'),
    accountId: text(record.account_id, 'account_id'),
    systemAccountId: text(record.system_account_id, 'system_account_id'),
    inputVersion: positive(record.input_version, 'input_version'),
    configRevision: positive(record.config_revision, 'config_revision'),
    trigger: enumValue(record.trigger, 'trigger', ['periodic', 'first_probe', 'manual'] as const),
    observedAt: requiredRfc3339Instant(record.observed_at, 'observed_at'),
    snapshot: decodeSnapshot(record.snapshot),
    nextRefreshAt: record.next_refresh_at === undefined || record.next_refresh_at === null ? null : requiredRfc3339Instant(record.next_refresh_at, 'next_refresh_at'),
    // This value is also the durable source cursor. Keep PostgreSQL's
    // microsecond fraction: reducing it to milliseconds can reorder outcomes
    // written in the same millisecond and make the monotonic cursor stall.
    storageObservedAt: requiredStorageCursorInstant(storageObservedAt)
  }
  if (record.adapter !== undefined) result.adapter = enumValue(record.adapter, 'adapter', ['custom', 'sub2api', 'newapi', 'openai_billing', 'litellm', 'user_balance'] as const)
  if (record.expected_next_refresh_at !== undefined) result.expectedNextRefreshAt = record.expected_next_refresh_at === null ? null : requiredRfc3339Instant(record.expected_next_refresh_at, 'expected_next_refresh_at')
  if (record.expected_input !== undefined) result.expectedInput = positive(record.expected_input, 'expected_input')
  if (record.expected_config !== undefined) result.expectedConfig = positive(record.expected_config, 'expected_config')
  if (record.error_code !== undefined) result.errorCode = text(record.error_code, 'error_code')
  if (record.error_message !== undefined) result.errorMessage = text(record.error_message, 'error_message')
  if (result.accountId !== text(record.account_id, 'account_id')) throw new Error('J2 outcome account fence 无效')
  return result
}

function requiredStorageCursorInstant(value: unknown): string {
  if (typeof value !== 'string' || !parseRfc3339Instant(value.trim())) {
    throw new Error('storage_observed_at必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return value.trim()
}

export async function listAccountBalanceJobsOutcomes(source: AccountBalanceJobsStoreSource, options: { after?: AccountBalanceJobsOutcomeCursor; limit: number }): Promise<AccountBalanceJobsOutcome[]> {
  if (source.mode === 'sqlite') return readSqliteOutcomes(source.databasePath, options.after, options.limit)
  if (!Number.isInteger(options.limit) || options.limit < 1 || options.limit > 1_000) throw new Error('J2 outcome limit 必须在 1..1000')
  const pool = await postgresOutcomePool(source)
  let connection: PoolClient | undefined
  let failed = false
  let resetPool = false
  try {
    const acquired = await pool.connect()
    connection = acquired
    await verifyPostgresOutcomeReaderRole(source, acquired)
    await acquired.query('BEGIN READ ONLY')
    await acquired.query('SET LOCAL statement_timeout = 5000')
    const after = options.after
    const rows = after
      ? await acquired.query('SELECT outcome_id,account_id,input_version,config_revision,trigger,payload,to_char(observed_at::timestamptz AT TIME ZONE \'UTC\', \'YYYY-MM-DD"T"HH24:MI:SS.US"Z"\') AS storage_observed_at FROM juhe_jobs.account_balance_outcomes WHERE committed=TRUE AND (observed_at::timestamptz > $1::timestamptz OR (observed_at::timestamptz = $1::timestamptz AND outcome_id > $2)) ORDER BY observed_at::timestamptz ASC,outcome_id ASC LIMIT $3', [after.observedAt, after.outcomeId, options.limit])
      : await acquired.query('SELECT outcome_id,account_id,input_version,config_revision,trigger,payload,to_char(observed_at::timestamptz AT TIME ZONE \'UTC\', \'YYYY-MM-DD"T"HH24:MI:SS.US"Z"\') AS storage_observed_at FROM juhe_jobs.account_balance_outcomes WHERE committed=TRUE ORDER BY observed_at::timestamptz ASC,outcome_id ASC LIMIT $1', [options.limit])
    await acquired.query('COMMIT')
    return rows.rows.map((row: any) => decodeOutcomeRow(row))
  } catch (error) {
    failed = true
    resetPool = true
    await connection?.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection?.release(failed ? new Error('J2 outcome reader connection failed') : undefined)
    if (resetPool) await closeAccountBalanceJobsStoreSource(source).catch(() => undefined)
  }
}

async function postgresOutcomePool(source: AccountBalanceJobsPostgresStoreSource): Promise<AccountBalanceJobsPostgresOutcomePool> {
  if (source.pool) return source.pool
  const { Pool } = await import('pg')
  const pool = new Pool({
    connectionString: source.postgresUrl,
    max: 1,
    idleTimeoutMillis: postgresOutcomeReaderIdleTimeoutMs,
    maxLifetimeSeconds: postgresOutcomeReaderMaxLifetimeSeconds,
    connectionTimeoutMillis: 5_000,
    query_timeout: 5_000,
    application_name: 'juhe-ai:db-service:j2-outcome-reader'
  }) as unknown as AccountBalanceJobsPostgresOutcomePool
  source.pool = pool
  return pool
}

async function verifyPostgresOutcomeReaderRole(source: AccountBalanceJobsPostgresStoreSource, connection: PoolClient): Promise<void> {
  if (source.readerRoleVerified) return
  const result = await connection.query('SELECT current_user AS current_user')
  const currentUser = result.rows[0]?.current_user
  if (currentUser !== postgresOutcomeReaderRole) {
    throw new Error(`J2 outcome reader 必须使用专用数据库角色 ${postgresOutcomeReaderRole}`)
  }
  source.readerRoleVerified = true
}

function readSqliteOutcomes(path: string, after: AccountBalanceJobsOutcomeCursor | undefined, limit: number): AccountBalanceJobsOutcome[] {
  if (!path.trim()) throw new Error('J2 SQLite outcome path 不能为空')
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => {
    exec(sql: string): void
    prepare(sql: string): { all(...values: unknown[]): unknown[]; get(...values: unknown[]): unknown }
    close(): void
  }
  const database = new Constructor(resolve(path), { readOnly: true })
  try {
    database.exec('PRAGMA query_only = ON')
    const state = database.prepare('PRAGMA query_only').all()[0] as Record<string, unknown> | undefined
    if (Number(state?.query_only ?? state?.[0] ?? 0) !== 1) throw new Error('J2 SQLite outcome 读取未进入 query_only')
    const rows = after
      ? database.prepare('SELECT outcome_id,account_id,input_version,config_revision,trigger,payload,observed_at AS storage_observed_at FROM account_balance_outcomes WHERE committed=1 AND (observed_at > ? OR (observed_at = ? AND outcome_id > ?)) ORDER BY observed_at ASC, outcome_id ASC LIMIT ?').all(after.observedAt, after.observedAt, after.outcomeId, limit)
      : database.prepare('SELECT outcome_id,account_id,input_version,config_revision,trigger,payload,observed_at AS storage_observed_at FROM account_balance_outcomes WHERE committed=1 ORDER BY observed_at ASC, outcome_id ASC LIMIT ?').all(limit)
    return rows.map((row) => decodeOutcomeRow(row))
  } finally {
    database.close()
  }
}

function decodeOutcomeRow(row: unknown): AccountBalanceJobsOutcome {
  if (!row || typeof row !== 'object') throw new Error('J2 jobs outcome 行格式无效')
  const record = row as Record<string, unknown>
  const storageObservedAt = record.storage_observed_at
  if (typeof storageObservedAt !== 'string' || !storageObservedAt.trim()) throw new Error('J2 jobs outcome storage observed_at 无效')
  const payload = record.payload && typeof record.payload === 'object'
    ? record.payload
    : typeof record.payload === 'string'
      ? JSON.parse(record.payload) as unknown
      : undefined
  const outcome = decodeAccountBalanceJobsOutcome(payload, storageObservedAt.trim())
  if (record.outcome_id !== outcome.outcomeId || record.account_id !== outcome.accountId || Number(record.input_version) !== outcome.inputVersion || Number(record.config_revision) !== outcome.configRevision || record.trigger !== outcome.trigger) {
    throw new Error('J2 outcome 行元数据与 payload 不一致')
  }
  return outcome
}

function parsePayload(value: unknown): unknown {
  if (value && typeof value === 'object') return value
  if (typeof value !== 'string') throw new Error('J2 outcome payload 无效')
  try { return JSON.parse(value) as unknown } catch { throw new Error('J2 outcome payload JSON 无效') }
}
function decodeSnapshot(value: unknown): Record<string, unknown> {
  const snapshot = object(value, 'snapshot')
  exact(snapshot, ['status', 'remainingUsd', 'rawRemaining', 'rawUnit', 'basis', 'errorMessage', 'lastAttemptAt', 'lastSuccessAt', 'consecutiveTransientFailures', 'lastTransientErrorMessage', 'lastTransientFailureAt'])
  return snapshot
}
function exact(value: Record<string, unknown>, allowed: string[]): void { if (Object.keys(value).some((key) => !allowed.includes(key))) throw new Error('J2 outcome 包含未知字段') }
function object(value: unknown, label: string): Record<string, unknown> { if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} 必须是对象`); return value as Record<string, unknown> }
function text(value: unknown, label: string): string { if (typeof value !== 'string' || !value.trim() || value.length > 4096) throw new Error(`${label} 无效`); return value.trim() }
function positive(value: unknown, label: string): number { if (!Number.isSafeInteger(value) || (value as number) < 1) throw new Error(`${label} 无效`); return value as number }
function enumValue<T extends readonly string[]>(value: unknown, label: string, allowed: T): T[number] { if (typeof value !== 'string' || !allowed.includes(value)) throw new Error(`${label} 无效`); return value as T[number] }
