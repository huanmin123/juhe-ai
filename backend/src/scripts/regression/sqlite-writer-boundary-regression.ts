import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-writer-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'sqlite-writer-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')

try {
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('business'), 'db-service')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('dataset'), 'ingest-worker')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('stats'), 'stats-writer')

  runtimeConfig.processRole = 'db-service'
  runtimeConfig.workerRole = 'worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), true)

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'maintenance-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)

  console.log('SQLite writer boundary 回归通过：主库 owner 划分、当前进程归属和严格模式入口已就绪')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
