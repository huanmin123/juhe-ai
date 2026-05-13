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
  statMonth: string
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
  total_cost: number
  last_used_at: string | null
}

type AuthorizationTeamUsageReportRow = UsageReportRow & {
  team_id: string
}

type AuthorizationUserUsageReportRow = UsageReportRow & {
  grantee_system_account_id: string
}

type AuthorizationUsageSummaryRow = UsageReportRow

export function getAuthorizationTeamUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, month: AuthorizationUsageMonth): AuthorizationTeamUsageOverview {
  const filterKey = authorizationReportFilterKey(filters, access, month)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageOverview(month)
  }

  const rows = getRecordDatabase().prepare(`
    SELECT
      report.team_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      report.total_cost_usd AS total_cost,
      report.last_used_at
    FROM authorization_team_usage_monthly report
    WHERE report.system_account_id = ?
      AND report.stat_month = ?
      AND (? = '' OR report.team_id = ?)
      AND report.resource_filter_type = ?
      AND report.resource_filter_id = ?
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.team_id ASC
  `).all(
    filterKey.systemAccountId,
    filterKey.statMonth,
    filterKey.teamFilterId,
    filterKey.teamFilterId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ) as unknown as AuthorizationTeamUsageReportRow[]
  const teams = loadTeamRowsByIds(rows.map((row) => row.team_id))
  const summary = loadAuthorizationTeamUsageSummary(filterKey)
  const overviewRows = rows.map((row): AuthorizationTeamUsageRow => ({
    id: row.team_id,
    teamId: row.team_id,
    teamName: teams.get(row.team_id)?.name ?? row.team_id,
    status: teams.get(row.team_id)?.status ?? 'active',
    usage: usageSummaryFromAggregate(row),
    lastUsedAt: row.last_used_at ?? undefined
  }))
  return {
    range: monthRange(month),
    summary,
    rows: overviewRows,
    teamCount: overviewRows.length
  }
}

export function getAuthorizationUserUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, month: AuthorizationUsageMonth): AuthorizationUserUsageOverview {
  const filterKey = authorizationReportFilterKey(filters, access, month)
  if (!filterKey) {
    return emptyAuthorizationUserUsageOverview(month)
  }

  const rows = getRecordDatabase().prepare(`
    SELECT
      report.grantee_system_account_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      report.total_cost_usd AS total_cost,
      report.last_used_at
    FROM authorization_user_usage_monthly report
    WHERE report.system_account_id = ?
      AND report.stat_month = ?
      AND report.team_filter_id = ?
      AND (? = '' OR report.grantee_system_account_id = ?)
      AND report.resource_filter_type = ?
      AND report.resource_filter_id = ?
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.grantee_system_account_id ASC
  `).all(
    filterKey.systemAccountId,
    filterKey.statMonth,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ) as unknown as AuthorizationUserUsageReportRow[]
  const accounts = loadSystemAccountsByIds(rows.map((row) => row.grantee_system_account_id))
  const summary = loadAuthorizationUserUsageSummary(filterKey)
  const sourceLabels = userUsageSourceLabels(filterKey.teamFilterId)
  const overviewRows = rows.map((row): AuthorizationUserUsageRow => {
    const user = accounts.get(row.grantee_system_account_id)
    return {
      id: row.grantee_system_account_id,
      systemAccountId: row.grantee_system_account_id,
      userName: user?.displayName ?? user?.username ?? row.grantee_system_account_id,
      username: user?.username,
      sourceLabels,
      usage: usageSummaryFromAggregate(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: monthRange(month),
    summary,
    rows: overviewRows,
    userCount: overviewRows.length
  }
}

function authorizationReportFilterKey(filters: AuthorizationUsageFilters, access: AccessScope | undefined, month: AuthorizationUsageMonth): ReportFilterKey | undefined {
  const systemAccountId = manageableSystemAccountId(access)
  if (!systemAccountId && !canAccessAll(access)) {
    return undefined
  }
  return {
    systemAccountId: systemAccountId ?? 'global',
    statMonth: month.statMonth,
    teamFilterId: filters.teamId ?? '',
    granteeFilterSystemAccountId: filters.granteeSystemAccountId ?? '',
    resourceFilterType: filters.resourceType ?? 'all',
    resourceFilterId: filters.resourceId && filters.resourceType ? filters.resourceId : ''
  }
}

function loadAuthorizationTeamUsageSummary(filterKey: ReportFilterKey): AccountUsageSummary {
  const row = getRecordDatabase().prepare(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      total_cost_usd AS total_cost,
      last_used_at
    FROM authorization_team_usage_summary_monthly
    WHERE system_account_id = ?
      AND stat_month = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    filterKey.systemAccountId,
    filterKey.statMonth,
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
      total_cost_usd AS total_cost,
      last_used_at
    FROM authorization_user_usage_summary_monthly
    WHERE system_account_id = ?
      AND stat_month = ?
      AND team_filter_id = ?
      AND grantee_filter_system_account_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    filterKey.systemAccountId,
    filterKey.statMonth,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ) as unknown as AuthorizationUsageSummaryRow | undefined
  return row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
}

function emptyAuthorizationTeamUsageOverview(month: AuthorizationUsageMonth): AuthorizationTeamUsageOverview {
  return {
    range: monthRange(month),
    summary: emptyAccountUsageSummary(),
    rows: [],
    teamCount: 0
  }
}

function emptyAuthorizationUserUsageOverview(month: AuthorizationUsageMonth): AuthorizationUserUsageOverview {
  return {
    range: monthRange(month),
    summary: emptyAccountUsageSummary(),
    rows: [],
    userCount: 0
  }
}

function monthRange(month: AuthorizationUsageMonth): AccountUsageStatsRange {
  return {
    startDate: month.startDate,
    endDate: month.endDate,
    days: daysInMonth(month.statMonth),
    maxDays: daysInMonth(month.statMonth)
  }
}

function daysInMonth(statMonth: string): number {
  const [year, month] = statMonth.split('-').map(Number)
  if (!Number.isFinite(year) || !Number.isFinite(month)) return 31
  return new Date(year, month, 0).getDate()
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
