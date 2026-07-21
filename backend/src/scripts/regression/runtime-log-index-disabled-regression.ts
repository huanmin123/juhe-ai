import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

process.env.JUHE_AI_RUNTIME_LOG_INDEX_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'true'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'

const { runtimeConfig } = await import('../../config/runtime.js')
const tempRoot = resolve(tmpdir(), `juhe-ai-runtime-log-index-disabled-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.secret = 'runtime-log-index-disabled-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
assert.equal(runtimeConfig.log.indexEnabled, false)
const backgroundJobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
const retentionSource = readFileSync(resolve('src/modules/background/data-retention-cleanup.service.ts'), 'utf8')
const postgresRetentionSource = readFileSync(resolve('src/modules/background/maintenance-cleanup-jobs.ts'), 'utf8')
assert.match(backgroundJobsSource, /if \(runtimeConfig\.log\.indexEnabled\)/, '索引关闭时不得调度或执行索引维护')
assert.match(retentionSource, /if \(runtimeConfig\.log\.indexEnabled\)[\s\S]*cleanupRuntimeLogIndex/, 'SQLite 保留清理不得在索引关闭时触碰运行日志索引')
assert.match(postgresRetentionSource, /if \(runtimeConfig\.log\.indexEnabled\)[\s\S]*cleanupRuntimeLogIndexAsync/, 'PostgreSQL 保留清理不得在索引关闭时触碰运行日志索引')

const [database, importer, repository, loggerModule] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-logs.repository.js'),
  import('../../shared/logger.js')
])

try {
  database.getDatasetDatabase()
  const marker = `runtime-log-index-disabled-${Date.now()}`
  const logPath = join(logDir, 'juhe-ai.ingest-worker.log')
  loggerModule.logger.info({ event: 'runtime_log_index_disabled_marker', marker }, '运行日志索引关闭时仍应写入文件')
  await sleep(100)

  importer.startRuntimeLogFileImport()
  await sleep(2200)

  assert.match(readFileSync(logPath, 'utf8'), new RegExp(marker))
  assert.equal(repository.listRuntimeLogs({ page: 1, pageSize: 20 }).total, 0, '索引关闭后不得入库 runtime_logs')
  const cursorCount = database.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS count FROM runtime_log_file_cursors')
    .get() as { count: number }
  assert.equal(cursorCount.count, 0, '索引关闭后不得写入 runtime_log_file_cursors')
  const facets = repository.getRuntimeLogFacets()
  assert.equal(facets.totalIndexed, 0)
  const runtime = importer.getRuntimeLogFileImportRuntime()
  assert.equal(importer.getRuntimeLogDiscoveryReadCountForTest(), 0, '索引关闭后不得扫描日志目录')
  assert.equal(runtime.pendingFileCount, 0)
  assert.equal(runtime.pendingBytes, 0)
  console.log('runtime log index disabled regression passed')
} finally {
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
