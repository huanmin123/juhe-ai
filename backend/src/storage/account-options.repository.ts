import type { AccountOptionSummary, AccountStatus, AuthorizationStatus, ModelCheckAccountOption } from '../domain/types.js'
import { canAccessAll, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { accountStatusFilterValues, normalizeAccountOptionListOptions, type AccountOptionListOptions } from './account-list-options.js'
import { accountApiKeyPoolAllUnavailableSql, ensureAccountDerivedStatusSqlFunctions } from './account-derived-status-sql.js'
import { accountNameSearchQueryTerms, normalizeAccountNameSearchText } from './account-name-search.repository.js'
import { authorizationRuntimeBlockingStatus, currentIsoSql } from './account-runtime-status.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { sqlPlaceholders } from './query-utils.js'
import { ensureRequestQuotaDatabaseAttached, requestQuotaExceededSql, type RequestQuotaSqlExpression } from './request-quota-sql.js'
import { authorizedAccountPermissions, ownerPermissions } from './resource-permissions.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { getPostgresPool } from './postgres-client.js'
import { loadAuthorizationQuotaExceededByAuthorizationIdAsync } from './account-summary.repository.js'
import type { AccountListRow } from './repository-row-types.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

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
  availability_schedule_json?: string | null
  cooldown_until?: string | null
  last_error_code?: string | null
  priority: number
  created_at: string
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  access_type: 'owner' | 'authorized'
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
  authorization_expires_at?: string | null
  authorization_limits_json?: string | null
  authorization_effective_source_team_id?: string | null
  authorization_resource_owner_system_account_id?: string | null
  authorization_resource_id?: string | null
  bound_group_id?: string | null
  bound_group_account_authorization_id?: string | null
  source_status?: AccountStatus | null
  source_schedulable?: number | null
  source_availability_schedule_json?: string | null
  source_account_expires_at?: string | null
  source_cooldown_until?: string | null
  source_last_error_code?: string | null
}

interface AccountOptionCandidatePage<T> {
  items: T[]
  exhausted: boolean
}

export interface ModelCheckAccountOptionListOptions {
  purpose: 'run' | 'history'
  keyword?: string
  selectedIds?: string[]
  limit: number
}

export function listModelCheckAccountOptions(access: AccessScope | undefined, options: ModelCheckAccountOptionListOptions): ModelCheckAccountOption[] {
  const base = normalizeAccountOptionListOptions({ keyword: options.keyword, status: options.purpose === 'run' ? 'active' : undefined, schedulable: options.purpose === 'run' ? 'enabled' : 'all', limit: options.limit })
  const rows = queryAccountOptionRowsForAccess(access, base)
  const selected = options.selectedIds?.length ? queryAccountOptionRowsForAccess(access, normalizeAccountOptionListOptions({ ids: options.selectedIds, status: options.purpose === 'run' ? 'active' : undefined, schedulable: options.purpose === 'run' ? 'enabled' : 'all', limit: options.selectedIds.length })) : []
  return modelCheckOptionsFromRows([...rows, ...selected], options.limit)
}

export async function listModelCheckAccountOptionsAsync(access: AccessScope | undefined, options: ModelCheckAccountOptionListOptions): Promise<ModelCheckAccountOption[]> {
  if (sqliteReadWorkerPoolEnabled()) return requestSqliteReadWorker({ type: 'list_model_check_account_options_read_only', access, options })
  if (runtimeConfig.databaseDriver !== 'postgres') return listModelCheckAccountOptions(access, options)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const base = normalizeAccountOptionListOptions({ keyword: options.keyword, status: options.purpose === 'run' ? 'active' : undefined, schedulable: options.purpose === 'run' ? 'enabled' : 'all', limit: options.limit })
  const rows = await queryAccountOptionRowsForAccessAsync(client, access, base)
  const selected = options.selectedIds?.length ? await queryAccountOptionRowsForAccessAsync(client, access, normalizeAccountOptionListOptions({ ids: options.selectedIds, status: options.purpose === 'run' ? 'active' : undefined, schedulable: options.purpose === 'run' ? 'enabled' : 'all', limit: options.selectedIds.length })) : []
  return modelCheckOptionsFromRows([...rows, ...selected], options.limit)
}

