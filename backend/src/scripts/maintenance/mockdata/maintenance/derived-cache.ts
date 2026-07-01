import { runtimeConfig } from '../../../../config/runtime.js'
import { refreshAccountQualityFromUsage } from '../../../../storage/account-quality.repository.js'
import {
  aggregateClientIpStatsBatch,
  rebuildClientIpUsageRangeWindows
} from '../../../../storage/client-ip-stats.repository.js'
import { getStatsDatabase, nowIso } from '../../../../storage/database.js'
import {
  aggregateUsageStatsBatch,
  refreshGroupAccountStatsCache,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageRankSnapshots
} from '../../../../storage/usage-stats.repository.js'
import type { DerivedCacheCounts } from '../shared.js'

type StatsDatabase = ReturnType<typeof getStatsDatabase>

export function rebuildDerivedCaches(statsDatabase: StatsDatabase): DerivedCacheCounts {
  assertSqliteMockdataMaintenance('rebuildDerivedCaches')
  resetUsageStatsCache(statsDatabase)
  let totalProcessed = 0
  while (true) {
    const processed = aggregateUsageStatsBatch(5000)
    totalProcessed += processed
    if (processed <= 0) break
  }
  refreshUsageRankSnapshots()
  refreshUsageQuotaHourlyWindowsCache()
  refreshGroupAccountStatsCache()
  const quality = refreshAccountQualityFromUsage(24 * 60)
  let clientIpProcessed = 0
  while (true) {
    const processed = aggregateClientIpStatsBatch(10000)
    clientIpProcessed += processed
    if (processed <= 0) break
  }
  rebuildClientIpUsageRangeWindows()
  console.log(`统计缓存已重建：聚合 ${totalProcessed} 条，用量质量刷新 ${quality.refreshed} 个账号，IP 统计聚合 ${clientIpProcessed} 条`)
  return {
    usageRecords: totalProcessed,
    accountQualityAccounts: quality.refreshed,
    clientIpRecords: clientIpProcessed
  }
}

function assertSqliteMockdataMaintenance(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.runtimeMode === 'performance') {
    throw new Error(`高性能 PG + Redis 模式禁止调用 SQLite mockdata 派生缓存重建入口：${operation}`)
  }
}

function resetUsageStatsCache(database: StatsDatabase): void {
  const updatedAt = nowIso()
  const usageStatsTables = [
    'usage_stats_totals',
    'usage_stats_minute',
    'usage_stats_hourly',
    'usage_stats_daily',
    'usage_stats_weekly',
    'usage_stats_monthly',
    'usage_model_minute',
    'usage_model_hourly',
    'usage_model_daily',
    'usage_model_weekly',
    'usage_model_monthly',
    'usage_error_minute',
    'usage_error_hourly',
    'usage_error_daily',
    'usage_error_weekly',
    'usage_error_monthly',
    'usage_latency_minute',
    'usage_latency_hourly',
    'usage_latency_daily',
    'usage_latency_weekly',
    'usage_latency_monthly',
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows',
    'usage_rank_snapshots',
    'usage_overview_summary_windows',
    'usage_overview_trend_windows',
    'usage_model_rank_windows',
    'usage_error_rank_windows',
    'ai_performance_summary_windows',
    'usage_quota_hourly_windows',
    'usage_scope_range_windows',
    'system_metrics_trend_windows',
    'process_event_loop_trend_windows',
    'account_quality_minute_stats',
    'account_quality_scores',
    'client_ip_registry',
    'client_ip_stats_daily',
    'client_ip_usage_range_windows',
    'client_ip_range_window_dirty_ips'
  ]
  database.exec('BEGIN')
  try {
    for (const tableName of usageStatsTables) {
      database.prepare(`DELETE FROM ${tableName}`).run()
    }
    database.prepare(`
      DELETE FROM stats_job_state
      WHERE job_name IN ('usage_stats_aggregation', 'client_ip_stats_aggregation', 'client_ip_range_window_refresh')
    `).run()
    database.prepare(`
      INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
      VALUES ('global', '', 'usage_stats_aggregation', '', '', NULL, NULL, 0, ?)
    `).run(updatedAt)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}
