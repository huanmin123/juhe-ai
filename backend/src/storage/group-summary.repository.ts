import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountGroupOptionSummary, GroupAuthorizationOption, GroupEditDetail, GroupListItem, GroupListPageResult, GroupListResult, GroupOptionSummary, GroupSchedulingPolicy, GroupSelectOption, GroupSummary, GroupType, ResourceAuthorizationSourceSummary, RouteStrategyGroupOption } from '../domain/types.js'
import { includeSystemAccountFields, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationSourcesByAuthorizationIdsAsync } from './authorization-read-loaders.js'
import { groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { runtimeConfig } from '../config/runtime.js'
import {
  findGroupRowForAccess,
  findGroupRowForAccessAsync,
  findGroupRowForAccessInClientAsync,
  listGroupOptionRowsForAccess,
  listGroupOptionRowsForAccessAsync,
  listGroupOptionRowsForAccessInClientAsync,
  listGroupRowsForAccess,
  listGroupRowsForAccessAsync,
  listGroupRowsPageForAccess,
  listGroupRowsPageForAccessAsync,
  listRouteStrategyGroupOptionRowsForAccessAsync,
  loadGroupAuthorizationUsageSummaries,
  loadGroupAuthorizationUsageSummariesAsync,
  type GroupListOptions,
  type GroupOptionListOptions
} from './group-read.repository.js'
import {
  loadGroupAccountIdsByGroupIds,
  loadGroupAccountIdsByGroupIdsAsync,
  loadGroupAccountStatsByGroupIds,
  loadGroupAccountStatsByGroupIdsAsync,
  type GroupAccountStatsRow
} from './group-read-loaders.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { chunkValues } from './query-utils.js'
import type { GroupListRow } from './repository-row-types.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { isResourceAuthorizationExpired, sanitizeAuthorizationSourcesForViewer, usageScope } from './resource-authorization-helpers.js'
import { authorizedGroupPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { loadGroupUsageSummariesForScopes, loadGroupUsageSummariesForScopesAsync } from './usage-summary-loaders.js'

export function listGroups(access?: AccessScope): GroupSummary[] {
  return listGroupsReadOnly(access)
}

export function listGroupsReadOnly(access?: AccessScope): GroupSummary[] {
  return buildGroupSummaries(listGroupRowsForAccess(access), access)
}

export async function listGroupsAsync(access?: AccessScope): Promise<GroupSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_groups_read_only',
        access
      })
    }
    return listGroupsReadOnly(access)
  }
  return buildGroupSummariesAsync(await listGroupRowsForAccessAsync(access), access)
}

export function listGroupsPage(access?: AccessScope, options?: GroupListOptions): GroupListResult {
  return listGroupsPageReadOnly(access, options)
}

export function listGroupsPageReadOnly(access?: AccessScope, options?: GroupListOptions): GroupListResult {
  const page = listGroupRowsPageForAccess(access, options)
  return {
    items: buildGroupSummaries(page.rows, access, { includeAccountIds: false }),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export async function listGroupsPageAsync(access?: AccessScope, options?: GroupListOptions): Promise<GroupListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_groups_page_read_only',
        access,
        options
      })
    }
    return listGroupsPageReadOnly(access, options)
  }
  const page = await listGroupRowsPageForAccessAsync(access, options)
  return {
    items: await buildGroupSummariesAsync(page.rows, access, { includeAccountIds: false }),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export function listGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupOptionSummary[] {
  return listGroupOptionsReadOnly(access, options)
}

export function listGroupOptionsReadOnly(access?: AccessScope, options?: GroupOptionListOptions): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupOptionRowsForAccess(access, options), access)
}

export async function listGroupOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupOptionSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_group_options_read_only',
        access,
        options
      })
    }
    return listGroupOptionsReadOnly(access, options)
  }
  return buildGroupOptionSummariesAsync(await listGroupOptionRowsForAccessAsync(access, options), access)
}

