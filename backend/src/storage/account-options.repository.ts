import type { AccountOptionSummary, AccountStatus, AuthorizationStatus } from '../domain/types.js'
import { canAccessAll, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { accountStatusFilterValues, normalizeAccountOptionListOptions, type AccountOptionListOptions } from './account-list-options.js'
import { authorizationRuntimeBlockingStatus, currentIsoSql, disableExpiredAccounts } from './account-runtime-status.js'
import { getBusinessDatabase } from './database.js'
import { escapeLikePrefix, sqlPlaceholders } from './query-utils.js'
import { ensureRequestQuotaDatabaseAttached, requestQuotaExceededSql, type RequestQuotaSqlExpression } from './request-quota-sql.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'

type AccountOptionFilterValue = string | number
type AccountOptionFilterExpression = {
  sql: string
  params: AccountOptionFilterValue[]
}

interface AccountOptionRow {
  id: string
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  type: string
  status: AccountStatus
  schedulable: number
  account_expires_at: string | null
  cooldown_until?: string | null
  priority: number
  created_at: string
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  access_type: 'owner' | 'authorized'
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
  authorization_expires_at?: string | null
  authorization_resource_owner_system_account_id?: string | null
  authorization_resource_id?: string | null
}

export function listAccountOptions(access?: AccessScope, options?: AccountOptionListOptions): AccountOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountOptionListOptions(options)
  const rows = queryAccountOptionRowsForAccess(access, listOptions)
  return accountOptionSummariesFromRows(rows, access, viewerSystemAccountId)
}

function accountOptionSummariesFromRows(rows: AccountOptionRow[], access: AccessScope | undefined, viewerSystemAccountId: string | undefined): AccountOptionSummary[] {
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows
    ? loadSystemAccountNameMapByIds(rows.flatMap((row) => [
        row.system_account_id,
        row.authorization_resource_owner_system_account_id ?? '',
        row.authorization_instance_owner_system_account_id ?? ''
      ]))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const effectiveStatus = isAuthorizedView
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: isAuthorizedView ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id : row.system_account_id,
      ownerSystemAccountName: accountNames.get(isAuthorizedView ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id : row.system_account_id),
      providerCode: row.provider_code,
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      name: row.name,
      type: row.type,
      status: effectiveStatus,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      accountExpiresAt: row.account_expires_at ?? undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(false) : ownerPermissions()
    }
  })
}

function queryAccountOptionRowsForAccess(access: AccessScope | undefined, options: ReturnType<typeof normalizeAccountOptionListOptions>): AccountOptionRow[] {
  const database = getBusinessDatabase()
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const limit = options.pageSize
  const offset = (options.page - 1) * options.pageSize
  const queryRows = (selectSql: string, params: AccountOptionFilterValue[]): AccountOptionRow[] => database
    .prepare(`
      SELECT *
      FROM (
        ${selectSql}
      ) account_option_rows
      ORDER BY CASE WHEN account_option_rows.access_type = 'authorized' THEN 0 ELSE account_option_rows.priority END ASC,
        account_option_rows.created_at ASC,
        account_option_rows.id ASC
      LIMIT ? OFFSET ?
    `)
    .all(...params, limit, offset) as unknown as AccountOptionRow[]

  if (!ownerSystemAccountId && canAccessAll(access)) {
    const filters = buildAccountOptionFilters(options, 'accounts.system_account_id')
    return queryRows(`
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at
      FROM accounts
      WHERE accounts.deleted_at IS NULL${filters.clause}
    `, filters.params)
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }

  const ownerId = ownerSystemAccountId ?? viewerSystemAccountId
  const includeAuthorizationQuotaFilter = accountOptionQuotaFilterRequested(options)
    && visibleAuthorizedAccountQuotaLimitsMayExist(database, ownerId, viewerSystemAccountId)
  if (includeAuthorizationQuotaFilter) {
    ensureRequestQuotaDatabaseAttached(database)
  }
  const ownerFilters = buildAccountOptionFilters(options, 'accounts.system_account_id')
  const authorizedFilters = buildAccountOptionFilters(options, '?', [viewerSystemAccountId], true, includeAuthorizationQuotaFilter)
  return queryRows(`
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type,
      NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at,
      NULL AS authorization_resource_owner_system_account_id, NULL AS authorization_resource_id
    FROM accounts
    WHERE accounts.system_account_id = ?
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL${ownerFilters.clause}
    UNION ALL
    SELECT ${accountOptionSelectColumns()}, 'authorized' AS access_type,
      ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
      ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      ra.resource_id AS authorization_resource_id
    FROM accounts
    INNER JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN group_accounts option_group_bindings
      ON option_group_bindings.account_id = accounts.id
      AND option_group_bindings.system_account_id = ?
      AND option_group_bindings.enabled = 1
    WHERE accounts.system_account_id = ?
      AND accounts.deleted_at IS NULL
      AND ra.resource_type = 'account'
      AND ra.grantee_system_account_id = ?
      AND ra.status IN ('active', 'paused', 'expired')
      AND accounts.authorization_instance_authorization_id IS NOT NULL${authorizedFilters.clause}
  `, [ownerId, ...ownerFilters.params, viewerSystemAccountId, ownerId, viewerSystemAccountId, ...authorizedFilters.params])
}

