import { aggregateUsageStatsBatch, refreshUsageRankSnapshots } from '../../storage/usage-stats.repository.js'
import { datasetDatabasePath, getDatasetDatabase, getStatsDatabase, nowIso, statsDatabasePath } from '../../storage/database.js'

const batchSize = normalizeBatchSize(process.argv[2])

function main(): void {
  getDatasetDatabase()
  const database = getStatsDatabase()
  const startedAt = Date.now()
  resetUsageStatsCache(database)

  let totalProcessed = 0
  while (true) {
    const processed = aggregateUsageStatsBatch(batchSize)
    totalProcessed += processed
    if (processed <= 0) {
      break
    }
  }
  refreshUsageRankSnapshots()

  const durationMs = Date.now() - startedAt
  console.log(`用量统计已重建：扫描 ${totalProcessed} 条记录，耗时 ${durationMs}ms`)
  console.log(`数据集库：${datasetDatabasePath()}`)
  console.log(`统计结果库：${statsDatabasePath()}`)
}

function resetUsageStatsCache(database: ReturnType<typeof getStatsDatabase>): void {
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
    'account_quality_minute_stats'
  ]
  database.exec('BEGIN')
  try {
    for (const tableName of usageStatsTables) {
      database.prepare(`DELETE FROM ${tableName}`).run()
    }
    database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'").run()
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

function normalizeBatchSize(value?: string): number {
  const number = Number(value ?? 2000)
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), 1), 50000) : 2000
}

main()