export function listGroupItemsPageReadOnly(access?: AccessScope, options?: GroupListOptions): GroupListPageResult {
  const page = listGroupRowsPageForAccess(access, options)
  return {
    items: buildGroupListItems(page.rows, access),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export async function listGroupItemsPageAsync(access?: AccessScope, options?: GroupListOptions): Promise<GroupListPageResult> {
  const page = runtimeConfig.databaseDriver === 'postgres'
    ? await listGroupRowsPageForAccessAsync(access, options)
    : listGroupRowsPageForAccess(access, options)
  return {
    items: await buildGroupListItemsAsync(page.rows, access),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

/** Build the groups table projection without usage, authorization source, quota or policy reads. */
export function buildGroupListItems(rows: GroupListRow[], access?: AccessScope): GroupListItem[] {
  const statsByGroup = loadGroupAccountStatsByGroupIds(rows.map((row) => row.id))
  const authorizationSourcesById = loadResourceAuthorizationSourcesByAuthorizationIds(
    rows.map((row) => row.authorization_id ?? '').filter(Boolean)
  )
  const accountNames = includeSystemAccountFields(access) || rows.some((row) => row.access_type === 'authorized')
    ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  return rows.map((row) => groupListItemFromRow(
    row,
    access,
    statsByGroup.get(row.id),
    accountNames,
    authorizationSourcesById.get(row.authorization_id ?? '')
  ))
}

export async function buildGroupListItemsAsync(rows: GroupListRow[], access?: AccessScope): Promise<GroupListItem[]> {
  const [statsByGroup, client, authorizationSourcesById] = await Promise.all([
    loadGroupAccountStatsByGroupIdsAsync(rows.map((row) => row.id)),
    getGroupSummaryDatabaseClient(),
    loadResourceAuthorizationSourcesByAuthorizationIdsAsync(
      rows.map((row) => row.authorization_id ?? '').filter(Boolean)
    )
  ])
  const accountNames = includeSystemAccountFields(access) || rows.some((row) => row.access_type === 'authorized')
    ? await loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  return rows.map((row) => groupListItemFromRow(
    row,
    access,
    statsByGroup.get(row.id),
    accountNames,
    authorizationSourcesById.get(row.authorization_id ?? '')
  ))
}

function groupListItemFromRow(
  row: GroupListRow,
  access: AccessScope | undefined,
  statsRow: GroupAccountStatsRow | undefined,
  accountNames: Map<string, string>,
  authorizationSources: ResourceAuthorizationSourceSummary[] | undefined
): GroupListItem {
  const authorized = row.access_type === 'authorized'
  const permissions = authorized && row.system_account_id !== userVisibleSystemAccountId(access)
    ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row))
    : ownerPermissions()
  return {
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    ownerSystemAccountId: row.system_account_id,
    ownerSystemAccountName: accountNames.get(row.system_account_id),
    name: row.name,
    providerCode: row.provider_code,
    description: row.description ?? undefined,
    enabled: Number(row.enabled) === 1,
    isDefault: authorized ? false : Number(row.is_default) === 1,
    groupType: groupTypeFromRow(row),
    accessType: row.access_type ?? 'owner',
    groupAuthorizationId: row.authorization_id ?? undefined,
    updatedAt: row.updated_at,
    authorizationStatus: row.authorization_status ?? undefined,
    authorizationExpiresAt: row.authorization_expires_at ?? undefined,
    authorizationSourceSummary: authorized
      ? summarizeGroupAuthorizationSources(authorizationSources ?? [])
      : undefined,
    accountStats: {
      total: Number(statsRow?.total ?? 0),
      available: Number(statsRow?.available ?? 0),
      active: Number(statsRow?.active ?? 0),
      disabled: Number(statsRow?.disabled ?? 0),
      error: Number(statsRow?.error ?? 0),
      rateLimited: Number(statsRow?.rate_limited ?? 0),
      concurrencyLimit: Number(statsRow?.concurrency_limit ?? 0)
    },
    canEdit: !Number(row.is_default ?? 0) && permissions.canEdit !== false,
    canDelete: !Number(row.is_default ?? 0) && permissions.canDelete !== false,
    canReturn: authorized && permissions.canReturnAuthorization === true
  }
}

function summarizeGroupAuthorizationSources(
  sources: ResourceAuthorizationSourceSummary[]
): NonNullable<GroupListItem['authorizationSourceSummary']> {
  const activeSources = sources.filter((source) => source.status === 'active')
  const teamNames = [...new Set(activeSources
    .map((source) => source.sourceTeamName?.trim())
    .filter((name): name is string => Boolean(name)))]
  return {
    activeSourceCount: activeSources.length,
    hasManual: activeSources.some((source) => source.sourceType === 'manual'),
    hasTeam: activeSources.some((source) => source.sourceType === 'team')
      || sources.some((source) => source.sourceType === 'team'),
    teamNames
  }
}

export async function listGroupOptionsInClientAsync(client: DatabaseClient, access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupOptionSummary[]> {
  return buildGroupOptionSummariesInClientAsync(client, await listGroupOptionRowsForAccessInClientAsync(client, access, options), access)
}

export function listGroupSelectOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupSelectOption[] {
  return listGroupOptionRowsForAccess(access, options).map(groupSelectOptionFromRow)
}

export async function listGroupSelectOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupSelectOption[]> {
  const rows = runtimeConfig.databaseDriver === 'postgres'
    ? await listGroupOptionRowsForAccessAsync(access, options)
    : listGroupOptionRowsForAccess(access, options)
  return rows.map(groupSelectOptionFromRow)
}

export async function listRouteStrategyGroupOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<RouteStrategyGroupOption[]> {
  const rows = await listRouteStrategyGroupOptionRowsForAccessAsync(access, options)
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    providerCode: row.provider_code,
    enabled: row.enabled === true || Number(row.enabled) === 1
  }))
}

