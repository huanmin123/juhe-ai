import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { loadUsageRangeSummaryForScopeAsync } from '../../storage/usage-summary-loaders.js'
import { refreshUsageRankSnapshotsInStages } from '../../storage/usage-stats.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '用量范围窗口 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_scope_range_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const accountId = `acct_${marker}`
const jobName = `usage-scope-range-windows-refresh:${marker}`
const updatedAt = new Date().toISOString()

try {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const fixedDates = fixedUsageStatsDateKeys(timezone, today)
  const yesterday = fixedDates[fixedDates.length - 2]
  assert.ok(yesterday, 'PG usage scope range smoke 需要至少两个固定日期')
  const range = {
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  }
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)

  await cleanupSmokeRows()
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, 11, 10, 1, 110, 55, 9, 0.004, 0.321, 1100, 11, 240, 330, 11, 60, ?, ?, ?)
  `, [systemAccountId, accountId, today, updatedAt, updatedAt, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, 3, 3, 0, 30, 15, 2, 0.001, 0.111, 300, 3, 120, 90, 3, 40, ?, NULL, ?)
  `, [systemAccountId, accountId, yesterday, updatedAt, updatedAt])

  const refreshed = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_scope_range_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG usage scope range refresh 不应跳过')
  assert.equal(refreshed.stages.length, 1, 'PG usage scope range refresh 应只执行一个阶段')

  const windowRow = await client.one<{
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
  assert.ok(windowRow, 'PG usage scope range refresh 应写入窗口表')
  assert.equal(Number(windowRow.request_count), 11, 'PG usage scope range request_count 应来自 daily 聚合')
  assert.equal(Number(windowRow.active_days), 1, 'PG usage scope range active_days 应来自 daily 聚合')
  assert.equal(Number(windowRow.total_cost_usd), 0.321, 'PG usage scope range total_cost_usd 应来自 daily 聚合')

  const summary = await loadUsageRangeSummaryForScopeAsync({
    systemAccountId,
    scopeType: 'account',
    scopeId: accountId,
    range
  })
  assert.equal(summary.requestCount, 11, 'PG usage range summary 应从窗口表读回 requestCount')
  assert.equal(summary.inputTokens, 110, 'PG usage range summary 应从窗口表读回 inputTokens')
  assert.equal(summary.outputTokens, 55, 'PG usage range summary 应从窗口表读回 outputTokens')
  assert.equal(summary.cacheReadTokens, 9, 'PG usage range summary 应从窗口表读回 cacheReadTokens')
  assert.equal(summary.totalCost, 0.321, 'PG usage range summary 应从窗口表读回 totalCost')

  const sentinelUpdatedAt = '1999-12-31T00:00:00.000Z'
  await client.execute(`
    UPDATE juhe_stats.usage_scope_range_windows
    SET updated_at = ?
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [sentinelUpdatedAt, systemAccountId, accountId, yesterday, yesterday])

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_scope_range_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG usage scope range refresh 源水位不变时应跳过')

  const fresherUpdatedAt = new Date(Date.now() + 1000).toISOString()
  await client.execute(`
    UPDATE juhe_stats.usage_stats_daily
    SET request_count = 13,
      success_count = 12,
      error_count = 1,
      input_tokens = 130,
      output_tokens = 65,
      total_cost_usd = 0.654,
      updated_at = ?
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND stat_date = ?
  `, [fresherUpdatedAt, systemAccountId, accountId, today])
  const incremental = await refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_scope_range_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(incremental.skipped, false, 'PG usage scope range 今日源水位变化后应刷新')
  const incrementalToday = await client.one<{ request_count: string | number }>(`
    SELECT request_count
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [systemAccountId, accountId, today, today])
  assert.equal(Number(incrementalToday?.request_count ?? 0), 13, 'PG usage scope range 今日窗口应刷新为最新请求数')
  const yesterdayWindow = await client.one<{ updated_at?: string }>(`
    SELECT updated_at
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `, [systemAccountId, accountId, yesterday, yesterday])
  assert.equal(yesterdayWindow?.updated_at, sentinelUpdatedAt, 'PG usage scope range 仅今日变更时不应重写昨日 end_date 窗口')

  console.log(JSON.stringify({
    message: '用量范围窗口 PG smoke 通过',
    requestCount: Number(incrementalToday?.request_count ?? 0),
    activeDays: Number(windowRow.active_days),
    totalCost: summary.totalCost,
    skipped: skipped.skipped === true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_scope_range_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_daily WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
}
