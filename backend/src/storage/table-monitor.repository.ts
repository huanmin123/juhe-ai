import { existsSync, statSync } from 'node:fs'
import { basename } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { sqlPlaceholders } from './query-utils.js'

export type MonitoredDatabaseRole = 'business' | 'records'

export interface TableStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  sampledAt: string
  rowCount?: number
  tableBytes: number
  indexBytes: number
  totalBytes: number
  pageCount: number
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

interface MonitoredDatabaseTarget {
  role: MonitoredDatabaseRole
  path: string
  database: DatabaseSync
}

interface ObjectSizeRow {
  name: string
  bytes: number
  page_count: number
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
  table_bytes: number
  index_bytes: number
  total_bytes: number
  page_count: number
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

export function collectTableStorageSnapshot(sampledAt = nowIso()): void {
  const recordDatabase = getRecordDatabase()
  const targets: MonitoredDatabaseTarget[] = [
    { role: 'business', path: runtimeConfig.databasePath, database: getBusinessDatabase() },
    { role: 'records', path: runtimeConfig.recordDatabasePath, database: recordDatabase }
  ]
  const transactionStarted = beginDatabaseTransaction(recordDatabase)
  try {
    for (const target of targets) {
      const tableRows = collectTargetTableRows(target, sampledAt)
      insertDatabaseSnapshot(recordDatabase, target, sampledAt, tableRows)
      insertTableSnapshots(recordDatabase, target, sampledAt, tableRows)
    }
    cleanupOldTableStorageSnapshots(recordDatabase, sampledAt)
    commitDatabaseTransaction(recordDatabase, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(recordDatabase, transactionStarted)
    throw error
  }
}

export function getTableStorageOverview(input: { startAt?: string; endAt?: string; limit?: number } = {}): TableStorageOverview {
  const database = getRecordDatabase()
  const range = normalizeDateRange(input.startAt, input.endAt)
  const databases = database
    .prepare(`
      SELECT d.*
      FROM database_storage_snapshots d
      INNER JOIN (
        SELECT database_role, MAX(sampled_at) AS sampled_at
        FROM database_storage_snapshots
        WHERE sampled_at >= ?
          AND sampled_at <= ?
        GROUP BY database_role
      ) latest
        ON latest.database_role = d.database_role
        AND latest.sampled_at = d.sampled_at
      ORDER BY d.database_role ASC
    `)
    .all(range.startAt, range.endAt) as unknown as LatestDatabaseSnapshotRow[]
  const sampledAt = databases.map((row) => row.sampled_at).sort().at(-1)
  const tables = database
    .prepare(`
      WITH latest_database_samples AS (
        SELECT database_role, MAX(sampled_at) AS sampled_at
        FROM database_storage_snapshots
        WHERE sampled_at >= ?
          AND sampled_at <= ?
        GROUP BY database_role
      )
      SELECT t.*
      FROM table_storage_snapshots t
      INNER JOIN latest_database_samples latest
        ON latest.database_role = t.database_role
        AND latest.sampled_at = t.sampled_at
      ORDER BY t.total_bytes DESC, t.row_count DESC, t.table_name ASC
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
  const rows = getRecordDatabase()
    .prepare(`
      SELECT *
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
      normalizeLimit(input.limit ?? 10000)
    ) as unknown as LatestTableSnapshotRow[]
  return rows.reverse().map(tableSnapshotFromRow)
}

export function cleanupTableStorageSnapshotsBefore(cutoffIso: string, limit = 10000): number {
  const database = getRecordDatabase()
  return deleteSnapshotRowsById(database, 'table_storage_snapshots', cutoffIso, limit)
    + deleteSnapshotRowsById(database, 'database_storage_snapshots', cutoffIso, limit)
}

function collectTargetTableRows(target: MonitoredDatabaseTarget, sampledAt: string): TableStorageSnapshotSummary[] {
  const tables = listTargetTables(target.database)
  const dbstatSizes = loadDbstatObjectSizes(target.database)
  const indexesByTable = listIndexesByTable(target.database)
  return tables.map((tableName) => {
    const tableSize = dbstatSizes.get(tableName)
    const indexNames = indexesByTable.get(tableName) ?? []
    const indexSizes = indexNames.map((indexName) => dbstatSizes.get(indexName)).filter((row): row is ObjectSizeRow => Boolean(row))
    const tableBytes = Number(tableSize?.bytes ?? 0)
    const tablePages = Number(tableSize?.page_count ?? 0)
    const indexBytes = indexSizes.reduce((sum, row) => sum + Number(row.bytes ?? 0), 0)
    const indexPages = indexSizes.reduce((sum, row) => sum + Number(row.page_count ?? 0), 0)
    const totalBytes = tableBytes + indexBytes
    const rowCount = countRows(target.database, tableName)
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
      pageCount: tablePages + indexPages,
      indexCount: indexNames.length,
      growthBytes1h: previous1h ? totalBytes - previous1h.total_bytes : undefined,
      growthRows1h: previous1h && rowCount !== undefined && previous1h.row_count !== null ? rowCount - previous1h.row_count : undefined,
      growthBytes24h: previous24h ? totalBytes - previous24h.total_bytes : undefined,
      growthRows24h: previous24h && rowCount !== undefined && previous24h.row_count !== null ? rowCount - previous24h.row_count : undefined
    }
  })
}

function insertDatabaseSnapshot(database: DatabaseSync, target: MonitoredDatabaseTarget, sampledAt: string, tables: TableStorageSnapshotSummary[]): void {
  const pageSize = pragmaNumber(target.database, 'page_size')
  const pageCount = pragmaNumber(target.database, 'page_count')
  const freelistCount = pragmaNumber(target.database, 'freelist_count')
  const fileBytes = fileSize(target.path)
  const walBytes = fileSize(`${target.path}-wal`)
  const shmBytes = fileSize(`${target.path}-shm`)
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
    walBytes ?? null,
    shmBytes ?? null,
    pageSize ?? null,
    pageCount ?? null,
    freelistCount ?? null,
    usedBytes ?? null,
    freeBytes ?? null,
    tables.length,
    tables.reduce((sum, row) => sum + row.indexCount, 0),
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
      table.tableBytes,
      table.indexBytes,
      table.totalBytes,
      table.pageCount,
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

function loadDbstatObjectSizes(database: DatabaseSync): Map<string, ObjectSizeRow> {
  try {
    const rows = database
      .prepare(`
        SELECT name, SUM(pgsize) AS bytes, COUNT(*) AS page_count
        FROM dbstat
        GROUP BY name
      `)
      .all() as unknown as ObjectSizeRow[]
    return new Map(rows.map((row) => [row.name, row]))
  } catch {
    return new Map()
  }
}

function countRows(database: DatabaseSync, tableName: string): number | undefined {
  if (isFtsShadowTable(tableName)) {
    return undefined
  }
  try {
    const row = database.prepare(`SELECT COUNT(*) AS count FROM "${escapeSqlIdentifier(tableName)}"`).get() as unknown as { count?: number } | undefined
    return Number(row?.count ?? 0)
  } catch {
    return undefined
  }
}

function isFtsShadowTable(tableName: string): boolean {
  return /_(data|idx|content|docsize|config)$/.test(tableName)
}

function findPreviousTableSnapshot(databaseRole: MonitoredDatabaseRole, tableName: string, sampledAt: string, minutesBack: number): LatestTableSnapshotRow | undefined {
  const targetTime = new Date(Date.parse(sampledAt) - minutesBack * 60 * 1000).toISOString()
  return getRecordDatabase()
    .prepare(`
      SELECT *
      FROM table_storage_snapshots
      WHERE database_role = ?
        AND table_name = ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC
      LIMIT 1
    `)
    .get(databaseRole, tableName, targetTime) as unknown as LatestTableSnapshotRow | undefined
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

function fileSize(path: string): number | undefined {
  try {
    return existsSync(path) ? statSync(path).size : 0
  } catch {
    return undefined
  }
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

function escapeSqlIdentifier(value: string): string {
  return value.replace(/"/g, '""')
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
    tableBytes: Number(row.table_bytes ?? 0),
    indexBytes: Number(row.index_bytes ?? 0),
    totalBytes: Number(row.total_bytes ?? 0),
    pageCount: Number(row.page_count ?? 0),
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
