import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getDatasetDatabase } from './database.js'
import {
  operationLogDetailSupplementFromRow,
  type OperationLogRow
} from './operation-log-mappers.js'
import type {
  OperationLogDetailSupplement,
  OperationLogDetailTarget,
  OperationLogDetailViewer,
  OperationLogDetailLevel,
  OperationLogVisibilityReason
} from './operation-log-types.js'
import { getPostgresPool } from './postgres-client.js'
import {
  loadSystemAccountNameMapByIds,
  loadSystemAccountNameMapByIdsAsync
} from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalString } from './value-utils.js'

export function getOperationLogDetailSupplement(id: string): OperationLogDetailSupplement | undefined {
  const database = getDatasetDatabase()
  const row = database
    .prepare(`
      SELECT ${operationLogAdminDetailSelectColumns('ol')}
      FROM operation_logs ol
      WHERE ol.id = ?
      LIMIT 1
    `)
    .get(id) as OperationLogRow | undefined
  if (!row) return undefined

  const targetRows = loadTargetRows(id)
  const viewerRows = loadViewerRows(id)
  return buildSupplement(row, targetRows, viewerRows, loadSystemAccountNames([
    ...targetRows.map((target) => optionalString(target.target_owner_system_account_id)),
    ...viewerRows.map((viewer) => optionalString(viewer.system_account_id))
  ]))
}

export async function getOperationLogDetailSupplementAsync(id: string): Promise<OperationLogDetailSupplement | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_operation_log_detail_supplement_read_only', id })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getOperationLogDetailSupplement(id)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<OperationLogRow>(`
    SELECT ${operationLogAdminDetailSelectColumns('ol')}
    FROM juhe_dataset.operation_logs ol
    WHERE ol.id = ?
    LIMIT 1
  `, [id])
  if (!row) return undefined

  const [targetRows, viewerRows] = await Promise.all([
    loadTargetRowsAsync(client, id),
    loadViewerRowsAsync(client, id)
  ])
  return buildSupplement(row, targetRows, viewerRows, await loadSystemAccountNamesAsync(client, [
    ...targetRows.map((target) => optionalString(target.target_owner_system_account_id)),
    ...viewerRows.map((viewer) => optionalString(viewer.system_account_id))
  ]))
}

export function getOperationLogDetailSupplementForViewer(
  id: string,
  systemAccountId: string
): OperationLogDetailSupplement | undefined {
  const database = getDatasetDatabase()
  const row = database
    .prepare(`
      SELECT ${operationLogViewerBaseSelectColumns('ol')},
             ${sqliteEffectiveViewerDetailLevelExpression}
      FROM operation_logs ol
      WHERE ol.id = ?
      AND (
        ol.visibility_scope = 'all_users'
        OR (
          ol.visibility_scope = 'targeted'
          AND EXISTS (
            SELECT 1
            FROM operation_log_viewers authorized_viewer
            WHERE authorized_viewer.operation_log_id = ol.id
            AND authorized_viewer.system_account_id = ?
          )
        )
      )
      LIMIT 1
    `)
    .get(systemAccountId, id, systemAccountId) as OperationLogRow | undefined
  if (!row) return undefined
  if (row.effective_detail_level === 'summary') {
    return operationLogDetailSupplementFromRow(row)
  }

  const payloadRow = database
    .prepare(`
      SELECT changes_json, method, path
      FROM operation_logs
      WHERE id = ?
      LIMIT 1
    `)
    .get(id) as OperationLogRow
  const targetRows = loadTargetRows(id)
  return buildSupplement(
    { ...row, ...payloadRow },
    targetRows,
    [],
    loadSystemAccountNames(targetRows.map((target) => optionalString(target.target_owner_system_account_id)))
  )
}

export async function getOperationLogDetailSupplementForViewerAsync(
  id: string,
  systemAccountId: string
): Promise<OperationLogDetailSupplement | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_operation_log_detail_supplement_for_viewer_read_only',
      id,
      systemAccountId
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getOperationLogDetailSupplementForViewer(id, systemAccountId)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<OperationLogRow>(`
    SELECT ${operationLogViewerBaseSelectColumns('ol')},
           ${postgresEffectiveViewerDetailLevelExpression}
    FROM juhe_dataset.operation_logs ol
    WHERE ol.id = ?
    AND (
      ol.visibility_scope = 'all_users'
      OR (
        ol.visibility_scope = 'targeted'
        AND EXISTS (
          SELECT 1
          FROM juhe_dataset.operation_log_viewers authorized_viewer
          WHERE authorized_viewer.operation_log_id = ol.id
          AND authorized_viewer.system_account_id = ?
        )
      )
    )
    LIMIT 1
  `, [systemAccountId, id, systemAccountId])
  if (!row) return undefined
  if (row.effective_detail_level === 'summary') {
    return operationLogDetailSupplementFromRow(row)
  }

  const [payloadRow, targetRows] = await Promise.all([
    client.one<OperationLogRow>(`
      SELECT changes_json, method, path
      FROM juhe_dataset.operation_logs
      WHERE id = ?
      LIMIT 1
    `, [id]),
    loadTargetRowsAsync(client, id)
  ])
  return buildSupplement(
    { ...row, ...payloadRow },
    targetRows,
    [],
    await loadSystemAccountNamesAsync(
      client,
      targetRows.map((target) => optionalString(target.target_owner_system_account_id))
    )
  )
}

function operationLogAdminDetailSelectColumns(alias: string): string {
  return [
    'operation_key',
    'resource_type',
    'resource_id',
    'resource_name',
    'visibility_scope',
    'changes_json',
    'method',
    'path',
    'client_ip'
  ].map((column) => `${alias}.${column}`).join(', ')
}

function operationLogViewerBaseSelectColumns(alias: string): string {
  return [
    'operation_key',
    'resource_type',
    'resource_id',
    'resource_name',
    'visibility_scope'
  ].map((column) => `${alias}.${column}`).join(', ')
}

const sqliteEffectiveViewerDetailLevelExpression = `
  CASE
    WHEN ol.visibility_scope = 'targeted'
    AND ol.detail_level = 'full'
    AND EXISTS (
      SELECT 1
      FROM operation_log_viewers full_viewer
      WHERE full_viewer.operation_log_id = ol.id
      AND full_viewer.system_account_id = ?
      AND full_viewer.detail_level = 'full'
    ) THEN 'full'
    ELSE 'summary'
  END AS effective_detail_level
