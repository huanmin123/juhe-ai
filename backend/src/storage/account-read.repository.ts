import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { manageableSystemAccountId, userVisibleSystemAccountId, canAccessAll, type AccessScope } from './access-scope.js'
import { buildAccountListOrderClause, type NormalizedAccountListOptions } from './account-list-options.js'
import { decryptJson } from './crypto.js'
import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import type { AccountListRow } from './repository-row-types.js'
import { compatiblePagedTotal, takePageRows } from './query-utils.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export interface AccountRowsPage {
  rows: AccountListRow[]
  total: number
}

type AccountFilterValue = string | number
type AccountRowQueryOptions = NormalizedAccountListOptions & { accountId?: string }
type AccountRowQuerySettings = {
  includeCredentials?: boolean
  includeTotal?: boolean
}
const accountQualityDatabaseAlias = 'account_quality_records'

export function listAccountRowsForAccess(access: AccessScope | undefined, options: NormalizedAccountListOptions): AccountListRow[] {
  return queryAccountRowsForAccess(access, options, undefined, false).rows
}

export function listAccountRowsPageForAccess(
  access: AccessScope | undefined,
  options: NormalizedAccountListOptions,
  settings: AccountRowQuerySettings = {}
): AccountRowsPage {
  return queryAccountRowsForAccess(access, options, {
    limit: settings.includeTotal === false ? options.pageSize : options.pageSize + 1,
    offset: (options.page - 1) * options.pageSize
  }, settings)
}

export function findAccountRowForAccess(access: AccessScope | undefined, accountId: string, options: NormalizedAccountListOptions): AccountListRow | undefined {
  const page = queryAccountRowsForAccess(access, { ...options, accountId }, {
    limit: 1,
    offset: 0
  }, false)
  return page.rows[0]
}

function queryAccountRowsForAccess(
  access: AccessScope | undefined,
  options: AccountRowQueryOptions,
  pagination?: { limit: number; offset: number },
  settings: AccountRowQuerySettings | boolean = true
): AccountRowsPage {
  const database = getDatabase()
  const normalizedSettings = typeof settings === 'boolean' ? { includeTotal: settings } : settings
  const includeTotal = normalizedSettings.includeTotal ?? true
  const accountSelectColumns = accountRowSelectColumns(normalizedSettings.includeCredentials ?? true)
  const includeQualityInQuery = hasAccountQualityScoreSort(options)
  if (includeQualityInQuery) {
    ensureAccountQualityDatabaseAttached(database)
  }
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const orderClause = buildAccountListOrderClause(options)
  const filters = buildAccountListFilters(options)
  const pageClause = pagination ? 'LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const queryRows = (baseSql: string, params: AccountFilterValue[] = []): AccountRowsPage => {
    const filteredSql = `${baseSql} ${filters.clause}`
    const rows = database.prepare(`${filteredSql} ${orderClause} ${pageClause}`).all(...params, ...filters.params, ...pageParams) as unknown as AccountListRow[]
    if (!includeTotal || !pagination) return { rows, total: rows.length }
    const pageRows = takePageRows(rows, pagination.limit - 1)
    return {
      rows: pageRows.rows,
      total: compatiblePagedTotal(options.page, options.pageSize, pageRows.rows.length, pageRows.hasMore)
    }
  }
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return queryRows(`
        SELECT account_rows.*, ${groupBindingSelectColumns()},
          COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
          ${accountQualitySelectColumns(includeQualityInQuery)}
        FROM (
          SELECT ${accountSelectColumns}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        ${accountQualityJoinClause(includeQualityInQuery)}
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
          AND group_bindings.enabled = 1
        LEFT JOIN groups bound_groups ON bound_groups.id = group_bindings.group_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
      `)
  }
  if (!viewerSystemAccountId) {
    return queryRows(`
        SELECT account_rows.*, ${groupBindingSelectColumns()},
          COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
          ${accountQualitySelectColumns(includeQualityInQuery)}
        FROM (
          SELECT ${accountSelectColumns}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
          FROM accounts
        ) account_rows
        ${accountQualityJoinClause(includeQualityInQuery)}
        LEFT JOIN ${accountBindingSubquery()} group_bindings
          ON group_bindings.account_id = account_rows.id
          AND group_bindings.system_account_id = account_rows.system_account_id
          AND group_bindings.enabled = 1
        LEFT JOIN groups bound_groups ON bound_groups.id = group_bindings.group_id
        LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
      `)
  }
  return queryRows(`
      SELECT account_rows.*, ${groupBindingSelectColumns()},
        COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
        ${accountQualitySelectColumns(includeQualityInQuery)}
      FROM (
        SELECT ${accountSelectColumns}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM accounts
        WHERE accounts.system_account_id = ?
        UNION ALL
        SELECT ${accountSelectColumns}, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN accounts ON accounts.id = ra.resource_id
        WHERE ra.resource_type = 'account'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND accounts.system_account_id <> ?
      ) account_rows
      ${accountQualityJoinClause(includeQualityInQuery)}
      LEFT JOIN ${accountBindingSubquery()} group_bindings
        ON group_bindings.account_id = account_rows.id
        AND group_bindings.system_account_id = CASE WHEN account_rows.access_type = 'authorized' THEN ? ELSE account_rows.system_account_id END
        AND group_bindings.enabled = 1
      LEFT JOIN groups bound_groups ON bound_groups.id = group_bindings.group_id
      LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
    `, [ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId])
}

