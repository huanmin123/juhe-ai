import { randomUUID } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import {
  reserveAccountHealthJobsInputVersionInTransaction,
  reserveAccountHealthJobsInputVersionInTransactionAsync
} from './account-health-jobs-input-version.repository.js'

export type AccountHealthJobsInputOutboxKind = 'snapshot' | 'tombstone'

export interface AccountHealthJobsInputOutboxIntent {
  accountId: string
  configRevision: number
  dispatchRevision: number
  kind: AccountHealthJobsInputOutboxKind
  reason: string
}

export interface ReservedAccountHealthJobsInputIntent {
  eventId: string
  accountId: string
  inputVersion: number
  kind: AccountHealthJobsInputOutboxKind
}

export interface AccountHealthJobsInputOutboxEvent extends ReservedAccountHealthJobsInputIntent {
  configRevision: number
  dispatchRevision: number
  reason: string
  attemptCount: number
  claimToken: string
  claimedUntil: string
}

type OutboxRow = {
  event_id: string
  account_id: string
  input_version: number | bigint | string
  event_kind: string
  reason: string
  config_revision: number | bigint | string
  dispatch_revision: number | bigint | string
  attempt_count: number | bigint | string
}

// This function is intentionally transaction-only. The caller must use the
// business DB-service transaction that changes configuration/authorization/
// proxy state, so an epoch can never be reserved without a durable replayable
// publication intent.
export function reserveAndEnqueueAccountHealthJobsInputInTransaction(
  intent: AccountHealthJobsInputOutboxIntent,
  database: DatabaseSync = getBusinessDatabase(),
  createdAt = nowIso()
): ReservedAccountHealthJobsInputIntent {
  const normalized = normalizeIntent(intent)
  const inputVersion = reserveAccountHealthJobsInputVersionInTransaction(normalized.accountId, database)
  const eventId = randomUUID()
  database.prepare(`
    INSERT INTO account_health_jobs_input_outbox (
      event_id, account_id, input_version, event_kind, reason,
      config_revision, dispatch_revision, status, available_at,
      created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)
  `).run(
    eventId,
    normalized.accountId,
    inputVersion,
    normalized.kind,
    normalized.reason,
    normalized.configRevision,
    normalized.dispatchRevision,
    createdAt,
    createdAt,
    createdAt
  )
  return { eventId, accountId: normalized.accountId, inputVersion, kind: normalized.kind }
}

export function reserveAndEnqueueAccountHealthJobsInput(
  intent: AccountHealthJobsInputOutboxIntent,
  database: DatabaseSync = getBusinessDatabase()
): ReservedAccountHealthJobsInputIntent {
  return runInDatabaseTransaction(() => reserveAndEnqueueAccountHealthJobsInputInTransaction(intent, database), database)
}

export async function reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(
  client: DatabaseClient,
  intent: AccountHealthJobsInputOutboxIntent,
  createdAt = nowIso()
): Promise<ReservedAccountHealthJobsInputIntent> {
  const normalized = normalizeIntent(intent)
  const inputVersion = await reserveAccountHealthJobsInputVersionInTransactionAsync(client, normalized.accountId)
  const eventId = randomUUID()
  const table = client.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_outbox')
  await client.execute(`
    INSERT INTO ${table} (
      event_id, account_id, input_version, event_kind, reason,
      config_revision, dispatch_revision, status, available_at,
      created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)
  `, [
    eventId,
    normalized.accountId,
    inputVersion,
    normalized.kind,
    normalized.reason,
    normalized.configRevision,
    normalized.dispatchRevision,
    createdAt,
    createdAt,
    createdAt
  ])
  return { eventId, accountId: normalized.accountId, inputVersion, kind: normalized.kind }
}

export async function reserveAndEnqueueAccountHealthJobsInputAsync(
  client: DatabaseClient,
  intent: AccountHealthJobsInputOutboxIntent
): Promise<ReservedAccountHealthJobsInputIntent> {
  return await client.transaction(async (tx) => await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(tx, intent))
}

// Claiming is deliberately independent from task execution. The caller is a
// Node input publisher: it may read business state and write a signed file,
// but it must ACK only with this exact lease token after the file is durable.
export function claimNextAccountHealthJobsInputOutboxEvent(
  leaseMs: number,
  database: DatabaseSync = getBusinessDatabase(),
  observedAt = nowIso()
): AccountHealthJobsInputOutboxEvent | undefined {
  return runInDatabaseTransaction(() => claimNextInSqlite(leaseMs, database, observedAt), database)
}

