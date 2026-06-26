import type { AccountClientCompatibility, AccountGroupBindStatus, AccountSummary } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { isGptVendorCode } from '../domain/provider-protocol.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { runtimeConfig } from '../config/runtime.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import { canAccessAll, manageableSystemAccountId, userVisibleSystemAccountId, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsWithRuntimeState, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions, type NormalizedAccountListOptions } from './account-list-options.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { authorizationRuntimeBlockingStatus, disableExpiredAccounts } from './account-runtime-status.js'
import { accountNameSearchQueryTerms, escapeAccountNameSearchLike, normalizeAccountNameSearchText } from './account-name-search.repository.js'
import { loadAccountTagsByAccountIds, loadAccountTagsByAccountIdsAsync } from './account-tags.repository.js'
import { loadModelMappingsByAccountIdsAsync } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIdsAsync } from './account-supported-models.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { accountSystemAccountId, activeAccountAuthorization, activeResourceAuthorizationById, canManageResourceOwner, sanitizeAuthorizationSourcesForViewer, usageScope } from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountListRow, ResourceAuthorizationRow } from './repository-row-types.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes } from './usage-summary-loaders.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from '../modules/gateway/quota/request-quota-checker.js'
import { optionalString } from './value-utils.js'
import { loadAccountApiKeyRuntimeDetailsByAccountIds, loadAccountApiKeyRuntimeSummariesByAccountIds } from './account-api-key-runtime-state.repository.js'

export interface AccountListResult {
  items: AccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface AccountSummaryBuildOptions {
  includeCredentials?: boolean
}

const businessSchemaName = 'juhe_business'

export function listAccounts(access?: AccessScope, options?: AccountListOptions): AccountSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const rows = hydrateAccountRowsWithRuntimeState(listAccountRowsForAccess(access, listOptions), { includeCredentials: true })
  return accountSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function listAccountsPage(access?: AccessScope, options?: AccountListOptions): AccountListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const databasePage = listAccountRowsPageForAccess(access, listOptions, { includeCredentials: false })
  const page = {
    rows: hydrateAccountRowsWithRuntimeState(databasePage.rows, { includeCredentials: false }),
    total: databasePage.total
  }
  const rows = page.rows
  return {
    items: accountSummariesFromRows(rows, access, viewerSystemAccountId, { includeCredentials: false }),
    total: page.total,
    hasMore: page.total > listOptions.page * listOptions.pageSize,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export async function listAccountsPageAsync(access?: AccessScope, options?: AccountListOptions): Promise<AccountListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountsPage(access, options)
  }
  const listOptions = normalizeAccountListOptions(options)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const databasePage = await listOwnerAccountRowsPageAsync(client, access, listOptions)
  const pageRows = takePageRows(databasePage.rows, listOptions.pageSize)
  const items = await ownerAccountSummariesFromRowsAsync(client, pageRows.rows, access, { includeCredentials: false })
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function findAccountSummary(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions({ page: 1, pageSize: 1 })
  const row = findAccountRowForAccess(access, accountId, listOptions)
  if (!row) return undefined
  const hydratedRows = hydrateAccountRowsWithRuntimeState([row], { includeCredentials: true })
  return accountSummariesFromRows(hydratedRows, access, viewerSystemAccountId)[0]
}

export async function findAccountSummaryAsync(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAccountSummary(accountId, access)
  }
  const id = accountId.trim()
  if (!id) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const ownerRow = await findOwnerAccountRowByIdAsync(client, id)
  if (ownerRow) {
    if (!canManageResourceOwner(ownerRow.system_account_id, access)) {
      return undefined
    }
    return (await ownerAccountSummariesFromRowsAsync(client, [ownerRow], access))[0]
  }

  const authorizedRow = await findAuthorizedAccountRowByIdAsync(client, id, access)
  if (!authorizedRow) {
    return undefined
  }
  return authorizedAccountSummaryFromRowAsync(client, authorizedRow, access)
}

