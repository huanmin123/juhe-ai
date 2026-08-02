import { runtimeConfig } from '../config/runtime.js'
import {
  auditLogDetailAttemptSupplementFromRow,
  auditLogDetailSupplementFromRow,
  auditLogDetailPayloadSupplementFromRow,
  type AuditLogRow
} from './audit-log-mappers.js'
import type { AuditLogDetailSupplement } from './audit-log-types.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getDatasetDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { loadAccountNameMap } from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalString } from './value-utils.js'
import { persistedAuditTrafficSourceClause, persistedAuditTrafficSourceParams } from './audit-log-list-query.js'

const auditLogDetailSupplementColumns = [
  'conversation_key',
  'query_string',
  'error_message',
  'sample_bucket',
  'sample_reason',
  'started_at',
  'ended_at',
  'http_completed_at'
] as const

const auditLogAttemptSupplementColumns = [
  'id',
  'attempt_index',
  'account_id',
  'upstream_url',
  'upstream_status_code',
  'success',
  'error_message',
  'started_at',
  'ended_at',
  'duration_ms'
] as const

const auditLogPayloadSupplementColumns = [
  'id',
  'attempt_id',
  'part_type',
  'sequence_index',
  'raw_size_bytes',
  'capture_status',
  'created_at',
  'headers_blob_id',
  'body_blob_id'
] as const

export function auditLogDetailSupplementSelectColumns(alias: string): string {
  return qualifiedColumns(alias, auditLogDetailSupplementColumns)
}

export function auditLogAttemptSupplementSelectColumns(alias: string): string {
  return qualifiedColumns(alias, auditLogAttemptSupplementColumns)
}

export function auditLogPayloadSupplementSelectColumns(alias: string): string {
  return qualifiedColumns(alias, auditLogPayloadSupplementColumns)
}

export function getAuditLogDetailSupplement(id: string): AuditLogDetailSupplement | undefined {
  const database = getDatasetDatabase()
  const row = database.prepare(`
    SELECT ${auditLogDetailSupplementSelectColumns('al')}
    FROM audit_logs al
    WHERE al.id = ?
      AND ${persistedAuditTrafficSourceClause('al')}
  `).get(id, ...persistedAuditTrafficSourceParams()) as AuditLogRow | undefined
  if (!row) return undefined

  const attemptRows = database.prepare(`
    SELECT ${auditLogAttemptSupplementSelectColumns('attempts')}
    FROM audit_log_attempts attempts
    WHERE attempts.audit_log_id = ?
    ORDER BY attempts.attempt_index ASC, attempts.id ASC
  `).all(id) as AuditLogRow[]
  const payloadRows = database.prepare(`
    SELECT ${auditLogPayloadSupplementSelectColumns('payloads')}
    FROM audit_payload_refs payloads
    WHERE payloads.audit_log_id = ?
    ORDER BY payloads.sequence_index ASC, payloads.id ASC
  `).all(id) as AuditLogRow[]
  const accountNames = loadAccountNameMap(attemptRows.map((attempt) => String(attempt.account_id ?? '')).filter(Boolean))
  return {
    ...auditLogDetailSupplementFromRow(row),
    attempts: attemptRows.map((attempt) => auditLogDetailAttemptSupplementFromRow(attempt, accountNames)),
    payloads: payloadRows.map(auditLogDetailPayloadSupplementFromRow)
  }
}

export async function getAuditLogDetailSupplementAsync(id: string): Promise<AuditLogDetailSupplement | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_audit_log_detail_supplement_read_only',
      id
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') return getAuditLogDetailSupplement(id)

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<AuditLogRow>(`
    SELECT ${auditLogDetailSupplementSelectColumns('al')}
    FROM juhe_dataset.audit_logs al
    WHERE al.id = ?
      AND ${persistedAuditTrafficSourceClause('al')}
  `, [id, ...persistedAuditTrafficSourceParams()])
  if (!row) return undefined

  const attemptRows = await client.query<AuditLogRow>(`
    SELECT
      ${auditLogAttemptSupplementSelectColumns('attempts')},
      accounts.name AS account_name
    FROM juhe_dataset.audit_log_attempts attempts
    LEFT JOIN juhe_business.accounts accounts ON accounts.id = attempts.account_id
    WHERE attempts.audit_log_id = ?
    ORDER BY attempts.attempt_index ASC, attempts.id ASC
  `, [id])
  const payloadRows = await client.query<AuditLogRow>(`
    SELECT ${auditLogPayloadSupplementSelectColumns('payloads')}
    FROM juhe_dataset.audit_payload_refs payloads
    WHERE payloads.audit_log_id = ?
    ORDER BY payloads.sequence_index ASC, payloads.id ASC
  `, [id])
  const accountNames = namesFromRows(attemptRows, 'account_id', 'account_name')
  return {
    ...auditLogDetailSupplementFromRow(row),
    attempts: attemptRows.map((attempt) => auditLogDetailAttemptSupplementFromRow(attempt, accountNames)),
    payloads: payloadRows.map(auditLogDetailPayloadSupplementFromRow)
  }
}

function qualifiedColumns(alias: string, columns: readonly string[]): string {
  return columns.map((column) => `${alias}.${column}`).join(', ')
}

function namesFromRows(rows: AuditLogRow[], idColumn: string, nameColumn: string): Map<string, string> {
  const names = new Map<string, string>()
  for (const row of rows) {
    const id = optionalString(row[idColumn])
    const name = optionalString(row[nameColumn])
    if (id && name) names.set(id, name)
  }
  return names
}
