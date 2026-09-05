import { createHash } from 'node:crypto'
import { pathToFileURL } from 'node:url'

import pg from 'pg'

interface ColumnContract {
  columnName: string
  dataType: string
  udtName: string
}

type KnownBusinessTypeEvolution = 'integer_boolean' | 'text_timestamptz'
type Row = Record<string, unknown>

export interface TableComparison {
  table: string
  sourceRows: number
  targetRows: number
  addedRows: number
  missingRows: number
  modifiedRows: number
  sourceDigest: string
  preservedDigest: string
  primaryKey: string[]
}

interface VerificationReport {
  sourceTableCount: number
  targetNewTables: string[]
  allowedNewTables: string[]
  allowedAdditionTables: string[]
  comparisons: TableComparison[]
  violations: string[]
}

export interface DatabaseIdentity {
  database_name: string
  database_oid: string
  server_address: string | null
  server_port: number | null
}

export interface ManagedClient {
  connect(): Promise<unknown>
  end(): Promise<void>
}

const businessSchema = 'juhe_business'

export function stableJson(value: unknown): string {
  if (value === null || value === undefined) return JSON.stringify(value ?? null)
  if (typeof value === 'bigint') return JSON.stringify(value.toString())
  if (typeof value === 'number' && !Number.isFinite(value)) {
    return stableJson({ $number: String(value) })
  }
  if (value instanceof Date) return stableJson({ $date: value.toISOString() })
  if (Buffer.isBuffer(value)) return stableJson({ $buffer: value.toString('base64') })
  if (Array.isArray(value)) return `[${value.map((item) => stableJson(item)).join(',')}]`
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>
    const properties = Object.keys(record)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableJson(record[key])}`)
    return `{${properties.join(',')}}`
  }
  return JSON.stringify(value)
}

export function rowDigest(row: Row): string {
  return createHash('sha256').update(stableJson(row)).digest('hex')
}

export function compareKeyedRows(
  sourceRows: Row[],
  targetRows: Row[],
  primaryKey: string[]
): Omit<TableComparison, 'table' | 'primaryKey'> {
  if (primaryKey.length === 0) return compareUnkeyedRows(sourceRows, targetRows)

  const sourceByKey = keyedRows(sourceRows, primaryKey)
  const targetByKey = keyedRows(targetRows, primaryKey)
  let missingRows = 0
  let modifiedRows = 0
  const sourceHashes: string[] = []
  const preservedHashes: string[] = []

  for (const [key, sourceRow] of sourceByKey) {
    const sourceHash = rowDigest(sourceRow)
    sourceHashes.push(sourceHash)
    const targetRow = targetByKey.get(key)
    if (!targetRow) {
      missingRows += 1
      continue
    }
    const targetHash = rowDigest(targetRow)
    if (sourceHash !== targetHash) modifiedRows += 1
    preservedHashes.push(targetHash)
  }

  return {
    sourceRows: sourceRows.length,
    targetRows: targetRows.length,
    addedRows: Math.max(0, targetRows.length - sourceRows.length + missingRows),
    missingRows,
    modifiedRows,
    sourceDigest: digestHashList(sourceHashes),
    preservedDigest: digestHashList(preservedHashes)
  }
}

export function classifyTargetNewTables(
  sourceTables: string[],
  targetTables: string[],
  allowedNewTables: Set<string>
): Pick<VerificationReport, 'targetNewTables' | 'allowedNewTables' | 'violations'> {
  const sourceTableSet = new Set(sourceTables)
  const targetNewTables = targetTables.filter((table) => !sourceTableSet.has(table))
  const targetNewTableSet = new Set(targetNewTables)
  const violations: string[] = []
  for (const table of targetNewTables) {
    if (!allowedNewTables.has(table)) violations.push(`${table}: unapproved target business table`)
  }
  for (const table of allowedNewTables) {
    if (!targetNewTableSet.has(table)) violations.push(`${table}: configured allowed new table does not exist`)
  }
  return {
    targetNewTables,
    allowedNewTables: [...allowedNewTables].filter((table) => targetNewTableSet.has(table)).sort(),
    violations
  }
}

export function normalizeKnownBusinessTypeEvolutionValue(
  evolution: KnownBusinessTypeEvolution,
  value: unknown,
  side: 'source' | 'target'
): unknown {
  if (value === null || value === undefined) return null
  if (evolution === 'integer_boolean') {
    if (side === 'target') {
      if (typeof value !== 'boolean') throw new Error('业务表布尔类型升级后的目标值不是 boolean')
      return value
    }
    if (value === 0 || value === 0n) return false
    if (value === 1 || value === 1n) return true
    throw new Error('业务表布尔类型升级前的源值不是 0/1')
  }
  const parsed = value instanceof Date ? value : new Date(String(value))
  if (Number.isNaN(parsed.getTime())) throw new Error('业务表时间类型升级值无法转换为 timestamptz')
  return parsed.toISOString()
}

async function main(): Promise<void> {
  const sourceUrl = requiredSecretEnv('JUHE_AI_BUSINESS_VERIFY_SOURCE_POSTGRES_URL')
  const targetUrl = requiredSecretEnv('JUHE_AI_BUSINESS_VERIFY_TARGET_POSTGRES_URL')
  assertDistinctConnectionTargets(sourceUrl, targetUrl)

  const allowedAdditionTables = parseTableSet('JUHE_AI_BUSINESS_VERIFY_ALLOWED_ADDITION_TABLES')
  const allowedNewTables = parseTableSet('JUHE_AI_BUSINESS_VERIFY_ALLOWED_NEW_TABLES')
  const source = new pg.Client({
    connectionString: sourceUrl,
    application_name: 'juhe-ai-business-preservation-source',
    connectionTimeoutMillis: 10_000
  })
  const target = new pg.Client({
    connectionString: targetUrl,
    application_name: 'juhe-ai-business-preservation-target',
    connectionTimeoutMillis: 10_000
  })

  await withConnectedClients([source, target], async () => {
    try {
      await assertDistinctDatabaseIdentities(source, target)
      await Promise.all([beginReadOnlySnapshot(source), beginReadOnlySnapshot(target)])
      const report = await verifyBusinessData(source, target, allowedNewTables, allowedAdditionTables)
      process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
      if (report.violations.length > 0) process.exitCode = 1
      await Promise.all([source.query('COMMIT'), target.query('COMMIT')])
    } catch (error) {
      await Promise.allSettled([source.query('ROLLBACK'), target.query('ROLLBACK')])
      throw error
    }
  })
}

export async function withConnectedClients<T>(
  clients: readonly ManagedClient[],
  operation: () => Promise<T>
): Promise<T> {
  try {
    const connectionResults = await Promise.allSettled(clients.map((client) => client.connect()))
    const connectionErrors = connectionResults.flatMap((result) => result.status === 'rejected' ? [result.reason] : [])
    if (connectionErrors.length > 0) {
      throw new AggregateError(connectionErrors, 'PostgreSQL 对账连接未全部建立')
    }
    return await operation()
  } finally {
    await Promise.allSettled(clients.map((client) => client.end()))
  }
}

async function verifyBusinessData(
  source: pg.Client,
  target: pg.Client,
  allowedNewTables: Set<string>,
  allowedAdditionTables: Set<string>
): Promise<VerificationReport> {
  const [sourceTables, targetTables] = await Promise.all([listTables(source), listTables(target)])
  assertBusinessSchemaPresent('source', sourceTables)
  assertBusinessSchemaPresent('target', targetTables)
  const targetTableSet = new Set(targetTables)
  const violations: string[] = []
  const comparisons: TableComparison[] = []

  for (const table of sourceTables) {
    if (!targetTableSet.has(table)) {
      violations.push(`${table}: target table missing`)
      continue
    }
    const [sourceColumns, targetColumns, sourcePrimaryKey, targetPrimaryKey] = await Promise.all([
      listColumns(source, table),
      listColumns(target, table),
      listPrimaryKey(source, table),
      listPrimaryKey(target, table)
    ])
    assertSourceColumnsPreserved(table, sourceColumns, targetColumns, violations)
    if (stableJson(sourcePrimaryKey) !== stableJson(targetPrimaryKey)) {
      violations.push(`${table}: target primary key changed`)
    }

    const selectedColumns = sourceColumns.map((column) => column.columnName)
    const [sourceRows, targetRows] = await Promise.all([
      readRows(source, table, selectedColumns, sourcePrimaryKey),
      readRows(target, table, selectedColumns, sourcePrimaryKey)
    ])
    const normalizedSourceRows = normalizeRowsForKnownBusinessTypeEvolutions(
      table, sourceRows, sourceColumns, targetColumns, 'source'
    )
    const normalizedTargetRows = normalizeRowsForKnownBusinessTypeEvolutions(
      table, targetRows, sourceColumns, targetColumns, 'target'
    )
    const result: TableComparison = {
      table,
      primaryKey: sourcePrimaryKey,
      ...compareKeyedRows(normalizedSourceRows, normalizedTargetRows, sourcePrimaryKey)
    }
    comparisons.push(result)
    if (result.missingRows > 0) violations.push(`${table}: missing rows=${result.missingRows}`)
    if (result.modifiedRows > 0) violations.push(`${table}: modified rows=${result.modifiedRows}`)
    if (result.addedRows > 0 && !allowedAdditionTables.has(table)) {
      violations.push(`${table}: unapproved target additions=${result.addedRows}`)
    }
  }

  const newTableClassification = classifyTargetNewTables(sourceTables, targetTables, allowedNewTables)
  violations.push(...newTableClassification.violations)
  return {
    sourceTableCount: sourceTables.length,
    targetNewTables: newTableClassification.targetNewTables,
    allowedNewTables: newTableClassification.allowedNewTables,
    allowedAdditionTables: [...allowedAdditionTables].sort(),
    comparisons,
    violations
  }
}

export function assertBusinessSchemaPresent(side: 'source' | 'target', tables: string[]): void {
  if (!tables.includes('system_accounts')) {
    throw new Error(`${side} ${businessSchema} 缺少 system_accounts 基准表，拒绝对账`)
  }
}

async function beginReadOnlySnapshot(client: pg.Client): Promise<void> {
  await client.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
  await client.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)")
}

async function listTables(client: pg.Client): Promise<string[]> {
  const result = await client.query<{ table_name: string }>(`
    SELECT table_name
    FROM information_schema.tables
    WHERE table_schema = $1 AND table_type = 'BASE TABLE'
    ORDER BY table_name
  `, [businessSchema])
  return result.rows.map((row) => row.table_name)
}

async function listColumns(client: pg.Client, table: string): Promise<ColumnContract[]> {
  const result = await client.query<{ column_name: string; data_type: string; udt_name: string }>(`
    SELECT column_name, data_type, udt_name
    FROM information_schema.columns
    WHERE table_schema = $1 AND table_name = $2
    ORDER BY ordinal_position
  `, [businessSchema, table])
  return result.rows.map((row) => ({
    columnName: row.column_name,
    dataType: row.data_type,
    udtName: row.udt_name
  }))
}

async function listPrimaryKey(client: pg.Client, table: string): Promise<string[]> {
  const result = await client.query<{ column_name: string }>(`
    SELECT attribute.attname AS column_name
    FROM pg_index index_catalog
    JOIN pg_class relation ON relation.oid = index_catalog.indrelid
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    JOIN LATERAL unnest(index_catalog.indkey) WITH ORDINALITY AS key_columns(attnum, ordinal_position) ON TRUE
    JOIN pg_attribute attribute ON attribute.attrelid = relation.oid AND attribute.attnum = key_columns.attnum
    WHERE namespace.nspname = $1 AND relation.relname = $2 AND index_catalog.indisprimary
    ORDER BY key_columns.ordinal_position
  `, [businessSchema, table])
  return result.rows.map((row) => row.column_name)
}

async function readRows(
  client: pg.Client,
  table: string,
  columns: string[],
  primaryKey: string[]
): Promise<Row[]> {
  const selected = columns.map(quoteIdentifier).join(', ')
  const orderBy = primaryKey.length > 0 ? ` ORDER BY ${primaryKey.map(quoteIdentifier).join(', ')}` : ''
  const result = await client.query<Row>(
    `SELECT ${selected} FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)}${orderBy}`
  )
  return result.rows
}

function assertSourceColumnsPreserved(
  table: string,
  sourceColumns: ColumnContract[],
  targetColumns: ColumnContract[],
  violations: string[]
): void {
  const targetByName = new Map(targetColumns.map((column) => [column.columnName, column]))
  for (const sourceColumn of sourceColumns) {
    const targetColumn = targetByName.get(sourceColumn.columnName)
    if (!targetColumn) {
      violations.push(`${table}.${sourceColumn.columnName}: target column missing`)
      continue
    }
    if (
      (sourceColumn.dataType !== targetColumn.dataType || sourceColumn.udtName !== targetColumn.udtName)
      && !knownBusinessTypeEvolution(table, sourceColumn, targetColumn)
    ) {
      violations.push(`${table}.${sourceColumn.columnName}: target type changed`)
    }
  }
}

function normalizeRowsForKnownBusinessTypeEvolutions(
  table: string,
  rows: Row[],
  sourceColumns: ColumnContract[],
  targetColumns: ColumnContract[],
  side: 'source' | 'target'
): Row[] {
  const targetByName = new Map(targetColumns.map((column) => [column.columnName, column]))
  const evolutions = sourceColumns.flatMap((sourceColumn) => {
    const targetColumn = targetByName.get(sourceColumn.columnName)
    if (!targetColumn) return []
    const evolution = knownBusinessTypeEvolution(table, sourceColumn, targetColumn)
    return evolution ? [{ columnName: sourceColumn.columnName, evolution }] : []
  })
  if (evolutions.length === 0) return rows
  return rows.map((row) => {
    const normalized = { ...row }
    for (const { columnName, evolution } of evolutions) {
      normalized[columnName] = normalizeKnownBusinessTypeEvolutionValue(evolution, normalized[columnName], side)
    }
    return normalized
  })
}

function knownBusinessTypeEvolution(
  table: string,
  sourceColumn: ColumnContract,
  targetColumn: ColumnContract
): KnownBusinessTypeEvolution | undefined {
  if (table !== 'proxy_profiles') return undefined
  if (
    sourceColumn.columnName === 'enabled'
    && sourceColumn.dataType === 'integer'
    && targetColumn.dataType === 'boolean'
  ) return 'integer_boolean'
  if (
    ['last_tested_at', 'created_at', 'updated_at'].includes(sourceColumn.columnName)
    && sourceColumn.dataType === 'text'
    && targetColumn.dataType === 'timestamp with time zone'
  ) return 'text_timestamptz'
  return undefined
}

function compareUnkeyedRows(
  sourceRows: Row[],
  targetRows: Row[]
): Omit<TableComparison, 'table' | 'primaryKey'> {
  const sourceCounts = digestCounts(sourceRows)
  const targetCounts = digestCounts(targetRows)
  let missingRows = 0
  for (const [digest, count] of sourceCounts) {
    missingRows += Math.max(0, count - (targetCounts.get(digest) ?? 0))
  }
  return {
    sourceRows: sourceRows.length,
    targetRows: targetRows.length,
    addedRows: Math.max(0, targetRows.length - sourceRows.length + missingRows),
    missingRows,
    modifiedRows: 0,
    sourceDigest: digestHashList([...sourceCounts.entries()].flatMap(([digest, count]) => Array(count).fill(digest))),
    preservedDigest: digestHashList(
      [...targetCounts.entries()].flatMap(([digest, count]) => Array(Math.min(count, sourceCounts.get(digest) ?? 0)).fill(digest))
    )
  }
}

function keyedRows(rows: Row[], primaryKey: string[]): Map<string, Row> {
  const result = new Map<string, Row>()
  for (const row of rows) {
    const key = stableJson(primaryKey.map((column) => row[column]))
    if (result.has(key)) throw new Error('业务表主键结果重复，拒绝继续对账')
    result.set(key, row)
  }
  return result
}

function digestCounts(rows: Row[]): Map<string, number> {
  const counts = new Map<string, number>()
  for (const row of rows) {
    const digest = rowDigest(row)
    counts.set(digest, (counts.get(digest) ?? 0) + 1)
  }
  return counts
}

function digestHashList(hashes: string[]): string {
  const hash = createHash('sha256')
  for (const value of [...hashes].sort()) hash.update(value)
  return hash.digest('hex')
}

function parseTableSet(name: string): Set<string> {
  return new Set(
    (process.env[name] ?? '')
      .split(',')
      .map((value) => value.trim())
      .filter(Boolean)
  )
}

function requiredSecretEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 未配置`)
  return value
}

