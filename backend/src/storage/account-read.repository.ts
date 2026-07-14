import { existsSync } from 'node:fs'
import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { manageableSystemAccountId, userVisibleSystemAccountId, canAccessAll, type AccessScope } from './access-scope.js'
import { accountStatusFilterValues, buildAccountListOrderClause, type NormalizedAccountListOptions } from './account-list-options.js'
import { accountApiKeyPoolAllUnavailableSql, ensureAccountDerivedStatusSqlFunctions } from './account-derived-status-sql.js'
import { accountNameContainsAccountIdSubquery } from './account-name-search.repository.js'
import { loadModelMappingsByAccountIds } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds } from './account-supported-models.repository.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, getStatsDatabase, statsDatabasePath } from './database.js'
import type { AccountListRow } from './repository-row-types.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { ensureRequestQuotaDatabaseAttached, requestQuotaExceededSql, type RequestQuotaSqlExpression } from './request-quota-sql.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export interface AccountRowsPage {
  rows: AccountListRow[]
  total: number
}

type AccountFilterValue = string | number
type AccountRowQueryOptions = NormalizedAccountListOptions & { accountId?: string }
type AccountNameContainsSubquery = { sql: string; params: AccountFilterValue[] }
type AccountRowQuerySettings = {
  includeCredentials?: boolean
  includeTotal?: boolean
}
type AccountFilterExpression = {
  sql: string
  params: AccountFilterValue[]
}
const accountQualityDatabaseAlias = 'account_quality_records'
const currentIsoSql = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

function authorizedAccountEffectiveStatusExpression(includeAuthorizationQuota = false): AccountFilterExpression {
  const quotaExpression = includeAuthorizationQuota ? authorizedAccountQuotaExceededExpression() : undefined
  return {
    sql: `CASE
    WHEN ${authorizedBindingUnavailableExpression()} THEN 'disabled'
    WHEN account_rows.authorization_status IS NULL
      OR account_rows.authorization_status <> 'active'
      OR (account_rows.authorization_expires_at IS NOT NULL AND account_rows.authorization_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    ${quotaExpression ? `WHEN ${quotaExpression.sql} THEN 'rate_limited'` : ''}
    WHEN account_rows.source_status IS NULL THEN 'disabled'
    WHEN account_rows.source_last_error_code = 'account_expired'
      OR (account_rows.source_account_expires_at IS NOT NULL AND account_rows.source_account_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN account_rows.source_status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN account_rows.source_status
    WHEN account_rows.source_cooldown_until IS NOT NULL AND account_rows.source_cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN account_rows.source_schedulable <> 1 THEN 'disabled'
    WHEN account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= ${currentIsoSql} THEN 'disabled'
    WHEN account_rows.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN account_rows.status
    WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN account_rows.schedulable <> 1 THEN 'disabled'
    WHEN ${accountApiKeyPoolAllUnavailableSql({
      accountIdSql: `CASE
        WHEN account_rows.access_type = 'authorized' AND account_rows.authorization_instance_source_account_id IS NOT NULL
        THEN account_rows.authorization_instance_source_account_id
        ELSE account_rows.id
      END`,
      providerCodeSql: 'COALESCE(account_rows.source_provider_code, account_rows.provider_code)',
      protocolCodeSql: 'COALESCE(account_rows.source_protocol_code, account_rows.protocol_code)',
      protocolVersionSql: 'COALESCE(account_rows.source_protocol_version, account_rows.protocol_version)',
      typeSql: 'COALESCE(account_rows.source_type, account_rows.type)'
    })} THEN 'temporary_unavailable'
    ELSE account_rows.status
  END`,
    params: quotaExpression?.params ?? []
  }
}

function accountEffectiveStatusFilterExpression(includeAuthorizationQuota = false): AccountFilterExpression {
  const authorizedExpression = authorizedAccountEffectiveStatusExpression(includeAuthorizationQuota)
  return {
    sql: `CASE
    WHEN account_rows.access_type = 'authorized' THEN ${authorizedExpression.sql}
    ELSE ${ownerAccountEffectiveStatusExpression()}
  END`,
    params: authorizedExpression.params
  }
}