export async function claimNextAccountHealthJobsInputOutboxEventAsync(
  client: DatabaseClient,
  leaseMs: number,
  observedAt = nowIso()
): Promise<AccountHealthJobsInputOutboxEvent | undefined> {
  return await client.transaction(async (tx) => {
    const normalizedLeaseMs = checkedLeaseMs(leaseMs)
    const table = tx.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_outbox')
    const lockClause = tx.driver === 'postgres' ? 'FOR UPDATE SKIP LOCKED' : ''
    const row = await tx.one<OutboxRow>(`
      SELECT event_id, account_id, input_version, event_kind, reason, config_revision, dispatch_revision, attempt_count
      FROM ${table}
      WHERE ((status IN ('pending', 'failed') AND available_at <= ?)
        OR (status = 'leased' AND claimed_until <= ?))
      ORDER BY available_at ASC, created_at ASC, event_id ASC
      LIMIT 1
      ${lockClause}
    `, [observedAt, observedAt])
    if (!row) return undefined
    const claimToken = randomUUID()
    const claimedUntil = futureIso(observedAt, normalizedLeaseMs)
    const changed = await tx.execute(`
      UPDATE ${table}
      SET status = 'leased', claim_token = ?, claimed_until = ?,
          attempt_count = attempt_count + 1, updated_at = ?
      WHERE event_id = ?
    `, [claimToken, claimedUntil, observedAt, row.event_id])
    if (changed.changes !== 1) throw new Error('J1 input outbox claim CAS 失败')
    return rowToLeasedEvent(row, claimToken, claimedUntil)
  })
}

export function acknowledgeAccountHealthJobsInputOutboxEvent(
  eventId: string,
  claimToken: string,
  database: DatabaseSync = getBusinessDatabase(),
  observedAt = nowIso()
): boolean {
  return runInDatabaseTransaction(() => acknowledgeInSqlite(eventId, claimToken, database, observedAt), database)
}

