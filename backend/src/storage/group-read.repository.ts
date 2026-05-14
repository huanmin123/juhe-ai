import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, nowIso } from './database.js'
import type { GroupListRow } from './repository-row-types.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export function listGroupRowsForAccess(access?: AccessScope): GroupListRow[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getDatabase()
      .prepare("SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups ORDER BY updated_at DESC, id DESC")
      .all() as unknown as GroupListRow[]
  }
  if (!viewerSystemAccountId) {
    return getDatabase()
      .prepare("SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups ORDER BY updated_at DESC, id DESC")
      .all() as unknown as GroupListRow[]
  }
  return getDatabase()
    .prepare(`
      SELECT * FROM (
        SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM groups
        WHERE groups.system_account_id = ?
        UNION ALL
        SELECT groups.*, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND groups.system_account_id <> ?
      )
      ORDER BY updated_at DESC, id DESC
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId) as unknown as GroupListRow[]
}

export function loadGroupAuthorizationUsageSummaries(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'group_authorization' | 'group_authorization_team' = 'group_authorization'
): Map<string, AccountUsageSummary> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopes(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopes(scopes, scopeType, statDateOrRange)
}
