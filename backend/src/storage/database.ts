import { lstatSync, mkdirSync, realpathSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, normalize, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { defaultDatasetDatabasePath, defaultUsageCatalogDatabasePath, isProductionRuntime, runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { applyBusinessSchema, applyChatSchema, applyCodexContextStateSchema, applyDatasetSchema, applyStatsSchema, applyUsageCatalogSchema, seedDefaults } from './schema.js'
import { sqliteBusyTimeoutMs } from './sqlite-config.js'
import { closeUsageRecordShardDatabases } from './usage-record-shards.js'

let businessDatabase: DatabaseSync | undefined
let chatDatabase: DatabaseSync | undefined
let datasetDatabase: DatabaseSync | undefined
let usageCatalogDatabase: DatabaseSync | undefined
let statsDatabase: DatabaseSync | undefined
let runtimeLogDatabase: DatabaseSync | undefined
let tableMonitorDatabase: DatabaseSync | undefined
const codexContextStateShardDatabases = new Map<number, DatabaseSync>()
type AfterCommitEffect = () => void
const afterCommitEffectsByDatabase = new WeakMap<DatabaseSync, AfterCommitEffect[]>()
const require = createRequire(import.meta.url)
let DatabaseSyncConstructor: typeof import('node:sqlite').DatabaseSync | undefined

export type SqliteMainDatabaseKind = 'business' | 'chat' | 'dataset' | 'runtime-log' | 'usage-catalog' | 'stats' | 'table-monitor' | 'codex-context-state'
export type SqliteWriterOwner = 'db-service' | 'ingest-worker' | 'stats-writer' | 'usage-shard-writer' | 'go-runtime-log' | 'go-table-monitor'

export interface SqliteDatabaseRuntimeInfo {
  kind: SqliteMainDatabaseKind
  path: string
  writerOwner: SqliteWriterOwner
  currentProcessOwner: boolean
  queryOnly: boolean
}

export function getBusinessDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  if (businessDatabase) {
    return businessDatabase
  }

  const databasePath = runtimeConfig.databasePath
  businessDatabase = createSqliteDatabase(databasePath, 'business')
  configureDatabase(businessDatabase, 'business')
  if (shouldApplyMainDatabaseSchema('business')) {
    applyBusinessSchema(businessDatabase)
    seedDefaults(businessDatabase)
  }
  return businessDatabase
}

export function getChatDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  if (chatDatabase) return chatDatabase
  chatDatabase = createSqliteDatabase(runtimeConfig.chatDatabasePath, 'chat')
  configureDatabase(chatDatabase, 'chat')
  if (shouldApplyMainDatabaseSchema('chat')) applyChatSchema(chatDatabase)
  return chatDatabase
}

export function getDatasetDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  const databasePath = datasetDatabasePath()
  if (datasetDatabase) {
    return datasetDatabase
  }
  datasetDatabase = openDatasetDatabase(databasePath)
  return datasetDatabase
}

export function getUsageCatalogDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  const databasePath = usageCatalogDatabasePath()
  if (usageCatalogDatabase) {
    return usageCatalogDatabase
  }
  usageCatalogDatabase = openUsageCatalogDatabase(databasePath)
  return usageCatalogDatabase
}

export function getStatsDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  const databasePath = statsDatabasePath()
  if (statsDatabase) {
    return statsDatabase
  }
  statsDatabase = openStatsDatabase(databasePath)
  return statsDatabase
}

export function getRuntimeLogDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  if (runtimeLogDatabase) return runtimeLogDatabase
  runtimeLogDatabase = createSqliteDatabase(runtimeConfig.runtimeLogDatabasePath, 'runtime-log')
  return runtimeLogDatabase
}

export function getTableMonitorDatabase(): DatabaseSync {
  assertSqliteDatabaseDriver()
  assertDistinctStoragePaths()
  if (tableMonitorDatabase) return tableMonitorDatabase
  tableMonitorDatabase = createSqliteDatabase(runtimeConfig.tableMonitorDatabasePath, 'table-monitor')
  configureDatabase(tableMonitorDatabase, 'table-monitor')
  return tableMonitorDatabase
}

