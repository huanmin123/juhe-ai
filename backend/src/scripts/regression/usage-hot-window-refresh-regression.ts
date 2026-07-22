import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { usageStatsTimezone } from '../../storage/usage-stats-helpers.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'
import { fixedUsageStatsDateKeys, rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-hot-window-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-hot-window-refresh.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-hot-window-refresh-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js')
])

const systemAccountId = 'sys_admin'
const accountId = 'acct_usage_hot_window'
const jobName = 'usage_hot_window_refresh_regression'
const adminAccess = { systemAccountId, role: 'admin' as const }

try {
  const timezone = usageStatsTimezone()
  const dates = fixedUsageStatsDateKeys(timezone)
  const today = dates[dates.length - 1]
  const previousEndDate = dates[dates.length - 2]
  assert.ok(today, '测试需要可用的今日日期键')
  assert.ok(previousEndDate, '测试需要可用的上一日日期键')

  seedPreviousWindow(previousEndDate)
  seedTodayUsageSources(today)

  const refreshed = await usageStatsRepository.refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次热用量窗口刷新不应跳过')
  assert.deepEqual(refreshed.stages.map((stage) => stage.name), ['usage_overview_windows', 'usage_scope_range_windows'], '热刷新应同时更新概览窗口和今天结束的账号范围窗口')

  const overview = await usageStatsRepository.getUsageStatsOverviewAsync(adminAccess, usageStatsRepository.normalizeDefaultUsageStatsRange())
  assert.equal(overview.summary.requestCount, 7, '热刷新后统计首页 summary 应读取今日窗口')
  assert.equal(overview.hourlyTrend[0]?.requestCount, 7, '热刷新后统计首页趋势应读取今日窗口')
  assert.equal(overview.modelDistribution[0]?.requestCount, 7, '热刷新后模型排行应读取今日窗口')
  assert.equal(overview.errors[0]?.errorCount, 2, '热刷新后错误排行应读取今日窗口')

  const statsDatabase = databaseModule.getStatsDatabase()
  const previousWindow = statsDatabase.prepare(`
    SELECT request_count
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `).get(systemAccountId, accountId, previousEndDate, previousEndDate) as { request_count?: number } | undefined
  assert.equal(previousWindow?.request_count, 99, '热刷新不应删除或重建历史结束日期范围窗口')

  const todayWindow = statsDatabase.prepare(`
    SELECT request_count
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `).get(systemAccountId, accountId, today, today) as { request_count?: number } | undefined
  assert.equal(todayWindow?.request_count, 11, '热刷新应发布今天结束的账号范围窗口')

  const skipped = await usageStatsRepository.refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, '同一天同源水位热刷新应跳过')

  console.log('热用量窗口 SQLite 回归通过：刷新概览和今日范围窗口、保留历史范围窗口且同水位跳过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedPreviousWindow(previousEndDate: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-01T00:00:00.000Z'
  database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, 99, 99, 0, 990, 99, 0, 0, 0.99, 9900, 99, 100, 990, 99, 10, 1, ?, ?)
  `).run(systemAccountId, accountId, previousEndDate, previousEndDate, `${previousEndDate}T00:00:00.000Z`, updatedAt)
}

function seedTodayUsageSources(today: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-02T00:00:00.000Z'
  for (const scope of [systemAccountId, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
    `).run(scope, scope, `${today}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_stats_daily (
        system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
    `).run(scope, scope, today, `${today}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 7, 5, 2, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
    `).run(scope, scope, `${today}T00`, `${today}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_model_daily (
        system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
      ) VALUES (?, ?, 'gpt', 'hot-model', 7, 5, 2, 70, 14, 3, 0.001, 0.07, ?)
    `).run(scope, today, updatedAt)
    database.prepare(`
      INSERT INTO usage_error_daily (
        system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message,
        request_count, error_count, updated_at
      ) VALUES (?, ?, 'gateway', 'gpt', 'hot_error', 429, 'hot error', 2, 2, ?)
    `).run(scope, today, updatedAt)
  }
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, last_error_at, updated_at
    ) VALUES (?, 'account', ?, ?, 11, 10, 1, 110, 55, 9, 0.004, 0.321, 1100, 11, 240, 330, 11, 60, ?, ?, ?)
  `).run(systemAccountId, accountId, today, `${today}T00:00:00.000Z`, `${today}T00:01:00.000Z`, updatedAt)
}
