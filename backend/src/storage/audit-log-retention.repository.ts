import { runtimeConfig } from '../config/runtime.js'
import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getDatasetDatabase,
  rollbackDatabaseTransaction
} from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import {
  cleanupUnreferencedAuditPayloadBlobsByIds,
  cleanupUnreferencedAuditPayloadBlobsByIdsAsync
} from './audit-log-payload-blobs.js'
import type { AuditLogRow } from './audit-log-mappers.js'
import type { AuditLogSuccessHotRetentionCleanupResult } from './audit-log-types.js'

type AuditLogFilterValue = string | number
type AuditPayloadBlobRefRow = { headers_blob_id?: unknown; body_blob_id?: unknown }
interface AuditLogRetentionMutationResult {
  affectedRows: number
  candidateBlobIds: string[]
}

const postgresAuditRetentionSelectBatchLimit = 100
const postgresAuditRetentionDeleteSubBatchLimit = 10
const postgresAuditErrorGroupDeleteSubBatchLimit = 25

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  assertSqliteAuditLogRetention('cleanupAuditLogsBefore')
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  cleanupAuditPayloadBlobCandidates(deleted, limit)
  return deleted.affectedRows
}

export async function cleanupAuditLogsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<number> {
  const deleted = runtimeConfig.databaseDriver === 'postgres'
    ? await deleteAuditLogsByWhereAsync('created_at < ?', [cutoffCreatedAt], limit)
    : deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  await cleanupAuditPayloadBlobCandidatesAsync(deleted, limit)
  return deleted.affectedRows
}

export async function cleanupAuditSuccessHotRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<AuditLogSuccessHotRetentionCleanupResult> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const trimmedLogs = runtimeConfig.databaseDriver === 'postgres'
    ? await trimAuditLogDetailsByWhereAsync(
      successHotRetentionTrimWhereClause,
      [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
      limit
    )
    : trimAuditLogDetailsByWhere(
    successHotRetentionTrimWhereClause,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
    limit
  )
  const deletedBlobs = await cleanupAuditPayloadBlobCandidatesAsync(trimmedLogs, limit)
  return {
    auditLogs: trimmedLogs.affectedRows,
    auditPayloadBlobs: deletedBlobs
  }
}