async function findAuthorizedAccountRowByIdAsync(
  client: DatabaseClient,
  accountId: string,
  access: AccessScope | undefined
): Promise<AccountListRow | undefined> {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (!viewerSystemAccountId && !canAccessAll(access)) {
    return undefined
  }
  const systemAccountClause = viewerSystemAccountId ? 'AND accounts.system_account_id = ?' : ''
  const params = viewerSystemAccountId ? [accountId, viewerSystemAccountId] : [accountId]
  return await client.one<AccountListRow>(`
    SELECT
      accounts.*,
      'authorized' AS access_type,
      authorizations.id AS authorization_id,
      authorizations.status AS authorization_status,
      authorizations.expires_at AS authorization_expires_at,
      authorizations.limits_json AS authorization_limits_json,
      authorizations.effective_source_type AS authorization_effective_source_type,
      authorizations.effective_source_team_id AS authorization_effective_source_team_id,
      authorizations.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      authorizations.resource_id AS authorization_resource_id,
      source_accounts.provider_code AS source_provider_code,
      source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id,
      source_accounts.protocol_code AS source_protocol_code,
      source_accounts.protocol_version AS source_protocol_version,
      source_accounts.type AS source_type,
      source_accounts.status AS source_status,
      source_accounts.schedulable AS source_schedulable,
      source_accounts.availability_schedule_json AS source_availability_schedule_json,
      source_accounts.availability_schedule_active AS source_availability_schedule_active,
      source_accounts.account_expires_at AS source_account_expires_at,
      source_accounts.cooldown_until AS source_cooldown_until,
      source_accounts.last_error_code AS source_last_error_code,
      source_accounts.last_error_message AS source_last_error_message,
      source_accounts.credential_mask AS source_credential_mask,
      source_accounts.credentials_encrypted AS source_credentials_encrypted,
      source_accounts.proxy_profile_id AS source_proxy_profile_id,
      source_accounts.concurrency_limit AS source_concurrency_limit,
      source_accounts.client_compatibility AS source_client_compatibility,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id,
      bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled
    FROM ${accountSummaryTable(client, 'accounts')} accounts
    INNER JOIN ${accountSummaryTable(client, 'resource_authorizations')} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${accountSummaryTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled,
        group_accounts.updated_at,
        group_accounts.account_id
      FROM ${accountSummaryTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = accounts.system_account_id
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON TRUE
    LEFT JOIN ${accountSummaryTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    WHERE accounts.id = ?
      ${systemAccountClause}
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NOT NULL
      AND authorizations.status IN ('active', 'paused', 'expired')
    LIMIT 1
  `, params)
}

async function authorizedAccountSummaryFromRowAsync(
  client: DatabaseClient,
  row: AccountListRow,
  access: AccessScope | undefined
): Promise<AccountSummary> {
  const factAccountId = accountResourceFactAccountId(row)
  const includeAccountNames = includeSystemAccountFields(access)
  const displayOwnerSystemAccountId = row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
  const [supportedModelsByAccount, modelMappingsByAccount, tagsByAccount, accountNames] = await Promise.all([
    factAccountId ? loadSupportedModelsByAccountIdsAsync([factAccountId]) : Promise.resolve(new Map<string, string[]>()),
    factAccountId ? loadModelMappingsByAccountIdsAsync([factAccountId]) : Promise.resolve(new Map()),
    loadAccountTagsByAccountIdsAsync([row.id]),
    loadAccountSummarySystemAccountNamesAsync(client, [
      row.system_account_id,
      displayOwnerSystemAccountId
    ])
  ])
  row.supported_models = factAccountId ? supportedModelsByAccount.get(factAccountId) ?? [] : []
  row.model_mappings = factAccountId ? modelMappingsByAccount.get(factAccountId) ?? [] : []

  const groupBinding = accountGroupBindingFromRow(row, row.system_account_id)
  const currentNow = nowIso()
  const effectiveAuthorizedStatus = authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
  const effectiveAuthorizedSchedulable = Boolean(groupBinding && groupBinding.groupBindStatus === 'bound')
    && authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) === undefined
    && Boolean(factAccountId)
    && isAuthorizedSourceAccountAvailableForDispatch(row, currentNow)
    && row.status === 'active'
    && row.schedulable === 1
    && row.availability_schedule_active !== 0
    && !isLaterIso(row.cooldown_until ?? undefined, currentNow)
  const resourceProviderCode = accountResourceProviderCode(row)
  const resourceType = accountResourceType(row)
  const dispatchPriority = Number(row.bound_group_local_priority ?? row.priority ?? 0)
  const dispatchSuperPriorityEnabled = row.bound_group_local_super_priority_enabled === 1
  const dispatchFallbackEnabled = row.bound_group_local_fallback_enabled === 1
  const sourceAvailabilityScheduleActive = row.source_availability_schedule_active !== 0

  return accountSummaryWithEffectiveAvailability({
    id: row.id,
    systemAccountId: includeAccountNames ? row.system_account_id : undefined,
    systemAccountName: includeAccountNames ? accountNames.get(row.system_account_id) : undefined,
    ownerSystemAccountId: displayOwnerSystemAccountId,
    ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
    providerCode: resourceProviderCode,
    providerProtocolProfileId: accountResourceProviderProtocolProfileId(row),
    protocolCode: accountResourceProtocolCode(row),
    protocolVersion: accountResourceProtocolVersion(row),
    name: row.name,
    type: resourceType,
    credentials: accountCredentialsForList(row, true),
    status: effectiveAuthorizedStatus,
    concurrencyLimit: accountResourceConcurrencyLimit(row),
    currentConcurrency: loadAccountCurrentConcurrencyByIds([row.id]).get(row.id) ?? 0,
    priority: dispatchPriority,
    superPriorityEnabled: dispatchSuperPriorityEnabled,
    fallbackEnabled: dispatchFallbackEnabled,
    clientCompatibility: accountResourceClientCompatibility(row),
    supportedModels: [...(row.supported_models ?? [])],
    modelMappings: [...(row.model_mappings ?? [])],
    tags: tagsByAccount.get(row.id) ?? [],
    lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
    proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
    schedulable: effectiveAuthorizedSchedulable,
    availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
    availabilityScheduleActive: row.availability_schedule_active !== 0,
    accountExpiresAt: row.account_expires_at ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    cooldownRetestFailureCount: 0,
    healthCheckEnabled: row.health_check_enabled === 1,
    lastHealthCheckAt: row.last_health_check_at ?? undefined,
    nextHealthCheckAt: row.next_health_check_at ?? undefined,
    lastHealthSuccessAt: row.last_health_success_at ?? undefined,
    healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)),
    lastHealthCheckStatusCode: optionalNumber(row.last_health_check_status_code),
    lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
    lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
    streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
    streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    accessType: 'authorized',
    accountAuthorizationId: row.authorization_id ?? undefined,
    authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
    authorizationInstanceOwnerSystemAccountId: row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined,
    authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
    authorizationInstanceSourceAccountSchedulable: typeof row.source_schedulable === 'number' ? row.source_schedulable === 1 : undefined,
    authorizationInstanceSourceAccountAvailabilitySchedule: parseAccountAvailabilityScheduleJson(row.source_availability_schedule_json),
    authorizationInstanceSourceAccountScheduleActive: sourceAvailabilityScheduleActive,
    authorizationInstanceSourceAccountExpiresAt: row.source_account_expires_at ?? undefined,
    authorizationInstanceSourceAccountCooldownUntil: row.source_cooldown_until ?? undefined,
    authorizationInstanceSourceAccountLastErrorCode: row.source_last_error_code ?? undefined,
    authorizationInstanceSourceAccountLastErrorMessage: row.source_last_error_message ?? undefined,
    boundGroupId: groupBinding?.groupId,
    boundGroupName: groupBinding?.groupName,
    groupBindStatus: groupBinding?.groupBindStatus,
    bindingSystemAccountId: groupBinding ? row.system_account_id : undefined,
    authorizationStatus: row.authorization_status ?? undefined,
    authorizationExpiresAt: row.authorization_expires_at ?? undefined,
    authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
    authorizationQuotaExceeded: false,
    permissions: authorizedAccountPermissions(false),
    authorizationUsageAvailable: false,
    authorizationCount: 0,
    authorizationTeamCount: 0
  })
}

