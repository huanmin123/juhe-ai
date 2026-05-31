import { basename } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction, statsDatabasePath } from './database.js'
import { sqlPlaceholders } from './query-utils.js'

export type MonitoredDatabaseRole = 'business' | 'dataset' | 'stats'

export interface TableStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  sampledAt: string
  rowCount?: number
  tableBytes?: number
  indexBytes?: number
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
}

interface PreparedTableMonitorTarget {
  target: MonitoredDatabaseTarget
  tables: string[]
  indexesByTable: Map<string, string[]>
  tableRows: TableStorageSnapshotSummary[]
  cursorTableName?: string
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
  row_count: number | null
  table_bytes: number | null
  index_bytes: number | null
  total_bytes: number | null
  page_count: number | null
  index_count: number
  growth_bytes_1h: number | null
  growth_rows_1h: number | null
  growth_bytes_24h: number | null
  growth_rows_24h: number | null
}

interface LatestDatabaseSnapshotRow {
  database_role: MonitoredDatabaseRole
  database_path: string
  sampled_at: string
  file_bytes: number | null
  wal_bytes: number | null
  shm_bytes: number | null
  page_size: number | null
  page_count: number | null
  freelist_count: number | null
  used_bytes: number | null
  free_bytes: number | null
  table_count: number | null
  index_count: number | null
}

export const tableMonitorSampleRetentionDays = 30
const defaultTableStorageHistoryLimit = 720

export function collectTableStorageSnapshot(sampledAt = nowIso(), options: CollectTableStorageSnapshotOptions = {}): CollectTableStorageSnapshotResult {
  const tableScanMode = options.tableScanMode ?? 'cursor'
  const targets = monitoredDatabaseTargets()
  const preparedTargets: PreparedTableMonitorTarget[] = targets.map((target) => {
    const tables = listTargetTables(target.database)
    const indexesByTable = listIndexesByTable(target.database)
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

export function getTableStorageOverview(input: { startAt?: string; endAt?: string; limit?: number } = {}): TableStorageOverview {
  const database = getStatsDatabase()
  const range = normalizeDateRange(input.startAt, input.endAt)
  const databases = database
    .prepare(`
      SELECT ${databaseStorageSnapshotSelectColumns()}
      FROM (
        SELECT
          ${databaseStorageSnapshotSelectColumns()},
          ROW_NUMBER() OVER (
            PARTITION BY database_role
            ORDER BY sampled_at DESC, id DESC
          ) AS rank
        FROM database_storage_snapshots
        WHERE sampled_at >= ?
          AND sampled_at <= ?
      )
      WHERE rank = 1
      ORDER BY database_role ASC
    `)
    .all(range.startAt, range.endAt) as unknown as LatestDatabaseSnapshotRow[]
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
        FROM table_storage_snapshots
        WHERE sampled_at >= ?
          AND sampled_at <= ?
      )
      WHERE rank = 1
      ORDER BY total_bytes DESC, row_count DESC, table_name ASC
      LIMIT ?
    `)
    .all(range.startAt, range.endAt, normalizeLimit(input.limit ?? 200)) as unknown as LatestTableSnapshotRow[]
  return {
    sampledAt,
    databases: databases.map(databaseSnapshotFromRow),
    tables: tables.map(tableSnapshotFromRow)
  }
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

export function listDatabaseStorageHistory(input: {
  startAt?: string
  endAt?: string
  limit?: number
} = {}): DatabaseStorageSnapshotSummary[] {
  const range = normalizeDateRange(input.startAt, input.endAt)
  const rows = getStatsDatabase()
    .prepare(`
      SELECT ${databaseStorageSnapshotSelectColumns()}
      FROM (
        SELECT
          ${databaseStorageSnapshotSelectColumns()},
          ROW_NUMBER() OVER (
            PARTITION BY database_role
            ORDER BY sampled_at DESC, id DESC
          ) AS rank
        FROM database_storage_snapshots
        WHERE sampled_at >= ?
          AND sampled_at <= ?
      )
      WHERE rank <= ?
      ORDER BY sampled_at ASC, database_role ASC
    `)
    .all(
      range.startAt,
      range.endAt,
      normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
    ) as unknown as LatestDatabaseSnapshotRow[]
  return rows.map(databaseSnapshotFromRow)
}

export function cleanupTableStorageSnapshotsBefore(cutoffIso: string, limit = 10000): number {
  const database = getStatsDatabase()
  return deleteSnapshotRowsById(database, 'table_storage_snapshots', cutoffIso, limit)
    + deleteSnapshotRowsById(database, 'database_storage_snapshots', cutoffIso, limit)
}

function monitoredDatabaseTargets(): MonitoredDatabaseTarget[] {
  return [
    { role: 'business', path: runtimeConfig.databasePath, database: getBusinessDatabase() },
    { role: 'dataset', path: datasetDatabasePath(), database: getDatasetDatabase() },
    { role: 'stats', path: statsDatabasePath(), database: getStatsDatabase() }
  ]
}

function collectTargetTableRows(
  target: MonitoredDatabaseTarget,
  sampledAt: string,
  tableNames: string[],
  indexesByTable: Map<string, string[]>
): TableStorageSnapshotSummary[] {
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
      rowCount,
      tableBytes,
      indexBytes,
      totalBytes,
      pageCount,
      indexCount: indexNames.length,
      growthBytes1h: previous1h && totalBytes !== undefined && previous1h.total_bytes !== null ? totalBytes - previous1h.total_bytes : undefined,
      growthRows1h: previous1h && rowCount !== undefined && previous1h.row_count !== null ? rowCount - previous1h.row_count : undefined,
      growthBytes24h: previous24h && totalBytes !== undefined && previous24h.total_bytes !== null ? totalBytes - previous24h.total_bytes : undefined,
      growthRows24h: previous24h && rowCount !== undefined && previous24h.row_count !== null ? rowCount - previous24h.row_count : undefined
    }
  })
}

function insertDatabaseSnapshot(database: DatabaseSync, target: MonitoredDatabaseTarget, sampledAt: string, tableCount: number, indexCount: number): void {
  const pageSize = pragmaNumber(target.database, 'page_size')
  const pageCount = pragmaNumber(target.database, 'page_count')
  const freelistCount = pragmaNumber(target.database, 'freelist_count')
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

function insertTableSnapshots(database: DatabaseSync, target: MonitoredDatabaseTarget, sampledAt: string, tables: TableStorageSnapshotSummary[]): void {
  const insert = database.prepare(`
    INSERT INTO table_storage_snapshots (
      id, database_role, table_name, sampled_at, row_count, table_bytes, index_bytes, total_bytes,
      page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const table of tables) {
    insert.run(
      newId('tblsnap'),
      target.role,
      table.tableName,
      sampledAt,
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
  return {
    databaseRole: row.database_role,
    tableName: row.table_name,
    sampledAt: row.sampled_at,
    rowCount: optionalNumber(row.row_count),
    tableBytes: optionalNumber(row.table_bytes),
    indexBytes: optionalNumber(row.index_bytes),
    totalBytes: optionalNumber(row.total_bytes),
    pageCount: optionalNumber(row.page_count),
    indexCount: Number(row.index_count ?? 0),
    growthBytes1h: optionalNumber(row.growth_bytes_1h),
    growthRows1h: optionalNumber(row.growth_rows_1h),
    growthBytes24h: optionalNumber(row.growth_bytes_24h),
    growthRows24h: optionalNumber(row.growth_rows_24h)
  }
}

function optionalNumber(value: number | null | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