function ownerAccountEffectiveStatusExpression(): string {
  return `CASE
    WHEN account_rows.last_error_code = 'account_expired'
      OR (account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN account_rows.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN account_rows.status
    WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN account_rows.schedulable <> 1 THEN 'disabled'
    WHEN ${accountApiKeyPoolAllUnavailableSql({
      accountIdSql: 'account_rows.id',
      providerCodeSql: 'account_rows.provider_code',
      protocolCodeSql: 'account_rows.protocol_code',
      protocolVersionSql: 'account_rows.protocol_version',
      typeSql: 'account_rows.type'
    })} THEN 'temporary_unavailable'
    ELSE account_rows.status
  END`
}

function authorizedBindingAvailableExpression(): string {
  return `group_bindings.group_id IS NOT NULL
    AND group_bindings.account_authorization_id IS NOT NULL
    AND group_bindings.account_authorization_id = account_rows.authorization_id`
}

function authorizedAuthorizationAvailableExpression(): string {
  return `account_rows.authorization_status = 'active'
    AND (account_rows.authorization_expires_at IS NULL OR account_rows.authorization_expires_at > ${currentIsoSql})`
}

function authorizedAccountAvailableExpression(): string {
  return `account_rows.schedulable = 1
    AND account_rows.status = 'active'
    AND (account_rows.cooldown_until IS NULL OR account_rows.cooldown_until <= ${currentIsoSql})
    AND (account_rows.account_expires_at IS NULL OR account_rows.account_expires_at > ${currentIsoSql})`
}

function authorizedSourceAccountAvailableExpression(): string {
  return `account_rows.source_status = 'active'
    AND account_rows.source_schedulable = 1
    AND (account_rows.source_cooldown_until IS NULL OR account_rows.source_cooldown_until <= ${currentIsoSql})
    AND (account_rows.source_account_expires_at IS NULL OR account_rows.source_account_expires_at > ${currentIsoSql})
    AND (account_rows.source_last_error_code IS NULL OR account_rows.source_last_error_code <> 'account_expired')`
}

function authorizedAccountHardUnavailableExpression(): string {
  return `account_rows.schedulable <> 1
    OR account_rows.status IN ('pending_test', 'disabled', 'error')
    OR (account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= ${currentIsoSql})`
}

function authorizedSourceAccountHardUnavailableExpression(): string {
  return `account_rows.source_status IS NULL
    OR COALESCE(account_rows.source_schedulable, 0) <> 1
    OR account_rows.source_status IN ('pending_test', 'disabled', 'error')
    OR COALESCE(account_rows.source_last_error_code, '') = 'account_expired'
    OR (account_rows.source_account_expires_at IS NOT NULL AND account_rows.source_account_expires_at <= ${currentIsoSql})`
}

function authorizedAccountCoolingExpression(): string {
  return `account_rows.status IN ('rate_limited', 'temporary_unavailable')
    OR (account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${currentIsoSql})`
}

function authorizedSourceAccountCoolingExpression(): string {
  return `account_rows.source_status IN ('rate_limited', 'temporary_unavailable')
    OR (account_rows.source_cooldown_until IS NOT NULL AND account_rows.source_cooldown_until > ${currentIsoSql})`
}

function authorizedBindingUnavailableExpression(): string {
  return `group_bindings.group_id IS NULL
    OR group_bindings.account_authorization_id IS NULL
    OR group_bindings.account_authorization_id <> account_rows.authorization_id`
}

function authorizedAccountQuotaExceededExpression(): AccountFilterExpression {
  const directQuota = requestQuotaExceededSql({
    limitsSql: 'account_rows.authorization_limits_json',
    systemAccountSql: 'account_rows.system_account_id',
    scopeType: 'account_authorization',
    scopeIdSql: 'account_rows.authorization_id'
  })
  const teamQuota = requestQuotaExceededSql({
    limitsSql: authorizedTeamGrantLimitsSql(),
    systemAccountSql: 'account_rows.system_account_id',
    scopeType: 'account_authorization_team',
    scopeIdSql: "account_rows.id || ':' || account_rows.authorization_effective_source_team_id"
  })
  return mergeQuotaExpressions(directQuota, teamQuota, 'account_rows.authorization_effective_source_team_id IS NOT NULL')
}

