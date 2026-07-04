import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { refreshHotUsageWindowSnapshots } from '../../storage/usage-stats.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '热用量窗口 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_hot_window_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const accountId = `acct_${marker}`
const jobName = `usage-hot-window-refresh:${marker}`
const updatedAt = new Date().toISOString()

try {
  const timezone = await usageStatsTimezoneAsync()
  const dates = fixedUsageStatsDateKeys(timezone, dateKey(new Date(), timezone))
  const today = dates[dates.length - 1]
  const previousEndDate = dates[dates.length - 2]
  assert.ok(today, 'PG smoke 需要可用的今日日期键')
  assert.ok(previousEndDate, 'PG smoke 需要可用的上一日日期键')

  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)

  await cleanupSmokeRows()
  await seedPreviousWindow(client, previousEndDate)
  await seedTodayUsageSources(client, today)

  const refreshed = await refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG 热用量窗口刷新不应跳过')
  assert.deepEqual(refreshed.stages.map((stage) => stage.name), ['usage_overview_windows', 'usage_scope_range_windows'], 'PG 热刷新只应执行概览窗口和范围窗口')

  const overviewRow = await client.one<{ request_count: string | number }>(`
    SELECT request_count
    FROM juhe_stats.usage_overview_summary_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [systemAccountId, today, today])
  assert.equal(Number(overviewRow?.request_count), 7, 'PG 热刷新应发布今日概览 summary 窗口')

  const scopeWindowRow = await client.one<{
    request_count: string | number
    active_days: string | number
    total_cost_usd: string | number
  }>(`
    SELECT request_count, active_days, total_cost_usd
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [systemAccountId, accountId, today, today])
  assert.equal(Number(scopeWindowRow?.request_count), 11, 'PG 热刷新应发布今日账号范围窗口')
  assert.equal(Number(scopeWindowRow?.active_days), 1, 'PG 今日账号范围窗口 active_days 应来自 daily')
  assert.equal(Number(scopeWindowRow?.total_cost_usd), 0.321, 'PG 今日账号范围窗口成本应来自 daily')

  const previousWindow = await client.one<{ request_count: string | number }>(`
    SELECT request_count
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [systemAccountId, accountId, previousEndDate, previousEndDate])
  assert.equal(Number(previousWindow?.request_count), 99, 'PG 热刷新不应删除非今日 end_date 的范围窗口')

  const skipped = await refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG 同一天同源水位热刷新应跳过')

  console.log(JSON.stringify({
    message: '热用量窗口 PG smoke 通过',
    requestCount: Number(scopeWindowRow?.request_count),
    activeDays: Number(scopeWindowRow?.active_days),
    totalCost: Number(scopeWindowRow?.total_cost_usd),
    skipped: skipped.skipped === true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function seedPreviousWindow(client: ReturnType<typeof createPostgresDatabaseClient>, previousEndDate: string): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, 99, 99, 0, 990, 99, 0, 0, 0.99, 9900, 99, 100, 990, 99, 10, 1, ?, ?)
  `, [systemAccountId, accountId, previousEndDate, previousEndDate, `${previousEndDate}T00:00:00.000Z`, updatedAt])
}

async function seedTodayUsageSources(client: ReturnType<typeof createPostgresDatabaseClient>, today: string): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
  `, [systemAccountId, systemAccountId, `${today}T00:00:00.000Z`, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
  `, [systemAccountId, systemAccountId, today, `${today}T00:00:00.000Z`, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
  `, [systemAccountId, systemAccountId, `${today}T00`, `${today}T00:00:00.000Z`, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_model_daily (
      system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
    ) VALUES (?, ?, 'gpt', 'hot-pg-model', 7, 5, 2, 70, 14, 3, 0.001, 0.07, ?)
  `, [systemAccountId, today, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_error_daily (
      system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message,
      request_count, error_count, updated_at
    ) VALUES (?, ?, 'gateway', 'gpt', 'hot_pg_error', 429, 'hot pg error', 2, 2, ?)
  `, [systemAccountId, today, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, 11, 10, 1, 110, 55, 9, 0.004, 0.321, 1100, 11, 240, 330, 11, 60, ?, ?, ?)
  `, [systemAccountId, accountId, today, `${today}T00:00:00.000Z`, `${today}T00:01:00.000Z`, updatedAt])
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_scope_range_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_overview_trend_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_model_rank_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_error_rank_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_hourly WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_model_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_error_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
}