function assertDistinctConnectionTargets(sourceUrl: string, targetUrl: string): void {
  if (sourceUrl === targetUrl) throw new Error('源库和目标库连接完全相同，拒绝执行无效对账')
}

export function databaseIdentitiesMatch(source: DatabaseIdentity, target: DatabaseIdentity): boolean {
  return source.database_name === target.database_name
    && source.database_oid === target.database_oid
}

async function assertDistinctDatabaseIdentities(source: pg.Client, target: pg.Client): Promise<void> {
  const identitySql = `
    SELECT current_database() AS database_name,
           (SELECT oid::text FROM pg_database WHERE datname = current_database()) AS database_oid,
           inet_server_addr()::text AS server_address,
           inet_server_port() AS server_port
  `
  const [sourceResult, targetResult] = await Promise.all([
    source.query<DatabaseIdentity>(identitySql),
    target.query<DatabaseIdentity>(identitySql)
  ])
  const sourceIdentity = sourceResult.rows[0]
  const targetIdentity = targetResult.rows[0]
  if (!sourceIdentity || !targetIdentity) throw new Error('无法读取 PostgreSQL 数据库身份')
  if (databaseIdentitiesMatch(sourceIdentity, targetIdentity)) {
    throw new Error('源库和目标库解析为同一 PostgreSQL 数据库，拒绝执行无效对账')
  }
}

function quoteIdentifier(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

const entryPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (entryPath === import.meta.url) {
  void main().catch((error: unknown) => {
    const message = error instanceof Error ? error.message : String(error)
    console.error(`业务数据保留对账失败：${message}`)
    process.exitCode = 1
  })
}
