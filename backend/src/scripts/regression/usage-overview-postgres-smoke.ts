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
import { fixedUsageStatsDateKeys, rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

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
  const fixedDates = fixedUsageStatsDateKeys(timezone, today)
  const yesterday = fixedDates[fixedDates.length - 2]
  assert.ok(yesterday, 'PG overview smoke 需要至少两个固定日期')
  const statHour = `${today}T00`
  const yesterdayStatHour = `${yesterday}T00`
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
      INSERT INTO juhe_stats.usage_stats_daily (
        system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 3, 3, 0, 30, 15, 2, 0.001, 0.111, 300, 3, 120, 90, 3, 40, ?, ?)
    `, [systemAccountId, systemAccountId, yesterday, updatedAt, updatedAt])
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
      INSERT INTO juhe_stats.usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 3, 3, 0, 30, 15, 2, 0.001, 0.111, 300, 3, 120, 90, 3, 40, ?, ?)
    `, [systemAccountId, systemAccountId, yesterdayStatHour, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_model_daily (
        system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
      ) VALUES (?, ?, 'gpt', 'gpt-5.5-smoke', 7, 6, 1, 70, 30, 5, 0.001, 0.123, ?)
    `, [systemAccountId, today, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_model_daily (
        system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
      ) VALUES (?, ?, 'gpt', 'gpt-5.5-smoke', 3, 3, 0, 30, 15, 2, 0.001, 0.111, ?)
    `, [systemAccountId, yesterday, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_error_daily (
        system_account_id, stat_date, error_group, provider_code, error_code, status_code,
        error_message, request_count, error_count, updated_at
      ) VALUES (?, ?, 'upstream', 'gpt', 'smoke_error', 502, 'overview smoke error', 1, 1, ?)
    `, [systemAccountId, today, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.usage_error_daily (
        system_account_id, stat_date, error_group, provider_code, error_code, status_code,
        error_message, request_count, error_count, updated_at
      ) VALUES (?, ?, 'upstream', 'gpt', 'smoke_error', 502, 'overview smoke error', 1, 1, ?)
    `, [systemAccountId, yesterday, updatedAt])
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

  const sentinelUpdatedAt = '1999-12-31T00:00:00.000Z'
  await markOverviewWindowUpdatedAt(client, yesterday, sentinelUpdatedAt)

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

  const incremental = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_overview_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(incremental.skipped, false, 'PG overview 今日源水位变化后应刷新窗口')
  const todaySummaryWindow = await client.one<{ request_count: string | number }>(`
    SELECT request_count
    FROM juhe_stats.usage_overview_summary_windows
    WHERE system_account_id = ?
      AND window_key = ?
  `, [systemAccountId, windowKey])
  assert.equal(Number(todaySummaryWindow?.request_count ?? 0), 9, 'PG overview 今日 summary 窗口应刷新为最新请求数')
  assert.equal(await overviewUpdatedAt(client, 'usage_overview_summary_windows', yesterday), sentinelUpdatedAt, 'PG overview 仅今日变更时不应重写昨日 summary 窗口')
  assert.equal(await overviewUpdatedAt(client, 'usage_overview_trend_windows', yesterday), sentinelUpdatedAt, 'PG overview 仅今日变更时不应重写昨日 trend 窗口')
  assert.equal(await overviewUpdatedAt(client, 'usage_model_rank_windows', yesterday), sentinelUpdatedAt, 'PG overview 仅今日变更时不应重写昨日 model rank 窗口')
  assert.equal(await overviewUpdatedAt(client, 'usage_error_rank_windows', yesterday), sentinelUpdatedAt, 'PG overview 仅今日变更时不应重写昨日 error rank 窗口')

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

async function markOverviewWindowUpdatedAt(client: ReturnType<typeof createPostgresDatabaseClient>, statDate: string, updatedAt: string): Promise<void> {
  const windowKey = rangeWindowKey({ startDate: statDate, endDate: statDate })
  for (const tableName of ['usage_overview_summary_windows', 'usage_overview_trend_windows', 'usage_model_rank_windows', 'usage_error_rank_windows']) {
    await client.execute(`
      UPDATE juhe_stats.${tableName}
      SET updated_at = ?
      WHERE system_account_id = ?
        AND window_key = ?
    `, [updatedAt, systemAccountId, windowKey])
  }
}

async function overviewUpdatedAt(client: ReturnType<typeof createPostgresDatabaseClient>, tableName: string, statDate: string): Promise<string | undefined> {
  const row = await client.one<{ updated_at?: string }>(`
    SELECT updated_at
    FROM juhe_stats.${tableName}
    WHERE system_account_id = ?
      AND window_key = ?
    LIMIT 1
  `, [systemAccountId, rangeWindowKey({ startDate: statDate, endDate: statDate })])
  return row?.updated_at
}
