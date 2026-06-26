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

type OperationLogInsertStatement = ReturnType<ReturnType<typeof getDatasetDatabase>['prepare']>

interface OperationLogInsertStatements {
  insertLog: OperationLogInsertStatement
  insertTarget: OperationLogInsertStatement
  insertViewer: OperationLogInsertStatement
  insertSearchTerm: OperationLogInsertStatement
}

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
    for (const prepared of preparedLogs) {
      await insertPreparedOperationLogPostgres(tx, prepared)
    }
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

async function insertPreparedOperationLogPostgres(client: DatabaseClient, prepared: PreparedOperationLogInput): Promise<void> {
  const input = prepared.input
  await client.execute(`
    INSERT INTO juhe_dataset.operation_logs (
      id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
      operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
      resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
      status_code, client_ip, user_agent, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
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
  ])

  await insertPostgresOperationLogTargets(client, prepared)
  await insertPostgresOperationLogViewers(client, prepared)
  await insertPostgresOperationLogSearchTerms(client, prepared)
}

async function insertPostgresOperationLogTargets(client: DatabaseClient, prepared: PreparedOperationLogInput): Promise<void> {
  if (prepared.targets.length === 0) return
  const params: unknown[] = []
  const rows = prepared.targets.map((target) => {
    params.push(
      newId('optgt'),
      prepared.id,
      target.targetType,
      target.targetId ?? null,
      target.targetName ?? null,
      target.targetOwnerSystemAccountId ?? null,
      target.relation ?? 'affected',
      prepared.createdAt
    )
    return '(?, ?, ?, ?, ?, ?, ?, ?)'
  })
  await client.execute(`
    INSERT INTO juhe_dataset.operation_log_targets (
      id, operation_log_id, target_type, target_id, target_name, target_owner_system_account_id, relation, created_at
    ) VALUES ${rows.join(', ')}
  `, params)
}

async function insertPostgresOperationLogViewers(client: DatabaseClient, prepared: PreparedOperationLogInput): Promise<void> {
  if (prepared.viewers.length === 0) return
  const params: unknown[] = []
  const rows = prepared.viewers.map((viewer) => {
    params.push(
      prepared.id,
      viewer.systemAccountId,
      viewer.visibilityReason,
      viewer.detailLevel ?? prepared.detailLevel,
      prepared.createdAt
    )
    return '(?, ?, ?, ?, ?)'
  })
  await client.execute(`
    INSERT INTO juhe_dataset.operation_log_viewers (
      operation_log_id, system_account_id, visibility_reason, detail_level, created_at
    ) VALUES ${rows.join(', ')}
    ON CONFLICT(operation_log_id, system_account_id, visibility_reason) DO NOTHING
  `, params)
}

async function insertPostgresOperationLogSearchTerms(client: DatabaseClient, prepared: PreparedOperationLogInput): Promise<void> {
  const terms = buildOperationLogSearchTerms(prepared.input.summary)
  if (terms.length === 0) return
  const params: unknown[] = []
  const rows = terms.map((term) => {
    params.push(prepared.id, term, prepared.createdAt)
    return '(?, ?, ?)'
  })
  await client.execute(`
    INSERT INTO juhe_dataset.operation_log_summary_search_terms (
      operation_log_id, term, created_at
    ) VALUES ${rows.join(', ')}
    ON CONFLICT(term, operation_log_id) DO NOTHING
  `, params)
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