function authorizedTeamGrantLimitsSql(): string {
  return `(SELECT grant_rows.limits_json
    FROM resource_authorization_grants grant_rows
    WHERE grant_rows.resource_type = 'account'
      AND grant_rows.resource_id = account_rows.authorization_resource_id
      AND grant_rows.resource_owner_system_account_id = account_rows.authorization_resource_owner_system_account_id
      AND grant_rows.grantee_type = 'team'
      AND grant_rows.grantee_team_id = account_rows.authorization_effective_source_team_id
      AND grant_rows.status = 'active'
      AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ${currentIsoSql})
    LIMIT 1)`
}

function mergeQuotaExpressions(
  directQuota: RequestQuotaSqlExpression,
  teamQuota: RequestQuotaSqlExpression,
  teamGuardSql: string
): AccountFilterExpression {
  return {
    sql: `(account_rows.authorization_id IS NOT NULL
      AND (${directQuota.sql}
        OR (${teamGuardSql} AND ${teamQuota.sql})))`,
    params: [...directQuota.params, ...teamQuota.params]
  }
}

function accountEffectiveSchedulableExpression(includeAuthorizationQuota = false): AccountFilterExpression {
  const quotaExpression = includeAuthorizationQuota ? authorizedAccountQuotaExceededExpression() : undefined
  return {
    sql: `CASE
    WHEN account_rows.access_type = 'authorized' THEN
      CASE
        WHEN ${authorizedBindingAvailableExpression()}
          AND ${authorizedAuthorizationAvailableExpression()}
          AND ${authorizedSourceAccountAvailableExpression()}
          AND ${authorizedAccountAvailableExpression()}
          ${quotaExpression ? `AND NOT (${quotaExpression.sql})` : ''}
        THEN 1
        ELSE 0
      END
    WHEN account_rows.status = 'active'
      AND account_rows.schedulable = 1
      AND (account_rows.cooldown_until IS NULL OR account_rows.cooldown_until <= ${currentIsoSql})
      AND (account_rows.account_expires_at IS NULL OR account_rows.account_expires_at > ${currentIsoSql})
      AND (account_rows.last_error_code IS NULL OR account_rows.last_error_code <> 'account_expired')
    THEN 1
    ELSE 0
  END`,
    params: quotaExpression?.params ?? []
  }
}

