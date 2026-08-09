import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, readdirSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-clean-start-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const codexShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = codexShardRoot
runtimeConfig.codexContextStateShardCount = 4
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.secret = 'table-monitor-clean-start-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const previousStrictBoundary = process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT
const databaseModule = await import('../../storage/database.js')
const tableMonitorRepository = await import('../../storage/table-monitor.repository.js')

try {
  process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  databaseModule.getBusinessDatabase()
  databaseModule.getDatasetDatabase()
  databaseModule.getUsageCatalogDatabase()
  databaseModule.getStatsDatabase()
  databaseModule.closeStorageDatabases()

  process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '1'
  const cleanResult = tableMonitorRepository.collectTableStorageSnapshot('2026-07-16T00:00:00.000Z', {
    tableScanMode: 'full'
  })
  assert.equal(cleanResult.databaseSnapshots, 4, 'Codex context 尚无实际 shard 时应采样其余四库，不应只读打开不存在的 shard')
  assert.equal(existsSync(codexShardRoot), false, '监控只读路径不能在干净启动时越权创建 Codex context shard 目录')

  mkdirSync(codexShardRoot, { recursive: true })
  const emptyRootResult = tableMonitorRepository.collectTableStorageSnapshot('2026-07-16T00:02:00.000Z', {
    tableScanMode: 'full'
  })
  assert.equal(emptyRootResult.databaseSnapshots, 4, '空 shard 根目录仍应只采样其余四库')
  assert.deepEqual(readdirSync(codexShardRoot), [], '监控只读路径不能在空 shard 根目录中补建配置 shard')
  rmSync(codexShardRoot, { recursive: true })

  writeFileSync(codexShardRoot, 'not-a-directory')
  assert.throws(
    () => tableMonitorRepository.collectTableStorageSnapshot('2026-07-16T00:05:00.000Z', {
      tableScanMode: 'full'
    }),
    (error: unknown) => (
      error instanceof Error
      && 'code' in error
      && error.code === 'ENOTDIR'
    ),
    'Codex context shard root 已存在但不是目录时必须传播路径错误，不能按空 shard 静默跳过'
  )
  unlinkSync(codexShardRoot)

  databaseModule.closeStorageDatabases()
  process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'
  runtimeConfig.processRole = 'db-service'
  const existingShard = databaseModule.getCodexContextStateShardDatabase(2)
  existingShard.exec('CREATE TABLE table_monitor_existing_shard_probe (id TEXT PRIMARY KEY)')
  existingShard.prepare('INSERT INTO table_monitor_existing_shard_probe (id) VALUES (?)').run('probe-1')
  databaseModule.closeStorageDatabases()

  process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '1'
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  const existingResult = tableMonitorRepository.collectTableStorageSnapshot('2026-07-16T00:10:00.000Z', {
    tableScanMode: 'full'
  })
  assert.equal(existingResult.databaseSnapshots, 5, '存在任意真实 Codex context shard 时应继续提供五库采样')
  assert.deepEqual(
    readdirSync(codexShardRoot).filter((name) => name.endsWith('.sqlite3')).sort(),
    ['state-002.sqlite3'],
    '监控应只打开真实 shard，不能补建其余配置 shard'
  )
  const probeSnapshot = databaseModule.getStatsDatabase().prepare(`
    SELECT table_name, parent_table_name, table_kind, is_partition, row_count
    FROM table_storage_snapshots
    WHERE database_role = 'codex-context-state'
      AND parent_table_name = 'table_monitor_existing_shard_probe'
      AND sampled_at = ?
  `).get('2026-07-16T00:10:00.000Z') as {
    table_name?: string
    parent_table_name?: string
    table_kind?: string
    is_partition?: number
    row_count?: number
  } | undefined
  assert.equal(probeSnapshot?.table_name, 'state-002.sqlite3:table_monitor_existing_shard_probe', 'shard-table pair 快照必须使用稳定物理表名，避免把局部数据伪装成全 shard 聚合')
  assert.equal(probeSnapshot?.parent_table_name, 'table_monitor_existing_shard_probe', 'pair 快照应保留逻辑表名用于分组展示')
  assert.equal(probeSnapshot?.table_kind, 'shard_table')
  assert.equal(probeSnapshot?.is_partition, 1)
  assert.equal(probeSnapshot?.row_count, 1, '非 0 shard 中的真实表必须被聚合监控，不能为了规避首次启动错误跳过实际数据')

  console.log('表监控 Codex context 干净启动回归通过：空 shard 不越权建库，已有非 0 shard 仍完整采样')
} finally {
  databaseModule.closeStorageDatabases()
  if (previousStrictBoundary === undefined) {
    delete process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT
  } else {
    process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = previousStrictBoundary
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
