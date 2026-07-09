import { getStatsDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'

export interface DataArchiveManifestInput {
  domain: string
  databaseRole: string
  sourceTable: string
  archiveAction: string
  storageUri: string
  partitionName?: string
  rangeStart?: string
  rangeEnd?: string
  rowCount?: number
  sizeBytes?: number
  status?: 'archived' | 'deleted'
  manifest?: Record<string, unknown>
  archivedAt?: string
}

export function recordDataArchiveManifest(input: DataArchiveManifestInput): string {
  const id = newId('archive')
  const now = nowIso()
  const archivedAt = input.archivedAt ?? now
  getStatsDatabase().prepare(`
    INSERT INTO data_archive_manifests (
      id, domain, database_role, source_table, archive_action, storage_uri,
      partition_name, range_start, range_end, row_count, size_bytes, status,
      manifest_json, archived_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    input.domain,
    input.databaseRole,
    input.sourceTable,
    input.archiveAction,
    input.storageUri,
    input.partitionName ?? null,
    input.rangeStart ?? null,
    input.rangeEnd ?? null,
    Math.max(0, Math.trunc(input.rowCount ?? 0)),
    input.sizeBytes === undefined ? null : Math.max(0, Math.trunc(input.sizeBytes)),
    input.status ?? 'archived',
    JSON.stringify(input.manifest ?? {}),
    archivedAt,
    now,
    now
  )
  return id
}

export async function recordDataArchiveManifestAsync(client: DatabaseClient, input: DataArchiveManifestInput): Promise<string> {
  const id = newId('archive')
  const now = nowIso()
  const archivedAt = input.archivedAt ?? now
  await client.execute(`
    INSERT INTO juhe_stats.data_archive_manifests (
      id, domain, database_role, source_table, archive_action, storage_uri,
      partition_name, range_start, range_end, row_count, size_bytes, status,
      manifest_json, archived_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    id,
    input.domain,
    input.databaseRole,
    input.sourceTable,
    input.archiveAction,
    input.storageUri,
    input.partitionName ?? null,
    input.rangeStart ?? null,
    input.rangeEnd ?? null,
    Math.max(0, Math.trunc(input.rowCount ?? 0)),
    input.sizeBytes === undefined ? null : Math.max(0, Math.trunc(input.sizeBytes)),
    input.status ?? 'archived',
    JSON.stringify(input.manifest ?? {}),
    archivedAt,
    now,
    now
  ])
  return id
}