function modelCheckOptionsFromRows(rows: AccountOptionRow[], limit: number): ModelCheckAccountOption[] {
  const seen = new Set<string>()
  return rows.filter((row) => {
    if (seen.has(row.id) || !row.name.trim()) return false
    seen.add(row.id)
    return [
      ['gpt', 'profile_gpt_openai_v1'], ['openai', 'profile_openai_openai_v1'],
      ['deepseek', 'profile_deepseek_openai_v1'], ['deepseek', 'profile_deepseek_anthropic_v1'],
      ['glm', 'profile_glm_general_openai_v1'], ['glm', 'profile_glm_coding_openai_v1'], ['glm', 'profile_glm_coding_anthropic_v1'],
      ['anthropic', 'profile_anthropic_anthropic_v1'], ['gemini', 'profile_gemini_native_v1beta'], ['gemini', 'profile_gemini_openai_chat_v1beta']
    ].some(([provider, profile]) => provider === row.provider_code && profile === row.provider_protocol_profile_id)
  }).sort((left, right) => left.name.localeCompare(right.name, undefined, { sensitivity: 'base' }) || left.id.localeCompare(right.id)).slice(0, limit).map((row) => ({ id: row.id, name: row.name, providerCode: row.provider_code, providerProtocolProfileId: row.provider_protocol_profile_id, protocolCode: row.protocol_code, protocolVersion: row.protocol_version }))
}

export async function collectAccountOptionCandidateMatches<T>(
  limit: number,
  loadPage: (page: number) => Promise<AccountOptionCandidatePage<T>>
): Promise<T[]> {
  const output: T[] = []
  let page = 1
  let exhausted = false
  while (output.length < limit && !exhausted) {
    const candidatePage = await loadPage(page)
    output.push(...candidatePage.items.slice(0, limit - output.length))
    exhausted = candidatePage.exhausted
    page += 1
  }
  return output
}

export function listAccountOptions(access?: AccessScope, options?: AccountOptionListOptions): AccountOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const listOptions = normalizeAccountOptionListOptions(options)
  const rows = queryAccountOptionRowsForAccess(access, listOptions)
  return accountOptionSummariesFromRows(rows, access, viewerSystemAccountId)
}

export async function listAccountOptionsAsync(access?: AccessScope, options?: AccountOptionListOptions): Promise<AccountOptionSummary[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_account_options_read_only',
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountOptions(access, options)
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const listOptions = normalizeAccountOptionListOptions(options)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  if (accountOptionQuotaFilterRequested(listOptions)) {
    return listAccountOptionsWithQuotaFilterAsync(client, access, listOptions, viewerSystemAccountId)
  }
  const rows = await queryAccountOptionRowsForAccessAsync(client, access, listOptions)
  return accountOptionSummariesFromRowsAsync(client, rows, access, viewerSystemAccountId)
}

async function listAccountOptionsWithQuotaFilterAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  viewerSystemAccountId: string | undefined
): Promise<AccountOptionSummary[]> {
  const statuses = accountStatusFilterValues(options.status)
  const candidatePageSize = 200
  const rows = await collectAccountOptionCandidateMatches(options.pageSize, async (candidatePage) => {
    const candidateRows = await queryAccountOptionRowsForAccessAsync(client, access, {
      ...options,
      status: undefined,
      schedulable: 'all',
      page: candidatePage,
      pageSize: candidatePageSize
    })
    const quotaExceededByAuthorization = await loadAuthorizationQuotaExceededByAuthorizationIdAsync(
      client,
      candidateRows as unknown as AccountListRow[]
    )
    const matchedRows: AccountOptionRow[] = []
    for (const row of candidateRows) {
      const quotaExceeded = row.authorization_id
        ? quotaExceededByAuthorization.get(row.authorization_id) === true
        : false
      const effectiveStatus: AccountStatus = quotaExceeded && row.status === 'active' ? 'rate_limited' : row.status
      if (statuses.length > 0 && !statuses.includes(effectiveStatus)) continue
      if (options.schedulable === 'enabled' && (effectiveStatus !== 'active' || quotaExceeded)) continue
      if (options.schedulable === 'disabled' && effectiveStatus !== 'disabled' && effectiveStatus !== 'error' && !quotaExceeded) continue
      matchedRows.push({ ...row, status: effectiveStatus })
    }
    return { items: matchedRows, exhausted: candidateRows.length < candidatePageSize }
  })
  return accountOptionSummariesFromRowsAsync(client, rows, access, viewerSystemAccountId)
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

async function accountOptionSummariesFromRowsAsync(
  client: DatabaseClient,
  rows: AccountOptionRow[],
  access: AccessScope | undefined,
  viewerSystemAccountId: string | undefined
): Promise<AccountOptionSummary[]> {
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows
    ? await loadAccountOptionSystemAccountNamesAsync(client, rows.flatMap((row) => [
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

async function accountOptionSummariesFromOwnerRowsAsync(
  client: DatabaseClient,
  rows: AccountOptionRow[],
  access: AccessScope | undefined
): Promise<AccountOptionSummary[]> {
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields
    ? await loadAccountOptionSystemAccountNamesAsync(client, rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
    systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
    ownerSystemAccountId: row.system_account_id,
    ownerSystemAccountName: accountNames.get(row.system_account_id),
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    name: row.name,
    type: row.type,
    status: row.status,
    accessType: 'owner',
    accountExpiresAt: row.account_expires_at ?? undefined,
    permissions: ownerPermissions()
  }))
}

async function queryOwnerAccountOptionRowsAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: ReturnType<typeof normalizeAccountOptionListOptions>
): Promise<AccountOptionRow[]> {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && !canAccessAll(access)) {
    throw new Error('缺少系统账户上下文')
  }
  const filters = buildOwnerAccountOptionFilters(client, options, ownerSystemAccountId)
  return await client.query<AccountOptionRow>(`
    SELECT
      accounts.id,
      accounts.system_account_id,
      accounts.provider_code,
      accounts.provider_protocol_profile_id,
      accounts.protocol_code,
      accounts.protocol_version,
      accounts.name,
      accounts.type,
      ${ownerAccountOptionEffectiveStatusSql()} AS status,
      accounts.schedulable,
      accounts.account_expires_at,
      accounts.availability_schedule_json,
      accounts.cooldown_until,
      accounts.last_error_code,
      accounts.priority,
      accounts.created_at,
      accounts.authorization_instance_source_account_id,
      accounts.authorization_instance_authorization_id,
      accounts.authorization_instance_owner_system_account_id,
      accounts.deleted_at,
      accounts.deleted_by,
      'owner' AS access_type,
      NULL AS authorization_id,
      NULL AS authorization_status,
      NULL AS authorization_expires_at,
      NULL AS authorization_limits_json,
      NULL AS authorization_effective_source_team_id,
      NULL AS authorization_resource_owner_system_account_id,
      NULL AS authorization_resource_id,
      NULL AS bound_group_id,
      NULL AS bound_group_account_authorization_id,
      NULL AS source_status,
      NULL AS source_schedulable,
      NULL AS source_availability_schedule_json,
      NULL AS source_account_expires_at,
      NULL AS source_cooldown_until,
      NULL AS source_last_error_code
    FROM ${accountOptionTable(client, 'accounts')} accounts
    ${filters.clause}
    ORDER BY accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
    LIMIT ? OFFSET ?
  `, [
    ...filters.params,
    options.pageSize,
    (options.page - 1) * options.pageSize
  ])
}

async function queryAccountOptionRowsForAccessAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: ReturnType<typeof normalizeAccountOptionListOptions>
): Promise<AccountOptionRow[]> {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return queryOwnerAccountOptionRowsAsync(client, access, options)
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }

  const ownerId = ownerSystemAccountId ?? viewerSystemAccountId
  const includeAuthorizationQuotaFilter = false
  const ownerFilters = buildOwnerAccountOptionFilters(client, options, ownerId)
  const authorizedFilters = buildAuthorizedAccountOptionFilters(client, options, viewerSystemAccountId, includeAuthorizationQuotaFilter)
  return await client.query<AccountOptionRow>(`
    SELECT *
    FROM (
      SELECT
        accounts.id,
        accounts.system_account_id,
        accounts.provider_code,
        accounts.provider_protocol_profile_id,
        accounts.protocol_code,
        accounts.protocol_version,
        accounts.name,
        accounts.type,
        ${ownerAccountOptionEffectiveStatusSql()} AS status,
        accounts.schedulable,
        accounts.account_expires_at,
        accounts.availability_schedule_json,
        accounts.cooldown_until,
        accounts.last_error_code,
        accounts.priority,
        accounts.created_at,
        accounts.authorization_instance_source_account_id,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_owner_system_account_id,
        'owner' AS access_type,
        NULL AS authorization_id,
        NULL AS authorization_status,
        NULL AS authorization_expires_at,
        NULL AS authorization_limits_json,
        NULL AS authorization_effective_source_team_id,
        NULL AS authorization_resource_owner_system_account_id,
        NULL AS authorization_resource_id,
        NULL AS bound_group_id,
        NULL AS bound_group_account_authorization_id,
        NULL AS source_status,
        NULL AS source_schedulable,
        NULL AS source_availability_schedule_json,
        NULL AS source_account_expires_at,
        NULL AS source_cooldown_until,
        NULL AS source_last_error_code
      FROM ${accountOptionTable(client, 'accounts')} accounts
      ${ownerFilters.clause}
      UNION ALL
      SELECT
        accounts.id,
        accounts.system_account_id,
        accounts.provider_code,
        accounts.provider_protocol_profile_id,
        accounts.protocol_code,
        accounts.protocol_version,
        accounts.name,
        accounts.type,
        ${authorizedAccountOptionEffectiveStatusSql(includeAuthorizationQuotaFilter)} AS status,
        accounts.schedulable,
        accounts.account_expires_at,
        accounts.availability_schedule_json,
        accounts.cooldown_until,
        accounts.last_error_code,
        accounts.priority,
        accounts.created_at,
        accounts.authorization_instance_source_account_id,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_owner_system_account_id,
        'authorized' AS access_type,
        ra.id AS authorization_id,
        ra.status AS authorization_status,
        ra.expires_at AS authorization_expires_at,
        ra.limits_json AS authorization_limits_json,
        ra.effective_source_team_id AS authorization_effective_source_team_id,
        ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
        ra.resource_id AS authorization_resource_id,
        option_group_bindings.group_id AS bound_group_id,
        option_group_bindings.account_authorization_id AS bound_group_account_authorization_id,
        ${sourceAccountOptionColumns()}
      FROM ${accountOptionTable(client, 'accounts')} accounts
      INNER JOIN ${accountOptionTable(client, 'resource_authorizations')} ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ${accountOptionTable(client, 'accounts')} source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      LEFT JOIN ${accountOptionTable(client, 'group_accounts')} option_group_bindings
        ON option_group_bindings.account_id = accounts.id
        AND option_group_bindings.system_account_id = ?
        AND option_group_bindings.enabled = 1
      ${authorizedFilters.clause}
    ) account_option_rows
    ORDER BY CASE WHEN account_option_rows.access_type = 'authorized' THEN 0 ELSE account_option_rows.priority END ASC,
      account_option_rows.created_at ASC,
      account_option_rows.id ASC
    LIMIT ? OFFSET ?
  `, [
    ...ownerFilters.params,
    viewerSystemAccountId,
    ...authorizedFilters.params,
    options.pageSize,
    (options.page - 1) * options.pageSize
  ])
}

