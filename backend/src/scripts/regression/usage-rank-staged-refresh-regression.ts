import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-rank-staged-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-rank-staged-refresh.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-rank-staged-refresh-secret'
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

const systemAccountId = 'sys_staged_refresh'
const scopeId = systemAccountId
const teamFilterId = 'team_staged_refresh'
const granteeSystemAccountId = 'sys_grantee_staged_refresh'

try {
  const database = databaseModule.getStatsDatabase()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate
  const fixedDates = usageStatsWindowHelpers.fixedUsageStatsDateKeys(usageStatsHelpers.usageStatsTimezone(), today)
  const fixedRangeCount = fixedDates.length * (fixedDates.length + 1) / 2
  seedOldRangeWindows(today)
  seedNewRangeSources(today)

  assert.equal(usageScopeRequestCount(today), 1, '测试前应读到旧 usage scope 范围窗口')
  assert.equal(authorizationTeamRequestCount(today), 1, '测试前应读到旧授权团队范围窗口')
  assert.equal(authorizationUserRequestCount(today), 1, '测试前应读到旧授权用户范围窗口')

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+usage_scope_range_windows_refresh_tmp\b/i.test(sql)) {
      throw new Error('模拟 usage scope 范围窗口临时表构建失败')
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    await assert.rejects(
      () => usageStatsRepository.refreshUsageRankSnapshotsInStages({ yieldToEventLoop: async () => {} }),
      /模拟 usage scope 范围窗口临时表构建失败/,
      '后台 staged 范围窗口构建失败应向上抛出'
    )
  } finally {
    database.prepare = originalPrepare
  }

  assert.equal(usageScopeRequestCount(today), 1, '临时表构建失败后不应删除正式 usage scope 范围窗口')
  assert.equal(authorizationTeamRequestCount(today), 1, '临时表构建失败后不应删除正式授权团队范围窗口')
  assert.equal(authorizationUserRequestCount(today), 1, '临时表构建失败后不应删除正式授权用户范围窗口')

  let yieldCount = 0
  const result = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    yieldToEventLoop: async () => {
      yieldCount += 1
    }
  })

  assert.ok(result.durationMs >= 0, 'staged 刷新应返回总耗时')
  assert.ok(result.stages.some((stage) => stage.name === 'usage_scope_range_windows'), '结果应包含 usage scope 范围窗口 stage')
  assert.ok(result.stages.some((stage) => stage.name === 'authorization_usage_range_windows'), '结果应包含授权范围窗口 stage')
  assert.ok(yieldCount >= fixedRangeCount * 2, '两个重型范围窗口 stage 应按日期区间分段 yield')
  assert.equal(usageScopeRequestCount(today), 5, '成功刷新后 usage scope 范围窗口应发布新数据')
  assert.equal(authorizationTeamRequestCount(today), 7, '成功刷新后授权团队范围窗口应发布新数据')
  assert.equal(authorizationUserRequestCount(today), 11, '成功刷新后授权用户范围窗口应发布新数据')

  console.log('用量排行 staged 刷新回归通过：范围窗口分段 yield，临时表失败不会半发布')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedOldRangeWindows(statDate: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-01T00:00:00.000Z'
  database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, 1, 10, 2, 0, 0, 0.01, ?, ?)
  `).run(systemAccountId, scopeId, statDate, statDate, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, 'all', '', 1, 10, 2, 0, 0, 0.01, ?, ?)
  `).run(systemAccountId, statDate, statDate, teamFilterId, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, 'all', '', 1, 10, 2, 0, 0, 0.01, ?, ?)
  `).run(systemAccountId, statDate, statDate, teamFilterId, granteeSystemAccountId, `${statDate}T00:00:00.000Z`, updatedAt)
}

function seedNewRangeSources(statDate: string): void {
  const database = databaseModule.getStatsDatabase()
  const updatedAt = '2000-01-02T00:00:00.000Z'
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, 5, 50, 10, 0, 0, 0.05, ?, ?)
  `).run(systemAccountId, scopeId, statDate, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO authorization_team_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, ?, ?, 'all', '', 7, 70, 14, 0, 0, 0.07, ?, ?)
  `).run(systemAccountId, statDate, teamFilterId, `${statDate}T00:00:00.000Z`, updatedAt)
  database.prepare(`
    INSERT INTO authorization_user_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, 'all', '', 11, 110, 22, 0, 0, 0.11, ?, ?)
  `).run(systemAccountId, statDate, teamFilterId, granteeSystemAccountId, `${statDate}T00:00:00.000Z`, updatedAt)
}

function usageScopeRequestCount(statDate: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT request_count AS requestCount
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `).get(systemAccountId, scopeId, statDate, statDate) as { requestCount?: number } | undefined
  return Number(row?.requestCount ?? 0)
}

function authorizationTeamRequestCount(statDate: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT request_count AS requestCount
    FROM authorization_team_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = 'all'
      AND resource_filter_id = ''
  `).get(systemAccountId, statDate, statDate, teamFilterId) as { requestCount?: number } | undefined
  return Number(row?.requestCount ?? 0)
}

function authorizationUserRequestCount(statDate: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT request_count AS requestCount
    FROM authorization_user_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND grantee_filter_system_account_id = ?
      AND resource_filter_type = 'all'
      AND resource_filter_id = ''
  `).get(systemAccountId, statDate, statDate, teamFilterId, granteeSystemAccountId) as { requestCount?: number } | undefined
  return Number(row?.requestCount ?? 0)
}