function accountOptionQuotaFilterRequested(options: ReturnType<typeof normalizeAccountOptionListOptions>): boolean {
  const statuses = accountStatusFilterValues(options.status)
  return statuses.includes('active')
    || statuses.includes('rate_limited')
    || options.schedulable === 'enabled'
    || options.schedulable === 'disabled'
}

function visibleAuthorizedAccountQuotaLimitsMayExist(
  database: ReturnType<typeof getBusinessDatabase>,
  ownerSystemAccountId: string,
  viewerSystemAccountId: string
): boolean {
  const row = database.prepare(`
    SELECT accounts.id
    FROM accounts
    INNER JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
    WHERE accounts.system_account_id = ?
      AND accounts.deleted_at IS NULL
      AND ra.resource_type = 'account'
      AND ra.grantee_system_account_id = ?
      AND ra.status IN ('active', 'paused', 'expired')
      AND accounts.authorization_instance_authorization_id IS NOT NULL
      AND (
        ra.limits_json IS NOT NULL
        OR (
          ra.effective_source_team_id IS NOT NULL
          AND EXISTS (
            SELECT 1
            FROM resource_authorization_grants grant_rows
            WHERE grant_rows.resource_type = 'account'
              AND grant_rows.resource_id = ra.resource_id
              AND grant_rows.resource_owner_system_account_id = ra.resource_owner_system_account_id
              AND grant_rows.grantee_type = 'team'
              AND grant_rows.grantee_team_id = ra.effective_source_team_id
              AND grant_rows.status = 'active'
              AND grant_rows.limits_json IS NOT NULL
              AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ${currentIsoSql})
            LIMIT 1
          )
        )
      )
    LIMIT 1
  `).get(ownerSystemAccountId, viewerSystemAccountId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function accountOptionSelectColumns(): string {
  return [
    'accounts.id',
    'accounts.system_account_id',
    'accounts.provider_code',
    'accounts.provider_protocol_profile_id',
    'accounts.protocol_code',
    'accounts.protocol_version',
    'accounts.name',
    'accounts.type',
    'accounts.status',
    'accounts.schedulable',
    'accounts.account_expires_at',
    'accounts.cooldown_until',
    'accounts.priority',
    'accounts.created_at',
    'accounts.authorization_instance_source_account_id',
    'accounts.authorization_instance_authorization_id',
    'accounts.authorization_instance_owner_system_account_id',
    'accounts.deleted_at',
    'accounts.deleted_by'
  ].join(', ')
}

function buildAccountOptionFilters(
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  groupBindingSystemAccountExpression: string,
  groupBindingSystemAccountParams: string[] = [],
  authorizedView = false,
  includeAuthorizationQuota = false
): { clause: string; params: AccountOptionFilterValue[] } {
  const clauses: string[] = []
  const params: AccountOptionFilterValue[] = []
  if (options.ids.length) {
    clauses.push(`accounts.id IN (${sqlPlaceholders(options.ids.length)})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordPrefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(`(
      accounts.name COLLATE NOCASE = ?
      OR accounts.name LIKE ? ESCAPE '\\'
    )`)
    params.push(
      keyword,
      keywordPrefix
    )
  }
  const groupId = options.groupId?.trim()
  if (groupId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM group_accounts option_group_accounts
      WHERE option_group_accounts.account_id = accounts.id
        AND option_group_accounts.system_account_id = ${groupBindingSystemAccountExpression}
        AND option_group_accounts.group_id = ?
        AND option_group_accounts.enabled = 1
    )`)
    params.push(...groupBindingSystemAccountParams, groupId)
  }
  if (options.tagIds.length) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM account_tag_bindings option_tag_bindings
      WHERE option_tag_bindings.account_id = accounts.id
        AND option_tag_bindings.system_account_id = accounts.system_account_id
        AND option_tag_bindings.tag_id IN (${sqlPlaceholders(options.tagIds.length)})
    )`)
    params.push(...options.tagIds)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  const includeAuthorizationQuotaStatus = authorizedView
    && includeAuthorizationQuota
    && (statuses.includes('active') || statuses.includes('rate_limited'))
  const quotaStatusExpression = includeAuthorizationQuotaStatus ? authorizedOptionQuotaExceededExpression() : undefined
  const authorizedStatusExpression = `CASE
    WHEN ra.status <> 'active'
      OR (ra.expires_at IS NOT NULL AND ra.expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql} THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN accounts.status
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    ${quotaStatusExpression ? `WHEN ${quotaStatusExpression.sql} THEN 'rate_limited'` : ''}
    ELSE accounts.status
  END`
  const authorizedBindingAvailableExpression = `option_group_bindings.group_id IS NOT NULL
    AND option_group_bindings.account_authorization_id IS NOT NULL
    AND option_group_bindings.account_authorization_id = ra.id`
  const authorizedBindingUnavailableExpression = `option_group_bindings.group_id IS NULL
    OR option_group_bindings.account_authorization_id IS NULL
    OR option_group_bindings.account_authorization_id <> ra.id`
  const authorizedAuthorizationAvailableExpression = `ra.status = 'active'
    AND (ra.expires_at IS NULL OR ra.expires_at > ${currentIsoSql})`
  const authorizedAccountAvailableExpression = `accounts.schedulable = 1
    AND accounts.status = 'active'
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ${currentIsoSql})`
  const authorizedAccountHardUnavailableExpression = `accounts.schedulable <> 1
    OR accounts.status IN ('pending_test', 'disabled', 'error')
    OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql})`
  const authorizedAccountCoolingExpression = `accounts.status IN ('rate_limited', 'temporary_unavailable')
    OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql})`
  if (statuses.length === 1) {
    clauses.push(authorizedView ? `${authorizedStatusExpression} = ?` : 'accounts.status = ?')
    if (authorizedView && quotaStatusExpression) params.push(...quotaStatusExpression.params)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(authorizedView
      ? `${authorizedStatusExpression} IN (${statuses.map(() => '?').join(', ')})`
      : `accounts.status IN (${statuses.map(() => '?').join(', ')})`)
    if (authorizedView && quotaStatusExpression) params.push(...quotaStatusExpression.params)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    if (authorizedView) {
      const quotaExpression = includeAuthorizationQuota ? authorizedOptionQuotaExceededExpression() : undefined
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND ${authorizedAccountAvailableExpression}
        ${quotaExpression ? `AND NOT (${quotaExpression.sql})` : ''}`)
      if (quotaExpression) params.push(...quotaExpression.params)
    } else {
      clauses.push(`accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})`)
    }
  } else if (options.schedulable === 'disabled') {
    if (authorizedView) {
      const quotaExpression = includeAuthorizationQuota ? authorizedOptionQuotaExceededExpression() : undefined
      clauses.push(`(${authorizedBindingUnavailableExpression}
        OR ${authorizedStatusExpression} IN ('disabled', 'error')
        ${quotaExpression ? `OR ${quotaExpression.sql}` : ''}
      )`)
      if (quotaStatusExpression) params.push(...quotaStatusExpression.params)
      if (quotaExpression) params.push(...quotaExpression.params)
    } else {
      clauses.push("(accounts.status = 'disabled' OR accounts.schedulable <> 1)")
    }
  } else if (options.schedulable === 'cooling') {
    if (authorizedView) {
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND NOT (${authorizedAccountHardUnavailableExpression})
        AND (${authorizedAccountCoolingExpression})`)
    } else {
      clauses.push(`(accounts.status IN ('rate_limited', 'temporary_unavailable')
        OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql}))`)
    }
  }
  return {
    clause: clauses.length ? ` AND ${clauses.join(' AND ')}` : '',
    params
  }
}

function authorizedOptionQuotaExceededExpression(): AccountOptionFilterExpression {
  const directQuota = requestQuotaExceededSql({
    limitsSql: 'ra.limits_json',
    systemAccountSql: 'accounts.system_account_id',
    scopeType: 'account_authorization',
    scopeIdSql: 'ra.id'
  })
  const teamQuota = requestQuotaExceededSql({
    limitsSql: authorizedOptionTeamGrantLimitsSql(),
    systemAccountSql: 'accounts.system_account_id',
    scopeType: 'account_authorization_team',
    scopeIdSql: "accounts.id || ':' || ra.effective_source_team_id"
  })
  return mergeOptionQuotaExpressions(directQuota, teamQuota, 'ra.effective_source_team_id IS NOT NULL')
}

function authorizedOptionTeamGrantLimitsSql(): string {
  return `(SELECT grant_rows.limits_json
    FROM resource_authorization_grants grant_rows
    WHERE grant_rows.resource_type = 'account'
      AND grant_rows.resource_id = ra.resource_id
      AND grant_rows.resource_owner_system_account_id = ra.resource_owner_system_account_id
      AND grant_rows.grantee_type = 'team'
      AND grant_rows.grantee_team_id = ra.effective_source_team_id
      AND grant_rows.status = 'active'
      AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ${currentIsoSql})
    LIMIT 1)`
}

function mergeOptionQuotaExpressions(
  directQuota: RequestQuotaSqlExpression,
  teamQuota: RequestQuotaSqlExpression,
  teamGuardSql: string
): AccountOptionFilterExpression {
  return {
    sql: `(ra.id IS NOT NULL
      AND (${directQuota.sql}
        OR (${teamGuardSql} AND ${teamQuota.sql})))`,
    params: [...directQuota.params, ...teamQuota.params]
  }
}
