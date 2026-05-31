import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-window-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-metrics-window-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const database = databaseModule.getStatsDatabase()
  const range = usageStatsRepository.normalizeDefaultUsageStatsRange()
  const windowKey = rangeWindowKey(range)
  const bucketKey = `${range.endDate}T00`
  database.prepare(`
    INSERT INTO process_event_loop_trend_windows (
      window_key, start_date, end_date, bucket_key, process_role, sample_count,
      event_loop_lag_ms_sum, event_loop_lag_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, 'server', 3, 60, 30, ?)
  `).run(windowKey, range.startDate, range.endDate, bucketKey, '2026-01-01T00:00:00.000Z')

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    if (/FROM\s+process_event_loop_samples\b[\s\S]*ORDER\s+BY\s+sampled_at\s+ASC,\s*process_role\s+ASC,\s*id\s+ASC/i.test(sql)) {
      throw new Error('系统指标接口不应按时间窗扫描进程事件循环原始采样')
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    const overview = usageStatsRepository.getSystemMetricsOverview(range)
    assert.equal(overview.processEventLoopTrend.length, 1, '系统指标趋势应读取窗口缓存')
    assert.equal(overview.processEventLoopTrend[0]?.processRole, 'server', '窗口缓存应保留进程角色')
    assert.equal(overview.processEventLoopTrend[0]?.sampleCount, 3, '窗口缓存应保留采样数')
    assert.equal(overview.processEventLoopTrend[0]?.eventLoopLagMsAvg, 20, '窗口缓存应计算平均延迟')
    assert.equal(overview.processEventLoopTrend[0]?.eventLoopLagMsMax, 30, '窗口缓存应保留峰值延迟')
  } finally {
    database.prepare = originalPrepare
  }

  console.log('系统指标窗口查询回归通过：统计概览不再按时间窗扫描进程事件循环原始采样')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
