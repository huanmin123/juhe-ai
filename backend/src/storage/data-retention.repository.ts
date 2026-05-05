import { getDatabase, nowIso } from './database.js'
import { sqlPlaceholders } from './query-utils.js'

type CleanupRow = Record<string, unknown>

export interface UsageStatsRetentionCleanupResult {
  usageStatsDaily: number
  usageModelDaily: number
  usageErrorDaily: number
  usageStatsHourly: number
  usageModelHourly: number
  usageErrorHourly: number
}

export interface SystemMetricsRetentionCleanupResult {
  systemMetricsSamples: number
  systemMetricsHourly: number
}

export function cleanupProcessedUsageRecordsBefore(cutoffCreatedAt: string, limit = 10000): number {
  const database = getDatabase()
  const state = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  const cursorCreatedAt = state?.cursor_created_at?.trim()
  const cursorId = state?.cursor_id?.trim()
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

export function cleanupUsageStatsBucketsBefore(input: { dailyCutoffDate: string; hourlyCutoffHour: string }): UsageStatsRetentionCleanupResult {
  const database = getDatabase()
  return {
    usageStatsDaily: changed(database.prepare('DELETE FROM usage_stats_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageModelDaily: changed(database.prepare('DELETE FROM usage_model_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageErrorDaily: changed(database.prepare('DELETE FROM usage_error_daily WHERE stat_date < ?').run(input.dailyCutoffDate)),
    usageStatsHourly: changed(database.prepare('DELETE FROM usage_stats_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageModelHourly: changed(database.prepare('DELETE FROM usage_model_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour)),
    usageErrorHourly: changed(database.prepare('DELETE FROM usage_error_hourly WHERE stat_hour < ?').run(input.hourlyCutoffHour))
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
