import type {
  AccountUsageStatsRange,
  AccountUsageSummary,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSummary,
  ResourceAuthorizationUsageDetail
} from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import { loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
import { normalizeListPage, pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { findResourceAuthorizationSummary } from './resource-authorization-read.repository.js'
import { resourceAuthorizationSelectColumns, usageScope } from './resource-authorization-helpers.js'
import { expireDueResourceAuthorizations } from './resource-authorization-write-state.repository.js'
import { loadSystemAccountPrincipalMapByIds } from './repository-lookups.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import {
  emptyAccountUsageSummary,
  normalizeAccountUsageStatsRange,
  usageStatsTimezone,
  usageSummaryFromAggregate
} from './usage-stats-helpers.js'
import { loadUsageRangeSummaryForScope } from './usage-summary-loaders.js'

const defaultResourceAuthorizationUsageDetailPageSize = 200

export interface ResourceAuthorizationUsageOptions {
  range?: AccountUsageStatsRange
  page?: number
  pageSize?: number
}

export function getResourceAuthorizationUsage(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationUsageOptions = {}): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  const authorization = findResourceAuthorizationSummary(authorizationId, access, { includeUsage: false })
  if (!authorization) return undefined
  const range = options.range ?? normalizeAccountUsageStatsRange({}, usageStatsTimezone())
  const detail = loadResourceAuthorizationUsageDetail(authorization, range, options)
  return {
    ...authorization,
    usage: detail.usage,
    lastUsedAt: detail.usage.lastUsedAt,
    usageBySystemAccount: detail.usageBySystemAccount,
    usageBySystemAccountTotal: detail.usageBySystemAccountTotal,
    usageBySystemAccountPage: detail.usageBySystemAccountPage,
    usageBySystemAccountPageSize: detail.usageBySystemAccountPageSize,
    usageBySystemAccountHasMore: detail.usageBySystemAccountHasMore,
    usageRange: range
  }
}

