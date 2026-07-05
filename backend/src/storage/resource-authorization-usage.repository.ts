import type {
  AccountUsageStatsRange,
  AccountUsageSummary,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSummary,
  ResourceAuthorizationUsageDetail
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import type { AccessScope } from './access-scope.js'
import { loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { findResourceAuthorizationSummary, findResourceAuthorizationSummaryAsync } from './resource-authorization-read.repository.js'
import { resourceAuthorizationSelectColumns, usageScope } from './resource-authorization-helpers.js'
import { expireDueResourceAuthorizationsAsync } from './resource-authorization-write.repository.js'
import { expireDueResourceAuthorizations } from './resource-authorization-write-state.repository.js'
import { loadSystemAccountPrincipalMapByIds, loadSystemAccountPrincipalMapByIdsAsync } from './repository-lookups.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import {
  emptyAccountUsageSummary,
  addUsageSummaries,
  normalizeAccountUsageStatsRange,
  usageStatsTimezone,
  usageStatsTimezoneAsync,
  usageSummaryFromAggregate
} from './usage-stats-helpers.js'
import {
  loadAuthorizationUsageRangeSummariesForScopes,
  loadAuthorizationUsageRangeSummariesForScopesAsync,
  loadUsageRangeSummaryForScope,
  loadUsageRangeSummaryForScopeAsync,
  type UsageSummaryScopeRequest
} from './usage-summary-loaders.js'

const defaultResourceAuthorizationUsageDetailPageSize = 200
const businessSchemaName = 'juhe_business'
const statsSchemaName = 'juhe_stats'

export interface ResourceAuthorizationUsageOptions {
  range?: AccountUsageStatsRange
  page?: number
  pageSize?: number
}

export function getResourceAuthorizationUsage(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationUsageOptions = {}): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  return getResourceAuthorizationUsageReadOnly(authorizationId, access, options)
}

export function getResourceAuthorizationUsageReadOnly(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationUsageOptions = {}): ResourceAuthorizationSummary | undefined {
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

export async function getResourceAuthorizationUsageAsync(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationUsageOptions = {}): Promise<ResourceAuthorizationSummary | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_resource_authorization_usage_read_only',
      id: authorizationId,
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getResourceAuthorizationUsage(authorizationId, access, options)
  }
  await expireDueResourceAuthorizationsAsync()
  const authorization = await findResourceAuthorizationSummaryAsync(authorizationId, access, { includeUsage: false })
  if (!authorization) return undefined
  const range = options.range ?? normalizeAccountUsageStatsRange({}, await usageStatsTimezoneAsync())
  const detail = await loadResourceAuthorizationUsageDetailAsync(authorization, range, options)
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

