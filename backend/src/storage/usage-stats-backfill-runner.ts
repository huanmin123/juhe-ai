import type { DatabaseSync } from 'node:sqlite'

import { beginImmediateDatabaseTransaction, commitDatabaseTransaction, nowIso, rollbackDatabaseTransaction } from './database.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { createUsageStatsAggregationContext, type UsageStatsAggregationContext } from './usage-stats-writers.js'

export interface UsageStatsBackfillCursor {
  cursorCreatedAt: string
  cursorId: string
}

export interface UsageStatsBackfillResult {
  complete: boolean
  processed: number
}

interface UsageStatsBackfillJobStateRow {
  cursor_created_at?: string | null
  cursor_id?: string | null
  last_success_at?: string | null
}

interface UsageStatsBackfillRunnerOptions {
  database: DatabaseSync
  jobName: string
  limit: number
  sourceCursor: UsageStatsBackfillCursor
  recordFilterSql: string
  failureMessage: string
  aggregateRecord: (database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string, context?: UsageStatsAggregationContext) => void
}

export function ensureUsageStatsBackfill(options: UsageStatsBackfillRunnerOptions): UsageStatsBackfillResult {
  const transactionStarted = beginImmediateDatabaseTransaction(options.database)
  try {
    const backfillState = readUsageStatsBackfillState(options.database, options.jobName)
    if (backfillState?.last_success_at) {
      commitDatabaseTransaction(options.database, transactionStarted)
      return { complete: true, processed: 0 }
    }

    if (!options.sourceCursor.cursorCreatedAt) {
      recordUsageStatsBackfillComplete(options.database, options.jobName, 'skipped')
      commitDatabaseTransaction(options.database, transactionStarted)
      return { complete: true, processed: 0 }
    }

    const cursorCreatedAt = backfillState?.cursor_created_at ?? ''
    const cursorId = backfillState?.cursor_id ?? ''
    const rows = readUsageStatsBackfillRows(options, cursorCreatedAt, cursorId)
    const updatedAt = nowIso()
    const aggregationContext = createUsageStatsAggregationContext(rows)
    for (const row of rows) {
      options.aggregateRecord(options.database, row, updatedAt, aggregationContext)
    }
    const last = rows[rows.length - 1]
    const complete = isUsageStatsBackfillComplete(last, rows.length, options.limit, options.sourceCursor)
    if (complete) {
      recordUsageStatsBackfillComplete(options.database, options.jobName, `processed:${rows.length}`, updatedAt)
    } else {
      recordUsageStatsBackfillProgress(options.database, options.jobName, last.created_at, last.id, updatedAt)
    }
    commitDatabaseTransaction(options.database, transactionStarted)
    return { complete, processed: rows.length }
  } catch (error) {
    rollbackDatabaseTransaction(options.database, transactionStarted)
    recordUsageStatsBackfillFailure(options.database, options.jobName, error, options.failureMessage)
    throw error
  }
}

function readUsageStatsBackfillState(database: DatabaseSync, jobName: string): UsageStatsBackfillJobStateRow | undefined {
  return database
    .prepare("SELECT cursor_created_at, cursor_id, last_success_at FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(jobName) as unknown as UsageStatsBackfillJobStateRow | undefined
}

function readUsageStatsBackfillRows(options: UsageStatsBackfillRunnerOptions, cursorCreatedAt: string, cursorId: string): UsageStatsRecordRow[] {
  return options.database
    .prepare(`
      SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
      FROM usage_records
      WHERE ${options.recordFilterSql}
        AND (created_at > ? OR (created_at = ? AND id > ?))
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(
      cursorCreatedAt,
      cursorCreatedAt,
      cursorId,
      options.sourceCursor.cursorCreatedAt,
      options.sourceCursor.cursorCreatedAt,
      options.sourceCursor.cursorId,
      options.limit
    ) as unknown as UsageStatsRecordRow[]
}

function isUsageStatsBackfillComplete(
  last: UsageStatsRecordRow | undefined,
  processed: number,
  limit: number,
  sourceCursor: UsageStatsBackfillCursor
): boolean {
  return !last
    || processed < limit
    || last.created_at > sourceCursor.cursorCreatedAt
    || (last.created_at === sourceCursor.cursorCreatedAt && last.id >= sourceCursor.cursorId)
}

function recordUsageStatsBackfillProgress(database: DatabaseSync, jobName: string, cursorCreatedAt: string, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, NULL, NULL, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = NULL,
      last_error_message = NULL,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `).run(jobName, cursorCreatedAt, cursorId, updatedAt)
}

function recordUsageStatsBackfillComplete(database: DatabaseSync, jobName: string, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', ?, ?, NULL, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `).run(jobName, cursorId, updatedAt, updatedAt)
}

function recordUsageStatsBackfillFailure(database: DatabaseSync, jobName: string, error: unknown, fallbackMessage: string): void {
  const updatedAt = nowIso()
  const message = error instanceof Error ? error.message : fallbackMessage
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', '', NULL, ?, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = NULL,
      last_error_message = excluded.last_error_message,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `).run(jobName, message, updatedAt)
}
