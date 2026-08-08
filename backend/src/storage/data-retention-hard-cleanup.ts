import type { DatabaseSync } from 'node:sqlite'

import { getDatasetDatabase, getStatsDatabase, getUsageCatalogDatabase } from './database.js'
import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, usageStatsTimezoneAsync, weekKey } from './usage-stats-helpers.js'

export type HardCleanupCutoffKey = 'iso' | 'minute' | 'hour' | 'date' | 'week' | 'month'
export type HardCleanupDatabaseRole = 'dataset' | 'usage-catalog' | 'stats'

export interface HardCleanupCutoffs extends Record<HardCleanupCutoffKey, string> {}

interface HardCleanupTableRule {
  databaseRole: HardCleanupDatabaseRole
  tableName: string
  timeColumnName: string
  cutoffKey: HardCleanupCutoffKey
}

const nonBusinessDatasetCleanupTables: HardCleanupTableRule[] = [
  { databaseRole: 'dataset', tableName: 'model_check_items', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'model_check_runs', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'operation_log_targets', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'operation_log_viewers', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'operation_log_summary_search_terms', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'operation_logs', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'public_api_logs', timeColumnName: 'created_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'api_key_record_cleanup_targets', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'dataset', tableName: 'account_record_cleanup_targets', timeColumnName: 'updated_at', cutoffKey: 'iso' }
]

const nonBusinessUsageCatalogCleanupTables: HardCleanupTableRule[] = [
  { databaseRole: 'usage-catalog', tableName: 'usage_record_account_shards', timeColumnName: 'last_seen_at', cutoffKey: 'iso' },
  { databaseRole: 'usage-catalog', tableName: 'usage_record_api_key_shards', timeColumnName: 'last_seen_at', cutoffKey: 'iso' }
]

const nonBusinessStatsCleanupTables: HardCleanupTableRule[] = [
  { databaseRole: 'stats', tableName: 'account_quality_minute_stats', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
  { databaseRole: 'stats', tableName: 'group_account_stats', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'account_quality_scores', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'account_quality_dirty_accounts', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'account_usage_snapshots', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'usage_stats_totals', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'usage_stats_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
  { databaseRole: 'stats', tableName: 'usage_stats_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'usage_stats_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_stats_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
  { databaseRole: 'stats', tableName: 'usage_stats_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
  { databaseRole: 'stats', tableName: 'authorization_team_usage_summary_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'authorization_team_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'authorization_user_usage_summary_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'authorization_user_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_model_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
  { databaseRole: 'stats', tableName: 'usage_model_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'usage_model_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_model_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
  { databaseRole: 'stats', tableName: 'usage_model_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
  { databaseRole: 'stats', tableName: 'usage_error_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
  { databaseRole: 'stats', tableName: 'usage_error_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'usage_error_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_error_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
  { databaseRole: 'stats', tableName: 'usage_error_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
  { databaseRole: 'stats', tableName: 'usage_latency_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
  { databaseRole: 'stats', tableName: 'usage_latency_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'usage_latency_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_latency_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
  { databaseRole: 'stats', tableName: 'usage_latency_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
  { databaseRole: 'stats', tableName: 'usage_rank_snapshots', timeColumnName: 'snapshot_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'usage_overview_summary_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_overview_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_model_rank_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_error_rank_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'ai_performance_summary_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_quota_hourly_windows', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'usage_scope_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'usage_range_window_requests', timeColumnName: 'expires_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'client_ip_registry', timeColumnName: 'last_seen_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'client_ip_stats_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'client_ip_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'client_ip_range_window_dirty_ips', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'client_ip_policy_hits', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'client_ip_account_stats_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'client_ip_account_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'client_ip_account_range_window_dirty_ips', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'usage_record_cleanup_deductions', timeColumnName: 'updated_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'system_metrics_samples', timeColumnName: 'sampled_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'system_metrics_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'system_metrics_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
  { databaseRole: 'stats', tableName: 'process_event_loop_samples', timeColumnName: 'sampled_at', cutoffKey: 'iso' },
  { databaseRole: 'stats', tableName: 'process_event_loop_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
  { databaseRole: 'stats', tableName: 'process_event_loop_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
]

const nonBusinessCleanupTablesByRole: Record<HardCleanupDatabaseRole, HardCleanupTableRule[]> = {
  dataset: nonBusinessDatasetCleanupTables,
  'usage-catalog': nonBusinessUsageCatalogCleanupTables,
  stats: nonBusinessStatsCleanupTables
}

export function cleanupDiscoveredHardCleanupTablesBefore(
  databaseRole: HardCleanupDatabaseRole,
  cutoffs: HardCleanupCutoffs,
  limit: number,
  addRows: (key: string, count: number) => void
): void {
  const database = databaseForHardCleanupRole(databaseRole)
	for (const rule of nonBusinessCleanupTablesByRole[databaseRole]) {
    const deleted = deleteRowsBeforeByRowid(
      database,
      rule.tableName,
      rule.timeColumnName,
      cutoffs[rule.cutoffKey],
      limit
    )
    addRows(`${rule.databaseRole}.${rule.tableName}`, deleted)
  }
}

export function hardCleanupCutoffs(cutoffAt: string, timezone = usageStatsTimezone()): HardCleanupCutoffs {
  const cutoffDate = new Date(cutoffAt)
  if (!Number.isFinite(cutoffDate.getTime())) {
    throw new Error('非业务数据清理截止时间无效')
  }
  return {
    iso: cutoffDate.toISOString(),
    minute: minuteKey(cutoffDate, timezone),
    hour: hourKey(cutoffDate, timezone),
    date: dateKey(cutoffDate, timezone),
    week: weekKey(cutoffDate, timezone),
    month: monthKey(cutoffDate, timezone)
  }
}

export async function hardCleanupCutoffsAsync(cutoffAt: string): Promise<HardCleanupCutoffs> {
  return hardCleanupCutoffs(cutoffAt, await usageStatsTimezoneAsync())
}

export function deleteRowsBeforeByRowid(
  database: DatabaseSync,
  tableName: string,
  timeColumnName: string,
  cutoffValue: string,
  limit: number
): number {
  const quotedTableName = quoteSqliteIdentifier(tableName)
  const quotedTimeColumnName = quoteSqliteIdentifier(timeColumnName)
  const result = database.prepare(`
    DELETE FROM ${quotedTableName}
    WHERE rowid IN (
      SELECT rowid
      FROM ${quotedTableName}
      WHERE ${quotedTimeColumnName} < ?
      ORDER BY ${quotedTimeColumnName} ASC, rowid ASC
      LIMIT ?
    )
  `).run(cutoffValue, positiveDeleteLimit(limit))
  return changedRows(result)
}

function databaseForHardCleanupRole(databaseRole: HardCleanupDatabaseRole): DatabaseSync {
  if (databaseRole === 'dataset') {
    return getDatasetDatabase()
  }
  if (databaseRole === 'usage-catalog') {
    return getUsageCatalogDatabase()
  }
  return getStatsDatabase()
}

function quoteSqliteIdentifier(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

function positiveDeleteLimit(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 10000
}

function changedRows(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}
