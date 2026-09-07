import type {
  AuthorizationStatus,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceSummary,
  ResourceAuthorizationSourceType,
  ResourceAuthorizationSummary
} from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import type { ResourceAuthorizationGrantRow } from './repository-row-types.js'
import { canManageResourceOwner, sanitizeAuthorizationSourcesForViewer } from './resource-authorization-helpers.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

export function normalizeResourceType(value: unknown): ResourceAuthorizationResourceType | undefined {
  return value === 'account' || value === 'group' ? value : undefined
}

export function authorizationDirectionFilter(value: unknown): 'outbound' | 'inbound' | undefined {
  return value === 'outbound' || value === 'inbound' ? value : undefined
}

export function authorizationStatusFilter(value: unknown): AuthorizationStatus | undefined {
  return value === 'active' || value === 'paused' || value === 'expired' || value === 'revoked' || value === 'returned'
    ? value
    : undefined
}

export function resourceAuthorizationGrantSourceSummary(row: ResourceAuthorizationGrantRow, teamName: string | undefined): ResourceAuthorizationSourceSummary {
  const sourceType: ResourceAuthorizationSourceType = row.grantee_type === 'team' ? 'team' : 'manual'
  return {
    id: row.id,
    authorizationId: row.id,
    sourceType,
    sourceTeamId: row.grantee_team_id ?? undefined,
    sourceTeamName: teamName,
    status: row.status === 'active' || row.status === 'paused' ? 'active' as const : 'revoked' as const,
    activatedAt: row.created_at,
    endedAt: row.revoked_at ?? (row.status === 'expired' || row.status === 'revoked' || row.status === 'returned' ? row.updated_at : undefined),
    endedReason: row.status === 'expired'
      ? 'authorization_expired'
      : row.status === 'returned'
        ? 'grantee_returned'
        : row.status === 'revoked'
          ? 'authorization_revoked'
          : undefined,
    createdBy: row.created_by,
    createdAt: row.created_at,
    revokedBy: row.revoked_by ?? undefined,
    revokedAt: row.revoked_at ?? undefined,
    updatedAt: row.updated_at
  }
}

export function compareResourceAuthorizationOperations(left: ResourceAuthorizationSummary, right: ResourceAuthorizationSummary): number {
  const leftCreatedAt = rfc3339InstantMilliseconds(requiredRfc3339Instant(left.createdAt, '授权 createdAt'))
  const rightCreatedAt = rfc3339InstantMilliseconds(requiredRfc3339Instant(right.createdAt, '授权 createdAt'))
  if (leftCreatedAt === undefined || rightCreatedAt === undefined) {
    throw new Error('授权 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  const createdDelta = rightCreatedAt - leftCreatedAt
  if (createdDelta !== 0) return createdDelta
  return right.id.localeCompare(left.id)
}

export function sanitizeResourceAuthorizationSummaryForAccess(summary: ResourceAuthorizationSummary, access?: AccessScope): ResourceAuthorizationSummary {
  if (canManageResourceOwner(summary.resourceOwnerSystemAccountId, access)) {
    return summary
  }
  const sources = sanitizeAuthorizationSourcesForViewer(summary.authorizationSources, true) ?? []
  return {
    ...summary,
    effectiveSourceTeamId: undefined,
    effectiveSourceTeamName: undefined,
    authorizationSources: sources,
    createdBy: '',
    revokedBy: undefined
  }
}

export function withResourceAuthorizationPermissions(summary: ResourceAuthorizationSummary, _viewerSystemAccountId: string | undefined, access?: AccessScope): ResourceAuthorizationSummary {
  const canManage = canManageResourceOwner(summary.resourceOwnerSystemAccountId, access)
  return {
    ...summary,
    permissions: {
      canEdit: canManage,
      canAuthorize: canManage
    }
  }
}
