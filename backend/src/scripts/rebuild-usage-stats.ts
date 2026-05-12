import { aggregateUsageStatsBatch, refreshUsageRankSnapshots } from '../storage/usage-stats.repository.js'
import { getDatabase, nowIso } from '../storage/database.js'
import { runtimeConfig } from '../config/runtime.js'

const batchSize = normalizeBatchSize(process.argv[2])

function main(): void {
  const database = getDatabase()
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
  console.log(`数据库：${runtimeConfig.databasePath}`)
}

function resetUsageStatsCache(database: ReturnType<typeof getDatabase>): void {
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
    'usage_rank_snapshots',
    'account_quality_minute_stats'
  ]
  const statsJobNames = [
    'usage_stats_aggregation',
    'caller_account_usage_stats_backfill',
    'account_quality_minute_stats_backfill',
    'usage_stats_extended_buckets_migration',
    'usage_model_error_extended_buckets_migration',
    'usage_latency_buckets_migration'
  ]
  database.exec('BEGIN')
  try {
    for (const tableName of usageStatsTables) {
      database.prepare(`DELETE FROM ${tableName}`).run()
    }
    const deleteState = database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    for (const jobName of statsJobNames) {
      deleteState.run(jobName)
    }
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
