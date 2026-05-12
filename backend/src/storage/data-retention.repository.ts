import { getDatabase, nowIso } from './database.js'
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
  usageRankSnapshots: number
}

export interface SystemMetricsRetentionCleanupResult {
  systemMetricsSamples: number
  systemMetricsHourly: number
}

export function cleanupProcessedUsageRecordsBefore(cutoffCreatedAt: string, limit = 10000): number {
  const database = getDatabase()
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

function usageRecordsCleanupCursor(database: ReturnType<typeof getDatabase>): { cursorCreatedAt: string; cursorId: string } | undefined {
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

function requiredJobCursor(database: ReturnType<typeof getDatabase>, jobName: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const state = jobState(database, jobName)
  const cursorCreatedAt = state?.cursor_created_at?.trim()
  const cursorId = state?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function jobState(database: ReturnType<typeof getDatabase>, jobName: string): { cursor_created_at?: string | null; cursor_id?: string | null; last_success_at?: string | null } | undefined {
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
}): UsageStatsRetentionCleanupResult {
  const database = getDatabase()
  return {
    usageStatsMinute: changed(database.prepare('DELETE FROM usage_stats_minute WHERE stat_minute < ?').run(input.minuteCutoffMinute)),
    usageModelMinute: changed(database.prepare('DELETE FROM usage_model_minute WHERE stat_minute < ?').run(input.minuteCutoffMinute)),
    usageErrorMinute: changed(database.prepare('DELETE FROM usage_error_minute WHERE stat_minute < ?').run(input.minuteCutoffMinute)),
    usageLatencyMinute: changed(database.prepare('DELETE FROM usage_latency_minute WHERE stat_minute < ?').run(input.minuteCutoffMinute)),
    usageStatsDaily: changed(database.prepare('DELETE FROM usage_stats_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageModelDaily: changed(database.prepare('DELETE FROM usage_model_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageErrorDaily: changed(database.prepare('DELETE FROM usage_error_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageLatencyDaily: changed(database.prepare('DELETE FROM usage_latency_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageStatsHourly: changed(database.prepare('DELETE FROM usage_stats_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageModelHourly: changed(database.prepare('DELETE FROM usage_model_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageErrorHourly: changed(database.prepare('DELETE FROM usage_error_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageLatencyHourly: changed(database.prepare('DELETE FROM usage_latency_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageStatsWeekly: changed(database.prepare('DELETE FROM usage_stats_weekly WHERE stat_week < ?').run(input.weeklyCutoffWeek)),
    usageModelWeekly: changed(database.prepare('DELETE FROM usage_model_weekly WHERE stat_week < ?').run(input.weeklyCutoffWeek)),
    usageErrorWeekly: changed(database.prepare('DELETE FROM usage_error_weekly WHERE stat_week < ?').run(input.weeklyCutoffWeek)),
    usageLatencyWeekly: changed(database.prepare('DELETE FROM usage_latency_weekly WHERE stat_week < ?').run(input.weeklyCutoffWeek)),
    usageStatsMonthly: changed(database.prepare('DELETE FROM usage_stats_monthly WHERE stat_month < ?').run(input.monthlyCutoffMonth)),
    usageModelMonthly: changed(database.prepare('DELETE FROM usage_model_monthly WHERE stat_month < ?').run(input.monthlyCutoffMonth)),
    usageErrorMonthly: changed(database.prepare('DELETE FROM usage_error_monthly WHERE stat_month < ?').run(input.monthlyCutoffMonth)),
    usageLatencyMonthly: changed(database.prepare('DELETE FROM usage_latency_monthly WHERE stat_month < ?').run(input.monthlyCutoffMonth)),
    usageRankSnapshots: changed(database.prepare('DELETE FROM usage_rank_snapshots WHERE snapshot_at < ?').run(input.rankSnapshotCutoffIso))
  }
}

export function cleanupSystemMetricsBefore(input: { samplesCutoffIso: string; hourlyCutoffHour: string }): SystemMetricsRetentionCleanupResult {
  const database = getDatabase()
  return {
    systemMetricsSamples: changed(database.prepare('DELETE FROM system_metrics_samples WHERE sampled_at < ?').run(input.samplesCutoffIso)),
    systemMetricsHourly: changed(database.prepare('DELETE FROM system_metrics_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour))
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

  const database = getDatabase()
  const placeholders = sqlPlaceholders(ids.length)
  const result = database.prepare(`DELETE FROM ${tableName} WHERE id IN (${placeholders})`).run(...ids)
  return changed(result)
}

function positiveLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 10000
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}
