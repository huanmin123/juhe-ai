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
import { getDatabase, getRecordDatabase } from './database.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadSystemAccountsByIds } from './repository-lookups.js'

interface AuthorizationUsageFilters {
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}

type AuthorizationReportResourceType = 'all' | ResourceAuthorizationResourceType

interface AuthorizationUsageMonth {
  statMonth: string
  startDate: string
  endDate: string
}

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

export function getAuthorizationTeamUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange | AuthorizationUsageMonth): AuthorizationTeamUsageOverview {
  const normalizedRange = authorizationUsageRange(range)
  const filterKey = authorizationReportFilterKey(filters, access, normalizedRange)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageOverview(normalizedRange)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getRecordDatabase().prepare(`
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
  `).all(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.teamFilterId,
    ...resourcePredicate.params
  ) as unknown as AuthorizationTeamUsageReportRow[]
  const teams = loadTeamRowsByIds(rows.map((row) => row.team_id))
  const resources = loadAuthorizationResourceInfoMap(rows)
  const resourceOwners = loadAuthorizationResourceOwners(resources)
  const summary = loadAuthorizationTeamUsageSummary(filterKey)
  const overviewRows = rows.map((row): AuthorizationTeamUsageRow => {
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
    teamCount: new Set(overviewRows.map((row) => row.teamId)).size
  }
}

export function getAuthorizationUserUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange | AuthorizationUsageMonth): AuthorizationUserUsageOverview {
  const normalizedRange = authorizationUsageRange(range)
  const filterKey = authorizationReportFilterKey(filters, access, normalizedRange)
  if (!filterKey) {
    return emptyAuthorizationUserUsageOverview(normalizedRange)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getRecordDatabase().prepare(`
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
  `).all(
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.granteeFilterSystemAccountId,
    ...resourcePredicate.params
  ) as unknown as AuthorizationUserUsageReportRow[]
  const accounts = loadSystemAccountsByIds(rows.map((row) => row.grantee_system_account_id))
  const teams = loadTeamRowsByIds(rows.map((row) => row.team_filter_id))
  const teamMemberships = loadActiveTeamMembershipsBySystemAccountIds(rows.map((row) => row.grantee_system_account_id))
  const resources = loadAuthorizationResourceInfoMap(rows)
  const resourceOwners = loadAuthorizationResourceOwners(resources)
  const summary = loadAuthorizationUserUsageSummary(filterKey)
  const sourceLabels = userUsageSourceLabels(filterKey.teamFilterId)
  const overviewRows = rows.map((row): AuthorizationUserUsageRow => {
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
    userCount: new Set(overviewRows.map((row) => row.systemAccountId)).size
  }
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
  const row = getRecordDatabase().prepare(`
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
  const row = getRecordDatabase().prepare(`
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

function emptyAuthorizationTeamUsageOverview(range: AccountUsageStatsRange): AuthorizationTeamUsageOverview {
  return {
    range,
    summary: emptyAccountUsageSummary(),
    rows: [],
    teamCount: 0
  }
}

function emptyAuthorizationUserUsageOverview(range: AccountUsageStatsRange): AuthorizationUserUsageOverview {
  return {
    range,
    summary: emptyAccountUsageSummary(),
    rows: [],
    userCount: 0
  }
}

function authorizationUsageRange(range: AccountUsageStatsRange | AuthorizationUsageMonth): AccountUsageStatsRange {
  if ('days' in range && 'maxDays' in range) {
    return range
  }
  const days = daysBetween(range.startDate, range.endDate)
  return {
    startDate: range.startDate,
    endDate: range.endDate,
    days,
    maxDays: days
  }
}

function daysBetween(startDate: string, endDate: string): number {
  const start = dateFromKey(startDate)
  const end = dateFromKey(endDate)
  if (!start || !end || start > end) return 1
  return Math.max(1, Math.floor((end.getTime() - start.getTime()) / (24 * 60 * 60 * 1000)) + 1)
}

function dateFromKey(value: string): Date | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  return date.getFullYear() === Number(match[1]) && date.getMonth() === Number(match[2]) - 1 && date.getDate() === Number(match[3]) ? date : undefined
}

function userUsageSourceLabels(teamFilterId: string): string[] {
  return teamFilterId ? ['团队授权'] : ['全部授权来源']
}

function loadTeamRowsByIds(teamIds: string[]): Map<string, { name: string; status: SystemTeamStatus }> {
  const ids = [...new Set(teamIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name, status FROM system_teams WHERE id IN (${ids.map(() => '?').join(',')})`)
    .all(...ids) as unknown as Array<{ id: string; name: string; status: SystemTeamStatus }>
  return new Map(rows.map((row) => [row.id, { name: row.name, status: row.status }]))
}

function loadActiveTeamMembershipsBySystemAccountIds(systemAccountIds: string[]): Map<string, string[]> {
  const ids = [...new Set(systemAccountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`
      SELECT members.system_account_id, teams.name
      FROM system_team_members members
      INNER JOIN system_teams teams ON teams.id = members.team_id
      WHERE members.status = 'active'
        AND members.system_account_id IN (${ids.map(() => '?').join(',')})
      ORDER BY teams.name COLLATE NOCASE ASC, teams.id ASC
    `)
    .all(...ids) as unknown as Array<{ system_account_id: string; name: string }>
  const result = new Map<string, string[]>()
  for (const row of rows) {
    const names = result.get(row.system_account_id) ?? []
    names.push(row.name)
    result.set(row.system_account_id, names)
  }
  return result
}

function userUsageTeamNames(
  row: Pick<AuthorizationUserUsageReportRow, 'team_filter_id' | 'grantee_system_account_id'>,
  teams: ReturnType<typeof loadTeamRowsByIds>,
  memberships: ReturnType<typeof loadActiveTeamMembershipsBySystemAccountIds>
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
  if (accountIds.length) {
    const accountRows = getDatabase()
      .prepare(`SELECT id, name, system_account_id FROM accounts WHERE id IN (${accountIds.map(() => '?').join(',')})`)
      .all(...accountIds) as unknown as Array<{ id: string; name: string; system_account_id: string }>
    for (const row of accountRows) {
      resources.set(authorizationResourceKey('account', row.id), { name: row.name, ownerSystemAccountId: row.system_account_id })
    }
  }
  if (groupIds.length) {
    const groupRows = getDatabase()
      .prepare(`SELECT id, name, system_account_id FROM groups WHERE id IN (${groupIds.map(() => '?').join(',')})`)
      .all(...groupIds) as unknown as Array<{ id: string; name: string; system_account_id: string }>
    for (const row of groupRows) {
      resources.set(authorizationResourceKey('group', row.id), { name: row.name, ownerSystemAccountId: row.system_account_id })
    }
  }
  return resources
}

function authorizationResourceKey(resourceType: ResourceAuthorizationResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`
}

function loadAuthorizationResourceOwners(resources: Map<string, AuthorizationResourceInfo>) {
  return loadSystemAccountsByIds([...resources.values()].map((resource) => resource.ownerSystemAccountId))
}

function resourceOwnerFields(resource: AuthorizationResourceInfo | undefined, owners: ReturnType<typeof loadSystemAccountsByIds>) {
  const ownerSystemAccountId = resource?.ownerSystemAccountId
  const owner = ownerSystemAccountId ? owners.get(ownerSystemAccountId) : undefined
  return {
    accountOwnerSystemAccountId: ownerSystemAccountId || undefined,
    accountOwnerSystemAccountName: ownerSystemAccountId ? owner?.displayName ?? owner?.username ?? ownerSystemAccountId : undefined
  }
}
