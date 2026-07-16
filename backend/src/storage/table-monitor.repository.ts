import { lstatSync, statSync } from 'node:fs'
import { basename } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import {
  beginDatabaseTransaction,
  codexContextStateShardIndexes,
  codexContextStateShardPath,
  codexContextStateShardRootPath,
  commitDatabaseTransaction,
  datasetDatabasePath,
  getBusinessDatabase,
  getCodexContextStateShardDatabase,
  getDatasetDatabase,
  getStatsDatabase,
  getUsageCatalogDatabase,
  newId,
  nowIso,
  rollbackDatabaseTransaction,
  statsDatabasePath,
  usageCatalogDatabasePath
} from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export type MonitoredDatabaseRole = 'business' | 'dataset' | 'usage-catalog' | 'stats' | 'codex-context-state'
type SnapshotNumberValue = number | string | null

export interface TableStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  sampledAt: string
  tableKind?: string
  parentTableName?: string
  isPartition?: boolean
  isArchive?: boolean
  rowCount?: number
  tableBytes?: number
  indexBytes?: number
  indexToTableRatio?: number
  indexToTotalRatio?: number
  totalBytes?: number
  pageCount?: number
  indexCount: number
  growthBytes1h?: number
  growthRows1h?: number
  growthBytes24h?: number
  growthRows24h?: number
}

export interface DatabaseStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  databasePath: string
  sampledAt: string
  fileBytes?: number
  walBytes?: number
  shmBytes?: number
  pageSize?: number
  pageCount?: number
  freelistCount?: number
  usedBytes?: number
  freeBytes?: number
  tableCount?: number
  indexCount?: number
}

export interface TableStorageOverview {
  sampledAt?: string
  databases: DatabaseStorageSnapshotSummary[]
  tables: TableStorageSnapshotSummary[]
}

type TableStorageOverviewInput = { startAt?: string; endAt?: string; limit?: number }

export interface CollectTableStorageSnapshotOptions {
  tableScanMode?: 'full' | 'cursor' | 'none'
  maxTablesPerDatabase?: number
}

export interface CollectTableStorageSnapshotResult {
  sampledAt: string
  databaseSnapshots: number
  tableSnapshots: number
  tableScanMode: 'full' | 'cursor' | 'none'
}

interface MonitoredDatabaseTarget {
  role: MonitoredDatabaseRole
  path: string
  database: DatabaseSync
  aggregateDatabases?: DatabaseSync[]
}

interface PreparedTableMonitorTarget {
  target: MonitoredDatabaseTarget
  tables: string[]
  indexesByTable: Map<string, string[]>
  tableRows: TableStorageSnapshotSummary[]
  cursorTableName?: string
}

type PostgresMonitoredSchemaName = 'juhe_business' | 'juhe_dataset' | 'juhe_usage' | 'juhe_stats' | 'juhe_codex_context'

interface PostgresMonitoredSchemaTarget {
  role: MonitoredDatabaseRole
  schemaName: PostgresMonitoredSchemaName
  databasePath: string
}

interface PostgresTableStorageCatalogRow {
  table_name: string
  table_kind: string | null
  parent_table_name: string | null
  is_partition: number | string | boolean | null
  row_count: number | string | null
  table_bytes: number | string | null
  index_bytes: number | string | null
  total_bytes: number | string | null
  index_count: number | string | null
}

interface TableScanSelection {
  tableNames: string[]
  cursorTableName?: string
}

interface ObjectSizeRow {
  name: string
  bytes: number
  page_count: number
  leaf_cell_count: number | null
}

interface TableInfoRow {
  name: string
  type: string
  tbl_name: string | null
}

interface LatestTableSnapshotRow {
  database_role: MonitoredDatabaseRole
  table_name: string
  sampled_at: string
  table_kind: string | null
  parent_table_name: string | null
  is_partition: SnapshotNumberValue
  is_archive: SnapshotNumberValue
  row_count: SnapshotNumberValue
  table_bytes: SnapshotNumberValue
  index_bytes: SnapshotNumberValue
  total_bytes: SnapshotNumberValue
  page_count: SnapshotNumberValue
  index_count: number | string
  growth_bytes_1h: SnapshotNumberValue
  growth_rows_1h: SnapshotNumberValue
  growth_bytes_24h: SnapshotNumberValue
  growth_rows_24h: SnapshotNumberValue
}

interface LatestDatabaseSnapshotRow {
  database_role: MonitoredDatabaseRole
  database_path: string
  sampled_at: string
  file_bytes: SnapshotNumberValue
  wal_bytes: SnapshotNumberValue
  shm_bytes: SnapshotNumberValue
  page_size: SnapshotNumberValue
  page_count: SnapshotNumberValue
  freelist_count: SnapshotNumberValue
  used_bytes: SnapshotNumberValue
  free_bytes: SnapshotNumberValue
  table_count: SnapshotNumberValue
  index_count: SnapshotNumberValue
}

export const tableMonitorSampleRetentionDays = 30
const defaultTableStorageHistoryLimit = 720
const tableStorageOverviewCacheTtlMs = 30_000
const monitoredDatabaseRoles: MonitoredDatabaseRole[] = ['business', 'dataset', 'usage-catalog', 'stats', 'codex-context-state']
const statsSchemaName = 'juhe_stats'
const postgresMonitoredSchemaTargets: PostgresMonitoredSchemaTarget[] = [
  { role: 'business', schemaName: 'juhe_business', databasePath: 'postgres:juhe_business' },
  { role: 'dataset', schemaName: 'juhe_dataset', databasePath: 'postgres:juhe_dataset' },
  { role: 'usage-catalog', schemaName: 'juhe_usage', databasePath: 'postgres:juhe_usage' },
  { role: 'stats', schemaName: 'juhe_stats', databasePath: 'postgres:juhe_stats' },
  { role: 'codex-context-state', schemaName: 'juhe_codex_context', databasePath: 'postgres:juhe_codex_context' }
]
let tableStorageOverviewCache: { key: string; cachedAtMs: number; value: TableStorageOverview } | undefined