async function findOwnerAccountRowByIdAsync(client: DatabaseClient, accountId: string): Promise<AccountListRow | undefined> {
  return await client.one<AccountListRow>(`
    SELECT
      accounts.*,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id,
      bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled
    FROM ${accountSummaryTable(client, 'accounts')} accounts
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled,
        group_accounts.updated_at,
        group_accounts.account_id
      FROM ${accountSummaryTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = accounts.system_account_id
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON TRUE
    LEFT JOIN ${accountSummaryTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
    LIMIT 1
  `, [accountId])
}

async function loadAccountSummarySystemAccountNamesAsync(client: DatabaseClient, systemAccountIds: string[]): Promise<Map<string, string>> {
  const ids = [...new Set(systemAccountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = await client.query<{ id: string; username: string; display_name: string }>(`
    SELECT id, username, display_name
    FROM ${accountSummaryTable(client, 'system_accounts')}
    WHERE id IN (${ids.map(() => '?').join(', ')})
  `, ids)
  return new Map(rows.map((row) => [row.id, row.display_name || row.username || row.id]))
}

async function listOwnerAccountRowsPageAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: NormalizedAccountListOptions
): Promise<{ rows: AccountListRow[] }> {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && !canAccessAll(access)) {
    throw new Error('缺少系统账户上下文')
  }
  const filters = ownerAccountListFilters(client, access, options, ownerSystemAccountId)
  const orderClause = ownerAccountListOrderClause(options)
  const rows = await client.query<AccountListRow>(`
    SELECT
      accounts.*,
      'owner' AS access_type,
      group_bindings.system_account_id AS binding_system_account_id,
      group_bindings.group_id AS bound_group_id,
      bound_groups.name AS bound_group_name,
      group_bindings.account_authorization_id AS bound_group_account_authorization_id,
      group_bindings.local_priority AS bound_group_local_priority,
      group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled,
      group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled,
      COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_sort_name
    FROM ${accountSummaryTable(client, 'accounts')} accounts
    LEFT JOIN LATERAL (
      SELECT
        group_accounts.system_account_id,
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        group_accounts.local_priority,
        group_accounts.local_super_priority_enabled,
        group_accounts.local_fallback_enabled,
        group_accounts.updated_at,
        group_accounts.account_id
      FROM ${accountSummaryTable(client, 'group_accounts')} group_accounts
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = accounts.system_account_id
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    ) group_bindings ON TRUE
    LEFT JOIN ${accountSummaryTable(client, 'groups')} bound_groups
      ON bound_groups.id = group_bindings.group_id
    LEFT JOIN ${accountSummaryTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = accounts.system_account_id
    ${filters.clause}
    ${orderClause}
    LIMIT ? OFFSET ?
  `, [
    ...filters.params,
    options.pageSize + 1,
    (options.page - 1) * options.pageSize
  ])
  return { rows }
}

