import { createRequire } from 'node:module'
import { resolve } from 'node:path'

export type AccountHealthJobsStoreSource =
  | { mode: 'sqlite'; databasePath: string }
  | { mode: 'postgres'; postgresUrl: string }

export interface AccountHealthJobsProjection {
  target_account_id: string
  transition_kind: string
  input_version: number
  config_revision: number
  dispatch_revision: number
  source_config_revision?: number
  expected_account_status: 'active' | 'pending_test' | 'temporary_unavailable' | 'rate_limited'
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
  projection?: AccountHealthJobsProjection
}

export interface AccountHealthJobsOutcomeCursor {
  observedAt: string
  outcomeId: string
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
  return await readPostgresOutcomes(source.postgresUrl, after, limit)
}

function readSqliteOutcomes(path: string, after: AccountHealthJobsOutcomeCursor | undefined, limit: number): AccountHealthJobsOutcome[] {
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => {
    exec(sql: string): void
    prepare(sql: string): { all(...values: unknown[]): unknown[] }
    close(): void
  }
  const database = new Constructor(resolve(path), { readOnly: true })
  try {
    database.exec('PRAGMA query_only = ON')
    const state = database.prepare('PRAGMA query_only').all()[0] as Record<string, unknown> | undefined
    if (Number(state?.query_only ?? state?.[0] ?? 0) !== 1) throw new Error('J1 jobs SQLite outcome 读取未进入 query_only')
    const rows = after
      ? database.prepare(`SELECT payload FROM account_health_outcomes WHERE observed_at > ? OR (observed_at = ? AND outcome_id > ?) ORDER BY observed_at ASC, outcome_id ASC LIMIT ?`).all(after.observedAt, after.observedAt, after.outcomeId, limit)
      : database.prepare(`SELECT payload FROM account_health_outcomes ORDER BY observed_at ASC, outcome_id ASC LIMIT ?`).all(limit)
    return rows.map((row) => decodeAccountHealthJobsOutcomePayload(rowPayload(row)))
  } finally {
    database.close()
  }
}

async function readPostgresOutcomes(postgresUrl: string, after: AccountHealthJobsOutcomeCursor | undefined, limit: number): Promise<AccountHealthJobsOutcome[]> {
  const { Pool } = await import('pg')
  const pool = new Pool({ connectionString: postgresUrl, max: 1 })
  const connection = await pool.connect()
  let inTransaction = false
  try {
    await connection.query('BEGIN READ ONLY')
    inTransaction = true
    const result = after
      ? await connection.query('SELECT payload FROM juhe_jobs.account_health_outcomes WHERE observed_at > $1 OR (observed_at = $1 AND outcome_id > $2) ORDER BY observed_at ASC, outcome_id ASC LIMIT $3', [after.observedAt, after.outcomeId, limit])
      : await connection.query('SELECT payload FROM juhe_jobs.account_health_outcomes ORDER BY observed_at ASC, outcome_id ASC LIMIT $1', [limit])
    await connection.query('COMMIT')
    inTransaction = false
    return result.rows.map((row: Record<string, unknown>) => decodeAccountHealthJobsOutcomePayload(row.payload))
  } catch (error) {
    if (inTransaction) await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
    await pool.end()
  }
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
    expected_account_status: requiredEnum(record.expected_account_status, 'projection.expected_account_status', ['active', 'pending_test', 'temporary_unavailable', 'rate_limited'] as const)
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
  if (!Number.isFinite(Date.parse(text))) throw new Error(`${field} 必须是 ISO 时间`)
  return text
}
