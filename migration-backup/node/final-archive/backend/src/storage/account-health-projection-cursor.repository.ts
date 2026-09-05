import type { DatabaseSync } from 'node:sqlite'

import { parseRfc3339Instant } from '../shared/rfc3339.js'
import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'

export interface AccountHealthProjectionCursor {
  observedAt: string
  outcomeId: string
}

interface CursorRow {
  observed_at: string | null
  outcome_id: string | null
}

export function currentAccountHealthProjectionCursor(
  consumerKey: string,
  database: DatabaseSync = getBusinessDatabase()
): AccountHealthProjectionCursor | undefined {
  return cursorFromRow(database.prepare(`SELECT observed_at, outcome_id FROM account_health_projection_cursors WHERE consumer_key = ?`).get(requiredConsumerKey(consumerKey)) as CursorRow | undefined)
}

export async function currentAccountHealthProjectionCursorAsync(
  client: DatabaseClient,
  consumerKey: string
): Promise<AccountHealthProjectionCursor | undefined> {
  const row = await client.one<CursorRow>(`SELECT observed_at, outcome_id FROM ${table(client)} WHERE consumer_key = ?`, [requiredConsumerKey(consumerKey)])
  return cursorFromRow(row)
}

// Advancing is monotonic across outcome IDs. A legacy cursor may carry a
// higher payload precision than the durable store; the same immutable outcome
// ID is allowed to normalize that representation once.
export function advanceAccountHealthProjectionCursor(
  consumerKey: string,
  next: AccountHealthProjectionCursor,
  database: DatabaseSync = getBusinessDatabase()
): boolean {
  const key = requiredConsumerKey(consumerKey)
  const cursor = normalizedCursor(next)
  return runInDatabaseTransaction(() => advanceSqlite(database, key, cursor), database)
}

export async function advanceAccountHealthProjectionCursorAsync(
  client: DatabaseClient,
  consumerKey: string,
  next: AccountHealthProjectionCursor
): Promise<boolean> {
  const key = requiredConsumerKey(consumerKey)
  const cursor = normalizedCursor(next)
  return await client.transaction(async (tx) => {
    const current = await tx.one<CursorRow>(`SELECT observed_at, outcome_id FROM ${table(tx)} WHERE consumer_key = ? FOR UPDATE`, [key])
    const existing = cursorFromRow(current)
    if (existing && compareCursor(existing, cursor) >= 0) {
      if (existing.outcomeId !== cursor.outcomeId) return false
      if (existing.observedAt === cursor.observedAt) return false
      const result = await tx.execute(`UPDATE ${table(tx)} SET observed_at = ?, updated_at = ? WHERE consumer_key = ? AND outcome_id = ?`, [cursor.observedAt, nowIso(), key, existing.outcomeId])
      return result.changes === 1
    }
    if (existing) {
      const result = await tx.execute(`UPDATE ${table(tx)} SET observed_at = ?, outcome_id = ?, updated_at = ? WHERE consumer_key = ?`, [cursor.observedAt, cursor.outcomeId, nowIso(), key])
      return result.changes === 1
    }
    await tx.execute(`INSERT INTO ${table(tx)}(consumer_key, observed_at, outcome_id, updated_at) VALUES (?, ?, ?, ?)`, [key, cursor.observedAt, cursor.outcomeId, nowIso()])
    return true
  })
}

function advanceSqlite(database: DatabaseSync, consumerKey: string, next: AccountHealthProjectionCursor): boolean {
  const existing = currentAccountHealthProjectionCursor(consumerKey, database)
  if (existing && compareCursor(existing, next) >= 0) {
    if (existing.outcomeId !== next.outcomeId) return false
    if (existing.observedAt === next.observedAt) return false
    const result = database.prepare(`UPDATE account_health_projection_cursors SET observed_at = ?, updated_at = ? WHERE consumer_key = ? AND outcome_id = ?`).run(next.observedAt, nowIso(), consumerKey, existing.outcomeId)
    return Number(result.changes ?? 0) === 1
  }
  if (existing) {
    const result = database.prepare(`UPDATE account_health_projection_cursors SET observed_at = ?, outcome_id = ?, updated_at = ? WHERE consumer_key = ?`).run(next.observedAt, next.outcomeId, nowIso(), consumerKey)
    return Number(result.changes ?? 0) === 1
  }
  database.prepare(`INSERT INTO account_health_projection_cursors(consumer_key, observed_at, outcome_id, updated_at) VALUES (?, ?, ?, ?)`).run(consumerKey, next.observedAt, next.outcomeId, nowIso())
  return true
}

function cursorFromRow(row: CursorRow | undefined): AccountHealthProjectionCursor | undefined {
  if (!row) return undefined
  if (row.observed_at === null && row.outcome_id === null) return undefined
  if (row.observed_at === null || row.outcome_id === null) throw new Error('J1 projection cursor 存储损坏')
  return normalizedCursor({ observedAt: row.observed_at, outcomeId: row.outcome_id })
}

function normalizedCursor(value: AccountHealthProjectionCursor): AccountHealthProjectionCursor {
  const observedAt = value.observedAt.trim()
  if (!parseRfc3339Instant(observedAt)) throw new Error('J1 projection cursor observedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  const outcomeId = value.outcomeId.trim()
  if (!outcomeId || outcomeId.length > 4_096) throw new Error('J1 projection cursor outcomeId 无效')
  return { observedAt, outcomeId }
}

function compareCursor(left: AccountHealthProjectionCursor, right: AccountHealthProjectionCursor): number {
  const leftObservedAt = instantNanoseconds(left.observedAt)
  const rightObservedAt = instantNanoseconds(right.observedAt)
  if (leftObservedAt === undefined || rightObservedAt === undefined) throw new Error('J1 projection cursor observedAt 无效')
  if (leftObservedAt < rightObservedAt) return -1
  if (leftObservedAt > rightObservedAt) return 1
  if (left.outcomeId < right.outcomeId) return -1
  if (left.outcomeId > right.outcomeId) return 1
  return 0
}

function instantNanoseconds(value: string): bigint | undefined {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(value)
  const parsed = parseRfc3339Instant(value)
  if (!match || !parsed) return undefined
  const fraction = (match[2] ?? '').padEnd(9, '0')
  return BigInt(parsed.getTime()) * 1_000_000n + BigInt(fraction.slice(3).padEnd(6, '0'))
}

function requiredConsumerKey(value: string): string {
  const key = value.trim()
  if (!key || key.length > 200) throw new Error('J1 projection consumer key 无效')
  return key
}

function table(client: DatabaseClient): string {
  return client.dialect.qualifyTable('juhe_business', 'account_health_projection_cursors')
}