function accountCoolingFilterExpression(): string {
  return `CASE
    WHEN account_rows.access_type = 'authorized' THEN
      CASE
        WHEN ${authorizedBindingAvailableExpression()}
          AND ${authorizedAuthorizationAvailableExpression()}
          AND NOT (${authorizedSourceAccountHardUnavailableExpression()})
          AND NOT (${authorizedAccountHardUnavailableExpression()})
          AND (${authorizedSourceAccountCoolingExpression()} OR ${authorizedAccountCoolingExpression()})
        THEN 1
        ELSE 0
      END
    WHEN account_rows.status IN ('rate_limited', 'temporary_unavailable')
      OR (account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${currentIsoSql})
    THEN 1
    ELSE 0
  END`
}

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
  const database = getBusinessDatabase()
  const normalizedSettings = typeof settings === 'boolean' ? { includeTotal: settings } : settings
  const includeTotal = normalizedSettings.includeTotal ?? true
  const accountSelectColumns = accountRowSelectColumns(normalizedSettings.includeCredentials ?? true)
  const includeQualityInQuery = hasAccountQualityScoreSort(options)
  if (includeQualityInQuery) {
    ensureAccountQualityDatabaseAttached(database)
  }
  if (accountListNeedsQuotaDatabase(options)) {
    ensureRequestQuotaDatabaseAttached(database)
  }
  if (accountListNeedsDerivedStatusFunctions(options)) {
    ensureAccountDerivedStatusSqlFunctions(database)
  }
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const orderClause = buildAccountListOrderClause(options)
  const containsSubquery = accountNameContainsAccountIdSubquery(options.keyword, access)
  const filters = buildAccountListFilters(options, containsSubquery)
  const pageClause = pagination ? 'LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const queryRows = (baseSql: string, params: AccountFilterValue[] = []): AccountRowsPage => {
    const filteredSql = `${baseSql} ${filters.clause}`
    const rows = database.prepare(`${filteredSql} ${orderClause} ${pageClause}`).all(...params, ...filters.params, ...pageParams) as unknown as AccountListRow[]
    if (!includeTotal || !pagination) return { rows, total: rows.length }
    const pageRows = takePageRows(rows, pagination.limit - 1)
    return {
      rows: pageRows.rows,
      total: pagedTotalUpperBound(options.page, options.pageSize, pageRows.rows.length, pageRows.hasMore)
    }
  }
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return queryRows(`
        SELECT ${accountListOuterSelectColumns()}, ${groupBindingSelectColumns()},
          COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
          ${accountQualitySelectColumns(includeQualityInQuery)}
        FROM (
          SELECT ${accountSelectColumns}, CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' ELSE 'owner' END AS access_type,
            ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
            ra.limits_json AS authorization_limits_json, ra.effective_source_type AS authorization_effective_source_type,
            ra.effective_source_team_id AS authorization_effective_source_team_id,
            ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
            ra.resource_id AS authorization_resource_id,
            ${sourceAccountSelectColumns(normalizedSettings.includeCredentials ?? true)}
          FROM accounts
          LEFT JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
          LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
            AND source_accounts.deleted_at IS NULL
          WHERE accounts.deleted_at IS NULL
            AND (
              accounts.authorization_instance_authorization_id IS NULL
              OR ra.status IN ('active', 'paused', 'expired')
            )
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
    throw new Error('缺少系统账户上下文')
  }
  return queryRows(`
      SELECT ${accountListOuterSelectColumns()}, ${groupBindingSelectColumns()},
        COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_sort_name,
        ${accountQualitySelectColumns(includeQualityInQuery)}
      FROM (
        SELECT ${accountSelectColumns}, CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' ELSE 'owner' END AS access_type,
          ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
          ra.limits_json AS authorization_limits_json, ra.effective_source_type AS authorization_effective_source_type,
          ra.effective_source_team_id AS authorization_effective_source_team_id,
          ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
          ra.resource_id AS authorization_resource_id,
          ${sourceAccountSelectColumns(normalizedSettings.includeCredentials ?? true)}
        FROM accounts
        LEFT JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
        LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
          AND source_accounts.deleted_at IS NULL
        WHERE accounts.system_account_id = ?
          AND accounts.deleted_at IS NULL
          AND (
            accounts.authorization_instance_authorization_id IS NULL
            OR ra.status IN ('active', 'paused', 'expired')
          )
      ) account_rows
      ${accountQualityJoinClause(includeQualityInQuery)}
      LEFT JOIN ${accountBindingSubquery()} group_bindings
        ON group_bindings.account_id = account_rows.id
        AND group_bindings.system_account_id = CASE WHEN account_rows.access_type = 'authorized' THEN ? ELSE account_rows.system_account_id END
        AND group_bindings.enabled = 1
      LEFT JOIN groups bound_groups ON bound_groups.id = group_bindings.group_id
      LEFT JOIN system_accounts ON system_accounts.id = account_rows.system_account_id
    `, [ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId])
}

function hasAccountQualityScoreSort(options: Pick<NormalizedAccountListOptions, 'sorts'>): boolean {
  return options.sorts.some((sort) => sort.field === 'qualityScore')
}

function accountListNeedsQuotaDatabase(options: Pick<NormalizedAccountListOptions, 'status' | 'schedulable'>): boolean {
  const statuses = accountStatusFilterValues(options.status)
  return statuses.includes('active')
    || statuses.includes('rate_limited')
    || options.schedulable === 'enabled'
    || options.schedulable === 'disabled'
}

function accountListNeedsDerivedStatusFunctions(options: Pick<NormalizedAccountListOptions, 'status' | 'schedulable'>): boolean {
  const statuses = accountStatusFilterValues(options.status)
  return statuses.length > 0
    || options.schedulable === 'disabled'
}

function ensureAccountQualityDatabaseAttached(database: ReturnType<typeof getBusinessDatabase>): void {
  getStatsDatabase()
  const rows = database.prepare('PRAGMA database_list').all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === accountQualityDatabaseAlias)) return
  database.prepare(`ATTACH DATABASE ? AS ${accountQualityDatabaseAlias}`).run(statsDatabasePath())
}

function accountQualitySelectColumns(includeQualityInQuery: boolean): string {
  if (!includeQualityInQuery) {
    return `NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms,
          NULL AS quality_recent_avg_first_token_ms,
          NULL AS quality_recent_request_count,
          NULL AS quality_recent_error_count,
          NULL AS quality_recent_success_rate,
          NULL AS quality_last_error_at,
          NULL AS quality_last_error_message,
          NULL AS quality_updated_at`
  }
  return `quality_scores.quality_score AS quality_score,
          quality_scores.quality_state AS quality_state,
          quality_scores.ewma_first_token_ms AS quality_ewma_first_token_ms,
          quality_scores.recent_avg_first_token_ms AS quality_recent_avg_first_token_ms,
          quality_scores.recent_request_count AS quality_recent_request_count,
          quality_scores.recent_error_count AS quality_recent_error_count,
          quality_scores.success_rate AS quality_recent_success_rate,
          quality_scores.last_error_at AS quality_last_error_at,
          quality_scores.last_error_message AS quality_last_error_message,
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
    'accounts.config_revision',
    'accounts.system_account_id',
    'accounts.provider_code',
    'accounts.provider_protocol_profile_id',
    'accounts.protocol_code',
    'accounts.protocol_version',
    'accounts.name',
    'accounts.notes',
    'accounts.type',
    'accounts.status',
    'accounts.credential_mask',
    includeCredentials ? 'accounts.credentials_encrypted' : "'' AS credentials_encrypted",
    'accounts.proxy_profile_id',
    'accounts.concurrency_limit',
    'accounts.priority',
    'accounts.super_priority_enabled',
    'accounts.fallback_enabled',
    'accounts.client_compatibility',
    'accounts.schedulable',
    'accounts.availability_schedule_json',
    'accounts.account_expires_at',
    'accounts.last_used_at',
    'accounts.cooldown_until',
    'accounts.last_error_code',
    'accounts.last_error_message',
    'accounts.cooldown_retest_failure_count',
    'accounts.cooldown_retest_observation_started_at',
    'accounts.cooldown_retest_last_at',
    'accounts.cooldown_retest_last_status_code',
    'accounts.health_check_model',
    'accounts.health_check_endpoint_mode',
    'accounts.last_health_check_at',
    'accounts.next_health_check_at',
    'accounts.last_health_success_at',
    'accounts.health_check_failure_count',
    'accounts.last_health_check_status_code',
    'accounts.last_health_check_error_code',
    'accounts.last_health_check_error_message',
    'accounts.stream_failure_count',
    'accounts.stream_failure_window_started_at',
    'accounts.authorization_instance_source_account_id',
    'accounts.authorization_instance_authorization_id',
    'accounts.authorization_instance_owner_system_account_id',
    'accounts.deleted_at',
    'accounts.deleted_by',
    'accounts.created_at',
    'accounts.updated_at'
  ]
  return columns.join(', ')
}

