import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { buildOperationLogSearchTerms } from './operation-log-search.js'
import type {
  OperationLogInput,
  OperationLogSummary
} from './operation-log-types.js'
import {
  operationLogSummaryFromPrepared,
  prepareOperationLogInput,
  type PreparedOperationLogInput
} from './operation-log-write-input.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'

type OperationLogInsertStatement = ReturnType<ReturnType<typeof getDatasetDatabase>['prepare']>

interface OperationLogInsertStatements {
  insertLog: OperationLogInsertStatement
  insertTarget: OperationLogInsertStatement
  insertViewer: OperationLogInsertStatement
  insertSearchTerm: OperationLogInsertStatement
}

const postgresOperationLogRowsPerInsert = 1000
const postgresOperationLogTargetRowsPerInsert = 4000
const postgresOperationLogViewerRowsPerInsert = 6000
const postgresOperationLogSearchTermRowsPerInsert = 5000

export function createOperationLog(input: OperationLogInput): OperationLogSummary {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式下操作日志写入必须使用 createOperationLogAsync')
  }
  const database = getDatasetDatabase()
  const prepared = prepareOperationLogInput(input)
  const statements = prepareOperationLogInsertStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    insertPreparedOperationLog(statements, prepared)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }

  return operationLogSummaryFromPrepared(prepared)
}

export async function createOperationLogAsync(input: OperationLogInput): Promise<OperationLogSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createOperationLog(input)
  }
  const prepared = prepareOperationLogInput(input)
  await createPreparedOperationLogsBatchPostgres([prepared])
  return operationLogSummaryFromPrepared(prepared)
}

export function createOperationLogsBatch(inputs: OperationLogInput[]): void {
  if (inputs.length === 0) return
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式下操作日志批量写入必须使用 createOperationLogsBatchAsync')
  }

  const database = getDatasetDatabase()
  const statements = prepareOperationLogInsertStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      insertPreparedOperationLog(statements, prepareOperationLogInput(input))
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export async function createOperationLogsBatchAsync(inputs: OperationLogInput[]): Promise<void> {
  if (inputs.length === 0) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    createOperationLogsBatch(inputs)
    return
  }
  await createPreparedOperationLogsBatchPostgres(inputs.map(prepareOperationLogInput))
}

async function createPreparedOperationLogsBatchPostgres(preparedLogs: PreparedOperationLogInput[]): Promise<void> {
  if (preparedLogs.length === 0) return
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    const insertedLogIds = await insertPostgresOperationLogsBatch(tx, preparedLogs)
    const insertedLogs = preparedLogs.filter((prepared) => insertedLogIds.has(prepared.id))
    await insertPostgresOperationLogTargetsBatch(tx, insertedLogs)
    await insertPostgresOperationLogViewersBatch(tx, insertedLogs)
    await insertPostgresOperationLogSearchTermsBatch(tx, insertedLogs)
  })
}

function prepareOperationLogInsertStatements(database: ReturnType<typeof getDatasetDatabase>): OperationLogInsertStatements {
  return {
    insertLog: database.prepare(`
      INSERT INTO operation_logs (
        id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
        operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
        resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
        status_code, client_ip, user_agent, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `),
    insertTarget: database.prepare(`
      INSERT INTO operation_log_targets (
        id, operation_log_id, target_type, target_id, target_name, target_owner_system_account_id, relation, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `),
    insertViewer: database.prepare(`
      INSERT OR IGNORE INTO operation_log_viewers (
        operation_log_id, system_account_id, visibility_reason, detail_level, created_at
      ) VALUES (?, ?, ?, ?, ?)
    `),
    insertSearchTerm: database.prepare(`
      INSERT OR IGNORE INTO operation_log_summary_search_terms (
        operation_log_id, term, created_at
      ) VALUES (?, ?, ?)
    `)
  }
}

async function insertPostgresOperationLogsBatch(client: DatabaseClient, preparedLogs: PreparedOperationLogInput[]): Promise<Set<string>> {
  const insertedIds = new Set<string>()
  if (preparedLogs.length === 0) return insertedIds
  for (const chunk of chunkValues(preparedLogs, postgresOperationLogRowsPerInsert)) {
    const params = chunk.flatMap((prepared) => {
      const input = prepared.input
      return [
        prepared.id,
        input.traceId ?? null,
        input.actorSystemAccountId,
        input.actorUsername ?? null,
        input.actorDisplayName ?? null,
        input.actorRole,
        input.operationScopeSystemAccountId ?? null,
        input.mode ?? 'self',
        input.module,
        input.action,
        input.operationKey,
        input.resourceType,
        input.resourceId ?? null,
        input.resourceName ?? null,
        input.summary,
        prepared.detailLevel,
        prepared.visibilityScope,
        prepared.changesJson,
        prepared.metadataJson,
        input.method ?? null,
        input.path ?? null,
        integerOrNull(input.statusCode),
        input.clientIp ?? null,
        input.userAgent ?? null,
        prepared.createdAt
      ]
    })
    const rows = await client.query<{ id: string }>(`
    INSERT INTO juhe_dataset.operation_logs (
      id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
      operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
      resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
      status_code, client_ip, user_agent, created_at
    ) VALUES ${postgresMultiRowPlaceholders(chunk.length, 25)}
    ON CONFLICT(id) DO NOTHING
    RETURNING id
  `, params)
    for (const row of rows) {
      insertedIds.add(row.id)
    }
  }
  return insertedIds
}

