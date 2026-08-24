import { parseRfc3339Instant } from '../shared/rfc3339.js'
import { nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'

export interface ProxyLatencyProjectionCursor {
  storedAt: string
  outcomeId: string
}

type Row = { stored_at: string | null; outcome_id: string | null }

export async function currentProxyLatencyProjectionCursorAsync(client: DatabaseClient, consumerKey: string): Promise<ProxyLatencyProjectionCursor | undefined> {
  return cursor(await client.one<Row>(`SELECT ${storedAtSql(client)} AS stored_at,outcome_id FROM ${table(client)} WHERE consumer_key=?`, [key(consumerKey)]))
}

export async function advanceProxyLatencyProjectionCursorAsync(client: DatabaseClient, consumerKey: string, next: ProxyLatencyProjectionCursor): Promise<boolean> {
  const consumer = key(consumerKey)
  const target = normalize(next)
  return client.transaction(async (tx) => {
    const existing = cursor(await tx.one<Row>(`SELECT ${storedAtSql(tx)} AS stored_at,outcome_id FROM ${table(tx)} WHERE consumer_key=?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}`, [consumer]))
    if (existing && compare(existing, target) >= 0) {
      if (existing.outcomeId !== target.outcomeId) return false
      return (await tx.execute(`UPDATE ${table(tx)} SET stored_at=?,updated_at=? WHERE consumer_key=? AND outcome_id=?`, [target.storedAt, nowIso(), consumer, existing.outcomeId])).changes === 1
    }
    if (existing) return (await tx.execute(`UPDATE ${table(tx)} SET stored_at=?,outcome_id=?,updated_at=? WHERE consumer_key=?`, [target.storedAt, target.outcomeId, nowIso(), consumer])).changes === 1
    await tx.execute(`INSERT INTO ${table(tx)}(consumer_key,stored_at,outcome_id,updated_at) VALUES(?,?,?,?)`, [consumer, target.storedAt, target.outcomeId, nowIso()])
    return true
  })
}

function cursor(row: Row | undefined): ProxyLatencyProjectionCursor | undefined {
  if (!row || (row.stored_at === null && row.outcome_id === null)) return undefined
  if (row.stored_at === null || row.outcome_id === null) throw new Error('J3a projection cursor 存储损坏')
  return normalize({ storedAt: row.stored_at, outcomeId: row.outcome_id })
}
function normalize(value: ProxyLatencyProjectionCursor): ProxyLatencyProjectionCursor {
  if (!parseRfc3339Instant(value.storedAt)) throw new Error('J3a projection cursor storedAt 无效')
  const outcomeId = value.outcomeId.trim()
  if (!outcomeId || outcomeId.length > 4096) throw new Error('J3a projection cursor outcomeId 无效')
  return { storedAt: value.storedAt.trim(), outcomeId }
}
function compare(left: ProxyLatencyProjectionCursor, right: ProxyLatencyProjectionCursor): number {
  const a = instant(left.storedAt)
  const b = instant(right.storedAt)
  if (a < b) return -1
  if (a > b) return 1
  return left.outcomeId.localeCompare(right.outcomeId)
}
function instant(value: string): bigint {
  const parsed = parseRfc3339Instant(value)
  if (!parsed) throw new Error('J3a projection cursor storedAt 无效')
  const match = /\.(\d{1,9})(?:Z|[+-]\d{2}:\d{2})$/.exec(value)
  return BigInt(parsed.getTime()) * 1_000_000n + BigInt((match?.[1] ?? '').padEnd(9, '0').slice(3).padEnd(6, '0'))
}
function key(value: string): string {
  const result = value.trim()
  if (!result || result.length > 200) throw new Error('J3a projection consumer key 无效')
  return result
}
function table(client: DatabaseClient): string { return client.dialect.qualifyTable('juhe_business', 'proxy_latency_projection_cursors') }
function storedAtSql(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? `to_char(stored_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
    : 'stored_at'
}