function sourceAccountSelectColumns(includeCredentials: boolean): string {
  return [
    'source_accounts.provider_code AS source_provider_code',
    'source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id',
    'source_accounts.protocol_code AS source_protocol_code',
    'source_accounts.protocol_version AS source_protocol_version',
    'source_accounts.type AS source_type',
    'source_accounts.status AS source_status',
    'source_accounts.schedulable AS source_schedulable',
    'source_accounts.availability_schedule_json AS source_availability_schedule_json',
    'source_accounts.account_expires_at AS source_account_expires_at',
    'source_accounts.cooldown_until AS source_cooldown_until',
    'source_accounts.last_error_code AS source_last_error_code',
    'source_accounts.last_error_message AS source_last_error_message',
    'source_accounts.credential_mask AS source_credential_mask',
    includeCredentials ? 'source_accounts.credentials_encrypted AS source_credentials_encrypted' : "'' AS source_credentials_encrypted",
    'source_accounts.proxy_profile_id AS source_proxy_profile_id',
    'source_accounts.concurrency_limit AS source_concurrency_limit',
    'source_accounts.client_compatibility AS source_client_compatibility'
  ].join(', ')
}

function accountListOuterSelectColumns(): string {
  return [
    'id',
    'config_revision',
    'system_account_id',
    'provider_code',
    'provider_protocol_profile_id',
    'protocol_code',
    'protocol_version',
    'name',
    'notes',
    'type',
    'status',
    'credential_mask',
    'credentials_encrypted',
    'proxy_profile_id',
    'concurrency_limit',
    'priority',
    'super_priority_enabled',
    'fallback_enabled',
    'client_compatibility',
    'schedulable',
    'availability_schedule_json',
    'account_expires_at',
    'last_used_at',
    'cooldown_until',
    'last_error_code',
    'last_error_message',
    'cooldown_retest_failure_count',
    'cooldown_retest_observation_started_at',
    'cooldown_retest_last_at',
    'cooldown_retest_last_status_code',
    'health_check_model',
    'health_check_endpoint_mode',
    'last_health_check_at',
    'next_health_check_at',
    'last_health_success_at',
    'health_check_failure_count',
    'last_health_check_status_code',
    'last_health_check_error_code',
    'last_health_check_error_message',
    'stream_failure_count',
    'stream_failure_window_started_at',
    'authorization_instance_source_account_id',
    'authorization_instance_authorization_id',
    'authorization_instance_owner_system_account_id',
    'deleted_at',
    'deleted_by',
    'created_at',
    'updated_at',
    'access_type',
    'authorization_id',
    'authorization_status',
    'authorization_expires_at',
    'authorization_limits_json',
    'authorization_effective_source_type',
    'authorization_effective_source_team_id',
    'authorization_resource_owner_system_account_id',
    'authorization_resource_id',
    'source_provider_code',
    'source_provider_protocol_profile_id',
    'source_protocol_code',
    'source_protocol_version',
    'source_type',
    'source_status',
    'source_schedulable',
    'source_availability_schedule_json',
    'source_account_expires_at',
    'source_cooldown_until',
    'source_last_error_code',
    'source_last_error_message',
    'source_credential_mask',
    'source_credentials_encrypted',
    'source_proxy_profile_id',
    'source_concurrency_limit',
    'source_client_compatibility'
  ].map((column) => `account_rows.${column}`).join(', ')
}

