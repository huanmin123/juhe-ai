import { existsSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { applyPostgresSchema, collectPostgresSchemaStatements, type PostgresSchemaName } from '../../storage/postgres-schema.js'
import type { PostgresPoolClient, PostgresQueryClient } from '../../storage/postgres-client.js'

interface MigrationOptions {
  confirmOffline: boolean
  dryRun: boolean
  skipSchema: boolean
  allowNonEmptyTarget: boolean
  skipUnsupportedTables: boolean
  includeUsageShards: boolean
  includeCodexContextState: boolean
  batchSize: number
  postgresUrl: string
  sourceBusinessPath: string
  sourceDatasetPath: string
  sourceUsageCatalogPath: string
  sourceStatsPath: string
  sourceUsageShardRoot: string
  sourceCodexContextStateShardRoot: string
  sourceCodexContextStateShardCount: number
}

interface SourceDatabasePlan {
  id: string
  schemaName: PostgresSchemaName
  path: string
  required: boolean
  tableNames?: readonly string[]
}

interface TableMigrationSummary {
  sourceId: string
  schemaName: PostgresSchemaName
  tableName: string
  rows: number
  batches: number
  dryRun: boolean
}

type PostgresMigrationPool = PostgresQueryClient & {
  connect(): Promise<PostgresPoolClient>
  end(): Promise<void>
}

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const defaultDataRoot = resolve(backendRoot, 'data')
const defaultBusinessPath = resolve(defaultDataRoot, 'juhe-ai.sqlite3')
const defaultDatasetPath = resolve(defaultDataRoot, 'juhe-ai-dataset.sqlite3')
const defaultUsageCatalogPath = resolve(defaultDataRoot, 'juhe-ai-usage-catalog.sqlite3')
const defaultStatsPath = resolve(defaultDataRoot, 'juhe-ai-stats.sqlite3')
const defaultCodexContextStateShardRoot = resolve(defaultDataRoot, 'codex-context', 'state-shards')

async function main(): Promise<void> {
  const options = parseOptions(process.argv.slice(2))
  if (!options.confirmOffline && process.env.JUHE_AI_CONFIRM_SQLITE_TO_POSTGRES_MIGRATION !== '1') {
    throw new Error('SQLite -> PostgreSQL 迁移必须显式确认停服/离线执行：追加 --confirm-offline，或设置 JUHE_AI_CONFIRM_SQLITE_TO_POSTGRES_MIGRATION=1')
  }
  if (!options.postgresUrl) {
    throw new Error('SQLite -> PostgreSQL 迁移必须配置 JUHE_AI_POSTGRES_URL 或 --postgres-url')
  }

  const startedAt = Date.now()
  const pool = await createPool(options.postgresUrl)
  const postgresClient = createPostgresDatabaseClient(pool)
  try {
    if (!options.dryRun && !options.skipSchema) {
      const schemaResult = await applyPostgresSchema(postgresClient)
      console.log(`PostgreSQL schema 已确认：schemas=${schemaResult.schemaCount}, schemaStatements=${schemaResult.statementCount}`)
    }

    const sources = collectSourceDatabasePlans(options)
    const tableOrderBySchema = collectPostgresTableOrderBySchema()
    const targetTables = new Set<string>()
    for (const source of sources) {
      for (const tableName of sourceTableNames(source, tableOrderBySchema)) {
        targetTables.add(targetTableKey(source.schemaName, tableName))
      }
    }
    await assertTargetTablesEmpty(pool, targetTables, options)

    const summaries: TableMigrationSummary[] = []
    for (const source of sources) {
      summaries.push(...await migrateSourceDatabase(pool, source, tableOrderBySchema, options))
    }

    const rows = summaries.reduce((total, summary) => total + summary.rows, 0)
    const batches = summaries.reduce((total, summary) => total + summary.batches, 0)
    const migratedTables = summaries.filter((summary) => summary.rows > 0 || !summary.dryRun).length
    const action = options.dryRun ? '迁移预检查完成' : '迁移完成'
    console.log(`${action}：sources=${sources.length}, tables=${migratedTables}, rows=${rows}, batches=${batches}, durationMs=${Date.now() - startedAt}`)
    console.log('后续建议：确认 System API / 网关 smoke 后，再按当前 PG 统计窗口重建流程刷新派生统计。')
  } finally {
    await pool.end()
  }
}

function parseOptions(args: string[]): MigrationOptions {
  const sourceBusinessPath = pathOption(args, '--source-business=', 'JUHE_AI_DATABASE_PATH', defaultBusinessPath)
  const sourceDatasetPath = pathOption(args, '--source-dataset=', 'JUHE_AI_DATASET_DATABASE_PATH', defaultDatasetPath)
  const sourceUsageCatalogPath = pathOption(
    args,
    '--source-usage-catalog=',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
    sourceDatasetPath !== defaultDatasetPath ? resolve(dirname(sourceDatasetPath), 'usage-catalog.sqlite3') : defaultUsageCatalogPath
  )
  const sourceStatsPath = pathOption(args, '--source-stats=', 'JUHE_AI_STATS_DATABASE_PATH', defaultStatsPath)
  const sourceUsageShardRoot = pathOption(
    args,
    '--source-usage-shard-root=',
    'JUHE_AI_USAGE_SHARD_ROOT',
    resolve(dirname(sourceUsageCatalogPath), 'usage-shards')
  )
  const sourceCodexContextStateShardRoot = pathOption(
    args,
    '--source-codex-context-state-shard-root=',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT',
    defaultCodexContextStateShardRoot
  )

  return {
    confirmOffline: args.includes('--confirm-offline'),
    dryRun: args.includes('--dry-run'),
    skipSchema: args.includes('--skip-schema'),
    allowNonEmptyTarget: args.includes('--allow-non-empty-target'),
    skipUnsupportedTables: args.includes('--skip-unsupported-tables'),
    includeUsageShards: !args.includes('--skip-usage-shards'),
    includeCodexContextState: !args.includes('--skip-codex-context-state'),
    batchSize: numberOption(args, '--batch-size=', 'JUHE_AI_SQLITE_TO_POSTGRES_BATCH_SIZE', 500, 1, 5000),
    postgresUrl: stringOption(args, '--postgres-url=', 'JUHE_AI_POSTGRES_URL'),
    sourceBusinessPath,
    sourceDatasetPath,
    sourceUsageCatalogPath,
    sourceStatsPath,
    sourceUsageShardRoot,
    sourceCodexContextStateShardRoot,
    sourceCodexContextStateShardCount: numberOption(args, '--source-codex-context-state-shard-count=', 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT', 16, 1, 256)
  }
}

function collectSourceDatabasePlans(options: MigrationOptions): SourceDatabasePlan[] {
  const sources: SourceDatabasePlan[] = [
    { id: 'business', schemaName: 'juhe_business', path: options.sourceBusinessPath, required: true },
    { id: 'dataset', schemaName: 'juhe_dataset', path: options.sourceDatasetPath, required: false },
    { id: 'usage-catalog', schemaName: 'juhe_usage', path: options.sourceUsageCatalogPath, required: false },
    { id: 'stats', schemaName: 'juhe_stats', path: options.sourceStatsPath, required: false }
  ]
  if (options.includeUsageShards) {
    sources.push(...collectUsageShardSources(options))
  }
  if (options.includeCodexContextState) {
    for (let index = 0; index < options.sourceCodexContextStateShardCount; index += 1) {
      const shardPath = resolve(options.sourceCodexContextStateShardRoot, `state-${String(index).padStart(3, '0')}.sqlite3`)
      sources.push({
        id: `codex-context-state:${index}`,
        schemaName: 'juhe_codex_context',
        path: shardPath,
        required: false
      })
    }
  }
  const existingSources = sources.filter((source) => {
    if (existsSync(source.path)) return true
    if (source.required) {
      throw new Error(`必需源 SQLite 数据库不存在：${source.id} ${source.path}`)
    }
    console.log(`跳过不存在的可选源 SQLite 数据库：${source.id} ${source.path}`)
    return false
  })
  if (existingSources.length === 0) {
    throw new Error('未找到任何可迁移的 SQLite 源数据库')
  }
  return existingSources
}

function collectUsageShardSources(options: MigrationOptions): SourceDatabasePlan[] {
  if (!existsSync(options.sourceUsageCatalogPath)) {
    return []
  }
  const database = openReadOnlySqlite(options.sourceUsageCatalogPath)
  try {
    if (!sqliteTableExists(database, 'usage_record_shards')) {
      return []
    }
    const rows = database.prepare(`
      SELECT shard_key, file_path
      FROM usage_record_shards
      ORDER BY bucket_date ASC, shard_id ASC, shard_key ASC
    `).all() as Array<{ shard_key?: unknown; file_path?: unknown }>
    return rows.map((row) => {
      const shardKey = String(row.shard_key ?? '').trim()
      const rawPath = String(row.file_path ?? '').trim()
      const filePath = resolveSourcePath(rawPath, options.sourceUsageShardRoot)
      if (!existsSync(filePath)) {
        throw new Error(`usage shard 目录存在但文件缺失：${shardKey} ${filePath}`)
      }
      return {
        id: `usage-shard:${shardKey}`,
        schemaName: 'juhe_usage',
        path: filePath,
        required: true,
        tableNames: ['usage_records']
      }
    })
  } finally {
    database.close()
  }
}

async function migrateSourceDatabase(
  pool: PostgresMigrationPool,
  source: SourceDatabasePlan,
  tableOrderBySchema: Map<PostgresSchemaName, string[]>,
  options: MigrationOptions
): Promise<TableMigrationSummary[]> {
  const database = openReadOnlySqlite(source.path)
  try {
    const tableNames = sourceTableNames(source, tableOrderBySchema, database, options)
    const summaries: TableMigrationSummary[] = []
    for (const tableName of tableNames) {
      const summary = options.dryRun
        ? dryRunTable(database, source, tableName)
        : await copyTable(pool, database, source, tableName, options)
      summaries.push(summary)
      const verb = options.dryRun ? '计划迁移' : '已迁移'
      console.log(`${verb} ${source.id}.${tableName} -> ${source.schemaName}.${tableName}: rows=${summary.rows}, batches=${summary.batches}`)
    }
    return summaries
  } finally {
    database.close()
  }
}

function sourceTableNames(
  source: SourceDatabasePlan,
  tableOrderBySchema: Map<PostgresSchemaName, string[]>,
  database?: DatabaseSync,
  options?: MigrationOptions
): string[] {
  const targetOrder = tableOrderBySchema.get(source.schemaName) ?? []
  if (!database) {
    return source.tableNames ? [...source.tableNames] : targetOrder
  }
  const existingTables = listSqliteTables(database)
  const requestedTables = source.tableNames ? source.tableNames.filter((table) => existingTables.has(table)) : [...existingTables]
  const unsupportedTables = requestedTables.filter((table) => !targetOrder.includes(table))
  if (unsupportedTables.length > 0 && !options?.skipUnsupportedTables) {
    throw new Error(`${source.id} 存在当前 PostgreSQL schema 不支持的表：${unsupportedTables.join(', ')}。如确认可丢弃旧表，请追加 --skip-unsupported-tables`)
  }
  const supported = requestedTables.filter((table) => targetOrder.includes(table))
  return supported.sort((left, right) => targetOrder.indexOf(left) - targetOrder.indexOf(right))
}

function dryRunTable(database: DatabaseSync, source: SourceDatabasePlan, tableName: string): TableMigrationSummary {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${quoteIdentifier(tableName)}`).get() as { total?: number | bigint } | undefined
  return {
    sourceId: source.id,
    schemaName: source.schemaName,
    tableName,
    rows: Number(row?.total ?? 0),
    batches: 0,
    dryRun: true
  }
}

async function copyTable(
  pool: PostgresMigrationPool,
  database: DatabaseSync,
  source: SourceDatabasePlan,
  tableName: string,
  options: MigrationOptions
): Promise<TableMigrationSummary> {
  const sourceColumns = sqliteTableColumns(database, tableName)
  const targetColumns = await postgresTableColumns(pool, source.schemaName, tableName)
  const copiedColumns = sourceColumns.filter((column) => targetColumns.has(column))
  if (copiedColumns.length === 0) {
    throw new Error(`${source.id}.${tableName} 与 ${source.schemaName}.${tableName} 没有可迁移的列交集`)
  }

  const batchSize = Math.max(1, Math.min(options.batchSize, Math.floor(60000 / copiedColumns.length)))
  let rows = 0
  let batches = 0
  let lastRowid: number | bigint = 0
  const selectSql = `
    SELECT rowid AS __migration_rowid, ${copiedColumns.map(quoteIdentifier).join(', ')}
    FROM ${quoteIdentifier(tableName)}
    WHERE rowid > ?
    ORDER BY rowid
    LIMIT ?
  `
  const selectStatement = database.prepare(selectSql)
  while (true) {
    const batch = selectStatement.all(lastRowid, batchSize) as Array<Record<string, unknown>>
    if (batch.length === 0) break
    const lastRow = batch[batch.length - 1]
    const rowid = lastRow.__migration_rowid
    if (typeof rowid !== 'number' && typeof rowid !== 'bigint') {
      throw new Error(`${source.id}.${tableName} 无法读取 rowid，迁移脚本不会使用分页偏移扫描大表`)
    }
    await insertPostgresRows(pool, source.schemaName, tableName, copiedColumns, batch)
    lastRowid = rowid
    rows += batch.length
    batches += 1
    await yieldToEventLoop()
  }
  return {
    sourceId: source.id,
    schemaName: source.schemaName,
    tableName,
    rows,
    batches,
    dryRun: false
  }
}

async function insertPostgresRows(
  pool: PostgresMigrationPool,
  schemaName: PostgresSchemaName,
  tableName: string,
  columns: readonly string[],
  rows: Array<Record<string, unknown>>
): Promise<void> {
  const columnSql = columns.map(quoteIdentifier).join(', ')
  const values: unknown[] = []
  let parameterIndex = 1
  const rowSql = rows.map((row) => {
    const placeholders = columns.map((column) => {
      values.push(normalizePostgresValue(row[column]))
      return `$${parameterIndex++}`
    }).join(', ')
    return `(${placeholders})`
  }).join(', ')
  await pool.query(
    `INSERT INTO ${quoteIdentifier(schemaName)}.${quoteIdentifier(tableName)} (${columnSql}) VALUES ${rowSql}`,
    values
  )
}

async function assertTargetTablesEmpty(pool: PostgresMigrationPool, tableKeys: Set<string>, options: MigrationOptions): Promise<void> {
  if (options.dryRun || options.allowNonEmptyTarget) {
    return
  }
  const nonEmptyTables: string[] = []
  for (const key of tableKeys) {
    const [schemaName, tableName] = key.split('.', 2) as [PostgresSchemaName, string]
    if (!await postgresTableExists(pool, schemaName, tableName)) {
      continue
    }
    const result = await pool.query(`SELECT 1 AS exists FROM ${quoteIdentifier(schemaName)}.${quoteIdentifier(tableName)} LIMIT 1`)
    if (result.rows.length > 0) {
      nonEmptyTables.push(key)
    }
  }
  if (nonEmptyTables.length > 0) {
    throw new Error(`目标 PostgreSQL 表非空，默认拒绝迁移以避免重复写入：${nonEmptyTables.join(', ')}。如确认要追加导入，请追加 --allow-non-empty-target`)
  }
}

async function postgresTableColumns(pool: PostgresMigrationPool, schemaName: PostgresSchemaName, tableName: string): Promise<Set<string>> {
  const result = await pool.query(`
    SELECT column_name
    FROM information_schema.columns
    WHERE table_schema = $1 AND table_name = $2
    ORDER BY ordinal_position ASC
  `, [schemaName, tableName])
  return new Set(result.rows.map((row) => String(row.column_name)))
}

async function postgresTableExists(pool: PostgresMigrationPool, schemaName: PostgresSchemaName, tableName: string): Promise<boolean> {
  const result = await pool.query(`
    SELECT 1 AS exists
    FROM information_schema.tables
    WHERE table_schema = $1 AND table_name = $2
    LIMIT 1
  `, [schemaName, tableName])
  return result.rows.length > 0
}

function collectPostgresTableOrderBySchema(): Map<PostgresSchemaName, string[]> {
  const result = new Map<PostgresSchemaName, string[]>()
  for (const statement of collectPostgresSchemaStatements()) {
    const tableName = extractCreatedTableName(statement.sql)
    if (!tableName) continue
    const tables = result.get(statement.schemaName) ?? []
    if (!tables.includes(tableName)) {
      tables.push(tableName)
      result.set(statement.schemaName, tables)
    }
  }
  return result
}

function extractCreatedTableName(sql: string): string | undefined {
  const match = /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/i.exec(sql.trim())
  return match?.[1]
}

function openReadOnlySqlite(path: string): DatabaseSync {
  return new DatabaseSync(path, { readOnly: true })
}

function listSqliteTables(database: DatabaseSync): Set<string> {
  const rows = database.prepare(`
    SELECT name
    FROM sqlite_master
    WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
    ORDER BY name ASC
  `).all() as Array<{ name?: unknown }>
  return new Set(rows.map((row) => String(row.name ?? '')).filter(Boolean))
}

function sqliteTableExists(database: DatabaseSync, tableName: string): boolean {
  const row = database.prepare(`
    SELECT 1 AS exists
    FROM sqlite_master
    WHERE type = 'table' AND name = ?
    LIMIT 1
  `).get(tableName)
  return Boolean(row)
}

function sqliteTableColumns(database: DatabaseSync, tableName: string): string[] {
  const rows = database.prepare(`PRAGMA table_info(${quoteIdentifier(tableName)})`).all() as Array<{ name?: unknown }>
  return rows.map((row) => String(row.name ?? '')).filter(Boolean)
}

async function createPool(postgresUrl: string): Promise<PostgresMigrationPool> {
  const { Pool } = await import('pg')
  return new Pool({
    connectionString: postgresUrl,
    max: 1,
    application_name: 'juhe-ai-sqlite-to-postgres-migration'
  }) as unknown as PostgresMigrationPool
}

function stringOption(args: readonly string[], prefix: string, envName: string): string {
  const arg = args.find((item) => item.startsWith(prefix))
  return (arg ? arg.slice(prefix.length) : process.env[envName] ?? '').trim()
}

function pathOption(args: readonly string[], prefix: string, envName: string, fallback: string): string {
  const value = stringOption(args, prefix, envName) || fallback
  return isAbsolute(value) ? value : resolve(backendRoot, value)
}

function numberOption(args: readonly string[], prefix: string, envName: string, fallback: number, min: number, max: number): number {
  const raw = stringOption(args, prefix, envName)
  const value = Number(raw || fallback)
  return Number.isFinite(value) ? Math.min(Math.max(Math.trunc(value), min), max) : fallback
}

function resolveSourcePath(value: string, fallbackRoot: string): string {
  if (!value) {
    throw new Error('源文件路径不能为空')
  }
  return isAbsolute(value) ? value : resolve(fallbackRoot, value)
}

function targetTableKey(schemaName: PostgresSchemaName, tableName: string): string {
  return `${schemaName}.${tableName}`
}

function normalizePostgresValue(value: unknown): unknown {
  if (value === undefined) return null
  if (value instanceof Uint8Array) return Buffer.from(value)
  return value
}

function quoteIdentifier(identifier: string): string {
  const normalized = identifier.trim()
  if (!normalized) {
    throw new Error('identifier cannot be empty')
  }
  return `"${normalized.replace(/"/g, '""')}"`
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolveYield) => setImmediate(resolveYield))
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
