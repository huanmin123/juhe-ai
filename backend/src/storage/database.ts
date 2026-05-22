import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { applyBusinessSchema, applyDatasetSchema, applyRecordSchema, applyStatsSchema, seedDefaults } from './schema.js'

let businessDatabase: DatabaseSync | undefined
let recordDatabase: DatabaseSync | undefined
let datasetDatabase: DatabaseSync | undefined
let statsDatabase: DatabaseSync | undefined
const sqliteBusyTimeoutMs = 5000
type AfterCommitEffect = () => void
const afterCommitEffectsByDatabase = new WeakMap<DatabaseSync, AfterCommitEffect[]>()

export function getDatabase(): DatabaseSync {
  return getBusinessDatabase()
}

export function getBusinessDatabase(): DatabaseSync {
  if (businessDatabase) {
    return businessDatabase
  }

  const databasePath = runtimeConfig.databasePath
  mkdirSync(dirname(databasePath), { recursive: true })

  businessDatabase = new DatabaseSync(databasePath)
  configureDatabase(businessDatabase)
  applyBusinessSchema(businessDatabase)
  seedDefaults(businessDatabase)
  return businessDatabase
}

export function getRecordDatabase(): DatabaseSync {
  if (recordDatabase) {
    return recordDatabase
  }

  recordDatabase = openRecordStyleDatabase(runtimeConfig.recordDatabasePath)
  return recordDatabase
}

export function getDatasetDatabase(): DatabaseSync {
  const databasePath = datasetDatabasePath()
  if (databasePath === runtimeConfig.recordDatabasePath) {
    return getRecordDatabase()
  }
  if (databasePath === statsDatabasePath() && statsDatabase) {
    applyDatasetSchema(statsDatabase)
    datasetDatabase = statsDatabase
    return datasetDatabase
  }
  if (datasetDatabase) {
    return datasetDatabase
  }
  datasetDatabase = openDatasetDatabase(databasePath)
  return datasetDatabase
}

export function getStatsDatabase(): DatabaseSync {
  const databasePath = statsDatabasePath()
  if (databasePath === runtimeConfig.recordDatabasePath) {
    return getRecordDatabase()
  }
  if (databasePath === datasetDatabasePath()) {
    const database = getDatasetDatabase()
    applyStatsSchema(database)
    statsDatabase = database
    return statsDatabase
  }
  if (statsDatabase) {
    return statsDatabase
  }
  statsDatabase = openStatsDatabase(databasePath)
  return statsDatabase
}

export function datasetDatabasePath(): string {
  return runtimeConfig.datasetDatabasePath ?? runtimeConfig.recordDatabasePath
}

export function statsDatabasePath(): string {
  return runtimeConfig.statsDatabasePath ?? runtimeConfig.recordDatabasePath
}

function openRecordStyleDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database)
  applyRecordSchema(database)
  return database
}

function openDatasetDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database)
  applyDatasetSchema(database)
  return database
}

function openStatsDatabase(databasePath: string): DatabaseSync {
  mkdirSync(dirname(databasePath), { recursive: true })
  const database = new DatabaseSync(databasePath)
  configureDatabase(database)
  applyStatsSchema(database)
  return database
}

export function beginDatabaseTransaction(target = getDatabase()): boolean {
  if (target.isTransaction) {
    return false
  }
  target.exec('BEGIN')
  return true
}

export function beginImmediateDatabaseTransaction(target = getDatabase()): boolean {
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

export function runInDatabaseTransaction<T>(operation: () => T, target = getDatabase()): T {
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

function configureDatabase(database: DatabaseSync): void {
  database.exec(`
    PRAGMA busy_timeout = ${sqliteBusyTimeoutMs};
  `)
}

export function nowIso(): string {
  return new Date().toISOString()
}

export function newId(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`
}

export function runAfterDatabaseCommit(effect: AfterCommitEffect, target = getDatabase()): void {
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