export function hydrateAccountRowsWithRuntimeState(rows: AccountListRow[], options: { includeCredentials?: boolean } = {}): AccountListRow[] {
  if (rows.length === 0) return rows
  const includeCredentials = options.includeCredentials ?? true
  const rowsWithSources = hydrateAuthorizedAccountSourceFacts(rows, includeCredentials)
  const ids = [...new Set(rows.map((row) => row.id).filter(Boolean))]
  if (ids.length === 0) return rows
  const supportedModelAccountIds = [...new Set(rowsWithSources.map((row) => supportedModelAccountIdForRow(row)).filter(Boolean))]
  const supportedModelsByAccountId = loadSupportedModelsByAccountIds(supportedModelAccountIds)
  const modelMappingsByAccountId = loadModelMappingsByAccountIds(supportedModelAccountIds)
  const qualityRows = loadAccountRuntimeQualityRows(ids)
  const qualityByAccount = new Map(qualityRows.map((row) => [row.account_id, row]))
  return rowsWithSources.map((row) => {
    const quality = qualityByAccount.get(row.id)
    const runtimeAccountId = supportedModelAccountIdForRow(row)
    const supportedModels = supportedModelsByAccountId.get(runtimeAccountId) ?? []
    const modelMappings = modelMappingsByAccountId.get(runtimeAccountId) ?? []
    if (!quality) return { ...row, supported_models: supportedModels, model_mappings: modelMappings }
    return {
      ...row,
      supported_models: supportedModels,
      model_mappings: modelMappings,
      quality_score: quality.quality_score,
      quality_state: quality.quality_state,
      quality_ewma_first_token_ms: quality.ewma_first_token_ms,
      quality_recent_avg_first_token_ms: quality.recent_avg_first_token_ms,
      quality_recent_request_count: quality.recent_request_count,
      quality_recent_error_count: quality.recent_error_count,
      quality_recent_success_rate: quality.success_rate,
      quality_last_error_at: quality.last_error_at,
      quality_last_error_message: quality.last_error_message,
      quality_updated_at: quality.updated_at
    }
  })
}

