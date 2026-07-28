import { runtimeConfig } from '../config/runtime.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import type {
  AccountClientCompatibility,
  AccountGroupBindStatus,
  AccountHealthCheckEndpointMode,
  AccountListItem,
  AccountListPermissions,
  AccountType,
  ProviderCode,
  ResourceAccessType
} from '../domain/types.js'
import { canAccessAll, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { normalizeAccountNameSearchText, accountNameSearchQueryTerms } from './account-name-search.repository.js'
import { loadAccountTagsByAccountIdsAsync } from './account-tags.repository.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { normalizeAccountListOptions, accountStatusFilterValues, type AccountListOptions, type NormalizedAccountListOptions } from './account-list-options.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, takePageRows, textPrefixUpperBound } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export type AccountManagementListBaseItem = Pick<AccountListItem,
  | 'id'
  | 'configRevision'
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
  | 'providerCode'
  | 'providerName'
  | 'providerProtocolProfileId'
  | 'protocolCode'
  | 'protocolVersion'
  | 'name'
  | 'notes'
  | 'type'
  | 'concurrencyLimit'
  | 'priority'
  | 'superPriorityEnabled'
  | 'fallbackEnabled'
  | 'clientCompatibility'
  | 'tags'
  | 'healthCheckModel'
  | 'healthCheckEndpointMode'
  | 'proxyProfileId'
  | 'proxyProfileName'
  | 'proxyProfileType'
  | 'proxyProfileEnabled'
  | 'proxyProfileUnavailable'
  | 'proxyProfileErrorMessage'
  | 'availabilitySchedule'
  | 'accessType'
  | 'accountAuthorizationId'
  | 'boundGroupId'
  | 'boundGroupName'
  | 'groupBindStatus'
  | 'bindingSystemAccountId'
  | 'permissions'
>

export interface AccountManagementListPage {
  items: AccountManagementListBaseItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AccountManagementListResult {
  items: AccountListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  generatedAt: string
}

interface AccountManagementListRow {
  id: string
  config_revision: number | string
  system_account_id: string
  system_account_name: string | null
  owner_system_account_id: string
  owner_system_account_name: string | null
  provider_code: ProviderCode
  provider_name: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  notes: string | null
  type: AccountType
  concurrency_limit: number | string
  priority: number | string
  super_priority_enabled: number | string
  fallback_enabled: number | string
  client_compatibility: AccountClientCompatibility
  availability_schedule_json: string | null
  health_check_model: string
  health_check_endpoint_mode: AccountHealthCheckEndpointMode
  authorization_instance_owner_system_account_id: string | null
  configured_proxy_profile_id: string | null
  authorization_id: string | null
  authorization_effective_source_type: 'manual' | 'team' | null
  authorization_resource_owner_system_account_id: string | null
  source_provider_code: ProviderCode | null
  source_provider_protocol_profile_id: string | null
  source_protocol_code: string | null
  source_protocol_version: string | null
  source_type: AccountType | null
  source_proxy_profile_id: string | null
  source_concurrency_limit: number | string | null
  source_client_compatibility: AccountClientCompatibility | null
  binding_system_account_id: string | null
  bound_group_id: string | null
  bound_group_name: string | null
  bound_group_account_authorization_id: string | null
  bound_group_local_priority: number | string | null
  bound_group_local_super_priority_enabled: number | string | null
  bound_group_local_fallback_enabled: number | string | null
  resolved_proxy_profile_id: string | null
  proxy_profile_name: string | null
  proxy_profile_type: string | null
  proxy_profile_enabled: number | string | null
}

const proxyProfileUnavailableMessage = '代理不存在或已停用，请选择一个已启用的代理'
export const maxAccountManagementCandidatePrefixSize = 10_000

export async function listAccountManagementItemsPageAsync(
  access?: AccessScope,
  options?: AccountListOptions
): Promise<AccountManagementListPage> {
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_account_management_items_page_read_only',
      access,
      options
    })
  }
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : sqliteAccountManagementListClient()
  return listAccountManagementItemsPageDirect(client, access, options)
}

