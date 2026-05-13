import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { applyBusinessSchema, applyRecordSchema, seedDefaults } from './schema.js'

let businessDatabase: DatabaseSync | undefined
let recordDatabase: DatabaseSync | undefined
const sqliteBusyTimeoutMs = 5000

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

  const databasePath = runtimeConfig.recordDatabasePath
  mkdirSync(dirname(databasePath), { recursive: true })

  recordDatabase = new DatabaseSync(databasePath)
  configureDatabase(recordDatabase)
  applyRecordSchema(recordDatabase)
  return recordDatabase
}

export function beginDatabaseTransaction(target = getDatabase()): boolean {
  if (target.isTransaction) {
    return false
  }
  target.exec('BEGIN')
  return true
}

export function commitDatabaseTransaction(target: DatabaseSync, started: boolean): void {
  if (started) {
    target.exec('COMMIT')
  }
}

export function rollbackDatabaseTransaction(target: DatabaseSync, started: boolean): void {
  if (started) {
    target.exec('ROLLBACK')
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
