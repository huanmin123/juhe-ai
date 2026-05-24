import type { DatabaseSync } from 'node:sqlite'

import { cleanupUnreferencedAuditPayloadBlobs } from './audit-log-payload-blobs.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocations, type UsageRecordShardLocation } from './usage-record-shards.js'
import { refreshUsageQuotaHourlyWindowsCache, refreshUsageRankSnapshots } from './usage-stats.repository.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'

export interface DeletedApiKeyRecordCleanupTarget {
  apiKeyId: string
  systemAccountId: string
}

export interface DeletedApiKeyRecordCleanupResult extends DeletedApiKeyRecordCleanupTarget {
  deletedRows: number
  hasMore: boolean
  blockedReason?: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
}

export interface PendingDeletedApiKeyRecordCleanupSummary {
  attempted: number
  completed: number
  deferred: number
  failed: number
  deletedRows: number
}

export interface DeletedApiKeyRecordCleanupQueueSummary {
  pendingTargets: number
  blockedTargets: number
  failedTargets: number
  oldestCreatedAt?: string
  lastAttemptAt?: string
}

export interface DeletedApiKeyRecordCleanupQueueTarget extends DeletedApiKeyRecordCleanupTarget {
  createdAt: string
  updatedAt: string
  attemptCount: number
  lastAttemptAt?: string
  lastBlockedReason?: string
  lastErrorMessage?: string
}

type PendingDeletedApiKeyRecordCleanupTargetRow = {
  api_key_id?: string | null
  system_account_id?: string | null
}

type DeletedApiKeyRecordCleanupQueueTargetRow = PendingDeletedApiKeyRecordCleanupTargetRow & {
  created_at?: string | null
  updated_at?: string | null
  attempt_count?: number | null
  last_attempt_at?: string | null
  last_blocked_reason?: string | null
  last_error_message?: string | null
}

type DeletedApiKeyRecordCleanupQueueSummaryRow = {
  pending_targets?: number | null
  blocked_targets?: number | null
  failed_targets?: number | null
  oldest_created_at?: string | null
  last_attempt_at?: string | null
}

type ApiKeyUsageShardRow = UsageStatsRecordRow & {
  location: UsageRecordShardLocation
  source_shard_key: string
}

type UsageRecordCleanupDeductionRow = {
  stats_subtracted_at?: string | null
}

interface ApiKeyUsageShardBatch {
  rows: ApiKeyUsageShardRow[]
  hasMoreCoveredRows: boolean
  hasUncoveredRows: boolean
}

const apiKeyScopeStatsTables = [
  'usage_stats_totals',
  'usage_stats_minute',
  'usage_stats_hourly',
  'usage_stats_daily',
  'usage_stats_weekly',
  'usage_stats_monthly',
  'usage_latency_minute',
  'usage_latency_hourly',
  'usage_latency_daily',
  'usage_latency_weekly',
  'usage_latency_monthly',
  'usage_rank_snapshots',
  'usage_quota_hourly_windows',
  'usage_scope_range_windows'
] as const
const deletedApiKeyRecordCleanupBatchLimit = 1000

export function registerDeletedApiKeyRecordCleanupTarget(input: DeletedApiKeyRecordCleanupTarget): void {
  upsertDeletedApiKeyRecordCleanupTarget(getDatasetDatabase(), input, nowIso())
}

