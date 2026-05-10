import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { applySchema, seedDefaults } from './schema.js'

let database: DatabaseSync | undefined
const sqliteBusyTimeoutMs = 5000

export function getDatabase(): DatabaseSync {
  if (database) {
    return database
  }

  const databasePath = runtimeConfig.databasePath
  mkdirSync(dirname(databasePath), { recursive: true })

  database = new DatabaseSync(databasePath)
  configureDatabase(database)
  applySchema(database)
  seedDefaults(database)
  return database
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
