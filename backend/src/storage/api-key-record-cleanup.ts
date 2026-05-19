import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { usageRecordsCleanupCursor } from './data-retention.repository.js'
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

type PendingDeletedApiKeyRecordCleanupTargetRow = {
  api_key_id?: string | null
  system_account_id?: string | null
}

const apiKeyScopeStatsTables = [
  'usage_stats_totals',
  'usage_stats_minute',
  'usage_stats_hourly',
  'usage_stats_daily',
  'usage_stats_weekly',
  'usage_stats_monthly'
] as const
const deletedApiKeyRecordCleanupBatchLimit = 1000

export function registerDeletedApiKeyRecordCleanupTarget(input: DeletedApiKeyRecordCleanupTarget): void {
  upsertDeletedApiKeyRecordCleanupTarget(getRecordDatabase(), input, nowIso())
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
      markDeletedApiKeyRecordCleanupTargetError(getRecordDatabase(), target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export function listDeletedApiKeyRecordCleanupTargets(limit = 50): DeletedApiKeyRecordCleanupTarget[] {
  const rows = getRecordDatabase()
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

export function cleanupDeletedApiKeyRelatedRecordData(input: DeletedApiKeyRecordCleanupTarget): DeletedApiKeyRecordCleanupResult {
  const database = getRecordDatabase()
  const updatedAt = nowIso()
  upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
  const cursor = usageRecordsCleanupCursor(database)
  const cursorCreatedAt = cursor.cursorCreatedAt
  const cursorId = cursor.cursorId
  if (!cursorCreatedAt || !cursorId) {
    const hasMore = hasApiKeyUsageRecords(database, input)
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows: 0,
      hasMore,
      blockedReason: hasMore ? cursor.blockedReason ?? '统计安全游标尚未建立，暂不清理已删除 API Key 的使用记录' : undefined
    }
    if (hasMore) {
      markDeletedApiKeyRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '统计安全游标尚未建立', updatedAt)
    } else {
      deleteApiKeyScopeStats(database, input)
      clearDeletedApiKeyRecordCleanupTarget(database, input)
    }
    return result
  }

  const batchLimit = deletedApiKeyRecordCleanupBatchLimit
  let deletedRows = 0
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const usageRows = database.prepare(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM usage_records
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND (created_at < ? OR (created_at = ? AND id <= ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `).all(input.apiKeyId, input.systemAccountId, cursorCreatedAt, cursorCreatedAt, cursorId, batchLimit + 1) as unknown as UsageStatsRecordRow[]
    const rowsToDelete = usageRows.slice(0, batchLimit)
    const deleteUsageRecord = database.prepare('DELETE FROM usage_records WHERE id = ? AND api_key_id = ? AND system_account_id = ?')
    for (const usageRow of rowsToDelete) {
      subtractUsageStatsRecord(database, usageRow, updatedAt)
      deletedRows += changed(deleteUsageRecord.run(usageRow.id, input.apiKeyId, input.systemAccountId))
    }

    const hasMore = hasApiKeyUsageRecords(database, input)
    const hasMoreCoveredRows = usageRows.length > batchLimit
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows,
      hasMore,
      blockedReason: hasMore
        ? (hasMoreCoveredRows
          ? '仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试'
          : '仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理')
        : undefined,
      safetyCursorCreatedAt: cursorCreatedAt,
      safetyCursorId: cursorId
    }
    if (hasMore) {
      markDeletedApiKeyRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      deleteApiKeyScopeStats(database, input)
      clearDeletedApiKeyRecordCleanupTarget(database, input)
    }
    commitDatabaseTransaction(database, transactionStarted)
    return result
  } catch (error) {
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

function hasApiKeyUsageRecords(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): boolean {
  const row = database
    .prepare('SELECT id FROM usage_records WHERE api_key_id = ? AND system_account_id = ? LIMIT 1')
    .get(input.apiKeyId, input.systemAccountId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function deleteApiKeyScopeStats(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  for (const tableName of apiKeyScopeStatsTables) {
    database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`)
      .run(input.systemAccountId, input.apiKeyId)
  }
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '已删除 API Key 记录库清理失败'
}
