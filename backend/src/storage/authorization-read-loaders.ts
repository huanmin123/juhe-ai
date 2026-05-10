import type {
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceSummary,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import { getDatabase } from './database.js'
import { sqlPlaceholders } from './query-utils.js'

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

export function loadResourceAuthorizationStatsByResourceIds(resourceType: ResourceAuthorizationResourceType, resourceIds: string[]): Map<string, ResourceAuthorizationStats> {
  const ids = uniqueIds(resourceIds)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`
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
      AND ra.resource_id IN (${sqlPlaceholders(ids.length)})
    GROUP BY ra.resource_id
  `).all(resourceType, ...ids) as unknown as Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }>
  return new Map(rows.map((row) => [row.resource_id, {
    authorizationCount: Number(row.authorization_count ?? 0),
    authorizationTeamCount: Number(row.authorization_team_count ?? 0)
  }]))
}

export function loadResourceAuthorizationSourcesByAuthorizationIds(authorizationIds: string[]): Map<string, ResourceAuthorizationSourceSummary[]> {
  const ids = uniqueIds(authorizationIds)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`
    SELECT ras.*, system_teams.name AS team_name
    FROM resource_authorization_sources ras
    LEFT JOIN system_teams ON system_teams.id = ras.source_team_id
    WHERE ras.authorization_id IN (${sqlPlaceholders(ids.length)})
    ORDER BY ras.status ASC, ras.created_at ASC, ras.id ASC
  `).all(...ids) as unknown as Array<ResourceAuthorizationSourceRow & { team_name?: string | null }>
  const result = new Map<string, ResourceAuthorizationSourceSummary[]>()
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
    result.set(row.authorization_id, [...(result.get(row.authorization_id) ?? []), summary])
  }
  return result
}