export async function listAccountManagementCandidatePrefixAsync(
  access: AccessScope | undefined,
  options: AccountListOptions,
  candidateLimit: number
): Promise<AccountManagementListPage> {
  const normalizedLimit = normalizedAccountManagementCandidateLimit(candidateLimit)
  if (runtimeConfig.databaseDriver === 'sqlite' && sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_account_management_items_page_read_only',
      access,
      options,
      candidateLimit: normalizedLimit
    })
  }
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : sqliteAccountManagementListClient()
  return listAccountManagementItemsPageDirect(client, access, options, normalizedLimit)
}

export async function listAccountManagementItemsPageReadOnly(
  access?: AccessScope,
  options?: AccountListOptions,
  candidateLimit?: number
): Promise<AccountManagementListPage> {
  return listAccountManagementItemsPageDirect(sqliteAccountManagementListClient(), access, options, candidateLimit)
}

async function listAccountManagementItemsPageDirect(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: AccountListOptions | undefined,
  candidateLimit?: number
): Promise<AccountManagementListPage> {
  const listOptions = normalizeAccountListOptions(options)
  const resultPage = candidateLimit === undefined ? listOptions.page : 1
  const resultPageSize = candidateLimit === undefined
    ? listOptions.pageSize
    : normalizedAccountManagementCandidateLimit(candidateLimit)
  const scopedAccountId = manageableSystemAccountId(access)
  if (!scopedAccountId && !canAccessAll(access)) {
    throw new Error('缺少系统账户上下文')
  }
  const filters = accountManagementListFilters(client, listOptions, scopedAccountId)
  const rows = await client.query<AccountManagementListRow>(`
    WITH account_rows AS (
      SELECT
        accounts.id,
        accounts.config_revision,
        accounts.system_account_id,
        accounts.provider_code,
        accounts.provider_protocol_profile_id,
        accounts.protocol_code,
        accounts.protocol_version,
        accounts.name,
        accounts.notes,
        accounts.type,
        accounts.status,
        accounts.concurrency_limit,
        accounts.priority,
        accounts.super_priority_enabled,
        accounts.fallback_enabled,
        accounts.client_compatibility,
        accounts.schedulable,
        accounts.availability_schedule_json,
        accounts.account_expires_at,
        accounts.cooldown_until,
        accounts.last_error_code,
        accounts.health_check_model,
        accounts.health_check_endpoint_mode,
        accounts.last_used_at,
        accounts.created_at,
        accounts.authorization_instance_owner_system_account_id,
        accounts.proxy_profile_id AS configured_proxy_profile_id,
        authorizations.id AS authorization_id,
        authorizations.status AS authorization_status,
        authorizations.expires_at AS authorization_expires_at,
        authorizations.effective_source_type AS authorization_effective_source_type,
        authorizations.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
        source_accounts.provider_code AS source_provider_code,
        source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id,
        source_accounts.protocol_code AS source_protocol_code,
        source_accounts.protocol_version AS source_protocol_version,
        source_accounts.type AS source_type,
        source_accounts.status AS source_status,
        source_accounts.schedulable AS source_schedulable,
        source_accounts.account_expires_at AS source_account_expires_at,
        source_accounts.cooldown_until AS source_cooldown_until,
        source_accounts.last_error_code AS source_last_error_code,
        source_accounts.proxy_profile_id AS source_proxy_profile_id,
        source_accounts.concurrency_limit AS source_concurrency_limit,
        source_accounts.client_compatibility AS source_client_compatibility
      FROM ${businessTable(client, 'accounts')} accounts
      LEFT JOIN ${businessTable(client, 'resource_authorizations')} authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ${businessTable(client, 'accounts')} source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      WHERE accounts.deleted_at IS NULL
        ${scopedAccountId ? 'AND accounts.system_account_id = ?' : ''}
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
    ), ranked_group_bindings AS (
      SELECT
        group_accounts.account_id,
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled,
        ROW_NUMBER() OVER (
          PARTITION BY group_accounts.account_id, group_accounts.system_account_id
          ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
        ) AS binding_rank
      FROM ${businessTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.enabled = 1
    )
    SELECT
      account_rows.id,
      account_rows.config_revision,
      account_rows.system_account_id,
      account_rows.provider_code,
      providers.name AS provider_name,
      account_rows.provider_protocol_profile_id,
      account_rows.protocol_code,
      account_rows.protocol_version,
      account_rows.name,
      account_rows.notes,
      account_rows.type,
      account_rows.concurrency_limit,
      account_rows.priority,
      account_rows.super_priority_enabled,
      account_rows.fallback_enabled,
      account_rows.client_compatibility,
      account_rows.availability_schedule_json,
      account_rows.health_check_model,
      account_rows.health_check_endpoint_mode,
      account_rows.authorization_instance_owner_system_account_id,
      account_rows.configured_proxy_profile_id,
      account_rows.authorization_id,
      account_rows.authorization_effective_source_type,
      account_rows.authorization_resource_owner_system_account_id,
      account_rows.source_provider_code,
      account_rows.source_provider_protocol_profile_id,
      account_rows.source_protocol_code,
      account_rows.source_protocol_version,
      account_rows.source_type,
      account_rows.source_proxy_profile_id,
      account_rows.source_concurrency_limit,
      account_rows.source_client_compatibility,
      COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id) AS system_account_name,
      COALESCE(
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      ) AS owner_system_account_id,
      COALESCE(
        owner_system_accounts.display_name,
        owner_system_accounts.username,
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      ) AS owner_system_account_name,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id,
      bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled,
      proxy_profiles.id AS resolved_proxy_profile_id,
      proxy_profiles.name AS proxy_profile_name,
      proxy_profiles.type AS proxy_profile_type,
      proxy_profiles.enabled AS proxy_profile_enabled
    FROM account_rows
    LEFT JOIN ranked_group_bindings group_bindings
      ON group_bindings.account_id = account_rows.id
      AND group_bindings.system_account_id = account_rows.system_account_id
      AND group_bindings.binding_rank = 1
    LEFT JOIN ${businessTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    LEFT JOIN ${businessTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = account_rows.system_account_id
    LEFT JOIN ${businessTable(client, 'system_accounts')} owner_system_accounts
      ON owner_system_accounts.id = COALESCE(
        account_rows.authorization_resource_owner_system_account_id,
        account_rows.authorization_instance_owner_system_account_id,
        account_rows.system_account_id
      )
    LEFT JOIN ${businessTable(client, 'proxy_profiles')} proxy_profiles
      ON proxy_profiles.id = COALESCE(account_rows.source_proxy_profile_id, account_rows.configured_proxy_profile_id)
    LEFT JOIN ${businessTable(client, 'providers')} providers
      ON providers.code = COALESCE(account_rows.source_provider_code, account_rows.provider_code)
    ${filters.clause}
    ${accountManagementListOrderClause(client, listOptions)}
    LIMIT ? OFFSET ?
  `, [
    ...(scopedAccountId ? [scopedAccountId] : []),
    ...filters.params,
    resultPageSize + 1,
    (resultPage - 1) * resultPageSize
  ])
  const pageRows = takePageRows(rows, resultPageSize)
  const tagsByAccount: Map<string, AccountManagementListBaseItem['tags']> = candidateLimit === undefined
    ? await loadAccountTagsByAccountIdsAsync(pageRows.rows.map((row) => row.id))
    : new Map()
  const items = pageRows.rows.map((row) => accountManagementListItemFromRow(
    row,
    includeSystemAccountFields(access),
    canAccessAll(access),
    (tagsByAccount.get(row.id) ?? []).map(({ id, name }) => ({ id, name }))
  ))
  return {
    items,
    total: pagedTotalUpperBound(resultPage, resultPageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: resultPage,
    pageSize: resultPageSize
  }
}

function accountManagementListItemFromRow(
  row: AccountManagementListRow,
  includeSystemAccount: boolean,
  includeProxyError: boolean,
  tags: AccountManagementListBaseItem['tags']
): AccountManagementListBaseItem {
  const accessType: ResourceAccessType = row.authorization_id ? 'authorized' : 'owner'
  const providerCode = accessType === 'authorized' && row.source_provider_code
    ? row.source_provider_code
    : row.provider_code
  const providerProtocolProfileId = accessType === 'authorized' && row.source_provider_protocol_profile_id
    ? row.source_provider_protocol_profile_id
    : row.provider_protocol_profile_id
  const protocolCode = accessType === 'authorized' && row.source_protocol_code
    ? row.source_protocol_code
    : row.protocol_code
  const protocolVersion = accessType === 'authorized' && row.source_protocol_version
    ? row.source_protocol_version
    : row.protocol_version
  const type = accessType === 'authorized' && row.source_type ? row.source_type : row.type
  const concurrencyLimit = numberValue(
    accessType === 'authorized' ? row.source_concurrency_limit ?? row.concurrency_limit : row.concurrency_limit
  )
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    providerCode,
    type,
    accessType === 'authorized' ? row.source_client_compatibility : row.client_compatibility,
    'openai_standard',
    { providerCode, protocolCode, protocolVersion, providerProtocolProfileId }
  )
  const groupBindStatus = accountManagementGroupBindStatus(row)
  const proxyProfileId = accessType === 'authorized'
    ? row.source_proxy_profile_id ?? undefined
    : row.configured_proxy_profile_id ?? undefined
  const proxyUnavailable = Boolean(proxyProfileId && (!row.resolved_proxy_profile_id || !booleanValue(row.proxy_profile_enabled)))
  return {
    id: row.id,
    configRevision: numberValue(row.config_revision, 1),
    ...(includeSystemAccount ? {
      systemAccountId: row.system_account_id,
      systemAccountName: row.system_account_name ?? undefined
    } : {}),
    ownerSystemAccountId: row.owner_system_account_id,
    ownerSystemAccountName: row.owner_system_account_name ?? undefined,
    providerCode,
    providerName: row.provider_name,
    providerProtocolProfileId,
    protocolCode,
    protocolVersion,
    name: row.name,
    notes: row.notes ?? undefined,
    type,
    concurrencyLimit,
    priority: accessType === 'authorized'
      ? numberValue(row.bound_group_local_priority ?? row.priority)
      : numberValue(row.priority),
    superPriorityEnabled: accessType === 'authorized'
      ? booleanValue(row.bound_group_local_super_priority_enabled)
      : booleanValue(row.super_priority_enabled),
    fallbackEnabled: accessType === 'authorized'
      ? booleanValue(row.bound_group_local_fallback_enabled)
      : booleanValue(row.fallback_enabled),
    clientCompatibility,
    tags,
    healthCheckModel: row.health_check_model.trim(),
    healthCheckEndpointMode: row.health_check_endpoint_mode,
    proxyProfileId,
    proxyProfileName: row.proxy_profile_name ?? undefined,
    proxyProfileType: proxyType(row.proxy_profile_type),
    proxyProfileEnabled: row.proxy_profile_enabled === null ? undefined : booleanValue(row.proxy_profile_enabled),
    proxyProfileUnavailable: proxyUnavailable || undefined,
    proxyProfileErrorMessage: proxyUnavailable && includeProxyError ? proxyProfileUnavailableMessage : undefined,
    availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
    accessType,
    accountAuthorizationId: row.authorization_id ?? undefined,
    boundGroupId: row.bound_group_id ?? undefined,
    boundGroupName: row.bound_group_name ?? undefined,
    groupBindStatus,
    bindingSystemAccountId: accessType === 'authorized' && row.bound_group_id ? row.binding_system_account_id ?? undefined : undefined,
    permissions: accountManagementPermissions(accessType, row.authorization_effective_source_type)
  }
}

