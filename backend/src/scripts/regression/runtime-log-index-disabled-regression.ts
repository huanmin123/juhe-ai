import assert from 'node:assert/strict'
import { existsSync, mkdirSync, readFileSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
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
runtimeConfig.log.retentionDays = 1
runtimeConfig.log.maxFiles = 8
assert.equal(runtimeConfig.log.indexEnabled, false)
const backgroundJobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
assert.match(backgroundJobsSource, /if \(runtimeConfig\.log\.indexEnabled\)/, '索引关闭时不得调度或执行索引维护')

const [database, importer, repository, loggerModule, retention] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-logs.repository.js'),
  import('../../shared/logger.js'),
  import('../../modules/runtime-logs/runtime-log-index-retention.service.js')
])

try {
  database.getDatasetDatabase()
  const marker = `runtime-log-index-disabled-${Date.now()}`
  const logPath = join(logDir, 'juhe-ai.ingest-worker.log')
  const rotatedPath = join(logDir, 'juhe-ai.ingest-worker.20200101T000000Z.00000000-0000-0000-0000-000000000001.log')
  writeFileSync(rotatedPath, 'expired rotated log\n', 'utf8')
  const expiredAt = new Date('2020-01-01T00:00:00.000Z')
  utimesSync(rotatedPath, expiredAt, expiredAt)
  repository.createRuntimeLogsBatch([{
    id: 'runtime_log_retention_disabled_fixture',
    logFile: logPath,
    time: expiredAt.toISOString(),
    level: 'warn',
    event: 'retention_disabled_fixture',
    message: '历史索引在总开关关闭后必须保留',
    rawJson: JSON.stringify({ level: 'warn', msg: '历史索引在总开关关闭后必须保留' }),
    createdAt: expiredAt.toISOString()
  }])
  repository.upsertRuntimeLogFileCursor({
    logFile: 'historical-runtime.log',
    fileIdentity: 'historical-runtime-file',
    cursorOffset: 128,
    lineNumber: 1,
    fileSize: 128,
    fileMtimeMs: expiredAt.getTime(),
    lastReadAt: expiredAt.toISOString()
  })
  const retainedTableCountsBefore = runtimeLogTableCounts(database.getDatasetDatabase())
  const sqliteCleanup = await retention.cleanupRuntimeLogIndexRetention({
    cutoffIso: '2026-07-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 2
  })
  assert.deepEqual(sqliteCleanup, { runtimeLogs: 0, runtimeLogFileCursors: 0 })
  assert.deepEqual(runtimeLogTableCounts(database.getDatasetDatabase()), retainedTableCountsBefore, 'SQLite 保留清理关闭态不得删除历史索引、cursor 或 facets')

  runtimeConfig.databaseDriver = 'postgres'
  const postgresCleanup = await retention.cleanupRuntimeLogIndexRetention({
    cutoffIso: '2026-07-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 2
  }, {
    cleanupRuntimeLogs: async () => { throw new Error('PostgreSQL runtime_logs cleanup 不应执行') },
    cleanupRuntimeLogFileCursors: async () => { throw new Error('PostgreSQL cursor cleanup 不应执行') }
  })
  assert.deepEqual(postgresCleanup, { runtimeLogs: 0, runtimeLogFileCursors: 0 })
  runtimeConfig.databaseDriver = 'sqlite'
  loggerModule.logger.info({ event: 'runtime_log_index_disabled_marker', marker }, '运行日志索引关闭时仍应写入文件')
  await sleep(100)

  importer.startRuntimeLogFileImport()
  loggerModule.startLogMaintenance()
  await sleep(2200)

  assert.match(readFileSync(logPath, 'utf8'), new RegExp(marker))
  assert.equal(existsSync(rotatedPath), false, '索引关闭后过期轮转文件仍应按普通保留策略清理')
  assert.equal(repository.listRuntimeLogs({ page: 1, pageSize: 20 }).total, 1, '索引关闭后不得新增 runtime_logs，历史索引仍须保留')
  const cursorCount = database.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS count FROM runtime_log_file_cursors')
    .get() as { count: number }
  assert.equal(cursorCount.count, 1, '索引关闭后不得新增 cursor，历史 cursor 仍须保留')
  const facets = repository.getRuntimeLogFacets()
  assert.equal(facets.totalIndexed, 0, '超出当前查询窗口的历史索引不应被 facets API 计入')
  const runtime = importer.getRuntimeLogFileImportRuntime()
  assert.equal(importer.getRuntimeLogDiscoveryReadCountForTest(), 0, '索引关闭后不得扫描日志目录')
  assert.equal(runtime.pendingFileCount, 0)
  assert.equal(runtime.pendingBytes, 0)
  console.log('runtime log index disabled regression passed')
} finally {
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function runtimeLogTableCounts(database: { prepare(sql: string): { get(): unknown } }): Record<string, number> {
  return Object.fromEntries([
    'runtime_logs',
    'runtime_log_file_cursors',
    'runtime_log_facet_summary',
    'runtime_log_level_facets',
    'runtime_log_event_facets'
  ].map((tableName) => {
    const row = database.prepare(`SELECT COUNT(*) AS count FROM ${tableName}`).get() as { count: number }
    return [tableName, row.count]
  }))
}