export function listGroupAuthorizationOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupAuthorizationOption[] {
  return buildGroupAuthorizationOptions(listGroupOptionRowsForAccess(access, options), access)
}

export async function listGroupAuthorizationOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupAuthorizationOption[]> {
  const rows = runtimeConfig.databaseDriver === 'postgres'
    ? await listGroupOptionRowsForAccessAsync(access, options)
    : listGroupOptionRowsForAccess(access, options)
  return buildGroupAuthorizationOptions(rows, access)
}

function groupSelectOptionFromRow(row: GroupListRow): GroupSelectOption {
  return { id: row.id, name: row.name }
}

function buildGroupAuthorizationOptions(rows: GroupListRow[], access?: AccessScope): GroupAuthorizationOption[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  return rows.map((row) => {
    const authorized = row.access_type === 'authorized' && row.system_account_id !== viewerSystemAccountId
    const permissions = authorized
      ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row))
      : ownerPermissions()
    return {
      id: row.id,
      name: row.name,
      canAuthorize: permissions.canAuthorize !== false
    }
  })
}

export function listAccountGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): AccountGroupOptionSummary[] {
  return listAccountGroupOptionsReadOnly(access, options)
}

export function listAccountGroupOptionsReadOnly(access?: AccessScope, options?: GroupOptionListOptions): AccountGroupOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const rows = listGroupOptionRowsForAccess(access, options)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(rows.map((row) => row.id))
  return buildGroupOptionSummaries(rows, access).map((group, index) => {
    const row = rows[index]
    const isAuthorizedView = row?.access_type === 'authorized' && row.system_account_id !== viewerSystemAccountId
    return {
      ...group,
      accountIds: isAuthorizedView ? [] : accountIdsByGroup.get(group.id) ?? []
    }
  })
}

export async function listAccountGroupOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<AccountGroupOptionSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_account_group_options_read_only',
        access,
        options
      })
    }
    return listAccountGroupOptionsReadOnly(access, options)
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const rows = await listGroupOptionRowsForAccessAsync(access, options)
  const accountIdsByGroup = await loadGroupAccountIdsByGroupIdsAsync(rows.map((row) => row.id))
  const groups = await buildGroupOptionSummariesAsync(rows, access)
  return groups.map((group, index) => {
    const row = rows[index]
    const isAuthorizedView = row?.access_type === 'authorized' && row.system_account_id !== viewerSystemAccountId
    return {
      ...group,
      accountIds: isAuthorizedView ? [] : accountIdsByGroup.get(group.id) ?? []
    }
  })
}

export function findGroupSummary(id: string, access?: AccessScope): GroupSummary | undefined {
  return findGroupSummaryReadOnly(id, access)
}

export function findGroupSummaryReadOnly(id: string, access?: AccessScope): GroupSummary | undefined {
  const row = findGroupRowForAccess(access, id)
  return row ? buildGroupSummaries([row], access)[0] : undefined
}

export async function findGroupSummaryAsync(id: string, access?: AccessScope): Promise<GroupSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'find_group_summary_read_only',
        id,
        access
      })
    }
    return findGroupSummaryReadOnly(id, access)
  }
  const row = await findGroupRowForAccessAsync(access, id)
  return row ? (await buildGroupSummariesAsync([row], access))[0] : undefined
}

