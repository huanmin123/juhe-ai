import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-table-monitor-default-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
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
  assertTableMonitorAsyncSourceGuard()

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
  assert(result.tableSnapshots < 20, `默认 cursor 采样不应一次采完业务库、数据集目录库、使用记录目录库、usage shard 和统计结果库所有表，实际 ${result.tableSnapshots}`)
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
  let latestOverview: ReturnType<typeof tableMonitorRepository.getTableStorageOverview>
  try {
    latestOverview = tableMonitorRepository.getTableStorageOverview({
      limit: 200
    })
  } finally {
    statsDatabase.prepare = originalPrepare as typeof statsDatabase.prepare
  }
  assert(capturedSql.length > 0, '表监控概览应查询统计结果库采样表')
  assert.deepEqual(
    Object.keys(latestOverview.databases[0] ?? {}).sort(),
    ['databasePath', 'databaseRole', 'fileBytes', 'freeBytes', 'sampledAt', 'shmBytes', 'tableCount', 'walBytes'].sort(),
    '表监控概览数据库卡片不应返回页、索引等未展示字段'
  )
  assert.deepEqual(
    Object.keys(latestOverview.tables[0] ?? {}).sort(),
    [
      'databaseRole', 'growthBytes1h', 'growthBytes24h', 'growthRows1h', 'growthRows24h',
      'indexBytes', 'indexToTableRatio', 'isArchive', 'isPartition', 'parentTableName',
      'rowCount', 'sampledAt', 'tableBytes', 'tableKind', 'tableName', 'totalBytes'
    ].sort(),
    '表监控概览表列表不应返回页、索引计数等未展示字段'
  )
  assert(capturedSql.every((sql) => !/\bJOIN\b/i.test(sql)), '表监控概览不应使用关联查询拼接采样结果')
  const overviewDatabaseSql = capturedSql.find((sql) => sql.includes('FROM database_storage_snapshots'))
  const overviewTableSampleSql = capturedSql.find((sql) => sql.includes('FROM table_storage_snapshots') && sql.includes('SELECT sampled_at'))
  const overviewTableSql = capturedSql.find((sql) => sql.includes('FROM table_storage_snapshots') && sql.includes('sampled_at = ?'))
  assert(overviewDatabaseSql, '表监控概览应按库角色读取最新数据库快照')
  assert(overviewTableSampleSql, '表监控概览应先读取每个库角色的最新表采样时间')
  assert(overviewTableSql, '表监控概览应只读取最新采样批次')
  const overviewDatabasePlan = explainQueryPlan(statsDatabase, overviewDatabaseSql, ['business'])
  assertNoTempBtree(overviewDatabasePlan, '表监控概览数据库快照查询')
  assert(overviewDatabasePlan.includes('idx_database_storage_snapshots_role_time_id'), `表监控概览数据库快照应使用 role+time+id 索引，实际计划：${overviewDatabasePlan}`)
  const overviewTableSamplePlan = explainQueryPlan(statsDatabase, overviewTableSampleSql, ['business'])
  assert(overviewTableSamplePlan.includes('idx_table_storage_snapshots_time'), `表监控概览最新批次查询应使用 time 索引，实际计划：${overviewTableSamplePlan}`)
  const asyncOverview = await tableMonitorRepository.getTableStorageOverviewAsync({
    limit: 200
  })
  assert.deepEqual(asyncOverview, tableMonitorRepository.getTableStorageOverview({
    limit: 200
  }), 'SQLite 模式下表监控概览 async 入口应回退同步读取并保持结果一致')

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
  assert.equal(databaseHistory.length, 5, '数据库增长趋势应一次返回业务库、数据集目录库、使用记录目录库、统计结果库和 codex context state 历史点')
  assert(databaseHistoryRoles.has('business'), '四库增长趋势应包含业务库')
  assert(databaseHistoryRoles.has('dataset'), '四库增长趋势应包含数据集目录库')
  assert(databaseHistoryRoles.has('usage-catalog'), '四库增长趋势应包含使用记录目录库')
  assert(databaseHistoryRoles.has('stats'), '四库增长趋势应包含统计结果库')
  assert(databaseHistoryRoles.has('codex-context-state'), '数据库增长趋势应包含 codex context state')
  const databaseHistorySql = capturedHistorySql.find((sql) => sql.includes('FROM database_storage_snapshots'))
  assert(databaseHistorySql, '四库增长趋势应按库角色读取历史快照')
  const databaseHistoryPlan = explainQueryPlan(statsDatabase, databaseHistorySql, ['business', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', 720])
  assertNoTempBtree(databaseHistoryPlan, '四库增长趋势查询')
  assert(databaseHistoryPlan.includes('idx_database_storage_snapshots_role_time_id'), `四库增长趋势应使用 role+time+id 索引，实际计划：${databaseHistoryPlan}`)
  assert.deepEqual(await tableMonitorRepository.listDatabaseStorageHistoryAsync({
    startAt: '2026-01-01T00:00:00.000Z',
    endAt: '2026-01-01T00:00:00.000Z',
    limit: 720
  }), databaseHistory, 'SQLite 模式下数据库增长趋势 async 入口应回退同步读取并保持结果一致')

  const sampledTable = statsDatabase.prepare(`
    SELECT database_role AS databaseRole, table_name AS tableName
    FROM table_storage_snapshots
    WHERE database_role = 'business'
    ORDER BY sampled_at DESC, table_name ASC
    LIMIT 1
  `).get() as { databaseRole?: 'business'; tableName?: string } | undefined
  assert(sampledTable?.databaseRole && sampledTable.tableName, '测试应存在业务库表快照')
  const tableHistory = tableMonitorRepository.listTableStorageHistory({
    databaseRole: sampledTable.databaseRole,
    tableName: sampledTable.tableName,
    startAt: '2026-01-01T00:00:00.000Z',
    endAt: '2026-01-01T00:00:00.000Z',
    limit: 720
  })
  assert.deepEqual(await tableMonitorRepository.listTableStorageHistoryAsync({
    databaseRole: sampledTable.databaseRole,
    tableName: sampledTable.tableName,
    startAt: '2026-01-01T00:00:00.000Z',
    endAt: '2026-01-01T00:00:00.000Z',
    limit: 720
  }), tableHistory, 'SQLite 模式下单表增长趋势 async 入口应回退同步读取并保持结果一致')

  console.log('表监控默认采样回归通过：默认 cursor，滚动写入行数，不做全表扫描，并固定管理端 async 读取入口')
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

function assertTableMonitorAsyncSourceGuard(): void {
  const repositorySource = readSource('../../storage/table-monitor.repository.ts')
  const routesSource = readSource('../../modules/table-monitor/table-monitor.routes.ts')
  for (const name of ['getTableStorageOverviewAsync', 'listTableStorageHistoryAsync', 'listDatabaseStorageHistoryAsync']) {
    assert(repositorySource.includes(`export async function ${name}`), `表监控仓储应导出 ${name}`)
    assert(routesSource.includes(name), `表监控路由应调用 ${name}`)
  }
  assert(repositorySource.includes('createPostgresDatabaseClient(await getPostgresPool())'), '表监控 async 读路径应使用 PostgreSQL client')
  assert(repositorySource.includes("statsTable(client, 'database_storage_snapshots')"), '表监控数据库快照 PG 读路径应使用 juhe_stats schema 表名')
  assert(repositorySource.includes("statsTable(client, 'table_storage_snapshots')"), '表监控表快照 PG 读路径应使用 juhe_stats schema 表名')
  assert(repositorySource.includes('tableStorageOverviewCacheTtlMs'), '表监控 overview async 入口应有短 TTL 缓存，避免管理端重复刷新反复扫描快照')
  assert(repositorySource.includes('getCachedTableStorageOverview(input)'), '表监控 overview async 入口应先读缓存')
  assert(repositorySource.includes('SELECT MAX(sampled_at)'), '表监控 PG overview 应先定位最新采样批次，避免扫描整段历史窗口')
  assert(routesSource.includes('await getTableStorageOverviewAsync('), '表监控 overview 路由必须 await async 读入口')
  assert(routesSource.includes('await listTableStorageHistoryAsync('), '表监控 history 路由必须 await async 读入口')
  assert(routesSource.includes('await listDatabaseStorageHistoryAsync('), '表监控 database-history 路由必须 await async 读入口')
  assert(!/import \{[^}]*\bgetTableStorageOverview\b/.test(routesSource), '表监控路由不应重新导入同步 overview 入口')
  assert(!/import \{[^}]*\blistTableStorageHistory\b/.test(routesSource), '表监控路由不应重新导入同步 table history 入口')
  assert(!/import \{[^}]*\blistDatabaseStorageHistory\b/.test(routesSource), '表监控路由不应重新导入同步 database history 入口')
}

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}