function accountManagementListFilters(
  client: DatabaseClient,
  options: NormalizedAccountListOptions,
  scopedAccountId: string | undefined
): { clause: string; params: unknown[] } {
  const clauses: string[] = []
  const params: unknown[] = []
  if (options.ids.length) {
    clauses.push(`account_rows.id IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const normalized = normalizeAccountNameSearchText(keyword)
    const prefixName = client.driver === 'postgres' ? 'account_rows.name COLLATE "C"' : 'account_rows.name'
    const keywordClauses = [`(${prefixName} >= ? AND ${prefixName} < ?)`]
    const keywordParams: unknown[] = [normalized, textPrefixUpperBound(normalized)]
    const terms = accountNameSearchQueryTerms(keyword)
    if (terms.length) {
      const scopeClause = scopedAccountId ? 'search.system_account_id = ? AND' : ''
      const containsExpression = client.driver === 'postgres'
        ? 'position(? in documents.normalized_name) > 0'
        : 'instr(documents.normalized_name, ?) > 0'
      keywordClauses.push(`account_rows.id IN (
        SELECT search.account_id
        FROM ${businessTable(client, 'account_name_search_terms')} search
        INNER JOIN ${businessTable(client, 'account_name_search_documents')} documents
          ON documents.account_id = search.account_id
        WHERE ${scopeClause} search.term IN (${terms.map(() => '?').join(', ')})
          AND ${containsExpression}
        GROUP BY search.account_id
        HAVING COUNT(DISTINCT search.term) = ?
      )`)
      keywordParams.push(...(scopedAccountId ? [scopedAccountId] : []), ...terms, normalized, terms.length)
    }
    clauses.push(`(${keywordClauses.join(' OR ')})`)
    params.push(...keywordParams)
  }
  if (options.providerCode && options.providerCode !== 'all') {
    clauses.push('COALESCE(account_rows.source_provider_code, account_rows.provider_code) = ?')
    params.push(options.providerCode)
  }
  if (options.providerProtocolProfileId && options.providerProtocolProfileId !== 'all') {
    clauses.push('COALESCE(account_rows.source_provider_protocol_profile_id, account_rows.provider_protocol_profile_id) = ?')
    params.push(options.providerProtocolProfileId)
  }
  if (options.groupId) {
    clauses.push('group_bindings.group_id = ?')
    params.push(options.groupId)
  }
  if (options.tagIds.length) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM ${businessTable(client, 'account_tag_bindings')} tag_filter
      WHERE tag_filter.account_id = account_rows.id
        AND tag_filter.system_account_id = account_rows.system_account_id
        AND tag_filter.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
    params.push(...options.tagIds)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('COALESCE(account_rows.source_type, account_rows.type) = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  const effectiveStatus = statuses.length || options.schedulable !== 'all'
    ? accountManagementEffectiveStatusSql()
    : undefined
  if (statuses.length) {
    clauses.push(`${effectiveStatus} IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    clauses.push(`${effectiveStatus} = 'active'`)
  } else if (options.schedulable === 'disabled') {
    clauses.push(`${effectiveStatus} NOT IN ('active', 'rate_limited', 'temporary_unavailable')`)
  } else if (options.schedulable === 'cooling') {
    clauses.push(`${effectiveStatus} IN ('rate_limited', 'temporary_unavailable')`)
  }
  return { clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '', params }
}

function accountManagementListOrderClause(client: DatabaseClient, options: NormalizedAccountListOptions): string {
  const parts = options.sorts.flatMap((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    const column = accountManagementSortColumn(client, sort.field)
    return sort.field === 'lastUsedAt'
      ? [`CASE WHEN ${column} IS NULL THEN 1 ELSE 0 END ASC`, `${column} ${direction}`]
      : [`${column} ${direction}`]
  })
  return `ORDER BY ${[...parts, 'account_rows.created_at ASC', 'account_rows.id ASC'].join(', ')}`
}

function accountManagementSortColumn(
  client: DatabaseClient,
  field: NormalizedAccountListOptions['sorts'][number]['field']
): string {
  const textCollation = client.driver === 'postgres' ? ' COLLATE "C"' : ''
  if (field === 'priority') return "CASE WHEN account_rows.authorization_id IS NOT NULL THEN COALESCE(group_bindings.local_priority, account_rows.priority) ELSE account_rows.priority END"
  if (field === 'superPriority') return "CASE WHEN account_rows.authorization_id IS NOT NULL THEN COALESCE(group_bindings.local_super_priority_enabled, 0) ELSE account_rows.super_priority_enabled END"
  if (field === 'fallback') return "CASE WHEN account_rows.authorization_id IS NOT NULL THEN COALESCE(group_bindings.local_fallback_enabled, 0) ELSE account_rows.fallback_enabled END"
  if (field === 'name') return `account_rows.name${textCollation}`
  if (field === 'type') return `COALESCE(account_rows.source_type, account_rows.type)${textCollation}`
  if (field === 'providerCode') return `COALESCE(account_rows.source_provider_code, account_rows.provider_code)${textCollation}`
  if (field === 'systemAccount') return `COALESCE(system_accounts.display_name, system_accounts.username, account_rows.system_account_id)${textCollation}`
  if (field === 'concurrency') return 'COALESCE(account_rows.source_concurrency_limit, account_rows.concurrency_limit)'
  if (field === 'status') return accountManagementEffectiveStatusSql()
  if (field === 'accountExpiresAt') return 'COALESCE(account_rows.authorization_expires_at, account_rows.source_account_expires_at, account_rows.account_expires_at)'
  if (field === 'lastUsedAt') return 'account_rows.last_used_at'
  return 'account_rows.priority'
}

function accountManagementEffectiveStatusSql(): string {
  const current = `'${nowIso().replace(/'/g, "''")}'`
  return `CASE
    WHEN account_rows.authorization_id IS NOT NULL THEN
      CASE
        WHEN group_bindings.group_id IS NULL
          OR group_bindings.account_authorization_id IS NULL
          OR group_bindings.account_authorization_id <> account_rows.authorization_id
        THEN 'disabled'
        WHEN account_rows.authorization_status <> 'active'
          OR (account_rows.authorization_expires_at IS NOT NULL AND account_rows.authorization_expires_at <= ${current})
        THEN 'disabled'
        WHEN account_rows.source_status IS NULL THEN 'disabled'
        WHEN account_rows.source_last_error_code = 'account_expired'
          OR (account_rows.source_account_expires_at IS NOT NULL AND account_rows.source_account_expires_at <= ${current})
        THEN 'disabled'
        WHEN account_rows.source_status <> 'active' THEN account_rows.source_status
        WHEN account_rows.source_cooldown_until IS NOT NULL AND account_rows.source_cooldown_until > ${current} THEN 'temporary_unavailable'
        WHEN COALESCE(account_rows.source_schedulable, 0) <> 1 THEN 'disabled'
        WHEN account_rows.last_error_code = 'account_expired'
          OR (account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= ${current})
        THEN 'disabled'
        WHEN account_rows.status <> 'active' THEN account_rows.status
        WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${current} THEN 'temporary_unavailable'
        WHEN account_rows.schedulable <> 1 THEN 'disabled'
        ELSE account_rows.status
      END
    ELSE
      CASE
        WHEN account_rows.last_error_code = 'account_expired'
          OR (account_rows.account_expires_at IS NOT NULL AND account_rows.account_expires_at <= ${current})
        THEN 'disabled'
        WHEN account_rows.status <> 'active' THEN account_rows.status
        WHEN account_rows.cooldown_until IS NOT NULL AND account_rows.cooldown_until > ${current} THEN 'temporary_unavailable'
        WHEN account_rows.schedulable <> 1 THEN 'disabled'
        ELSE account_rows.status
      END
  END`
}

function accountManagementGroupBindStatus(row: AccountManagementListRow): AccountGroupBindStatus | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== row.system_account_id) return undefined
  if (row.bound_group_account_authorization_id !== row.authorization_id) {
    return 'authorization_unavailable'
  }
  return 'bound'
}