export async function findGroupEditDetailAsync(id: string, access?: AccessScope): Promise<GroupEditDetail | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'find_group_edit_detail_read_only',
        id,
        access
      })
    }
    return findGroupEditDetailReadOnly(id, access)
  }
  const row = await findGroupRowForAccessAsync(access, id)
  return row ? groupEditDetailFromRow(row, access) : undefined
}

export function findGroupEditDetailReadOnly(id: string, access?: AccessScope): GroupEditDetail | undefined {
  const row = findGroupRowForAccess(access, id)
  return row ? groupEditDetailFromRow(row, access) : undefined
}

function groupEditDetailFromRow(row: GroupListRow, access?: AccessScope): GroupEditDetail {
  const authorized = row.access_type === 'authorized'
  return {
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    name: row.name,
    providerCode: row.provider_code,
    description: row.description ?? undefined,
    enabled: Number(row.enabled) === 1,
    isDefault: authorized ? false : Number(row.is_default) === 1,
    groupType: groupTypeFromRow(row),
    schedulingPolicy: groupSchedulingPolicyFromRow(row),
    accessType: row.access_type ?? 'owner',
    updatedAt: row.updated_at
  }
}

export async function findGroupSummaryInClientAsync(client: DatabaseClient, id: string, access?: AccessScope): Promise<GroupSummary | undefined> {
  const row = await findGroupRowForAccessInClientAsync(client, access, id)
  return row ? (await buildGroupSummariesInClientAsync(client, [row], access))[0] : undefined
}

export function buildGroupOptionSummaries(rows: GroupListRow[], access?: AccessScope): GroupOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      enabled: row.enabled === 1,
      isDefault: isAuthorizedView ? false : row.is_default === 1,
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row)) : ownerPermissions()
    }
  })
}

export async function buildGroupOptionSummariesAsync(rows: GroupListRow[], access?: AccessScope): Promise<GroupOptionSummary[]> {
  const client = await getGroupSummaryDatabaseClient()
  return buildGroupOptionSummariesInClientAsync(client, rows, access)
}

async function buildGroupOptionSummariesInClientAsync(client: DatabaseClient, rows: GroupListRow[], access?: AccessScope): Promise<GroupOptionSummary[]> {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows
    ? await loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      enabled: Number(row.enabled) === 1,
      isDefault: isAuthorizedView ? false : Number(row.is_default) === 1,
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row)) : ownerPermissions()
    }
  })
}

interface BuildGroupSummaryOptions {
  includeAccountIds?: boolean
}

function buildGroupSummaries(rows: GroupListRow[], access?: AccessScope, options: BuildGroupSummaryOptions = {}): GroupSummary[] {
  const timezone = usageStatsTimezone()
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const includeAccountIds = options.includeAccountIds !== false
  const groupIds = rows.map((row) => row.id)
  const groupStatsByGroup = loadGroupAccountStatsByGroupIds(groupIds)
  const accountIdsByGroup = includeAccountIds ? loadGroupAccountIdsByGroupIds(groupIds) : new Map<string, string[]>()
  const currentConcurrencyByAccount = includeAccountIds ? loadAccountCurrentConcurrencyByIds([...accountIdsByGroup.values()].flat()) : new Map<string, number>()
  const groupUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const groupAuthorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const todayUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes, todayDateKey(timezone))
  const totalUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes)
  const todayUsageByAuthorization = loadGroupAuthorizationUsageSummaries(groupAuthorizationScopes, todayDateKey(timezone))
  const totalUsageByAuthorization = loadGroupAuthorizationUsageSummaries(groupAuthorizationScopes)
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const accountNames = loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id))
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const accountIds = !includeAccountIds || isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? []
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const totalUsage = isAuthorizedView && row.authorization_id
      ? totalUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : totalUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const accountStats = groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsage, totalUsage)
    if (!isAuthorizedView) {
      if (includeAccountIds) {
        accountStats.total = accountIds.length
        accountStats.currentConcurrency = sumAccountCurrentConcurrency(accountIds, currentConcurrencyByAccount)
      }
    }
    return {
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      description: row.description ?? undefined,
      enabled: row.enabled === 1,
      isDefault: isAuthorizedView ? false : row.is_default === 1,
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
      accountIds,
      accountStats,
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId
        ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row), hasActiveManualAuthorizationSource(sourcesByAuthorization.get(row.authorization_id ?? '') ?? []))
        : ownerPermissions()
    }
  })
}

