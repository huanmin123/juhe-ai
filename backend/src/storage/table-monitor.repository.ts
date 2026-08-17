import { basename } from 'node:path'

import { runtimeConfig } from '../config/runtime.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { getTableMonitorDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { escapeLikePrefix } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export type MonitoredDatabaseRole = 'business' | 'dataset' | 'usage-catalog' | 'stats' | 'codex-context-state'
type SnapshotNumberValue = number | string | null

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

export interface DatabaseStorageHistoryPoint {
  databaseRole: MonitoredDatabaseRole
  sampledAt: string
  fileBytes?: number
  walBytes?: number
  freeBytes?: number
  tableCount?: number
}

export interface TableStorageHistoryPoint {
  sampledAt: string
  rowCount?: number
  totalBytes?: number
}

export interface TableStorageOverviewSummary {
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
  totalBytes?: number
  growthBytes1h?: number
  growthRows1h?: number
  growthBytes24h?: number
  growthRows24h?: number
}

export interface TableStorageOverview {
  sampledAt?: string
  databases: DatabaseStorageSnapshotSummary[]
  tables: TableStorageOverviewSummary[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}

type TableStorageOverviewInput = {
  page?: number
  pageSize?: number
  keyword?: string
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
  free_bytes: SnapshotNumberValue
  table_count: SnapshotNumberValue
}

interface DatabaseStorageHistoryRow {
  database_role: MonitoredDatabaseRole
  sampled_at: string
  file_bytes: SnapshotNumberValue
  wal_bytes: SnapshotNumberValue
  free_bytes: SnapshotNumberValue
  table_count: SnapshotNumberValue
}

interface TableStorageHistoryRow {
  sampled_at: string
  row_count: SnapshotNumberValue
  total_bytes: SnapshotNumberValue
}

const tableMonitorHistoryWindowDays = 30
const defaultTableStorageHistoryLimit = 720
const monitoredDatabaseRoles: MonitoredDatabaseRole[] = ['business', 'dataset', 'usage-catalog', 'stats', 'codex-context-state']
const statsSchemaName = 'juhe_stats'

export function getTableStorageOverview(input: TableStorageOverviewInput = {}): TableStorageOverview {
  const database = getTableMonitorDatabase()
  const databases = (database.prepare(`
    SELECT ${databaseStorageOverviewSelectColumns('snapshots')}
    FROM database_storage_snapshots AS snapshots
    WHERE snapshots.id = (
      SELECT latest.id
      FROM database_storage_snapshots AS latest INDEXED BY idx_database_storage_snapshots_role_time_id
      WHERE latest.database_role = snapshots.database_role
      ORDER BY latest.sampled_at DESC, latest.id DESC
      LIMIT 1
    )
  `).all() as unknown as LatestDatabaseSnapshotRow[])
    .sort(compareDatabaseSnapshotsByRole)
  const sampledAt = latestSnapshotSampledAt(databases, 'database_storage_snapshots.sampled_at')
  const pagination = normalizeOverviewPagination(input)
  const keywordPattern = tableNamePrefixPattern(input.keyword)
  const keywordClause = keywordPattern ? "AND lower(table_name) LIKE lower(?) ESCAPE '\\'" : ''
  const countRow = database.prepare(`
    SELECT COUNT(*) AS total
    FROM (
      SELECT database_role, table_name
      FROM table_storage_snapshots
      WHERE 1 = 1 ${keywordClause}
      GROUP BY database_role, table_name
    )
  `).get(...(keywordPattern ? [keywordPattern] : [])) as { total?: number | string } | undefined
  const tables = database.prepare(`
    WITH table_keys AS (
      SELECT database_role, table_name
      FROM table_storage_snapshots
      WHERE 1 = 1 ${keywordClause}
      GROUP BY database_role, table_name
    ), latest_ids AS (
      SELECT (
        SELECT latest.id
        FROM table_storage_snapshots AS latest INDEXED BY idx_table_storage_snapshots_latest_id
        WHERE latest.database_role = table_keys.database_role
          AND latest.table_name = table_keys.table_name
        ORDER BY latest.sampled_at DESC, latest.id DESC
        LIMIT 1
      ) AS id
      FROM table_keys
    )
    SELECT ${tableStorageOverviewSelectColumns('snapshots')}
    FROM table_storage_snapshots AS snapshots
    WHERE snapshots.id IN (SELECT id FROM latest_ids WHERE id IS NOT NULL)
    ORDER BY snapshots.total_bytes DESC, snapshots.row_count DESC, snapshots.table_name ASC, snapshots.database_role ASC
    LIMIT ? OFFSET ?
  `).all(
    ...(keywordPattern ? [keywordPattern] : []),
    pagination.pageSize,
    pagination.offset
  ) as unknown as LatestTableSnapshotRow[]
  const total = optionalNumber(countRow?.total) ?? 0
  return {
    sampledAt,
    databases: databases.map(databaseOverviewSnapshotFromRow),
    tables: tables.map(tableOverviewSnapshotFromRow),
    page: pagination.page,
    pageSize: pagination.pageSize,
    total,
    hasMore: pagination.offset + tables.length < total
  }
}

export async function getTableStorageOverviewAsync(input: TableStorageOverviewInput = {}): Promise<TableStorageOverview> {
  if (sqliteReadWorkerPoolEnabled()) {
    return await requestSqliteReadWorker({
      type: 'get_table_storage_overview_read_only',
      input
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getTableStorageOverview(input)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const databases = (await client.query<LatestDatabaseSnapshotRow>(`
    SELECT DISTINCT ON (database_role)
      ${databaseStorageOverviewSelectColumns()}
    FROM ${statsTable(client, 'database_storage_snapshots')}
    ORDER BY database_role, sampled_at DESC, id DESC
  `))
    .sort(compareDatabaseSnapshotsByRole)
  const sampledAt = latestSnapshotSampledAt(databases, 'database_storage_snapshots.sampled_at')
  const pagination = normalizeOverviewPagination(input)
  const keywordPattern = tableNamePrefixPattern(input.keyword)
  const keywordClause = keywordPattern ? "WHERE lower(table_name) LIKE lower(?) ESCAPE '\\'" : ''
  const [countRow, tables] = await Promise.all([
    client.one<{ total?: number | string }>(`
      SELECT COUNT(*) AS total
      FROM (
        SELECT database_role, table_name
        FROM ${statsTable(client, 'table_storage_snapshots')}
        ${keywordClause}
        GROUP BY database_role, table_name
      ) AS table_keys
    `, keywordPattern ? [keywordPattern] : []),
    client.query<LatestTableSnapshotRow>(`
      WITH latest_snapshots AS (
        SELECT DISTINCT ON (database_role, table_name)
          id, ${tableStorageOverviewSelectColumns()}
        FROM ${statsTable(client, 'table_storage_snapshots')}
        ${keywordClause}
        ORDER BY database_role, table_name, sampled_at DESC, id DESC
      )
      SELECT ${tableStorageOverviewSelectColumns()}
      FROM latest_snapshots
      ORDER BY total_bytes DESC NULLS LAST, row_count DESC NULLS LAST, table_name ASC, database_role ASC
      LIMIT ? OFFSET ?
    `, [
      ...(keywordPattern ? [keywordPattern] : []),
      pagination.pageSize,
      pagination.offset
    ])
  ])
  const total = optionalNumber(countRow?.total) ?? 0
  return {
    sampledAt,
    databases: databases.map(databaseOverviewSnapshotFromRow),
    tables: tables.map(tableOverviewSnapshotFromRow),
    page: pagination.page,
    pageSize: pagination.pageSize,
    total,
    hasMore: pagination.offset + tables.length < total
  }
}

export function listTableStorageHistory(input: {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}): TableStorageHistoryPoint[] {
  const range = normalizeDateRange(input.startAt, input.endAt)
  const rows = getTableMonitorDatabase()
    .prepare(`
      SELECT ${tableStorageHistorySelectColumns()}
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
    ) as unknown as TableStorageHistoryRow[]
  return rows
    .map(tableHistoryPointFromRow)
    .sort((left, right) => compareSnapshotSampledAt(left.sampledAt, right.sampledAt, 'table_storage_snapshots.sampled_at'))
}

export async function listTableStorageHistoryAsync(input: {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}): Promise<TableStorageHistoryPoint[]> {
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
  const rows = await client.query<TableStorageHistoryRow>(`
    SELECT ${tableStorageHistorySelectColumns()}
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
  return rows
    .map(tableHistoryPointFromRow)
    .sort((left, right) => compareSnapshotSampledAt(left.sampledAt, right.sampledAt, 'table_storage_snapshots.sampled_at'))
}

export function listDatabaseStorageHistory(input: {
  startAt?: string
  endAt?: string
  limit?: number
} = {}): DatabaseStorageHistoryPoint[] {
  const range = normalizeDateRange(input.startAt, input.endAt)
  const database = getTableMonitorDatabase()
  const limit = normalizeLimit(input.limit ?? defaultTableStorageHistoryLimit)
  const statement = database
    .prepare(`
      SELECT ${databaseStorageHistorySelectColumns()}
      FROM database_storage_snapshots
      WHERE database_role = ?
        AND sampled_at >= ?
        AND sampled_at <= ?
      ORDER BY sampled_at DESC, id DESC
      LIMIT ?
    `)
  const rows = monitoredDatabaseRoles.flatMap((databaseRole) => (
    statement.all(databaseRole, range.startAt, range.endAt, limit) as unknown as DatabaseStorageHistoryRow[]
  ))
  return rows
    .sort(compareDatabaseSnapshotsByTimeAsc)
    .map(databaseHistoryPointFromRow)
}

export async function listDatabaseStorageHistoryAsync(input: {
  startAt?: string
  endAt?: string
  limit?: number
} = {}): Promise<DatabaseStorageHistoryPoint[]> {
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
  const rowsByRole = await Promise.all(monitoredDatabaseRoles.map((databaseRole) => client.query<DatabaseStorageHistoryRow>(`
    SELECT ${databaseStorageHistorySelectColumns()}
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
    .map(databaseHistoryPointFromRow)
}

function databaseStorageOverviewSelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return [
    'database_role',
    'database_path',
    'sampled_at',
    'file_bytes',
    'wal_bytes',
    'shm_bytes',
    'free_bytes',
    'table_count'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function databaseStorageHistorySelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return [
    'database_role',
    'sampled_at',
    'file_bytes',
    'wal_bytes',
    'free_bytes',
    'table_count'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function tableStorageOverviewSelectColumns(alias?: string): string {
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
    'growth_bytes_1h',
    'growth_rows_1h',
    'growth_bytes_24h',
    'growth_rows_24h'
  ].map((column) => `${prefix}${column}`).join(', ')
}

function tableStorageHistorySelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return ['sampled_at', 'row_count', 'total_bytes']
    .map((column) => `${prefix}${column}`).join(', ')
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

function compareDatabaseSnapshotsByRole(
  left: Pick<LatestDatabaseSnapshotRow, 'database_role'>,
  right: Pick<LatestDatabaseSnapshotRow, 'database_role'>
): number {
  return databaseRoleSortRank(left.database_role) - databaseRoleSortRank(right.database_role)
}

function compareDatabaseSnapshotsByTimeAsc(
  left: Pick<LatestDatabaseSnapshotRow, 'database_role' | 'sampled_at'>,
  right: Pick<LatestDatabaseSnapshotRow, 'database_role' | 'sampled_at'>
): number {
  const sampledAt = compareSnapshotSampledAt(left.sampled_at, right.sampled_at, 'database_storage_snapshots.sampled_at')
  return sampledAt !== 0 ? sampledAt : compareDatabaseSnapshotsByRole(left, right)
}

function databaseRoleSortRank(databaseRole: MonitoredDatabaseRole): number {
  const index = monitoredDatabaseRoles.indexOf(databaseRole)
  return index >= 0 ? index : monitoredDatabaseRoles.length
}

function normalizeLimit(value: number): number {
  return Number.isFinite(value) ? Math.min(Math.max(Math.trunc(value), 1), 10000) : 200
}

function normalizeOverviewPagination(input: TableStorageOverviewInput): { page: number; pageSize: number; offset: number } {
  const page = Number.isFinite(input.page) ? Math.max(1, Math.trunc(input.page ?? 1)) : 1
  const pageSize = Number.isFinite(input.pageSize) ? Math.min(100, Math.max(1, Math.trunc(input.pageSize ?? 10))) : 10
  return { page, pageSize, offset: (page - 1) * pageSize }
}

function tableNamePrefixPattern(keyword: string | undefined): string | undefined {
  const normalized = keyword?.trim()
  return normalized ? `${escapeLikePrefix(normalized)}%` : undefined
}

function normalizeDateTime(value: string | undefined, fallback: string): string {
  return value === undefined ? fallback : requiredRfc3339Instant(value, '表监控时间范围')
}

function normalizeDateRange(startAt?: string, endAt?: string): { startAt: string; endAt: string } {
  const defaultEndAt = nowIso()
  const defaultEndAtMilliseconds = rfc3339InstantMilliseconds(defaultEndAt)
  if (defaultEndAtMilliseconds === undefined) {
    throw new Error('表监控默认结束时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  const defaultStartAt = new Date(defaultEndAtMilliseconds - tableMonitorHistoryWindowDays * 24 * 60 * 60 * 1000).toISOString()
  const normalizedStartAt = normalizeDateTime(startAt, defaultStartAt)
  const normalizedEndAt = normalizeDateTime(endAt, defaultEndAt)
  const normalizedStartAtMilliseconds = rfc3339InstantMilliseconds(normalizedStartAt)
  const normalizedEndAtMilliseconds = rfc3339InstantMilliseconds(normalizedEndAt)
  if (normalizedStartAtMilliseconds === undefined || normalizedEndAtMilliseconds === undefined) {
    throw new Error('表监控时间范围必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return normalizedStartAtMilliseconds <= normalizedEndAtMilliseconds
    ? { startAt: normalizedStartAt, endAt: normalizedEndAt }
    : { startAt: normalizedEndAt, endAt: normalizedStartAt }
}

function latestSnapshotSampledAt(rows: Array<Pick<LatestDatabaseSnapshotRow, 'sampled_at'>>, label: string): string | undefined {
  let latest: string | undefined
  for (const row of rows) {
    const sampledAt = requiredRfc3339Instant(row.sampled_at, label)
    if (latest === undefined || compareSnapshotSampledAt(sampledAt, latest, label) > 0) latest = sampledAt
  }
  return latest
}

function compareSnapshotSampledAt(left: unknown, right: unknown, label: string): number {
  const leftMilliseconds = rfc3339InstantMilliseconds(requiredRfc3339Instant(left, label))
  const rightMilliseconds = rfc3339InstantMilliseconds(requiredRfc3339Instant(right, label))
  if (leftMilliseconds === undefined || rightMilliseconds === undefined) {
    throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  }
  return leftMilliseconds === rightMilliseconds ? 0 : leftMilliseconds > rightMilliseconds ? 1 : -1
}

function databaseHistoryPointFromRow(row: DatabaseStorageHistoryRow): DatabaseStorageHistoryPoint {
  return {
    databaseRole: row.database_role,
    sampledAt: requiredRfc3339Instant(row.sampled_at, 'database_storage_snapshots.sampled_at'),
    fileBytes: optionalNumber(row.file_bytes),
    walBytes: optionalNumber(row.wal_bytes),
    freeBytes: optionalNumber(row.free_bytes),
    tableCount: optionalNumber(row.table_count)
  }
}

function databaseOverviewSnapshotFromRow(row: LatestDatabaseSnapshotRow): DatabaseStorageSnapshotSummary {
  return {
    databaseRole: row.database_role,
    databasePath: basename(row.database_path),
    sampledAt: requiredRfc3339Instant(row.sampled_at, 'database_storage_snapshots.sampled_at'),
    fileBytes: optionalNumber(row.file_bytes),
    walBytes: optionalNumber(row.wal_bytes),
    shmBytes: optionalNumber(row.shm_bytes),
    freeBytes: optionalNumber(row.free_bytes),
    tableCount: optionalNumber(row.table_count)
  }
}

function tableOverviewSnapshotFromRow(row: LatestTableSnapshotRow): TableStorageOverviewSummary {
  const tableBytes = optionalNumber(row.table_bytes)
  const indexBytes = optionalNumber(row.index_bytes)
  return {
    databaseRole: row.database_role,
    tableName: row.table_name,
    sampledAt: requiredRfc3339Instant(row.sampled_at, 'table_storage_snapshots.sampled_at'),
    tableKind: row.table_kind ?? undefined,
    parentTableName: row.parent_table_name ?? undefined,
    isPartition: booleanFromSnapshot(row.is_partition),
    isArchive: booleanFromSnapshot(row.is_archive),
    rowCount: optionalNumber(row.row_count),
    tableBytes,
    indexBytes,
    indexToTableRatio: ratio(indexBytes, tableBytes),
    totalBytes: optionalNumber(row.total_bytes),
    growthBytes1h: optionalNumber(row.growth_bytes_1h),
    growthRows1h: optionalNumber(row.growth_rows_1h),
    growthBytes24h: optionalNumber(row.growth_bytes_24h),
    growthRows24h: optionalNumber(row.growth_rows_24h)
  }
}

function tableHistoryPointFromRow(row: TableStorageHistoryRow): TableStorageHistoryPoint {
  return {
    sampledAt: requiredRfc3339Instant(row.sampled_at, 'table_storage_snapshots.sampled_at'),
    rowCount: optionalNumber(row.row_count),
    totalBytes: optionalNumber(row.total_bytes)
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