function loadAccountRuntimeQualityRows(ids: string[]): Array<{
  account_id: string
  quality_score: number | null
  quality_state: string | null
  ewma_first_token_ms: number | null
  recent_avg_first_token_ms: number | null
  recent_request_count: number | null
  recent_error_count: number | null
  success_rate: number | null
  last_error_at: string | null
  last_error_message: string | null
  updated_at: string | null
}> {
  if (isSqliteReadWorkerProcess() && !existsSync(statsDatabasePath())) {
    return []
  }
  try {
    return getStatsDatabase()
      .prepare(`
        SELECT account_id, quality_score, quality_state, ewma_first_token_ms, recent_avg_first_token_ms,
          recent_request_count, recent_error_count, success_rate, last_error_at, last_error_message, updated_at
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
        recent_error_count: number | null
        success_rate: number | null
        last_error_at: string | null
        last_error_message: string | null
        updated_at: string | null
      }>
  } catch (error) {
    if (isSqliteReadWorkerProcess() && isMissingAccountQualityTableError(error)) {
      return []
    }
    throw error
  }
}

function isSqliteReadWorkerProcess(): boolean {
  return process.env.JUHE_AI_SQLITE_READ_WORKER === 'true'
}

function isMissingAccountQualityTableError(error: unknown): boolean {
  return error instanceof Error && /no such table:\s*account_quality_scores/i.test(error.message)
}

export function accountCredentialsForList(row: AccountListRow, includeCredentials = true): Record<string, unknown> {
  if (!includeCredentials) return {}
  if (row.access_type === 'authorized' && !row.source_credentials_encrypted) return {}
  const credentialsEncrypted = row.access_type === 'authorized'
    ? row.source_credentials_encrypted
    : row.credentials_encrypted
  if (!credentialsEncrypted) return {}
  const credentials = decryptJson<Record<string, unknown>>(credentialsEncrypted)
  if (row.access_type !== 'authorized') {
    return credentials
  }
  return publicAccountCredentials(credentials)
}

function publicAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of publicAccountCredentialKeys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) {
      output[key] = credentials[key]
    }
  }
  return output
}

const publicAccountCredentialKeys = [
  'base_url',
  'expires_at',
  'client_id',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type'
] as const

function hydrateAuthorizedAccountSourceFacts(rows: AccountListRow[], includeCredentials: boolean): AccountListRow[] {
  const sourceIds = [...new Set(rows
    .filter((row) => row.access_type === 'authorized')
    .map((row) => row.authorization_instance_source_account_id ?? '')
    .filter(Boolean))]
  if (!sourceIds.length) return rows

  const sourceRows: Array<{
    id: string
    provider_code: AccountListRow['provider_code']
    provider_protocol_profile_id: string
    protocol_code: string
    protocol_version: string
    type: AccountListRow['type']
    status: AccountListRow['status']
    schedulable: number
    availability_schedule_json: string | null
    account_expires_at: string | null
    cooldown_until: string | null
    last_error_code: string | null
    last_error_message: string | null
    credential_mask: string | null
    credentials_encrypted: string | null
    proxy_profile_id: string | null
    concurrency_limit: number | null
    client_compatibility: AccountListRow['client_compatibility']
  }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(sourceIds, 900)) {
    sourceRows.push(...database
      .prepare(`
        SELECT id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, type, status, schedulable, availability_schedule_json, account_expires_at, cooldown_until,
          last_error_code, last_error_message, credential_mask,
          ${includeCredentials ? 'credentials_encrypted' : "'' AS credentials_encrypted"},
          proxy_profile_id, concurrency_limit, client_compatibility
        FROM accounts
        WHERE deleted_at IS NULL
          AND id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as typeof sourceRows)
  }
  const sourceById = new Map(sourceRows.map((row) => [row.id, row]))
  return rows.map((row) => {
    const source = row.access_type === 'authorized' && row.authorization_instance_source_account_id
      ? sourceById.get(row.authorization_instance_source_account_id)
      : undefined
    if (!source) return row
    return {
      ...row,
      source_provider_code: source.provider_code,
      source_provider_protocol_profile_id: source.provider_protocol_profile_id,
      source_protocol_code: source.protocol_code,
      source_protocol_version: source.protocol_version,
      source_type: source.type,
      source_status: source.status,
      source_schedulable: source.schedulable,
      source_availability_schedule_json: source.availability_schedule_json,
      source_account_expires_at: source.account_expires_at,
      source_cooldown_until: source.cooldown_until,
      source_last_error_code: source.last_error_code,
      source_last_error_message: source.last_error_message,
      source_credential_mask: source.credential_mask,
      source_credentials_encrypted: source.credentials_encrypted,
      source_proxy_profile_id: source.proxy_profile_id,
      source_concurrency_limit: source.concurrency_limit,
      source_client_compatibility: source.client_compatibility
    }
  })
}

