import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../../../config/runtime.js'
import {
  codexContextStateShardIndexes,
  codexContextStateShardRootPath,
  datasetDatabasePath,
  getBusinessDatabase,
  getCodexContextStateShardDatabase,
  getDatasetDatabase,
  getStatsDatabase,
  getUsageCatalogDatabase,
  statsDatabasePath,
  usageCatalogDatabasePath
} from '../../../../storage/database.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  type CreatedMockdata,
  type MockdataOptions
} from '../shared.js'

type StorageRole = 'business' | 'dataset' | 'usage-catalog' | 'stats' | 'codex-context-state'

interface TableStorageSeed {
  tableName: string
  latestRowCount: number
  latestTotalBytes: number
  indexCount: number
  dailyRowGrowth: number
  dailyByteGrowth: number
}

interface DatabaseStorageSeed {
  role: StorageRole
  path: string
  pageSize: number
  pageCount: number
  freelistCount: number
  latestFileBytes: number
  dailyFileGrowth: number
  tableCount: number
  indexCount: number
  tables: TableStorageSeed[]
}

interface SqliteTargetInput {
  role: Exclude<StorageRole, 'codex-context-state'>
  path: string
  database: DatabaseSync
  displayFloorBytes: number
  dailyFileGrowth: number
  dailyRowGrowth: number
  rowCountOverrides?: Map<string, number>
}

