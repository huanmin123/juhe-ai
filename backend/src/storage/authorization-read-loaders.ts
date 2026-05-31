import type {
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceSummary,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import { createAppCache } from '../shared/cache.js'
import { getBusinessDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

interface ResourceAuthorizationSourceRow {
  id: string
  authorization_id: string
  source_type: ResourceAuthorizationSourceType
  source_team_id: string | null
  status: ResourceAuthorizationSourceStatus
  activated_at: string | null
  ended_at: string | null
  ended_reason: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

export interface ResourceAuthorizationStats {
  authorizationCount: number
  authorizationTeamCount: number
}

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

const authorizationStatsCache = createAppCache<string, ResourceAuthorizationStats>({
  name: 'lookup:resource-authorization-stats',
  max: 10_000,
  ttlMs: 5 * 60 * 1000,
  updateAgeOnGet: true
})

const authorizationSourcesCache = createAppCache<string, ResourceAuthorizationSourceSummary[]>({
  name: 'lookup:resource-authorization-sources',
  max: 10_000,
  ttlMs: 5 * 60 * 1000,
  updateAgeOnGet: true
})

const emptyAuthorizationStats: ResourceAuthorizationStats = {
  authorizationCount: 0,
  authorizationTeamCount: 0
}

export function loadResourceAuthorizationStatsByResourceIds(resourceType: ResourceAuthorizationResourceType, resourceIds: string[]): Map<string, ResourceAuthorizationStats> {
  const ids = uniqueIds(resourceIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationStats>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = authorizationStatsCache.get(resourceAuthorizationStatsCacheKey(resourceType, id))
    if (cached !== undefined) {
      result.set(id, cached)
    } else {
      missingIds.push(id)
    }
  }
  if (!missingIds.length) return result

  const rows: Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(missingIds, 900)) {
    rows.push(...database.prepare(`
      SELECT
        ra.resource_id,
        COUNT(DISTINCT ra.id) AS authorization_count,
        COUNT(DISTINCT CASE WHEN ras.source_type = 'team' AND ras.status = 'active' THEN ras.source_team_id END) AS authorization_team_count
      FROM resource_authorizations ra
      LEFT JOIN resource_authorization_sources ras
        ON ras.authorization_id = ra.id
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      WHERE ra.resource_type = ?
        AND ra.status = 'active'
        AND ra.resource_id IN (${sqlPlaceholders(chunk.length)})
      GROUP BY ra.resource_id
    `).all(resourceType, ...chunk) as unknown as Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }>)
  }
  const loaded = new Map(rows.map((row) => [row.resource_id, {
    authorizationCount: Number(row.authorization_count ?? 0),
    authorizationTeamCount: Number(row.authorization_team_count ?? 0)
  }]))
  for (const id of missingIds) {
    const stats = loaded.get(id) ?? emptyAuthorizationStats
    authorizationStatsCache.set(resourceAuthorizationStatsCacheKey(resourceType, id), stats)
    result.set(id, stats)
  }
  return result
}

export function loadResourceAuthorizationSourcesByAuthorizationIds(authorizationIds: string[]): Map<string, ResourceAuthorizationSourceSummary[]> {
  const ids = uniqueIds(authorizationIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationSourceSummary[]>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = authorizationSourcesCache.get(id)
    if (cached !== undefined) {
      result.set(id, [...cached])
    } else {
      missingIds.push(id)
    }
  }
  if (!missingIds.length) return result

  const rows: Array<ResourceAuthorizationSourceRow & { team_name?: string | null }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(missingIds, 900)) {
    rows.push(...database.prepare(`
      SELECT ${resourceAuthorizationSourceSelectColumns('ras')}, system_teams.name AS team_name
      FROM resource_authorization_sources ras
      LEFT JOIN system_teams ON system_teams.id = ras.source_team_id
      WHERE ras.authorization_id IN (${sqlPlaceholders(chunk.length)})
      ORDER BY ras.status ASC, ras.created_at ASC, ras.id ASC
    `).all(...chunk) as unknown as Array<ResourceAuthorizationSourceRow & { team_name?: string | null }>)
  }
  const loaded = new Map<string, ResourceAuthorizationSourceSummary[]>()
  for (const row of rows) {
    const summary: ResourceAuthorizationSourceSummary = {
      id: row.id,
      authorizationId: row.authorization_id,
      sourceType: row.source_type,
      sourceTeamId: row.source_team_id ?? undefined,
      sourceTeamName: row.team_name ?? undefined,
      status: row.status,
      activatedAt: row.activated_at ?? undefined,
      endedAt: row.ended_at ?? undefined,
      endedReason: row.ended_reason ?? undefined,
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      updatedAt: row.updated_at
    }
    loaded.set(row.authorization_id, [...(loaded.get(row.authorization_id) ?? []), summary])
  }
  for (const id of missingIds) {
    const sources = loaded.get(id) ?? []
    authorizationSourcesCache.set(id, sources)
    result.set(id, [...sources])
  }
  return result
}

export function invalidateResourceAuthorizationStatsCache(resourceType?: ResourceAuthorizationResourceType, resourceIds?: string | string[]): void {
  if (!resourceType || resourceIds === undefined) {
    authorizationStatsCache.clear()
    return
  }
  const ids = Array.isArray(resourceIds) ? resourceIds : [resourceIds]
  for (const id of uniqueIds(ids)) {
    authorizationStatsCache.delete(resourceAuthorizationStatsCacheKey(resourceType, id))
  }
}

export function invalidateResourceAuthorizationSourceCache(authorizationIds?: string | string[]): void {
  if (authorizationIds === undefined) {
    authorizationSourcesCache.clear()
    return
  }
  const ids = Array.isArray(authorizationIds) ? authorizationIds : [authorizationIds]
  for (const id of uniqueIds(ids)) {
    authorizationSourcesCache.delete(id)
  }
}

export function clearResourceAuthorizationLookupCaches(): void {
  authorizationStatsCache.clear()
  authorizationSourcesCache.clear()
}

function resourceAuthorizationStatsCacheKey(resourceType: ResourceAuthorizationResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`
}

function resourceAuthorizationSourceSelectColumns(alias: string): string {
  return [
    'id',
    'authorization_id',
    'source_type',
    'source_team_id',
    'status',
    'activated_at',
    'ended_at',
    'ended_reason',
    'created_by',
    'created_at',
    'revoked_by',
    'revoked_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}