export async function acknowledgeAccountHealthJobsInputOutboxEventAsync(
  client: DatabaseClient,
  eventId: string,
  claimToken: string,
  observedAt = nowIso()
): Promise<boolean> {
  return await client.transaction(async (tx) => {
    const table = tx.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_outbox')
    const changed = await tx.execute(`
      UPDATE ${table}
      SET status = 'published', claim_token = NULL, claimed_until = NULL,
          last_error = NULL, updated_at = ?
      WHERE event_id = ? AND status = 'leased' AND claim_token = ?
    `, [observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')])
    return changed.changes === 1
  })
}

export function failAccountHealthJobsInputOutboxEvent(
  eventId: string,
  claimToken: string,
  error: string,
  retryAt: string,
  database: DatabaseSync = getBusinessDatabase(),
  observedAt = nowIso()
): boolean {
  return runInDatabaseTransaction(() => failInSqlite(eventId, claimToken, error, retryAt, database, observedAt), database)
}

export async function failAccountHealthJobsInputOutboxEventAsync(
  client: DatabaseClient,
  eventId: string,
  claimToken: string,
  error: string,
  retryAt: string,
  observedAt = nowIso()
): Promise<boolean> {
  return await client.transaction(async (tx) => {
    const table = tx.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_outbox')
    const changed = await tx.execute(`
      UPDATE ${table}
      SET status = 'failed', claim_token = NULL, claimed_until = NULL,
          last_error = ?, available_at = ?, updated_at = ?
      WHERE event_id = ? AND status = 'leased' AND claim_token = ?
    `, [safeError(error), checkedIso(retryAt, 'retryAt'), observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')])
    return changed.changes === 1
  })
}

export function supersedeAccountHealthJobsInputOutboxEvent(
  eventId: string,
  claimToken: string,
  database: DatabaseSync = getBusinessDatabase(),
  observedAt = nowIso()
): boolean {
  return runInDatabaseTransaction(() => supersedeInSqlite(eventId, claimToken, database, observedAt), database)
}

export async function supersedeAccountHealthJobsInputOutboxEventAsync(
  client: DatabaseClient,
  eventId: string,
  claimToken: string,
  observedAt = nowIso()
): Promise<boolean> {
  return await client.transaction(async (tx) => {
    const table = tx.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_outbox')
    const changed = await tx.execute(`
      UPDATE ${table}
      SET status = 'superseded', claim_token = NULL, claimed_until = NULL, updated_at = ?
      WHERE event_id = ? AND status = 'leased' AND claim_token = ?
    `, [observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')])
    return changed.changes === 1
  })
}

function normalizeIntent(input: AccountHealthJobsInputOutboxIntent): Required<AccountHealthJobsInputOutboxIntent> {
  const accountId = input.accountId.trim()
  const reason = input.reason.trim()
  if (!accountId) throw new Error('J1 input outbox 缺少 account ID')
  if (!reason) throw new Error('J1 input outbox 缺少 reason')
  if (input.kind !== 'snapshot' && input.kind !== 'tombstone') throw new Error('J1 input outbox event_kind 无效')
  if (!Number.isSafeInteger(input.configRevision) || input.configRevision < 1) throw new Error('J1 input outbox config revision 无效')
  if (!Number.isSafeInteger(input.dispatchRevision) || input.dispatchRevision < 1) throw new Error('J1 input outbox dispatch revision 无效')
  return { accountId, reason, kind: input.kind, configRevision: input.configRevision, dispatchRevision: input.dispatchRevision }
}

function claimNextInSqlite(leaseMs: number, database: DatabaseSync, observedAt: string): AccountHealthJobsInputOutboxEvent | undefined {
  const normalizedLeaseMs = checkedLeaseMs(leaseMs)
  const row = database.prepare(`
    SELECT event_id, account_id, input_version, event_kind, reason, config_revision, dispatch_revision, attempt_count
    FROM account_health_jobs_input_outbox
    WHERE ((status IN ('pending', 'failed') AND available_at <= ?)
      OR (status = 'leased' AND claimed_until <= ?))
    ORDER BY available_at ASC, created_at ASC, event_id ASC
    LIMIT 1
  `).get(observedAt, observedAt) as OutboxRow | undefined
  if (!row) return undefined
  const claimToken = randomUUID()
  const claimedUntil = futureIso(observedAt, normalizedLeaseMs)
  const changed = database.prepare(`
    UPDATE account_health_jobs_input_outbox
    SET status = 'leased', claim_token = ?, claimed_until = ?,
        attempt_count = attempt_count + 1, updated_at = ?
    WHERE event_id = ?
  `).run(claimToken, claimedUntil, observedAt, row.event_id)
  if (changed.changes !== 1) throw new Error('J1 input outbox claim CAS 失败')
  return rowToLeasedEvent(row, claimToken, claimedUntil)
}

function acknowledgeInSqlite(eventId: string, claimToken: string, database: DatabaseSync, observedAt: string): boolean {
  return database.prepare(`
    UPDATE account_health_jobs_input_outbox
    SET status = 'published', claim_token = NULL, claimed_until = NULL,
        last_error = NULL, updated_at = ?
    WHERE event_id = ? AND status = 'leased' AND claim_token = ?
  `).run(observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')).changes === 1
}

function failInSqlite(eventId: string, claimToken: string, error: string, retryAt: string, database: DatabaseSync, observedAt: string): boolean {
  return database.prepare(`
    UPDATE account_health_jobs_input_outbox
    SET status = 'failed', claim_token = NULL, claimed_until = NULL,
        last_error = ?, available_at = ?, updated_at = ?
    WHERE event_id = ? AND status = 'leased' AND claim_token = ?
  `).run(safeError(error), checkedIso(retryAt, 'retryAt'), observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')).changes === 1
}

function supersedeInSqlite(eventId: string, claimToken: string, database: DatabaseSync, observedAt: string): boolean {
  return database.prepare(`
    UPDATE account_health_jobs_input_outbox
    SET status = 'superseded', claim_token = NULL, claimed_until = NULL, updated_at = ?
    WHERE event_id = ? AND status = 'leased' AND claim_token = ?
  `).run(observedAt, requiredId(eventId, 'event ID'), requiredId(claimToken, 'claim token')).changes === 1
}

function rowToLeasedEvent(row: OutboxRow, claimToken: string, claimedUntil: string): AccountHealthJobsInputOutboxEvent {
  if (row.event_kind !== 'snapshot' && row.event_kind !== 'tombstone') throw new Error('J1 input outbox event_kind 存储损坏')
  return {
    eventId: requiredId(row.event_id, 'event ID'),
    accountId: requiredId(row.account_id, 'account ID'),
    inputVersion: checkedPositiveInteger(row.input_version, 'input version'),
    kind: row.event_kind,
    reason: requiredId(row.reason, 'reason'),
    configRevision: checkedPositiveInteger(row.config_revision, 'config revision'),
    dispatchRevision: checkedPositiveInteger(row.dispatch_revision, 'dispatch revision'),
    attemptCount: checkedNonNegativeInteger(row.attempt_count, 'attempt count') + 1,
    claimToken,
    claimedUntil
  }
}

function checkedLeaseMs(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1_000 || value > 10 * 60_000) throw new Error('J1 input outbox lease 必须在 1 秒到 10 分钟之间')
  return value
}

function futureIso(observedAt: string, delayMs: number): string {
  const date = new Date(checkedIso(observedAt, 'observedAt'))
  return new Date(date.getTime() + delayMs).toISOString()
}

function checkedIso(value: string, field: string): string {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) throw new Error(`J1 input outbox ${field} 必须是 ISO 时间`)
  return date.toISOString()
}

function safeError(value: string): string {
  const text = value.trim()
  if (!text) return 'input_publish_failed'
  return text.slice(0, 1_000)
}

function requiredId(value: string, field: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`J1 input outbox 缺少 ${field}`)
  return normalized
}

function checkedPositiveInteger(value: number | bigint | string, field: string): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 1) throw new Error(`J1 input outbox ${field} 存储损坏`)
  return normalized
}

function checkedNonNegativeInteger(value: number | bigint | string, field: string): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 0) throw new Error(`J1 input outbox ${field} 存储损坏`)
  return normalized
}
