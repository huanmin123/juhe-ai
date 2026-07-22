import type { AccountUsageSummary, ProviderCode, ResourceAuthorizationResourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import type { UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export function accountSystemAccountId(accountId: string): string | undefined {
  const row = getBusinessDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function groupSystemAccountId(groupId: string): string | undefined {
  const row = getBusinessDatabase().prepare('SELECT system_account_id FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function groupOwnerAndProvider(groupId: string): { systemAccountId: string; providerCode: ProviderCode; name?: string } | undefined {
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, provider_code, name FROM groups WHERE id = ?')
    .get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode; name?: string } | undefined
  return row?.system_account_id && row.provider_code
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code,
        name: row.name
      }
    : undefined
}

export function activeAccountAuthorization(accountId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('account', accountId, granteeSystemAccountId)
}

export function activeGroupAuthorization(groupId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('group', groupId, granteeSystemAccountId)
}

export function activeResourceAuthorization(resourceType: ResourceAuthorizationResourceType, resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getBusinessDatabase()
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`)
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

export function activeResourceAuthorizationById(authorizationId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getBusinessDatabase()
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`)
    .get(authorizationId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

export function activeResourceAuthorizationsByIds(authorizationIds: string[], granteeSystemAccountId: string): Map<string, ResourceAuthorizationRow> {
  const ids = [...new Set(authorizationIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const now = nowIso()
  const rows: ResourceAuthorizationRow[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT ${resourceAuthorizationSelectColumns()}
        FROM resource_authorizations
        WHERE grantee_system_account_id = ?
          AND status = 'active'
          AND (expires_at IS NULL OR expires_at > ?)
          AND id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(granteeSystemAccountId, now, ...chunk) as unknown as ResourceAuthorizationRow[])
  }
  return new Map(rows.map((row) => [row.id, row]))
}

export function activeResourceAuthorizationsByResourceIds(resourceType: ResourceAuthorizationResourceType, resourceIds: string[], granteeSystemAccountId: string): Map<string, ResourceAuthorizationRow> {
  const ids = [...new Set(resourceIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const now = nowIso()
  const rows: ResourceAuthorizationRow[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT ${resourceAuthorizationSelectColumns()}
        FROM resource_authorizations
        WHERE resource_type = ?
          AND grantee_system_account_id = ?
          AND status = 'active'
          AND (expires_at IS NULL OR expires_at > ?)
          AND resource_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(resourceType, granteeSystemAccountId, now, ...chunk) as unknown as ResourceAuthorizationRow[])
  }
  return new Map(rows.map((row) => [row.resource_id, row]))
}

export function resourceAuthorizationSelectColumns(alias?: string): string {
  const prefix = alias ? `${alias}.` : ''
  return [
    'id',
    'resource_type',
    'resource_id',
    'resource_owner_system_account_id',
    'grantee_system_account_id',
    'scope',
    'status',
    'effective_source_type',
    'effective_source_team_id',
    'activated_at',
    'last_source_changed_at',
    'remark',
    'expires_at',
    'limits_json',
    'created_by',
    'created_at',
    'revoked_by',
    'revoked_at',
    'revoked_reason',
    'updated_at'
  ].map((column) => `${prefix}${column}`).join(', ')
}

export function isResourceAuthorizationExpired(expiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!expiresAt) return false
  const timestamp = Date.parse(expiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

export function usageScope(rowKey: string, systemAccountId: string, scopeId: string): UsageSummaryScopeRequest {
  return { rowKey, systemAccountId, scopeId }
}

export function sanitizeAuthorizationSourcesForViewer(
  authorizationSources: ResourceAuthorizationSummary['authorizationSources'],
  limited: boolean
): ResourceAuthorizationSummary['authorizationSources'] {
  if (!authorizationSources) return authorizationSources
  if (!limited) return authorizationSources
  return authorizationSources.map((source) => ({
    id: source.id,
    authorizationId: source.authorizationId,
    sourceType: source.sourceType,
    sourceTeamName: source.sourceTeamName,
    status: source.status,
    activatedAt: source.activatedAt,
    endedReason: source.endedReason,
    createdBy: '',
    createdAt: source.createdAt,
    updatedAt: source.updatedAt
  }))
}

export function canManageResourceOwner(ownerSystemAccountId: string, access?: AccessScope): boolean {
  const scopedOwnerId = manageableSystemAccountId(access)
  if (scopedOwnerId) return scopedOwnerId === ownerSystemAccountId
  return canAccessAll(access)
}

export type { AccountUsageSummary }
