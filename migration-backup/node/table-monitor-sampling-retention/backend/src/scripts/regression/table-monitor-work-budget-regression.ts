import { strict as assert } from 'node:assert'
import { mkdirSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const shardCount = 256
const pairBudget = 7
const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-work-budget-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const codexShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = codexShardRoot
runtimeConfig.codexContextStateShardCount = shardCount
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.secret = 'table-monitor-work-budget-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(codexShardRoot, { recursive: true })
logger.level = 'silent'

const previousStrictBoundary = process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT
process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'
runtimeConfig.processRole = 'db-service'
const databaseModule = await import('../../storage/database.js')
databaseModule.getBusinessDatabase()
databaseModule.getDatasetDatabase()
databaseModule.getUsageCatalogDatabase()
databaseModule.getStatsDatabase()
databaseModule.closeStorageDatabases()

for (let shardIndex = 0; shardIndex < shardCount; shardIndex += 1) {
  const database = new DatabaseSync(databaseModule.codexContextStateShardPath(shardIndex))
  database.exec('CREATE TABLE pair_budget_target (id TEXT PRIMARY KEY, value TEXT)')
  database.exec('CREATE TABLE pair_budget_secondary (id TEXT PRIMARY KEY, value TEXT)')
  database.prepare('INSERT INTO pair_budget_target (id, value) VALUES (?, ?)').run(`row-${shardIndex}`, `value-${shardIndex}`)
  database.prepare('INSERT INTO pair_budget_secondary (id, value) VALUES (?, ?)').run(`secondary-${shardIndex}`, `value-${shardIndex}`)
  database.close()
}

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '1'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'stats-worker'
const tableMonitorRepository = await import('../../storage/table-monitor.repository.js')

const restores: Array<() => void> = []
let exactTableCounts = 0
let dbstatQueries = 0
let shardCatalogQueries = 0
let shardPragmaQueries = 0
const catalogTouchedShards = new Set<number>()

try {
  for (let shardIndex = 0; shardIndex < shardCount; shardIndex += 1) {
    const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
    const originalPrepare = database.prepare.bind(database)
    database.prepare = ((sql: string) => {
      if (/SELECT\s+COUNT\s*\(\s*\*\s*\).*?FROM\s+["`]?pair_budget_target/i.test(sql)) {
        exactTableCounts += 1
      }
      if (/\bFROM\s+dbstat\b/i.test(sql)) {
        dbstatQueries += 1
      }
      if (/\bFROM\s+sqlite_schema\b/i.test(sql)) {
        shardCatalogQueries += 1
        catalogTouchedShards.add(shardIndex)
      }
      if (/^\s*PRAGMA\s+(?:page_size|page_count|freelist_count)\b/i.test(sql)) {
        shardPragmaQueries += 1
      }
      return originalPrepare(sql)
    }) as typeof database.prepare
    restores.push(() => {
      database.prepare = originalPrepare as typeof database.prepare
    })
  }

  const oldSampledAt = '2000-01-01T00:00:00.000Z'
  tableMonitorRepository.collectTableStorageSnapshot(oldSampledAt, { tableScanMode: 'none' })
  tableMonitorRepository.collectTableStorageSnapshot('2026-01-01T00:00:00.000Z', { tableScanMode: 'none' })
  const retainedOldDatabaseSnapshots = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM database_storage_snapshots WHERE sampled_at = ?')
    .get(oldSampledAt) as { total?: number } | undefined
  assert(Number(retainedOldDatabaseSnapshots?.total ?? 0) > 0, '表监控采样不得顺带删除旧快照，retention 必须由中央清理任务唯一负责')
  tableMonitorRepository.cleanupTableStorageSnapshotsBefore('2025-01-01T00:00:00.000Z', 10000)
  const cleanedOldDatabaseSnapshots = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM database_storage_snapshots WHERE sampled_at = ?')
    .get(oldSampledAt) as { total?: number } | undefined
  assert.equal(Number(cleanedOldDatabaseSnapshots?.total ?? 0), 0, '显式 retention 入口仍应删除过期表监控快照')

  const firstSampledAt = '2026-01-01T00:10:00.000Z'
  tableMonitorRepository.collectTableStorageSnapshot(firstSampledAt, {
    tableScanMode: 'cursor',
    maxTablesPerDatabase: pairBudget
  })
  const firstPairs = pairSnapshots(firstSampledAt)
  assert.equal(firstPairs.length, pairBudget, '256 shard 场景单轮 Codex table snapshot 数必须受 pair budget 约束')
  assert.equal(new Set(firstPairs.map((row) => row.table_name)).size, pairBudget, 'pair 快照物理表名必须唯一')
  assert.deepEqual(
    [...new Set(firstPairs.map((row) => row.parent_table_name))].sort(),
    ['pair_budget_secondary', 'pair_budget_target'],
    '同一 shard 的多张表必须分别消耗 pair budget，并保留真实逻辑表名'
  )
  assert(
    new Set(firstPairs.map((row) => row.table_name.slice(0, row.table_name.indexOf(':')))).size < pairBudget,
    '预算单位必须是 shard-table pair，不能退化为每个 shard 一个单位'
  )
  assert(firstPairs.every((row) => row.table_kind === 'shard_table' && Number(row.is_partition) === 1), 'pair 快照必须明确标记 shard table 语义')
  assert(firstPairs.every((row) => Number(row.row_count) === 1), 'pair 快照应使用 dbstat 叶子页估算出各 shard 行数')

  const secondSampledAt = '2026-01-01T00:20:00.000Z'
  tableMonitorRepository.collectTableStorageSnapshot(secondSampledAt, {
    tableScanMode: 'cursor',
    maxTablesPerDatabase: pairBudget
  })
  const secondPairs = pairSnapshots(secondSampledAt)
  assert.equal(secondPairs.length, pairBudget, '第二轮仍必须遵守 pair budget')
  assert(secondPairs.every((row) => !firstPairs.some((first) => first.table_name === row.table_name)), 'pair cursor 必须推进到下一批 shard-table pair')
  assert.equal(exactTableCounts, 0, '常驻表监控不得对 shard table 执行精确 COUNT(*)')
  assert(shardCatalogQueries <= pairBudget * 2, `两轮 shard sqlite_schema 查询必须受 pair budget 约束，实际 ${shardCatalogQueries}`)
  assert(catalogTouchedShards.size <= pairBudget * 2, `两轮不得触碰全部 256 个 shard catalog，实际 ${catalogTouchedShards.size}`)
  assert.equal(shardPragmaQueries, 0, 'cursor/none 模式不得为数据库级汇总对全部 shard 执行 PRAGMA')
  assert(dbstatQueries <= pairBudget * 2, `两轮 dbstat 查询数不得超过 pair budget 上界，实际 ${dbstatQueries}`)
  assert.equal(readdirSync(codexShardRoot).filter((name) => name.endsWith('.sqlite3')).length, shardCount, '监控不得额外创建配置外 shard')

  const cursor = databaseModule.getStatsDatabase().prepare(`
    SELECT cursor_id
    FROM stats_job_state
    WHERE scope_type = 'table_monitor'
      AND scope_id = 'codex-context-state'
      AND job_name = 'table_storage_shard_pairs'
  `).get() as { cursor_id?: string } | undefined
  assert(cursor?.cursor_id, 'Codex pair 扫描必须使用独立持久 cursor')

  console.log('表监控 work budget 回归通过：256 shard catalog/pair 有界轮转、无全量 PRAGMA/精确 COUNT，采样与 retention 职责分离')
} finally {
  for (const restore of restores) restore()
  databaseModule.closeStorageDatabases()
  if (previousStrictBoundary === undefined) {
    delete process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT
  } else {
    process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = previousStrictBoundary
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function pairSnapshots(sampledAt: string): Array<{
  table_name: string
  parent_table_name: string | null
  table_kind: string | null
  is_partition: number
  row_count: number | null
}> {
  return databaseModule.getStatsDatabase().prepare(`
    SELECT table_name, parent_table_name, table_kind, is_partition, row_count
    FROM table_storage_snapshots
    WHERE database_role = 'codex-context-state'
      AND sampled_at = ?
    ORDER BY table_name ASC
  `).all(sampledAt) as Array<{
    table_name: string
    parent_table_name: string | null
    table_kind: string | null
    is_partition: number
    row_count: number | null
  }>
}