export function collectTableStorageSnapshot(sampledAt = nowIso(), options: CollectTableStorageSnapshotOptions = {}): CollectTableStorageSnapshotResult {
  const tableScanMode = options.tableScanMode ?? 'cursor'
  const targets = monitoredDatabaseTargets()
  const preparedTargets: PreparedTableMonitorTarget[] = targets.map((target) => {
    const tables = listTargetTables(target.database)
    const indexesByTable = target.aggregateDatabases?.length
      ? listAggregateIndexesByTable(target.aggregateDatabases, tables)
      : listIndexesByTable(target.database)
    const tableSelection = selectTableScan(getStatsDatabase(), target.role, tables, tableScanMode, options.maxTablesPerDatabase ?? 4)
    const tableRows = collectTargetTableRows(target, sampledAt, tableSelection.tableNames, indexesByTable)
    return {
      target,
      tables,
      indexesByTable,
      tableRows,
      cursorTableName: tableSelection.cursorTableName
    }
  })

  const statsDatabase = getStatsDatabase()
  const transactionStarted = beginDatabaseTransaction(statsDatabase)
  let tableSnapshots = 0
  try {
    for (const prepared of preparedTargets) {
      insertDatabaseSnapshot(statsDatabase, prepared.target, sampledAt, prepared.tables.length, countIndexes(prepared.indexesByTable))
      insertTableSnapshots(statsDatabase, prepared.target, sampledAt, prepared.tableRows)
      if (tableScanMode === 'cursor') {
        recordTableScanCursor(statsDatabase, prepared.target.role, prepared.cursorTableName, sampledAt)
      }
      tableSnapshots += prepared.tableRows.length
    }
    cleanupOldTableStorageSnapshots(statsDatabase, sampledAt)
    commitDatabaseTransaction(statsDatabase, transactionStarted)
    return {
      sampledAt,
      databaseSnapshots: targets.length,
      tableSnapshots,
      tableScanMode
    }
  } catch (error) {
    rollbackDatabaseTransaction(statsDatabase, transactionStarted)
    throw error
  }
}

export async function collectTableStorageSnapshotAsync(sampledAt = nowIso(), options: CollectTableStorageSnapshotOptions = {}): Promise<CollectTableStorageSnapshotResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return collectTableStorageSnapshot(sampledAt, options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const tableScanMode = options.tableScanMode ?? 'cursor'
  const maxTablesPerDatabase = options.maxTablesPerDatabase ?? 4
  return await client.transaction(async (tx) => {
    const blockSize = await postgresBlockSize(tx)
    let tableSnapshots = 0
    for (const target of postgresMonitoredSchemaTargets) {
      const catalogRows = await listPostgresSchemaTables(tx, target.schemaName)
      const tableNames = catalogRows.map((row) => row.table_name)
      const tableSelection = await selectPostgresTableScan(tx, target.role, tableNames, tableScanMode, maxTablesPerDatabase)
      const tableRows = await collectPostgresTargetTableRows(tx, target, sampledAt, tableSelection.tableNames, catalogRows, blockSize)
      await insertPostgresDatabaseSnapshot(tx, target, sampledAt, catalogRows, blockSize)
      await insertPostgresTableSnapshots(tx, sampledAt, tableRows)
      if (tableScanMode === 'cursor') {
        await recordPostgresTableScanCursor(tx, target.role, tableSelection.cursorTableName, sampledAt)
      }
      tableSnapshots += tableRows.length
    }
    await cleanupOldPostgresTableStorageSnapshots(tx, sampledAt)
    return {
      sampledAt,
      databaseSnapshots: postgresMonitoredSchemaTargets.length,
      tableSnapshots,
      tableScanMode
    }
  })
}

export function getTableStorageOverview(input: TableStorageOverviewInput = {}): TableStorageOverview {
  const database = getStatsDatabase()
  const range = normalizeDateRange(input.startAt, input.endAt)
  const databaseSnapshotStatement = database
    .prepare(`
      SELECT ${databaseStorageSnapshotSelectColumns()}
      FROM database_storage_snapshots
      WHERE database_role = ?
        AND sampled_at >= ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC, id DESC
      LIMIT 1
    `)
  const databases = monitoredDatabaseRoles
    .map((databaseRole) => databaseSnapshotStatement.get(databaseRole, range.startAt, range.endAt) as unknown as LatestDatabaseSnapshotRow | undefined)
    .filter((row): row is LatestDatabaseSnapshotRow => Boolean(row))
    .sort(compareDatabaseSnapshotsByRole)
  const sampledAt = databases.map((row) => row.sampled_at).sort().at(-1)
  const tables = database
    .prepare(`
      SELECT ${tableStorageSnapshotSelectColumns()}
      FROM (
        SELECT
          ${tableStorageSnapshotSelectColumns()},
          ROW_NUMBER() OVER (
            PARTITION BY database_role, table_name
            ORDER BY sampled_at DESC, id DESC
          ) AS rank
        FROM table_storage_snapshots INDEXED BY idx_table_storage_snapshots_latest_id
        WHERE sampled_at >= ?
          AND sampled_at <= ?
          AND database_role IN (${sqlPlaceholders(monitoredDatabaseRoles.length)})
      )
      WHERE rank = 1
    `)
    .all(range.startAt, range.endAt, ...monitoredDatabaseRoles) as unknown as LatestTableSnapshotRow[]
  return {
    sampledAt,
    databases: databases.map(databaseSnapshotFromRow),
    tables: tables
      .sort(compareTableSnapshotsForOverview)
      .slice(0, normalizeLimit(input.limit ?? 200))
      .map(tableSnapshotFromRow)
  }
}

