import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-default-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'table-monitor-default-secret'
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
  for (let index = 0; index < 8; index += 1) {
    businessDatabase.prepare(`CREATE TABLE table_monitor_default_${index} (id TEXT PRIMARY KEY, value TEXT)`).run()
    businessDatabase.prepare(`INSERT INTO table_monitor_default_${index} (id, value) VALUES (?, ?)`).run(`id-${index}`, `value-${index}`)
  }

  const result = tableMonitorRepository.collectTableStorageSnapshot('2026-01-01T00:00:00.000Z')
  assert.equal(result.tableScanMode, 'cursor', '表监控默认采样应使用 cursor，避免误触发全库表扫描')
  assert.equal(result.rowCountMode, 'none', '表监控默认采样不应执行 COUNT(*) 行数统计')
  assert(result.tableSnapshots > 0, '表监控默认采样应写入本轮 cursor 表快照')
  assert(result.tableSnapshots < 16, `默认 cursor 采样不应一次采完业务库和记录库所有表，实际 ${result.tableSnapshots}`)

  const sampledRows = databaseModule.getStatsDatabase()
    .prepare('SELECT row_count FROM table_storage_snapshots')
    .all() as Array<{ row_count?: number | null }>
  assert(sampledRows.length === result.tableSnapshots, '采样结果数量应与表快照记录数一致')
  assert(sampledRows.every((row) => row.row_count === null || row.row_count === undefined), '默认采样不应写入行数，避免 COUNT(*) 扫描大表')
  const jobState = databaseModule.getStatsDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'table_monitor' AND scope_id = 'business' AND job_name = 'table_storage_snapshots'")
    .get() as { lag_seconds?: number | null } | undefined
  assert.equal(jobState?.lag_seconds, null, '表监控游标状态不应伪装成 0')

  const recordDatabase = databaseModule.getStatsDatabase()
  const capturedSql: string[] = []
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase)
  recordDatabase.prepare = ((sql: string) => {
    capturedSql.push(sql)
    return originalPrepare(sql)
  }) as typeof recordDatabase.prepare
  try {
    tableMonitorRepository.getTableStorageOverview({
      startAt: '2026-01-01T00:00:00.000Z',
      endAt: '2026-01-01T00:00:00.000Z',
      limit: 200
    })
  } finally {
    recordDatabase.prepare = originalPrepare as typeof recordDatabase.prepare
  }
  assert(capturedSql.length > 0, '表监控概览应查询记录库采样表')
  assert(capturedSql.every((sql) => !/\bJOIN\b/i.test(sql)), '表监控概览不应使用关联查询拼接采样结果')

  console.log('表监控默认采样回归通过：默认 cursor/none，不做全表扫描和 COUNT(*)')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
