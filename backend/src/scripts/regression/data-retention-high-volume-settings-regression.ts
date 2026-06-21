import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-data-retention-high-volume-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'data-retention-high-volume-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, settingsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/settings.repository.js')
])

try {
  const settings = settingsRepository.getSettings()
  assert.equal(settings.dataRetentionCleanupIntervalMinutes, 10, '数据保留清理默认应每 10 分钟运行')
  assert.equal(settings.dataRetentionCleanupBatchSize, 1000, '数据保留清理默认单批应为 1000 行')
  assert.equal(settings.dataRetentionCleanupMaxBatchesPerRun, 20, '数据保留清理默认单轮应为 20 批')
  assert.equal(
    Number(settings.dataRetentionCleanupBatchSize) * Number(settings.dataRetentionCleanupMaxBatchesPerRun),
    20_000,
    '数据保留清理默认单轮每类数据处理能力应为 2 万行，靠周期持续追平'
  )

  const updated = settingsRepository.updateSettings({
    dataRetentionCleanupIntervalMinutes: 5,
    dataRetentionCleanupBatchSize: 5_000,
    dataRetentionCleanupMaxBatchesPerRun: 100
  })
  assert.equal(updated.dataRetentionCleanupIntervalMinutes, 5, '清理间隔应允许调到 5 分钟')
  assert.equal(updated.dataRetentionCleanupBatchSize, 5_000, '单批行数应允许调到 5000')
  assert.equal(updated.dataRetentionCleanupMaxBatchesPerRun, 100, '单轮批数应允许调到 100')

  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupIntervalMinutes: 4 }),
    /dataRetentionCleanupIntervalMinutes 必须在 5 到 1440 之间/,
    '清理间隔不能低于 5 分钟，避免维护任务过于激进'
  )
  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupBatchSize: 5_001 }),
    /dataRetentionCleanupBatchSize 必须在 100 到 5000 之间/,
    '单批行数必须按 SQLite 写锁压力限制在 5000 以内'
  )
  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupMaxBatchesPerRun: 101 }),
    /dataRetentionCleanupMaxBatchesPerRun 必须在 1 到 100 之间/,
    '单轮批数仍必须有上限'
  )

  assertSourceGuards()
  console.log('高流量数据保留清理设置回归通过：默认 10 分钟一轮、1000 行/批、2 万行/类/轮，按 SQLite 小批多轮维护')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertSourceGuards(): void {
  const backgroundJobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
  assert(
    backgroundJobsSource.includes("settingsNumber('dataRetentionCleanupIntervalMinutes', 5, 1440) * minuteMs"),
    'data-retention-cleanup 调度必须读取 dataRetentionCleanupIntervalMinutes'
  )
  assert(
    !backgroundJobsSource.includes("backgroundScheduledJobName('data-retention-cleanup'), intervalMs: dailyIntervalMs"),
    'data-retention-cleanup 不能退回固定 dailyIntervalMs'
  )

  const cleanupSource = readFileSync(resolve('src/modules/background/data-retention-cleanup.service.ts'), 'utf8')
  assert(cleanupSource.includes('retentionCleanupBatchSizeMax = 5_000'), '清理服务单批上限必须控制在 5000 以内')
  assert(cleanupSource.includes('retentionCleanupMaxBatchesMax = 100'), '清理服务运行时批数上限必须控制在 100 以内')
  assert(cleanupSource.includes('retentionCleanupBatchPauseMs = 25'), '清理服务连续批次之间必须保留 SQLite 写入间隙')
  assert(cleanupSource.includes('pauseBetweenCleanupBatches()'), '清理服务必须在继续下一批前节流')
  assert(cleanupSource.includes('checkpointDatasetAndUsageDatabases()'), '清理删除数据后必须维护 dataset / usage shard WAL')

  const statsWriterSource = readFileSync(resolve('src/modules/background/background-stats-writer.ts'), 'utf8')
  assert(statsWriterSource.includes('cleanupStatsDatabaseAfterDelete'), 'stats-writer 清理统计数据后必须维护 stats WAL')
}
