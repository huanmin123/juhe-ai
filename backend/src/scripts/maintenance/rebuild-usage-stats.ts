import { aggregateUsageStatsBatch, refreshUsageRankSnapshotsInStages } from '../../storage/usage-stats.repository.js'
import { datasetDatabasePath, getDatasetDatabase, getStatsDatabase, getUsageCatalogDatabase, nowIso, statsDatabasePath, usageCatalogDatabasePath } from '../../storage/database.js'
import { runtimeConfig } from '../../config/runtime.js'

interface RebuildUsageStatsOptions {
  batchSize: number
  maxBatches: number
  confirmOffline: boolean
  refreshRankSnapshots: boolean
}

async function main(): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 高性能模式暂不支持 SQLite 用量统计重建脚本，请使用 PostgreSQL 专用离线重建流程')
  }
  const options = parseOptions(process.argv.slice(2))
  if (!options.confirmOffline && process.env.JUHE_AI_CONFIRM_USAGE_STATS_REBUILD !== '1') {
    throw new Error('重建用量统计必须显式确认停服/离线执行：追加 --confirm-offline，或设置 JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1')
  }

  getDatasetDatabase()
  getUsageCatalogDatabase()
  const database = getStatsDatabase()
  const startedAt = Date.now()
  resetUsageStatsCache(database)

  let totalProcessed = 0
  let batches = 0
  while (batches < options.maxBatches) {
    const processed = aggregateUsageStatsBatch(options.batchSize)
    batches += 1
    totalProcessed += processed
    if (processed <= 0) {
      break
    }
    await yieldToEventLoop()
  }
  if (options.refreshRankSnapshots) {
    await refreshUsageRankSnapshotsInStages({ yieldToEventLoop })
  }

  const durationMs = Date.now() - startedAt
  const capped = batches >= options.maxBatches
    ? `，已达到本轮批次上限 ${options.maxBatches}，如仍有统计滞后请再次离线执行`
    : ''
  console.log(`用量统计已重建：扫描 ${totalProcessed} 条记录，批次 ${batches}，耗时 ${durationMs}ms${capped}`)
  console.log(`数据集目录库：${datasetDatabasePath()}`)
  console.log(`使用记录目录库：${usageCatalogDatabasePath()}`)
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
    database.prepare("DELETE FROM stats_job_state WHERE job_name = 'usage_stats_aggregation'").run()
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

function normalizeMaxBatches(value?: string): number {
  const number = Number(value ?? 1000)
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), 1), 10000) : 1000
}

function parseOptions(args: string[]): RebuildUsageStatsOptions {
  let batchSize: string | undefined
  let maxBatches: string | undefined
  let confirmOffline = false
  let refreshRankSnapshots = true
  for (const arg of args) {
    if (arg === '--confirm-offline') {
      confirmOffline = true
      continue
    }
    if (arg === '--skip-rank-refresh') {
      refreshRankSnapshots = false
      continue
    }
    if (arg.startsWith('--batch-size=')) {
      batchSize = arg.slice('--batch-size='.length)
      continue
    }
    if (arg.startsWith('--max-batches=')) {
      maxBatches = arg.slice('--max-batches='.length)
      continue
    }
    if (!arg.startsWith('--') && batchSize === undefined) {
      batchSize = arg
    }
  }
  return {
    batchSize: normalizeBatchSize(batchSize),
    maxBatches: normalizeMaxBatches(maxBatches),
    confirmOffline,
    refreshRankSnapshots
  }
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