export function getCodexContextStateShardDatabase(shardIndex: number): DatabaseSync {
  assertSqliteDatabaseDriver()
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
  closeDatabaseHandle(chatDatabase)
  closeDatabaseHandle(datasetDatabase)
  closeDatabaseHandle(usageCatalogDatabase)
  closeDatabaseHandle(statsDatabase)
  closeDatabaseHandle(runtimeLogDatabase)
  closeDatabaseHandle(tableMonitorDatabase)
  for (const database of codexContextStateShardDatabases.values()) {
    closeDatabaseHandle(database)
  }
  businessDatabase = undefined
  chatDatabase = undefined
  datasetDatabase = undefined
  usageCatalogDatabase = undefined
  statsDatabase = undefined
  runtimeLogDatabase = undefined
  tableMonitorDatabase = undefined
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
  const database = createSqliteDatabase(databasePath, 'dataset')
  configureDatabase(database, 'dataset')
  if (shouldApplyMainDatabaseSchema('dataset')) {
    applyDatasetSchema(database)
  }
  return database
}

function openUsageCatalogDatabase(databasePath: string): DatabaseSync {
  const database = createSqliteDatabase(databasePath, 'usage-catalog')
  configureDatabase(database, 'usage-catalog')
  if (shouldApplyMainDatabaseSchema('usage-catalog')) {
    applyUsageCatalogSchema(database)
  }
  return database
}

function openStatsDatabase(databasePath: string): DatabaseSync {
  const database = createSqliteDatabase(databasePath, 'stats')
  configureDatabase(database, 'stats')
  if (shouldApplyMainDatabaseSchema('stats')) {
    applyStatsSchema(database)
  }
  return database
}

function openCodexContextStateDatabase(databasePath: string): DatabaseSync {
  const database = createSqliteDatabase(databasePath, 'codex-context-state')
  configureDatabase(database, 'codex-context-state')
  if (shouldApplyMainDatabaseSchema('codex-context-state')) {
    applyCodexContextStateSchema(database)
  }
  return database
}

function createSqliteDatabase(databasePath: string, kind: SqliteMainDatabaseKind): DatabaseSync {
  const Constructor = getDatabaseSyncConstructor()
  if (shouldOpenSqliteDatabaseReadOnly(kind)) {
    return new Constructor(databasePath, { readOnly: true })
  }
  mkdirSync(dirname(databasePath), { recursive: true })
  return new Constructor(databasePath)
}

function shouldOpenSqliteDatabaseReadOnly(kind: SqliteMainDatabaseKind): boolean {
	// The F1 SQLite file is owned by the Go indexer even when the legacy
	// writer-boundary compatibility switch is disabled. Node only serves the
	// query-side consumers of this database.
	if (kind === 'runtime-log' || kind === 'table-monitor') return true
	return sqliteWriterBoundaryStrictModeEnabled() && !currentProcessOwnsSqliteMainDatabase(kind)
}

function getDatabaseSyncConstructor(): typeof import('node:sqlite').DatabaseSync {
  if (!DatabaseSyncConstructor) {
    DatabaseSyncConstructor = require('node:sqlite').DatabaseSync as typeof import('node:sqlite').DatabaseSync
  }
  return DatabaseSyncConstructor
}

