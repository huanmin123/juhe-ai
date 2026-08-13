import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getDatasetDatabase } from './database.js'
import { buildOperationLogSearchTerms } from './operation-log-search.js'
import { getPostgresPool } from './postgres-client.js'

interface OperationLogSearchCursorRow {
  id: string
  summary: string
  created_at: string
}

export interface OperationLogSearchRebuildResult {
  logCount: number
  termCount: number
  batchCount: number
}

export async function rebuildOperationLogSearchTermsSqlite(batchSize = 200): Promise<OperationLogSearchRebuildResult> {
  return rebuildOperationLogSearchTerms(createSqliteDatabaseClient(getDatasetDatabase()), batchSize)
}

export async function rebuildOperationLogSearchTermsPostgres(batchSize = 200): Promise<OperationLogSearchRebuildResult> {
  return rebuildOperationLogSearchTerms(createPostgresDatabaseClient(await getPostgresPool()), batchSize)
}

export async function rebuildOperationLogSearchTerms(client: DatabaseClient, batchSize = 200): Promise<OperationLogSearchRebuildResult> {
  const normalizedBatchSize = normalizeBatchSize(batchSize)
  const tablePrefix = client.driver === 'postgres' ? 'juhe_dataset.' : ''
  let cursorCreatedAt: string | undefined
  let cursorId: string | undefined
  let logCount = 0
  let termCount = 0
  let batchCount = 0

  for (;;) {
    const cursorClause = cursorCreatedAt && cursorId
      ? 'WHERE (created_at > ? OR (created_at = ? AND id > ?))'
      : ''
    const cursorParams = cursorCreatedAt && cursorId ? [cursorCreatedAt, cursorCreatedAt, cursorId] : []
    const rows = await client.query<OperationLogSearchCursorRow>(`
      SELECT id, summary, created_at
      FROM ${tablePrefix}operation_logs
      ${cursorClause}
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `, [...cursorParams, normalizedBatchSize])
    if (rows.length === 0) break

    const counts = await client.transaction(async (tx) => {
      const ids = rows.map((row) => row.id)
      await tx.execute(`
        DELETE FROM ${tablePrefix}operation_log_summary_search_terms
        WHERE operation_log_id IN (${tx.dialect.bindPlaceholders(ids.length)})
      `, ids)
      let insertedTerms = 0
      for (const row of rows) {
        const terms = buildOperationLogSearchTerms(row.summary)
        for (let offset = 0; offset < terms.length; offset += 200) {
          const chunk = terms.slice(offset, offset + 200)
          const valuesClause = Array.from({ length: chunk.length }, () => `(${tx.dialect.bindPlaceholders(3)})`).join(', ')
          const params = chunk.flatMap((term) => [row.id, term, row.created_at])
          const result = await tx.execute(client.driver === 'postgres'
            ? `INSERT INTO ${tablePrefix}operation_log_summary_search_terms (operation_log_id, term, created_at) VALUES ${valuesClause} ON CONFLICT(term, operation_log_id) DO NOTHING`
            : `INSERT OR IGNORE INTO ${tablePrefix}operation_log_summary_search_terms (operation_log_id, term, created_at) VALUES ${valuesClause}`,
          params)
          insertedTerms += result.changes
        }
      }
      return insertedTerms
    })

    const last = rows[rows.length - 1]
    cursorCreatedAt = last.created_at
    cursorId = last.id
    logCount += rows.length
    termCount += counts
    batchCount += 1
  }

  return { logCount, termCount, batchCount }
}

function normalizeBatchSize(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > 1000) {
    throw new Error('操作日志搜索词重建批大小必须是 1 到 1000 的整数')
  }
  return value
}
