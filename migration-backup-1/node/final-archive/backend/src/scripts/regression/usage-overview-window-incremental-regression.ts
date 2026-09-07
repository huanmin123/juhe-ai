import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageRankSnapshotStageName } from '../../storage/usage-stats.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-overview-window-incremental-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-overview-window-incremental.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-overview-window-incremental-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  usageStatsRepository,
  usageStatsHelpers,
  usageStatsWindowHelpers
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/usage-stats-window-helpers.js')
])

const systemAccountId = 'sys_overview_incremental'
const jobName = 'usage-overview-windows-refresh-incremental-regression'
const stageNames: UsageRankSnapshotStageName[] = ['usage_overview_windows']
const sentinelUpdatedAt = '1999-12-31T00:00:00.000Z'

try {
  const database = databaseModule.getStatsDatabase()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate
  const fixedDates = usageStatsWindowHelpers.fixedUsageStatsDateKeys(usageStatsHelpers.usageStatsTimezone(), today)
  const yesterday = fixedDates[fixedDates.length - 2]
  assert.ok(yesterday, '用量概览增量回归需要至少两个固定日期')

  seedOverviewSource(yesterday, 3, '2000-01-01T00:00:00.000Z')
  seedOverviewSource(today, 5, '2000-01-01T00:00:00.000Z')

  const first = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(first.skipped, false, '首次 overview 窗口刷新不应跳过')
  assert.equal(summaryRequestCount(yesterday), 3, '首次刷新应发布昨日 summary 窗口')
  assert.equal(summaryRequestCount(today), 5, '首次刷新应发布今日 summary 窗口')

  markOverviewWindowUpdatedAt(yesterday, sentinelUpdatedAt)
  seedOverviewSource(today, 9, '2000-01-02T00:00:00.000Z')

  const second = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(second.skipped, false, '今日源表 updated_at 变化后应刷新 overview 窗口')
  assert.equal(summaryRequestCount(today), 9, '今日 overview summary 应刷新为最新请求数')
  assert.equal(summaryUpdatedAt(yesterday), sentinelUpdatedAt, '仅今日变更时不应重写昨日 summary 窗口')
  assert.equal(trendUpdatedAt(yesterday), sentinelUpdatedAt, '仅今日变更时不应重写昨日 trend 窗口')
  assert.equal(modelUpdatedAt(yesterday), sentinelUpdatedAt, '仅今日变更时不应重写昨日 model rank 窗口')
  assert.equal(errorUpdatedAt(yesterday), sentinelUpdatedAt, '仅今日变更时不应重写昨日 error rank 窗口')

  const skipped = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'overview 源水位不变时应跳过')

  console.log('用量概览窗口增量回归通过：仅重刷变更日期及之后的 end_date 窗口')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedOverviewSource(statDate: string, requestCount: number, updatedAt: string): void {
  const database = databaseModule.getStatsDatabase()
  const statHour = `${statDate}T00`
  database.prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, 0, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = excluded.request_count,
      success_count = excluded.success_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      duration_ms_sum = excluded.duration_ms_sum,
      duration_ms_count = excluded.duration_ms_count,
      duration_ms_max = excluded.duration_ms_max,
      first_token_ms_sum = excluded.first_token_ms_sum,
      first_token_ms_count = excluded.first_token_ms_count,
      first_token_ms_max = excluded.first_token_ms_max,
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `).run(systemAccountId, systemAccountId, requestCount, requestCount, requestCount * 10, requestCount * 2, requestCount / 100, requestCount * 100, requestCount, requestCount * 20, requestCount * 30, requestCount, requestCount * 5, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, 0, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = excluded.request_count,
      success_count = excluded.success_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      duration_ms_sum = excluded.duration_ms_sum,
      duration_ms_count = excluded.duration_ms_count,
      duration_ms_max = excluded.duration_ms_max,
      first_token_ms_sum = excluded.first_token_ms_sum,
      first_token_ms_count = excluded.first_token_ms_count,
      first_token_ms_max = excluded.first_token_ms_max,
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `).run(systemAccountId, systemAccountId, statDate, requestCount, requestCount, requestCount * 10, requestCount * 2, requestCount / 100, requestCount * 100, requestCount, requestCount * 20, requestCount * 30, requestCount, requestCount * 5, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, 0, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_hour) DO UPDATE SET
      request_count = excluded.request_count,
      success_count = excluded.success_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      duration_ms_sum = excluded.duration_ms_sum,
      duration_ms_count = excluded.duration_ms_count,
      duration_ms_max = excluded.duration_ms_max,
      first_token_ms_sum = excluded.first_token_ms_sum,
      first_token_ms_count = excluded.first_token_ms_count,
      first_token_ms_max = excluded.first_token_ms_max,
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `).run(systemAccountId, systemAccountId, statHour, requestCount, requestCount, requestCount * 10, requestCount * 2, requestCount / 100, requestCount * 100, requestCount, requestCount * 20, requestCount * 30, requestCount, requestCount * 5, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO usage_model_daily (
      system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
    ) VALUES (?, ?, 'gpt', 'gpt-5.5-incremental', ?, ?, 0, ?, ?, 0, 0, ?, ?)
    ON CONFLICT(system_account_id, stat_date, provider_code, model) DO UPDATE SET
      request_count = excluded.request_count,
      success_count = excluded.success_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, requestCount, requestCount, requestCount * 10, requestCount * 2, requestCount / 100, updatedAt)
  database.prepare(`
    INSERT INTO usage_error_daily (
      system_account_id, stat_date, error_group, provider_code, error_code, status_code,
      error_message, request_count, error_count, updated_at
    ) VALUES (?, ?, 'upstream', 'gpt', 'incremental_error', 502, 'incremental error', 1, 1, ?)
    ON CONFLICT(system_account_id, stat_date, error_group, provider_code, error_code, status_code) DO UPDATE SET
      request_count = excluded.request_count,
      error_count = excluded.error_count,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, updatedAt)
}

