import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-dbstat-unavailable-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'table-monitor-dbstat-unavailable-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, tableMonitorRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/table-monitor.repository.js')
])

try {
  const businessDatabase = databaseModule.getBusinessDatabase()
  businessDatabase.prepare('CREATE TABLE table_monitor_dbstat_unavailable_target (id TEXT PRIMARY KEY, value TEXT)').run()
  businessDatabase.prepare('INSERT INTO table_monitor_dbstat_unavailable_target (id, value) VALUES (?, ?)').run('row-1', 'value-1')

  const originalPrepare = businessDatabase.prepare.bind(businessDatabase)
  businessDatabase.prepare = ((sql: string) => {
    if (sql.includes('FROM dbstat')) {
      throw new Error('forced dbstat unavailable')
    }
    return originalPrepare(sql)
  }) as typeof businessDatabase.prepare

  const sampledAt = '2026-01-01T00:00:00.000Z'
  const result = tableMonitorRepository.collectTableStorageSnapshot(sampledAt, {
    tableScanMode: 'full',
    rowCountMode: 'none'
  })
  assert(result.tableSnapshots > 0, '表监控采样应写入本轮快照')

  const databaseRows = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT table_name, table_bytes, index_bytes, total_bytes, page_count
      FROM table_storage_snapshots
      WHERE database_role = 'business' AND table_name = ?
      ORDER BY sampled_at DESC
      LIMIT 1
    `)
    .all('table_monitor_dbstat_unavailable_target') as Array<{
    table_name?: string
    table_bytes?: number | null
    index_bytes?: number | null
    total_bytes?: number | null
    page_count?: number | null
  }>

  assert.equal(databaseRows.length, 1, '应能查到业务库目标表的最新快照')
  const tableRow = databaseRows[0]
  assert.equal(tableRow?.table_name, 'table_monitor_dbstat_unavailable_target')
  assert.equal(tableRow?.table_bytes, null, 'dbstat 不可用时表大小应保持未知，而不是压成 0')
  assert.equal(tableRow?.index_bytes, null, 'dbstat 不可用时索引大小应保持未知，而不是压成 0')
  assert.equal(tableRow?.total_bytes, null, 'dbstat 不可用时总大小应保持未知，而不是压成 0')
  assert.equal(tableRow?.page_count, null, 'dbstat 不可用时页数应保持未知，而不是压成 0')

  const overview = tableMonitorRepository.getTableStorageOverview({ startAt: sampledAt, endAt: sampledAt, limit: 200 })
  const overviewRow = overview.tables.find((row) => row.databaseRole === 'business' && row.tableName === 'table_monitor_dbstat_unavailable_target')
  assert(overviewRow, '概览应返回目标表')
  assert.equal(overviewRow?.tableBytes, undefined, '概览映射应保留不可用状态')
  assert.equal(overviewRow?.totalBytes, undefined, '概览映射应保留不可用状态')

  console.log('表监控 dbstat 不可用回归通过：不再把未知大小压成 0')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
