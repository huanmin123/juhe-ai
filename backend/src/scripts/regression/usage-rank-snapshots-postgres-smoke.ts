import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { refreshUsageRankSnapshotsInStages, type UsageRankSnapshotStageName } from '../../storage/usage-stats.repository.js'
import { dateKey, monthKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '用量 TopN 排行 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

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

try {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const currentMonth = monthKey(new Date(), timezone)
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)

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
  assert.deepEqual(refreshed.stages.map((stage) => stage.name), stageNames, 'PG usage rank refresh 应执行 5 个 TopN 阶段')

  await assertTopRank('account', 'last7d', 'request_count', `account_a_${marker}`, 19)
  await assertTopRank('caller_account', 'last7d', 'request_count', `caller_a_${marker}`, 23)
  await assertTopRank('api_key', 'current_month', 'total_cost_usd', `api_key_a_${marker}`, 0.531)
  await assertTopRank('account_authorization', 'current_month', 'total_cost_usd', `account_auth_${marker}`, 0.729)
  await assertTopRank('group_authorization', 'current_month', 'total_cost_usd', `group_auth_${marker}`, 0.837)

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG usage rank refresh 源水位不变时应跳过')

  console.log(JSON.stringify({
    message: '用量 TopN 排行 PG smoke 通过',
    stages: refreshed.stages.length,
    accountTop: `account_a_${marker}`,
    callerTop: `caller_a_${marker}`,
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

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_rank_snapshots WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_monthly WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
}
