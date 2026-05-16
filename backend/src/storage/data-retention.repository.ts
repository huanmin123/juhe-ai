import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import { sqlPlaceholders } from './query-utils.js'

type CleanupRow = Record<string, unknown>

export interface UsageStatsRetentionCleanupResult {
  usageStatsMinute: number
  usageModelMinute: number
  usageErrorMinute: number
  usageLatencyMinute: number
  usageStatsDaily: number
  usageModelDaily: number
  usageErrorDaily: number
  usageLatencyDaily: number
  usageStatsHourly: number
  usageModelHourly: number
  usageErrorHourly: number
  usageLatencyHourly: number
  usageStatsWeekly: number
  usageModelWeekly: number
  usageErrorWeekly: number
  usageLatencyWeekly: number
  usageStatsMonthly: number
  usageModelMonthly: number
  usageErrorMonthly: number
  usageLatencyMonthly: number
  authorizationTeamUsageSummaryDaily: number
  authorizationTeamUsageRangeWindows: number
  authorizationUserUsageSummaryDaily: number
  authorizationUserUsageRangeWindows: number
  usageRankSnapshots: number
  usageOverviewSummaryWindows: number
  usageOverviewTrendWindows: number
  usageModelRankWindows: number
  usageErrorRankWindows: number
  aiPerformanceSummaryWindows: number
  usageQuotaHourlyWindows: number
  usageScopeRangeWindows: number
}

export interface SystemMetricsRetentionCleanupResult {
  systemMetricsSamples: number
  systemMetricsHourly: number
}