async function insertPostgresOperationLogTargetsBatch(client: DatabaseClient, preparedLogs: PreparedOperationLogInput[]): Promise<void> {
  const rows = preparedLogs.flatMap((prepared) => prepared.targets.map((target) => [
    newId('optgt'),
    prepared.id,
    target.targetType,
    target.targetId ?? null,
    target.targetName ?? null,
    target.targetOwnerSystemAccountId ?? null,
    target.relation ?? 'affected',
    prepared.createdAt
  ]))
  for (const chunk of chunkValues(rows, postgresOperationLogTargetRowsPerInsert)) {
    if (chunk.length === 0) continue
    await client.execute(`
    INSERT INTO juhe_dataset.operation_log_targets (
      id, operation_log_id, target_type, target_id, target_name, target_owner_system_account_id, relation, created_at
    ) VALUES ${postgresMultiRowPlaceholders(chunk.length, 8)}
  `, chunk.flat())
  }
}

async function insertPostgresOperationLogViewersBatch(client: DatabaseClient, preparedLogs: PreparedOperationLogInput[]): Promise<void> {
  const rows = preparedLogs.flatMap((prepared) => prepared.viewers.map((viewer) => [
    prepared.id,
    viewer.systemAccountId,
    viewer.visibilityReason,
    viewer.detailLevel ?? prepared.detailLevel,
    prepared.createdAt
  ]))
  for (const chunk of chunkValues(rows, postgresOperationLogViewerRowsPerInsert)) {
    if (chunk.length === 0) continue
    await client.execute(`
    INSERT INTO juhe_dataset.operation_log_viewers (
      operation_log_id, system_account_id, visibility_reason, detail_level, created_at
    ) VALUES ${postgresMultiRowPlaceholders(chunk.length, 5)}
    ON CONFLICT(operation_log_id, system_account_id, visibility_reason) DO NOTHING
  `, chunk.flat())
  }
}

async function insertPostgresOperationLogSearchTermsBatch(client: DatabaseClient, preparedLogs: PreparedOperationLogInput[]): Promise<void> {
  const params: unknown[] = []
  let rowCount = 0
  const flush = async (): Promise<void> => {
    if (rowCount === 0) return
    await client.execute(`
    INSERT INTO juhe_dataset.operation_log_summary_search_terms (
      operation_log_id, term, created_at
    ) VALUES ${postgresMultiRowPlaceholders(rowCount, 3)}
    ON CONFLICT(term, operation_log_id) DO NOTHING
  `, params)
    params.length = 0
    rowCount = 0
  }
  for (const prepared of preparedLogs) {
    for (const term of buildOperationLogSearchTerms(prepared.input.summary)) {
      params.push(prepared.id, term, prepared.createdAt)
      rowCount += 1
      if (rowCount >= postgresOperationLogSearchTermRowsPerInsert) {
        await flush()
      }
    }
  }
  await flush()
}

function insertPreparedOperationLog(statements: OperationLogInsertStatements, prepared: PreparedOperationLogInput): void {
  const input = prepared.input
  statements.insertLog.run(
    prepared.id,
    input.traceId ?? null,
    input.actorSystemAccountId,
    input.actorUsername ?? null,
    input.actorDisplayName ?? null,
    input.actorRole,
    input.operationScopeSystemAccountId ?? null,
    input.mode ?? 'self',
    input.module,
    input.action,
    input.operationKey,
    input.resourceType,
    input.resourceId ?? null,
    input.resourceName ?? null,
    input.summary,
    prepared.detailLevel,
    prepared.visibilityScope,
    prepared.changesJson,
    prepared.metadataJson,
    input.method ?? null,
    input.path ?? null,
    integerOrNull(input.statusCode),
    input.clientIp ?? null,
    input.userAgent ?? null,
    prepared.createdAt
  )

  for (const target of prepared.targets) {
    statements.insertTarget.run(
      newId('optgt'),
      prepared.id,
      target.targetType,
      target.targetId ?? null,
      target.targetName ?? null,
      target.targetOwnerSystemAccountId ?? null,
      target.relation ?? 'affected',
      prepared.createdAt
    )
  }

  for (const viewer of prepared.viewers) {
    statements.insertViewer.run(
      prepared.id,
      viewer.systemAccountId,
      viewer.visibilityReason,
      viewer.detailLevel ?? prepared.detailLevel,
      prepared.createdAt
    )
  }

  for (const term of buildOperationLogSearchTerms(prepared.input.summary)) {
    statements.insertSearchTerm.run(prepared.id, term, prepared.createdAt)
  }
}

function integerOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : null
}

function postgresMultiRowPlaceholders(rowCount: number, columnCount: number): string {
  const row = `(${Array.from({ length: columnCount }, () => '?').join(', ')})`
  return Array.from({ length: rowCount }, () => row).join(', ')
}
