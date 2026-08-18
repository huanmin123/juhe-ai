import { parseRfc3339Instant } from '../shared/rfc3339.js'
import { nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'

export interface AccountBalanceProjectionCursor { observedAt: string; outcomeId: string }
type Row = { observed_at: string | null; outcome_id: string | null }

export async function currentAccountBalanceProjectionCursorAsync(client: DatabaseClient, consumerKey: string): Promise<AccountBalanceProjectionCursor | undefined> {
  return cursor(await client.one<Row>(`SELECT observed_at,outcome_id FROM ${table(client)} WHERE consumer_key=?`, [key(consumerKey)]))
}

export async function advanceAccountBalanceProjectionCursorAsync(client: DatabaseClient, consumerKey: string, next: AccountBalanceProjectionCursor): Promise<boolean> {
  const consumer = key(consumerKey); const target = normalize(next)
  return await client.transaction(async (tx) => {
    const existing = cursor(await tx.one<Row>(`SELECT observed_at,outcome_id FROM ${table(tx)} WHERE consumer_key=?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}`, [consumer]))
    if (existing && compare(existing, target) >= 0) {
      if (existing.outcomeId !== target.outcomeId) return false
      return (await tx.execute(`UPDATE ${table(tx)} SET observed_at=?,updated_at=? WHERE consumer_key=? AND outcome_id=?`, [target.observedAt, nowIso(), consumer, existing.outcomeId])).changes === 1
    }
    if (existing) return (await tx.execute(`UPDATE ${table(tx)} SET observed_at=?,outcome_id=?,updated_at=? WHERE consumer_key=?`, [target.observedAt, target.outcomeId, nowIso(), consumer])).changes === 1
    await tx.execute(`INSERT INTO ${table(tx)}(consumer_key,observed_at,outcome_id,updated_at) VALUES(?,?,?,?)`, [consumer, target.observedAt, target.outcomeId, nowIso()])
    return true
  })
}

function cursor(row: Row | undefined): AccountBalanceProjectionCursor | undefined {
  if (!row || (row.observed_at === null && row.outcome_id === null)) return undefined
  if (row.observed_at === null || row.outcome_id === null) throw new Error('J2 projection cursor 存储损坏')
  return normalize({ observedAt: row.observed_at, outcomeId: row.outcome_id })
}
function normalize(value: AccountBalanceProjectionCursor): AccountBalanceProjectionCursor {
  if (!parseRfc3339Instant(value.observedAt)) throw new Error('J2 projection cursor observedAt 无效')
  const outcomeId = value.outcomeId.trim(); if (!outcomeId || outcomeId.length > 4096) throw new Error('J2 projection cursor outcomeId 无效')
  return { observedAt: value.observedAt.trim(), outcomeId }
}
function compare(left: AccountBalanceProjectionCursor, right: AccountBalanceProjectionCursor): number { const a = instant(left.observedAt); const b = instant(right.observedAt); if (a < b) return -1; if (a > b) return 1; return left.outcomeId.localeCompare(right.outcomeId) }
function instant(value: string): bigint { const parsed = parseRfc3339Instant(value); const match = /\.(\d{1,9})(?:Z|[+-]\d{2}:\d{2})$/.exec(value); if (!parsed) throw new Error('J2 projection cursor observedAt 无效'); return BigInt(parsed.getTime()) * 1_000_000n + BigInt((match?.[1] ?? '').padEnd(9, '0').slice(3).padEnd(6, '0')) }
function key(value: string): string { const result = value.trim(); if (!result || result.length > 200) throw new Error('J2 projection consumer key 无效'); return result }
function table(client: DatabaseClient): string { return client.dialect.qualifyTable('juhe_business', 'account_balance_projection_cursors') }
