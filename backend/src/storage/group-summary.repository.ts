import { loadAccountCurrentConcurrencyByIds, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import type { AccountGroupOptionSummary, GroupListResult, GroupOptionSummary, GroupSchedulingPolicy, GroupSummary, GroupType } from '../domain/types.js'
import { includeSystemAccountFields, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds } from './authorization-read-loaders.js'
import { groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import {
  findGroupRowForAccess,
  listGroupOptionRowsForAccess,
  listGroupRowsForAccess,
  listGroupRowsPageForAccess,
  loadGroupAuthorizationUsageSummaries,
  type GroupListOptions,
  type GroupOptionListOptions
} from './group-read.repository.js'
import { loadGroupAccountIdsByGroupIds, loadGroupAccountStatsByGroupIds } from './group-read-loaders.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import type { GroupListRow } from './repository-row-types.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { isResourceAuthorizationExpired, sanitizeAuthorizationSourcesForViewer, usageScope } from './resource-authorization-helpers.js'
import { authorizedGroupPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { loadGroupUsageSummariesForScopes } from './usage-summary-loaders.js'

export function listGroups(access?: AccessScope): GroupSummary[] {
  return buildGroupSummaries(listGroupRowsForAccess(access), access)
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

export function listGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupOptionRowsForAccess(access, options), access)
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

export function findGroupSummary(id: string, access?: AccessScope): GroupSummary | undefined {
  const row = findGroupRowForAccess(access, id)
  return row ? buildGroupSummaries([row], access)[0] : undefined
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
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
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
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
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

function canBindAuthorizedGroupRowToApiKey(row: GroupListRow): boolean {
  return row.enabled === 1 && row.authorization_status === 'active' && !isResourceAuthorizationExpired(row.authorization_expires_at)
}

function groupTypeFromRow(row: Pick<GroupListRow, 'group_type'>): GroupType {
  return normalizeGroupType(row.group_type)
}

function groupSchedulingPolicyFromRow(row: Pick<GroupListRow, 'group_type' | 'scheduling_policy_json'>): GroupSchedulingPolicy | undefined {
  return parseGroupSchedulingPolicyJson(row.scheduling_policy_json, groupTypeFromRow(row))
}