async function ownerAccountSummariesFromRowsAsync(
  client: DatabaseClient,
  rows: AccountListRow[],
  access: AccessScope | undefined,
  options: AccountSummaryBuildOptions = {}
): Promise<AccountSummary[]> {
  if (!rows.length) return []
  const includeCredentials = options.includeCredentials ?? true
  const accountIds = rows.map((row) => row.id)
  const includeAccountNames = includeSystemAccountFields(access)
  const [supportedModelsByAccount, modelMappingsByAccount, tagsByAccount, accountNames] = await Promise.all([
    loadSupportedModelsByAccountIdsAsync(accountIds),
    loadModelMappingsByAccountIdsAsync(accountIds),
    loadAccountTagsByAccountIdsAsync(accountIds),
    loadAccountSummarySystemAccountNamesAsync(client, includeAccountNames ? rows.map((row) => row.system_account_id) : [])
  ])

  return rows.map((row) => {
    row.supported_models = supportedModelsByAccount.get(row.id) ?? []
    row.model_mappings = modelMappingsByAccount.get(row.id) ?? []
    const groupBinding = accountGroupBindingFromRow(row, row.system_account_id)
    const ownerSystemAccountName = accountNames.get(row.system_account_id)
    return accountSummaryWithEffectiveAvailability({
      id: row.id,
      systemAccountId: includeAccountNames ? row.system_account_id : undefined,
      systemAccountName: includeAccountNames ? ownerSystemAccountName : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName,
      providerCode: row.provider_code,
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      name: row.name,
      notes: row.notes ?? undefined,
      type: row.type,
      credentials: accountCredentialsForList(row, includeCredentials),
      status: row.status,
      concurrencyLimit: Number(row.concurrency_limit),
      currentConcurrency: 0,
      priority: Number(row.priority ?? 0),
      superPriorityEnabled: row.super_priority_enabled === 1,
      fallbackEnabled: row.fallback_enabled === 1,
      clientCompatibility: accountResourceClientCompatibility(row),
      supportedModels: [...(row.supported_models ?? [])],
      modelMappings: [...(row.model_mappings ?? [])],
      tags: tagsByAccount.get(row.id) ?? [],
      lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
      proxyProfileId: row.proxy_profile_id ?? undefined,
      schedulable: row.schedulable === 1,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
      availabilityScheduleActive: row.availability_schedule_active !== 0,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
      healthCheckEnabled: row.health_check_enabled === 1,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)),
      lastHealthCheckStatusCode: optionalNumber(row.last_health_check_status_code),
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
      lastUsedAt: row.last_used_at ?? undefined,
      todayUsage: emptyAccountUsageSummary(),
      usage: emptyAccountUsageSummary(),
      accessType: 'owner',
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      permissions: ownerPermissions(),
      authorizationUsageAvailable: false,
      authorizationCount: 0,
      authorizationTeamCount: 0
    })
  })
}