function buildOwnerAccountOptionFilters(
  client: DatabaseClient,
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  ownerSystemAccountId?: string
): { clause: string; params: unknown[] } {
  const clauses = [
    'accounts.deleted_at IS NULL',
    'accounts.authorization_instance_authorization_id IS NULL'
  ]
  const params: unknown[] = []
  if (ownerSystemAccountId) {
    clauses.push('accounts.system_account_id = ?')
    params.push(ownerSystemAccountId)
  }
  if (options.ids.length) {
    clauses.push(`accounts.id IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordPrefix = normalizeAccountNameSearchText(keyword)
    const keywordClauses = [
      '(accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ?)'
    ]
    const keywordParams: unknown[] = [keywordPrefix, accountOptionNamePrefixUpperBound(keywordPrefix)]
    const containsSubquery = ownerAccountOptionNameContainsSubquery(client, keyword, ownerSystemAccountId)
    if (containsSubquery) {
      keywordClauses.push(`accounts.id IN (${containsSubquery.sql})`)
      keywordParams.push(...containsSubquery.params)
    }
    clauses.push(`(${keywordClauses.join(' OR ')})`)
    params.push(...keywordParams)
  }
  if (options.providerCode && options.providerCode !== 'all') {
    clauses.push('accounts.provider_code = ?')
    params.push(options.providerCode)
  }
  if (options.providerProtocolProfileId && options.providerProtocolProfileId !== 'all') {
    clauses.push('accounts.provider_protocol_profile_id = ?')
    params.push(options.providerProtocolProfileId)
  }
  const groupId = options.groupId?.trim()
  if (groupId) {
    if (ownerSystemAccountId) {
      clauses.push(`accounts.id IN (
      SELECT option_group_accounts.account_id
      FROM ${accountOptionTable(client, 'group_accounts')} option_group_accounts
      WHERE option_group_accounts.system_account_id = ?
        AND option_group_accounts.group_id = ?
        AND option_group_accounts.enabled = 1
    )`)
      params.push(ownerSystemAccountId, groupId)
    } else {
      clauses.push(`accounts.id IN (
      SELECT option_group_accounts.account_id
      FROM ${accountOptionTable(client, 'group_accounts')} option_group_accounts
      WHERE option_group_accounts.group_id = ?
        AND option_group_accounts.enabled = 1
    )`)
      params.push(groupId)
    }
  }
  if (options.tagIds.length) {
    if (ownerSystemAccountId) {
      clauses.push(`accounts.id IN (
      SELECT option_tag_bindings.account_id
      FROM ${accountOptionTable(client, 'account_tag_bindings')} option_tag_bindings
      WHERE option_tag_bindings.system_account_id = ?
        AND option_tag_bindings.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
      params.push(ownerSystemAccountId, ...options.tagIds)
    } else {
      clauses.push(`accounts.id IN (
      SELECT option_tag_bindings.account_id
      FROM ${accountOptionTable(client, 'account_tag_bindings')} option_tag_bindings
      WHERE option_tag_bindings.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
      params.push(...options.tagIds)
    }
  }
  if (options.type && options.type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  if (statuses.length === 1) {
    clauses.push(`${ownerAccountOptionEffectiveStatusSql()} = ?`)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(`${ownerAccountOptionEffectiveStatusSql()} IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    clauses.push(`${ownerAccountOptionEffectiveSchedulableSql()} = 1`)
  } else if (options.schedulable === 'disabled') {
    clauses.push(`(${ownerAccountOptionEffectiveSchedulableSql()} = 0 AND ${ownerAccountOptionCoolingSql()} = 0)`)
  } else if (options.schedulable === 'cooling') {
    clauses.push(`${ownerAccountOptionCoolingSql()} = 1`)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function ownerAccountOptionNameContainsSubquery(
  client: DatabaseClient,
  keyword: string,
  ownerSystemAccountId?: string
): { sql: string; params: unknown[] } | undefined {
  const terms = accountNameSearchQueryTerms(keyword)
  if (!terms.length) return undefined
  const systemAccountClause = ownerSystemAccountId ? 'search.system_account_id = ? AND' : ''
  const keywordContains = normalizeAccountNameSearchText(keyword)
  const params: unknown[] = ownerSystemAccountId
    ? [ownerSystemAccountId, ...terms, keywordContains, terms.length]
    : [...terms, keywordContains, terms.length]
  const containsExpression = client.driver === 'postgres'
    ? 'position(? in documents.normalized_name) > 0'
    : 'instr(documents.normalized_name, ?) > 0'
  return {
    sql: `
      WITH candidate_terms AS MATERIALIZED (
        SELECT search.account_id, search.term
        FROM ${accountOptionTable(client, 'account_name_search_terms')} search
        WHERE ${systemAccountClause} search.term IN (${terms.map(() => '?').join(', ')})
      )
      SELECT candidate_terms.account_id
      FROM candidate_terms
      INNER JOIN ${accountOptionTable(client, 'account_name_search_documents')} documents
        ON documents.account_id = candidate_terms.account_id
      WHERE ${containsExpression}
      GROUP BY candidate_terms.account_id
      HAVING COUNT(DISTINCT candidate_terms.term) = ?
    `,
    params
  }
}

function buildAuthorizedAccountOptionFilters(
  client: DatabaseClient,
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  viewerSystemAccountId: string,
  includeAuthorizationQuota: boolean
): { clause: string; params: unknown[] } {
  const clauses = [
    'accounts.system_account_id = ?',
    'accounts.deleted_at IS NULL',
    "ra.resource_type = 'account'",
    "ra.grantee_system_account_id = ?",
    "ra.status IN ('active', 'paused', 'expired')",
    'accounts.authorization_instance_authorization_id IS NOT NULL'
  ]
  const params: unknown[] = [viewerSystemAccountId, viewerSystemAccountId]
  if (options.ids.length) {
    clauses.push(`accounts.id IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordPrefix = normalizeAccountNameSearchText(keyword)
    const keywordClauses = [
      '(accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ?)'
    ]
    const keywordParams: unknown[] = [keywordPrefix, accountOptionNamePrefixUpperBound(keywordPrefix)]
    const containsSubquery = ownerAccountOptionNameContainsSubquery(client, keyword, viewerSystemAccountId)
    if (containsSubquery) {
      keywordClauses.push(`accounts.id IN (${containsSubquery.sql})`)
      keywordParams.push(...containsSubquery.params)
    }
    clauses.push(`(${keywordClauses.join(' OR ')})`)
    params.push(...keywordParams)
  }
  if (options.providerCode && options.providerCode !== 'all') {
    clauses.push('accounts.provider_code = ?')
    params.push(options.providerCode)
  }
  if (options.providerProtocolProfileId && options.providerProtocolProfileId !== 'all') {
    clauses.push('accounts.provider_protocol_profile_id = ?')
    params.push(options.providerProtocolProfileId)
  }
  const groupId = options.groupId?.trim()
  if (groupId) {
    clauses.push(`accounts.id IN (
      SELECT option_group_accounts.account_id
      FROM ${accountOptionTable(client, 'group_accounts')} option_group_accounts
      WHERE option_group_accounts.system_account_id = ?
        AND option_group_accounts.group_id = ?
        AND option_group_accounts.enabled = 1
    )`)
    params.push(viewerSystemAccountId, groupId)
  }
  if (options.tagIds.length) {
    clauses.push(`accounts.id IN (
      SELECT option_tag_bindings.account_id
      FROM ${accountOptionTable(client, 'account_tag_bindings')} option_tag_bindings
      WHERE option_tag_bindings.system_account_id = ?
        AND option_tag_bindings.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
    params.push(viewerSystemAccountId, ...options.tagIds)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  if (statuses.length === 1) {
    clauses.push(`${authorizedAccountOptionEffectiveStatusSql(includeAuthorizationQuota)} = ?`)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(`${authorizedAccountOptionEffectiveStatusSql(includeAuthorizationQuota)} IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    clauses.push(authorizedAccountOptionAvailableSql())
  } else if (options.schedulable === 'disabled') {
    clauses.push(`(${authorizedAccountOptionBindingUnavailableSql()}
      OR ${authorizedAccountOptionEffectiveStatusSql(includeAuthorizationQuota)} IN ('disabled', 'error'))`)
  } else if (options.schedulable === 'cooling') {
    clauses.push(`${authorizedAccountOptionBindingAvailableSql()}
      AND ${authorizedAccountOptionAuthorizationAvailableSql()}
      AND NOT (${authorizedAccountOptionHardUnavailableSql('accounts')})
      AND NOT (${authorizedAccountOptionHardUnavailableSql('source_accounts')})
      AND (${authorizedAccountOptionCoolingSql('accounts')} OR ${authorizedAccountOptionCoolingSql('source_accounts')})`)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

async function loadAccountOptionSystemAccountNamesAsync(client: DatabaseClient, systemAccountIds: string[]): Promise<Map<string, string>> {
  const ids = [...new Set(systemAccountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = await client.query<{ id: string; username: string; display_name: string }>(`
    SELECT id, username, display_name
    FROM ${accountOptionTable(client, 'system_accounts')}
    WHERE id IN (${ids.map(() => '?').join(', ')})
  `, ids)
  return new Map(rows.map((row) => [row.id, row.display_name || row.username || row.id]))
}

function ownerAccountOptionEffectiveStatusSql(): string {
  return `CASE
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at::timestamptz <= now())
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until::timestamptz > now() THEN 'temporary_unavailable'
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    ELSE accounts.status
  END`
}

function ownerAccountOptionEffectiveSchedulableSql(): string {
  return `CASE
    WHEN accounts.status = 'active'
      AND accounts.schedulable = 1
      AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until::timestamptz <= now())
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at::timestamptz > now())
      AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
    THEN 1
    ELSE 0
  END`
}

function ownerAccountOptionCoolingSql(): string {
  return `CASE
    WHEN accounts.status IN ('rate_limited', 'temporary_unavailable')
      OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until::timestamptz > now())
    THEN 1
    ELSE 0
  END`
}

function authorizedAccountOptionEffectiveStatusSql(includeAuthorizationQuota: boolean): string {
  const quotaExpression = includeAuthorizationQuota ? authorizedOptionQuotaExceededExpression() : undefined
  return `CASE
    WHEN ${authorizedAccountOptionBindingUnavailableSql()} THEN 'disabled'
    WHEN ra.status <> 'active'
      OR (ra.expires_at IS NOT NULL AND ra.expires_at::timestamptz <= now())
    THEN 'disabled'
    ${quotaExpression ? `WHEN ${quotaExpression.sql} THEN 'rate_limited'` : ''}
    WHEN source_accounts.id IS NULL THEN 'disabled'
    WHEN source_accounts.last_error_code = 'account_expired'
      OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at::timestamptz <= now())
    THEN 'disabled'
    WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN source_accounts.status
    WHEN source_accounts.cooldown_until IS NOT NULL AND source_accounts.cooldown_until::timestamptz > now() THEN 'temporary_unavailable'
    WHEN source_accounts.schedulable <> 1 THEN 'disabled'
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at::timestamptz <= now())
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until::timestamptz > now() THEN 'temporary_unavailable'
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    ELSE accounts.status
  END`
}

function authorizedAccountOptionAvailableSql(): string {
  return `${authorizedAccountOptionBindingAvailableSql()}
    AND ${authorizedAccountOptionAuthorizationAvailableSql()}
    AND source_accounts.id IS NOT NULL
    AND source_accounts.status = 'active'
    AND source_accounts.schedulable = 1
    AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until::timestamptz <= now())
    AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at::timestamptz > now())
    AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
    AND accounts.status = 'active'
    AND accounts.schedulable = 1
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until::timestamptz <= now())
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at::timestamptz > now())
    AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')`
}

