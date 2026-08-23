import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import type { PoolClient } from 'pg'
import { parseRfc3339Instant } from '../shared/rfc3339.js'

export type ProxyLatencyJobsStoreSource =
  | { mode: 'sqlite'; databasePath: string }
  | { mode: 'postgres'; postgresUrl: string }

export interface ProxyLatencyJobsOutcomeCursor {
  storedAt: string
  outcomeId: string
}

export type ProxyLatencyItemStatus = 'passed' | 'warning' | 'failed' | 'unknown'
export type ProxyLatencyOverallStatus = 'passed' | 'warning' | 'failed' | 'unknown'

export interface ProxyLatencyJobsOutcomeItem {
  provider: string
  profileId: string
  status: ProxyLatencyItemStatus
  outcome: string
  httpStatus?: number
  latencyMs?: number
  errorCode?: string
}

export interface ProxyLatencyJobsOutcome {
  outcomeId: string
  requestId: string
  proxyId: string
  observedAt: string
  inputVersion: number
  configRevision: string
  trigger: 'periodic' | 'manual'
  ownerFenceToken: number
  proxyFenceToken: number
  overallStatus: ProxyLatencyOverallStatus
  items: ProxyLatencyJobsOutcomeItem[]
  storageObservedAt: string
}

const outcomeKeys = [
  'outcome_id', 'request_id', 'proxy_id', 'observed_at', 'input_version', 'config_revision',
  'trigger', 'owner_fence_token', 'proxy_fence_token', 'overall_status', 'items'
]
const itemKeys = ['provider', 'profile_id', 'status', 'outcome', 'http_status', 'latency_ms', 'error_code']

export async function listProxyLatencyJobsOutcomes(
  source: ProxyLatencyJobsStoreSource,
  options: { after?: ProxyLatencyJobsOutcomeCursor; limit: number }
): Promise<ProxyLatencyJobsOutcome[]> {
  if (!Number.isSafeInteger(options.limit) || options.limit < 1 || options.limit > 1_000) {
    throw new Error('J3a outcome limit 必须在 1..1000')
  }
  if (source.mode === 'sqlite') return readSqlite(source.databasePath, options.after, options.limit)
  const { Pool } = await import('pg')
  const pool = new Pool({ connectionString: source.postgresUrl, max: 1, connectionTimeoutMillis: 5_000, query_timeout: 5_000 })
  let connection: PoolClient | undefined
  try {
    connection = await pool.connect()
    await connection.query('BEGIN READ ONLY')
    await connection.query('SET LOCAL statement_timeout = 5000')
    const rows = options.after
      ? await connection.query(`
        SELECT outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,
          to_char(observed_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS observed_at,payload,
          to_char(stored_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at
        FROM juhe_jobs.proxy_latency_outcomes
        WHERE committed=TRUE
          AND (stored_at::timestamptz > $1::timestamptz OR (stored_at::timestamptz = $1::timestamptz AND outcome_id > $2))
        ORDER BY stored_at ASC,outcome_id ASC LIMIT $3
      `, [options.after.storedAt, options.after.outcomeId, options.limit])
      : await connection.query(`
        SELECT outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,
          to_char(observed_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS observed_at,payload,
          to_char(stored_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS storage_observed_at
        FROM juhe_jobs.proxy_latency_outcomes
        WHERE committed=TRUE
        ORDER BY stored_at ASC,outcome_id ASC LIMIT $1
      `, [options.limit])
    await connection.query('COMMIT')
    return rows.rows.map(decodeRow)
  } catch (error) {
    await connection?.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection?.release()
    await pool.end()
  }
}

function readSqlite(path: string, after: ProxyLatencyJobsOutcomeCursor | undefined, limit: number): ProxyLatencyJobsOutcome[] {
  if (!path.trim()) throw new Error('J3a SQLite outcome path 不能为空')
  const require = createRequire(import.meta.url)
  const Constructor = require('node:sqlite').DatabaseSync as new (path: string, options?: { readOnly?: boolean }) => {
    exec(sql: string): void
    prepare(sql: string): { all(...values: unknown[]): unknown[] }
    close(): void
  }
  const database = new Constructor(resolve(path), { readOnly: true })
  try {
    database.exec('PRAGMA query_only = ON')
    const rows = after
      ? database.prepare(`SELECT outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,payload,stored_at AS storage_observed_at FROM proxy_latency_outcomes WHERE committed=1 AND (stored_at > ? OR (stored_at = ? AND outcome_id > ?)) ORDER BY stored_at ASC,outcome_id ASC LIMIT ?`).all(after.storedAt, after.storedAt, after.outcomeId, limit)
      : database.prepare(`SELECT outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,payload,stored_at AS storage_observed_at FROM proxy_latency_outcomes WHERE committed=1 ORDER BY stored_at ASC,outcome_id ASC LIMIT ?`).all(limit)
    return rows.map(decodeRow)
  } finally {
    database.close()
  }
}

function decodeRow(row: unknown): ProxyLatencyJobsOutcome {
  if (!row || typeof row !== 'object') throw new Error('J3a jobs outcome 行格式无效')
  const record = row as Record<string, unknown>
  const storageObservedAt = requiredStorageInstant(record.storage_observed_at)
  const payload = parsePayload(record.payload)
  const outcome = decodeOutcome(payload, storageObservedAt)
  if (record.outcome_id !== outcome.outcomeId
    || record.request_id !== outcome.requestId
    || record.proxy_id !== outcome.proxyId
    || Number(record.input_version) !== outcome.inputVersion
    || String(record.config_revision) !== outcome.configRevision
    || record.trigger !== outcome.trigger
    || Number(record.owner_fence_token) !== outcome.ownerFenceToken
    || Number(record.proxy_fence_token) !== outcome.proxyFenceToken
    || !samePreciseInstant(record.observed_at, outcome.observedAt)) {
    throw new Error('J3a outcome 行元数据与 payload 不一致')
  }
  return outcome
}