function ownerAccountListFilters(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: NormalizedAccountListOptions,
  ownerSystemAccountId: string | undefined
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
    const keywordPrefix = `${escapeAccountNameSearchLike(keyword)}%`
    const keywordClauses = [
      'lower(accounts.name) = lower(?)',
      "lower(accounts.name) LIKE lower(?) ESCAPE '\\'"
    ]
    const keywordParams: unknown[] = [keyword, keywordPrefix]
    const containsSubquery = ownerAccountNameContainsSubquery(client, keyword, ownerSystemAccountId ?? undefined)
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
  if (options.groupId) {
    clauses.push('group_bindings.group_id = ?')
    params.push(options.groupId)
  }
  if (options.tagIds.length) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM ${accountSummaryTable(client, 'account_tag_bindings')} tag_filter
      WHERE tag_filter.account_id = accounts.id
        AND tag_filter.system_account_id = accounts.system_account_id
        AND tag_filter.tag_id IN (${options.tagIds.map(() => '?').join(', ')})
    )`)
    params.push(...options.tagIds)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  if (statuses.length === 1) {
    clauses.push(`${ownerAccountEffectiveStatusSql()} = ?`)
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(`${ownerAccountEffectiveStatusSql()} IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    clauses.push(`${ownerAccountEffectiveSchedulableSql()} = 1`)
  } else if (options.schedulable === 'disabled') {
    clauses.push(`(${ownerAccountEffectiveSchedulableSql()} = 0 AND ${ownerAccountCoolingSql()} = 0)`)
  } else if (options.schedulable === 'cooling') {
    clauses.push(`${ownerAccountCoolingSql()} = 1`)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function ownerAccountNameContainsSubquery(
  client: DatabaseClient,
  keyword: string,
  ownerSystemAccountId?: string
): { sql: string; params: unknown[] } | undefined {
  const terms = accountNameSearchQueryTerms(keyword)
  if (!terms.length) return undefined
  const systemAccountClause = ownerSystemAccountId ? 'AND search.system_account_id = ?' : ''
  const keywordContains = `%${escapeAccountNameSearchLike(normalizeAccountNameSearchText(keyword))}%`
  const params: unknown[] = ownerSystemAccountId
    ? [...terms, ownerSystemAccountId, keywordContains, terms.length]
    : [...terms, keywordContains, terms.length]
  return {
    sql: `
      SELECT search.account_id
      FROM ${accountSummaryTable(client, 'account_name_search_terms')} search
      INNER JOIN ${accountSummaryTable(client, 'account_name_search_documents')} documents
        ON documents.account_id = search.account_id
      WHERE search.term IN (${terms.map(() => '?').join(', ')})
        ${systemAccountClause}
        AND documents.normalized_name LIKE ? ESCAPE '\\'
      GROUP BY search.account_id
      HAVING COUNT(DISTINCT search.term) = ?
    `,
    params
  }
}

function ownerAccountListOrderClause(options: NormalizedAccountListOptions): string {
  const orderParts = options.sorts.map((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    const column = ownerAccountListSortColumn(sort.field)
    if (sort.field === 'qualityScore') {
      return `${column} ${direction} NULLS LAST`
    }
    return `${column} ${direction}`
  })
  return `ORDER BY ${[...orderParts, 'accounts.created_at ASC', 'accounts.id ASC'].join(', ')}`
}

function ownerAccountListSortColumn(field: NormalizedAccountListOptions['sorts'][number]['field']): string {
  if (field === 'priority') return 'accounts.priority'
  if (field === 'superPriority') return 'accounts.super_priority_enabled'
  if (field === 'fallback') return 'accounts.fallback_enabled'
  if (field === 'qualityScore') return 'NULL'
  if (field === 'name') return 'lower(accounts.name)'
  if (field === 'type') return 'lower(accounts.type)'
  if (field === 'providerCode') return 'lower(accounts.provider_code)'
  if (field === 'systemAccount') return 'lower(COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id))'
  if (field === 'concurrency') return 'accounts.concurrency_limit'
  if (field === 'status') return ownerAccountEffectiveStatusSql()
  if (field === 'accountExpiresAt') return 'accounts.account_expires_at'
  if (field === 'lastUsedAt') return 'accounts.last_used_at'
  return 'accounts.priority'
}

function ownerAccountEffectiveStatusSql(): string {
  return `CASE
    WHEN accounts.last_error_code = 'account_expired'
      OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at::timestamptz <= now())
    THEN 'disabled'
    WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN accounts.status
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until::timestamptz > now() THEN 'temporary_unavailable'
    WHEN COALESCE(accounts.availability_schedule_active, 1) = 0 THEN 'disabled'
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    ELSE accounts.status
  END`
}

function ownerAccountEffectiveSchedulableSql(): string {
  return `CASE
    WHEN accounts.status = 'active'
      AND accounts.schedulable = 1
      AND COALESCE(accounts.availability_schedule_active, 1) <> 0
      AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until::timestamptz <= now())
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at::timestamptz > now())
      AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
    THEN 1
    ELSE 0
  END`
}

function ownerAccountCoolingSql(): string {
  return `CASE
    WHEN accounts.status IN ('rate_limited', 'temporary_unavailable')
      OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until::timestamptz > now())
    THEN 1
    ELSE 0
  END`
}

function accountSummariesFromRows(
  rows: AccountListRow[],
  access: AccessScope | undefined,
  viewerSystemAccountId: string | undefined,
  options: AccountSummaryBuildOptions = {}
): AccountSummary[] {
  const includeCredentials = options.includeCredentials ?? true
  const timezone = usageStatsTimezone()
  const accountIds = rows.map((row) => row.id)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)
  const tagsByAccount = loadAccountTagsByAccountIds(accountIds)
  const accountUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const usageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes)
  const todayUsageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes, todayDateKey(timezone))
  const authorizationStatsByAccount = loadResourceAuthorizationStatsByResourceIds('account', accountIds)
  const authorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const usageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes)
  const todayUsageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes, todayDateKey(timezone))
  const quotaExceededByAuthorization = loadAuthorizationQuotaExceededByAuthorizationId(rows)
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => accountResourceFactAccountId(row)))
  const apiKeyRuntimeByAccount = loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds)
  const ownerAccountIds = includeCredentials
    ? rows.filter((row) => (row.access_type ?? 'owner') !== 'authorized').map((row) => row.id)
    : []
  const apiKeyRuntimeDetailsByAccount = loadAccountApiKeyRuntimeDetailsByAccountIds(ownerAccountIds)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows
    ? loadSystemAccountNameMapByIds(rows.flatMap((row) => [
        row.system_account_id,
        row.authorization_resource_owner_system_account_id ?? '',
        row.authorization_instance_owner_system_account_id ?? ''
      ]))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const usage = isAuthorizedView && row.authorization_id
      ? usageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : usageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const authorizationStats = authorizationStatsByAccount.get(row.id) ?? { authorizationCount: 0, authorizationTeamCount: 0 }
    const groupBindingSystemAccountId = row.system_account_id
    const groupBinding = groupBindingSystemAccountId
      ? accountGroupBindingFromRow(row, groupBindingSystemAccountId) ?? accountGroupBinding(row.id, groupBindingSystemAccountId)
      : undefined
    const currentNow = nowIso()
    const effectiveAuthorizedStatus = isAuthorizedView
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    const effectiveAuthorizedSchedulable = isAuthorizedView
      ? Boolean(groupBinding && groupBinding.groupBindStatus === 'bound')
        && authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) === undefined
        && Boolean(accountResourceFactAccountId(row))
        && isAuthorizedSourceAccountAvailableForDispatch(row, currentNow)
        && row.status === 'active'
        && row.schedulable === 1
        && row.availability_schedule_active !== 0
        && !isLaterIso(row.cooldown_until ?? undefined, currentNow)
      : row.schedulable === 1
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    const resourceProviderCode = accountResourceProviderCode(row)
    const resourceType = accountResourceType(row)
    const dispatchPriority = isAuthorizedView ? Number(row.bound_group_local_priority ?? row.priority ?? 0) : row.priority
    const dispatchSuperPriorityEnabled = isAuthorizedView ? row.bound_group_local_super_priority_enabled === 1 : row.super_priority_enabled === 1
    const dispatchFallbackEnabled = isAuthorizedView ? row.bound_group_local_fallback_enabled === 1 : row.fallback_enabled === 1
    const clientCompatibility = accountResourceClientCompatibility(row)
    const availabilitySchedule = parseAccountAvailabilityScheduleJson(row.availability_schedule_json)
    const availabilityScheduleActive = row.availability_schedule_active !== 0
    const sourceAvailabilityScheduleActive = isAuthorizedView
      ? row.source_availability_schedule_active !== 0
      : undefined
    const authorizationSources = row.authorization_id ? sourcesByAuthorization.get(row.authorization_id) ?? [] : []
    const authorizationQuotaExceeded = row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined
    return accountSummaryWithEffectiveAvailability({
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: displayOwnerSystemAccountId,
      ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
      providerCode: resourceProviderCode,
      providerProtocolProfileId: accountResourceProviderProtocolProfileId(row),
      protocolCode: accountResourceProtocolCode(row),
      protocolVersion: accountResourceProtocolVersion(row),
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: resourceType,
      credentials: accountCredentialsForList(row, includeCredentials),
      status: effectiveAuthorizedStatus,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: dispatchPriority,
      superPriorityEnabled: dispatchSuperPriorityEnabled,
      fallbackEnabled: dispatchFallbackEnabled,
      clientCompatibility,
      supportedModels: [...(row.supported_models ?? [])],
      modelMappings: [...(row.model_mappings ?? [])],
      tags: tagsByAccount.get(row.id) ?? [],
      lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
      qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
      qualityRecentAvgFirstTokenMs: typeof row.quality_recent_avg_first_token_ms === 'number' ? row.quality_recent_avg_first_token_ms : undefined,
      qualityRecentRequestCount: typeof row.quality_recent_request_count === 'number' ? row.quality_recent_request_count : undefined,
      qualityRecentErrorCount: typeof row.quality_recent_error_count === 'number' ? row.quality_recent_error_count : undefined,
      qualityRecentSuccessRate: typeof row.quality_recent_success_rate === 'number' ? row.quality_recent_success_rate : undefined,
      qualityLastErrorAt: row.quality_last_error_at ?? undefined,
      qualityLastErrorMessage: row.quality_last_error_message ?? undefined,
      qualityUpdatedAt: row.quality_updated_at ?? undefined,
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
      schedulable: isAuthorizedView ? effectiveAuthorizedSchedulable && authorizationQuotaExceeded !== true : effectiveAuthorizedSchedulable,
      availabilitySchedule,
      availabilityScheduleActive,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: isAuthorizedView ? undefined : row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: isAuthorizedView ? 0 : Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: isAuthorizedView ? undefined : row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: isAuthorizedView ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: isAuthorizedView ? undefined : optionalNumber(row.cooldown_retest_last_status_code),
      healthCheckEnabled: row.health_check_enabled === 1,
      lastHealthCheckAt: row.last_health_check_at ?? undefined,
      nextHealthCheckAt: row.next_health_check_at ?? undefined,
      lastHealthSuccessAt: row.last_health_success_at ?? undefined,
      healthCheckFailureCount: Math.max(0, Number(row.health_check_failure_count ?? 0)),
      lastHealthCheckStatusCode: optionalNumber(row.last_health_check_status_code),
      lastHealthCheckErrorCode: row.last_health_check_error_code ?? undefined,
      lastHealthCheckErrorMessage: row.last_health_check_error_message ?? undefined,
      apiKeyRuntime: apiKeyRuntimeByAccount.get(row.id),
      apiKeyRuntimeDetails: isAuthorizedView ? undefined : apiKeyRuntimeDetailsByAccount.get(row.id),
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
      lastUsedAt: isAuthorizedView ? usage.lastUsedAt : row.last_used_at ?? undefined,
      todayUsage,
      usage,
      oauthUsage: isGptVendorCode(resourceProviderCode) && resourceType === 'oauth' ? oauthUsageByAccount.get(accountResourceFactAccountId(row)) : undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationInstanceSourceAccountStatus: isAuthorizedView ? row.source_status ?? undefined : undefined,
      authorizationInstanceSourceAccountSchedulable: isAuthorizedView && typeof row.source_schedulable === 'number' ? row.source_schedulable === 1 : undefined,
      authorizationInstanceSourceAccountAvailabilitySchedule: isAuthorizedView ? parseAccountAvailabilityScheduleJson(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountScheduleActive: sourceAvailabilityScheduleActive,
      authorizationInstanceSourceAccountExpiresAt: isAuthorizedView ? row.source_account_expires_at ?? undefined : undefined,
      authorizationInstanceSourceAccountCooldownUntil: isAuthorizedView ? row.source_cooldown_until ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorCode: isAuthorizedView ? row.source_last_error_code ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorMessage: isAuthorizedView ? row.source_last_error_message ?? undefined : undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? groupBindingSystemAccountId : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationQuotaExceeded,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(authorizationSources, isAuthorizedView) : undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(hasActiveManualAuthorizationSource(authorizationSources)) : ownerPermissions(),
      authorizationUsageAvailable: !isAuthorizedView && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
      authorizationCount: isAuthorizedView ? 0 : authorizationStats.authorizationCount,
      authorizationTeamCount: isAuthorizedView ? 0 : authorizationStats.authorizationTeamCount
    })
  })
}

