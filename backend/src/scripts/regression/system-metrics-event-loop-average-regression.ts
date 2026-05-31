import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-event-loop-average-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-metrics-event-loop-average-secret'
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
  usageStatsRepository.insertSystemMetricsSample({
    cpuPercent: 10,
    memoryUsedPercent: 20,
    eventLoopLagMs: 12
  })
  usageStatsRepository.insertSystemMetricsSample({
    cpuPercent: 30,
    memoryUsedPercent: 40
  })
  usageStatsRepository.refreshUsageRankSnapshots()

  const overview = usageStatsRepository.getSystemMetricsOverview()
  assert.equal(overview.hourlyTrend.length, 1, '本测试只应生成一个系统指标趋势桶')
  const [bucket] = overview.hourlyTrend
  assert.equal(bucket.sampleCount, 2, '系统指标总样本数应记录两次采样')
  assert.equal(bucket.eventLoopLagMsSampleCount, 1, '事件循环平均值应只统计有效 lag 样本数')
  assert.equal(bucket.eventLoopLagMsAvg, 12, '事件循环平均值不应被缺失 lag 的样本稀释')
  assert.equal(bucket.cpuPercentAvg, 20, '其他总样本平均口径应保持不变')

  console.log('系统指标事件循环平均值回归通过：缺失 lag 不会压低新数据平均值')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
