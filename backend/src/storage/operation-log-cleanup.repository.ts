import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, rollbackDatabaseTransaction } from './database.js'
import type { OperationLogRow } from './operation-log-mappers.js'
import { sqlPlaceholders } from './query-utils.js'

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
