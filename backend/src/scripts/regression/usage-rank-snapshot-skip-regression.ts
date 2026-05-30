import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageRankSnapshotStageName } from '../../storage/usage-stats.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-rank-snapshot-skip-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-rank-snapshot-skip.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-rank-snapshot-skip-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  usageStatsRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js')
])

const systemAccountId = 'sys_snapshot_skip'
const scopeId = systemAccountId
const jobName = 'usage-scope-range-windows-refresh-regression'
const stageNames: UsageRankSnapshotStageName[] = ['usage_scope_range_windows']

try {
  const database = databaseModule.getStatsDatabase()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate

  seedDaily(today, 5, '2000-01-01T00:00:00.000Z')

  const first = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(first.skipped, false, '首次没有水位标记时应刷新')
  assert.deepEqual(first.stages.map((stage) => stage.name), stageNames, '首次刷新应只运行指定 stage')
  assert.equal(usageScopeRequestCount(today), 5, '首次刷新应发布范围窗口')

  const second = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(second.skipped, true, '聚合水位和日期没变时应跳过')
  assert.equal(second.stages.length, 0, '跳过时不应运行任何 stage')

  seedDaily(today, 9, '2000-01-02T00:00:00.000Z')
  const third = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(third.skipped, false, '聚合表 updated_at 变化后应重新刷新')
  assert.equal(usageScopeRequestCount(today), 9, '重新刷新应发布新的范围窗口')

  database.prepare(`
    UPDATE stats_job_state
    SET cursor_id = '1999-01-01', updated_at = '2000-01-03T00:00:00.000Z'
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ?
  `).run(jobName)
  const fourth = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(fourth.skipped, false, '统计窗口日期变化时应强制刷新')

  console.log('用量排行快照跳过回归通过：无新增聚合数据跳过，水位或日期变化会重新刷新')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedDaily(statDate: string, requestCount: number, updatedAt: string): void {
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = excluded.request_count,
      input_tokens = excluded.input_tokens,
      output_tokens = excluded.output_tokens,
      total_cost_usd = excluded.total_cost_usd,
      last_used_at = excluded.last_used_at,
      updated_at = excluded.updated_at
  `).run(
    systemAccountId,
    scopeId,
    statDate,
    requestCount,
    requestCount * 10,
    requestCount * 2,
    requestCount / 100,
    `${statDate}T00:00:00.000Z`,
    updatedAt
  )
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
