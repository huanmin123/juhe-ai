import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { ensurePostgresUsageRecordPartitions } from '../../storage/postgres-usage-record-partitions.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '用量统计完整重建 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_USAGE_STATS_REBUILD_POSTGRES_SMOKE,
  '1',
  '该 smoke 会清空当前数据库的全部用量统计派生表，必须在隔离测试库显式设置 JUHE_AI_ALLOW_USAGE_STATS_REBUILD_POSTGRES_SMOKE=1'
)

const marker = `usage_rebuild_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const apiKeyId = `key_${marker}`
const accountId = `acct_${marker}`
const groupId = `group_${marker}`
const providerCode = 'gpt'
const model = `model_${marker}`
const resetStatsTableNames = [
  'account_health_hourly',
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
  'ai_performance_summary_dirty_system_accounts',
  'usage_quota_hourly_windows',
  'usage_quota_hourly_window_dirty_scopes',
  'usage_overview_dirty_scopes',
  'usage_scope_range_windows',
  'system_metrics_trend_windows',
  'account_quality_minute_stats',
  'account_quality_scores',
  'account_quality_dirty_accounts'
] as const
const resetStatsJobNames = [
  'usage_stats_aggregation',
  'usage_quota_hourly_windows_expiry',
  'usage_quota_hourly_windows_config_seed',
  'usage_overview_daily_seed',
  'ai_performance_summary_daily_seed'
] as const
let isolatedDatabaseVerified = false

try {
  const pool = await getPostgresPool()
  const database = await pool.query('SELECT current_database() AS database_name')
  const databaseName = String(database.rows[0]?.database_name ?? '')
  assert.match(databaseName, /^codex_scheduler_smoke_[a-z0-9_-]+$/i, '完整重建 smoke 只允许使用本次 harness 创建的 codex_scheduler_smoke_* 隔离数据库')

  const existingUsage = await pool.query('SELECT COUNT(*) AS total FROM juhe_usage.usage_records')
  assert.equal(Number(existingUsage.rows[0]?.total ?? 0), 0, '完整重建 smoke 要求隔离数据库中不存在既有 usage_records')
  const existingResetRows = await pool.query(resetStatsTableNames.map((tableName) => `
    SELECT '${tableName}' AS table_name, COUNT(*) AS total FROM juhe_stats.${tableName}
  `).join(' UNION ALL '))
  const nonEmptyResetTables = existingResetRows.rows
    .filter((row) => Number(row.total ?? 0) > 0)
    .map((row) => String(row.table_name))
  assert.deepEqual(nonEmptyResetTables, [], `完整重建 smoke 要求生产 reset 清单全部为空，非空表：${nonEmptyResetTables.join(', ')}`)
  const existingResetStates = await pool.query(`
    SELECT job_name
    FROM juhe_stats.stats_job_state
    WHERE job_name = ANY($1::text[])
    LIMIT 1
  `, [[...resetStatsJobNames]])
  assert.equal(existingResetStates.rows.length, 0, '完整重建 smoke 要求将被重置的 stats_job_state 不存在')
  isolatedDatabaseVerified = true

  const timezone = await usageStatsTimezoneAsync()
  const createdAt = new Date(Date.now() - 10 * 60 * 1000).toISOString()
  const today = dateKey(new Date(createdAt), timezone)
  await seedUsageRecords(createdAt)

  await closePostgresPool()
  const rebuild = spawnSync(process.execPath, [
    '--import',
    'tsx',
    'src/scripts/maintenance/rebuild-usage-stats.ts',
    '--confirm-offline',
    '--batch-size=2',
    '--max-batches=64'
  ], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_CONFIRM_USAGE_STATS_REBUILD: '1',
      JUHE_AI_INSTANCE_ID: process.env.JUHE_AI_INSTANCE_ID?.trim() || `rebuild-smoke-${marker}`,
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8',
    timeout: 120_000
  })
  assert.equal(
    rebuild.status,
    0,
    `完整重建子进程失败：${summarizeChildFailure(rebuild.error, rebuild.stderr)}`
  )
  assert.match(rebuild.stdout, /用量统计已重建：扫描 3 条记录/, '完整重建应从 usage_records 重新扫描全部测试事实')

  await assertBaseAggregates()
  await assertQuotaWindow()
  await assertOverviewWindow(today)
  await assertAiPerformanceWindow(today)
  await assertAccountRank()
  await assertDirtyQueuesEmpty()

  console.log(JSON.stringify({
    message: '用量统计完整重建 PG smoke 通过',
    databaseName,
    usageRecords: 3,
    dirtyQueuesEmpty: true
  }))
} finally {
  await cleanupSmokeRows().catch(() => undefined)
  await closeRedisClients()
  await closePostgresPool()
}

async function seedUsageRecords(createdAt: string): Promise<void> {
  const pool = await getPostgresPool()
  await ensurePostgresUsageRecordPartitions(createPostgresDatabaseClient(pool), [createdAt])
  const values = [
    { suffix: 'a', success: 1, statusCode: 200, cost: 0.001, errorCode: null, errorMessage: null, failureAttribution: null },
    { suffix: 'b', success: 1, statusCode: 200, cost: 0.002, errorCode: null, errorMessage: null, failureAttribution: null },
    { suffix: 'c', success: 0, statusCode: 503, cost: 0.003, errorCode: 'upstream_unavailable', errorMessage: 'rebuild smoke upstream unavailable', failureAttribution: 'upstream' }
  ]
  for (const [index, value] of values.entries()) {
    await pool.query(`
      INSERT INTO juhe_usage.usage_records (
        id, system_account_id, trace_id, traffic_source, api_key_id, group_id, account_id,
        endpoint, provider_code, model, status_code, success, failure_attribution,
        first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, cost_usd, error_code, error_message,
        account_owner_system_account_id, group_owner_system_account_id,
        account_access_type, group_access_type, created_at
      ) VALUES (
        $1, $2, $3, 'gateway', $4, $5, $6,
        '/v1/responses', $7, $8, $9, $10, $11,
        $12, $13, $14, $15, $16,
        $17, $18, $19, $20,
        $2, $2, 'owner', 'owner', $21
      )
    `, [
      `usage_${marker}_${value.suffix}`,
      systemAccountId,
      `trace_${marker}_${value.suffix}`,
      apiKeyId,
      groupId,
      accountId,
      providerCode,
      model,
      value.statusCode,
      value.success,
      value.failureAttribution,
      100 + index * 10,
      500 + index * 100,
      10 + index,
      5 + index,
      index,
      0.0001 * index,
      value.cost,
      value.errorCode,
      value.errorMessage,
      new Date(Date.parse(createdAt) + index).toISOString()
    ])
  }
}

async function assertBaseAggregates(): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT request_count, success_count, error_count, total_cost_usd
    FROM juhe_stats.usage_stats_totals
    WHERE system_account_id = $1 AND scope_type = 'system_account' AND scope_id = $1
  `, [systemAccountId])
  assert.equal(Number(row.rows[0]?.request_count), 3, '完整重建应恢复 system_account 请求总数')
  assert.equal(Number(row.rows[0]?.success_count), 2, '完整重建应恢复 system_account 成功数')
  assert.equal(Number(row.rows[0]?.error_count), 1, '完整重建应恢复 system_account 错误数')
  assert.equal(Number(row.rows[0]?.total_cost_usd), 0.006, '完整重建应恢复 system_account 成本')
}

