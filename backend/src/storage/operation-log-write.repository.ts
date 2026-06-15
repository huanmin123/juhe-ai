import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, rollbackDatabaseTransaction } from './database.js'
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

type OperationLogInsertStatement = ReturnType<ReturnType<typeof getDatasetDatabase>['prepare']>

interface OperationLogInsertStatements {
  insertLog: OperationLogInsertStatement
  insertTarget: OperationLogInsertStatement
  insertViewer: OperationLogInsertStatement
  insertSearchTerm: OperationLogInsertStatement
}

export function createOperationLog(input: OperationLogInput): OperationLogSummary {
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

export function createOperationLogsBatch(inputs: OperationLogInput[]): void {
  if (inputs.length === 0) return

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
