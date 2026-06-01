import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

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

  const capturedSamplingSql: string[] = []
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase)
  businessDatabase.prepare = ((sql: string) => {
    capturedSamplingSql.push(sql)
    return originalBusinessPrepare(sql)
  }) as typeof businessDatabase.prepare
  const result = (() => {
    try {
      return tableMonitorRepository.collectTableStorageSnapshot('2026-01-01T00:00:00.000Z')
    } finally {
      businessDatabase.prepare = originalBusinessPrepare as typeof businessDatabase.prepare
    }
  })()
  assert.equal(result.tableScanMode, 'cursor', '表监控默认采样应使用 cursor，避免误触发全库表扫描')
  assert(result.tableSnapshots > 0, '表监控默认采样应写入本轮 cursor 表快照')
  assert(result.tableSnapshots < 16, `默认 cursor 采样不应一次采完业务库、数据集目录库、usage shard 和统计结果库所有表，实际 ${result.tableSnapshots}`)
  assert(capturedSamplingSql.every((sql) => !/SELECT\s+COUNT\s*\(\s*\*\s*\)\s+AS\s+count\s+FROM/i.test(sql)), '表监控行数采样不应回退为精确 COUNT(*) 扫表')

  const sampledRows = databaseModule.getStatsDatabase()
    .prepare('SELECT row_count FROM table_storage_snapshots')
    .all() as Array<{ row_count?: number | null }>
  assert(sampledRows.length === result.tableSnapshots, '采样结果数量应与表快照记录数一致')
  assert(sampledRows.some((row) => typeof row.row_count === 'number'), '默认采样应能为 dbstat 可观测表写入滚动行数')
  const databaseSnapshot = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT file_bytes, wal_bytes, shm_bytes, page_size, page_count
      FROM database_storage_snapshots
      WHERE database_role = 'business'
      ORDER BY sampled_at DESC
      LIMIT 1
    `)
    .get() as { file_bytes?: number | null; wal_bytes?: number | null; shm_bytes?: number | null; page_size?: number | null; page_count?: number | null } | undefined
  assert(databaseSnapshot, '表监控应写入业务库文件级快照')
  assert.equal(databaseSnapshot?.file_bytes, Number(databaseSnapshot?.page_size ?? 0) * Number(databaseSnapshot?.page_count ?? 0), '主库大小应来自 SQLite 页数估算，避免同步 stat 文件')
  assert.equal(databaseSnapshot?.wal_bytes, null, 'WAL 大小不应在表监控采样中同步 stat 文件')
  assert.equal(databaseSnapshot?.shm_bytes, null, 'SHM 大小不应在表监控采样中同步 stat 文件')
  const jobState = databaseModule.getStatsDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'table_monitor' AND scope_id = 'business' AND job_name = 'table_storage_snapshots'")
    .get() as { lag_seconds?: number | null } | undefined
  assert.equal(jobState?.lag_seconds, null, '表监控游标状态不应伪装成 0')

  const statsDatabase = databaseModule.getStatsDatabase()
  const capturedSql: string[] = []
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase)
  statsDatabase.prepare = ((sql: string) => {
    capturedSql.push(sql)
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare
  try {
    tableMonitorRepository.getTableStorageOverview({
      startAt: '2026-01-01T00:00:00.000Z',
      endAt: '2026-01-01T00:00:00.000Z',
      limit: 200
    })
  } finally {
    statsDatabase.prepare = originalPrepare as typeof statsDatabase.prepare
  }
  assert(capturedSql.length > 0, '表监控概览应查询统计结果库采样表')
  assert(capturedSql.every((sql) => !/\bJOIN\b/i.test(sql)), '表监控概览不应使用关联查询拼接采样结果')
  const overviewDatabaseSql = capturedSql.find((sql) => sql.includes('FROM database_storage_snapshots'))
  const overviewTableSql = capturedSql.find((sql) => sql.includes('FROM table_storage_snapshots') && sql.includes('ROW_NUMBER() OVER'))
  assert(overviewDatabaseSql, '表监控概览应按库角色读取最新数据库快照')
  assert(overviewTableSql, '表监控概览应读取每张表的最新采样')
  const overviewDatabasePlan = explainQueryPlan(statsDatabase, overviewDatabaseSql, ['business', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'])
  assertNoTempBtree(overviewDatabasePlan, '表监控概览数据库快照查询')
  assert(overviewDatabasePlan.includes('idx_database_storage_snapshots_role_time_id'), `表监控概览数据库快照应使用 role+time+id 索引，实际计划：${overviewDatabasePlan}`)
  const overviewTablePlan = explainQueryPlan(statsDatabase, overviewTableSql, ['2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'])
  assertNoTempBtree(overviewTablePlan, '表监控概览表快照查询')
  assert(overviewTablePlan.includes('idx_table_storage_snapshots_latest_id'), `表监控概览表快照应使用 latest+id 索引，实际计划：${overviewTablePlan}`)

  const capturedHistorySql: string[] = []
  statsDatabase.prepare = ((sql: string) => {
    capturedHistorySql.push(sql)
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare
  let databaseHistory: ReturnType<typeof tableMonitorRepository.listDatabaseStorageHistory>
  try {
    databaseHistory = tableMonitorRepository.listDatabaseStorageHistory({
      startAt: '2026-01-01T00:00:00.000Z',
      endAt: '2026-01-01T00:00:00.000Z',
      limit: 720
    })
  } finally {
    statsDatabase.prepare = originalPrepare as typeof statsDatabase.prepare
  }
  const databaseHistoryRoles = new Set(databaseHistory.map((row) => row.databaseRole))
  assert.equal(databaseHistory.length, 3, '三库增长趋势应一次返回业务库、数据集目录库和统计结果库历史点')
  assert(databaseHistoryRoles.has('business'), '三库增长趋势应包含业务库')
  assert(databaseHistoryRoles.has('dataset'), '三库增长趋势应包含数据集目录库')
  assert(databaseHistoryRoles.has('stats'), '三库增长趋势应包含统计结果库')
  const databaseHistorySql = capturedHistorySql.find((sql) => sql.includes('FROM database_storage_snapshots'))
  assert(databaseHistorySql, '三库增长趋势应按库角色读取历史快照')
  const databaseHistoryPlan = explainQueryPlan(statsDatabase, databaseHistorySql, ['business', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', 720])
  assertNoTempBtree(databaseHistoryPlan, '三库增长趋势查询')
  assert(databaseHistoryPlan.includes('idx_database_storage_snapshots_role_time_id'), `三库增长趋势应使用 role+time+id 索引，实际计划：${databaseHistoryPlan}`)

  console.log('表监控默认采样回归通过：默认 cursor，滚动写入行数且不做全表扫描和精确 COUNT(*)')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function explainQueryPlan(database: DatabaseSync, sql: string, params: SQLInputValue[]): string {
  const rows = database
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').filter(Boolean).join('\n')
}

function assertNoTempBtree(details: string, label: string): void {
  assert(!/USE TEMP B-TREE/i.test(details), `${label}不应创建临时排序树，实际计划：${details}`)
}