async function buildGroupSummariesAsync(rows: GroupListRow[], access?: AccessScope, options: BuildGroupSummaryOptions = {}): Promise<GroupSummary[]> {
  const [timezone, client] = await Promise.all([
    usageStatsTimezoneAsync(),
    getGroupSummaryDatabaseClient()
  ])
  return buildGroupSummariesInClientAsync(client, rows, access, options, timezone)
}

async function buildGroupSummariesInClientAsync(
  client: DatabaseClient,
  rows: GroupListRow[],
  access?: AccessScope,
  options: BuildGroupSummaryOptions = {},
  timezoneInput?: string
): Promise<GroupSummary[]> {
  const timezone = timezoneInput ?? await usageStatsTimezoneAsync()
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const includeAccountIds = options.includeAccountIds !== false
  const groupIds = rows.map((row) => row.id)
  const groupUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const groupAuthorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const [
    groupStatsByGroup,
    accountIdsByGroup,
    accountNames,
    sourcesByAuthorization,
    todayUsageByGroup,
    totalUsageByGroup,
    todayUsageByAuthorization,
    totalUsageByAuthorization
  ] = await Promise.all([
    loadGroupAccountStatsByGroupIdsAsync(groupIds),
    includeAccountIds ? loadGroupAccountIdsByGroupIdsAsync(groupIds) : Promise.resolve(new Map<string, string[]>()),
    loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id)),
    loadResourceAuthorizationSourcesByAuthorizationIdsAsync(rows.map((row) => row.authorization_id ?? '')),
    loadGroupUsageSummariesForScopesAsync(groupUsageScopes, todayDateKey(timezone)),
    loadGroupUsageSummariesForScopesAsync(groupUsageScopes),
    loadGroupAuthorizationUsageSummariesAsync(groupAuthorizationScopes, todayDateKey(timezone)),
    loadGroupAuthorizationUsageSummariesAsync(groupAuthorizationScopes)
  ])
  const currentConcurrencyByAccount = includeAccountIds
    ? await loadAccountCurrentConcurrencyByIdsAsync([...accountIdsByGroup.values()].flat())
    : new Map<string, number>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const accountIds = !includeAccountIds || isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? []
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const totalUsage = isAuthorizedView && row.authorization_id
      ? totalUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : totalUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const accountStats = groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsage, totalUsage)
    if (!isAuthorizedView) {
      if (includeAccountIds) {
        accountStats.total = accountIds.length
        accountStats.currentConcurrency = sumAccountCurrentConcurrency(accountIds, currentConcurrencyByAccount)
      }
    }
    return {
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      description: row.description ?? undefined,
      enabled: Number(row.enabled) === 1,
      isDefault: isAuthorizedView ? false : Number(row.is_default) === 1,
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
      accountIds,
      accountStats,
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId
        ? authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row), hasActiveManualAuthorizationSource(sourcesByAuthorization.get(row.authorization_id ?? '') ?? []))
        : ownerPermissions()
    }
  })
}

function canBindAuthorizedGroupRowToApiKey(row: GroupListRow): boolean {
  return Number(row.enabled) === 1 && row.authorization_status === 'active' && !isResourceAuthorizationExpired(row.authorization_expires_at)
}

function groupTypeFromRow(row: Pick<GroupListRow, 'group_type'>): GroupType {
  return normalizeGroupType(row.group_type)
}

function groupSchedulingPolicyFromRow(row: Pick<GroupListRow, 'group_type' | 'scheduling_policy_json'>): GroupSchedulingPolicy | undefined {
  return parseGroupSchedulingPolicyJson(row.scheduling_policy_json, groupTypeFromRow(row))
}

async function getGroupSummaryDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

async function loadSystemAccountNameMapByIdsAsync(client: DatabaseClient, systemAccountIds: Array<string | undefined>): Promise<Map<string, string>> {
  const ids = uniqueTextValues(systemAccountIds)
  if (!ids.length) return new Map()
  const rows: Array<{ id: string; display_name: string }> = []
  const table = groupSummaryTable(client, 'system_accounts')
  for (const chunk of chunkValues(ids, 500)) {
    rows.push(...await client.query<{ id: string; display_name: string }>(`
      SELECT id, display_name
      FROM ${table}
      WHERE id IN (${client.dialect.bindPlaceholders(chunk.length)})
    `, chunk))
  }
  return new Map(rows.map((row) => [row.id, row.display_name]))
}

function uniqueTextValues(values: Array<string | undefined | null>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function groupSummaryTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