export function accountGroupBinding(accountId: string, systemAccountId: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        groups.name AS group_name
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
      LIMIT 1
    `)
    .get(accountId, systemAccountId) as unknown as { group_id?: string; group_name?: string; account_authorization_id?: string | null } | undefined
  if (!row?.group_id) {
    return undefined
  }
  const ownerId = accountSystemAccountId(accountId)
  const authorization = ownerId && ownerId !== systemAccountId ? activeAccountAuthorization(accountId, systemAccountId) : authorizationInstanceRuntimeAuthorization(accountId, systemAccountId)
  return {
    groupId: row.group_id,
    groupName: row.group_name ?? '',
    groupBindStatus: row.account_authorization_id && authorization?.id !== row.account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

export function accountGroupBindingFromRow(row: AccountListRow, systemAccountId?: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== systemAccountId) {
    return undefined
  }
  const accountOwnerId = row.system_account_id
  const activeAuthorizationId = accountOwnerId && systemAccountId && row.authorization_instance_authorization_id
    ? row.authorization_id ?? undefined
    : accountOwnerId && systemAccountId && accountOwnerId !== systemAccountId
    ? row.authorization_id ?? undefined
    : undefined
  return {
    groupId: row.bound_group_id,
    groupName: row.bound_group_name ?? '',
    groupBindStatus: row.bound_group_account_authorization_id && activeAuthorizationId !== row.bound_group_account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

export function accountResourceFactAccountId(row: AccountListRow): string {
  if (row.access_type !== 'authorized') return row.id
  return row.authorization_instance_source_account_id && row.source_provider_code
    ? row.authorization_instance_source_account_id
    : ''
}

export function accountResourceProviderCode(row: AccountListRow): AccountListRow['provider_code'] {
  return row.access_type === 'authorized' && row.source_provider_code
    ? row.source_provider_code
    : row.provider_code
}

export function accountResourceProviderProtocolProfileId(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_provider_protocol_profile_id
    ? row.source_provider_protocol_profile_id
    : row.provider_protocol_profile_id
}

export function accountResourceProtocolCode(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_protocol_code
    ? row.source_protocol_code
    : row.protocol_code
}

export function accountResourceProtocolVersion(row: AccountListRow): string {
  return row.access_type === 'authorized' && row.source_protocol_version
    ? row.source_protocol_version
    : row.protocol_version
}

export function accountResourceType(row: AccountListRow): AccountListRow['type'] {
  return row.access_type === 'authorized' && row.source_type
    ? row.source_type
    : row.type
}

export function accountResourceConcurrencyLimit(row: AccountListRow): number {
  if (row.access_type !== 'authorized') return Number(row.concurrency_limit)
  return Number(row.source_concurrency_limit ?? 0)
}

export function accountResourceProxyProfileId(row: AccountListRow): string | null {
  return row.access_type === 'authorized' ? row.source_proxy_profile_id ?? null : row.proxy_profile_id
}

export function accountResourceClientCompatibility(row: AccountListRow): AccountClientCompatibility {
  return normalizeOpenAIAccountClientCompatibility(
    accountResourceProviderCode(row),
    accountResourceType(row),
    row.access_type === 'authorized' ? row.source_client_compatibility : row.client_compatibility,
    'openai_standard',
    { protocolCode: accountResourceProtocolCode(row), protocolVersion: accountResourceProtocolVersion(row) }
  )
}

export function isAuthorizedSourceAccountAvailableForDispatch(row: AccountListRow, now: string): boolean {
  if (row.access_type !== 'authorized') return true
  const nowMs = Date.parse(now)
  const nowDate = Number.isFinite(nowMs) ? new Date(nowMs) : new Date()
  return Boolean(row.source_status)
    && row.source_status === 'active'
    && row.source_schedulable === 1
    && row.source_availability_schedule_active !== 0
    && row.source_last_error_code !== 'account_expired'
    && !isAccountExpired(row.source_account_expires_at ?? undefined, Number.isFinite(nowMs) ? nowMs : undefined)
    && !isLaterIso(row.source_cooldown_until ?? undefined, now)
}

export function loadAuthorizationQuotaExceededByAuthorizationId(rows: AccountListRow[]): Map<string, boolean> {
  const now = new Date()
  const output = new Map<string, boolean>()
  const checks: Array<{
    authorizationId: string
    limits: ReturnType<typeof parseRequestQuotaLimitsJson>
    input: RequestQuotaCostInput
  }> = []
  const teamGrantLimitJsonByAuthorizationId = loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows)
  for (const row of rows) {
    if (!row.authorization_id) continue
    output.set(row.authorization_id, false)
    const limits = parseRequestQuotaLimitsJson(row.authorization_limits_json)
    if (hasEnabledRequestQuotaLimit(limits)) {
      checks.push({
        authorizationId: row.authorization_id,
        limits,
        input: {
          systemAccountId: row.system_account_id,
          scopeType: 'account_authorization',
          scopeId: row.authorization_id,
          now,
          hourlyWindowHours: limits.hourly?.hours
        }
      })
    }
    const teamId = row.authorization_effective_source_team_id
    if (!teamId) continue
    const teamLimits = parseRequestQuotaLimitsJson(teamGrantLimitJsonByAuthorizationId.get(row.authorization_id))
    if (!hasEnabledRequestQuotaLimit(teamLimits)) continue
    checks.push({
      authorizationId: row.authorization_id,
      limits: teamLimits,
      input: {
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization_team',
        scopeId: `${row.id}:${teamId}`,
        now,
        hourlyWindowHours: teamLimits.hourly?.hours
      }
    })
  }
  if (!checks.length) return output
  const costsByKey = loadRequestQuotaCostsBatch(getStatsDatabase(), checks.map((check) => check.input))
  for (const check of checks) {
    const costs = costsByKey.get(requestQuotaCostKey(check.input))
    if (costs && isRequestQuotaExceeded(check.limits, costs)) {
      output.set(check.authorizationId, true)
    }
  }
  return output
}

function loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows: AccountListRow[]): Map<string, string | null> {
  const ids = [...new Set(rows
    .filter((row) => row.authorization_id && row.authorization_effective_source_team_id)
    .map((row) => row.authorization_id as string))]
  if (!ids.length) return new Map()
  const output = new Map<string, string | null>()
  const database = getBusinessDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = database.prepare(`
      SELECT ra.id AS authorization_id, grant_rows.limits_json
      FROM resource_authorizations ra
      INNER JOIN resource_authorization_grants grant_rows
        ON grant_rows.resource_type = ra.resource_type
        AND grant_rows.resource_id = ra.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = ra.effective_source_team_id
        AND grant_rows.status = 'active'
        AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ?)
      WHERE ra.status = 'active'
        AND (ra.expires_at IS NULL OR ra.expires_at > ?)
        AND ra.effective_source_team_id IS NOT NULL
        AND ra.id IN (${sqlPlaceholders(chunk.length)})
    `).all(now, now, ...chunk) as unknown as Array<{ authorization_id?: string; limits_json?: string | null }>
    for (const row of teamRows) {
      if (row.authorization_id) {
        output.set(row.authorization_id, row.limits_json ?? null)
      }
    }
  }
  return output
}

function authorizationInstanceRuntimeAuthorization(accountId: string, systemAccountId: string, database = getBusinessDatabase()): ResourceAuthorizationRow | undefined {
  const row = database
    .prepare('SELECT authorization_instance_authorization_id FROM accounts WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId, systemAccountId) as unknown as { authorization_instance_authorization_id?: string | null } | undefined
  return row?.authorization_instance_authorization_id
    ? activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId)
    : undefined
}

function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
}

function accountSummaryTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