export function cleanupPendingDeletedApiKeyRecordTargets(limit = 50): PendingDeletedApiKeyRecordCleanupSummary {
  const targets = listDeletedApiKeyRecordCleanupTargets(Math.max(1, Math.trunc(limit)))
  const summary: PendingDeletedApiKeyRecordCleanupSummary = {
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0
  }
  for (const target of targets) {
    summary.attempted += 1
    try {
      const result = cleanupDeletedApiKeyRelatedRecordData(target)
      summary.deletedRows += result.deletedRows
      if (result.hasMore || result.blockedReason) {
        summary.deferred += 1
      } else {
        summary.completed += 1
      }
    } catch (error) {
      summary.failed += 1
      markDeletedApiKeyRecordCleanupTargetError(getDatasetDatabase(), target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export function listDeletedApiKeyRecordCleanupTargets(limit = 50): DeletedApiKeyRecordCleanupTarget[] {
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT api_key_id, system_account_id
      FROM api_key_record_cleanup_targets
      ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as PendingDeletedApiKeyRecordCleanupTargetRow[]
  return rows
    .map((row) => ({
      apiKeyId: String(row.api_key_id ?? ''),
      systemAccountId: String(row.system_account_id ?? '')
    }))
    .filter((row) => row.apiKeyId && row.systemAccountId)
}

export function getDeletedApiKeyRecordCleanupQueueSummary(): DeletedApiKeyRecordCleanupQueueSummary {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        COUNT(*) AS pending_targets,
        SUM(CASE WHEN last_blocked_reason IS NOT NULL THEN 1 ELSE 0 END) AS blocked_targets,
        SUM(CASE WHEN last_error_message IS NOT NULL THEN 1 ELSE 0 END) AS failed_targets,
        MIN(created_at) AS oldest_created_at,
        MAX(last_attempt_at) AS last_attempt_at
      FROM api_key_record_cleanup_targets
    `)
    .get() as DeletedApiKeyRecordCleanupQueueSummaryRow | undefined
  return {
    pendingTargets: Number(row?.pending_targets ?? 0),
    blockedTargets: Number(row?.blocked_targets ?? 0),
    failedTargets: Number(row?.failed_targets ?? 0),
    oldestCreatedAt: optionalText(row?.oldest_created_at),
    lastAttemptAt: optionalText(row?.last_attempt_at)
  }
}

export function listDeletedApiKeyRecordCleanupQueueTargets(limit = 50): DeletedApiKeyRecordCleanupQueueTarget[] {
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT
        api_key_id,
        system_account_id,
        created_at,
        updated_at,
        attempt_count,
        last_attempt_at,
        last_blocked_reason,
        last_error_message
      FROM api_key_record_cleanup_targets
      ORDER BY
        CASE
          WHEN last_error_message IS NOT NULL THEN 0
          WHEN last_blocked_reason IS NOT NULL THEN 1
          ELSE 2
        END ASC,
        COALESCE(last_attempt_at, created_at) ASC,
        created_at ASC,
        api_key_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as DeletedApiKeyRecordCleanupQueueTargetRow[]
  return rows
    .map((row) => ({
      apiKeyId: String(row.api_key_id ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      createdAt: String(row.created_at ?? ''),
      updatedAt: String(row.updated_at ?? ''),
      attemptCount: Number(row.attempt_count ?? 0),
      lastAttemptAt: optionalText(row.last_attempt_at),
      lastBlockedReason: optionalText(row.last_blocked_reason),
      lastErrorMessage: optionalText(row.last_error_message)
    }))
    .filter((row) => row.apiKeyId && row.systemAccountId && row.createdAt && row.updatedAt)
}

export function cleanupDeletedApiKeyRelatedRecordData(input: DeletedApiKeyRecordCleanupTarget): DeletedApiKeyRecordCleanupResult {
  const database = getDatasetDatabase()
  const statsDatabase = getStatsDatabase()
  const updatedAt = nowIso()
  upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
  let shouldRefreshDerivedWindows = false
  const batchLimit = deletedApiKeyRecordCleanupBatchLimit
  let deletedRows = 0
  let transactionStarted = false
  let statsTransactionStarted = false
  try {
    transactionStarted = beginDatabaseTransaction(database)
    const usageBatch = selectApiKeyUsageRowsCoveredByShardCursors(statsDatabase, input, batchLimit)
    const rowsToDelete = usageBatch.rows.slice(0, batchLimit)
    const hasMoreCoveredRows = usageBatch.hasMoreCoveredRows
    if (rowsToDelete.length > 0) {
      subtractApiKeyUsageRowsOnce(statsDatabase, rowsToDelete, input, updatedAt)
    }
    deletedRows += deleteApiKeyUsageRows(rowsToDelete, input)
    markApiKeyUsageCleanupRowsDeleted(statsDatabase, rowsToDelete, input, updatedAt)
    deletedRows += deleteApiKeyAuditDataBatch(database, input, batchLimit)

    const hasUsageMore = hasApiKeyUsageRecords(input)
    const hasAuditMore = hasApiKeyAuditLogs(database, input)
    const hasMore = hasUsageMore || hasAuditMore
    if (!hasMore) {
      if (!statsTransactionStarted) {
        statsTransactionStarted = beginDatabaseTransaction(statsDatabase)
      }
      deleteApiKeyScopeStatsRows(statsDatabase, input)
      deleteApiKeyUsageCleanupDeductions(statsDatabase, input)
    }
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows,
      hasMore,
      blockedReason: hasMore
        ? (hasAuditMore
          ? '仍有已删除 API Key 的原始审计记录待后续批次清理，已保留待后台重试'
          : hasMoreCoveredRows
          ? '仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试'
          : usageBatch.hasUncoveredRows
          ? '仍有使用记录尚未被对应分片统计安全游标覆盖，已保留待后台重试清理'
          : '仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理')
        : undefined
    }
    if (hasMore) {
      markDeletedApiKeyRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedApiKeyRecordCleanupTarget(database, input)
      shouldRefreshDerivedWindows = true
    }
    commitDatabaseTransaction(statsDatabase, statsTransactionStarted)
    statsTransactionStarted = false
    commitDatabaseTransaction(database, transactionStarted)
    transactionStarted = false
    refreshDeletedApiKeyDerivedWindowsIfNeeded(input, shouldRefreshDerivedWindows)
    cleanupAuditPayloadBlobsBestEffort(batchLimit)
    return result
  } catch (error) {
    rollbackDatabaseTransaction(statsDatabase, statsTransactionStarted)
    rollbackDatabaseTransaction(database, transactionStarted)
    markDeletedApiKeyRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

function upsertDeletedApiKeyRecordCleanupTarget(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, updatedAt: string): void {
  database.prepare(`
    INSERT INTO api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(api_key_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      updated_at = excluded.updated_at
  `).run(input.apiKeyId, input.systemAccountId, updatedAt, updatedAt)
}

function markDeletedApiKeyRecordCleanupTargetDeferred(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, blockedReason: string, updatedAt: string): void {
  database.prepare(`
    UPDATE api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `).run(updatedAt, blockedReason, updatedAt, input.apiKeyId, input.systemAccountId)
}

function markDeletedApiKeyRecordCleanupTargetError(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, message: string, updatedAt: string): void {
  database.prepare(`
    UPDATE api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `).run(updatedAt, message, updatedAt, input.apiKeyId, input.systemAccountId)
}

function clearDeletedApiKeyRecordCleanupTarget(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  database.prepare('DELETE FROM api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?')
    .run(input.apiKeyId, input.systemAccountId)
}

function hasApiKeyUsageRecords(input: DeletedApiKeyRecordCleanupTarget): boolean {
  for (const location of listUsageRecordShardLocations()) {
    const row = getUsageRecordShardDatabase(location)
      .prepare('SELECT id FROM usage_records WHERE api_key_id = ? AND system_account_id = ? LIMIT 1')
      .get(input.apiKeyId, input.systemAccountId) as unknown as { id?: string } | undefined
    if (row?.id) return true
  }
  return false
}

function selectApiKeyUsageRowsCoveredByShardCursors(
  statsDatabase: DatabaseSync,
  input: DeletedApiKeyRecordCleanupTarget,
  limit: number
): ApiKeyUsageShardBatch {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const queryLimit = batchLimit + 1
  const rows: ApiKeyUsageShardRow[] = []
  let hasUncoveredRows = false
  for (const location of listUsageRecordShardLocations()) {
    const shardDatabase = getUsageRecordShardDatabase(location)
    const anyUsage = shardDatabase
      .prepare('SELECT id FROM usage_records WHERE api_key_id = ? AND system_account_id = ? LIMIT 1')
      .get(input.apiKeyId, input.systemAccountId) as unknown as { id?: string } | undefined
    if (!anyUsage?.id) continue
    const cursor = usageStatsShardCursor(statsDatabase, location.shardKey)
    if (!cursor) {
      hasUncoveredRows = true
      continue
    }
    const uncovered = shardDatabase
      .prepare(`
        SELECT id
        FROM usage_records
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND (created_at > ? OR (created_at = ? AND id > ?))
        LIMIT 1
      `)
      .get(input.apiKeyId, input.systemAccountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId) as unknown as { id?: string } | undefined
    if (uncovered?.id) {
      hasUncoveredRows = true
    }
    rows.push(...(shardDatabase.prepare(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM usage_records
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND (created_at < ? OR (created_at = ? AND id <= ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `).all(input.apiKeyId, input.systemAccountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, queryLimit) as unknown as UsageStatsRecordRow[])
      .map((row) => ({
        ...row,
        location,
        source_shard_key: location.shardKey
      })))
  }
  const sortedRows = rows
    .sort((left, right) => left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id))
  return {
    rows: sortedRows.slice(0, queryLimit),
    hasMoreCoveredRows: sortedRows.length > batchLimit,
    hasUncoveredRows
  }
}

