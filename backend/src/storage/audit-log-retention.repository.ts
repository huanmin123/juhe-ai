import { runtimeConfig } from '../config/runtime.js'
import { getDatasetDatabase } from './database.js'
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
interface AuditLogDeleteResult {
  deletedRows: number
  candidateBlobIds: string[]
}

const postgresAuditRetentionSelectBatchLimit = 100
const postgresAuditRetentionDeleteSubBatchLimit = 10
const postgresAuditErrorGroupDeleteSubBatchLimit = 25

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  assertSqliteAuditLogRetention('cleanupAuditLogsBefore')
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  cleanupAuditPayloadBlobCandidates(deleted, limit)
  return deleted.deletedRows
}

export async function cleanupAuditLogsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<number> {
  const deleted = runtimeConfig.databaseDriver === 'postgres'
    ? await deleteAuditLogsByWhereAsync('created_at < ?', [cutoffCreatedAt], limit)
    : deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  await cleanupAuditPayloadBlobCandidatesAsync(deleted, limit)
  return deleted.deletedRows
}

export async function cleanupAuditSuccessHotRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<AuditLogSuccessHotRetentionCleanupResult> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = runtimeConfig.databaseDriver === 'postgres'
    ? await deleteAuditLogsByWhereAsync(
      successHotRetentionDeleteWhereClause,
      [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
      limit
    )
    : deleteAuditLogsByWhere(
    successHotRetentionDeleteWhereClause,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
    limit
  )
  const deletedBlobs = await cleanupAuditPayloadBlobCandidatesAsync(deletedLogs, limit)
  return {
    auditLogs: deletedLogs.deletedRows,
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
  const deletedLogs = deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = cleanupAuditPayloadBlobCandidates(deletedLogs, limit)
  return deletedLogs.deletedRows + deletedGroups + deletedBlobs
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
  const deletedLogs = runtimeConfig.databaseDriver === 'postgres'
    ? await deleteAuditLogsByWhereAsync(
      `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
      [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
      limit
    )
    : deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = runtimeConfig.databaseDriver === 'postgres'
    ? await cleanupAuditErrorGroupsBeforeAsync(input.errorGroupCutoffUpdatedAt, limit)
    : cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = await cleanupAuditPayloadBlobCandidatesAsync(deletedLogs, limit)
  return deletedLogs.deletedRows + deletedGroups + deletedBlobs
}

const successHotRetentionDeleteWhereClause = "audit_outcome = 'success' AND created_at < ? AND sample_bucket >= ?"

function normalizeSuccessSampleBucketThreshold(value: number | undefined): number {
  if (!Number.isFinite(value)) return 1000
  return Math.min(Math.max(Math.trunc(value ?? 1000), 0), 10000)
}

function deleteAuditLogsByWhere(whereClause: string, params: AuditLogFilterValue[], limit: number): AuditLogDeleteResult {
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogDeleteResult()

  const placeholders = ids.map(() => '?').join(',')
  const candidateBlobIds = auditPayloadBlobCandidateIds(
    database
      .prepare(`SELECT headers_blob_id, body_blob_id FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`)
      .all(...ids) as AuditPayloadBlobRefRow[]
  )
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return {
    deletedRows: Number(result.changes ?? 0),
    candidateBlobIds
  }
}

async function deleteAuditLogsByWhereAsync(whereClause: string, params: AuditLogFilterValue[], limit: number): Promise<AuditLogDeleteResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<AuditLogRow>(`
    SELECT id
    FROM juhe_dataset.audit_logs
    WHERE ${whereClause}
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [...params, postgresAuditRetentionLimit(limit)])
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return emptyAuditLogDeleteResult()

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
    deletedRows: deleted,
    candidateBlobIds: uniqueStringIds(candidateBlobIds)
  }
}

function cleanupAuditPayloadBlobCandidates(result: AuditLogDeleteResult, limit: number): number {
  if (result.deletedRows <= 0 || result.candidateBlobIds.length === 0) return 0
  return cleanupUnreferencedAuditPayloadBlobsByIds(result.candidateBlobIds, limit)
}

async function cleanupAuditPayloadBlobCandidatesAsync(result: AuditLogDeleteResult, limit: number): Promise<number> {
  if (result.deletedRows <= 0 || result.candidateBlobIds.length === 0) return 0
  return cleanupUnreferencedAuditPayloadBlobsByIdsAsync(result.candidateBlobIds, limit)
}

function emptyAuditLogDeleteResult(): AuditLogDeleteResult {
  return {
    deletedRows: 0,
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