export function cleanupAuditLogsByRetention(input: {
  successHotCutoffCreatedAt: string
  successCutoffCreatedAt: string
  failureCutoffCreatedAt: string
  errorGroupCutoffUpdatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): number {
  assertSqliteAuditLogRetention('cleanupAuditLogsByRetention')
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const trimmedLogs = trimAuditLogDetailsByWhere(
    successHotRetentionTrimBeforeLongCutoffWhereClause,
    [input.successHotCutoffCreatedAt, input.successCutoffCreatedAt, successSampleBucketThreshold],
    limit
  )
  const deletedLogs = deleteAuditLogsByWhere(
    "((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))",
    [input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = cleanupAuditPayloadBlobCandidates(mergeAuditLogMutationResults(trimmedLogs, deletedLogs), limit)
  return trimmedLogs.affectedRows + deletedLogs.affectedRows + deletedGroups + deletedBlobs
}

export async function cleanupAuditLogsByRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successCutoffCreatedAt: string
  failureCutoffCreatedAt: string
  errorGroupCutoffUpdatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<number> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const trimmedLogs = runtimeConfig.databaseDriver === 'postgres'
    ? await trimAuditLogDetailsByWhereAsync(
      successHotRetentionTrimBeforeLongCutoffWhereClause,
      [input.successHotCutoffCreatedAt, input.successCutoffCreatedAt, successSampleBucketThreshold],
      limit
    )
    : trimAuditLogDetailsByWhere(
      successHotRetentionTrimBeforeLongCutoffWhereClause,
      [input.successHotCutoffCreatedAt, input.successCutoffCreatedAt, successSampleBucketThreshold],
      limit
    )
  const deletedLogs = runtimeConfig.databaseDriver === 'postgres'
    ? await deleteAuditLogsByWhereAsync(
      "((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))",
      [input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
      limit
    )
    : deleteAuditLogsByWhere(
    "((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))",
    [input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = runtimeConfig.databaseDriver === 'postgres'
    ? await cleanupAuditErrorGroupsBeforeAsync(input.errorGroupCutoffUpdatedAt, limit)
    : cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = await cleanupAuditPayloadBlobCandidatesAsync(mergeAuditLogMutationResults(trimmedLogs, deletedLogs), limit)
  return trimmedLogs.affectedRows + deletedLogs.affectedRows + deletedGroups + deletedBlobs
}

const successHotRetentionTrimWhereClause = "audit_outcome = 'success' AND created_at < ? AND sample_bucket >= ? AND capture_status <> 'metadata_only'"
const successHotRetentionTrimBeforeLongCutoffWhereClause = "audit_outcome = 'success' AND created_at < ? AND created_at >= ? AND sample_bucket >= ? AND capture_status <> 'metadata_only'"

function normalizeSuccessSampleBucketThreshold(value: number | undefined): number {
  if (!Number.isFinite(value)) return 1000
  return Math.min(Math.max(Math.trunc(value ?? 1000), 0), 10000)
}

function trimAuditLogDetailsByWhere(whereClause: string, params: AuditLogFilterValue[], limit: number): AuditLogRetentionMutationResult {
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogMutationResult()

  const placeholders = ids.map(() => '?').join(',')
  const candidateBlobIds = auditPayloadBlobCandidateIds(
    database
      .prepare(`SELECT headers_blob_id, body_blob_id FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`)
      .all(...ids) as AuditPayloadBlobRefRow[]
  )
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare(`DELETE FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`).run(...ids)
    database.prepare(`DELETE FROM audit_log_attempts WHERE audit_log_id IN (${placeholders})`).run(...ids)
    const result = database.prepare(`
      UPDATE audit_logs
      SET attempt_count = 0,
          payload_count = 0,
          raw_payload_bytes = 0,
          compressed_payload_bytes = 0,
          compression_saved_bytes = 0,
          capture_status = 'metadata_only'
      WHERE id IN (${placeholders})
        AND capture_status <> 'metadata_only'
    `).run(...ids)
    commitDatabaseTransaction(database, transactionStarted)
    return {
      affectedRows: Number(result.changes ?? 0),
      candidateBlobIds
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

async function trimAuditLogDetailsByWhereAsync(whereClause: string, params: AuditLogFilterValue[], limit: number): Promise<AuditLogRetentionMutationResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<AuditLogRow>(`
    SELECT id
    FROM juhe_dataset.audit_logs
    WHERE ${whereClause}
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [...params, postgresAuditRetentionLimit(limit)])
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogMutationResult()

  let trimmed = 0
  let candidateBlobIds: string[] = []
  for (const chunk of chunkStringIds(ids, postgresAuditRetentionDeleteSubBatchLimit)) {
    await client.transaction(async (tx) => {
      candidateBlobIds.push(...auditPayloadBlobCandidateIds(await tx.query<AuditPayloadBlobRefRow>(`
        SELECT headers_blob_id, body_blob_id
        FROM juhe_dataset.audit_payload_refs
        WHERE audit_log_id = ANY(?::text[])
      `, [chunk])))
      await tx.execute('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY(?::text[])', [chunk])
      await tx.execute('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY(?::text[])', [chunk])
      trimmed += Number((await tx.execute(`
        UPDATE juhe_dataset.audit_logs
        SET attempt_count = 0,
            payload_count = 0,
            raw_payload_bytes = 0,
            compressed_payload_bytes = 0,
            compression_saved_bytes = 0,
            capture_status = 'metadata_only'
        WHERE id = ANY(?::text[])
          AND capture_status <> 'metadata_only'
      `, [chunk])).changes ?? 0)
    })
    await yieldToEventLoop()
  }
  return {
    affectedRows: trimmed,
    candidateBlobIds: uniqueStringIds(candidateBlobIds)
  }
}

function deleteAuditLogsByWhere(whereClause: string, params: AuditLogFilterValue[], limit: number): AuditLogRetentionMutationResult {
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogMutationResult()

  const placeholders = ids.map(() => '?').join(',')
  const candidateBlobIds = auditPayloadBlobCandidateIds(
    database
      .prepare(`SELECT headers_blob_id, body_blob_id FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`)
      .all(...ids) as AuditPayloadBlobRefRow[]
  )
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return {
    affectedRows: Number(result.changes ?? 0),
    candidateBlobIds
  }
}

async function deleteAuditLogsByWhereAsync(whereClause: string, params: AuditLogFilterValue[], limit: number): Promise<AuditLogRetentionMutationResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<AuditLogRow>(`
    SELECT id
    FROM juhe_dataset.audit_logs
    WHERE ${whereClause}
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [...params, postgresAuditRetentionLimit(limit)])
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogMutationResult()

  let deleted = 0
  let candidateBlobIds: string[] = []
  for (const chunk of chunkStringIds(ids, postgresAuditRetentionDeleteSubBatchLimit)) {
    await client.transaction(async (tx) => {
      candidateBlobIds.push(...auditPayloadBlobCandidateIds(await tx.query<AuditPayloadBlobRefRow>(`
        SELECT headers_blob_id, body_blob_id
        FROM juhe_dataset.audit_payload_refs
        WHERE audit_log_id = ANY(?::text[])
      `, [chunk])))
      await tx.execute('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY(?::text[])', [chunk])
      await tx.execute('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY(?::text[])', [chunk])
      deleted += Number((await tx.execute('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY(?::text[])', [chunk])).changes ?? 0)
    })
    await yieldToEventLoop()
  }
  return {
    affectedRows: deleted,
    candidateBlobIds: uniqueStringIds(candidateBlobIds)
  }
}

function cleanupAuditPayloadBlobCandidates(result: AuditLogRetentionMutationResult, limit: number): number {
  if (result.affectedRows <= 0 || result.candidateBlobIds.length === 0) return 0
  return cleanupUnreferencedAuditPayloadBlobsByIds(result.candidateBlobIds, limit)
}

async function cleanupAuditPayloadBlobCandidatesAsync(result: AuditLogRetentionMutationResult, limit: number): Promise<number> {
  if (result.affectedRows <= 0 || result.candidateBlobIds.length === 0) return 0
  return cleanupUnreferencedAuditPayloadBlobsByIdsAsync(result.candidateBlobIds, limit)
}

function emptyAuditLogMutationResult(): AuditLogRetentionMutationResult {
  return {
    affectedRows: 0,
    candidateBlobIds: []
  }
}

function auditPayloadBlobCandidateIds(rows: AuditPayloadBlobRefRow[]): string[] {
  return [...new Set(rows.flatMap((row) => [
    String(row.headers_blob_id ?? '').trim(),
    String(row.body_blob_id ?? '').trim()
  ]).filter(Boolean))]
}

function cleanupAuditErrorGroupsBefore(cutoffUpdatedAt: string, limit: number): number {
  const database = getDatasetDatabase()
  const unreferencedGroupWhere = `
    updated_at < ?
    AND NOT EXISTS (
      SELECT 1
      FROM audit_logs
      WHERE audit_logs.error_group_id = audit_error_groups.id
    )
  `
  const rows = database
    .prepare(`SELECT id FROM audit_error_groups WHERE ${unreferencedGroupWhere} ORDER BY updated_at ASC, id ASC LIMIT ?`)
    .all(cutoffUpdatedAt, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0
  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

async function cleanupAuditErrorGroupsBeforeAsync(cutoffUpdatedAt: string, limit: number): Promise<number> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<AuditLogRow>(`
    SELECT id
    FROM juhe_dataset.audit_error_groups
    WHERE updated_at < ?
      AND NOT EXISTS (
        SELECT 1
        FROM juhe_dataset.audit_logs
        WHERE audit_logs.error_group_id = audit_error_groups.id
      )
    ORDER BY updated_at ASC, id ASC
    LIMIT ?
  `, [cutoffUpdatedAt, postgresAuditRetentionLimit(limit)])
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0
  let deleted = 0
  for (const chunk of chunkStringIds(ids, postgresAuditErrorGroupDeleteSubBatchLimit)) {
    deleted += Number((await client.execute('DELETE FROM juhe_dataset.audit_error_groups WHERE id = ANY(?::text[])', [chunk])).changes ?? 0)
    await yieldToEventLoop()
  }
  return deleted
}

function mergeAuditLogMutationResults(...results: AuditLogRetentionMutationResult[]): AuditLogRetentionMutationResult {
  return {
    affectedRows: results.reduce((total, result) => total + result.affectedRows, 0),
    candidateBlobIds: uniqueStringIds(results.flatMap((result) => result.candidateBlobIds))
  }
}

function assertSqliteAuditLogRetention(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error(`高性能模式禁止调用 SQLite 审计日志保留清理入口：${operation}`)
  }
}

function uniqueStringIds(ids: string[]): string[] {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
}

function chunkStringIds(ids: string[], size: number): string[][] {
  const normalizedIds = uniqueStringIds(ids)
  const chunkSize = Math.max(1, Math.trunc(size))
  const chunks: string[][] = []
  for (let offset = 0; offset < normalizedIds.length; offset += chunkSize) {
    chunks.push(normalizedIds.slice(offset, offset + chunkSize))
  }
  return chunks
}

function postgresAuditRetentionLimit(limit: number): number {
  return Math.min(Math.max(1, Math.trunc(limit)), postgresAuditRetentionSelectBatchLimit)
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}