async function loadResourceAuthorizationUsageDetailAsync(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  options: ResourceAuthorizationUsageOptions = {}
): Promise<{
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
}> {
  const pageOptions = normalizeResourceAuthorizationUsagePageOptions(options)
  if (authorization.granteeType === 'team') {
    return loadResourceAuthorizationGrantUsageDetailForTeamAsync(authorization, range, pageOptions)
  }
  const granteeSystemAccountId = authorization.granteeSystemAccountId
  if (!granteeSystemAccountId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const client = await getResourceAuthorizationUsageBusinessClient()
  const runtime = await client.one<ResourceAuthorizationRow>(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM ${resourceAuthorizationUsageBusinessTable(client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [authorization.resourceType, authorization.resourceId, granteeSystemAccountId])
  if (!runtime) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization' : 'group_authorization'
  const rangeUsage = await loadUsageRangeSummaryForScopeAsync({
    systemAccountId: authorizationUsageStatsSystemAccountId(authorization.resourceType, authorization.resourceOwnerSystemAccountId, granteeSystemAccountId),
    scopeType,
    scopeId: runtime.id,
    range
  })
  const account = (await loadSystemAccountPrincipalMapByIdsAsync(client, [granteeSystemAccountId])).get(granteeSystemAccountId)
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
  const rangeUsage = loadAuthorizationTeamUsageRangeSummary(authorization, teamId, range)
    ?? loadAuthorizationTeamUsageFallbackSummary(authorization, teamId, range)
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

async function loadResourceAuthorizationGrantUsageDetailForTeamAsync(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  pageOptions: { page: number; pageSize: number }
): Promise<{
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
}> {
  const teamId = authorization.granteeTeamId
  if (!teamId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const client = await getResourceAuthorizationUsageBusinessClient()
  const rows = await client.query<ResourceAuthorizationRow>(`
    SELECT DISTINCT ${resourceAuthorizationSelectColumns('ra')}
    FROM ${resourceAuthorizationUsageBusinessTable(client, 'resource_authorizations')} ra
    INNER JOIN ${resourceAuthorizationUsageBusinessTable(client, 'resource_authorization_sources')} ras
      ON ras.authorization_id = ra.id
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ra.resource_owner_system_account_id = ?
    ORDER BY ra.created_at ASC, ra.id ASC
    LIMIT ? OFFSET ?
  `, [
    teamId,
    authorization.resourceType,
    authorization.resourceId,
    authorization.resourceOwnerSystemAccountId,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ])
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const usageBySystemAccount = await buildRuntimeAuthorizationUsageDetailsAsync(authorization, pageRows.rows, range)
  const rangeUsage = await loadAuthorizationTeamUsageRangeSummaryAsync(authorization, teamId, range)
    ?? await loadAuthorizationTeamUsageFallbackSummaryAsync(authorization, teamId, range)
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

function loadAuthorizationTeamUsageFallbackSummary(
  authorization: ResourceAuthorizationSummary,
  teamId: string,
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): AccountUsageSummary {
  if (authorization.resourceType === 'group') {
    return loadUsageRangeSummaryForScope({
      systemAccountId: authorization.resourceOwnerSystemAccountId,
      scopeType: 'group_authorization_team',
      scopeId: `${authorization.resourceId}:${teamId}`,
      range
    })
  }
  const rows = loadAuthorizationTeamRuntimeRows(authorization, teamId)
  const scopes = accountAuthorizationTeamUsageScopes(rows, teamId, loadAccountAuthorizationInstanceAccountIds(rows))
  const summaries = loadAuthorizationUsageRangeSummariesForScopes(scopes, 'account_authorization_team', range)
  return sumUsageSummariesForScopes(scopes, summaries)
}

async function loadAuthorizationTeamUsageFallbackSummaryAsync(
  authorization: ResourceAuthorizationSummary,
  teamId: string,
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Promise<AccountUsageSummary> {
  if (authorization.resourceType === 'group') {
    return loadUsageRangeSummaryForScopeAsync({
      systemAccountId: authorization.resourceOwnerSystemAccountId,
      scopeType: 'group_authorization_team',
      scopeId: `${authorization.resourceId}:${teamId}`,
      range
    })
  }
  const client = await getResourceAuthorizationUsageBusinessClient()
  const rows = await loadAuthorizationTeamRuntimeRowsAsync(client, authorization, teamId)
  const scopes = accountAuthorizationTeamUsageScopes(rows, teamId, await loadAccountAuthorizationInstanceAccountIdsAsync(client, rows))
  const summaries = await loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, 'account_authorization_team', range)
  return sumUsageSummariesForScopes(scopes, summaries)
}

function loadAuthorizationTeamRuntimeRows(authorization: ResourceAuthorizationSummary, teamId: string): ResourceAuthorizationRow[] {
  return getBusinessDatabase().prepare(`
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
  `).all(
    teamId,
    authorization.resourceType,
    authorization.resourceId,
    authorization.resourceOwnerSystemAccountId
  ) as unknown as ResourceAuthorizationRow[]
}

async function loadAuthorizationTeamRuntimeRowsAsync(client: DatabaseClient, authorization: ResourceAuthorizationSummary, teamId: string): Promise<ResourceAuthorizationRow[]> {
  return client.query<ResourceAuthorizationRow>(`
    SELECT DISTINCT ${resourceAuthorizationSelectColumns('ra')}
    FROM ${resourceAuthorizationUsageBusinessTable(client, 'resource_authorizations')} ra
    INNER JOIN ${resourceAuthorizationUsageBusinessTable(client, 'resource_authorization_sources')} ras
      ON ras.authorization_id = ra.id
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ra.resource_owner_system_account_id = ?
    ORDER BY ra.created_at ASC, ra.id ASC
  `, [
    teamId,
    authorization.resourceType,
    authorization.resourceId,
    authorization.resourceOwnerSystemAccountId
  ])
}

function loadAccountAuthorizationInstanceAccountIds(rows: ResourceAuthorizationRow[]): Map<string, string> {
  const result = new Map<string, string>()
  const ids = [...new Set(rows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return result
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 400)) {
    const lookupRows = database.prepare(`
      SELECT authorization_instance_authorization_id, id
      FROM accounts
      WHERE authorization_instance_authorization_id IN (${sqlPlaceholders(chunk.length)})
        AND deleted_at IS NULL
    `).all(...chunk) as unknown as Array<{ authorization_instance_authorization_id?: string | null; id?: string | null }>
    for (const row of lookupRows) {
      if (row.authorization_instance_authorization_id && row.id) {
        result.set(row.authorization_instance_authorization_id, row.id)
      }
    }
  }
  return result
}

async function loadAccountAuthorizationInstanceAccountIdsAsync(client: DatabaseClient, rows: ResourceAuthorizationRow[]): Promise<Map<string, string>> {
  const result = new Map<string, string>()
  const ids = [...new Set(rows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return result
  for (const chunk of chunkValues(ids, 400)) {
    const lookupRows = await client.query<{ authorization_instance_authorization_id?: string | null; id?: string | null }>(`
      SELECT authorization_instance_authorization_id, id
      FROM ${resourceAuthorizationUsageBusinessTable(client, 'accounts')}
      WHERE authorization_instance_authorization_id IN (${sqlPlaceholders(chunk.length)})
        AND deleted_at IS NULL
    `, chunk)
    for (const row of lookupRows) {
      if (row.authorization_instance_authorization_id && row.id) {
        result.set(row.authorization_instance_authorization_id, row.id)
      }
    }
  }
  return result
}

function accountAuthorizationTeamUsageScopes(rows: ResourceAuthorizationRow[], teamId: string, instanceAccountIds: Map<string, string>): UsageSummaryScopeRequest[] {
  return rows.flatMap((row) => {
    const systemAccountId = row.grantee_system_account_id
    const instanceAccountId = instanceAccountIds.get(row.id)
    if (!systemAccountId || !instanceAccountId) return []
    return [usageScope(row.id, systemAccountId, `${instanceAccountId}:${teamId}`)]
  })
}

function sumUsageSummariesForScopes(scopes: UsageSummaryScopeRequest[], summaries: Map<string, AccountUsageSummary>): AccountUsageSummary {
  let total: AccountUsageSummary | undefined
  for (const scope of scopes) {
    total = addUsageSummaries(total, summaries.get(scope.rowKey))
  }
  return total ?? emptyAccountUsageSummary()
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

async function buildRuntimeAuthorizationUsageDetailsAsync(
  authorization: ResourceAuthorizationSummary,
  rows: ResourceAuthorizationRow[],
  range: AccountUsageStatsRange
): Promise<ResourceAuthorizationUsageDetail[]> {
  if (!rows.length) return []
  const client = await getResourceAuthorizationUsageBusinessClient()
  const scopes = rows.map((row) => usageScope(
    row.id,
    authorizationUsageStatsSystemAccountId(row.resource_type, row.resource_owner_system_account_id, row.grantee_system_account_id ?? undefined),
    row.id
  ))
  const usageByAuthorization = authorization.resourceType === 'account'
    ? await loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, 'account_authorization', range)
    : await loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, 'group_authorization', range)
  const accounts = await loadSystemAccountPrincipalMapByIdsAsync(client, rows.map((row) => row.grantee_system_account_id ?? ''))
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
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd AS total_cost, last_used_at
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

async function loadAuthorizationTeamUsageRangeSummaryAsync(
  authorization: ResourceAuthorizationSummary,
  teamId: string,
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Promise<AccountUsageSummary | undefined> {
  const client = await getResourceAuthorizationUsageStatsClient()
  const row = await client.one<Parameters<typeof usageSummaryFromAggregate>[0]>(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
      CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, CAST(total_cost_usd AS double precision) AS total_cost, last_used_at
    FROM ${resourceAuthorizationUsageStatsTable(client, 'authorization_team_usage_range_windows')}
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `, [
    authorization.resourceOwnerSystemAccountId,
    range.startDate,
    range.endDate,
    teamId,
    authorization.resourceType,
    authorization.resourceId
  ])
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

async function getResourceAuthorizationUsageBusinessClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

async function getResourceAuthorizationUsageStatsClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function resourceAuthorizationUsageBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function resourceAuthorizationUsageStatsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(statsSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