function authorizedAccountOptionBindingAvailableSql(): string {
  return `option_group_bindings.group_id IS NOT NULL
    AND option_group_bindings.account_authorization_id IS NOT NULL
    AND option_group_bindings.account_authorization_id = ra.id`
}

function authorizedAccountOptionBindingUnavailableSql(): string {
  return `option_group_bindings.group_id IS NULL
    OR option_group_bindings.account_authorization_id IS NULL
    OR option_group_bindings.account_authorization_id <> ra.id`
}

function authorizedAccountOptionAuthorizationAvailableSql(): string {
  return `ra.status = 'active'
    AND (ra.expires_at IS NULL OR ra.expires_at::timestamptz > now())`
}

function authorizedAccountOptionHardUnavailableSql(alias: 'accounts' | 'source_accounts'): string {
  return `${alias}.id IS NULL
    OR ${alias}.schedulable <> 1
    OR ${alias}.status IN ('pending_test', 'disabled', 'error', 'quality_isolated')
    OR ${alias}.last_error_code = 'account_expired'
    OR (${alias}.account_expires_at IS NOT NULL AND ${alias}.account_expires_at::timestamptz <= now())`
}

function authorizedAccountOptionCoolingSql(alias: 'accounts' | 'source_accounts'): string {
  return `${alias}.status IN ('rate_limited', 'temporary_unavailable')
    OR (${alias}.cooldown_until IS NOT NULL AND ${alias}.cooldown_until::timestamptz > now())`
}

function accountOptionTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function queryAccountOptionRowsForAccess(access: AccessScope | undefined, options: ReturnType<typeof normalizeAccountOptionListOptions>): AccountOptionRow[] {
  const database = getBusinessDatabase()
  if (accountOptionNeedsDerivedStatusFunctions(options)) {
    ensureAccountDerivedStatusSqlFunctions(database)
  }
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
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type,
        NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at,
        NULL AS authorization_limits_json, NULL AS authorization_effective_source_team_id,
        NULL AS authorization_resource_owner_system_account_id, NULL AS authorization_resource_id,
        ${emptyAccountOptionBindingColumns()},
        ${emptySourceAccountOptionColumns()}
      FROM accounts
      WHERE accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL${filters.clause}
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
      NULL AS authorization_limits_json, NULL AS authorization_effective_source_team_id,
      NULL AS authorization_resource_owner_system_account_id, NULL AS authorization_resource_id,
      ${emptyAccountOptionBindingColumns()},
      ${emptySourceAccountOptionColumns()}
    FROM accounts
    WHERE accounts.system_account_id = ?
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL${ownerFilters.clause}
    UNION ALL
    SELECT ${accountOptionSelectColumns()}, 'authorized' AS access_type,
      ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
      ra.limits_json AS authorization_limits_json,
      ra.effective_source_team_id AS authorization_effective_source_team_id,
      ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      ra.resource_id AS authorization_resource_id,
      option_group_bindings.group_id AS bound_group_id,
      option_group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      ${sourceAccountOptionColumns()}
    FROM accounts
    INNER JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN accounts source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
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

function emptyAccountOptionBindingColumns(): string {
  return `NULL AS bound_group_id,
      NULL AS bound_group_account_authorization_id`
}

function emptySourceAccountOptionColumns(): string {
  return `NULL AS source_status,
      NULL AS source_schedulable,
      NULL AS source_availability_schedule_json,
      NULL AS source_account_expires_at,
      NULL AS source_cooldown_until,
      NULL AS source_last_error_code`
}

function sourceAccountOptionColumns(): string {
  return `source_accounts.status AS source_status,
      source_accounts.schedulable AS source_schedulable,
      source_accounts.availability_schedule_json AS source_availability_schedule_json,
      source_accounts.account_expires_at AS source_account_expires_at,
      source_accounts.cooldown_until AS source_cooldown_until,
      source_accounts.last_error_code AS source_last_error_code`
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

function accountOptionNeedsDerivedStatusFunctions(options: ReturnType<typeof normalizeAccountOptionListOptions>): boolean {
  const statuses = accountStatusFilterValues(options.status)
  return statuses.length > 0
    || options.schedulable === 'disabled'
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
    'accounts.availability_schedule_json',
    'accounts.cooldown_until',
    'accounts.last_error_code',
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
    clauses.push(`(
      accounts.name >= ?
      AND accounts.name < ?
    )`)
    params.push(
      keyword,
      accountOptionNamePrefixUpperBound(keyword)
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
  const ownerStatusExpression = ownerOptionStatusExpression()
  const authorizedStatusExpression = `CASE
    WHEN ra.status <> 'active'
      OR (ra.expires_at IS NOT NULL AND ra.expires_at <= ${currentIsoSql})
    THEN 'disabled'
    ${quotaStatusExpression ? `WHEN ${quotaStatusExpression.sql} THEN 'rate_limited'` : ''}
    WHEN source_accounts.id IS NULL THEN 'disabled'
    WHEN source_accounts.last_error_code = 'account_expired'
      OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN source_accounts.status
    WHEN source_accounts.cooldown_until IS NOT NULL AND source_accounts.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN source_accounts.schedulable <> 1 THEN 'disabled'
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    WHEN ${authorizedOptionApiKeyPoolAllUnavailableExpression()} THEN 'temporary_unavailable'
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
  const authorizedSourceAccountAvailableExpression = `source_accounts.id IS NOT NULL
    AND source_accounts.status = 'active'
    AND source_accounts.schedulable = 1
    AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ${currentIsoSql})
    AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ${currentIsoSql})
    AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')`
  const authorizedAccountAvailableExpression = `accounts.schedulable = 1
    AND accounts.status = 'active'
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ${currentIsoSql})
    AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')`
  const authorizedAccountHardUnavailableExpression = `accounts.schedulable <> 1
    OR accounts.status IN ('pending_test', 'disabled', 'error', 'quality_isolated')
    OR accounts.last_error_code = 'account_expired'
    OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql})`
  const authorizedSourceAccountHardUnavailableExpression = `source_accounts.id IS NULL
    OR source_accounts.schedulable <> 1
    OR source_accounts.status IN ('pending_test', 'disabled', 'error', 'quality_isolated')
    OR source_accounts.last_error_code = 'account_expired'
    OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at <= ${currentIsoSql})`
  const authorizedAccountCoolingExpression = `accounts.status IN ('rate_limited', 'temporary_unavailable')
    OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql})`
  const authorizedSourceAccountCoolingExpression = `source_accounts.status IN ('rate_limited', 'temporary_unavailable')
    OR (source_accounts.cooldown_until IS NOT NULL AND source_accounts.cooldown_until > ${currentIsoSql})`
  if (statuses.length === 1) {
    clauses.push(`${authorizedView ? authorizedStatusExpression : ownerStatusExpression} = ?`)
    if (authorizedView && quotaStatusExpression) params.push(...quotaStatusExpression.params)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(`${authorizedView ? authorizedStatusExpression : ownerStatusExpression} IN (${statuses.map(() => '?').join(', ')})`)
    if (authorizedView && quotaStatusExpression) params.push(...quotaStatusExpression.params)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    if (authorizedView) {
      const quotaExpression = includeAuthorizationQuota ? authorizedOptionQuotaExceededExpression() : undefined
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND ${authorizedSourceAccountAvailableExpression}
        AND ${authorizedAccountAvailableExpression}
        ${quotaExpression ? `AND NOT (${quotaExpression.sql})` : ''}`)
      if (quotaExpression) params.push(...quotaExpression.params)
    } else {
      clauses.push(`accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ${currentIsoSql})
        AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')`)
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
      clauses.push(`(accounts.status = 'disabled'
        OR accounts.schedulable <> 1
        OR accounts.last_error_code = 'account_expired'
        OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql}))`)
    }
  } else if (options.schedulable === 'cooling') {
    if (authorizedView) {
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND NOT (${authorizedSourceAccountHardUnavailableExpression})
        AND NOT (${authorizedAccountHardUnavailableExpression})
        AND (${authorizedSourceAccountCoolingExpression} OR ${authorizedAccountCoolingExpression})`)
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

function ownerOptionStatusExpression(): string {
  return `CASE
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    WHEN ${ownerOptionApiKeyPoolAllUnavailableExpression()} THEN 'temporary_unavailable'
    ELSE accounts.status
  END`
}

function ownerOptionApiKeyPoolAllUnavailableExpression(): string {
  return accountApiKeyPoolAllUnavailableSql({
    accountIdSql: 'accounts.id',
    providerCodeSql: 'accounts.provider_code',
    protocolCodeSql: 'accounts.protocol_code',
    protocolVersionSql: 'accounts.protocol_version',
    typeSql: 'accounts.type'
  })
}

function authorizedOptionApiKeyPoolAllUnavailableExpression(): string {
  return accountApiKeyPoolAllUnavailableSql({
    accountIdSql: 'COALESCE(source_accounts.id, accounts.id)',
    providerCodeSql: 'COALESCE(source_accounts.provider_code, accounts.provider_code)',
    protocolCodeSql: 'COALESCE(source_accounts.protocol_code, accounts.protocol_code)',
    protocolVersionSql: 'COALESCE(source_accounts.protocol_version, accounts.protocol_version)',
    typeSql: 'COALESCE(source_accounts.type, accounts.type)'
  })
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

function accountOptionNamePrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\uffff`
}