async function assertQuotaWindow(): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT total_cost_usd
    FROM juhe_stats.usage_quota_hourly_windows
    WHERE system_account_id = $1 AND scope_type = 'api_key' AND scope_id = $2 AND window_hours = 1
  `, [systemAccountId, apiKeyId])
  assert.equal(Number(row.rows[0]?.total_cost_usd), 0.006, '完整重建应恢复 API Key 1 小时额度成本')
}

async function assertOverviewWindow(today: string): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT request_count, success_count, error_count
    FROM juhe_stats.usage_overview_summary_windows
    WHERE system_account_id = $1 AND window_key = $2
  `, [systemAccountId, rangeWindowKey({ startDate: today, endDate: today })])
  assert.equal(Number(row.rows[0]?.request_count), 3, '完整重建应恢复今日 overview summary')
  assert.equal(Number(row.rows[0]?.success_count), 2, '完整重建 overview 成功数不正确')
  assert.equal(Number(row.rows[0]?.error_count), 1, '完整重建 overview 错误数不正确')
}

async function assertAiPerformanceWindow(today: string): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT request_count, duration_ms_sum, first_token_ms_sum
    FROM juhe_stats.ai_performance_summary_windows
    WHERE system_account_id = $1 AND window_key = $2
  `, [systemAccountId, rangeWindowKey({ startDate: today, endDate: today })])
  assert.equal(Number(row.rows[0]?.request_count), 3, '完整重建应恢复今日 AI 性能摘要请求数')
  assert.equal(Number(row.rows[0]?.duration_ms_sum), 1800, '完整重建应恢复 AI 性能总耗时')
  assert.equal(Number(row.rows[0]?.first_token_ms_sum), 330, '完整重建应恢复 AI 性能首 token 总耗时')
}

async function assertAccountRank(): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT scope_id, metric_value, rank
    FROM juhe_stats.usage_rank_snapshots
    WHERE system_account_id = $1
      AND scope_type = 'account'
      AND window_key = 'last7d'
      AND metric = 'request_count'
    ORDER BY rank
    LIMIT 1
  `, [systemAccountId])
  assert.equal(row.rows[0]?.scope_id, accountId, '完整重建应恢复账户请求排行')
  assert.equal(Number(row.rows[0]?.metric_value), 3, '完整重建账户排行请求数不正确')
  assert.equal(Number(row.rows[0]?.rank), 1, '完整重建账户排行名次不正确')
}

async function assertDirtyQueuesEmpty(): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT
      (SELECT COUNT(*) FROM juhe_stats.usage_quota_hourly_window_dirty_scopes) AS quota,
      (SELECT COUNT(*) FROM juhe_stats.usage_overview_dirty_scopes) AS overview,
      (SELECT COUNT(*) FROM juhe_stats.ai_performance_summary_dirty_system_accounts) AS ai_performance
  `)
  assert.equal(Number(row.rows[0]?.quota), 0, '完整重建结束后 quota dirty 队列必须归零')
  assert.equal(Number(row.rows[0]?.overview), 0, '完整重建结束后 overview dirty 队列必须归零')
  assert.equal(Number(row.rows[0]?.ai_performance), 0, '完整重建结束后 AI performance dirty 队列必须归零')
}

async function cleanupSmokeRows(): Promise<void> {
  if (!isolatedDatabaseVerified) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE system_account_id = $1', [systemAccountId])
  for (const tableName of resetStatsTableNames) {
    await pool.query(`DELETE FROM juhe_stats.${tableName}`)
  }
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = ANY($1::text[])', [[...resetStatsJobNames]])
}

function summarizeChildFailure(error: Error | undefined, stderr: string): string {
  const errorMessage = error?.message?.trim()
  if (errorMessage) return errorMessage.slice(0, 1000)
  const stderrSummary = stderr.trim().slice(-2000)
  return stderrSummary || '子进程未返回错误摘要'
}
