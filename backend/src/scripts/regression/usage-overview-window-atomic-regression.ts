import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-overview-window-atomic-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-overview-window-atomic.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-overview-window-atomic-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js')
])

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const statsDatabase = databaseModule.getStatsDatabase()
  const range = usageStatsRepository.normalizeDefaultUsageStatsRange()
  const windowKey = rangeWindowKey(range)
  seedPublishedOverviewWindows(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, windowKey, range.startDate, range.endDate)
  seedNewUsageSources(range.endDate)

  const before = usageStatsRepository.getUsageStatsOverview(adminAccess, range)
  assert.equal(before.summary.requestCount, 1, '测试前应读到已发布 summary 窗口')
  assert.equal(before.hourlyTrend[0]?.requestCount, 1, '测试前应读到已发布 trend 窗口')
  assert.equal(before.modelDistribution[0]?.requestCount, 1, '测试前应读到已发布 model 窗口')
  assert.equal(before.errors[0]?.errorCount, 1, '测试前应读到已发布 error 窗口')

  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*DELETE\s+FROM\s+usage_overview_trend_windows\b/i.test(sql)) {
      throw new Error('模拟概览趋势窗口刷新失败')
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare
  try {
    await assert.rejects(
      () => usageStatsRepository.refreshUsageRankSnapshotsInStages({ yieldToEventLoop: async () => {} }),
      /模拟概览趋势窗口刷新失败/,
      '概览窗口刷新失败应向上抛出'
    )
  } finally {
    statsDatabase.prepare = originalPrepare
  }

  const afterFailure = usageStatsRepository.getUsageStatsOverview(adminAccess, range)
  assert.equal(afterFailure.summary.requestCount, 1, '概览窗口 stage 失败后 summary 不应单独发布为新数据')
  assert.equal(afterFailure.hourlyTrend[0]?.requestCount, 1, '概览窗口 stage 失败后 trend 应保留原有数据')
  assert.equal(afterFailure.modelDistribution[0]?.requestCount, 1, '概览窗口 stage 失败后 model 排行应保留原有数据')
  assert.equal(afterFailure.errors[0]?.errorCount, 1, '概览窗口 stage 失败后 error 排行应保留原有数据')
  assertOverviewWindowTables({
    systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
    windowKey,
    startDate: range.startDate,
    endDate: range.endDate,
    summaryRequests: 1,
    trendRequests: 1,
    modelRequests: 1,
    errorCount: 1
  })

  await usageStatsRepository.refreshUsageRankSnapshotsInStages({ yieldToEventLoop: async () => {} })
  const afterSuccess = usageStatsRepository.getUsageStatsOverview(adminAccess, range)
  assert.equal(afterSuccess.summary.requestCount, 5, '恢复后 summary 应发布新数据')
  assert.equal(afterSuccess.hourlyTrend[0]?.requestCount, 5, '恢复后 trend 应发布新数据')
  assert.equal(afterSuccess.modelDistribution[0]?.requestCount, 5, '恢复后 model 排行应发布新数据')
  assert.equal(afterSuccess.errors[0]?.errorCount, 2, '恢复后 error 排行应发布新数据')
  assertOverviewWindowTables({
    systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
    windowKey,
    startDate: range.startDate,
    endDate: range.endDate,
    summaryRequests: 5,
    trendRequests: 5,
    modelRequests: 5,
    errorCount: 2
  })

  console.log('用量概览窗口原子发布回归通过：概览四类窗口同一 stage 内失败不会半发布')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedPublishedOverviewWindows(systemAccountId: string, windowKey: string, startDate: string, endDate: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-01T00:00:00.000Z'
  database.prepare(`
    INSERT INTO usage_overview_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, 1, 1, 0, 10, 2, 0, 0, 0.01, 100, 1, 20, 1, ?, ?)
  `).run(systemAccountId, windowKey, startDate, endDate, `${endDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO usage_overview_trend_windows (
      system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, 1, 0, 10, 2, 0, 0, 0.01, 100, 1, ?)
  `).run(systemAccountId, windowKey, startDate, endDate, `${endDate}T00`, updatedAt)
  database.prepare(`
    INSERT INTO usage_model_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, 1, 'openai', 'published-model', 1, 10, 2, 0, 0, 0.01, ?)
  `).run(systemAccountId, windowKey, startDate, endDate, updatedAt)
  database.prepare(`
    INSERT INTO usage_error_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, error_code,
      status_code, error_message, error_count, updated_at
    ) VALUES (?, ?, ?, ?, 1, 'openai', 'published_error', 500, 'published error', 1, ?)
  `).run(systemAccountId, windowKey, startDate, endDate, updatedAt)
}

function seedNewUsageSources(statDate: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-02T00:00:00.000Z'
  for (const scope of ['sys_admin', GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, 5, 3, 2, 50, 10, 0, 0, 0.05, 500, 5, 120, 100, 5, 30, ?, ?)
    `).run(scope, scope, `${statDate}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_stats_daily (
        system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 5, 3, 2, 50, 10, 0, 0, 0.05, 500, 5, 120, 100, 5, 30, ?, ?)
    `).run(scope, scope, statDate, `${statDate}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        first_token_ms_max, last_used_at, updated_at
      ) VALUES (?, 'system_account', ?, ?, 5, 3, 2, 50, 10, 0, 0, 0.05, 500, 5, 120, 100, 5, 30, ?, ?)
    `).run(scope, scope, `${statDate}T00`, `${statDate}T00:00:00.000Z`, updatedAt)
    database.prepare(`
      INSERT INTO usage_model_daily (
        system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
      ) VALUES (?, ?, 'openai', 'new-model', 5, 3, 2, 50, 10, 0, 0, 0.05, ?)
    `).run(scope, statDate, updatedAt)
    database.prepare(`
      INSERT INTO usage_error_daily (
        system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message,
        request_count, error_count, updated_at
      ) VALUES (?, ?, 'gateway', 'openai', 'new_error', 429, 'new error', 2, 2, ?)
    `).run(scope, statDate, updatedAt)
  }
}

function assertOverviewWindowTables(input: {
  systemAccountId: string
  windowKey: string
  startDate: string
  endDate: string
  summaryRequests: number
  trendRequests: number
  modelRequests: number
  errorCount: number
}): void {
  const database = databaseModule.getStatsDatabase()
  const summary = database.prepare(`
    SELECT COUNT(*) AS rows, COALESCE(SUM(request_count), 0) AS requests
    FROM usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(input.systemAccountId, input.windowKey, input.startDate, input.endDate) as { rows?: number; requests?: number } | undefined
  const trend = database.prepare(`
    SELECT COUNT(*) AS rows, COALESCE(SUM(request_count), 0) AS requests
    FROM usage_overview_trend_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(input.systemAccountId, input.windowKey, input.startDate, input.endDate) as { rows?: number; requests?: number } | undefined
  const model = database.prepare(`
    SELECT COUNT(*) AS rows, COALESCE(SUM(request_count), 0) AS requests
    FROM usage_model_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(input.systemAccountId, input.windowKey, input.startDate, input.endDate) as { rows?: number; requests?: number } | undefined
  const errors = database.prepare(`
    SELECT COUNT(*) AS rows, COALESCE(SUM(error_count), 0) AS errors
    FROM usage_error_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(input.systemAccountId, input.windowKey, input.startDate, input.endDate) as { rows?: number; errors?: number } | undefined

  assert.equal(summary?.rows, 1, 'summary 窗口表目标窗口应只有一行')
  assert.equal(summary?.requests, input.summaryRequests, 'summary 窗口表请求数不符合预期')
  assert.equal(trend?.rows, 1, 'trend 窗口表目标窗口应只有一行')
  assert.equal(trend?.requests, input.trendRequests, 'trend 窗口表请求数不符合预期')
  assert.equal(model?.rows, 1, 'model 窗口表目标窗口应只有一行')
  assert.equal(model?.requests, input.modelRequests, 'model 窗口表请求数不符合预期')
  assert.equal(errors?.rows, 1, 'error 窗口表目标窗口应只有一行')
  assert.equal(errors?.errors, input.errorCount, 'error 窗口表错误数不符合预期')
}
