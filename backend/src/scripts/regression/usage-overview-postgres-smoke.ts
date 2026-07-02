import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  getUsageStatsOverviewAsync,
  refreshUsageRankSnapshotsInStages
} from '../../storage/usage-stats.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '用量概览 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_overview_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const jobName = `usage-overview-windows-refresh:${marker}`
const updatedAt = new Date().toISOString()

const access: AccessScope = {
  systemAccountId: `admin_${marker}`,
  role: 'admin',
  systemAccountFilterId: systemAccountId
}

try {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const statHour = `${today}T00`
  const range = {
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  }
  const windowKey = rangeWindowKey(range)
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)

  await cleanupSmokeRows()
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_totals (
        system_account_id, scope_type, scope_id, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, 7, 6, 1, 70, 30, 5, 0.001, 0.123, 700, 7, 180, 210, 7, 40, ?, ?)
    `, [systemAccountId, systemAccountId, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_daily (
        system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 7, 6, 1, 70, 30, 5, 0.001, 0.123, 700, 7, 180, 210, 7, 40, ?, ?)
    `, [systemAccountId, systemAccountId, today, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 7, 6, 1, 70, 30, 5, 0.001, 0.123, 700, 7, 180, 210, 7, 40, ?, ?)
    `, [systemAccountId, systemAccountId, statHour, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_model_daily (
        system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
      ) VALUES (?, ?, 'gpt', 'gpt-5.5-smoke', 7, 6, 1, 70, 30, 5, 0.001, 0.123, ?)
    `, [systemAccountId, today, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_error_daily (
        system_account_id, stat_date, error_group, provider_code, error_code, status_code,
        error_message, request_count, error_count, updated_at
      ) VALUES (?, ?, 'upstream', 'gpt', 'smoke_error', 502, 'overview smoke error', 1, 1, ?)
    `, [systemAccountId, today, updatedAt])
  })

  const refreshed = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_overview_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG overview window refresh 不应跳过')
  assert.equal(refreshed.stages.length, 1, 'PG overview window refresh 应只执行一个阶段')

  const overview = await getUsageStatsOverviewAsync(access, range)
  assert.equal(overview.summary.requestCount, 7, 'PG overview summary request_count 应来自日聚合表')
  assert.equal(overview.summary.successCount, 6, 'PG overview summary success_count 应来自日聚合表')
  assert.equal(overview.summary.errorCount, 1, 'PG overview summary error_count 应来自日聚合表')
  assert.equal(overview.hourlyTrend.length, 1, 'PG overview trend 应返回一个小时桶')
  assert.equal(overview.hourlyTrend[0]?.requestCount, 7, 'PG overview trend request_count 应来自窗口表')
  assert.equal(overview.modelDistribution[0]?.model, 'gpt-5.5-smoke', 'PG overview model rank 应来自窗口表')
  assert.equal(overview.errors[0]?.errorCode, 'smoke_error', 'PG overview error rank 应来自窗口表')

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_overview_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG overview window refresh 源水位不变时应跳过')

  const fresherUpdatedAt = new Date(Date.now() + 1000).toISOString()
  await client.execute(`
    UPDATE juhe_stats.usage_stats_daily
    SET request_count = 9,
      success_count = 8,
      error_count = 1,
      input_tokens = 90,
      output_tokens = 45,
      cache_read_tokens = 8,
      total_cost_usd = 0.456,
      duration_ms_sum = 900,
      duration_ms_count = 9,
      first_token_ms_sum = 270,
      first_token_ms_count = 9,
      updated_at = ?
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_date = ?
  `, [fresherUpdatedAt, systemAccountId, systemAccountId, today])

  const freshSummaryOverview = await getUsageStatsOverviewAsync(access, range)
  assert.equal(freshSummaryOverview.summary.requestCount, 9, 'PG overview summary 应读取最新日聚合，不等待 30 分钟窗口刷新')
  assert.equal(freshSummaryOverview.summary.inputTokens, 90, 'PG overview summary inputTokens 应读取最新日聚合')
  assert.equal(freshSummaryOverview.summary.outputTokens, 45, 'PG overview summary outputTokens 应读取最新日聚合')
  assert.equal(freshSummaryOverview.summary.cacheReadTokens, 8, 'PG overview summary cacheReadTokens 应读取最新日聚合')
  assert.equal(freshSummaryOverview.summary.totalCost, 0.456, 'PG overview summary totalCost 应读取最新日聚合')
  assert.equal(freshSummaryOverview.hourlyTrend[0]?.requestCount, 7, 'PG overview trend 仍来自窗口表')

  console.log(JSON.stringify({
    message: '用量概览 PG smoke 通过',
    windowKey,
    requestCount: freshSummaryOverview.summary.requestCount,
    trendBuckets: overview.hourlyTrend.length,
    modelRanks: overview.modelDistribution.length,
    errorRanks: overview.errors.length,
    skipped: skipped.skipped === true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_overview_trend_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_model_rank_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_error_rank_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_hourly WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_model_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_error_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
}
