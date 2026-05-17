import type { AccountUsageSummary, ProviderCode, ResourceAuthorizationResourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, nowIso } from './database.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import type { UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export function accountSystemAccountId(accountId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function groupSystemAccountId(groupId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function groupOwnerAndProvider(groupId: string): { systemAccountId: string; providerCode: ProviderCode; name?: string } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id, provider_code, name FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode; name?: string } | undefined
  return row?.system_account_id && row.provider_code ? { systemAccountId: row.system_account_id, providerCode: row.provider_code, name: row.name } : undefined
}

export function activeAccountAuthorization(accountId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('account', accountId, granteeSystemAccountId)
}

export function activeGroupAuthorization(groupId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('group', groupId, granteeSystemAccountId)
}

export function activeResourceAuthorization(resourceType: ResourceAuthorizationResourceType, resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT * FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

export function resolveAccountSystemAccountId(accountId: string): string | undefined {
  return accountSystemAccountId(accountId)
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
  sources: ResourceAuthorizationSummary['authorizationSources'],
  limited: boolean
): ResourceAuthorizationSummary['authorizationSources'] {
  if (!sources) return sources
  if (!limited) return sources
  return sources.map((source) => ({
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
