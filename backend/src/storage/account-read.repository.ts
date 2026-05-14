import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { manageableSystemAccountId, userVisibleSystemAccountId, canAccessAll, type AccessScope } from './access-scope.js'
import { buildAccountListOrderClause, type NormalizedAccountListOptions } from './account-list-options.js'
import { decryptJson } from './crypto.js'
import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import type { AccountListRow } from './repository-row-types.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export interface AccountRowsPage {
  rows: AccountListRow[]
  total: number
}

type AccountFilterValue = string | number

export function listAccountRowsForAccess(access: AccessScope | undefined, options: NormalizedAccountListOptions): AccountListRow[] {
  return queryAccountRowsForAccess(access, options).rows
}

export function listAccountRowsPageForAccess(access: AccessScope | undefined, options: NormalizedAccountListOptions): AccountRowsPage {
  return queryAccountRowsForAccess(access, options, {
    limit: options.pageSize,
    offset: (options.page - 1) * options.pageSize
  })
}

function queryAccountRowsForAccess(
  access: AccessScope | undefined,
  options: NormalizedAccountListOptions,
  pagination?: { limit: number; offset: number }
): AccountRowsPage {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const orderClause = buildAccountListOrderClause(options)
  const filters = buildAccountListFilters(options)
  const pageClause = pagination ? 'LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const queryRows = (baseSql: string, params: AccountFilterValue[] = []): AccountRowsPage => {
    const filteredSql = `${baseSql} ${filters.clause}`
    const totalRow = getDatabase().prepare(`SELECT COUNT(*) AS total FROM (${filteredSql}) counted_rows`).get(...params, ...filters.params) as { total?: number } | undefined
    const rows = getDatabase().prepare(`${filteredSql} ${orderClause} ${pageClause}`).all(...params, ...filters.params, ...pageParams) as unknown as AccountListRow[]
    return { rows, total: Number(totalRow?.total ?? 0) }
  }
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return queryRows(`
        SELECT account_rows.*, ${groupBindingSelectColumns()},
          COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
          NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms,
          NULL AS quality_recent_avg_first_token_ms,
          NULL AS quality_recent_request_count,
          NULL AS quality_recent_success_rate,
          NULL AS quality_updated_at
        FROM (
          SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
      `)
  }
  if (!viewerSystemAccountId) {
    return queryRows(`
        SELECT account_rows.*, ${groupBindingSelectColumns()},
          COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
          NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms,
          NULL AS quality_recent_avg_first_token_ms,
          NULL AS quality_recent_request_count,
          NULL AS quality_recent_success_rate,
          NULL AS quality_updated_at
        FROM (
          SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
      `)
  }
  return queryRows(`
      SELECT account_rows.*, ${groupBindingSelectColumns()},
        COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms,
        NULL AS quality_recent_avg_first_token_ms,
        NULL AS quality_recent_request_count,
        NULL AS quality_recent_success_rate,
        NULL AS quality_updated_at
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
    `, [ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId])
}

export function hydrateAccountRowsFromRecordDatabase(rows: AccountListRow[]): AccountListRow[] {
  if (rows.length === 0) return rows
  const ids = [...new Set(rows.map((row) => row.id).filter(Boolean))]
  if (ids.length === 0) return rows
  const qualityRows = getRecordDatabase()
    .prepare(`
      SELECT account_id, quality_score, quality_state, ewma_first_token_ms, recent_avg_first_token_ms,
        recent_request_count, success_rate, updated_at
      FROM account_quality_scores
      WHERE account_id IN (${ids.map(() => '?').join(',')})
    `)
    .all(...ids) as unknown as Array<{
      account_id: string
      quality_score: number | null
      quality_state: string | null
      ewma_first_token_ms: number | null
      recent_avg_first_token_ms: number | null
      recent_request_count: number | null
      success_rate: number | null
      updated_at: string | null
    }>
  const qualityByAccount = new Map(qualityRows.map((row) => [row.account_id, row]))
  return rows.map((row) => {
    const quality = qualityByAccount.get(row.id)
    if (!quality) return row
    return {
      ...row,
      quality_score: quality.quality_score,
      quality_state: quality.quality_state,
      quality_ewma_first_token_ms: quality.ewma_first_token_ms,
      quality_recent_avg_first_token_ms: quality.recent_avg_first_token_ms,
      quality_recent_request_count: quality.recent_request_count,
      quality_recent_success_rate: quality.success_rate,
      quality_updated_at: quality.updated_at
    }
  })
}

export function accountCredentialsForList(row: AccountListRow): Record<string, unknown> {
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  if (row.access_type !== 'authorized') {
    return credentials
  }
  return typeof credentials.base_url === 'string' && credentials.base_url ? { base_url: credentials.base_url } : {}
}

export function loadAccountAuthorizationUsageSummaries(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'account_authorization' | 'account_authorization_team' = 'account_authorization'
): Map<string, AccountUsageSummary> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopes(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopes(scopes, scopeType, statDateOrRange)
}

function groupBindingSelectColumns(): string {
  return `group_bindings.system_account_id AS binding_system_account_id,
          group_bindings.group_id AS bound_group_id,
          group_bindings.group_name AS bound_group_name,
          group_bindings.account_authorization_id AS bound_group_account_authorization_id,
          group_bindings.local_status AS bound_group_local_status,
          group_bindings.local_cooldown_until AS bound_group_local_cooldown_until,
          group_bindings.local_last_error_message AS bound_group_local_last_error_message,
          group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
          group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled`
}

function accountBindingSubquery(): string {
  return `(
    SELECT
      group_accounts.system_account_id,
      group_accounts.account_id,
      group_accounts.group_id,
      group_accounts.account_authorization_id,
      group_accounts.local_status,
      group_accounts.local_cooldown_until,
      group_accounts.local_last_error_message,
      group_accounts.local_super_priority_enabled,
      group_accounts.local_fallback_enabled,
      groups.name AS group_name
    FROM group_accounts
    INNER JOIN groups ON groups.id = group_accounts.group_id
    WHERE group_accounts.enabled = 1
  )`
}

function buildAccountListFilters(options: NormalizedAccountListOptions): { clause: string; params: AccountFilterValue[] } {
  const clauses: string[] = []
  const params: AccountFilterValue[] = []
  const keyword = options.keyword?.trim()
  if (keyword) {
    clauses.push(`(
      account_rows.name LIKE ?
      OR COALESCE(account_rows.notes, '') LIKE ?
      OR account_rows.provider_code LIKE ?
      OR account_rows.type LIKE ?
      OR account_rows.id LIKE ?
      OR COALESCE(group_bindings.group_name, '') LIKE ?
    )`)
    params.push(...Array.from({ length: 6 }, () => `%${keyword}%`))
  }
  if (options.type && options.type !== 'all') {
    clauses.push('account_rows.type = ?')
    params.push(options.type)
  }
  if (options.status && options.status !== 'all') {
    clauses.push('account_rows.status = ?')
    params.push(options.status)
  }
  if (options.schedulable === 'enabled') {
    clauses.push("account_rows.status = 'active' AND account_rows.schedulable = 1 AND (account_rows.cooldown_until IS NULL OR account_rows.cooldown_until <= ?)")
    params.push(nowIso())
  } else if (options.schedulable === 'disabled') {
    clauses.push("(account_rows.status = 'disabled' OR account_rows.schedulable <> 1)")
  } else if (options.schedulable === 'cooling') {
    clauses.push("(account_rows.status IN ('rate_limited', 'temporary_unavailable') OR (account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ?))")
    params.push(nowIso())
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}
