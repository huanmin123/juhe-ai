import { getDatasetDatabase } from './database.js'
import {
  cleanupUnreferencedAuditPayloadBlobs,
  cleanupUnreferencedAuditPayloadBlobsAsync
} from './audit-log-payload-blobs.js'
import type { AuditLogRow } from './audit-log-mappers.js'
import type { AuditLogSuccessHotRetentionCleanupResult } from './audit-log-types.js'

type AuditLogFilterValue = string | number

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  cleanupUnreferencedAuditPayloadBlobs(limit)
  return deleted
}

export async function cleanupAuditLogsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<number> {
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return deleted
}

export async function cleanupAuditSuccessHotRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<AuditLogSuccessHotRetentionCleanupResult> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = deleteAuditLogsByWhere(
    successHotRetentionDeleteWhereClause,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
    limit
  )
  const deletedBlobs = await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return {
    auditLogs: deletedLogs,
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
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = cleanupUnreferencedAuditPayloadBlobs(limit)
  return deletedLogs + deletedGroups + deletedBlobs
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
  const deletedLogs = deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return deletedLogs + deletedGroups + deletedBlobs
}

const successHotRetentionDeleteWhereClause = "audit_outcome = 'success' AND created_at < ? AND sample_bucket >= ?"

function normalizeSuccessSampleBucketThreshold(value: number | undefined): number {
  if (!Number.isFinite(value)) return 1000
  return Math.min(Math.max(Math.trunc(value ?? 1000), 0), 10000)
}

function deleteAuditLogsByWhere(whereClause: string, params: AuditLogFilterValue[], limit: number): number {
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
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
