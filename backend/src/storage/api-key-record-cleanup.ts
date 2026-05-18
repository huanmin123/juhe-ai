import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'

export interface DeletedApiKeyRecordCleanupTarget {
  apiKeyId: string
  systemAccountId: string
}

type UsageStatsAggregationCursor = {
  cursorCreatedAt: string
  cursorId: string
}

export function cleanupDeletedApiKeyRelatedRecordData(input: DeletedApiKeyRecordCleanupTarget): void {
  const database = getRecordDatabase()
  const updatedAt = nowIso()
  const cursor = usageStatsAggregationCursor(database)
  let cursorCreatedAt = ''
  let cursorId = ''
  const batchLimit = 1000
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    while (true) {
      const usageRows = database.prepare(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM usage_records
        WHERE api_key_id = ?
          AND (created_at > ? OR (created_at = ? AND id > ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `).all(input.apiKeyId, cursorCreatedAt, cursorCreatedAt, cursorId, batchLimit) as unknown as UsageStatsRecordRow[]
      if (usageRows.length === 0) {
        break
      }
      for (const usageRow of usageRows) {
        if (isUsageRecordAlreadyAggregated(usageRow, cursor)) {
          subtractUsageStatsRecord(database, usageRow, updatedAt)
        }
      }
      const last = usageRows[usageRows.length - 1]
      cursorCreatedAt = last.created_at
      cursorId = last.id
      if (usageRows.length < batchLimit) {
        break
      }
    }
    for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
      database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`).run(input.systemAccountId, input.apiKeyId)
    }
    database.prepare('DELETE FROM usage_records WHERE api_key_id = ?').run(input.apiKeyId)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function usageStatsAggregationCursor(database: DatabaseSync): UsageStatsAggregationCursor {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function isUsageRecordAlreadyAggregated(row: UsageStatsRecordRow, cursor: UsageStatsAggregationCursor): boolean {
  if (!cursor.cursorCreatedAt) return false
  return row.created_at < cursor.cursorCreatedAt || (row.created_at === cursor.cursorCreatedAt && row.id <= cursor.cursorId)
}