export function createStorageMockdata(_created: CreatedMockdata, options: MockdataOptions): void {
  const statsDatabase = getStatsDatabase()
  const now = Date.now() - 10 * minuteMs
  const businessTarget = createSqliteTarget({
    role: 'business',
    path: runtimeConfig.databasePath,
    database: getBusinessDatabase(),
    displayFloorBytes: 110_000_000,
    dailyFileGrowth: 1_200_000,
    dailyRowGrowth: 2
  })
  const datasetTarget = createSqliteTarget({
    role: 'dataset',
    path: datasetDatabasePath(),
    database: getDatasetDatabase(),
    displayFloorBytes: 410_000_000,
    dailyFileGrowth: 7_000_000,
    dailyRowGrowth: 12
  })
  const usageCatalogTarget = createSqliteTarget({
    role: 'usage-catalog',
    path: usageCatalogDatabasePath(),
    database: getUsageCatalogDatabase(),
    displayFloorBytes: 240_000_000,
    dailyFileGrowth: 4_500_000,
    dailyRowGrowth: 12
  })
  const codexContextTarget = createCodexContextStateTarget()
  const statsTableNames = listApplicationTables(statsDatabase)
  const projectedDatabaseSnapshotRows = options.days * 5
  const projectedTableSnapshotRows = options.days * (
    businessTarget.tableCount
    + datasetTarget.tableCount
    + usageCatalogTarget.tableCount
    + statsTableNames.length
    + codexContextTarget.tableCount
  )
  const statsRowCountOverrides = new Map<string, number>([
    ['database_storage_snapshots', tableRowCount(statsDatabase, 'database_storage_snapshots') + projectedDatabaseSnapshotRows],
    ['table_storage_snapshots', tableRowCount(statsDatabase, 'table_storage_snapshots') + projectedTableSnapshotRows]
  ])
  const statsTarget = createSqliteTarget({
    role: 'stats',
    path: statsDatabasePath(),
    database: statsDatabase,
    displayFloorBytes: 130_000_000,
    dailyFileGrowth: 1_500_000,
    dailyRowGrowth: 4,
    rowCountOverrides: statsRowCountOverrides
  })
  const targets = [
    businessTarget,
    datasetTarget,
    usageCatalogTarget,
    statsTarget,
    codexContextTarget
  ]
  const insertDatabase = statsDatabase.prepare(`
    INSERT INTO database_storage_snapshots (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertTable = statsDatabase.prepare(`
    INSERT INTO table_storage_snapshots (
      id, database_role, table_name, sampled_at, row_count, table_bytes, index_bytes, total_bytes,
      page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)

  statsDatabase.exec('BEGIN')
  try {
    for (let dayIndex = 0; dayIndex < options.days; dayIndex += 1) {
      const sampledAt = new Date(now - (options.days - dayIndex - 1) * dayMs).toISOString()
      for (const target of targets) {
        const fileBytes = historicalValue(target.latestFileBytes, target.dailyFileGrowth, dayIndex, options.days, 4096)
        const walBytes = Math.floor(fileBytes * 0.08)
        const pageCount = Math.max(1, Math.ceil(fileBytes / target.pageSize))
        const freelistCount = Math.min(pageCount, Math.max(0, target.freelistCount + options.days - dayIndex - 1))
        const freeBytes = target.pageSize * freelistCount
        const usedBytes = Math.max(0, fileBytes - freeBytes)
        insertDatabase.run(
          `${idPrefix}storage_db_${target.role}_${String(dayIndex + 1).padStart(2, '0')}`,
          target.role,
          target.path,
          sampledAt,
          fileBytes,
          walBytes,
          32768,
          target.pageSize,
          pageCount,
          freelistCount,
          usedBytes,
          freeBytes,
          target.tableCount,
          target.indexCount,
          sampledAt
        )
        for (const table of target.tables) {
          const rowCount = historicalRowCount(table.latestRowCount, table.dailyRowGrowth, dayIndex, options.days)
          const totalBytes = historicalValue(table.latestTotalBytes, table.dailyByteGrowth, dayIndex, options.days, 4096)
          const indexBytes = Math.floor(totalBytes * indexByteRatio(table.indexCount))
          const tableBytes = totalBytes - indexBytes
          insertTable.run(
            `${idPrefix}storage_table_${target.role}_${table.tableName}_${String(dayIndex + 1).padStart(2, '0')}`,
            target.role,
            table.tableName,
            sampledAt,
            rowCount,
            tableBytes,
            indexBytes,
            totalBytes,
            Math.max(1, Math.ceil(totalBytes / target.pageSize)),
            table.indexCount,
            Math.floor(table.dailyByteGrowth / 24),
            Math.max(0, Math.floor(table.dailyRowGrowth / 24)),
            dayIndex === 0 ? 0 : table.dailyByteGrowth,
            dayIndex === 0 ? 0 : table.dailyRowGrowth,
            sampledAt
          )
        }
      }
    }
    statsDatabase.exec('COMMIT')
  } catch (error) {
    statsDatabase.exec('ROLLBACK')
    throw error
  }
}

function createSqliteTarget(input: SqliteTargetInput): DatabaseStorageSeed {
  const tableNames = listApplicationTables(input.database)
  const indexesByTable = listIndexesByTable(input.database)
  const pageSize = pragmaNumber(input.database, 'page_size') ?? 4096
  const pageCount = pragmaNumber(input.database, 'page_count') ?? 1
  const freelistCount = pragmaNumber(input.database, 'freelist_count') ?? 0
  const actualFileBytes = pageSize * pageCount
  const latestFileBytes = Math.max(input.displayFloorBytes, actualFileBytes)
  const tables = tableNames.map((tableName) => {
    const latestRowCount = input.rowCountOverrides?.get(tableName) ?? tableRowCount(input.database, tableName)
    const indexCount = indexesByTable.get(tableName)?.length ?? 0
    return createTableSeed(tableName, latestRowCount, indexCount, input.dailyRowGrowth)
  })
  return {
    role: input.role,
    path: input.path,
    pageSize,
    pageCount,
    freelistCount,
    latestFileBytes,
    dailyFileGrowth: input.dailyFileGrowth,
    tableCount: tables.length,
    indexCount: countIndexes(indexesByTable),
    tables
  }
}

function createCodexContextStateTarget(): DatabaseStorageSeed {
  const shardIndexes = codexContextStateShardIndexes()
  const shardDatabases = shardIndexes.map((shardIndex) => getCodexContextStateShardDatabase(shardIndex))
  const firstShard = shardDatabases[0]
  const tableNames = firstShard ? listApplicationTables(firstShard) : []
  const pageSize = firstShard ? pragmaNumber(firstShard, 'page_size') ?? 4096 : 4096
  let pageCount = 0
  let freelistCount = 0
  let actualFileBytes = 0
  let indexCount = 0
  const indexCountsByTable = new Map<string, number>()
  for (const database of shardDatabases) {
    const shardPageSize = pragmaNumber(database, 'page_size') ?? pageSize
    const shardPageCount = pragmaNumber(database, 'page_count') ?? 1
    pageCount += shardPageCount
    freelistCount += pragmaNumber(database, 'freelist_count') ?? 0
    actualFileBytes += shardPageSize * shardPageCount
    const indexesByTable = listIndexesByTable(database)
    indexCount += countIndexes(indexesByTable)
    for (const tableName of tableNames) {
      indexCountsByTable.set(tableName, (indexCountsByTable.get(tableName) ?? 0) + (indexesByTable.get(tableName)?.length ?? 0))
    }
  }
  const tables = tableNames.map((tableName) => {
    const latestRowCount = shardDatabases.reduce((sum, database) => sum + tableRowCount(database, tableName), 0)
    return createTableSeed(tableName, latestRowCount, indexCountsByTable.get(tableName) ?? 0, 1)
  })
  return {
    role: 'codex-context-state',
    path: codexContextStateShardRootPath(),
    pageSize,
    pageCount: Math.max(1, pageCount),
    freelistCount,
    latestFileBytes: Math.max(24_000_000, actualFileBytes),
    dailyFileGrowth: 600_000,
    tableCount: tables.length,
    indexCount,
    tables
  }
}

function createTableSeed(tableName: string, latestRowCount: number, indexCount: number, dailyRowGrowth: number): TableStorageSeed {
  const normalizedRowCount = Math.max(0, latestRowCount)
  const dailyByteGrowth = Math.max(16_384, dailyRowGrowth * (900 + indexCount * 120))
  const latestTotalBytes = Math.max(4096, 24_000 + normalizedRowCount * 900 + indexCount * 4096)
  return {
    tableName,
    latestRowCount: normalizedRowCount,
    latestTotalBytes,
    indexCount,
    dailyRowGrowth,
    dailyByteGrowth
  }
}

function listApplicationTables(database: DatabaseSync): string[] {
  const rows = database.prepare(`
    SELECT name
    FROM sqlite_schema
    WHERE type = 'table'
      AND name NOT LIKE 'sqlite_%'
    ORDER BY name ASC
  `).all() as Array<{ name?: string }>
  return rows.map((row) => row.name).filter((name): name is string => Boolean(name))
}

function listIndexesByTable(database: DatabaseSync): Map<string, string[]> {
  const rows = database.prepare(`
    SELECT name, tbl_name
    FROM sqlite_schema
    WHERE type = 'index'
      AND name NOT LIKE 'sqlite_%'
      AND tbl_name IS NOT NULL
    ORDER BY tbl_name ASC, name ASC
  `).all() as Array<{ name?: string; tbl_name?: string | null }>
  const output = new Map<string, string[]>()
  for (const row of rows) {
    if (!row.name || !row.tbl_name) continue
    const indexes = output.get(row.tbl_name) ?? []
    indexes.push(row.name)
    output.set(row.tbl_name, indexes)
  }
  return output
}

function countIndexes(indexesByTable: Map<string, string[]>): number {
  let total = 0
  for (const indexes of indexesByTable.values()) {
    total += indexes.length
  }
  return total
}

function tableRowCount(database: DatabaseSync, tableName: string): number {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${quoteIdentifier(tableName)}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function pragmaNumber(database: DatabaseSync, name: 'page_size' | 'page_count' | 'freelist_count'): number | undefined {
  const row = database.prepare(`PRAGMA ${name}`).get() as Record<string, unknown> | undefined
  const value = row?.[name]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function historicalValue(latestValue: number, dailyGrowth: number, dayIndex: number, totalDays: number, minimum: number): number {
  const daysBeforeLatest = Math.max(0, totalDays - dayIndex - 1)
  return Math.max(minimum, latestValue - daysBeforeLatest * dailyGrowth)
}

function historicalRowCount(latestRowCount: number, dailyGrowth: number, dayIndex: number, totalDays: number): number {
  if (latestRowCount <= 0) return 0
  const daysBeforeLatest = Math.max(0, totalDays - dayIndex - 1)
  return Math.max(1, latestRowCount - daysBeforeLatest * dailyGrowth)
}

function indexByteRatio(indexCount: number): number {
  return Math.min(0.48, 0.24 + indexCount * 0.015)
}

function quoteIdentifier(value: string): string {
  return `"${value.replaceAll('"', '""')}"`
}
