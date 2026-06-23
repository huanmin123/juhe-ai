import { mkdirSync } from 'node:fs'
import { dirname, normalize, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { defaultDatasetDatabasePath, defaultUsageCatalogDatabasePath, isProductionRuntime, runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { applyBusinessSchema, applyCodexContextStateSchema, applyDatasetSchema, applyStatsSchema, applyUsageCatalogSchema, seedDefaults } from './schema.js'
import { sqliteBusyTimeoutMs } from './sqlite-config.js'
import { closeUsageRecordShardDatabases } from './usage-record-shards.js'

let businessDatabase: DatabaseSync | undefined
let datasetDatabase: DatabaseSync | undefined
let usageCatalogDatabase: DatabaseSync | undefined
let statsDatabase: DatabaseSync | undefined
const codexContextStateShardDatabases = new Map<number, DatabaseSync>()
type AfterCommitEffect = () => void
const afterCommitEffectsByDatabase = new WeakMap<DatabaseSync, AfterCommitEffect[]>()

export type SqliteMainDatabaseKind = 'business' | 'dataset' | 'usage-catalog' | 'stats' | 'codex-context-state'
export type SqliteWriterOwner = 'db-service' | 'ingest-worker' | 'stats-writer' | 'usage-shard-writer'

export interface SqliteDatabaseRuntimeInfo {
  kind: SqliteMainDatabaseKind
  path: string
  writerOwner: SqliteWriterOwner
  currentProcessOwner: boolean
  queryOnly: boolean
}

export function getBusinessDatabase(): DatabaseSync {
  assertDistinctStoragePaths()
  if (businessDatabase) {
    return businessDatabase
  }

  const databasePath = runtimeConfig.databasePath
  mkdirSync(dirname(databasePath), { recursive: true })

  businessDatabase = new DatabaseSync(databasePath)
  configureDatabase(businessDatabase, 'business')
  if (shouldApplyMainDatabaseSchema('business')) {
    applyBusinessSchema(businessDatabase)
    seedDefaults(businessDatabase)
  }
  return businessDatabase
}

export function getDatasetDatabase(): DatabaseSync {
  assertDistinctStoragePaths()
  const databasePath = datasetDatabasePath()
  if (datasetDatabase) {
    return datasetDatabase
  }
  datasetDatabase = openDatasetDatabase(databasePath)
  return datasetDatabase
}

export function getUsageCatalogDatabase(): DatabaseSync {
  assertDistinctStoragePaths()
  const databasePath = usageCatalogDatabasePath()
  if (usageCatalogDatabase) {
    return usageCatalogDatabase
  }
  usageCatalogDatabase = openUsageCatalogDatabase(databasePath)
  return usageCatalogDatabase
}

export function getStatsDatabase(): DatabaseSync {
  assertDistinctStoragePaths()
  const databasePath = statsDatabasePath()
  if (statsDatabase) {
    return statsDatabase
  }
  statsDatabase = openStatsDatabase(databasePath)
  return statsDatabase
}

export function getCodexContextStateShardDatabase(shardIndex: number): DatabaseSync {
  assertDistinctStoragePaths()
  const normalizedShardIndex = normalizeCodexContextStateShardIndex(shardIndex)
  const existing = codexContextStateShardDatabases.get(normalizedShardIndex)
  if (existing) {
    return existing
  }
  const database = openCodexContextStateDatabase(codexContextStateShardPath(normalizedShardIndex))
  codexContextStateShardDatabases.set(normalizedShardIndex, database)
  return database
}

export function closeStorageDatabases(): void {
  closeUsageRecordShardDatabases()
  closeDatabaseHandle(businessDatabase)
  closeDatabaseHandle(datasetDatabase)
  closeDatabaseHandle(usageCatalogDatabase)
  closeDatabaseHandle(statsDatabase)
  for (const database of codexContextStateShardDatabases.values()) {
    closeDatabaseHandle(database)
  }
  businessDatabase = undefined
  datasetDatabase = undefined
  usageCatalogDatabase = undefined
  statsDatabase = undefined
  codexContextStateShardDatabases.clear()
}

export function datasetDatabasePath(): string {
  return runtimeConfig.datasetDatabasePath
}

export function usageCatalogDatabasePath(): string {
  if (
    normalize(runtimeConfig.usageCatalogDatabasePath) === normalize(defaultUsageCatalogDatabasePath)
    && normalize(runtimeConfig.datasetDatabasePath) !== normalize(defaultDatasetDatabasePath)
  ) {
    return resolve(dirname(runtimeConfig.datasetDatabasePath), 'usage-catalog.sqlite3')
  }
  return runtimeConfig.usageCatalogDatabasePath
}

export function statsDatabasePath(): string {
  return runtimeConfig.statsDatabasePath
}

export function codexContextStateShardRootPath(): string {
  return runtimeConfig.codexContextStateShardRoot
}

export function codexContextStateShardCount(): number {
  return Math.max(1, Math.min(Math.trunc(runtimeConfig.codexContextStateShardCount), 256))
}

export function codexContextStateShardIndexes(): number[] {
  return Array.from({ length: codexContextStateShardCount() }, (_, index) => index)
}

export function codexContextStateShardIndexForKey(key: string): number {
  const text = String(key || '')
  let hash = 2166136261
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0) % codexContextStateShardCount()
}

export function codexContextStateShardPath(shardIndex: number): string {
  const normalizedShardIndex = normalizeCodexContextStateShardIndex(shardIndex)
  return resolve(runtimeConfig.codexContextStateShardRoot, `state-${String(normalizedShardIndex).padStart(3, '0')}.sqlite3`)
}

function openDatasetDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database, 'dataset')
  if (shouldApplyMainDatabaseSchema('dataset')) {
    applyDatasetSchema(database)
  }
  return database
}

function openUsageCatalogDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database, 'usage-catalog')
  if (shouldApplyMainDatabaseSchema('usage-catalog')) {
    applyUsageCatalogSchema(database)
  }
  return database
}

function openStatsDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database, 'stats')
  if (shouldApplyMainDatabaseSchema('stats')) {
    applyStatsSchema(database)
  }
  return database
}

function openCodexContextStateDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database, 'codex-context-state')
  if (shouldApplyMainDatabaseSchema('codex-context-state')) {
    applyCodexContextStateSchema(database)
  }
  return database
}

export function beginDatabaseTransaction(target = getBusinessDatabase()): boolean {
  if (target.isTransaction) {
    return false
  }
  target.exec('BEGIN')
  return true
}

export function beginImmediateDatabaseTransaction(target = getBusinessDatabase()): boolean {
  if (target.isTransaction) {
    return false
  }
  target.exec('BEGIN IMMEDIATE')
  return true
}

export function commitDatabaseTransaction(target: DatabaseSync, started: boolean): void {
  if (started) {
    target.exec('COMMIT')
    flushAfterCommitEffects(target)
  }
}

export function rollbackDatabaseTransaction(target: DatabaseSync, started: boolean): void {
  if (started) {
    try {
      target.exec('ROLLBACK')
    } finally {
      discardAfterCommitEffects(target)
    }
  }
}

export function runInDatabaseTransaction<T>(operation: () => T, target = getBusinessDatabase()): T {
  const transactionStarted = beginDatabaseTransaction(target)
  try {
    const result = operation()
    commitDatabaseTransaction(target, transactionStarted)
    return result
  } catch (error) {
    rollbackDatabaseTransaction(target, transactionStarted)
    throw error
  }
}

function configureDatabase(database: DatabaseSync, kind: SqliteMainDatabaseKind): void {
  database.exec(`
    PRAGMA busy_timeout = ${sqliteBusyTimeoutMs};
  `)
  applySqliteWriterBoundary(database, kind)
}

function closeDatabaseHandle(database: DatabaseSync | undefined): void {
  if (!database) {
    return
  }
  try {
    database.close()
  } catch {
  }
}

function assertDistinctStoragePaths(): void {
  const targets = [
    { role: '业务库', path: runtimeConfig.databasePath },
    { role: '数据集目录库', path: datasetDatabasePath() },
    { role: '使用记录目录库', path: usageCatalogDatabasePath() },
    { role: '统计结果库', path: statsDatabasePath() },
    ...codexContextStateShardIndexes().map((shardIndex) => ({
      role: `Codex Responses 上下文索引库分片 ${shardIndex}`,
      path: codexContextStateShardPath(shardIndex)
    }))
  ]
  const seen = new Map<string, string>()
  for (const target of targets) {
    const key = normalize(target.path).toLowerCase()
    const existingRole = seen.get(key)
    if (existingRole) {
      throw new Error(`${target.role} 与 ${existingRole} 指向同一个 SQLite 文件，请分别配置 JUHE_AI_DATABASE_PATH、JUHE_AI_DATASET_DATABASE_PATH、JUHE_AI_USAGE_CATALOG_DATABASE_PATH、JUHE_AI_STATS_DATABASE_PATH、JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 和 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT`)
    }
    seen.set(key, target.role)
  }
}

export function nowIso(): string {
  return new Date().toISOString()
}