export async function getTableStorageOverviewAsync(input: TableStorageOverviewInput = {}): Promise<TableStorageOverview> {
  const cachedOverview = getCachedTableStorageOverview(input)
  if (cachedOverview) return cachedOverview

  let overview: TableStorageOverview
  if (sqliteReadWorkerPoolEnabled()) {
    overview = await requestSqliteReadWorker({
      type: 'get_table_storage_overview_read_only',
      input
    })
    setCachedTableStorageOverview(input, overview)
    return overview
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    overview = getTableStorageOverview(input)
    setCachedTableStorageOverview(input, overview)
    return overview
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const range = normalizeDateRange(input.startAt, input.endAt)
  const databaseRows = await Promise.all(monitoredDatabaseRoles.map((databaseRole) => client.one<LatestDatabaseSnapshotRow>(`
    SELECT ${databaseStorageSnapshotSelectColumns()}
    FROM ${statsTable(client, 'database_storage_snapshots')}
    WHERE database_role = ?
      AND sampled_at >= ?
      AND sampled_at <= ?
    ORDER BY sampled_at DESC, id DESC
    LIMIT 1
  `, [databaseRole, range.startAt, range.endAt])))
  const databases = databaseRows
    .filter((row): row is LatestDatabaseSnapshotRow => Boolean(row))
    .sort(compareDatabaseSnapshotsByRole)
  const sampledAt = databases.map((row) => row.sampled_at).sort().at(-1)
  const tables = await client.query<LatestTableSnapshotRow>(`
    WITH table_keys AS (
      SELECT database_role, table_name
      FROM ${statsTable(client, 'table_storage_snapshots')}
      WHERE sampled_at >= ?
        AND sampled_at <= ?
        AND database_role = ANY(?::text[])
      GROUP BY database_role, table_name
    )
    SELECT ${tableStorageSnapshotSelectColumns('latest')}
    FROM table_keys key
    CROSS JOIN LATERAL (
      SELECT ${tableStorageSnapshotSelectColumns()}
      FROM ${statsTable(client, 'table_storage_snapshots')} latest
      WHERE latest.database_role = key.database_role
        AND latest.table_name = key.table_name
        AND latest.sampled_at >= ?
        AND latest.sampled_at <= ?
      ORDER BY latest.sampled_at DESC, latest.id DESC
      LIMIT 1
    ) latest
  `, [range.startAt, range.endAt, monitoredDatabaseRoles, range.startAt, range.endAt])
  overview = {
    sampledAt,
    databases: databases.map(databaseSnapshotFromRow),
    tables: tables
      .sort(compareTableSnapshotsForOverview)
      .slice(0, normalizeLimit(input.limit ?? 200))
      .map(tableSnapshotFromRow)
  }
  setCachedTableStorageOverview(input, overview)
  return overview
}

function getCachedTableStorageOverview(input: TableStorageOverviewInput): TableStorageOverview | undefined {
  const key = tableStorageOverviewCacheKey(input)
  const cached = tableStorageOverviewCache
  if (!cached || cached.key !== key) return undefined
  if (Date.now() - cached.cachedAtMs > tableStorageOverviewCacheTtlMs) return undefined
  return cached.value
}

function setCachedTableStorageOverview(input: TableStorageOverviewInput, value: TableStorageOverview): void {
  tableStorageOverviewCache = {
    key: tableStorageOverviewCacheKey(input),
    cachedAtMs: Date.now(),
    value
  }
}

function tableStorageOverviewCacheKey(input: TableStorageOverviewInput): string {
  return JSON.stringify({
    startAt: input.startAt ?? '',
    endAt: input.endAt ?? '',
    limit: input.limit ?? ''
  })
}

export function listTableStorageHistory(input: {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}): TableStorageSnapshotSummary[] {
  const range = normalizeDateRange(input.startAt, input.endAt)
  const rows = getStatsDatabase()
    .prepare(`
      SELECT ${tableStorageSnapshotSelectColumns()}
      FROM table_storage_snapshots
      WHERE database_role = ?
        AND table_name = ?
        AND sampled_at >= ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC
      LIMIT ?
    `)
    .all(
      input.databaseRole,
      input.tableName,
      range.startAt,
      range.endAt,
      normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
    ) as unknown as LatestTableSnapshotRow[]
  return rows.reverse().map(tableSnapshotFromRow)
}

export async function listTableStorageHistoryAsync(input: {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}): Promise<TableStorageSnapshotSummary[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_table_storage_history_read_only',
      input
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listTableStorageHistory(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const range = normalizeDateRange(input.startAt, input.endAt)
  const rows = await client.query<LatestTableSnapshotRow>(`
    SELECT ${tableStorageSnapshotSelectColumns()}
    FROM ${statsTable(client, 'table_storage_snapshots')}
    WHERE database_role = ?
      AND table_name = ?
      AND sampled_at >= ?
      AND sampled_at <= ?
    ORDER BY sampled_at DESC
    LIMIT ?
  `, [
    input.databaseRole,
    input.tableName,
    range.startAt,
    range.endAt,
    normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
  ])
  return rows.reverse().map(tableSnapshotFromRow)
}

export function listDatabaseStorageHistory(input: {
  startAt?: string
  endAt?: string
  limit?: number
} = {}): DatabaseStorageSnapshotSummary[] {
  const range = normalizeDateRange(input.startAt, input.endAt)
  const database = getStatsDatabase()
  const limit = normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
  const statement = database
    .prepare(`
      SELECT ${databaseStorageSnapshotSelectColumns()}
      FROM database_storage_snapshots
      WHERE database_role = ?
        AND sampled_at >= ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC, id DESC
      LIMIT ?
    `)
  const rows = monitoredDatabaseRoles.flatMap((databaseRole) => (
    statement.all(databaseRole, range.startAt, range.endAt, limit) as unknown as LatestDatabaseSnapshotRow[]
  ))
  return rows
    .sort(compareDatabaseSnapshotsByTimeAsc)
    .map(databaseSnapshotFromRow)
}

export async function listDatabaseStorageHistoryAsync(input: {
  startAt?: string
  endAt?: string
  limit?: number
} = {}): Promise<DatabaseStorageSnapshotSummary[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_database_storage_history_read_only',
      input
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listDatabaseStorageHistory(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const range = normalizeDateRange(input.startAt, input.endAt)
  const limit = normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
  const rowsByRole = await Promise.all(monitoredDatabaseRoles.map((databaseRole) => client.query<LatestDatabaseSnapshotRow>(`
    SELECT ${databaseStorageSnapshotSelectColumns()}
    FROM ${statsTable(client, 'database_storage_snapshots')}
    WHERE database_role = ?
      AND sampled_at >= ?
      AND sampled_at <= ?
    ORDER BY sampled_at DESC, id DESC
    LIMIT ?
  `, [databaseRole, range.startAt, range.endAt, limit])))
  return rowsByRole
    .flat()
    .sort(compareDatabaseSnapshotsByTimeAsc)
    .map(databaseSnapshotFromRow)
}

export function cleanupTableStorageSnapshotsBefore(cutoffIso: string, limit = 10000): number {
  const database = getStatsDatabase()
  return deleteSnapshotRowsById(database, 'table_storage_snapshots', cutoffIso, limit)
    + deleteSnapshotRowsById(database, 'database_storage_snapshots', cutoffIso, limit)
}

export async function cleanupTableStorageSnapshotsBeforeAsync(cutoffIso: string, limit = 10000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupTableStorageSnapshotsBefore(cutoffIso, limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => (
    await deletePostgresSnapshotRowsById(tx, 'table_storage_snapshots', cutoffIso, limit)
    + await deletePostgresSnapshotRowsById(tx, 'database_storage_snapshots', cutoffIso, limit)
  ))
}

function monitoredDatabaseTargets(): MonitoredDatabaseTarget[] {
  const codexContextStateShardDatabases = existingCodexContextStateShardIndexes()
    .map((shardIndex) => getCodexContextStateShardDatabase(shardIndex))
  const codexContextStatePrimaryDatabase = codexContextStateShardDatabases[0]
  return [
    { role: 'business', path: runtimeConfig.databasePath, database: getBusinessDatabase() },
    { role: 'dataset', path: datasetDatabasePath(), database: getDatasetDatabase() },
    { role: 'usage-catalog', path: usageCatalogDatabasePath(), database: getUsageCatalogDatabase() },
    { role: 'stats', path: statsDatabasePath(), database: getStatsDatabase() },
    ...(codexContextStatePrimaryDatabase
      ? [{
          role: 'codex-context-state' as const,
          path: codexContextStateShardRootPath(),
          database: codexContextStatePrimaryDatabase,
          aggregateDatabases: codexContextStateShardDatabases
        }]
      : [])
  ]
}

function existingCodexContextStateShardIndexes(): number[] {
  const shardRoot = codexContextStateShardRootPath()
  let shardRootStats
  try {
    shardRootStats = statSync(shardRoot)
  } catch (error) {
    if (isMissingPathError(error)) return []
    throw error
  }
  if (!shardRootStats.isDirectory()) {
    throw Object.assign(new Error(`Codex context state shard root is not a directory: ${shardRoot}`), {
      code: 'ENOTDIR',
      path: shardRoot,
      syscall: 'stat'
    })
  }
  return codexContextStateShardIndexes().filter((shardIndex) => {
    try {
      lstatSync(codexContextStateShardPath(shardIndex))
      return true
    } catch (error) {
      if (isMissingPathError(error)) return false
      throw error
    }
  })
}

function isMissingPathError(error: unknown): error is NodeJS.ErrnoException {
  return error instanceof Error && 'code' in error && error.code === 'ENOENT'
}

function collectTargetTableRows(
  target: MonitoredDatabaseTarget,
  sampledAt: string,
  tableNames: string[],
  indexesByTable: Map<string, string[]>
): TableStorageSnapshotSummary[] {
  if (target.aggregateDatabases?.length) {
    return collectAggregateTargetTableRows(target, sampledAt, tableNames)
  }
  const objectNames = new Set<string>()
  for (const tableName of tableNames) {
    objectNames.add(tableName)
    for (const indexName of indexesByTable.get(tableName) ?? []) {
      objectNames.add(indexName)
    }
  }
  const dbstatSizes = loadDbstatObjectSizes(target.database, [...objectNames])
  return tableNames.map((tableName) => {
    const tableSize = dbstatSizes?.get(tableName)
    const indexNames = indexesByTable.get(tableName) ?? []
    const indexSizes = indexNames.map((indexName) => dbstatSizes?.get(indexName)).filter((row): row is ObjectSizeRow => Boolean(row))
    const tableBytes = dbstatSizes ? Number(tableSize?.bytes ?? 0) : undefined
    const tablePages = dbstatSizes ? Number(tableSize?.page_count ?? 0) : undefined
    const indexBytes = dbstatSizes ? indexSizes.reduce((sum, row) => sum + Number(row.bytes ?? 0), 0) : undefined
    const indexPages = dbstatSizes ? indexSizes.reduce((sum, row) => sum + Number(row.page_count ?? 0), 0) : undefined
    const totalBytes = tableBytes !== undefined && indexBytes !== undefined ? tableBytes + indexBytes : undefined
    const pageCount = tablePages !== undefined && indexPages !== undefined ? tablePages + indexPages : undefined
    const rowCount = dbstatRowCount(tableName, tableSize)
    const previous1h = findPreviousTableSnapshot(target.role, tableName, sampledAt, 60)
    const previous24h = findPreviousTableSnapshot(target.role, tableName, sampledAt, 24 * 60)
    return {
      databaseRole: target.role,
      tableName,
      sampledAt,
      tableKind: 'table',
      isPartition: false,
      isArchive: false,
      rowCount,
      tableBytes,
      indexBytes,
      totalBytes,
      pageCount,
      indexCount: indexNames.length,
      growthBytes1h: numericDelta(totalBytes, previous1h?.total_bytes),
      growthRows1h: numericDelta(rowCount, previous1h?.row_count),
      growthBytes24h: numericDelta(totalBytes, previous24h?.total_bytes),
      growthRows24h: numericDelta(rowCount, previous24h?.row_count)
    }
  })
}

function collectAggregateTargetTableRows(
  target: MonitoredDatabaseTarget,
  sampledAt: string,
  tableNames: string[]
): TableStorageSnapshotSummary[] {
  const databases = target.aggregateDatabases?.length ? target.aggregateDatabases : [target.database]
  return tableNames.map((tableName) => {
    let rowCount = 0
    let tableBytes = 0
    let indexBytes = 0
    let pageCount = 0
    let indexCount = 0
    let hasDbstat = true
    for (const database of databases) {
      rowCount += tableRowCount(database, tableName)
      const indexNames = listIndexesByTable(database).get(tableName) ?? []
      indexCount += indexNames.length
      const dbstatSizes = loadDbstatObjectSizes(database, [tableName, ...indexNames])
      if (!dbstatSizes) {
        hasDbstat = false
        continue
      }
      const tableSize = dbstatSizes.get(tableName)
      tableBytes += Number(tableSize?.bytes ?? 0)
      pageCount += Number(tableSize?.page_count ?? 0)
      for (const indexName of indexNames) {
        const indexSize = dbstatSizes.get(indexName)
        indexBytes += Number(indexSize?.bytes ?? 0)
        pageCount += Number(indexSize?.page_count ?? 0)
      }
    }
    const totalBytes = hasDbstat ? tableBytes + indexBytes : undefined
    const previous1h = findPreviousTableSnapshot(target.role, tableName, sampledAt, 60)
    const previous24h = findPreviousTableSnapshot(target.role, tableName, sampledAt, 24 * 60)
    return {
      databaseRole: target.role,
      tableName,
      sampledAt,
      tableKind: 'table',
      isPartition: false,
      isArchive: false,
      rowCount,
      tableBytes: hasDbstat ? tableBytes : undefined,
      indexBytes: hasDbstat ? indexBytes : undefined,
      totalBytes,
      pageCount: hasDbstat ? pageCount : undefined,
      indexCount,
      growthBytes1h: numericDelta(totalBytes, previous1h?.total_bytes),
      growthRows1h: numericDelta(rowCount, previous1h?.row_count),
      growthBytes24h: numericDelta(totalBytes, previous24h?.total_bytes),
      growthRows24h: numericDelta(rowCount, previous24h?.row_count)
    }
  })
}

function insertDatabaseSnapshot(database: DatabaseSync, target: MonitoredDatabaseTarget, sampledAt: string, tableCount: number, indexCount: number): void {
  const storageStats = databaseStorageStats(target)
  const pageSize = storageStats.pageSize
  const pageCount = storageStats.pageCount
  const freelistCount = storageStats.freelistCount
  const fileBytes = estimateDatabaseMainFileBytes(pageSize, pageCount)
  const freeBytes = pageSize !== undefined && freelistCount !== undefined ? pageSize * freelistCount : undefined
  const usedBytes = pageSize !== undefined && pageCount !== undefined && freelistCount !== undefined ? pageSize * Math.max(0, pageCount - freelistCount) : undefined
  database.prepare(`
    INSERT INTO database_storage_snapshots (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    newId('dbsnap'),
    target.role,
    target.path,
    sampledAt,
    fileBytes ?? null,
    null,
    null,
    pageSize ?? null,
    pageCount ?? null,
    freelistCount ?? null,
    usedBytes ?? null,
    freeBytes ?? null,
    tableCount,
    indexCount,
    sampledAt
  )
}

function databaseStorageStats(target: MonitoredDatabaseTarget): { pageSize?: number; pageCount?: number; freelistCount?: number } {
  const databases = target.aggregateDatabases?.length ? target.aggregateDatabases : [target.database]
  if (databases.length === 1) {
    return {
      pageSize: pragmaNumber(databases[0], 'page_size'),
      pageCount: pragmaNumber(databases[0], 'page_count'),
      freelistCount: pragmaNumber(databases[0], 'freelist_count')
    }
  }
  const pageSize = pragmaNumber(databases[0], 'page_size')
  let pageCount = 0
  let freelistCount = 0
  for (const database of databases) {
    pageCount += pragmaNumber(database, 'page_count') ?? 0
    freelistCount += pragmaNumber(database, 'freelist_count') ?? 0
  }
  return { pageSize, pageCount, freelistCount }
}

function insertTableSnapshots(database: DatabaseSync, target: MonitoredDatabaseTarget, sampledAt: string, tables: TableStorageSnapshotSummary[]): void {
  const insert = database.prepare(`
    INSERT INTO table_storage_snapshots (
      id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive, row_count, table_bytes, index_bytes, total_bytes,
      page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const table of tables) {
    insert.run(
      newId('tblsnap'),
      target.role,
      table.tableName,
      sampledAt,
      table.tableKind ?? 'table',
      table.parentTableName ?? null,
      table.isPartition ? 1 : 0,
      table.isArchive ? 1 : 0,
      table.rowCount ?? null,
      table.tableBytes ?? null,
      table.indexBytes ?? null,
      table.totalBytes ?? null,
      table.pageCount ?? null,
      table.indexCount,
      table.growthBytes1h ?? null,
      table.growthRows1h ?? null,
      table.growthBytes24h ?? null,
      table.growthRows24h ?? null,
      sampledAt
    )
  }
}

function listTargetTables(database: DatabaseSync): string[] {
  const rows = database
    .prepare(`
      SELECT name
      FROM sqlite_schema
      WHERE type = 'table'
        AND name NOT LIKE 'sqlite_%'
      ORDER BY name ASC
    `)
    .all() as unknown as Array<{ name?: string }>
  return rows.map((row) => row.name).filter((name): name is string => Boolean(name))
}

function listIndexesByTable(database: DatabaseSync): Map<string, string[]> {
  const rows = database
    .prepare(`
      SELECT name, tbl_name
      FROM sqlite_schema
      WHERE type = 'index'
        AND name NOT LIKE 'sqlite_%'
        AND tbl_name IS NOT NULL
      ORDER BY tbl_name ASC, name ASC
    `)
    .all() as unknown as TableInfoRow[]
  const result = new Map<string, string[]>()
  for (const row of rows) {
    if (!row.name || !row.tbl_name) continue
    result.set(row.tbl_name, [...(result.get(row.tbl_name) ?? []), row.name])
  }
  return result
}

function listAggregateIndexesByTable(databases: DatabaseSync[], tableNames: string[]): Map<string, string[]> {
  const result = new Map<string, string[]>()
  for (const [databaseIndex, database] of databases.entries()) {
    const indexesByTable = listIndexesByTable(database)
    for (const tableName of tableNames) {
      const aggregateIndexes = result.get(tableName) ?? []
      for (const indexName of indexesByTable.get(tableName) ?? []) {
        aggregateIndexes.push(`${databaseIndex}:${indexName}`)
      }
      result.set(tableName, aggregateIndexes)
    }
  }
  return result
}

function tableRowCount(database: DatabaseSync, tableName: string): number {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${quoteIdentifier(tableName)}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function quoteIdentifier(value: string): string {
  return `"${value.replaceAll('"', '""')}"`
}

function loadDbstatObjectSizes(database: DatabaseSync, objectNames: string[]): Map<string, ObjectSizeRow> | undefined {
  const names = [...new Set(objectNames.filter(Boolean))]
  if (names.length === 0) {
    return new Map()
  }
  try {
    const placeholders = sqlPlaceholders(names.length)
    const rows = database
      .prepare(`
        SELECT
          name,
          SUM(pgsize) AS bytes,
          SUM(1) AS page_count,
          SUM(CASE WHEN pagetype = 'leaf' THEN ncell ELSE 0 END) AS leaf_cell_count
        FROM dbstat
        WHERE name IN (${placeholders})
        GROUP BY name
      `)
      .all(...names) as unknown as ObjectSizeRow[]
    return new Map(rows.map((row) => [row.name, row]))
  } catch {
    return undefined
  }
}

async function listPostgresSchemaTables(client: DatabaseClient, schemaName: PostgresMonitoredSchemaTarget['schemaName']): Promise<PostgresTableStorageCatalogRow[]> {
  return await client.query<PostgresTableStorageCatalogRow>(`
    SELECT
      c.relname AS table_name,
      CASE c.relkind
        WHEN 'p' THEN 'partitioned_table'
        WHEN 'm' THEN 'materialized_view'
        ELSE 'table'
      END AS table_kind,
      parent.relname AS parent_table_name,
      (parent.oid IS NOT NULL)::integer AS is_partition,
      GREATEST(COALESCE(s.n_live_tup::double precision, c.reltuples, 0), 0)::bigint AS row_count,
      pg_relation_size(c.oid)::bigint AS table_bytes,
      pg_indexes_size(c.oid)::bigint AS index_bytes,
      pg_total_relation_size(c.oid)::bigint AS total_bytes,
      COALESCE(i.index_count, 0)::integer AS index_count
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_inherits inh ON inh.inhrelid = c.oid
    LEFT JOIN pg_class parent ON parent.oid = inh.inhparent
    LEFT JOIN pg_stat_all_tables s ON s.relid = c.oid
    LEFT JOIN (
      SELECT indrelid, COUNT(*)::integer AS index_count
      FROM pg_index
      GROUP BY indrelid
    ) i ON i.indrelid = c.oid
    WHERE n.nspname = ?
      AND c.relkind IN ('r', 'p', 'm')
    ORDER BY c.relname ASC
  `, [schemaName])
}

async function postgresBlockSize(client: DatabaseClient): Promise<number | undefined> {
  const row = await client.one<{ block_size?: number | string }>("SELECT current_setting('block_size')::integer AS block_size")
  return optionalNumber(row?.block_size)
}

async function collectPostgresTargetTableRows(
  client: DatabaseClient,
  target: PostgresMonitoredSchemaTarget,
  sampledAt: string,
  tableNames: string[],
  catalogRows: PostgresTableStorageCatalogRow[],
  blockSize: number | undefined
): Promise<TableStorageSnapshotSummary[]> {
  const catalogByTable = new Map(catalogRows.map((row) => [row.table_name, row]))
  const previous1h = await findPreviousPostgresTableSnapshots(client, target.role, tableNames, sampledAt, 60)
  const previous24h = await findPreviousPostgresTableSnapshots(client, target.role, tableNames, sampledAt, 24 * 60)
  return tableNames.map((tableName) => {
    const row = catalogByTable.get(tableName)
    const rowCount = optionalNumber(row?.row_count)
    const tableBytes = optionalNumber(row?.table_bytes)
    const indexBytes = optionalNumber(row?.index_bytes)
    const totalBytes = optionalNumber(row?.total_bytes)
    const pageCount = estimatePageCount(totalBytes, blockSize)
    const indexCount = optionalNumber(row?.index_count) ?? 0
    const previousHour = previous1h.get(tableName)
    const previousDay = previous24h.get(tableName)
    return {
      databaseRole: target.role,
      tableName,
      sampledAt,
      tableKind: row?.table_kind ?? 'table',
      parentTableName: row?.parent_table_name ?? undefined,
      isPartition: booleanFromSnapshot(row?.is_partition),
      isArchive: false,
      rowCount,
      tableBytes,
      indexBytes,
      totalBytes,
      pageCount,
      indexCount,
      growthBytes1h: numericDelta(totalBytes, previousHour?.total_bytes),
      growthRows1h: numericDelta(rowCount, previousHour?.row_count),
      growthBytes24h: numericDelta(totalBytes, previousDay?.total_bytes),
      growthRows24h: numericDelta(rowCount, previousDay?.row_count)
    }
  })
}

async function insertPostgresDatabaseSnapshot(
  client: DatabaseClient,
  target: PostgresMonitoredSchemaTarget,
  sampledAt: string,
  catalogRows: PostgresTableStorageCatalogRow[],
  blockSize: number | undefined
): Promise<void> {
  const totalBytes = catalogRows.reduce((sum, row) => sum + (optionalNumber(row.total_bytes) ?? 0), 0)
  const pageCount = estimatePageCount(totalBytes, blockSize)
  const indexCount = catalogRows.reduce((sum, row) => sum + (optionalNumber(row.index_count) ?? 0), 0)
  await client.execute(`
    INSERT INTO ${statsTable(client, 'database_storage_snapshots')} (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    newId('dbsnap'),
    target.role,
    target.databasePath,
    sampledAt,
    totalBytes,
    null,
    null,
    blockSize ?? null,
    pageCount ?? null,
    null,
    totalBytes,
    null,
    catalogRows.length,
    indexCount,
    sampledAt
  ])
}

async function insertPostgresTableSnapshots(client: DatabaseClient, sampledAt: string, tables: TableStorageSnapshotSummary[]): Promise<void> {
  const columns = [
    'id',
    'database_role',
    'table_name',
    'sampled_at',
    'table_kind',
    'parent_table_name',
    'is_partition',
    'is_archive',
    'row_count',
    'table_bytes',
    'index_bytes',
    'total_bytes',
    'page_count',
    'index_count',
    'growth_bytes_1h',
    'growth_rows_1h',
    'growth_bytes_24h',
    'growth_rows_24h',
    'created_at'
  ]
  const rows = tables.map((table) => [
    newId('tblsnap'),
    table.databaseRole,
    table.tableName,
    sampledAt,
    table.tableKind ?? 'table',
    table.parentTableName ?? null,
    table.isPartition ? 1 : 0,
    table.isArchive ? 1 : 0,
    table.rowCount ?? null,
    table.tableBytes ?? null,
    table.indexBytes ?? null,
    table.totalBytes ?? null,
    table.pageCount ?? null,
    table.indexCount,
    table.growthBytes1h ?? null,
    table.growthRows1h ?? null,
    table.growthBytes24h ?? null,
    table.growthRows24h ?? null,
    sampledAt
  ])
  for (const chunk of chunkValues(rows, 250)) {
    if (chunk.length === 0) continue
    const placeholders = chunk
      .map((row) => `(${row.map(() => '?').join(', ')})`)
      .join(', ')
    await client.execute(`
      INSERT INTO ${statsTable(client, 'table_storage_snapshots')} (${columns.map((column) => client.dialect.quoteIdentifier(column)).join(', ')})
      VALUES ${placeholders}
      ON CONFLICT(database_role, table_name, sampled_at) DO UPDATE SET
        table_kind = excluded.table_kind,
        parent_table_name = excluded.parent_table_name,
        is_partition = excluded.is_partition,
        is_archive = excluded.is_archive,
        row_count = excluded.row_count,
        table_bytes = excluded.table_bytes,
        index_bytes = excluded.index_bytes,
        total_bytes = excluded.total_bytes,
        page_count = excluded.page_count,
        index_count = excluded.index_count,
        growth_bytes_1h = excluded.growth_bytes_1h,
        growth_rows_1h = excluded.growth_rows_1h,
        growth_bytes_24h = excluded.growth_bytes_24h,
        growth_rows_24h = excluded.growth_rows_24h,
        created_at = excluded.created_at
    `, chunk.flat())
  }
}

async function selectPostgresTableScan(
  client: DatabaseClient,
  databaseRole: MonitoredDatabaseRole,
  tables: string[],
  tableScanMode: 'full' | 'cursor' | 'none',
  maxTables: number
): Promise<TableScanSelection> {
  if (tableScanMode === 'none') {
    return { tableNames: [] }
  }
  if (tableScanMode === 'full') {
    return { tableNames: tables }
  }
  return await selectPostgresCursorTableNames(client, databaseRole, tables, maxTables)
}

async function selectPostgresCursorTableNames(
  client: DatabaseClient,
  databaseRole: MonitoredDatabaseRole,
  tables: string[],
  maxTables: number
): Promise<TableScanSelection> {
  if (tables.length === 0 || maxTables <= 0) {
    return { tableNames: [] }
  }
  const normalizedMaxTables = Math.min(Math.trunc(maxTables), tables.length)
  if (normalizedMaxTables >= tables.length) {
    return { tableNames: tables, cursorTableName: tables.at(-1) }
  }

  const cursor = await latestPostgresTableScanCursor(client, databaseRole)
  const cursorIndex = cursor ? tables.indexOf(cursor) : -1
  const startIndex = cursorIndex >= 0 ? (cursorIndex + 1) % tables.length : 0
  const selected = Array.from({ length: normalizedMaxTables }, (_value, offset) => tables[(startIndex + offset) % tables.length])
  return { tableNames: selected, cursorTableName: selected.at(-1) }
}

async function latestPostgresTableScanCursor(client: DatabaseClient, databaseRole: MonitoredDatabaseRole): Promise<string | undefined> {
  const row = await client.one<{ cursor_id?: string | null }>(`
    SELECT cursor_id
    FROM ${statsTable(client, 'stats_job_state')}
    WHERE scope_type = 'table_monitor'
      AND scope_id = ?
      AND job_name = 'table_storage_snapshots'
    LIMIT 1
  `, [databaseRole])
  return row?.cursor_id || undefined
}

async function recordPostgresTableScanCursor(client: DatabaseClient, databaseRole: MonitoredDatabaseRole, tableName: string | undefined, sampledAt: string): Promise<void> {
  if (!tableName) return
  await client.execute(`
    INSERT INTO ${statsTable(client, 'stats_job_state')} (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, lag_seconds, updated_at
    ) VALUES ('table_monitor', ?, 'table_storage_snapshots', ?, ?, ?, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `, [databaseRole, sampledAt, tableName, sampledAt, sampledAt])
}

async function findPreviousPostgresTableSnapshots(
  client: DatabaseClient,
  databaseRole: MonitoredDatabaseRole,
  tableNames: string[],
  sampledAt: string,
  minutesBack: number
): Promise<Map<string, LatestTableSnapshotRow>> {
  if (tableNames.length === 0) {
    return new Map()
  }
  const targetTime = new Date(Date.parse(sampledAt) - minutesBack * 60 * 1000).toISOString()
  const rows = await client.query<LatestTableSnapshotRow>(`
    SELECT DISTINCT ON (table_name) ${tableStorageSnapshotSelectColumns()}
    FROM ${statsTable(client, 'table_storage_snapshots')}
    WHERE database_role = ?
      AND table_name = ANY(?::text[])
      AND sampled_at <= ?
    ORDER BY table_name ASC, sampled_at DESC, id DESC
  `, [databaseRole, tableNames, targetTime])
  return new Map(rows.map((row) => [row.table_name, row]))
}

function countIndexes(indexesByTable: Map<string, string[]>): number {
  let count = 0
  for (const indexes of indexesByTable.values()) {
    count += indexes.length
  }
  return count
}

function selectTableScan(
  database: DatabaseSync,
  databaseRole: MonitoredDatabaseRole,
  tables: string[],
  tableScanMode: 'full' | 'cursor' | 'none',
  maxTables: number
): TableScanSelection {
  if (tableScanMode === 'none') {
    return { tableNames: [] }
  }
  if (tableScanMode === 'full') {
    return { tableNames: tables }
  }
  return selectCursorTableNames(database, databaseRole, tables, maxTables)
}

function selectCursorTableNames(
  database: DatabaseSync,
  databaseRole: MonitoredDatabaseRole,
  tables: string[],
  maxTables: number
): TableScanSelection {
  if (tables.length === 0 || maxTables <= 0) {
    return { tableNames: [] }
  }
  const normalizedMaxTables = Math.min(Math.trunc(maxTables), tables.length)
  if (normalizedMaxTables >= tables.length) {
    return { tableNames: tables, cursorTableName: tables.at(-1) }
  }

  const cursor = latestTableScanCursor(database, databaseRole)
  const cursorIndex = cursor ? tables.indexOf(cursor) : -1
  const startIndex = cursorIndex >= 0 ? (cursorIndex + 1) % tables.length : 0
  const selected = Array.from({ length: normalizedMaxTables }, (_value, offset) => tables[(startIndex + offset) % tables.length])
  return { tableNames: selected, cursorTableName: selected.at(-1) }
}

function latestTableScanCursor(database: DatabaseSync, databaseRole: MonitoredDatabaseRole): string | undefined {
  const row = database
    .prepare(`
      SELECT cursor_id
      FROM stats_job_state
      WHERE scope_type = 'table_monitor'
        AND scope_id = ?
        AND job_name = 'table_storage_snapshots'
      LIMIT 1
    `)
    .get(databaseRole) as { cursor_id?: string | null } | undefined
  return row?.cursor_id || undefined
}

function recordTableScanCursor(database: DatabaseSync, databaseRole: MonitoredDatabaseRole, tableName: string | undefined, sampledAt: string): void {
  if (!tableName) return
  database
    .prepare(`
      INSERT INTO stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, lag_seconds, updated_at
      ) VALUES ('table_monitor', ?, 'table_storage_snapshots', ?, ?, ?, NULL, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = excluded.cursor_created_at,
        cursor_id = excluded.cursor_id,
        last_success_at = excluded.last_success_at,
        lag_seconds = NULL,
        updated_at = excluded.updated_at
    `)
    .run(databaseRole, sampledAt, tableName, sampledAt, sampledAt)
}

function dbstatRowCount(tableName: string, tableSize: ObjectSizeRow | undefined): number | undefined {
  if (!tableSize || isFtsShadowTable(tableName)) {
    return undefined
  }
  const value = Number(tableSize.leaf_cell_count ?? 0)
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : undefined
}

function isFtsShadowTable(tableName: string): boolean {
  return /_(data|idx|content|docsize|config)$/.test(tableName)
}

function findPreviousTableSnapshot(databaseRole: MonitoredDatabaseRole, tableName: string, sampledAt: string, minutesBack: number): LatestTableSnapshotRow | undefined {
  const targetTime = new Date(Date.parse(sampledAt) - minutesBack * 60 * 1000).toISOString()
  return getStatsDatabase()
    .prepare(`
      SELECT ${tableStorageSnapshotSelectColumns()}
      FROM table_storage_snapshots
      WHERE database_role = ?
        AND table_name = ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC
      LIMIT 1
    `)
    .get(databaseRole, tableName, targetTime) as unknown as LatestTableSnapshotRow | undefined
}

function databaseStorageSnapshotSelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return [
    'database_role',
    'database_path',
    'sampled_at',
    'file_bytes',
    'wal_bytes',
    'shm_bytes',
    'page_size',
    'page_count',
    'freelist_count',
    'used_bytes',
    'free_bytes',
    'table_count',
    'index_count'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function tableStorageSnapshotSelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return [
    'database_role',
    'table_name',
    'sampled_at',
    'table_kind',
    'parent_table_name',
    'is_partition',
    'is_archive',
    'row_count',
    'table_bytes',
    'index_bytes',
    'total_bytes',
    'page_count',
    'index_count',
    'growth_bytes_1h',
    'growth_rows_1h',
    'growth_bytes_24h',
    'growth_rows_24h'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

function compareDatabaseSnapshotsByRole(left: LatestDatabaseSnapshotRow, right: LatestDatabaseSnapshotRow): number {
  return databaseRoleSortRank(left.database_role) - databaseRoleSortRank(right.database_role)
}

function compareDatabaseSnapshotsByTimeAsc(left: LatestDatabaseSnapshotRow, right: LatestDatabaseSnapshotRow): number {
  const sampledAt = left.sampled_at.localeCompare(right.sampled_at)
  return sampledAt !== 0 ? sampledAt : compareDatabaseSnapshotsByRole(left, right)
}

function compareTableSnapshotsForOverview(left: LatestTableSnapshotRow, right: LatestTableSnapshotRow): number {
  const totalBytes = compareNullableNumberDesc(left.total_bytes, right.total_bytes)
  if (totalBytes !== 0) return totalBytes
  const rowCount = compareNullableNumberDesc(left.row_count, right.row_count)
  if (rowCount !== 0) return rowCount
  const tableName = left.table_name.localeCompare(right.table_name)
  return tableName !== 0 ? tableName : left.database_role.localeCompare(right.database_role)
}

function compareNullableNumberDesc(left: number | string | null | undefined, right: number | string | null | undefined): number {
  const leftNumber = optionalNumber(left) ?? Number.NEGATIVE_INFINITY
  const rightNumber = optionalNumber(right) ?? Number.NEGATIVE_INFINITY
  if (leftNumber === rightNumber) return 0
  return leftNumber > rightNumber ? -1 : 1
}

function databaseRoleSortRank(databaseRole: MonitoredDatabaseRole): number {
  const index = monitoredDatabaseRoles.indexOf(databaseRole)
  return index >= 0 ? index : monitoredDatabaseRoles.length
}

function cleanupOldTableStorageSnapshots(database: DatabaseSync, sampledAt: string): void {
  const cutoff = new Date(Date.parse(sampledAt) - tableMonitorSampleRetentionDays * 24 * 60 * 60 * 1000).toISOString()
  deleteSnapshotRowsById(database, 'table_storage_snapshots', cutoff, 10000)
  deleteSnapshotRowsById(database, 'database_storage_snapshots', cutoff, 10000)
}

function pragmaNumber(database: DatabaseSync, pragmaName: 'page_size' | 'page_count' | 'freelist_count'): number | undefined {
  const row = database.prepare(`PRAGMA ${pragmaName}`).get() as unknown as Record<string, number> | undefined
  const value = row?.[pragmaName]
  return Number.isFinite(value) ? Number(value) : undefined
}

function estimateDatabaseMainFileBytes(pageSize: number | undefined, pageCount: number | undefined): number | undefined {
  if (pageSize === undefined || pageCount === undefined) {
    return undefined
  }
  return pageSize * pageCount
}

function estimatePageCount(totalBytes: number | undefined, pageSize: number | undefined): number | undefined {
  if (totalBytes === undefined || pageSize === undefined || pageSize <= 0) {
    return undefined
  }
  return Math.ceil(totalBytes / pageSize)
}

function normalizeLimit(value: number): number {
  return Number.isFinite(value) ? Math.min(Math.max(Math.trunc(value), 1), 10000) : 200
}

function normalizeDateTime(value: string | undefined, fallback: string): string {
  if (!value) return fallback
  const time = Date.parse(value)
  return Number.isNaN(time) ? fallback : new Date(time).toISOString()
}

function normalizeDateRange(startAt?: string, endAt?: string): { startAt: string; endAt: string } {
  const defaultEndAt = nowIso()
  const defaultStartAt = new Date(Date.parse(defaultEndAt) - tableMonitorSampleRetentionDays * 24 * 60 * 60 * 1000).toISOString()
  const normalizedStartAt = normalizeDateTime(startAt, defaultStartAt)
  const normalizedEndAt = normalizeDateTime(endAt, defaultEndAt)
  return normalizedStartAt <= normalizedEndAt
    ? { startAt: normalizedStartAt, endAt: normalizedEndAt }
    : { startAt: normalizedEndAt, endAt: normalizedStartAt }
}

function deleteSnapshotRowsById(
  database: DatabaseSync,
  tableName: 'database_storage_snapshots' | 'table_storage_snapshots',
  cutoffIso: string,
  limit: number
): number {
  const rows = database
    .prepare(`SELECT id FROM ${tableName} WHERE sampled_at < ? ORDER BY sampled_at ASC, id ASC LIMIT ?`)
    .all(cutoffIso, normalizeLimit(limit)) as Array<{ id?: string | null }>
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  const result = database.prepare(`DELETE FROM ${tableName} WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

async function cleanupOldPostgresTableStorageSnapshots(client: DatabaseClient, sampledAt: string): Promise<void> {
  const cutoff = new Date(Date.parse(sampledAt) - tableMonitorSampleRetentionDays * 24 * 60 * 60 * 1000).toISOString()
  await deletePostgresSnapshotRowsById(client, 'table_storage_snapshots', cutoff, 10000)
  await deletePostgresSnapshotRowsById(client, 'database_storage_snapshots', cutoff, 10000)
}

async function deletePostgresSnapshotRowsById(
  client: DatabaseClient,
  tableName: 'database_storage_snapshots' | 'table_storage_snapshots',
  cutoffIso: string,
  limit: number
): Promise<number> {
  const result = await client.execute(`
    WITH expired AS (
      SELECT id
      FROM ${statsTable(client, tableName)}
      WHERE sampled_at < ?
      ORDER BY sampled_at ASC, id ASC
      LIMIT ?
    )
    DELETE FROM ${statsTable(client, tableName)}
    WHERE id IN (SELECT id FROM expired)
  `, [cutoffIso, normalizeLimit(limit)])
  return result.changes
}

function databaseSnapshotFromRow(row: LatestDatabaseSnapshotRow): DatabaseStorageSnapshotSummary {
  return {
    databaseRole: row.database_role,
    databasePath: basename(row.database_path),
    sampledAt: row.sampled_at,
    fileBytes: optionalNumber(row.file_bytes),
    walBytes: optionalNumber(row.wal_bytes),
    shmBytes: optionalNumber(row.shm_bytes),
    pageSize: optionalNumber(row.page_size),
    pageCount: optionalNumber(row.page_count),
    freelistCount: optionalNumber(row.freelist_count),
    usedBytes: optionalNumber(row.used_bytes),
    freeBytes: optionalNumber(row.free_bytes),
    tableCount: optionalNumber(row.table_count),
    indexCount: optionalNumber(row.index_count)
  }
}

function tableSnapshotFromRow(row: LatestTableSnapshotRow): TableStorageSnapshotSummary {
  const tableBytes = optionalNumber(row.table_bytes)
  const indexBytes = optionalNumber(row.index_bytes)
  const totalBytes = optionalNumber(row.total_bytes)
  return {
    databaseRole: row.database_role,
    tableName: row.table_name,
    sampledAt: row.sampled_at,
    tableKind: row.table_kind ?? undefined,
    parentTableName: row.parent_table_name ?? undefined,
    isPartition: booleanFromSnapshot(row.is_partition),
    isArchive: booleanFromSnapshot(row.is_archive),
    rowCount: optionalNumber(row.row_count),
    tableBytes,
    indexBytes,
    indexToTableRatio: ratio(indexBytes, tableBytes),
    indexToTotalRatio: ratio(indexBytes, totalBytes),
    totalBytes,
    pageCount: optionalNumber(row.page_count),
    indexCount: Number(row.index_count ?? 0),
    growthBytes1h: optionalNumber(row.growth_bytes_1h),
    growthRows1h: optionalNumber(row.growth_rows_1h),
    growthBytes24h: optionalNumber(row.growth_bytes_24h),
    growthRows24h: optionalNumber(row.growth_rows_24h)
  }
}

function booleanFromSnapshot(value: SnapshotNumberValue | boolean | undefined): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') return value === '1' || value.toLowerCase() === 'true'
  return false
}

function optionalNumber(value: number | string | null | undefined): number | undefined {
  const numberValue = typeof value === 'string' ? Number(value) : value
  return typeof numberValue === 'number' && Number.isFinite(numberValue) ? numberValue : undefined
}

function ratio(numerator: number | undefined, denominator: number | undefined): number | undefined {
  if (numerator === undefined || denominator === undefined || denominator <= 0) return undefined
  return numerator / denominator
}

function numericDelta(current: number | undefined, previous: number | string | null | undefined): number | undefined {
  const previousNumber = optionalNumber(previous)
  return current !== undefined && previousNumber !== undefined ? current - previousNumber : undefined
}
