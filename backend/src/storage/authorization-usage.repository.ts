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
import { loadAccountNameMap, loadGroupNameMap, loadSystemAccountsByIds } from './repository-lookups.js'

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
  resource_filter_type: ResourceAuthorizationResourceType
  resource_filter_id: string
  hit_account_id: string
  hit_account_owner_system_account_id: string
}

type AuthorizationUserUsageReportRow = UsageReportRow & {
  grantee_system_account_id: string
  resource_filter_type: ResourceAuthorizationResourceType
  resource_filter_id: string
  hit_account_id: string
  hit_account_owner_system_account_id: string
}

type AuthorizationUsageSummaryRow = UsageReportRow

export function getAuthorizationTeamUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, month: AuthorizationUsageMonth): AuthorizationTeamUsageOverview {
  const filterKey = authorizationReportFilterKey(filters, access, month)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageOverview(month)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getRecordDatabase().prepare(`
    SELECT
      report.team_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.hit_account_id,
      report.hit_account_owner_system_account_id,
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
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.team_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC, report.hit_account_id ASC
  `).all(
    filterKey.systemAccountId,
    filterKey.statMonth,
    filterKey.teamFilterId,
    filterKey.teamFilterId,
    ...resourcePredicate.params
  ) as unknown as AuthorizationTeamUsageReportRow[]
  const teams = loadTeamRowsByIds(rows.map((row) => row.team_id))
  const resourceNames = loadAuthorizationResourceNameMap(rows)
  const hitAccounts = loadAccountUsageTargetRows(rows.map((row) => row.hit_account_id))
  const accountOwners = loadHitAccountOwners(rows, hitAccounts)
  const summary = loadAuthorizationTeamUsageSummary(filterKey)
  const overviewRows = rows.map((row): AuthorizationTeamUsageRow => {
    const hitAccount = hitAccountFields(row, hitAccounts, accountOwners)
    return {
      id: [row.team_id, row.resource_filter_type, row.resource_filter_id, row.hit_account_id, row.hit_account_owner_system_account_id].filter(Boolean).join(':'),
      teamId: row.team_id,
      teamName: teams.get(row.team_id)?.name ?? row.team_id,
      status: teams.get(row.team_id)?.status ?? 'active',
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resourceNames.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id)) ?? row.resource_filter_id,
      ...hitAccount,
      usage: usageSummaryFromAggregate(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: monthRange(month),
    summary,
    rows: overviewRows,
    teamCount: new Set(overviewRows.map((row) => row.teamId)).size
  }
}

export function getAuthorizationUserUsageOverview(filters: AuthorizationUsageFilters, access: AccessScope | undefined, month: AuthorizationUsageMonth): AuthorizationUserUsageOverview {
  const filterKey = authorizationReportFilterKey(filters, access, month)
  if (!filterKey) {
    return emptyAuthorizationUserUsageOverview(month)
  }

  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = getRecordDatabase().prepare(`
    SELECT
      report.grantee_system_account_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.hit_account_id,
      report.hit_account_owner_system_account_id,
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
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.grantee_system_account_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC, report.hit_account_id ASC
  `).all(
    filterKey.systemAccountId,
    filterKey.statMonth,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.granteeFilterSystemAccountId,
    ...resourcePredicate.params
  ) as unknown as AuthorizationUserUsageReportRow[]
  const accounts = loadSystemAccountsByIds(rows.map((row) => row.grantee_system_account_id))
  const resourceNames = loadAuthorizationResourceNameMap(rows)
  const hitAccounts = loadAccountUsageTargetRows(rows.map((row) => row.hit_account_id))
  const accountOwners = loadHitAccountOwners(rows, hitAccounts)
  const summary = loadAuthorizationUserUsageSummary(filterKey)
  const sourceLabels = userUsageSourceLabels(filterKey.teamFilterId)
  const overviewRows = rows.map((row): AuthorizationUserUsageRow => {
    const user = accounts.get(row.grantee_system_account_id)
    const hitAccount = hitAccountFields(row, hitAccounts, accountOwners)
    return {
      id: [row.grantee_system_account_id, row.resource_filter_type, row.resource_filter_id, row.hit_account_id, row.hit_account_owner_system_account_id].filter(Boolean).join(':'),
      systemAccountId: row.grantee_system_account_id,
      userName: user?.displayName ?? user?.username ?? row.grantee_system_account_id,
      username: user?.username,
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resourceNames.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id)) ?? row.resource_filter_id,
      ...hitAccount,
      sourceLabels,
      usage: usageSummaryFromAggregate(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: monthRange(month),
    summary,
    rows: overviewRows,
    userCount: new Set(overviewRows.map((row) => row.systemAccountId)).size
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

function loadAccountUsageTargetRows(accountIds: string[]): Map<string, { name: string; systemAccountId: string }> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name, system_account_id FROM accounts WHERE id IN (${ids.map(() => '?').join(',')})`)
    .all(...ids) as unknown as Array<{ id: string; name: string; system_account_id: string }>
  return new Map(rows.map((row) => [row.id, { name: row.name, systemAccountId: row.system_account_id }]))
}

function loadAuthorizationResourceNameMap(rows: Array<{ resource_filter_type: ResourceAuthorizationResourceType; resource_filter_id: string }>): Map<string, string> {
  const accountNames = loadAccountNameMap(rows.filter((row) => row.resource_filter_type === 'account').map((row) => row.resource_filter_id))
  const groupNames = loadGroupNameMap(rows.filter((row) => row.resource_filter_type === 'group').map((row) => row.resource_filter_id))
  const resourceNames = new Map<string, string>()
  for (const [id, name] of accountNames) {
    resourceNames.set(authorizationResourceKey('account', id), name)
  }
  for (const [id, name] of groupNames) {
    resourceNames.set(authorizationResourceKey('group', id), name)
  }
  return resourceNames
}

function authorizationResourceKey(resourceType: ResourceAuthorizationResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`
}

function loadHitAccountOwners(
  rows: Array<{ hit_account_id: string; hit_account_owner_system_account_id: string }>,
  hitAccounts: Map<string, { name: string; systemAccountId: string }>
) {
  return loadSystemAccountsByIds(rows.map((row) => {
    const ownerFromRecord = row.hit_account_owner_system_account_id
    if (ownerFromRecord) return ownerFromRecord
    return row.hit_account_id ? hitAccounts.get(row.hit_account_id)?.systemAccountId ?? '' : ''
  }))
}

function hitAccountFields(
  row: { hit_account_id: string; hit_account_owner_system_account_id: string },
  hitAccounts: Map<string, { name: string; systemAccountId: string }>,
  accountOwners: ReturnType<typeof loadSystemAccountsByIds>
) {
  const hitAccountId = row.hit_account_id
  const hitAccount = hitAccountId ? hitAccounts.get(hitAccountId) : undefined
  const ownerSystemAccountId = row.hit_account_owner_system_account_id || hitAccount?.systemAccountId
  const owner = ownerSystemAccountId ? accountOwners.get(ownerSystemAccountId) : undefined
  return {
    accountId: hitAccountId || undefined,
    accountName: hitAccountId ? hitAccount?.name ?? hitAccountId : undefined,
    accountOwnerSystemAccountId: ownerSystemAccountId || undefined,
    accountOwnerSystemAccountName: ownerSystemAccountId ? owner?.displayName ?? owner?.username ?? ownerSystemAccountId : undefined
  }
}
