import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

process.env.JUHE_AI_RUNTIME_LOG_INDEX_ENABLED = 'true'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'true'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-stop-'))
const logDirectory = join(root, 'logs')
mkdirSync(logDirectory)

const { runtimeConfig } = await import('../../config/runtime.js')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.workerReplicaIndex = 0
runtimeConfig.secret = 'runtime-log-import-stop-secret'
runtimeConfig.databasePath = join(root, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(root, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(root, 'stats.sqlite3')
runtimeConfig.log.directory = logDirectory
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.indexEnabled = true

const [database, importer, loggerModule] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../shared/logger.js')
])

try {
  database.getDatasetDatabase()
  importer.startRuntimeLogFileImport()
  await waitUntil(() => importer.getRuntimeLogFileImportLifecycleForTest().pollRunCount >= 1)
  await importer.stopRuntimeLogFileImport({ drainTimeoutMs: 2_000 })

  const stopped = importer.getRuntimeLogFileImportLifecycleForTest()
  assert.equal(stopped.started, false)
  assert.equal(stopped.pollScheduled, false)
  assert.equal(stopped.pollRunning, false)

  await sleep(1_200)
  const afterPollInterval = importer.getRuntimeLogFileImportLifecycleForTest()
  assert.equal(afterPollInterval.pollRunCount, stopped.pollRunCount, '停止后已完成 poll 不得重新排程下一轮')
  assert.equal(afterPollInterval.pollScheduled, false)
  console.log('运行日志 importer 停止与防重排回归通过')
} finally {
  await importer.stopRuntimeLogFileImport({ drainTimeoutMs: 100 })
  database.closeStorageDatabases()
  await loggerModule.closeLogger()
  rmSync(root, { recursive: true, force: true })
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 3_000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待 runtime log importer 首轮 poll 超时')
    await sleep(20)
  }
}
