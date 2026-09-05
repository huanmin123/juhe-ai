import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { refreshUsageRankSnapshotsInStages, type UsageRankSnapshotStageName } from '../../storage/usage-stats.repository.js'
import { dateKey, monthKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '用量 TopN 排行 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_USAGE_STATS_REBUILD_POSTGRES_SMOKE,
  '1',
  '排行 smoke 会重建全局 TopN 快照，只允许在隔离库设置 JUHE_AI_ALLOW_USAGE_STATS_REBUILD_POSTGRES_SMOKE=1 后运行'
)

const marker = `usage_rank_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const jobName = `usage-rank-snapshots-refresh:${marker}`
const updatedAt = new Date().toISOString()
const stageNames: UsageRankSnapshotStageName[] = [
  'account_last7d_request_rank',
  'caller_account_last7d_request_rank',
  'api_key_current_month_cost_rank',
  'account_authorization_current_month_cost_rank',
  'group_authorization_current_month_cost_rank'
]
const aiPerformanceStageNames: UsageRankSnapshotStageName[] = ['ai_performance_summary_windows']

try {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const currentMonth = monthKey(new Date(), timezone)
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)
  const database = await pool.query('SELECT current_database() AS database_name')
  assert.match(
    String(database.rows[0]?.database_name ?? ''),
    /^codex_scheduler_smoke_[a-z0-9_-]+$/i,
    '排行 smoke 只允许使用本次 harness 创建的 codex_scheduler_smoke_* 隔离数据库'
  )

  await cleanupSmokeRows()
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_daily (
        system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES
        (?, 'account', ?, ?, 19, 19, 0, 190, 57, 0, 0, 0.019, 1900, 19, 200, 380, 19, 60, ?, ?),
        (?, 'account', ?, ?, 7, 7, 0, 70, 21, 0, 0, 0.007, 700, 7, 120, 140, 7, 35, ?, ?),
        (?, 'caller_account', ?, ?, 23, 22, 1, 230, 69, 0, 0, 0.023, 2300, 23, 250, 460, 23, 70, ?, ?),
        (?, 'caller_account', ?, ?, 11, 11, 0, 110, 33, 0, 0, 0.011, 1100, 11, 180, 220, 11, 42, ?, ?)
    `, [
      systemAccountId, `account_a_${marker}`, today, updatedAt, updatedAt,
      systemAccountId, `account_b_${marker}`, today, updatedAt, updatedAt,
      systemAccountId, `caller_a_${marker}`, today, updatedAt, updatedAt,
      systemAccountId, `caller_b_${marker}`, today, updatedAt, updatedAt
    ])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_totals (
        system_account_id, scope_type, scope_id, request_count, success_count, error_count,
        input_tokens, output_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at
      ) VALUES
        (?, 'system_account', ?, 26, 26, 0, 260, 78, 0.026, 2600, 26, 200, 520, 26, 60, ?, ?)
    `, [systemAccountId, systemAccountId, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_monthly (
        system_account_id, scope_type, scope_id, stat_month, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES
        (?, 'api_key', ?, ?, 31, 31, 0, 310, 93, 0, 0, 0.531, 3100, 31, 310, 620, 31, 93, ?, ?),
        (?, 'api_key', ?, ?, 17, 17, 0, 170, 51, 0, 0, 0.217, 1700, 17, 170, 340, 17, 51, ?, ?),
        (?, 'account_authorization', ?, ?, 29, 29, 0, 290, 87, 0, 0, 0.729, 2900, 29, 290, 580, 29, 87, ?, ?),
        (?, 'group_authorization', ?, ?, 37, 36, 1, 370, 111, 0, 0, 0.837, 3700, 37, 370, 740, 37, 111, ?, ?)
    `, [
      systemAccountId, `api_key_a_${marker}`, currentMonth, updatedAt, updatedAt,
      systemAccountId, `api_key_b_${marker}`, currentMonth, updatedAt, updatedAt,
      systemAccountId, `account_auth_${marker}`, currentMonth, updatedAt, updatedAt,
      systemAccountId, `group_auth_${marker}`, currentMonth, updatedAt, updatedAt
    ])
  })

  const refreshed = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG usage rank refresh 不应跳过')
  assert.deepEqual(refreshed.stages.map((stage) => stage.name), stageNames, 'PG usage rank refresh 应只执行 TopN 阶段')

  await assertTopRank('account', 'last7d', 'request_count', `account_a_${marker}`, 19)
  await assertTopRank('caller_account', 'last7d', 'request_count', `caller_a_${marker}`, 23)
  await assertTopRank('api_key', 'current_month', 'total_cost_usd', `api_key_a_${marker}`, 0.531)
  await assertTopRank('account_authorization', 'current_month', 'total_cost_usd', `account_auth_${marker}`, 0.729)
  await assertTopRank('group_authorization', 'current_month', 'total_cost_usd', `group_auth_${marker}`, 0.837)
  await client.execute(`
    INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (
      system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at
    ) VALUES (?, ?, ?, 1, ?, ?)
    ON CONFLICT(system_account_id) DO UPDATE SET
      min_stat_date = LEAST(ai_performance_summary_dirty_system_accounts.min_stat_date, EXCLUDED.min_stat_date),
      max_stat_date = GREATEST(ai_performance_summary_dirty_system_accounts.max_stat_date, EXCLUDED.max_stat_date),
      generation = ai_performance_summary_dirty_system_accounts.generation + 1,
      updated_at = EXCLUDED.updated_at
  `, [systemAccountId, today, today, updatedAt, updatedAt])
  const aiRefreshed = await refreshUsageRankSnapshotsInStages({
    stageNames: aiPerformanceStageNames,
    skipIfUnchanged: true,
    jobName: `${jobName}:ai-performance`,
    yieldToEventLoop: async () => {}
  })
  assert.equal(aiRefreshed.skipped, false, 'PG AI 性能 dirty 队列非空时不应被 source watermark 跳过')
  await assertAiPerformanceSummaryWindow(today)

  const aiSkipped = await refreshUsageRankSnapshotsInStages({
    stageNames: aiPerformanceStageNames,
    skipIfUnchanged: true,
    jobName: `${jobName}:ai-performance`,
    yieldToEventLoop: async () => {}
  })
  assert.equal(aiSkipped.skipped, true, 'PG AI 性能 dirty 已排空且源水位不变时第二次刷新应跳过')

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG usage rank refresh 源水位不变时应跳过')

  console.log(JSON.stringify({
    message: '用量 TopN 排行 PG smoke 通过',
    stages: refreshed.stages.length + aiRefreshed.stages.length,
    accountTop: `account_a_${marker}`,
    callerTop: `caller_a_${marker}`,
    aiSkipped: aiSkipped.skipped === true,
    skipped: skipped.skipped === true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function assertTopRank(scopeType: string, windowKey: string, metric: string, scopeId: string, metricValue: number): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT scope_id, metric_value, rank
    FROM juhe_stats.usage_rank_snapshots
    WHERE system_account_id = $1
      AND scope_type = $2
      AND window_key = $3
      AND metric = $4
    ORDER BY snapshot_at DESC, rank ASC
    LIMIT 1
  `, [systemAccountId, scopeType, windowKey, metric])
  const top = row.rows[0] as { scope_id: string; metric_value: string | number; rank: string | number } | undefined
  assert.ok(top, `PG usage rank ${scopeType}/${windowKey}/${metric} 应写入排行`)
  assert.equal(top.scope_id, scopeId, `PG usage rank ${scopeType}/${windowKey}/${metric} 第一名 scope_id 不正确`)
  assert.equal(Number(top.rank), 1, `PG usage rank ${scopeType}/${windowKey}/${metric} 第一名 rank 应为 1`)
  assert.equal(Number(top.metric_value), metricValue, `PG usage rank ${scopeType}/${windowKey}/${metric} metric_value 不正确`)
}

async function assertAiPerformanceSummaryWindow(today: string): Promise<void> {
  const pool = await getPostgresPool()
  const row = await pool.query(`
    SELECT request_count, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max
    FROM juhe_stats.ai_performance_summary_windows
    WHERE system_account_id = $1
      AND window_key = $2
      AND start_date = $3
      AND end_date = $3
  `, [systemAccountId, `${today}:${today}`, today])
  const summary = row.rows[0] as {
    request_count: string | number
    duration_ms_sum: string | number
    duration_ms_count: string | number
    duration_ms_max: string | number
    first_token_ms_sum: string | number
    first_token_ms_count: string | number
    first_token_ms_max: string | number
  } | undefined
  assert.ok(summary, 'PG AI 性能 summary window 应由用量排行刷新阶段生成')
  assert.equal(Number(summary.request_count), 26, 'PG AI 性能 summary window 请求数不正确')
  assert.equal(Number(summary.duration_ms_sum), 2600, 'PG AI 性能 summary window 总耗时累加不正确')
  assert.equal(Number(summary.duration_ms_count), 26, 'PG AI 性能 summary window 总耗时计数不正确')
  assert.equal(Number(summary.duration_ms_max), 200, 'PG AI 性能 summary window 最大总耗时不正确')
  assert.equal(Number(summary.first_token_ms_sum), 520, 'PG AI 性能 summary window 首 token 耗时累加不正确')
  assert.equal(Number(summary.first_token_ms_count), 26, 'PG AI 性能 summary window 首 token 耗时计数不正确')
  assert.equal(Number(summary.first_token_ms_max), 60, 'PG AI 性能 summary window 最大首 token 耗时不正确')
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_rank_snapshots WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_monthly WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.ai_performance_summary_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.ai_performance_summary_dirty_system_accounts WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = ANY($1::text[])', [[jobName, `${jobName}:ai-performance`]])
}
