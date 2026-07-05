import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  collectTableStorageSnapshotAsync,
  getTableStorageOverviewAsync,
  listDatabaseStorageHistoryAsync,
  listTableStorageHistoryAsync
} from '../../storage/table-monitor.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '表监控 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `table_monitor_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const startAt = '2099-01-01T00:00:00.000Z'
const middleAt = '2099-01-01T00:05:00.000Z'
const endAt = '2099-01-01T00:10:00.000Z'
const targetTableName = `tm_pg_smoke_target_${marker}`
const otherTableName = `tm_pg_smoke_other_${marker}`
const sampledTableName = `tm_collect_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const sampledAt = '2099-01-01T00:20:00.000Z'
const databaseSnapshotIds = [
  `db_${marker}_business_old`,
  `db_${marker}_business_new`,
  `db_${marker}_stats_new`
]
const tableSnapshotIds = [
  `tbl_${marker}_target_old`,
  `tbl_${marker}_target_new`,
  `tbl_${marker}_other_new`
]
const pool = await getPostgresPool()

try {
  await createSampledPostgresTable()
  await insertSmokeRows()

  const overview = await getTableStorageOverviewAsync({ startAt, endAt, limit: 10 })
  const businessDatabase = overview.databases.find((row) => row.databaseRole === 'business')
  assert(businessDatabase, 'PG 表监控 overview 应返回 business 数据库快照')
  assert.equal(businessDatabase.sampledAt, middleAt, 'overview 应读取 business 最新数据库快照')
  assert.equal(businessDatabase.fileBytes, 4096, 'overview 应映射数据库 file_bytes')
  assert.equal(businessDatabase.tableCount, 12, 'overview 应映射数据库 table_count')

  const targetOverview = overview.tables.find((row) => row.databaseRole === 'business' && row.tableName === targetTableName)
  assert(targetOverview, 'PG 表监控 overview 应返回目标表最新快照')
  assert.equal(targetOverview.sampledAt, middleAt, 'overview 应读取目标表最新采样')
  assert.equal(targetOverview.rowCount, 123, 'overview 应映射 row_count')
  assert.equal(targetOverview.totalBytes, 3072, 'overview 应映射 total_bytes')
  assert.equal(targetOverview.growthRows1h, 23, 'overview 应映射 1h 行增长')
  const smokeTableOrder = overview.tables
    .filter((row) => row.tableName === targetTableName || row.tableName === otherTableName)
    .map((row) => `${row.databaseRole}:${row.tableName}`)
  assert.deepEqual(
    smokeTableOrder,
    [`business:${targetTableName}`, `stats:${otherTableName}`],
    'PG 表监控 overview 表列表应按总大小倒序排序，不应因 bigint 字符串退化为表名排序'
  )

  const targetHistory = await listTableStorageHistoryAsync({
    databaseRole: 'business',
    tableName: targetTableName,
    startAt,
    endAt,
    limit: 10
  })
  assert.deepEqual(targetHistory.map((row) => row.sampledAt), [startAt, middleAt], 'PG 表历史应按时间正序返回')
  assert.deepEqual(targetHistory.map((row) => row.rowCount), [100, 123], 'PG 表历史应保留各采样 row_count')

  const databaseHistory = await listDatabaseStorageHistoryAsync({ startAt, endAt, limit: 10 })
  const smokeDatabaseHistory = databaseHistory.filter((row) => row.databasePath.includes(marker))
  assert.deepEqual(
    smokeDatabaseHistory.map((row) => `${row.databaseRole}:${row.sampledAt}`),
    [`business:${startAt}`, `business:${middleAt}`, `stats:${middleAt}`],
    'PG 数据库历史应按时间正序和数据库角色排序'
  )

  await assertTableMonitorExplainPlans()
  await assertPostgresCollectorWritesSnapshots()

  console.log(JSON.stringify({
    message: '表监控 PG smoke 通过',
    databases: overview.databases.length,
    tables: overview.tables.length,
    tableHistory: targetHistory.length,
    databaseHistory: smokeDatabaseHistory.length,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function insertSmokeRows(): Promise<void> {
  await pool.query(`
    INSERT INTO juhe_stats.database_storage_snapshots (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes,
      table_count, index_count, created_at
    ) VALUES
      ($1, 'business', $2, $3, 2048, 128, 64, 4096, 10, 1, 1900, 148, 10, 20, $3),
      ($4, 'business', $2, $5, 4096, 256, 64, 4096, 20, 2, 3900, 196, 12, 24, $5),
      ($6, 'stats', $7, $5, 8192, 512, 64, 4096, 30, 3, 7900, 292, 14, 28, $5)
  `, [
    databaseSnapshotIds[0],
    `pg-smoke-business-${marker}`,
    startAt,
    databaseSnapshotIds[1],
    middleAt,
    databaseSnapshotIds[2],
    `pg-smoke-stats-${marker}`
  ])

  await pool.query(`
    INSERT INTO juhe_stats.table_storage_snapshots (
      id, database_role, table_name, sampled_at, row_count, table_bytes, index_bytes,
      total_bytes, page_count, index_count, growth_bytes_1h, growth_rows_1h,
      growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES
      ($1, 'business', $2, $3, 100, 1024, 512, 1536, 10, 2, 0, 0, 1000, 10, $3),
      ($4, 'business', $2, $5, 123, 2048, 1024, 3072, 20, 3, 1536, 23, 2000, 40, $5),
      ($6, 'stats', $7, $5, 7, 512, 256, 768, 5, 1, 128, 2, 256, 4, $5)
  `, [
    tableSnapshotIds[0],
    targetTableName,
    startAt,
    tableSnapshotIds[1],
    middleAt,
    tableSnapshotIds[2],
    otherTableName
  ])
}

async function createSampledPostgresTable(): Promise<void> {
  const tableIdentifier = quoteIdentifier(sampledTableName)
  await pool.query(`
    CREATE TABLE IF NOT EXISTS juhe_dataset.${tableIdentifier} (
      id text PRIMARY KEY,
      value text NOT NULL,
      created_at text NOT NULL
    )
  `)
  await pool.query(`CREATE INDEX IF NOT EXISTS ${quoteIdentifier(`${sampledTableName}_value_idx`)} ON juhe_dataset.${tableIdentifier}(value)`)
  await pool.query(`
    INSERT INTO juhe_dataset.${tableIdentifier} (id, value, created_at)
    VALUES ($1, 'alpha', $3), ($2, 'beta', $3)
    ON CONFLICT(id) DO UPDATE SET value = excluded.value, created_at = excluded.created_at
  `, [`${marker}-1`, `${marker}-2`, startAt])
  await pool.query(`ANALYZE juhe_dataset.${tableIdentifier}`)
}

async function assertPostgresCollectorWritesSnapshots(): Promise<void> {
  await pool.query(`
    DELETE FROM juhe_stats.table_storage_snapshots
    WHERE database_role = 'dataset' AND table_name = $1
  `, [sampledTableName])
  await pool.query(`
    DELETE FROM juhe_stats.stats_job_state
    WHERE scope_type = 'table_monitor' AND scope_id = 'dataset' AND job_name = 'table_storage_snapshots'
  `)
  const result = await collectTableStorageSnapshotAsync(sampledAt, {
    tableScanMode: 'full',
    maxTablesPerDatabase: 100
  })
  assert.equal(result.databaseSnapshots, 5, 'PG 采样应只写入 PostgreSQL 五个逻辑 schema 快照')
  assert(result.tableSnapshots >= 1, 'PG 采样应写入表级快照')

  const history = await listTableStorageHistoryAsync({
    databaseRole: 'dataset',
    tableName: sampledTableName,
    startAt: sampledAt,
    endAt: sampledAt,
    limit: 5
  })
  assert.equal(history.length, 1, 'PG 采样应写入临时表历史快照')
  assert.equal(history[0]?.sampledAt, sampledAt, 'PG 表级快照应使用采样时间')
  assert((history[0]?.rowCount ?? 0) >= 2, 'PG 表级快照应读取 pg_stat 行数估算')
  assert((history[0]?.totalBytes ?? 0) > 0, 'PG 表级快照应记录 pg_total_relation_size')
  assert((history[0]?.indexCount ?? 0) >= 1, 'PG 表级快照应记录索引数量')

  const databaseHistory = await listDatabaseStorageHistoryAsync({
    startAt: sampledAt,
    endAt: sampledAt,
    limit: 10
  })
  const datasetSnapshot = databaseHistory.find((row) => row.databaseRole === 'dataset' && row.sampledAt === sampledAt)
  assert(datasetSnapshot, 'PG 采样应写入 dataset schema 数据库快照')
  assert.equal(datasetSnapshot?.databasePath, 'postgres:juhe_dataset', 'PG 采样不应暴露 SQLite 文件路径')
  assert((datasetSnapshot?.fileBytes ?? 0) > 0, 'PG schema 快照应记录 schema 总大小')
}

async function assertTableMonitorExplainPlans(): Promise<void> {
  await assertIndexedPlan(
    '表监控 overview 数据库快照 PG 查询',
    `
      SELECT database_role
      FROM juhe_stats.database_storage_snapshots
      WHERE database_role = $1
        AND sampled_at >= $2
        AND sampled_at <= $3
      ORDER BY sampled_at DESC, id DESC
      LIMIT 1
    `,
    ['business', startAt, endAt],
    ['idx_database_storage_snapshots_role_time_id']
  )
  await assertIndexedPlan(
    '表监控 overview 表快照 PG 查询',
    `
      SELECT table_name
      FROM (
        SELECT
          table_name,
          ROW_NUMBER() OVER (
            PARTITION BY database_role, table_name
            ORDER BY sampled_at DESC, id DESC
          ) AS rank
        FROM juhe_stats.table_storage_snapshots
        WHERE sampled_at >= $1
          AND sampled_at <= $2
      ) ranked
      WHERE ranked.rank = 1
    `,
    [startAt, endAt],
    ['idx_table_storage_snapshots_time', 'idx_table_storage_snapshots_latest_id']
  )
  await assertIndexedPlan(
    '表监控单表历史 PG 查询',
    `
      SELECT table_name
      FROM juhe_stats.table_storage_snapshots
      WHERE database_role = $1
        AND table_name = $2
        AND sampled_at >= $3
        AND sampled_at <= $4
      ORDER BY sampled_at DESC
      LIMIT 10
    `,
    ['business', targetTableName, startAt, endAt],
    ['idx_table_storage_snapshots_latest', 'idx_table_storage_snapshots_latest_id', 'idx_table_storage_snapshots_time', 'table_storage_snapshots_database_role_table_name_sampled_at_key']
  )
  await assertIndexedPlan(
    '表监控数据库历史 PG 查询',
    `
      SELECT database_role
      FROM juhe_stats.database_storage_snapshots
      WHERE database_role = $1
        AND sampled_at >= $2
        AND sampled_at <= $3
      ORDER BY sampled_at DESC, id DESC
      LIMIT 10
    `,
    ['business', startAt, endAt],
    ['idx_database_storage_snapshots_role_time_id']
  )
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  await pool.query(`DROP TABLE IF EXISTS juhe_dataset.${quoteIdentifier(sampledTableName)}`)
  await pool.query('DELETE FROM juhe_stats.table_storage_snapshots WHERE database_role = $1 AND table_name = $2', ['dataset', sampledTableName])
  await pool.query('DELETE FROM juhe_stats.database_storage_snapshots WHERE sampled_at = $1 AND database_path LIKE $2', [sampledAt, 'postgres:%'])
  await pool.query("DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'table_monitor' AND scope_id = 'dataset' AND job_name = 'table_storage_snapshots'")
  await pool.query('DELETE FROM juhe_stats.table_storage_snapshots WHERE id = ANY($1::text[])', [tableSnapshotIds])
  await pool.query('DELETE FROM juhe_stats.database_storage_snapshots WHERE id = ANY($1::text[])', [databaseSnapshotIds])
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