function hasAccountQualityScoreSort(options: Pick<NormalizedAccountListOptions, 'sorts'>): boolean {
  return options.sorts.some((sort) => sort.field === 'qualityScore')
}

function ensureAccountQualityDatabaseAttached(database: ReturnType<typeof getDatabase>): void {
  getRecordDatabase()
  const rows = database.prepare('PRAGMA database_list').all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === accountQualityDatabaseAlias)) return
  database.prepare(`ATTACH DATABASE ? AS ${accountQualityDatabaseAlias}`).run(runtimeConfig.recordDatabasePath)
}

function accountQualitySelectColumns(includeQualityInQuery: boolean): string {
  if (!includeQualityInQuery) {
    return `NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms,
          NULL AS quality_recent_avg_first_token_ms,
          NULL AS quality_recent_request_count,
          NULL AS quality_recent_success_rate,
          NULL AS quality_updated_at`
  }
  return `quality_scores.quality_score AS quality_score,
          quality_scores.quality_state AS quality_state,
          quality_scores.ewma_first_token_ms AS quality_ewma_first_token_ms,
          quality_scores.recent_avg_first_token_ms AS quality_recent_avg_first_token_ms,
          quality_scores.recent_request_count AS quality_recent_request_count,
          quality_scores.success_rate AS quality_recent_success_rate,
          quality_scores.updated_at AS quality_updated_at`
}

function accountQualityJoinClause(includeQualityInQuery: boolean): string {
  if (!includeQualityInQuery) return ''
  return `LEFT JOIN ${accountQualityDatabaseAlias}.account_quality_scores quality_scores
        ON quality_scores.account_id = account_rows.id`
}

function accountRowSelectColumns(includeCredentials: boolean): string {
  const columns = [
    'accounts.id',
    'accounts.system_account_id',
    'accounts.provider_code',
    'accounts.name',
    'accounts.notes',
    'accounts.type',
    'accounts.status',
    'accounts.credential_mask',
    includeCredentials ? 'accounts.credentials_encrypted' : "'' AS credentials_encrypted",
    'accounts.proxy_profile_id',
    'accounts.concurrency_limit',
    'accounts.passthrough_enabled',
    'accounts.error_policy_id',
    'accounts.priority',
    'accounts.super_priority_enabled',
    'accounts.fallback_enabled',
    'accounts.schedulable',
    'accounts.account_expires_at',
    'accounts.last_used_at',
    'accounts.cooldown_until',
    'accounts.last_error_code',
    'accounts.last_error_message',
    'accounts.stream_failure_count',
    'accounts.stream_failure_window_started_at',
    'accounts.created_at',
    'accounts.updated_at'
  ]
  return columns.join(', ')
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

export function accountCredentialsForList(row: AccountListRow, includeCredentials = true): Record<string, unknown> {
  if (!includeCredentials) return {}
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
          bound_groups.name AS bound_group_name,
          group_bindings.account_authorization_id AS bound_group_account_authorization_id,
          group_bindings.local_status AS bound_group_local_status,
          group_bindings.local_cooldown_until AS bound_group_local_cooldown_until,
          group_bindings.local_last_error_message AS bound_group_local_last_error_message,
          group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
          group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled`
}

function accountBindingSubquery(): string {
  return `group_accounts`
}

function buildAccountListFilters(options: AccountRowQueryOptions): { clause: string; params: AccountFilterValue[] } {
  const clauses: string[] = []
  const params: AccountFilterValue[] = []
  if (options.accountId) {
    clauses.push('account_rows.id = ?')
    params.push(options.accountId)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    clauses.push(`(
      account_rows.name LIKE ?
      OR COALESCE(account_rows.notes, '') LIKE ?
      OR account_rows.provider_code LIKE ?
      OR account_rows.type LIKE ?
      OR account_rows.id LIKE ?
      OR COALESCE(bound_groups.name, '') LIKE ?
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
