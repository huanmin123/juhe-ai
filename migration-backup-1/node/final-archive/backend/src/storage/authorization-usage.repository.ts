import type {
  AccountUsageStatsRange,
  AuthorizationUsageAggregateSummary,
  AuthorizationUsageRowSummary,
  AuthorizationTeamUsageRowsResult,
  AuthorizationTeamUsageSummary,
  AuthorizationTeamUsageRow,
  AuthorizationUserUsageRowsResult,
  AuthorizationUserUsageSummary,
  AuthorizationUserUsageRow,
  ResourceAuthorizationResourceType,
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import {
  loadAccountLookupMap,
  loadAccountLookupMapAsync,
  loadGroupLookupMap,
  loadGroupLookupMapAsync,
  loadSystemAccountNameMapByIds,
  loadSystemAccountNameMapByIdsAsync,
  loadSystemAccountPrincipalMapByIds,
  loadSystemAccountPrincipalMapByIdsAsync,
  loadSystemTeamLookupMap,
  loadSystemTeamLookupMapAsync
} from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export interface AuthorizationUsageFilters {
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}

export interface AuthorizationUsagePageOptions {
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
  cache_read_cost_usd: number
  cache_write_tokens?: number
  cache_write_1h_tokens?: number
  cache_write_cost_usd?: number
  thinking_tokens?: number
  input_image_tokens?: number
  output_image_tokens?: number
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

export function getAuthorizationTeamUsageRows(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): AuthorizationTeamUsageRowsResult {
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageRowsResult(range, pageOptions)
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
      report.cache_read_cost_usd,
      report.cache_write_tokens,
      report.cache_write_1h_tokens,
      report.cache_write_cost_usd,
      report.thinking_tokens,
      report.input_image_tokens,
      report.output_image_tokens,
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
  const overviewRows = pageRows.rows.map((row): AuthorizationTeamUsageRow => {
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.team_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      teamId: row.team_id,
      teamName: teams.get(row.team_id)?.name ?? '',
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resource?.name ?? '',
      ...resourceOwnerFields(resource, resourceOwners),
      usage: authorizationUsageRowSummary(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    rows: overviewRows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

export function getAuthorizationTeamUsageSummary(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange): AuthorizationTeamUsageSummary {
  const filterKey = authorizationReportFilterKey(filters, access, range)
  return { range, summary: filterKey ? loadAuthorizationTeamUsageSummary(filterKey) : authorizationUsageAggregateSummary() }
}

export async function getAuthorizationTeamUsageSummaryAsync(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange): Promise<AuthorizationTeamUsageSummary> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_authorization_team_usage_summary_read_only', filters, access, range })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') return getAuthorizationTeamUsageSummary(filters, access, range)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return { range, summary: filterKey ? await loadAuthorizationTeamUsageSummaryAsync(client, filterKey) : authorizationUsageAggregateSummary() }
}

export async function getAuthorizationTeamUsageRowsAsync(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): Promise<AuthorizationTeamUsageRowsResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_authorization_team_usage_rows_read_only',
      filters,
      access,
      range,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAuthorizationTeamUsageRows(filters, access, range, options)
  }
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationTeamUsageRowsResult(range, pageOptions)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = await client.query<AuthorizationTeamUsageReportRow>(`
    SELECT
      report.team_filter_id AS team_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      CAST(report.cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      report.cache_write_tokens,
      report.cache_write_1h_tokens,
      CAST(report.cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      report.thinking_tokens,
      report.input_image_tokens,
      report.output_image_tokens,
      CAST(report.total_cost_usd AS double precision) AS total_cost,
      report.last_used_at
    FROM ${statsTable(client, 'authorization_team_usage_range_windows')} report
    WHERE report.system_account_id = ?
      AND report.start_date = ?
      AND report.end_date = ?
      AND report.team_filter_id <> ''
      AND (? = '' OR report.team_filter_id = ?)
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.team_filter_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
    LIMIT ? OFFSET ?
  `, [
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.teamFilterId,
    ...resourcePredicate.params,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ])
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const [teams, resources] = await Promise.all([
    loadTeamRowsByIdsAsync(client, pageRows.rows.map((row) => row.team_id)),
    loadAuthorizationResourceInfoMapAsync(client, pageRows.rows)
  ])
  const resourceOwners = await loadAuthorizationResourceOwnersAsync(client, resources)
  const overviewRows = pageRows.rows.map((row): AuthorizationTeamUsageRow => {
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.team_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      teamId: row.team_id,
      teamName: teams.get(row.team_id)?.name ?? '',
      resourceType: row.resource_filter_type,
      resourceId: row.resource_filter_id,
      resourceName: resource?.name ?? '',
      ...resourceOwnerFields(resource, resourceOwners),
      usage: authorizationUsageRowSummary(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    rows: overviewRows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

export function getAuthorizationUserUsageRows(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): AuthorizationUserUsageRowsResult {
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationUserUsageRowsResult(range, pageOptions)
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
      report.cache_read_cost_usd,
      report.cache_write_tokens,
      report.cache_write_1h_tokens,
      report.cache_write_cost_usd,
      report.thinking_tokens,
      report.input_image_tokens,
      report.output_image_tokens,
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
  const resources = loadAuthorizationResourceInfoMap(pageRows.rows)
  const resourceOwners = loadAuthorizationResourceOwners(resources)
  const overviewRows = pageRows.rows.map((row): AuthorizationUserUsageRow => {
    const user = accounts.get(row.grantee_system_account_id)
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.grantee_system_account_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      userName: user?.displayName ?? '',
      username: user?.username,
      teamNames: userUsageTeamNames(row, teams),
      resourceType: row.resource_filter_type,
      resourceName: resource?.name ?? '',
      accountOwnerSystemAccountName: resource?.ownerSystemAccountId ? resourceOwners.get(resource.ownerSystemAccountId) : undefined,
      usage: authorizationUsageRowSummary(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    rows: overviewRows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

export function getAuthorizationUserUsageSummary(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange): AuthorizationUserUsageSummary {
  const filterKey = authorizationReportFilterKey(filters, access, range)
  return { range, summary: filterKey ? loadAuthorizationUserUsageSummary(filterKey) : authorizationUsageAggregateSummary() }
}

export async function getAuthorizationUserUsageSummaryAsync(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange): Promise<AuthorizationUserUsageSummary> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_authorization_user_usage_summary_read_only', filters, access, range })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') return getAuthorizationUserUsageSummary(filters, access, range)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return { range, summary: filterKey ? await loadAuthorizationUserUsageSummaryAsync(client, filterKey) : authorizationUsageAggregateSummary() }
}

export async function getAuthorizationUserUsageRowsAsync(filters: AuthorizationUsageFilters, access: AccessScope | undefined, range: AccountUsageStatsRange, options: AuthorizationUsagePageOptions = {}): Promise<AuthorizationUserUsageRowsResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_authorization_user_usage_rows_read_only',
      filters,
      access,
      range,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAuthorizationUserUsageRows(filters, access, range, options)
  }
  const pageOptions = normalizeAuthorizationUsagePageOptions(options)
  const filterKey = authorizationReportFilterKey(filters, access, range)
  if (!filterKey) {
    return emptyAuthorizationUserUsageRowsResult(range, pageOptions)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const resourcePredicate = authorizationDetailResourcePredicate(filterKey)
  const rows = await client.query<AuthorizationUserUsageReportRow>(`
    SELECT
      report.team_filter_id,
      report.grantee_filter_system_account_id AS grantee_system_account_id,
      report.resource_filter_type,
      report.resource_filter_id,
      report.request_count,
      report.input_tokens,
      report.output_tokens,
      report.cache_read_tokens,
      CAST(report.cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      report.cache_write_tokens,
      report.cache_write_1h_tokens,
      CAST(report.cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      report.thinking_tokens,
      report.input_image_tokens,
      report.output_image_tokens,
      CAST(report.total_cost_usd AS double precision) AS total_cost,
      report.last_used_at
    FROM ${statsTable(client, 'authorization_user_usage_range_windows')} report
    WHERE report.system_account_id = ?
      AND report.start_date = ?
      AND report.end_date = ?
      AND report.team_filter_id = ?
      AND report.grantee_filter_system_account_id <> ''
      AND (? = '' OR report.grantee_filter_system_account_id = ?)
      AND ${resourcePredicate.sql}
    ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC, report.grantee_filter_system_account_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
    LIMIT ? OFFSET ?
  `, [
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.granteeFilterSystemAccountId,
    ...resourcePredicate.params,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ])
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const granteeIds = pageRows.rows.map((row) => row.grantee_system_account_id)
  const [accounts, teams, resources] = await Promise.all([
    loadSystemAccountPrincipalMapByIdsAsync(client, granteeIds),
    loadTeamRowsByIdsAsync(client, pageRows.rows.map((row) => row.team_filter_id)),
    loadAuthorizationResourceInfoMapAsync(client, pageRows.rows)
  ])
  const resourceOwners = await loadAuthorizationResourceOwnersAsync(client, resources)
  const overviewRows = pageRows.rows.map((row): AuthorizationUserUsageRow => {
    const user = accounts.get(row.grantee_system_account_id)
    const resource = resources.get(authorizationResourceKey(row.resource_filter_type, row.resource_filter_id))
    return {
      id: [row.grantee_system_account_id, row.resource_filter_type, row.resource_filter_id].filter(Boolean).join(':'),
      userName: user?.displayName ?? '',
      username: user?.username,
      teamNames: userUsageTeamNames(row, teams),
      resourceType: row.resource_filter_type,
      resourceName: resource?.name ?? '',
      accountOwnerSystemAccountName: resource?.ownerSystemAccountId ? resourceOwners.get(resource.ownerSystemAccountId) : undefined,
      usage: authorizationUsageRowSummary(row),
      lastUsedAt: row.last_used_at ?? undefined
    }
  })
  return {
    range: filterKey.range,
    rows: overviewRows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, overviewRows.length, pageRows.hasMore),
    page: pageOptions.page,
    pageSize: pageOptions.pageSize,
    hasMore: pageRows.hasMore
  }
}

function normalizeAuthorizationUsagePageOptions(options: AuthorizationUsagePageOptions): Required<AuthorizationUsagePageOptions> {
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.max(1, Math.min(options.pageSize, authorizationUsageMaxPageSize))
    : authorizationUsageDefaultPageSize
  const maxPage = Math.max(1, Math.floor((authorizationUsageMaxListWindowRows - 1) / pageSize))
  const page = typeof options.page === 'number' && Number.isInteger(options.page) && options.page > 0 ? Math.min(options.page, maxPage) : 1
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

function loadAuthorizationTeamUsageSummary(filterKey: ReportFilterKey): AuthorizationUsageAggregateSummary {
  const row = getStatsDatabase().prepare(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      cache_read_cost_usd,
      cache_write_tokens,
      cache_write_1h_tokens,
      cache_write_cost_usd,
      thinking_tokens,
      input_image_tokens,
      output_image_tokens,
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
  return authorizationUsageAggregateSummary(row)
}

async function loadAuthorizationTeamUsageSummaryAsync(client: DatabaseClient, filterKey: ReportFilterKey): Promise<AuthorizationUsageAggregateSummary> {
  const row = await client.one<AuthorizationUsageSummaryRow>(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      cache_write_tokens,
      cache_write_1h_tokens,
      CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      thinking_tokens,
      input_image_tokens,
      output_image_tokens,
      CAST(total_cost_usd AS double precision) AS total_cost,
      last_used_at
    FROM ${statsTable(client, 'authorization_team_usage_range_windows')}
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `, [
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ])
  return authorizationUsageAggregateSummary(row)
}

function loadAuthorizationUserUsageSummary(filterKey: ReportFilterKey): AuthorizationUsageAggregateSummary {
  const row = getStatsDatabase().prepare(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      cache_read_cost_usd,
      cache_write_tokens,
      cache_write_1h_tokens,
      cache_write_cost_usd,
      thinking_tokens,
      input_image_tokens,
      output_image_tokens,
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
  return authorizationUsageAggregateSummary(row)
}

async function loadAuthorizationUserUsageSummaryAsync(client: DatabaseClient, filterKey: ReportFilterKey): Promise<AuthorizationUsageAggregateSummary> {
  const row = await client.one<AuthorizationUsageSummaryRow>(`
    SELECT
      request_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      cache_write_tokens,
      cache_write_1h_tokens,
      CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      thinking_tokens,
      input_image_tokens,
      output_image_tokens,
      CAST(total_cost_usd AS double precision) AS total_cost,
      last_used_at
    FROM ${statsTable(client, 'authorization_user_usage_range_windows')}
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND grantee_filter_system_account_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `, [
    filterKey.systemAccountId,
    filterKey.range.startDate,
    filterKey.range.endDate,
    filterKey.teamFilterId,
    filterKey.granteeFilterSystemAccountId,
    filterKey.resourceFilterType,
    filterKey.resourceFilterId
  ])
  return authorizationUsageAggregateSummary(row)
}

function emptyAuthorizationTeamUsageRowsResult(range: AccountUsageStatsRange, options: Required<AuthorizationUsagePageOptions>): AuthorizationTeamUsageRowsResult {
  return {
    range,
    rows: [],
    total: 0,
    page: options.page,
    pageSize: options.pageSize,
    hasMore: false
  }
}

function emptyAuthorizationUserUsageRowsResult(range: AccountUsageStatsRange, options: Required<AuthorizationUsagePageOptions>): AuthorizationUserUsageRowsResult {
  return {
    range,
    rows: [],
    total: 0,
    page: options.page,
    pageSize: options.pageSize,
    hasMore: false
  }
}

function loadTeamRowsByIds(teamIds: string[]): Map<string, { name: string }> {
  const teams = loadSystemTeamLookupMap(teamIds)
  return new Map([...teams].map(([id, team]) => [id, { name: team.name }]))
}

async function loadTeamRowsByIdsAsync(client: DatabaseClient, teamIds: string[]): Promise<Map<string, { name: string }>> {
  const teams = await loadSystemTeamLookupMapAsync(client, teamIds)
  return new Map([...teams].map(([id, team]) => [id, { name: team.name }]))
}

function userUsageTeamNames(
  row: Pick<AuthorizationUserUsageReportRow, 'team_filter_id'>,
  teams: ReturnType<typeof loadTeamRowsByIds>
): string[] {
  const teamName = row.team_filter_id ? teams.get(row.team_filter_id)?.name : undefined
  return teamName ? [teamName] : []
}

function authorizationUsageRowSummary(row: UsageReportRow): AuthorizationUsageRowSummary {
  const summary = usageSummaryFromAggregate(row)
  return { requestCount: summary.requestCount, totalTokens: summary.totalTokens, totalCost: summary.totalCost }
}

function authorizationUsageAggregateSummary(row?: UsageReportRow): AuthorizationUsageAggregateSummary {
  const summary = row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
  return {
    requestCount: summary.requestCount,
    inputTokens: summary.inputTokens,
    cacheWriteTokens: summary.cacheWriteTokens,
    totalTokens: summary.totalTokens,
    totalCost: summary.totalCost,
    lastUsedAt: summary.lastUsedAt
  }
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

async function loadAuthorizationResourceInfoMapAsync(client: DatabaseClient, rows: Array<{ resource_filter_type: ResourceAuthorizationResourceType; resource_filter_id: string }>): Promise<Map<string, AuthorizationResourceInfo>> {
  const accountIds = [...new Set(rows.filter((row) => row.resource_filter_type === 'account').map((row) => row.resource_filter_id).filter(Boolean))]
  const groupIds = [...new Set(rows.filter((row) => row.resource_filter_type === 'group').map((row) => row.resource_filter_id).filter(Boolean))]
  const resources = new Map<string, AuthorizationResourceInfo>()
  for (const [id, account] of await loadAccountLookupMapAsync(client, accountIds)) {
    resources.set(authorizationResourceKey('account', id), { name: account.name, ownerSystemAccountId: account.systemAccountId })
  }
  for (const [id, group] of await loadGroupLookupMapAsync(client, groupIds)) {
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

async function loadAuthorizationResourceOwnersAsync(client: DatabaseClient, resources: Map<string, AuthorizationResourceInfo>): Promise<Map<string, string>> {
  return loadSystemAccountNameMapByIdsAsync(client, [...resources.values()].map((resource) => resource.ownerSystemAccountId))
}

function resourceOwnerFields(resource: AuthorizationResourceInfo | undefined, owners: ReturnType<typeof loadAuthorizationResourceOwners>) {
  const ownerSystemAccountId = resource?.ownerSystemAccountId
  return {
    accountOwnerSystemAccountId: ownerSystemAccountId || undefined,
    accountOwnerSystemAccountName: ownerSystemAccountId ? owners.get(ownerSystemAccountId) : undefined
  }
}

const authorizationUsageDefaultPageSize = 20
const authorizationUsageMaxPageSize = 200
const authorizationUsageMaxListWindowRows = 1001

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}
