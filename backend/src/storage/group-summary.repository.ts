import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountGroupOptionSummary, GroupListResult, GroupOptionSummary, GroupSchedulingPolicy, GroupSummary, GroupType } from '../domain/types.js'
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
  listGroupOptionRowsForAccess,
  listGroupOptionRowsForAccessAsync,
  listGroupRowsForAccess,
  listGroupRowsForAccessAsync,
  listGroupRowsPageForAccess,
  listGroupRowsPageForAccessAsync,
  loadGroupAuthorizationUsageSummaries,
  loadGroupAuthorizationUsageSummariesAsync,
  type GroupListOptions,
  type GroupOptionListOptions
} from './group-read.repository.js'
import { loadGroupAccountIdsByGroupIds, loadGroupAccountIdsByGroupIdsAsync, loadGroupAccountStatsByGroupIds, loadGroupAccountStatsByGroupIdsAsync } from './group-read-loaders.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { chunkValues } from './query-utils.js'
import type { GroupListRow } from './repository-row-types.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { isResourceAuthorizationExpired, sanitizeAuthorizationSourcesForViewer, usageScope } from './resource-authorization-helpers.js'
import { authorizedGroupPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { loadGroupUsageSummariesForScopes, loadGroupUsageSummariesForScopesAsync } from './usage-summary-loaders.js'

export function listGroups(access?: AccessScope): GroupSummary[] {
  return buildGroupSummaries(listGroupRowsForAccess(access), access)
}

export async function listGroupsAsync(access?: AccessScope): Promise<GroupSummary[]> {
  return buildGroupSummariesAsync(await listGroupRowsForAccessAsync(access), access)
}

export function listGroupsPage(access?: AccessScope, options?: GroupListOptions): GroupListResult {
  const page = listGroupRowsPageForAccess(access, options)
  return {
    items: buildGroupSummaries(page.rows, access),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export async function listGroupsPageAsync(access?: AccessScope, options?: GroupListOptions): Promise<GroupListResult> {
  const page = await listGroupRowsPageForAccessAsync(access, options)
  return {
    items: await buildGroupSummariesAsync(page.rows, access),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export function listGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupOptionRowsForAccess(access, options), access)
}

export async function listGroupOptionsAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupOptionSummary[]> {
  return buildGroupOptionSummariesAsync(await listGroupOptionRowsForAccessAsync(access, options), access)
}

export function listAccountGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): AccountGroupOptionSummary[] {
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
  const row = findGroupRowForAccess(access, id)
  return row ? buildGroupSummaries([row], access)[0] : undefined
}

export async function findGroupSummaryAsync(id: string, access?: AccessScope): Promise<GroupSummary | undefined> {
  const row = await findGroupRowForAccessAsync(access, id)
  return row ? (await buildGroupSummariesAsync([row], access))[0] : undefined
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
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const client = await getGroupSummaryDatabaseClient()
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

function buildGroupSummaries(rows: GroupListRow[], access?: AccessScope): GroupSummary[] {
  const timezone = usageStatsTimezone()
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const groupIds = rows.map((row) => row.id)
  const groupStatsByGroup = loadGroupAccountStatsByGroupIds(groupIds)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(groupIds)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds([...accountIdsByGroup.values()].flat())
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
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const totalUsage = isAuthorizedView && row.authorization_id
      ? totalUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : totalUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const accountStats = groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsage, totalUsage)
    if (!isAuthorizedView) {
      accountStats.currentConcurrency = sumAccountCurrentConcurrency(accountIdsByGroup.get(row.id) ?? [], currentConcurrencyByAccount)
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
      accountIds: isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? [],
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

async function buildGroupSummariesAsync(rows: GroupListRow[], access?: AccessScope): Promise<GroupSummary[]> {
  const [timezone, client] = await Promise.all([
    usageStatsTimezoneAsync(),
    getGroupSummaryDatabaseClient()
  ])
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
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
    loadGroupAccountIdsByGroupIdsAsync(groupIds),
    loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id)),
    loadResourceAuthorizationSourcesByAuthorizationIdsAsync(rows.map((row) => row.authorization_id ?? '')),
    loadGroupUsageSummariesForScopesAsync(groupUsageScopes, todayDateKey(timezone)),
    loadGroupUsageSummariesForScopesAsync(groupUsageScopes),
    loadGroupAuthorizationUsageSummariesAsync(groupAuthorizationScopes, todayDateKey(timezone)),
    loadGroupAuthorizationUsageSummariesAsync(groupAuthorizationScopes)
  ])
  const currentConcurrencyByAccount = await loadAccountCurrentConcurrencyByIdsAsync([...accountIdsByGroup.values()].flat())
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const accountIds = isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? []
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const totalUsage = isAuthorizedView && row.authorization_id
      ? totalUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : totalUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const accountStats = groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsage, totalUsage)
    if (!isAuthorizedView) {
      accountStats.currentConcurrency = sumAccountCurrentConcurrency(accountIds, currentConcurrencyByAccount)
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
