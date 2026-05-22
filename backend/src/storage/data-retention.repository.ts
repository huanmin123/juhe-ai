import { getDatabase, getDatasetDatabase, getStatsDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

type CleanupRow = Record<string, unknown>
type DatasetDatabase = ReturnType<typeof getDatasetDatabase>
type StatsDatabase = ReturnType<typeof getStatsDatabase>

export interface UsageRecordsCleanupCursor {
  cursorCreatedAt?: string
  cursorId?: string
  blockedReason?: string
}

export interface ProcessedUsageRecordsCleanupBatchResult {
  cutoffCreatedAt: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
  deletedRows: number
  hasMore: boolean
  blockedReason?: string
}

export interface ProcessedUsageRecordsCleanupPreviewResult {
  cutoffCreatedAt: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
  eligibleRows: number
  hasMore: boolean
  blockedReason?: string
}

interface UsageRecordsCleanupBatchResult {
  cutoffCreatedAt: string
  deletedRows: number
  hasMore: boolean
}

export interface UsageRecordsCleanupPreviewResult {
  cutoffCreatedAt: string
  eligibleRows: number
  hasMore: boolean
}

export interface UsageStatsRetentionCleanupResult {
  accountQualityMinuteStats: number
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
  accountUsageSnapshots: number
}

export interface SystemMetricsRetentionCleanupResult {
  systemMetricsSamples: number
  systemMetricsHourly: number
  systemMetricsTrendWindows: number
  processEventLoopSamples: number
  processEventLoopHourly: number
  processEventLoopTrendWindows: number
}

export interface ModelCheckRetentionCleanupResult {
  modelCheckRuns: number
  modelCheckItems: number
}

export function cleanupProcessedUsageRecordsBefore(cutoffCreatedAt: string, limit = 10000): number {
  return cleanupProcessedUsageRecordsBeforeWithResult(cutoffCreatedAt, limit).deletedRows
}

export function inspectUsageRecordsCleanupBefore(cutoffCreatedAt: string, limit = 10000): UsageRecordsCleanupPreviewResult {
  const batchLimit = positiveLimit(limit)
  const rows = selectUsageRecordCleanupRows(getDatasetDatabase(), cutoffCreatedAt, batchLimit + 1)
  return {
    cutoffCreatedAt,
    eligibleRows: Math.min(rows.length, batchLimit),
    hasMore: rows.length > batchLimit
  }
}

function cleanupUsageRecordsBeforeWithResult(cutoffCreatedAt: string, limit = 10000): UsageRecordsCleanupBatchResult {
  const batchLimit = positiveLimit(limit)
  const database = getDatasetDatabase()
  const rows = selectUsageRecordCleanupRows(database, cutoffCreatedAt, batchLimit + 1)
  return {
    cutoffCreatedAt,
    deletedRows: deleteRowsById(database, 'usage_records', rows.slice(0, batchLimit)),
    hasMore: rows.length > batchLimit
  }
}

export function inspectProcessedUsageRecordsCleanupBefore(cutoffCreatedAt: string, limit = 10000): ProcessedUsageRecordsCleanupPreviewResult {
  const database = getDatasetDatabase()
  const cursor = usageRecordsCleanupCursor(getStatsDatabase())
  const cursorCreatedAt = cursor?.cursorCreatedAt
  const cursorId = cursor?.cursorId
  if (!cursorCreatedAt || !cursorId) {
    if (!hasUsageRecordsBefore(database, cutoffCreatedAt)) {
      return {
        cutoffCreatedAt,
        eligibleRows: 0,
        hasMore: false
      }
    }
    return {
      cutoffCreatedAt,
      eligibleRows: 0,
      hasMore: false,
      blockedReason: cursor?.blockedReason ?? '统计安全游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试'
    }
  }

  const batchLimit = positiveLimit(limit)
  const rows = selectProcessedUsageRecordCleanupRows(database, cutoffCreatedAt, cursorCreatedAt, cursorId, batchLimit + 1)
  return {
    cutoffCreatedAt,
    safetyCursorCreatedAt: cursorCreatedAt,
    safetyCursorId: cursorId,
    eligibleRows: Math.min(rows.length, batchLimit),
    hasMore: rows.length > batchLimit
  }
}

export function cleanupProcessedUsageRecordsBeforeWithResult(cutoffCreatedAt: string, limit = 10000): ProcessedUsageRecordsCleanupBatchResult {
  const database = getDatasetDatabase()
  const cursor = usageRecordsCleanupCursor(getStatsDatabase())
  const cursorCreatedAt = cursor?.cursorCreatedAt
  const cursorId = cursor?.cursorId
  if (!cursorCreatedAt || !cursorId) {
    if (!hasUsageRecordsBefore(database, cutoffCreatedAt)) {
      return {
        cutoffCreatedAt,
        deletedRows: 0,
        hasMore: false
      }
    }
    return {
      cutoffCreatedAt,
      deletedRows: 0,
      hasMore: false,
      blockedReason: cursor?.blockedReason ?? '统计安全游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试'
    }
  }

  const batchLimit = positiveLimit(limit)
  const rows = selectProcessedUsageRecordCleanupRows(database, cutoffCreatedAt, cursorCreatedAt, cursorId, batchLimit + 1)
  return {
    cutoffCreatedAt,
    safetyCursorCreatedAt: cursorCreatedAt,
    safetyCursorId: cursorId,
    deletedRows: deleteRowsById('usage_records', rows.slice(0, batchLimit)),
    hasMore: rows.length > batchLimit
  }
}

function selectProcessedUsageRecordCleanupRows(
  database: DatasetDatabase,
  cutoffCreatedAt: string,
  cursorCreatedAt: string,
  cursorId: string,
  limit: number
): CleanupRow[] {
  return database
    .prepare(`
      SELECT id
      FROM usage_records
      WHERE created_at < ?
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, cursorCreatedAt, cursorCreatedAt, cursorId, positiveLimit(limit)) as CleanupRow[]
}

function selectUsageRecordCleanupRows(
  database: DatasetDatabase,
  cutoffCreatedAt: string,
  limit: number
): CleanupRow[] {
  return database
    .prepare(`
      SELECT id
      FROM usage_records
      WHERE created_at < ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, positiveLimit(limit)) as CleanupRow[]
}

function hasUsageRecordsBefore(database: DatasetDatabase, cutoffCreatedAt: string): boolean {
  const row = database
    .prepare('SELECT id FROM usage_records WHERE created_at < ? LIMIT 1')
    .get(cutoffCreatedAt) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

export function usageRecordsCleanupCursor(database: StatsDatabase): UsageRecordsCleanupCursor {
  const aggregationCursor = requiredJobCursor(database, 'usage_stats_aggregation')
  if (!aggregationCursor) {
    return {
      blockedReason: '统计聚合游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试'
    }
  }
  return aggregationCursor
}

function requiredJobCursor(database: StatsDatabase, jobName: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const state = jobState(database, jobName)
  const cursorCreatedAt = state?.cursor_created_at?.trim()
  const cursorId = state?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function jobState(database: StatsDatabase, jobName: string): { cursor_created_at?: string | null; cursor_id?: string | null } | undefined {
  return database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(jobName) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
}

export function cleanupUsageStatsBucketsBefore(input: {
  accountQualityMinuteCutoffMinute: string
  minuteCutoffMinute: string
  hourlyCutoffHour: string
  dailyCutoffDate: string
  weeklyCutoffWeek: string
  monthlyCutoffMonth: string
  rankSnapshotCutoffIso: string
  windowCutoffDate: string
  windowCutoffIso: string
  limit?: number
}): UsageStatsRetentionCleanupResult {
  const database = getStatsDatabase()
  const limit = positiveLimit(input.limit)
  return runInDatabaseTransaction(() => ({
    accountQualityMinuteStats: deleteRowsBeforeByRowid(database, 'account_quality_minute_stats', 'stat_minute', input.accountQualityMinuteCutoffMinute, limit),
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
    authorizationTeamUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_team_usage_range_windows', 'end_date', input.windowCutoffDate, limit),
    authorizationUserUsageSummaryDaily: deleteRowsBeforeByRowid(database, 'authorization_user_usage_summary_daily', 'stat_date', input.dailyCutoffDate, limit),
    authorizationUserUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_user_usage_range_windows', 'end_date', input.windowCutoffDate, limit),
    usageRankSnapshots: deleteRowsBeforeByRowid(database, 'usage_rank_snapshots', 'snapshot_at', input.rankSnapshotCutoffIso, limit),
    usageOverviewSummaryWindows: deleteRowsBeforeByRowid(database, 'usage_overview_summary_windows', 'end_date', input.windowCutoffDate, limit),
    usageOverviewTrendWindows: deleteRowsBeforeByRowid(database, 'usage_overview_trend_windows', 'end_date', input.windowCutoffDate, limit),
    usageModelRankWindows: deleteRowsBeforeByRowid(database, 'usage_model_rank_windows', 'end_date', input.windowCutoffDate, limit),
    usageErrorRankWindows: deleteRowsBeforeByRowid(database, 'usage_error_rank_windows', 'end_date', input.windowCutoffDate, limit),
    aiPerformanceSummaryWindows: deleteRowsBeforeByRowid(database, 'ai_performance_summary_windows', 'end_date', input.windowCutoffDate, limit),
    usageQuotaHourlyWindows: deleteRowsBeforeByRowid(database, 'usage_quota_hourly_windows', 'updated_at', input.windowCutoffIso, limit),
    usageScopeRangeWindows: deleteRowsBeforeByRowid(database, 'usage_scope_range_windows', 'end_date', input.windowCutoffDate, limit),
    accountUsageSnapshots: deleteRowsBeforeByRowid(database, 'account_usage_snapshots', 'updated_at', input.windowCutoffIso, limit)
  }), database)
}

export function cleanupSystemMetricsBefore(input: { samplesCutoffIso: string; hourlyCutoffHour: string; trendWindowCutoffDate: string; limit?: number }): SystemMetricsRetentionCleanupResult {
  const database = getStatsDatabase()
  const limit = positiveLimit(input.limit)
  return runInDatabaseTransaction(() => ({
    systemMetricsSamples: deleteRowsBeforeByRowid(database, 'system_metrics_samples', 'sampled_at', input.samplesCutoffIso, limit),
    systemMetricsHourly: deleteRowsBeforeByRowid(database, 'system_metrics_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    systemMetricsTrendWindows: deleteRowsBeforeByRowid(database, 'system_metrics_trend_windows', 'end_date', input.trendWindowCutoffDate, limit),
    processEventLoopSamples: deleteRowsBeforeByRowid(database, 'process_event_loop_samples', 'sampled_at', input.samplesCutoffIso, limit),
    processEventLoopHourly: deleteRowsBeforeByRowid(database, 'process_event_loop_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    processEventLoopTrendWindows: deleteRowsBeforeByRowid(database, 'process_event_loop_trend_windows', 'end_date', input.trendWindowCutoffDate, limit)
  }), database)
}

export function cleanupModelCheckRunsBefore(cutoffCreatedAt: string, limit = 10000): ModelCheckRetentionCleanupResult {
  const database = getDatasetDatabase()
  const batchLimit = positiveLimit(limit)
  const rows = database
    .prepare(`
      SELECT id
      FROM model_check_runs
      WHERE created_at < ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, batchLimit) as CleanupRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) {
    return {
      modelCheckRuns: 0,
      modelCheckItems: 0
    }
  }

  return runInDatabaseTransaction(() => {
    let modelCheckItems = 0
    let modelCheckRuns = 0
    for (const chunk of chunkValues(ids, 900)) {
      const placeholders = sqlPlaceholders(chunk.length)
      modelCheckItems += changed(database.prepare(`DELETE FROM model_check_items WHERE run_id IN (${placeholders})`).run(...chunk))
      modelCheckRuns += changed(database.prepare(`DELETE FROM model_check_runs WHERE id IN (${placeholders})`).run(...chunk))
    }
    return {
      modelCheckRuns,
      modelCheckItems
    }
  }, database)
}

export function cleanupExpiredSystemSessions(expiredBefore = nowIso()): number {
  return changed(getDatabase().prepare('DELETE FROM system_sessions WHERE expires_at < ?').run(expiredBefore))
}

function deleteRowsById(database: DatasetDatabase, tableName: 'usage_records', rows: CleanupRow[]): number {
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) {
    return 0
  }

  const placeholders = sqlPlaceholders(ids.length)
  const result = database.prepare(`DELETE FROM ${tableName} WHERE id IN (${placeholders})`).run(...ids)
  return changed(result)
}

function deleteRowsBeforeByRowid(
  database: StatsDatabase,
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