function loadResourceAuthorizationUsageDetail(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  options: ResourceAuthorizationUsageOptions = {}
): {
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
} {
  const pageOptions = normalizeResourceAuthorizationUsagePageOptions(options)
  if (authorization.granteeType === 'team') {
    return loadResourceAuthorizationGrantUsageDetailForTeam(authorization, range, pageOptions)
  }
  const granteeSystemAccountId = authorization.granteeSystemAccountId
  if (!granteeSystemAccountId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const runtime = getBusinessDatabase().prepare(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM resource_authorizations
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `).get(authorization.resourceType, authorization.resourceId, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!runtime) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization' : 'group_authorization'
  const rangeUsage = loadUsageRangeSummaryForScope({
    systemAccountId: authorizationUsageStatsSystemAccountId(authorization.resourceType, authorization.resourceOwnerSystemAccountId, granteeSystemAccountId),
    scopeType,
    scopeId: runtime.id,
    range
  })
  const account = loadSystemAccountPrincipalMapByIds([granteeSystemAccountId]).get(granteeSystemAccountId)
  const usageBySystemAccount: ResourceAuthorizationUsageDetail[] = [{
    systemAccountId: granteeSystemAccountId,
    systemAccountName: account?.displayName ?? authorization.granteeSystemAccountName,
    username: account?.username ?? authorization.granteeUsername,
    ...rangeUsage,
    rangeUsage
  }]

  return {
    usage: rangeUsage,
    usageBySystemAccount: pageOptions.page === 1 ? usageBySystemAccount.sort((left, right) => {
      const leftTime = left.lastUsedAt ? Date.parse(left.lastUsedAt) : 0
      const rightTime = right.lastUsedAt ? Date.parse(right.lastUsedAt) : 0
      if (rightTime !== leftTime) {
        return rightTime - leftTime
      }
      return left.systemAccountId.localeCompare(right.systemAccountId)
    }) : [],
    usageBySystemAccountTotal: 1,
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: false
  }
}

function loadResourceAuthorizationGrantUsageDetailForTeam(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  pageOptions: { page: number; pageSize: number }
): {
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
} {
  const teamId = authorization.granteeTeamId
  if (!teamId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const database = getBusinessDatabase()
  const rows = database.prepare(`
    SELECT DISTINCT ra.*
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_sources ras
      ON ras.authorization_id = ra.id
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ra.resource_owner_system_account_id = ?
    ORDER BY ra.created_at ASC, ra.id ASC
    LIMIT ? OFFSET ?
  `).all(
    teamId,
    authorization.resourceType,
    authorization.resourceId,
    authorization.resourceOwnerSystemAccountId,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ) as unknown as ResourceAuthorizationRow[]
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const usageBySystemAccount = buildRuntimeAuthorizationUsageDetails(authorization, pageRows.rows, range)
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization_team' : 'group_authorization_team'
  const rangeUsage = loadAuthorizationTeamUsageRangeSummary(authorization, teamId, range)
    ?? loadUsageRangeSummaryForScope({
      systemAccountId: authorization.resourceOwnerSystemAccountId,
      scopeType,
      scopeId: `${authorization.resourceId}:${teamId}`,
      range
    })
  return {
    usage: rangeUsage,
    usageBySystemAccount: usageBySystemAccount.sort((left, right) => {
      const leftTime = left.lastUsedAt ? Date.parse(left.lastUsedAt) : 0
      const rightTime = right.lastUsedAt ? Date.parse(right.lastUsedAt) : 0
      if (rightTime !== leftTime) {
        return rightTime - leftTime
      }
      return left.systemAccountId.localeCompare(right.systemAccountId)
    }),
    usageBySystemAccountTotal: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, usageBySystemAccount.length, pageRows.hasMore),
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: pageRows.hasMore
  }
}

function buildRuntimeAuthorizationUsageDetails(
  authorization: ResourceAuthorizationSummary,
  rows: ResourceAuthorizationRow[],
  range: AccountUsageStatsRange
): ResourceAuthorizationUsageDetail[] {
  if (!rows.length) return []
  const scopes = rows.map((row) => usageScope(
    row.id,
    authorizationUsageStatsSystemAccountId(row.resource_type, row.resource_owner_system_account_id, row.grantee_system_account_id ?? undefined),
    row.id
  ))
  const usageByAuthorization = authorization.resourceType === 'account'
    ? loadAccountAuthorizationUsageSummaries(scopes, range)
    : loadGroupAuthorizationUsageSummaries(scopes, range)
  const accounts = loadSystemAccountPrincipalMapByIds(rows.map((row) => row.grantee_system_account_id ?? ''))
  return rows.flatMap((row) => {
    const systemAccountId = row.grantee_system_account_id
    if (!systemAccountId) return []
    const account = accounts.get(systemAccountId)
    const rangeUsage = usageByAuthorization.get(row.id) ?? emptyAccountUsageSummary()
    return [{
      systemAccountId,
      systemAccountName: account?.displayName,
      username: account?.username,
      ...rangeUsage,
      rangeUsage
    }]
  })
}

function authorizationUsageStatsSystemAccountId(
  resourceType: ResourceAuthorizationResourceType,
  resourceOwnerSystemAccountId: string,
  granteeSystemAccountId?: string
): string {
  return resourceType === 'account'
    ? granteeSystemAccountId ?? resourceOwnerSystemAccountId
    : resourceOwnerSystemAccountId
}

function loadAuthorizationTeamUsageRangeSummary(
  authorization: ResourceAuthorizationSummary,
  teamId: string,
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): AccountUsageSummary | undefined {
  const row = getStatsDatabase().prepare(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd AS total_cost, last_used_at
    FROM authorization_team_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    authorization.resourceOwnerSystemAccountId,
    range.startDate,
    range.endDate,
    teamId,
    authorization.resourceType,
    authorization.resourceId
  ) as unknown as Parameters<typeof usageSummaryFromAggregate>[0] | undefined
  return row ? usageSummaryFromAggregate(row) : undefined
}

function normalizeResourceAuthorizationUsagePageOptions(options: ResourceAuthorizationUsageOptions): { page: number; pageSize: number } {
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.min(200, Math.max(1, options.pageSize))
    : defaultResourceAuthorizationUsageDetailPageSize
  const page = normalizeListPage(options.page, pageSize)
  return { page, pageSize }
}

function emptyResourceAuthorizationUsageDetailPage(pageOptions: { page: number; pageSize: number }) {
  return {
    usage: emptyAccountUsageSummary(),
    usageBySystemAccount: [],
    usageBySystemAccountTotal: 0,
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: false
  }
}