function summaryRequestCount(statDate: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT request_count AS requestCount
    FROM usage_overview_summary_windows
    WHERE system_account_id = ?
      AND window_key = ?
  `).get(systemAccountId, usageStatsWindowHelpers.rangeWindowKey({ startDate: statDate, endDate: statDate })) as { requestCount?: number } | undefined
  return Number(row?.requestCount ?? 0)
}

function summaryUpdatedAt(statDate: string): string | undefined {
  return overviewUpdatedAt('usage_overview_summary_windows', statDate)
}

function trendUpdatedAt(statDate: string): string | undefined {
  return overviewUpdatedAt('usage_overview_trend_windows', statDate)
}

function modelUpdatedAt(statDate: string): string | undefined {
  return overviewUpdatedAt('usage_model_rank_windows', statDate)
}

function errorUpdatedAt(statDate: string): string | undefined {
  return overviewUpdatedAt('usage_error_rank_windows', statDate)
}

function overviewUpdatedAt(tableName: string, statDate: string): string | undefined {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT updated_at AS updatedAt
    FROM ${tableName}
    WHERE system_account_id = ?
      AND window_key = ?
    LIMIT 1
  `).get(systemAccountId, usageStatsWindowHelpers.rangeWindowKey({ startDate: statDate, endDate: statDate })) as { updatedAt?: string } | undefined
  return row?.updatedAt
}

function markOverviewWindowUpdatedAt(statDate: string, updatedAt: string): void {
  const windowKey = usageStatsWindowHelpers.rangeWindowKey({ startDate: statDate, endDate: statDate })
  for (const tableName of ['usage_overview_summary_windows', 'usage_overview_trend_windows', 'usage_model_rank_windows', 'usage_error_rank_windows']) {
    databaseModule.getStatsDatabase().prepare(`
      UPDATE ${tableName}
      SET updated_at = ?
      WHERE system_account_id = ?
        AND window_key = ?
    `).run(updatedAt, systemAccountId, windowKey)
  }
}