export function cleanupProcessedUsageRecordsBefore(cutoffCreatedAt: string, limit = 10000): number {
  const database = getRecordDatabase()
  const cursor = usageRecordsCleanupCursor(database)
  const cursorCreatedAt = cursor?.cursorCreatedAt
  const cursorId = cursor?.cursorId
  if (!cursorCreatedAt || !cursorId) {
    return 0
  }

  const rows = database
    .prepare(`
      SELECT id
      FROM usage_records
      WHERE created_at < ?
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, cursorCreatedAt, cursorCreatedAt, cursorId, positiveLimit(limit)) as CleanupRow[]
  return deleteRowsById('usage_records', rows)
}

function usageRecordsCleanupCursor(database: ReturnType<typeof getRecordDatabase>): { cursorCreatedAt: string; cursorId: string } | undefined {
  const aggregationCursor = requiredJobCursor(database, 'usage_stats_aggregation')
  if (!aggregationCursor) return undefined

  const backfillJobs = [
    'caller_account_usage_stats_backfill',
    'account_quality_minute_stats_backfill',
    'usage_stats_extended_buckets_migration',
    'usage_model_error_extended_buckets_migration',
    'usage_latency_buckets_migration'
  ]
  let cleanupCursor = aggregationCursor
  for (const jobName of backfillJobs) {
    const state = jobState(database, jobName)
    if (!state?.last_success_at) {
      const backfillCursor = state?.cursor_created_at && state.cursor_id
        ? { cursorCreatedAt: state.cursor_created_at, cursorId: state.cursor_id }
        : undefined
      if (!backfillCursor) return undefined
      cleanupCursor = earlierCursor(cleanupCursor, backfillCursor)
    }
  }
  return cleanupCursor
}

function requiredJobCursor(database: ReturnType<typeof getRecordDatabase>, jobName: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const state = jobState(database, jobName)
  const cursorCreatedAt = state?.cursor_created_at?.trim()
  const cursorId = state?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function jobState(database: ReturnType<typeof getRecordDatabase>, jobName: string): { cursor_created_at?: string | null; cursor_id?: string | null; last_success_at?: string | null } | undefined {
  return database
    .prepare("SELECT cursor_created_at, cursor_id, last_success_at FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(jobName) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null; last_success_at?: string | null } | undefined
}

function earlierCursor(
  left: { cursorCreatedAt: string; cursorId: string },
  right: { cursorCreatedAt: string; cursorId: string }
): { cursorCreatedAt: string; cursorId: string } {
  if (left.cursorCreatedAt < right.cursorCreatedAt) return left
  if (left.cursorCreatedAt > right.cursorCreatedAt) return right
  return left.cursorId <= right.cursorId ? left : right
}

export function cleanupUsageStatsBucketsBefore(input: {
  minuteCutoffMinute: string
  hourlyCutoffHour: string
  dailyCutoffDate: string
  weeklyCutoffWeek: string
  monthlyCutoffMonth: string
  rankSnapshotCutoffIso: string
  limit?: number
}): UsageStatsRetentionCleanupResult {
  const database = getRecordDatabase()
  const limit = positiveLimit(input.limit)
  return {
    usageStatsMinute: deleteRowsBeforeByRowid(database, 'usage_stats_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageModelMinute: deleteRowsBeforeByRowid(database, 'usage_model_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageErrorMinute: deleteRowsBeforeByRowid(database, 'usage_error_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageLatencyMinute: deleteRowsBeforeByRowid(database, 'usage_latency_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageStatsDaily: deleteRowsBeforeByRowid(database, 'usage_stats_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageModelDaily: deleteRowsBeforeByRowid(database, 'usage_model_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageErrorDaily: deleteRowsBeforeByRowid(database, 'usage_error_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageLatencyDaily: deleteRowsBeforeByRowid(database, 'usage_latency_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageStatsHourly: deleteRowsBeforeByRowid(database, 'usage_stats_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageModelHourly: deleteRowsBeforeByRowid(database, 'usage_model_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageErrorHourly: deleteRowsBeforeByRowid(database, 'usage_error_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageLatencyHourly: deleteRowsBeforeByRowid(database, 'usage_latency_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageStatsWeekly: deleteRowsBeforeByRowid(database, 'usage_stats_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageModelWeekly: deleteRowsBeforeByRowid(database, 'usage_model_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageErrorWeekly: deleteRowsBeforeByRowid(database, 'usage_error_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageLatencyWeekly: deleteRowsBeforeByRowid(database, 'usage_latency_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageStatsMonthly: deleteRowsBeforeByRowid(database, 'usage_stats_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageModelMonthly: deleteRowsBeforeByRowid(database, 'usage_model_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageErrorMonthly: deleteRowsBeforeByRowid(database, 'usage_error_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageLatencyMonthly: deleteRowsBeforeByRowid(database, 'usage_latency_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    authorizationTeamUsageSummaryDaily: deleteRowsBeforeByRowid(database, 'authorization_team_usage_summary_daily', 'stat_date', input.dailyCutoffDate, limit),
    authorizationTeamUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_team_usage_range_windows', 'end_date', input.dailyCutoffDate, limit),
    authorizationUserUsageSummaryDaily: deleteRowsBeforeByRowid(database, 'authorization_user_usage_summary_daily', 'stat_date', input.dailyCutoffDate, limit),
    authorizationUserUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_user_usage_range_windows', 'end_date', input.dailyCutoffDate, limit),
    usageRankSnapshots: deleteRowsBeforeByRowid(database, 'usage_rank_snapshots', 'snapshot_at', input.rankSnapshotCutoffIso, limit),
    usageOverviewSummaryWindows: 0,
    usageOverviewTrendWindows: 0,
    usageModelRankWindows: 0,
    usageErrorRankWindows: 0,
    aiPerformanceSummaryWindows: 0,
    usageQuotaHourlyWindows: 0,
    usageScopeRangeWindows: deleteRowsBeforeByRowid(database, 'usage_scope_range_windows', 'end_date', input.dailyCutoffDate, limit)
  }
}

export function cleanupSystemMetricsBefore(input: { samplesCutoffIso: string; hourlyCutoffHour: string; limit?: number }): SystemMetricsRetentionCleanupResult {
  const database = getRecordDatabase()
  const limit = positiveLimit(input.limit)
  return {
    systemMetricsSamples: deleteRowsBeforeByRowid(database, 'system_metrics_samples', 'sampled_at', input.samplesCutoffIso, limit),
    systemMetricsHourly: deleteRowsBeforeByRowid(database, 'system_metrics_hourly', 'stat_hour', input.hourlyCutoffHour, limit)
  }
}

export function cleanupExpiredSystemSessions(expiredBefore = nowIso()): number {
  return changed(getDatabase().prepare('DELETE FROM system_sessions WHERE expires_at < ?').run(expiredBefore))
}

function deleteRowsById(tableName: 'usage_records', rows: CleanupRow[]): number {
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) {
    return 0
  }

  const database = getRecordDatabase()
  const placeholders = sqlPlaceholders(ids.length)
  const result = database.prepare(`DELETE FROM ${tableName} WHERE id IN (${placeholders})`).run(...ids)
  return changed(result)
}

function deleteRowsBeforeByRowid(
  database: ReturnType<typeof getRecordDatabase>,
  tableName: string,
  timeColumnName: string,
  cutoffValue: string,
  limit: number
): number {
  const result = database.prepare(`
    DELETE FROM ${tableName}
    WHERE rowid IN (
      SELECT rowid
      FROM ${tableName}
      WHERE ${timeColumnName} < ?
      ORDER BY ${timeColumnName} ASC, rowid ASC
      LIMIT ?
    )
  `).run(cutoffValue, positiveLimit(limit))
  return changed(result)
}

function positiveLimit(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 10000
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}
