import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

type VersionRow = { current_version: number | bigint | string }

// This epoch is deliberately separate from account config/dispatch revisions:
// disabling or revoking an account can invalidate a probe without changing
// either business revision. Callers reserve before publishing a replacement
// signed snapshot; the projector only accepts the latest reserved epoch.
export function reserveAccountHealthJobsInputVersion(
  accountId: string,
  database: DatabaseSync = getBusinessDatabase()
): number {
  const normalizedAccountId = requiredAccountId(accountId)
  return runInDatabaseTransaction(() => reserveAccountHealthJobsInputVersionInTransaction(normalizedAccountId, database), database)
}

// Callers that are already inside the DB-service business transaction use
// this form to reserve the epoch together with their durable publish intent.
// Keeping both writes in one transaction closes the commit-to-file crash
// window without allowing jobs to become a business SQLite writer.
export function reserveAccountHealthJobsInputVersionInTransaction(
  accountId: string,
  database: DatabaseSync = getBusinessDatabase()
): number {
  return reserveInSqlite(requiredAccountId(accountId), database)
}

export async function reserveAccountHealthJobsInputVersionAsync(
  client: DatabaseClient,
  accountId: string
): Promise<number> {
  return await client.transaction(async (tx) => await reserveAccountHealthJobsInputVersionInTransactionAsync(tx, accountId))
}

export async function reserveAccountHealthJobsInputVersionInTransactionAsync(
  client: DatabaseClient,
  accountId: string
): Promise<number> {
  const normalizedAccountId = requiredAccountId(accountId)
  const table = client.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_versions')
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  const existing = await client.one<VersionRow>(`SELECT current_version FROM ${table} WHERE account_id = ?${lockClause}`, [normalizedAccountId])
  const next = existing ? checkedNextVersion(existing.current_version) : 1
  if (existing) {
    await client.execute(`UPDATE ${table} SET current_version = ?, reserved_at = ? WHERE account_id = ?`, [next, nowIso(), normalizedAccountId])
  } else {
    await client.execute(`INSERT INTO ${table} (account_id, current_version, reserved_at) VALUES (?, ?, ?)`, [normalizedAccountId, next, nowIso()])
  }
  return next
}

export function currentAccountHealthJobsInputVersion(
  accountId: string,
  database: DatabaseSync = getBusinessDatabase()
): number | undefined {
  const row = database.prepare('SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = ?').get(requiredAccountId(accountId)) as VersionRow | undefined
  return row ? checkedVersion(row.current_version) : undefined
}

export async function currentAccountHealthJobsInputVersionAsync(
  client: DatabaseClient,
  accountId: string
): Promise<number | undefined> {
  const row = await client.one<VersionRow>(`SELECT current_version FROM ${client.dialect.qualifyTable('juhe_business', 'account_health_jobs_input_versions')} WHERE account_id = ?`, [requiredAccountId(accountId)])
  return row ? checkedVersion(row.current_version) : undefined
}

export async function currentAccountHealthJobsInputVersionForRuntimeAsync(accountId: string): Promise<number | undefined> {
  if (runtimeConfig.databaseDriver === 'sqlite') return currentAccountHealthJobsInputVersion(accountId)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await currentAccountHealthJobsInputVersionAsync(client, accountId)
}

function reserveInSqlite(accountId: string, database: DatabaseSync): number {
  const existing = database.prepare('SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = ?').get(accountId) as VersionRow | undefined
  const next = existing ? checkedNextVersion(existing.current_version) : 1
  if (existing) {
    database.prepare('UPDATE account_health_jobs_input_versions SET current_version = ?, reserved_at = ? WHERE account_id = ?').run(next, nowIso(), accountId)
  } else {
    database.prepare('INSERT INTO account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES (?, ?, ?)').run(accountId, next, nowIso())
  }
  return next
}

function requiredAccountId(value: string): string {
  const accountId = value.trim()
  if (!accountId) throw new Error('J1 snapshot version 缺少 account ID')
  return accountId
}

function checkedNextVersion(value: VersionRow['current_version']): number {
  const current = checkedVersion(value)
  if (current >= Number.MAX_SAFE_INTEGER) throw new Error('J1 snapshot version 已达到安全整数上限')
  return current + 1
}

function checkedVersion(value: VersionRow['current_version']): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 1) throw new Error('J1 snapshot version 存储损坏')
  return normalized
}
