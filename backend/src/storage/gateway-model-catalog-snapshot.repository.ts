import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export type GatewayModelCatalogProtocol = 'openai' | 'anthropic' | 'gemini'
export type GatewayModelCatalogVariant = 'default' | 'codex' | 'chat'

export interface GatewayModelCatalogSnapshot {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
  payload: object
  modelCount: number
  revision: string
  createdAt: string
  updatedAt: string
}

interface SnapshotRow {
  system_account_id: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
  payload_json: string
  model_count: number | string | bigint
  revision: string
  created_at: string
  updated_at: string
}

export async function findGatewayModelCatalogSnapshotAsync(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): Promise<GatewayModelCatalogSnapshot | undefined> {
  const client = await snapshotClient()
  const row = await client.one<SnapshotRow>(`
    SELECT system_account_id, protocol, variant, payload_json, model_count, revision, created_at, updated_at
    FROM ${snapshotTable(client)}
    WHERE system_account_id = ? AND protocol = ? AND variant = ?
    LIMIT 1
  `, [input.systemAccountId, input.protocol, input.variant])
  return row ? snapshotFromRow(row) : undefined
}

export function findGatewayModelCatalogSnapshot(input: {
  systemAccountId: string
  protocol: GatewayModelCatalogProtocol
  variant: GatewayModelCatalogVariant
}): GatewayModelCatalogSnapshot | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT system_account_id, protocol, variant, payload_json, model_count, revision, created_at, updated_at
    FROM gateway_model_catalog_snapshots
    WHERE system_account_id = ? AND protocol = ? AND variant = ?
    LIMIT 1
  `).get(input.systemAccountId, input.protocol, input.variant) as unknown as SnapshotRow | undefined
  return row ? snapshotFromRow(row) : undefined
}

export async function listGatewayModelCatalogSnapshotsAsync(systemAccountId?: string): Promise<GatewayModelCatalogSnapshot[]> {
  const client = await snapshotClient()
  const rows = await client.query<SnapshotRow>(`
    SELECT system_account_id, protocol, variant, payload_json, model_count, revision, created_at, updated_at
    FROM ${snapshotTable(client)}
    ${systemAccountId === undefined ? '' : 'WHERE system_account_id = ?'}
    ORDER BY system_account_id, protocol, variant
  `, systemAccountId === undefined ? [] : [systemAccountId])
  return rows.map(snapshotFromRow)
}

export function listGatewayModelCatalogSnapshots(systemAccountId?: string): GatewayModelCatalogSnapshot[] {
  const rows = getBusinessDatabase().prepare(`
    SELECT system_account_id, protocol, variant, payload_json, model_count, revision, created_at, updated_at
    FROM gateway_model_catalog_snapshots
    ${systemAccountId === undefined ? '' : 'WHERE system_account_id = ?'}
    ORDER BY system_account_id, protocol, variant
  `).all(...(systemAccountId === undefined ? [] : [systemAccountId])) as unknown as SnapshotRow[]
  return rows.map(snapshotFromRow)
}

export async function listGatewayModelCatalogSystemAccountIdsAsync(): Promise<string[]> {
  const client = await snapshotClient()
  const rows = await client.query<{ id: string }>(`
    SELECT id FROM ${client.driver === 'postgres'
      ? client.dialect.qualifyTable('juhe_business', 'system_accounts')
      : client.dialect.quoteIdentifier('system_accounts')}
    WHERE status = 'active'
    ORDER BY id
  `)
  return rows.map((row) => row.id)
}

export async function replaceGatewayModelCatalogSnapshotsAsync(
  systemAccountId: string,
  snapshots: Array<Pick<GatewayModelCatalogSnapshot, 'protocol' | 'variant' | 'payload' | 'modelCount' | 'revision'>>
): Promise<GatewayModelCatalogSnapshot[]> {
  const client = await snapshotClient()
  const now = nowIso()
  await client.transaction(async (tx) => {
    await tx.execute(`
      DELETE FROM ${snapshotTable(tx)}
      WHERE system_account_id = ?
    `, [systemAccountId])
    for (const snapshot of snapshots) {
      await tx.execute(`
        INSERT INTO ${snapshotTable(tx)} (
          system_account_id, protocol, variant, payload_json, model_count, revision, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(system_account_id, protocol, variant) DO UPDATE SET
          payload_json = excluded.payload_json,
          model_count = excluded.model_count,
          revision = excluded.revision,
          updated_at = excluded.updated_at
      `, [
        systemAccountId,
        snapshot.protocol,
        snapshot.variant,
        JSON.stringify(snapshot.payload),
        snapshot.modelCount,
        snapshot.revision,
        now,
        now
      ])
    }
  })
  return await listGatewayModelCatalogSnapshotsAsync(systemAccountId)
}

export async function pruneGatewayModelCatalogSnapshotsAsync(activeSystemAccountIds: string[]): Promise<number> {
  const client = await snapshotClient()
  const keep = ['', ...new Set(activeSystemAccountIds)]
  const placeholders = keep.map(() => '?').join(', ')
  const result = await client.execute(`
    DELETE FROM ${snapshotTable(client)}
    WHERE system_account_id NOT IN (${placeholders})
  `, keep)
  return result.changes
}

async function snapshotClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function snapshotTable(client: DatabaseClient): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', 'gateway_model_catalog_snapshots')
    : client.dialect.quoteIdentifier('gateway_model_catalog_snapshots')
}

function snapshotFromRow(row: SnapshotRow): GatewayModelCatalogSnapshot {
  const payload = JSON.parse(row.payload_json) as unknown
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('网关模型目录快照 payload 无效')
  }
  return {
    systemAccountId: row.system_account_id,
    protocol: row.protocol,
    variant: row.variant,
    payload: payload as object,
    modelCount: Number(row.model_count),
    revision: row.revision,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}