function supportedModelAccountIdForRow(row: AccountListRow): string {
  if (row.access_type !== 'authorized') return row.id
  return row.authorization_instance_source_account_id && row.source_provider_code
    ? row.authorization_instance_source_account_id
    : ''
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
          group_bindings.local_priority AS bound_group_local_priority,
          group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
          group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled`
}

function accountBindingSubquery(): string {
  return `group_accounts`
}

function buildAccountListFilters(options: AccountRowQueryOptions, containsSubquery?: AccountNameContainsSubquery): { clause: string; params: AccountFilterValue[] } {
  const clauses: string[] = []
  const params: AccountFilterValue[] = []
  if (options.accountId) {
    clauses.push('account_rows.id = ?')
    params.push(options.accountId)
  }
  if (options.ids.length) {
    clauses.push(`account_rows.id IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordUpperBound = accountTextPrefixUpperBound(keyword)
    const keywordClauses = [
      '(account_rows.name >= ? AND account_rows.name < ?)'
    ]
    const keywordParams: AccountFilterValue[] = [
      keyword,
      keywordUpperBound
    ]
    if (containsSubquery) {
      keywordClauses.push(`account_rows.id IN (${containsSubquery.sql})`)
      keywordParams.push(...containsSubquery.params)
    }
    clauses.push(`(
      ${keywordClauses.join('\n      OR ')}
    )`)
    params.push(...keywordParams)
  }
  if (options.providerCode && options.providerCode !== 'all') {
    clauses.push('account_rows.provider_code = ?')
    params.push(options.providerCode)
  }
  if (options.providerProtocolProfileId && options.providerProtocolProfileId !== 'all') {
    clauses.push('account_rows.provider_protocol_profile_id = ?')
    params.push(options.providerProtocolProfileId)
  }
  const groupId = options.groupId?.trim()
  if (groupId) {
    clauses.push('group_bindings.group_id = ?')
    params.push(groupId)
  }
  if (options.tagIds.length) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM account_tag_bindings tag_filter
      WHERE tag_filter.account_id = account_rows.id
        AND tag_filter.system_account_id = account_rows.system_account_id
        AND tag_filter.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
    params.push(...options.tagIds)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('account_rows.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  const includeAuthorizationQuotaStatus = statuses.includes('active') || statuses.includes('rate_limited')
  if (statuses.length === 1) {
    const statusExpression = accountEffectiveStatusFilterExpression(includeAuthorizationQuotaStatus)
    clauses.push(`${statusExpression.sql} = ?`)
    params.push(...statusExpression.params)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    const statusExpression = accountEffectiveStatusFilterExpression(includeAuthorizationQuotaStatus)
    clauses.push(`${statusExpression.sql} IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statusExpression.params)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    const schedulableExpression = accountEffectiveSchedulableExpression(true)
    clauses.push(`${schedulableExpression.sql} = 1`)
    params.push(...schedulableExpression.params)
  } else if (options.schedulable === 'disabled') {
    const statusExpression = accountEffectiveStatusFilterExpression(false)
    const quotaExpression = authorizedAccountQuotaExceededExpression()
    clauses.push(`(
      (account_rows.access_type = 'authorized' AND (
        ${authorizedBindingUnavailableExpression()}
        OR ${authorizedSourceAccountHardUnavailableExpression()}
        OR ${statusExpression.sql} IN ('disabled', 'error')
        OR ${quotaExpression.sql}
      ))
      OR (account_rows.access_type <> 'authorized' AND (account_rows.status = 'disabled' OR account_rows.schedulable <> 1))
    )`)
    params.push(...statusExpression.params, ...quotaExpression.params)
  } else if (options.schedulable === 'cooling') {
    clauses.push(`${accountCoolingFilterExpression()} = 1`)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function accountTextPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\uffff`
}
