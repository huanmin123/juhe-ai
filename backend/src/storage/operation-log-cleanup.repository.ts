import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, rollbackDatabaseTransaction } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import type { OperationLogRow } from './operation-log-mappers.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export function cleanupOperationLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  const database = getDatasetDatabase()
  const rows = database
    .prepare('SELECT id FROM operation_logs WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?')
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as OperationLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = sqlPlaceholders(ids.length)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const result = database.prepare(`DELETE FROM operation_logs WHERE id IN (${placeholders})`).run(...ids)
    commitDatabaseTransaction(database, transactionStarted)
    return Number(result.changes ?? 0)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export async function cleanupOperationLogsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupOperationLogsBefore(cutoffCreatedAt, limit)
  }
  const batchLimit = Math.max(1, Math.trunc(limit))
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<OperationLogRow>(`
    SELECT id
    FROM juhe_dataset.operation_logs
    WHERE created_at < ?
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [cutoffCreatedAt, batchLimit])
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  let deleted = 0
  await client.transaction(async (tx) => {
    for (const chunk of chunkValues(ids, 10000)) {
      deleted += Number((await tx.execute('DELETE FROM juhe_dataset.operation_logs WHERE id = ANY(?::text[])', [chunk])).changes ?? 0)
    }
  })
  return deleted
}