export function decodeProxyLatencyJobsOutcome(value: unknown, storageObservedAt: string): ProxyLatencyJobsOutcome {
  return decodeOutcome(value, storageObservedAt)
}

function decodeOutcome(value: unknown, storageObservedAt: string): ProxyLatencyJobsOutcome {
  const record = object(value, 'J3a outcome')
  exact(record, outcomeKeys, 'J3a outcome')
  const outcomeId = text(record.outcome_id, 'outcome_id')
  return {
    outcomeId,
    requestId: text(record.request_id, 'request_id'),
    proxyId: text(record.proxy_id, 'proxy_id'),
    observedAt: requiredPreciseRfc3339Instant(record.observed_at, 'observed_at'),
    inputVersion: positive(record.input_version, 'input_version'),
    configRevision: requiredPreciseRfc3339Instant(record.config_revision, 'config_revision'),
    trigger: enumValue(record.trigger, 'trigger', ['periodic', 'manual'] as const),
    ownerFenceToken: positive(record.owner_fence_token, 'owner_fence_token'),
    proxyFenceToken: positive(record.proxy_fence_token, 'proxy_fence_token'),
    overallStatus: enumValue(record.overall_status, 'overall_status', ['passed', 'warning', 'failed', 'unknown'] as const),
    items: decodeItems(record.items),
    storageObservedAt: requiredStorageInstant(storageObservedAt)
  }
}

function decodeItems(value: unknown): ProxyLatencyJobsOutcomeItem[] {
  if (!Array.isArray(value) || value.length > 1_000) throw new Error('J3a outcome items 无效')
  return value.map((item, index) => {
    const record = object(item, `J3a outcome item[${index}]`)
    exact(record, itemKeys, 'J3a outcome item')
    const result: ProxyLatencyJobsOutcomeItem = {
      provider: text(record.provider, 'item.provider'),
      profileId: text(record.profile_id, 'item.profile_id'),
      status: enumValue(record.status, 'item.status', ['passed', 'warning', 'failed', 'unknown'] as const),
      outcome: text(record.outcome, 'item.outcome')
    }
    if (record.http_status !== undefined) result.httpStatus = integer(record.http_status, 'item.http_status', 100, 599)
    if (record.latency_ms !== undefined) result.latencyMs = integer(record.latency_ms, 'item.latency_ms', 0, 86_400_000)
    if (record.error_code !== undefined) result.errorCode = text(record.error_code, 'item.error_code')
    return result
  })
}

function parsePayload(value: unknown): unknown {
  if (value && typeof value === 'object') return value
  if (typeof value !== 'string') throw new Error('J3a outcome payload 无效')
  try { return JSON.parse(value) as unknown } catch { throw new Error('J3a outcome payload JSON 无效') }
}
function object(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} 必须是对象`)
  return value as Record<string, unknown>
}
function exact(value: Record<string, unknown>, allowed: string[], label: string): void {
  if (Object.keys(value).some((key) => !allowed.includes(key))) throw new Error(`${label} 包含未知字段`)
}
function text(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim() || value.length > 4096) throw new Error(`${label} 无效`)
  return value.trim()
}
function positive(value: unknown, label: string): number {
  if (!Number.isSafeInteger(value) || (value as number) < 1) throw new Error(`${label} 无效`)
  return value as number
}
function integer(value: unknown, label: string, minimum: number, maximum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) throw new Error(`${label} 无效`)
  return value as number
}
function enumValue<T extends readonly string[]>(value: unknown, label: string, allowed: T): T[number] {
  if (typeof value !== 'string' || !allowed.includes(value)) throw new Error(`${label} 无效`)
  return value as T[number]
}
function requiredStorageInstant(value: unknown): string {
  if (typeof value !== 'string' || !value.trim() || !parseRfc3339Instant(value.trim())) throw new Error('J3a stored_at 必须是 RFC3339 时间')
  return value.trim()
}

/** Preserve the sub-millisecond fence used by PostgreSQL timestamps. */
function requiredPreciseRfc3339Instant(value: unknown, label: string): string {
  if (typeof value !== 'string') throw new Error(`${label} 无效`)
  const text = value.trim()
  if (!parseRfc3339Instant(text)) throw new Error(`${label} 必须是 RFC3339 时间`)
  return text
}

function samePreciseInstant(left: unknown, right: string): boolean {
  if (typeof left !== 'string') return false
  const leftText = left.trim()
  const rightText = right.trim()
  if (!parseRfc3339Instant(leftText) || !parseRfc3339Instant(rightText)) return false
  const leftMatch = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/u.exec(leftText)
  const rightMatch = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/u.exec(rightText)
  if (!leftMatch || !rightMatch) return false
  const leftSeconds = BigInt(Math.trunc(Date.parse(leftText) / 1000))
  const rightSeconds = BigInt(Math.trunc(Date.parse(rightText) / 1000))
  return leftSeconds * 1_000_000_000n + BigInt((leftMatch[2] ?? '').padEnd(9, '0'))
    === rightSeconds * 1_000_000_000n + BigInt((rightMatch[2] ?? '').padEnd(9, '0'))
}