function accountManagementPermissions(
  accessType: ResourceAccessType,
  sourceType: AccountManagementListRow['authorization_effective_source_type']
): AccountListPermissions {
  if (accessType === 'owner') {
    return {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canReturnAuthorization: false,
      canAuthorize: true,
      canViewCredentials: true
    }
  }
  return {
    canUse: true,
    canEdit: false,
    canDelete: false,
    canReturnAuthorization: sourceType === 'manual',
    canAuthorize: false,
    canViewCredentials: false
  }
}

function sqliteAccountManagementListClient(): DatabaseClient {
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function normalizedAccountManagementCandidateLimit(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > maxAccountManagementCandidatePrefixSize) {
    throw new Error(`账户运行态筛选候选上限必须在 1-${maxAccountManagementCandidatePrefixSize} 之间`)
  }
  return value
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function numberValue(value: number | string | null | undefined, fallback = 0): number {
  const output = Number(value)
  return Number.isFinite(output) ? output : fallback
}

function booleanValue(value: number | string | null | undefined): boolean {
  return numberValue(value) === 1
}

function proxyType(value: string | null): AccountManagementListBaseItem['proxyProfileType'] {
  return value === 'http' || value === 'https' || value === 'socks5' || value === 'socks5h'
    ? value
    : undefined
}