function usageStatsShardCursor(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = ? AND job_name = 'usage_stats_aggregation'")
    .get(shardKey) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  const cursorCreatedAt = row?.cursor_created_at?.trim()
  const cursorId = row?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function usageStatsRecordForCleanup(row: ApiKeyUsageShardRow): UsageStatsRecordRow {
  const { location: _location, ...record } = row
  return record
}

function subtractApiKeyUsageRowsOnce(
  database: DatabaseSync,
  rows: ApiKeyUsageShardRow[],
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): void {
  if (rows.length === 0) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const insertDeductionStatement = database.prepare(`
      INSERT INTO usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, excluded.account_id),
        updated_at = excluded.updated_at
    `)
    const findDeductionStatement = database.prepare(`
      SELECT stats_subtracted_at
      FROM usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ?
      LIMIT 1
    `)
    const markSubtractedStatement = database.prepare(`
      UPDATE usage_record_cleanup_deductions
      SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?),
          updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
    `)

    for (const row of rows) {
      insertDeductionStatement.run(
        row.id,
        input.apiKeyId,
        row.account_id ?? null,
        input.systemAccountId,
        row.source_shard_key,
        JSON.stringify(usageStatsRecordForCleanup(row)),
        updatedAt,
        updatedAt
      )
      const deduction = findDeductionStatement.get(row.id, row.source_shard_key) as UsageRecordCleanupDeductionRow | undefined
      if (deduction?.stats_subtracted_at) {
        continue
      }
      subtractUsageStatsRecord(database, usageStatsRecordForCleanup(row), updatedAt)
      markSubtractedStatement.run(updatedAt, updatedAt, row.id, row.source_shard_key)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function deleteApiKeyUsageRows(rows: ApiKeyUsageShardRow[], input: DeletedApiKeyRecordCleanupTarget): number {
  let deletedRows = 0
  const rowsByShard = new Map<string, ApiKeyUsageShardRow[]>()
  for (const row of rows) {
    rowsByShard.set(row.location.shardKey, [...(rowsByShard.get(row.location.shardKey) ?? []), row])
  }
  for (const shardRows of rowsByShard.values()) {
    const database = getUsageRecordShardDatabase(shardRows[0].location)
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      const deleteStatement = database.prepare('DELETE FROM usage_records WHERE id = ? AND api_key_id = ? AND system_account_id = ?')
      for (const row of shardRows) {
        deletedRows += changed(deleteStatement.run(row.id, input.apiKeyId, input.systemAccountId))
      }
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
  return deletedRows
}

function markApiKeyUsageCleanupRowsDeleted(
  database: DatabaseSync,
  rows: ApiKeyUsageShardRow[],
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): void {
  if (rows.length === 0) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const statement = database.prepare(`
      UPDATE usage_record_cleanup_deductions
      SET shard_deleted_at = COALESCE(shard_deleted_at, ?),
          updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
    `)
    for (const row of rows) {
      statement.run(updatedAt, updatedAt, row.id, row.source_shard_key)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function hasApiKeyAuditLogs(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): boolean {
  const row = database
    .prepare('SELECT id FROM audit_logs WHERE api_key_id = ? AND system_account_id = ? LIMIT 1')
    .get(input.apiKeyId, input.systemAccountId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function deleteApiKeyAuditDataBatch(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, limit: number): number {
  const rows = database
    .prepare(`
      SELECT id
      FROM audit_logs
      WHERE api_key_id = ?
        AND system_account_id = ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.apiKeyId, input.systemAccountId, Math.max(1, Math.trunc(limit))) as unknown as Array<{ id?: string }>
  const auditLogIds = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  let deletedRows = 0
  if (auditLogIds.length > 0) {
    const placeholders = sqlPlaceholders(auditLogIds.length)
    deletedRows += changed(database.prepare(`DELETE FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_log_attempts WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders}) AND api_key_id = ? AND system_account_id = ?`).run(...auditLogIds, input.apiKeyId, input.systemAccountId))
  }
  deletedRows += changed(database
    .prepare('DELETE FROM audit_error_groups WHERE api_key_id = ? AND system_account_id = ?')
    .run(input.apiKeyId, input.systemAccountId))
  return deletedRows
}

function deleteApiKeyScopeStatsRows(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  for (const tableName of apiKeyScopeStatsTables) {
    database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`)
      .run(input.systemAccountId, input.apiKeyId)
  }
  database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'api_key' AND scope_id = ?")
    .run(input.apiKeyId)
}

function deleteApiKeyUsageCleanupDeductions(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  database.prepare('DELETE FROM usage_record_cleanup_deductions WHERE api_key_id = ? AND system_account_id = ?')
    .run(input.apiKeyId, input.systemAccountId)
}

function refreshDeletedApiKeyDerivedWindowsIfNeeded(input: DeletedApiKeyRecordCleanupTarget, shouldRefresh: boolean): void {
  if (!shouldRefresh) return
  try {
    refreshUsageQuotaHourlyWindowsCache()
    refreshUsageRankSnapshots()
  } catch (error) {
    const database = getDatasetDatabase()
    const updatedAt = nowIso()
    upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
    markDeletedApiKeyRecordCleanupTargetError(database, input, `已删除 API Key 衍生统计窗口刷新失败：${errorMessage(error)}`, updatedAt)
    throw error
  }
}

function cleanupAuditPayloadBlobsBestEffort(limit: number): void {
  try {
    cleanupUnreferencedAuditPayloadBlobs(limit)
  } catch {
  }
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '已删除 API Key 关联数据清理失败'
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}
