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
  assert.equal(Object.prototype.hasOwnProperty.call(settings, 'dataRetentionCleanupIntervalMinutes'), false, '清理间隔不应暴露为系统设置')
  assert.equal(Object.prototype.hasOwnProperty.call(settings, 'dataRetentionCleanupBatchSize'), false, '单批删除行数不应暴露为系统设置')
  assert.equal(Object.prototype.hasOwnProperty.call(settings, 'dataRetentionCleanupMaxBatchesPerRun'), false, '单轮最大批数不应暴露为系统设置')

  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupIntervalMinutes: 5 }),
    /未知系统设置字段：dataRetentionCleanupIntervalMinutes/,
    '清理间隔属于内部常量，不能通过系统设置修改'
  )
  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupBatchSize: 5_000 }),
    /未知系统设置字段：dataRetentionCleanupBatchSize/,
    '单批删除行数属于内部常量，不能通过系统设置修改'
  )
  assert.throws(
    () => settingsRepository.updateSettings({ dataRetentionCleanupMaxBatchesPerRun: 100 }),
    /未知系统设置字段：dataRetentionCleanupMaxBatchesPerRun/,
    '单轮最大批数属于内部常量，不能通过系统设置修改'
  )

  assertSourceGuards()
  console.log('数据保留清理内部常量回归通过：10 分钟一轮、1000 行/批、20 批/轮，不再暴露为系统设置')
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
    backgroundJobsSource.includes('DATA_RETENTION_CLEANUP_INTERVAL_MINUTES * minuteMs'),
    'data-retention-cleanup 调度必须使用内部清理间隔常量'
  )
  assert(!backgroundJobsSource.includes('dataRetentionCleanupIntervalMinutes'), 'data-retention-cleanup 调度不能读取系统设置里的清理间隔')
  assert(
    !backgroundJobsSource.includes("backgroundScheduledJobName('data-retention-cleanup'), intervalMs: dailyIntervalMs"),
    'data-retention-cleanup 不能退回固定 dailyIntervalMs'
  )

  const cleanupSource = readFileSync(resolve('src/modules/background/data-retention-cleanup.service.ts'), 'utf8')
  assert(cleanupSource.includes('DATA_RETENTION_CLEANUP_BATCH_SIZE'), '清理服务必须使用内部单批行数常量')
  assert(cleanupSource.includes('DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN'), '清理服务必须使用内部单轮批数常量')
  assert(!cleanupSource.includes('dataRetentionCleanupBatchSize'), '清理服务不能读取系统设置里的单批删除行数')
  assert(!cleanupSource.includes('dataRetentionCleanupMaxBatchesPerRun'), '清理服务不能读取系统设置里的单轮最大批数')
  assert(cleanupSource.includes('DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS'), '清理服务连续批次之间必须保留 SQLite 写入间隙')
  assert(cleanupSource.includes('pauseBetweenCleanupBatches()'), '清理服务必须在继续下一批前节流')
  assert(cleanupSource.includes('checkpointDatasetAndUsageDatabases()'), '清理删除数据后必须维护 dataset / usage shard WAL')

  const constantsSource = readFileSync(resolve('src/modules/background/data-retention-cleanup.constants.ts'), 'utf8')
  assert(constantsSource.includes('DATA_RETENTION_CLEANUP_INTERVAL_MINUTES = 10'), '内部清理间隔必须固定为 10 分钟')
  assert(constantsSource.includes('DATA_RETENTION_CLEANUP_BATCH_SIZE = 1000'), '内部单批删除行数必须固定为 1000')
  assert(constantsSource.includes('DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN = 20'), '内部单轮最大批数必须固定为 20')

  const statsWriterSource = readFileSync(resolve('src/modules/background/background-stats-writer.ts'), 'utf8')
  assert(statsWriterSource.includes('cleanupStatsDatabaseAfterDelete'), 'stats-writer 清理统计数据后必须维护 stats WAL')
}
