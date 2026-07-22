import type { DatabaseClient } from '../../storage/database-client.js'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'temporary-maintenance-worker'

const { runtimeConfig } = await import('../../config/runtime.js')
const offlineConfirmed = process.argv.includes('--confirm-offline')
  || process.env.JUHE_AI_CONFIRM_USAGE_STATS_REBUILD === '1'
if (runtimeConfig.databaseDriver === 'sqlite' && offlineConfirmed) {
  process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE = '1'
}

const [usageStatsRepository, databaseModule, databaseClientModule, postgresModule] = await Promise.all([
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/database.js'),
  import('../../storage/database-client.js'),
  import('../../storage/postgres-client.js')
])
const { aggregateUsageStatsBatch, aggregateUsageStatsBatchAsync, refreshUsageRankSnapshotsInStages } = usageStatsRepository
const {
  closeStorageDatabases,
  datasetDatabasePath,
  getDatasetDatabase,
  getStatsDatabase,
  getUsageCatalogDatabase,
  nowIso,
  statsDatabasePath,
  usageCatalogDatabasePath
} = databaseModule
const { createPostgresDatabaseClient } = databaseClientModule
const { closePostgresPool, getPostgresPool } = postgresModule

interface RebuildUsageStatsOptions {
  batchSize: number
  maxBatches: number
  confirmOffline: boolean
  refreshRankSnapshots: boolean
  help: boolean
}

async function main(): Promise<void> {
  const options = parseOptions(process.argv.slice(2))
  if (options.help) {
    printHelp()
    return
  }
  if (!options.confirmOffline && process.env.JUHE_AI_CONFIRM_USAGE_STATS_REBUILD !== '1') {
    throw new Error('重建用量统计必须显式确认停服/离线执行：追加 --confirm-offline，或设置 JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1')
  }

  const startedAt = Date.now()
  if (runtimeConfig.databaseDriver === 'postgres') {
    await resetUsageStatsCacheAsync()
  } else {
    getDatasetDatabase()
    getUsageCatalogDatabase()
    resetUsageStatsCache(getStatsDatabase())
  }

  let totalProcessed = 0
  let batches = 0
  while (batches < options.maxBatches) {
    const processed = runtimeConfig.databaseDriver === 'postgres'
      ? await aggregateUsageStatsBatchAsync(options.batchSize)
      : aggregateUsageStatsBatch(options.batchSize)
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
  if (runtimeConfig.databaseDriver === 'postgres') {
    console.log('PostgreSQL schema：juhe_usage -> juhe_stats')
  } else {
    console.log(`数据集目录库：${datasetDatabasePath()}`)
    console.log(`使用记录目录库：${usageCatalogDatabasePath()}`)
    console.log(`统计结果库：${statsDatabasePath()}`)
  }
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
    'account_quality_minute_stats',
    'account_quality_scores',
    'account_quality_dirty_accounts'
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

async function resetUsageStatsCacheAsync(): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
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
    'account_quality_minute_stats',
    'account_quality_scores',
    'account_quality_dirty_accounts'
  ]
  await client.transaction(async (tx) => {
    for (const tableName of usageStatsTables) {
      await tx.execute(`DELETE FROM ${statsTable(tx, tableName)}`)
    }
    await tx.execute("DELETE FROM juhe_stats.stats_job_state WHERE job_name = 'usage_stats_aggregation'")
    await tx.execute(`
      INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
      VALUES ('global', '', 'usage_stats_aggregation', '', '', NULL, NULL, 0, ?)
    `, [updatedAt])
  })
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
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
  let help = false
  for (const arg of args) {
    if (arg === '--help' || arg === '-h') {
      help = true
      continue
    }
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
      continue
    }
    throw new Error(`未知参数：${arg}`)
  }
  return {
    batchSize: normalizeBatchSize(batchSize),
    maxBatches: normalizeMaxBatches(maxBatches),
    confirmOffline,
    refreshRankSnapshots,
    help
  }
}

function printHelp(): void {
  console.log(`
用法：
  pnpm --filter juhe-ai-backend maintenance:rebuild-usage-stats -- --confirm-offline
  pnpm --filter juhe-ai-backend maintenance:rebuild-usage-stats -- --confirm-offline --batch-size=5000 --max-batches=2000

说明：
  - 必须停服或确认没有网关/worker 写入后离线执行。
  - SQLite 模式会清空统计结果库派生表，从 usage shard 文件重建统计。
  - PostgreSQL 模式会清空 juhe_stats 派生表，从 juhe_usage.usage_records 重建统计。
  - 默认会刷新排行快照；如只重建基础聚合，可追加 --skip-rank-refresh。
`)
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

main()
  .catch((error) => {
    console.error(error instanceof Error ? error.message : error)
    process.exitCode = 1
  })
  .finally(async () => {
    closeStorageDatabases()
    await closePostgresPool()
  })