export function sqliteWriterOwnerForMainDatabase(kind: SqliteMainDatabaseKind): SqliteWriterOwner {
  if (kind === 'business' || kind === 'codex-context-state') {
    return 'db-service'
  }
  if (kind === 'dataset' || kind === 'usage-catalog') {
    return 'ingest-worker'
  }
  return 'stats-writer'
}

export function currentProcessOwnsSqliteMainDatabase(kind: SqliteMainDatabaseKind): boolean {
  if (kind === 'business' || kind === 'codex-context-state') {
    return runtimeConfig.processRole === 'db-service'
  }
  if (kind === 'dataset' || kind === 'usage-catalog') {
    return runtimeConfig.processRole === 'worker'
      && runtimeConfig.workerRole === 'ingest-worker'
  }
  return runtimeConfig.processRole === 'worker'
    && runtimeConfig.workerRole === 'stats-worker'
}

export function sqliteWriterBoundaryStrictModeEnabled(): boolean {
  const configured = process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT?.trim().toLowerCase()
  if (configured && ['1', 'true', 'yes', 'on'].includes(configured)) {
    return true
  }
  if (configured && ['0', 'false', 'no', 'off'].includes(configured)) {
    return isProductionRuntime()
  }
  return !isOfflineSqliteScriptRuntime()
}

export function mainDatabaseRuntimeInfo(kind: SqliteMainDatabaseKind): SqliteDatabaseRuntimeInfo {
  const path = kind === 'business'
    ? runtimeConfig.databasePath
    : kind === 'dataset'
      ? datasetDatabasePath()
      : kind === 'usage-catalog'
        ? usageCatalogDatabasePath()
        : kind === 'stats'
          ? statsDatabasePath()
          : codexContextStateShardRootPath()
  return {
    kind,
    path,
    writerOwner: sqliteWriterOwnerForMainDatabase(kind),
    currentProcessOwner: currentProcessOwnsSqliteMainDatabase(kind),
    queryOnly: sqliteWriterBoundaryStrictModeEnabled() && !currentProcessOwnsSqliteMainDatabase(kind)
  }
}

export function isSqliteDatabaseLocked(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false
  }
  const sqliteError = error as Error & { code?: unknown; errcode?: unknown; errstr?: unknown }
  return sqliteError.errcode === 5
    || sqliteError.errstr === 'database is locked'
    || error.message.includes('database is locked')
    || error.message.includes('SQLITE_BUSY')
}

function applySqliteWriterBoundary(database: DatabaseSync, kind: SqliteMainDatabaseKind): void {
  if (!sqliteWriterBoundaryStrictModeEnabled() || currentProcessOwnsSqliteMainDatabase(kind)) {
    return
  }
  database.exec('PRAGMA query_only = ON')
}

function shouldApplyMainDatabaseSchema(kind: SqliteMainDatabaseKind): boolean {
  return currentProcessOwnsSqliteMainDatabase(kind) || !sqliteWriterBoundaryStrictModeEnabled()
}

function normalizeCodexContextStateShardIndex(shardIndex: number): number {
  const count = codexContextStateShardCount()
  const integer = Math.trunc(Number(shardIndex))
  if (!Number.isFinite(integer) || integer < 0 || integer >= count) {
    throw new Error(`Codex Responses 上下文索引库分片编号必须在 0 到 ${count - 1} 之间`)
  }
  return integer
}

function isOfflineSqliteScriptRuntime(): boolean {
  const entry = process.argv[1]
  if (!entry) {
    return false
  }
  const normalized = normalize(entry).replace(/\\/g, '/').toLowerCase()
  return normalized.includes('/src/scripts/')
    || normalized.includes('/dist/scripts/')
}

export function newId(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`
}

export function runAfterDatabaseCommit(effect: AfterCommitEffect, target = getBusinessDatabase()): void {
  if (!target.isTransaction) {
    effect()
    return
  }
  const queue = afterCommitEffectsByDatabase.get(target)
  if (queue) {
    queue.push(effect)
    return
  }
  afterCommitEffectsByDatabase.set(target, [effect])
}

function flushAfterCommitEffects(target: DatabaseSync): void {
  const effects = afterCommitEffectsByDatabase.get(target)
  if (!effects?.length) {
    return
  }
  afterCommitEffectsByDatabase.delete(target)
  for (const effect of effects) {
    try {
      effect()
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'database_after_commit_effect_failed'
      }), '数据库提交后副作用执行失败')
    }
  }
}

function discardAfterCommitEffects(target: DatabaseSync): void {
  afterCommitEffectsByDatabase.delete(target)
}
