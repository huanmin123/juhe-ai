import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { sqliteBusyTimeoutMs } from '../../storage/sqlite-config.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-busy-timeout-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'sqlite-busy-timeout-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  assert(sqliteBusyTimeoutMs <= 100, `SQLite busy_timeout 应保持在 100ms 以内，当前为 ${sqliteBusyTimeoutMs}ms`)
  assert.equal(readBusyTimeout(databaseModule.getBusinessDatabase()), sqliteBusyTimeoutMs, '业务库 busy_timeout 应使用统一低等待配置')
  assert.equal(readBusyTimeout(databaseModule.getDatasetDatabase()), sqliteBusyTimeoutMs, '数据集目录库 busy_timeout 应使用统一低等待配置')
  assert.equal(readBusyTimeout(databaseModule.getStatsDatabase()), sqliteBusyTimeoutMs, '统计库 busy_timeout 应使用统一低等待配置')

  const shardLocation = usageRecordShards.usageRecordShardLocationForRecord('usage_20260101_s00_boundary', '2026-01-01T00:00:00.000Z')
  assert.equal(readBusyTimeout(usageRecordShards.getUsageRecordShardDatabase(shardLocation)), sqliteBusyTimeoutMs, 'usage shard busy_timeout 应使用统一低等待配置')

  console.log('SQLite busy timeout 边界回归通过：所有运行库锁等待保持在 100ms 以内，避免 DB service 秒级事件循环阻塞')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function readBusyTimeout(database: { prepare: (sql: string) => { get: () => Record<string, unknown> | undefined } }): number {
  const row = database.prepare('PRAGMA busy_timeout').get()
  const value = row ? Object.values(row)[0] : undefined
  return Number(value ?? Number.NaN)
}
