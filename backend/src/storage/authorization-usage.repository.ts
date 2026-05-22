import type {
  AccountUsageStatsRange,
  AccountUsageSummary,
  AuthorizationTeamUsageOverview,
  AuthorizationTeamUsageRow,
  AuthorizationUserUsageOverview,
  AuthorizationUserUsageRow,
  ResourceAuthorizationResourceType,
  SystemTeamStatus
} from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, getStatsDatabase } from './database.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadAccountLookupMap, loadActiveSystemAccountTeamNameMapByIds, loadGroupLookupMap, loadSystemAccountNameMapByIds, loadSystemAccountPrincipalMapByIds, loadSystemTeamLookupMap } from './repository-lookups.js'

interface AuthorizationUsageFilters {
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}

interface AuthorizationUsagePageOptions {
  page?: number
  pageSize?: number
}

type AuthorizationReportResourceType = 'all' | ResourceAuthorizationResourceType

interface ReportFilterKey {
  systemAccountId: string
  range: AccountUsageStatsRange
  teamFilterId: string
  granteeFilterSystemAccountId: string
  resourceFilterType: AuthorizationReportResourceType
  resourceFilterId: string
}

type UsageReportRow = {
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost: number
  total_cost: number
  last_used_at: string | null
}

type AuthorizationTeamUsageReportRow = UsageReportRow & {
  team_id: string
  resource_filter_type: ResourceAuthorizationResourceType
  resource_filter_id: string
}

type AuthorizationUserUsageReportRow = UsageReportRow & {
  team_filter_id: string
  grantee_system_account_id: string
  resource_filter_type: ResourceAuthorizationResourceType
  resource_filter_id: string
}

type AuthorizationUsageSummaryRow = UsageReportRow

interface AuthorizationResourceInfo {
  name: string
  ownerSystemAccountId: string
}

