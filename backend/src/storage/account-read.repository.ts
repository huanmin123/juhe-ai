import type { AccountUsageSummary } from '../domain/types.js'
import { manageableSystemAccountId, userVisibleSystemAccountId, canAccessAll, type AccessScope } from './access-scope.js'
import { buildAccountListOrderClause, type AccountListOptions } from './account-list-options.js'
import { decryptJson } from './crypto.js'
import { getDatabase, nowIso } from './database.js'
import type { AccountListRow } from './repository-row-types.js'
import { loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export function listAccountRowsForAccess(access: AccessScope | undefined, options: Required<AccountListOptions>): AccountListRow[] {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const orderClause = buildAccountListOrderClause(options)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getDatabase()
      .prepare(`
        SELECT account_rows.*, group_bindings.system_account_id AS binding_system_account_id, group_bindings.group_id AS bound_group_id, group_bindings.group_name AS bound_group_name, group_bindings.account_authorization_id AS bound_group_account_authorization_id
        FROM (
          SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
        LEFT JOIN usage_stats_totals account_usage
          ON account_usage.system_account_id = account_rows.system_account_id
          AND account_usage.scope_type = 'account'
          AND account_usage.scope_id = account_rows.id
        LEFT JOIN usage_stats_totals authorization_usage
          ON authorization_usage.system_account_id = account_rows.system_account_id
          AND authorization_usage.scope_type = 'account_authorization'
          AND authorization_usage.scope_id = account_rows.authorization_id
        ${orderClause}
      `)
      .all() as unknown as AccountListRow[]
  }
  if (!viewerSystemAccountId) {
    return getDatabase()
      .prepare(`
        SELECT account_rows.*, group_bindings.system_account_id AS binding_system_account_id, group_bindings.group_id AS bound_group_id, group_bindings.group_name AS bound_group_name, group_bindings.account_authorization_id AS bound_group_account_authorization_id
        FROM (
          SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
        LEFT JOIN usage_stats_totals account_usage
          ON account_usage.system_account_id = account_rows.system_account_id
          AND account_usage.scope_type = 'account'
          AND account_usage.scope_id = account_rows.id
        LEFT JOIN usage_stats_totals authorization_usage
          ON authorization_usage.system_account_id = account_rows.system_account_id
          AND authorization_usage.scope_type = 'account_authorization'
          AND authorization_usage.scope_id = account_rows.authorization_id
        ${orderClause}
      `)
      .all() as unknown as AccountListRow[]
  }
  return getDatabase()
    .prepare(`
      SELECT account_rows.*, group_bindings.system_account_id AS binding_system_account_id, group_bindings.group_id AS bound_group_id, group_bindings.group_name AS bound_group_name, group_bindings.account_authorization_id AS bound_group_account_authorization_id
      FROM (
        SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM accounts
        WHERE accounts.system_account_id = ?
        UNION ALL
        SELECT accounts.*, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN accounts ON accounts.id = ra.resource_id
        WHERE ra.resource_type = 'account'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND accounts.system_account_id <> ?
      ) account_rows
      LEFT JOIN ${accountBindingSubquery()} group_bindings
        ON group_bindings.account_id = account_rows.id
        AND group_bindings.system_account_id = CASE WHEN account_rows.access_type = 'authorized' THEN ? ELSE account_rows.system_account_id END
      LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
      LEFT JOIN usage_stats_totals account_usage
        ON account_usage.system_account_id = account_rows.system_account_id
        AND account_usage.scope_type = 'account'
        AND account_usage.scope_id = account_rows.id
      LEFT JOIN usage_stats_totals authorization_usage
        ON authorization_usage.system_account_id = account_rows.system_account_id
        AND authorization_usage.scope_type = 'account_authorization'
        AND authorization_usage.scope_id = account_rows.authorization_id
      ${orderClause}
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId) as unknown as AccountListRow[]
}

export function accountCredentialsForList(row: AccountListRow): Record<string, unknown> {
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  if (row.access_type !== 'authorized') {
    return credentials
  }
  return typeof credentials.base_url === 'string' && credentials.base_url ? { base_url: credentials.base_url } : {}
}

export function loadAccountAuthorizationUsageSummaries(scopes: UsageSummaryScopeRequest[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadAuthorizationUsageSummariesForScopes(scopes, 'account_authorization', statDate)
}

function accountBindingSubquery(): string {
  return `(
    SELECT
      group_accounts.system_account_id,
      group_accounts.account_id,
      group_accounts.group_id,
      group_accounts.account_authorization_id,
      groups.name AS group_name
    FROM group_accounts
    INNER JOIN groups ON groups.id = group_accounts.group_id
    WHERE group_accounts.enabled = 1
  )`
}
