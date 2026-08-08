import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { cleanupRuntimeLogIndexRetention } from '../../modules/runtime-logs/runtime-log-index-retention.service.js'
import { getRuntimeLogFileImportRuntime, startRuntimeLogFileImport, stopRuntimeLogFileImport } from '../../modules/runtime-logs/runtime-log-index-lifecycle.js'

const previousOwner = runtimeConfig.log.indexOwner
const previousEnabled = runtimeConfig.log.indexEnabled
const previousFileEnabled = runtimeConfig.log.fileEnabled
const previousDatabasePath = runtimeConfig.databasePath
const previousDatasetPath = runtimeConfig.datasetDatabasePath
const previousStatsPath = runtimeConfig.statsDatabasePath
const previousDatabaseDriver = runtimeConfig.databaseDriver
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-owner-gate-'))

try {
  runtimeConfig.log.indexOwner = 'go'
  runtimeConfig.log.indexEnabled = true
  runtimeConfig.log.fileEnabled = true
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  startRuntimeLogFileImport()
  const runtime = getRuntimeLogFileImportRuntime()
  assert.equal(runtime.currentFile, undefined, 'Go owner 下 Node importer 不得开始轮询文件')

  let runtimeLogCleanupCalls = 0
  let cursorCleanupCalls = 0
  const result = await cleanupRuntimeLogIndexRetention({
    cutoffIso: '2026-08-01T00:00:00.000Z',
    batchSize: 10,
    maxBatches: 1
  }, {
    cleanupRuntimeLogs: async () => {
      runtimeLogCleanupCalls += 1
      return 1
    },
    cleanupRuntimeLogFileCursors: async () => {
      cursorCleanupCalls += 1
      return 1
    }
  })
  assert.deepEqual(result, { runtimeLogs: 0, runtimeLogFileCursors: 0 })
  assert.equal(runtimeLogCleanupCalls, 0, 'Go owner 下 Node runtime_logs retention 不得执行')
  assert.equal(cursorCleanupCalls, 0, 'Go owner 下 Node cursor retention 不得执行')

  const [database, hardCleanup] = await Promise.all([
    import('../../storage/database.js'),
    import('../../storage/data-retention-hard-cleanup.js')
  ])
  const dataset = database.getDatasetDatabase()
  dataset.prepare(`
    INSERT INTO runtime_logs (id, time, level, raw_json, created_at)
    VALUES ('runtime_log_owner_gate', '2000-01-01T00:00:00.000Z', 'info', '{}', '2000-01-01T00:00:00.000Z')
  `).run()
  const deletedKeys: string[] = []
  hardCleanup.cleanupDiscoveredHardCleanupTablesBefore(
    'dataset',
    hardCleanup.hardCleanupCutoffs('2001-01-01T00:00:00.000Z'),
    100,
    (key: string) => deletedKeys.push(key)
  )
  const retained = dataset.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE id = ?').get('runtime_log_owner_gate') as { count: number }
  assert.equal(retained.count, 1, 'Go owner 下通用 SQLite 硬清理不得绕过 F1 runtime_logs')
  assert.ok(!deletedKeys.some((key) => key.includes('runtime_log_')), 'Go owner 下通用 SQLite 硬清理不得报告 F1 表删除')
  database.closeStorageDatabases()
  console.log('Runtime log owner gate regression passed')
} finally {
  runtimeConfig.log.indexOwner = previousOwner
  runtimeConfig.log.indexEnabled = previousEnabled
  runtimeConfig.log.fileEnabled = previousFileEnabled
  runtimeConfig.databasePath = previousDatabasePath
  runtimeConfig.datasetDatabasePath = previousDatasetPath
  runtimeConfig.statsDatabasePath = previousStatsPath
  runtimeConfig.databaseDriver = previousDatabaseDriver
  await stopRuntimeLogFileImport()
  rmSync(tempRoot, { recursive: true, force: true })
}
