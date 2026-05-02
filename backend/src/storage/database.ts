import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { applySchema, seedDefaults } from './schema.js'

let database: DatabaseSync | undefined

export function getDatabase(): DatabaseSync {
  if (database) {
    return database
  }

  const databasePath = runtimeConfig.databasePath
  mkdirSync(dirname(databasePath), { recursive: true })

  database = new DatabaseSync(databasePath)
  applySchema(database)
  seedDefaults(database)
  return database
}

export function nowIso(): string {
  return new Date().toISOString()
}

export function newId(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`
}