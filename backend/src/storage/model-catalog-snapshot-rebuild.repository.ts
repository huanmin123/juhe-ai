import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

export type ModelCatalogSnapshotRebuildScope = 'all' | 'personal'

export interface ModelCatalogSnapshotRebuildRequest {
  scope: ModelCatalogSnapshotRebuildScope
  systemAccountId?: string
  generation: number
  updatedAt: string
}

interface ModelCatalogSnapshotRebuildRequestRow {
  scope?: unknown
  system_account_id?: unknown
  generation?: unknown
  updated_at?: unknown
}

export async function listPendingModelCatalogSnapshotRebuildRequestsAsync(): Promise<ModelCatalogSnapshotRebuildRequest[]> {
  const client = await modelCatalogSnapshotRebuildRequestClient()
  const rows = await client.query<ModelCatalogSnapshotRebuildRequestRow>(`
    SELECT scope, system_account_id, generation, updated_at
    FROM ${rebuildRequestTable(client)}
    ORDER BY CASE WHEN scope = 'all' THEN 0 ELSE 1 END, updated_at ASC, system_account_id ASC
  `)
  return rows.flatMap(modelCatalogSnapshotRebuildRequestFromRow)
}

export async function findPendingModelCatalogSnapshotRebuildRequestAsync(input: {
  scope: ModelCatalogSnapshotRebuildScope
  systemAccountId?: string
}): Promise<ModelCatalogSnapshotRebuildRequest | undefined> {
  const client = await modelCatalogSnapshotRebuildRequestClient()
  const owner = requestOwner(input.scope, input.systemAccountId)
  const row = await client.one<ModelCatalogSnapshotRebuildRequestRow>(`
    SELECT scope, system_account_id, generation, updated_at
    FROM ${rebuildRequestTable(client)}
    WHERE scope = ? AND system_account_id = ?
    LIMIT 1
  `, [input.scope, owner])
  return row ? modelCatalogSnapshotRebuildRequestFromRow(row)[0] : undefined
}

export async function ackModelCatalogSnapshotRebuildRequestAsync(
  request: Pick<ModelCatalogSnapshotRebuildRequest, 'scope' | 'systemAccountId' | 'generation'>
): Promise<boolean> {
  const client = await modelCatalogSnapshotRebuildRequestClient()
  const owner = requestOwner(request.scope, request.systemAccountId)
  const generation = requiredGeneration(request.generation)
  const result = await client.execute(`
    DELETE FROM ${rebuildRequestTable(client)}
    WHERE scope = ? AND system_account_id = ? AND generation = ?
  `, [request.scope, owner, generation])
  return result.changes === 1
}

async function modelCatalogSnapshotRebuildRequestClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function rebuildRequestTable(client: DatabaseClient): string {
  return client.dialect.qualifyTable('juhe_business', 'model_catalog_snapshot_rebuild_requests')
}

function requestOwner(scope: ModelCatalogSnapshotRebuildScope, systemAccountId: string | undefined): string {
  if (scope === 'all') {
    if (systemAccountId !== undefined) throw new Error('模型目录快照 all 重建请求不能包含 systemAccountId')
    return ''
  }
  if (typeof systemAccountId !== 'string' || systemAccountId.trim().length === 0) {
    throw new Error('模型目录快照 personal 重建请求缺少 systemAccountId')
  }
  return systemAccountId.trim()
}

function modelCatalogSnapshotRebuildRequestFromRow(row: ModelCatalogSnapshotRebuildRequestRow): ModelCatalogSnapshotRebuildRequest[] {
  if (row.scope !== 'all' && row.scope !== 'personal') return []
  const generation = Number(row.generation)
  if (!Number.isSafeInteger(generation) || generation < 1) return []
  const updatedAt = timestampString(row.updated_at)
  if (!updatedAt) return []

  if (row.scope === 'all') {
    if (row.system_account_id !== '') return []
    return [{ scope: 'all', generation, updatedAt }]
  }
  if (typeof row.system_account_id !== 'string' || row.system_account_id.trim().length === 0) return []
  return [{
    scope: 'personal',
    systemAccountId: row.system_account_id.trim(),
    generation,
    updatedAt
  }]
}

function requiredGeneration(value: unknown): number {
  const generation = Number(value)
  if (!Number.isSafeInteger(generation) || generation < 1) {
    throw new Error('模型目录快照重建请求 generation 无效')
  }
  return generation
}

function timestampString(value: unknown): string | undefined {
  if (typeof value === 'string' && value.length > 0) return value
  if (value instanceof Date && Number.isFinite(value.getTime())) return value.toISOString()
  return undefined
}