export function beginDatabaseTransaction(target = getBusinessDatabase()): boolean {
  if (target.isTransaction) {
    return false
  }
  target.exec('BEGIN IMMEDIATE')
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

export function runWithSqliteBusyTimeout<T>(target: DatabaseSync, timeoutMs: number, operation: () => T): T {
  const normalizedTimeoutMs = Math.max(0, Math.trunc(timeoutMs))
  target.exec(`PRAGMA busy_timeout = ${normalizedTimeoutMs}`)
  try {
    return operation()
  } finally {
    target.exec(`PRAGMA busy_timeout = ${sqliteBusyTimeoutMs}`)
  }
}

function configureDatabase(database: DatabaseSync, kind: SqliteMainDatabaseKind): void {
  database.exec(`
    PRAGMA busy_timeout = ${sqliteBusyTimeoutMs};
  `)
  applySqliteWriterBoundary(database, kind)
}

function assertSqliteDatabaseDriver(): void {
  if (runtimeConfig.databaseDriver !== 'sqlite') {
    throw new Error('当前代码阶段尚未接入 PostgreSQL 数据库 driver，JUHE_AI_DATABASE_DRIVER=postgres 不能回退写入 SQLite')
  }
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

export function assertDistinctStoragePaths(): void {
  const targets = [
    { role: '业务库', path: runtimeConfig.databasePath },
    { role: '聊天库', path: runtimeConfig.chatDatabasePath },
    { role: '数据集目录库', path: datasetDatabasePath() },
    { role: '运行日志索引库', path: runtimeConfig.runtimeLogDatabasePath },
    { role: '表存储监控输出库', path: runtimeConfig.tableMonitorDatabasePath },
    { role: '使用记录目录库', path: usageCatalogDatabasePath() },
    { role: '统计结果库', path: statsDatabasePath() },
    ...codexContextStateShardIndexes().map((shardIndex) => ({
      role: `Responses 桥接状态索引库分片 ${shardIndex}`,
      path: codexContextStateShardPath(shardIndex)
    }))
  ]
  const identities = targets.map((target) => sqliteStoragePathIdentity(target.role, target.path))
  for (let leftIndex = 0; leftIndex < identities.length; leftIndex += 1) {
    const left = identities[leftIndex]
    for (let rightIndex = leftIndex + 1; rightIndex < identities.length; rightIndex += 1) {
      const right = identities[rightIndex]
      if (storagePathKey(left.canonicalPath) === storagePathKey(right.canonicalPath)) {
        throwDuplicateSqliteStoragePathError(left.role, right.role)
      }
      if (left.fileIdentity && right.fileIdentity && left.fileIdentity === right.fileIdentity) {
        throwDuplicateSqliteStoragePathError(left.role, right.role)
      }
    }
  }
}

interface SqliteStoragePathIdentity {
  role: string
  canonicalPath: string
  fileIdentity?: string
}

function sqliteStoragePathIdentity(role: string, configuredPath: string): SqliteStoragePathIdentity {
  const canonicalPath = canonicalizeSqliteStoragePath(role, configuredPath)
  return {
    role,
    canonicalPath,
    fileIdentity: existingSqliteFileIdentity(role, configuredPath)
  }
}

function canonicalizeSqliteStoragePath(role: string, configuredPath: string): string {
  if (!configuredPath.trim()) {
    throw new Error(`${role} 未配置 SQLite 路径，无法证明存储文件隔离`)
  }

  const absolutePath = resolve(configuredPath)
  const unresolvedSegments: string[] = []
  let probePath = absolutePath
  while (true) {
    try {
      const resolvedParent = realpathSync.native(probePath)
      return unresolvedSegments.reduce((currentPath, segment) => resolve(currentPath, segment), resolvedParent)
    } catch (error) {
      const errorCode = nodeErrorCode(error)
      if (errorCode !== 'ENOENT') {
        throw new Error(`${role} 的 SQLite 路径无法解析为物理文件：${absolutePath}`, { cause: error })
      }
    }

    let entry
    try {
      entry = lstatSync(probePath)
    } catch (error) {
      if (nodeErrorCode(error) !== 'ENOENT') {
        throw new Error(`${role} 的 SQLite 路径无法检查：${absolutePath}`, { cause: error })
      }
      const parentPath = dirname(probePath)
      if (parentPath === probePath) {
        throw new Error(`${role} 的 SQLite 路径没有可解析的父目录：${absolutePath}`)
      }
      unresolvedSegments.unshift(probePath.slice(parentPath.length).replace(/^[\\/]+/, ''))
      probePath = parentPath
      continue
    }

    if (entry.isSymbolicLink()) {
      throw new Error(`${role} 的 SQLite 路径包含无法解析的符号链接：${absolutePath}`)
    }
    throw new Error(`${role} 的 SQLite 路径无法证明物理身份：${absolutePath}`)
  }
}

function existingSqliteFileIdentity(role: string, configuredPath: string): string | undefined {
  try {
    const entry = statSync(configuredPath)
    if (!entry.isFile()) {
      throw new Error(`${role} 的 SQLite 路径不是常规文件：${resolve(configuredPath)}`)
    }
    if (!Number.isSafeInteger(entry.nlink) || entry.nlink !== 1) {
      throw new Error(`${role} 的 SQLite 路径包含硬链接，无法证明单文件单 owner：${resolve(configuredPath)}`)
    }
    if (process.platform === 'win32') {
      // Node 在部分 Windows 文件系统上不提供可靠 inode；符号链接已由
      // realpath 归一化，硬链接则被上面的 nlink=1 门禁拒绝。
      return undefined
    }
    if (!Number.isSafeInteger(entry.ino) || entry.ino <= 0) {
      throw new Error(`${role} 的 SQLite 路径无法提供稳定的物理文件 identity：${resolve(configuredPath)}`)
    }
    return `${entry.dev}:${entry.ino}`
  } catch (error) {
    if (nodeErrorCode(error) === 'ENOENT') {
      return undefined
    }
    if (error instanceof Error && error.message.includes('SQLite 路径')) {
      throw error
    }
    throw new Error(`${role} 的 SQLite 路径无法读取物理文件 identity：${resolve(configuredPath)}`, { cause: error })
  }
}

function storagePathKey(path: string): string {
  const normalizedPath = normalize(path)
  return process.platform === 'win32' ? normalizedPath.toLowerCase() : normalizedPath
}

function nodeErrorCode(error: unknown): string | undefined {
  return typeof error === 'object' && error !== null && 'code' in error
    ? String((error as { code?: unknown }).code ?? '')
    : undefined
}

function throwDuplicateSqliteStoragePathError(role: string, existingRole: string): never {
  throw new Error(`${role} 与 ${existingRole} 指向同一个 SQLite 物理文件，请分别配置 JUHE_AI_DATABASE_PATH、JUHE_AI_CHAT_DATABASE_PATH、JUHE_AI_DATASET_DATABASE_PATH、JUHE_AI_RUNTIME_LOG_DATABASE_PATH、JUHE_AI_TABLE_MONITOR_DATABASE_PATH、JUHE_AI_USAGE_CATALOG_DATABASE_PATH、JUHE_AI_STATS_DATABASE_PATH、JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 和 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT`)
}

export function nowIso(): string {
  return new Date().toISOString()
}

export function sqliteWriterOwnerForMainDatabase(kind: SqliteMainDatabaseKind): SqliteWriterOwner {
  if (kind === 'business' || kind === 'chat' || kind === 'codex-context-state') {
    return 'db-service'
  }
  if (kind === 'dataset' || kind === 'usage-catalog') {
    return 'ingest-worker'
  }
  if (kind === 'runtime-log') return 'go-runtime-log'
  if (kind === 'table-monitor') return 'go-table-monitor'
  return 'stats-writer'
}

export function currentProcessOwnsSqliteMainDatabase(kind: SqliteMainDatabaseKind): boolean {
  if (kind === 'runtime-log' || kind === 'table-monitor') return false
  if (sqliteOfflineMaintenanceOwnsAllMainDatabases()) {
    return true
  }
  if (kind === 'business' || kind === 'chat' || kind === 'codex-context-state') {
    return runtimeConfig.processRole === 'db-service'
  }
  if (kind === 'dataset' || kind === 'usage-catalog') {
    return runtimeConfig.processRole === 'worker'
      && (runtimeConfig.workerRole === 'ingest-worker' || runtimeConfig.workerRole === 'temporary-maintenance-worker')
  }
  return runtimeConfig.processRole === 'worker'
    && (runtimeConfig.workerRole === 'stats-worker' || runtimeConfig.workerRole === 'temporary-maintenance-worker')
}

function sqliteOfflineMaintenanceOwnsAllMainDatabases(): boolean {
  const enabled = process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE?.trim().toLowerCase()
  return Boolean(enabled && ['1', 'true', 'yes', 'on'].includes(enabled))
    && runtimeConfig.processRole === 'worker'
    && runtimeConfig.workerRole === 'temporary-maintenance-worker'
}

export function sqliteWriterBoundaryStrictModeEnabled(): boolean {
  const configured = process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT?.trim().toLowerCase()
  if (configured && ['1', 'true', 'yes', 'on'].includes(configured)) {
    return true
  }
  if (configured && ['0', 'false', 'no', 'off'].includes(configured)) {
    return isProductionRuntime()
  }
  return !isLocalRegressionOrPerformanceScriptRuntime()
}

export function mainDatabaseRuntimeInfo(kind: SqliteMainDatabaseKind): SqliteDatabaseRuntimeInfo {
  const path = kind === 'business'
    ? runtimeConfig.databasePath
    : kind === 'chat'
      ? runtimeConfig.chatDatabasePath
    : kind === 'dataset'
      ? datasetDatabasePath()
      : kind === 'runtime-log'
        ? runtimeConfig.runtimeLogDatabasePath
      : kind === 'table-monitor'
        ? runtimeConfig.tableMonitorDatabasePath
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
    queryOnly: kind === 'runtime-log' || kind === 'table-monitor' || (sqliteWriterBoundaryStrictModeEnabled() && !currentProcessOwnsSqliteMainDatabase(kind))
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
    throw new Error(`Responses 桥接状态索引库分片编号必须在 0 到 ${count - 1} 之间`)
  }
  return integer
}

export function newId(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`
}

function isLocalRegressionOrPerformanceScriptRuntime(): boolean {
  const entry = process.argv[1]
  if (!entry) {
    return false
  }
  const normalized = normalize(entry).replace(/\\/g, '/').toLowerCase()
  return normalized.includes('/src/scripts/regression/')
    || normalized.includes('/src/scripts/performance/')
    || normalized.includes('/dist/scripts/regression/')
    || normalized.includes('/dist/scripts/performance/')
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