export function getAuthorizationTeamUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): AuthorizationTeamUsageOverview {
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageOverview(range, pageOptions)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getStatsDatabase().prepare(`
    SELECT
      report.team_filter_id AS team_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      report.cache_read_cost_usd AS cache_read_cost,
      report.total_cost_usd AS total_cost,
      report.last_used_at
    FROM authorization_team_usage_range_windows report
    WHERE report.system_account_id = ?
      AND report.start_date = ?
      AND report.end_date = ?
      AND report.team_filter_id <> ''
      AND (? = '' OR report.team_filter_id = ?)
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.team_filter_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
    LIMIT ? OFFSET ?
  `).all(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.teamFilterId,
    ...resourcePredicate.params,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ) as unknown as AuthorizationTeamUsageReportRow[]
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const teams = loadTeamRowsByIds(pageRows.rows.map((row) => row.team_id))
  const resources = loadAuthorizationResourceInfoMap(pageRows.rows)
  const resourceOwners = loadAuthorizationResourceOwners(resources)
  const summary = loadAuthorizationTeamUsageSummary(filterKey)
  const overviewRows = pageRows.rows.map((row): AuthorizationTeamUsageRow => {
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.team_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      teamId: row.team_id,
      teamName: teams.get(row.team_id)?.name ?? row.team_id,
      status: teams.get(row.team_id)?.status ?? 'active',
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resource?.name ?? row.resource_filter_id,
      ...resourceOwnerFields(resource, resourceOwners),
      usage: usageSummaryFromAggregate(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    summary,
    rows: overviewRows,
    teamCount: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

export function getAuthorizationUserUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): AuthorizationUserUsageOverview {
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationUserUsageOverview(range, pageOptions)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getStatsDatabase().prepare(`
    SELECT
      report.team_filter_id,
      report.grantee_filter_system_account_id AS grantee_system_account_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      report.cache_read_cost_usd AS cache_read_cost,
      report.total_cost_usd AS total_cost,
      report.last_used_at
    FROM authorization_user_usage_range_windows report
    WHERE report.system_account_id = ?
      AND report.start_date = ?
      AND report.end_date = ?
      AND report.team_filter_id = ?
      AND report.grantee_filter_system_account_id <> ''
      AND (? = '' OR report.grantee_filter_system_account_id = ?)
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.grantee_filter_system_account_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
    LIMIT ? OFFSET ?
  `).all(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.granteeFilterSystemAccountId,
    ...resourcePredicate.params,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ) as unknown as AuthorizationUserUsageReportRow[]
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const accounts = loadSystemAccountPrincipalMapByIds(pageRows.rows.map((row) => row.grantee_system_account_id))
  const teams = loadTeamRowsByIds(pageRows.rows.map((row) => row.team_filter_id))
  const teamMemberships = loadActiveSystemAccountTeamNameMapByIds(pageRows.rows.map((row) => row.grantee_system_account_id))
  const resources = loadAuthorizationResourceInfoMap(pageRows.rows)
  const resourceOwners = loadAuthorizationResourceOwners(resources)
  const summary = loadAuthorizationUserUsageSummary(filterKey)
  const sourceLabels = userUsageSourceLabels(filterKey.teamFilterId)
  const overviewRows = pageRows.rows.map((row): AuthorizationUserUsageRow => {
    const user = accounts.get(row.grantee_system_account_id)
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.grantee_system_account_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      systemAccountId: row.grantee_system_account_id,
      userName: user?.displayName ?? user?.username ?? row.grantee_system_account_id,
      username: user?.username,
      teamNames: userUsageTeamNames(row, teams, teamMemberships),
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resource?.name ?? row.resource_filter_id,
      ...resourceOwnerFields(resource, resourceOwners),
      sourceLabels,
      usage: usageSummaryFromAggregate(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    summary,
    rows: overviewRows,
    userCount: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

function normalizeAuthorizationUsagePageOptions(options: AuthorizationUsagePageOptions): Required<AuthorizationUsagePageOptions> {
  const page = typeof options.page === 'number' && Number.isInteger(options.page) && options.page > 0 ? options.page : 1
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.max(1, Math.min(options.pageSize, authorizationUsageMaxPageSize))
    : authorizationUsageDefaultPageSize
  return { page, pageSize }
}

function authorizationReportFilterKey(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange): ReportFilterKey | undefined {
  const systemAccountId = manageableSystemAccountId(access)
  if (!systemAccountId && !canAccessAll(access)) {
    return undefined
  }
  return {
    systemAccountId: systemAccountId ?? 'global',
    range,
    teamFilterId: filters.teamId ?? '',
    granteeFilterSystemAccountId: filters.granteeSystemAccountId ?? '',
    resourceFilterType: filters.resourceType ?? 'all',
    resourceFilterId: filters.resourceId && filters.resourceType ? filters.resourceId : ''
  }
}

function authorizationDetailResourcePredicate(filterKey: ReportFilterKey): { sql: string; params: string[] } {
  if (filterKey.resourceFilterType === 'all') {
    return {
      sql: "report.resource_filter_type IN ('account', 'group') AND report.resource_filter_id <> ''",
      params: []
    }
  }
  if (!filterKey.resourceFilterId) {
    return {
      sql: "report.resource_filter_type = ? AND report.resource_filter_id <> ''",
      params: [filterKey.resourceFilterType]
    }
  }
  return {
    sql: 'report.resource_filter_type = ? AND report.resource_filter_id = ?',
    params: [filterKey.resourceFilterType, filterKey.resourceFilterId]
  }
}

function loadAuthorizationTeamUsageSummary(filterKey: ReportFilterKey): AccountUsageSummary {
  const row = getStatsDatabase().prepare(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      cache_read_cost_usd AS cache_read_cost,
      total_cost_usd AS total_cost,
      last_used_at
    FROM authorization_team_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ) as unknown as AuthorizationUsageSummaryRow | undefined
  return row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
}

function loadAuthorizationUserUsageSummary(filterKey: ReportFilterKey): AccountUsageSummary {
  const row = getStatsDatabase().prepare(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      cache_read_cost_usd AS cache_read_cost,
      total_cost_usd AS total_cost,
      last_used_at
    FROM authorization_user_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND grantee_filter_system_account_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ) as unknown as AuthorizationUsageSummaryRow | undefined
  return row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
}

function emptyAuthorizationTeamUsageOverview(range: AccountUsageStatsRange, options: Required<AuthorizationUsagePageOptions>): AuthorizationTeamUsageOverview {
  return {
    range,
    summary: emptyAccountUsageSummary(),
    rows: [],
    teamCount: 0,
    total: 0,
    page: options.page,
    pageSize: options.pageSize,
    hasMore: false
  }
}

function emptyAuthorizationUserUsageOverview(range: AccountUsageStatsRange, options: Required<AuthorizationUsagePageOptions>): AuthorizationUserUsageOverview {
  return {
    range,
    summary: emptyAccountUsageSummary(),
    rows: [],
    userCount: 0,
    total: 0,
    page: options.page,
    pageSize: options.pageSize,
    hasMore: false
  }
}

function userUsageSourceLabels(teamFilterId: string): string[] {
  return teamFilterId ? ['团队授权'] : ['全部授权来源']
}

function loadTeamRowsByIds(teamIds: string[]): Map<string, { name: string; status: SystemTeamStatus }> {
  const teams = loadSystemTeamLookupMap(teamIds)
  return new Map([...teams].map(([id, team]) => [id, { name: team.name, status: team.status }]))
}

function userUsageTeamNames(
  row: Pick<AuthorizationUserUsageReportRow, 'team_filter_id' | 'grantee_system_account_id'>,
  teams: ReturnType<typeof loadTeamRowsByIds>,
  memberships: ReturnType<typeof loadActiveSystemAccountTeamNameMapByIds>
): string[] {
  if (row.team_filter_id) {
    return [teams.get(row.team_filter_id)?.name ?? row.team_filter_id]
  }
  return memberships.get(row.grantee_system_account_id) ?? []
}

function loadAuthorizationResourceInfoMap(rows: Array<{ resource_filter_type: ResourceAuthorizationResourceType; resource_filter_id: string }>): Map<string, AuthorizationResourceInfo> {
  const accountIds = [...new Set(rows.filter((row) => row.resource_filter_type === 'account').map((row) => row.resource_filter_id).filter(Boolean))]
  const groupIds = [...new Set(rows.filter((row) => row.resource_filter_type === 'group').map((row) => row.resource_filter_id).filter(Boolean))]
  const resources = new Map<string, AuthorizationResourceInfo>()
  for (const [id, account] of loadAccountLookupMap(accountIds)) {
    resources.set(authorizationResourceKey('account', id), { name: account.name, ownerSystemAccountId: account.systemAccountId })
  }
  for (const [id, group] of loadGroupLookupMap(groupIds)) {
    resources.set(authorizationResourceKey('group', id), { name: group.name, ownerSystemAccountId: group.systemAccountId })
  }
  return resources
}

function authorizationResourceKey(resourceType: ResourceAuthorizationResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`
}

function loadAuthorizationResourceOwners(resources: Map<string, AuthorizationResourceInfo>) {
  return loadSystemAccountNameMapByIds([...resources.values()].map((resource) => resource.ownerSystemAccountId))
}

function resourceOwnerFields(resource: AuthorizationResourceInfo | undefined, owners: ReturnType<typeof loadAuthorizationResourceOwners>) {
  const ownerSystemAccountId = resource?.ownerSystemAccountId
  return {
    accountOwnerSystemAccountId: ownerSystemAccountId || undefined,
    accountOwnerSystemAccountName: ownerSystemAccountId ? owners.get(ownerSystemAccountId) ?? ownerSystemAccountId : undefined
  }
}

const authorizationUsageDefaultPageSize = 20
const authorizationUsageMaxPageSize = 200