`

const postgresEffectiveViewerDetailLevelExpression = sqliteEffectiveViewerDetailLevelExpression
  .replaceAll('FROM operation_log_viewers', 'FROM juhe_dataset.operation_log_viewers')

function buildSupplement(
  row: OperationLogRow,
  targetRows: OperationLogRow[],
  viewerRows: OperationLogRow[],
  systemAccountNames: Map<string, string>
): OperationLogDetailSupplement {
  return {
    ...operationLogDetailSupplementFromRow(row),
    targets: targetRows.map((target) => operationLogDetailTargetFromRow(target, systemAccountNames)),
    viewers: viewerRows.map((viewer) => operationLogDetailViewerFromRow(viewer, systemAccountNames))
  }
}

function operationLogDetailTargetFromRow(
  row: OperationLogRow,
  systemAccountNames: Map<string, string>
): OperationLogDetailTarget {
  const ownerId = optionalString(row.target_owner_system_account_id)
  return {
    id: String(row.id),
    targetType: String(row.target_type),
    targetId: optionalString(row.target_id),
    targetName: optionalString(row.target_name),
    targetOwnerSystemAccountName: ownerId ? systemAccountNames.get(ownerId) : undefined,
    relation: String(row.relation)
  }
}

function operationLogDetailViewerFromRow(
  row: OperationLogRow,
  systemAccountNames: Map<string, string>
): OperationLogDetailViewer {
  const systemAccountId = String(row.system_account_id)
  return {
    systemAccountId,
    systemAccountName: systemAccountNames.get(systemAccountId),
    visibilityReason: String(row.visibility_reason) as OperationLogVisibilityReason,
    detailLevel: String(row.detail_level) as OperationLogDetailLevel
  }
}

function loadTargetRows(id: string): OperationLogRow[] {
  return getDatasetDatabase()
    .prepare(`
      SELECT id, target_type, target_id, target_name, target_owner_system_account_id, relation
      FROM operation_log_targets
      WHERE operation_log_id = ?
      ORDER BY created_at ASC, id ASC
    `)
    .all(id) as OperationLogRow[]
}

function loadViewerRows(id: string): OperationLogRow[] {
  return getDatasetDatabase()
    .prepare(`
      SELECT system_account_id, visibility_reason, detail_level
      FROM operation_log_viewers
      WHERE operation_log_id = ?
      ORDER BY created_at ASC, system_account_id ASC
    `)
    .all(id) as OperationLogRow[]
}

async function loadTargetRowsAsync(client: DatabaseClient, id: string): Promise<OperationLogRow[]> {
  return client.query<OperationLogRow>(`
    SELECT id, target_type, target_id, target_name, target_owner_system_account_id, relation
    FROM juhe_dataset.operation_log_targets
    WHERE operation_log_id = ?
    ORDER BY created_at ASC, id ASC
  `, [id])
}

async function loadViewerRowsAsync(client: DatabaseClient, id: string): Promise<OperationLogRow[]> {
  return client.query<OperationLogRow>(`
    SELECT system_account_id, visibility_reason, detail_level
    FROM juhe_dataset.operation_log_viewers
    WHERE operation_log_id = ?
    ORDER BY created_at ASC, system_account_id ASC
  `, [id])
}

function loadSystemAccountNames(ids: Array<string | undefined>): Map<string, string> {
  const uniqueIds = [...new Set(ids.filter((id): id is string => Boolean(id?.trim())))]
  if (uniqueIds.length === 0) return new Map()
  return loadSystemAccountNameMapByIds(uniqueIds)
}

async function loadSystemAccountNamesAsync(
  client: DatabaseClient,
  ids: Array<string | undefined>
): Promise<Map<string, string>> {
  return loadSystemAccountNameMapByIdsAsync(client, ids)
}
