import type {
  AccountStatus,
  AccountSummary,
  AccountType,
  AccountUsageStatsRange,
  AccountUsageStatsOverview,
  AccountUsageStatsRow,
  AccountUsageStatsTrendOverview
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { dateKey, emptyAccountUsageSummary, usageStatsTimezone, usageStatsTimezoneAsync, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { GLOBAL_STATS_SCOPE_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { hotUsageStatsRanges, rangeWindowKey } from './usage-stats-window-helpers.js'
import { loadUsageDailySeriesForScopeRequests, loadUsageDailySeriesForScopeRequestsAsync, type UsageStatsDailySeries } from './usage-window-loaders.js'
import { registerUsageRangeWindowRequest, registerUsageRangeWindowRequestAsync } from './usage-range-window-requests.repository.js'

const accountUsageBusinessDatabaseAlias = 'account_usage_business'
const accountUsageMaxListWindowRows = 1001

export function getAccountUsageStatsOverview(input: {
  access?: AccessScope
  accounts: AccountSummary[]
  total?: number
  page?: number
  pageSize?: number
  range: AccountUsageStatsRange
  defaultTrendAccountIds?: string[]
  loadUsageDailySeries: (scopes: UsageScopeRequest[], range: AccountUsageStatsRange) => Map<string, UsageStatsDailySeries>
}): AccountUsageStatsOverview {
  const scopes = input.accounts.map((account) => accountUsageScope(account, input.access))
  const dailySeriesByRowKey = input.loadUsageDailySeries(scopes, input.range)
  const rows = input.accounts.map((account): AccountUsageStatsRow => {
    const rowKey = accountUsageStatsRowKey(account)
    const dailySeries = dailySeriesByRowKey.get(rowKey)
    return {
      id: account.id,
      systemAccountId: account.systemAccountId,
      systemAccountName: account.systemAccountName,
      ownerSystemAccountId: requiredAccountUsageOwnerSystemAccountId(account),
      ownerSystemAccountName: account.ownerSystemAccountName,
      providerCode: account.providerCode,
      name: account.name,
      type: account.type,
      status: account.status,
      accessType: account.accessType,
      rangeUsage: dailySeries?.rangeUsage ?? emptyAccountUsageSummary(),
      dailyUsage: dailySeries?.dailyUsage ?? [],
      authorizationUsageAvailable: account.authorizationUsageAvailable === true,
      authorizationCount: account.authorizationCount ?? 0,
      authorizationTeamCount: account.authorizationTeamCount ?? 0
    }
  })
  return {
    range: input.range,
    summary: loadAccountUsageOverviewSummary(input.access, input.range),
    rows,
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: input.total ?? input.accounts.length,
    hasMore: false,
    page: input.page ?? 1,
    pageSize: input.pageSize ?? input.accounts.length
  }
}

export interface AccountUsageStatsPageOptions {
  access?: AccessScope
  range: AccountUsageStatsRange
  page: number
  pageSize: number
  keyword?: string
  type?: string
  accountIds?: string[]
  defaultTrendAccountIds?: string[]
}

type AccountUsageScopeType = 'account' | 'caller_account'

interface AccountUsageStatsSourceRow {
  scope_id: string
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

interface AccountUsageMetadataRow {
  id: string
  system_account_id: string
  system_account_name: string | null
  provider_code: string
  name: string
  type: AccountType
  status: AccountStatus
  access_type: 'owner' | 'authorized'
  authorization_id: string | null
}

const accountUsageSelectedAccountLimit = 50

export function getAccountUsageStatsOverviewPageFromWindows(input: AccountUsageStatsPageOptions): AccountUsageStatsOverview {
  const pageSize = Math.max(1, Math.min(Math.trunc(input.pageSize), 200))
  const maxPage = Math.max(1, Math.floor((accountUsageMaxListWindowRows - 1) / pageSize))
  const page = Math.min(maxPage, Math.max(1, Math.trunc(input.page)))
  const usageScope = accountUsageListScope(input.access)
  const database = getStatsDatabase()
  const filter = accountUsageFilterPredicate(input, usageScope.scopeType, database)
  const windowKey = rangeWindowKey(input.range)
  registerColdUsageScopeRangeWindowRequest(input, usageScope, usageStatsTimezone())
  const rows = database.prepare(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      usage_window.cache_read_cost_usd,
      usage_window.cache_write_tokens,
      usage_window.cache_write_1h_tokens,
      usage_window.cache_write_cost_usd,
      usage_window.thinking_tokens,
      usage_window.input_image_tokens,
      usage_window.output_image_tokens,
      usage_window.total_cost_usd AS total_cost,
      usage_window.last_used_at
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.window_key = ?
      AND (
        usage_window.request_count > 0
        OR usage_window.input_tokens > 0
        OR usage_window.output_tokens > 0
        OR usage_window.cache_read_tokens > 0
        OR usage_window.total_cost_usd > 0
        OR usage_window.last_used_at IS NOT NULL
      )
      ${filter.sql}
    ORDER BY usage_window.request_count DESC, usage_window.total_cost_usd DESC, (usage_window.input_tokens + usage_window.output_tokens) DESC, usage_window.last_used_at DESC, usage_window.scope_id ASC
    LIMIT ? OFFSET ?
  `).all(
    usageScope.systemAccountId,
    usageScope.scopeType,
    windowKey,
    ...filter.params,
    pageSize + 1,
    (page - 1) * pageSize
  ) as unknown as AccountUsageStatsSourceRow[]
  const pageRows = takePageRows(rows, pageSize)
  const selectedRows = loadSelectedAccountUsageRows({
    database,
    excludeAccountIds: pageRows.rows.map((row) => row.scope_id),
    input,
    usageScope
  })
  const sourceRows = mergeAccountUsageSourceRows(pageRows.rows, selectedRows)
  const metadataRows = loadAccountUsageMetadataRows(input.access, sourceRows.map((row) => row.scope_id), usageScope.scopeType)
  const metadataById = new Map(metadataRows.map((row) => [row.id, row]))
  const overviewRows = sourceRows.flatMap((row): AccountUsageStatsRow[] => {
    const metadata = metadataById.get(row.scope_id)
    if (!metadata) return []
    const rangeUsage = usageSummaryFromAggregate(row)
    return [{
      id: metadata.id,
      systemAccountId: canAccessAll(input.access) ? metadata.system_account_id : undefined,
      systemAccountName: canAccessAll(input.access) ? metadata.system_account_name ?? undefined : undefined,
      ownerSystemAccountId: metadata.system_account_id,
      ownerSystemAccountName: metadata.system_account_name ?? undefined,
      providerCode: metadata.provider_code,
      name: metadata.name,
      type: metadata.type,
      status: metadata.status,
      accessType: metadata.access_type,
      rangeUsage,
      dailyUsage: [],
      authorizationUsageAvailable: false,
      authorizationCount: 0,
      authorizationTeamCount: 0
    }]
  })

  return {
    range: input.range,
    summary: loadAccountUsageOverviewSummary(input.access, input.range),
    rows: overviewRows,
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: Math.max(pagedTotalUpperBound(page, pageSize, pageRows.rows.length, pageRows.hasMore), (page - 1) * pageSize + overviewRows.length),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function getAccountUsageStatsOverviewPageFromWindowsAsync(input: AccountUsageStatsPageOptions): Promise<AccountUsageStatsOverview> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountUsageStatsOverviewPageFromWindows(input)
  }
  const pageSize = Math.max(1, Math.min(Math.trunc(input.pageSize), 200))
  const maxPage = Math.max(1, Math.floor((accountUsageMaxListWindowRows - 1) / pageSize))
  const page = Math.min(maxPage, Math.max(1, Math.trunc(input.page)))
  const usageScope = accountUsageListScope(input.access)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const filter = await accountUsageFilterPredicateAsync(input, usageScope.scopeType, client)
  const windowKey = rangeWindowKey(input.range)
  await registerColdUsageScopeRangeWindowRequestAsync(client, input, usageScope, await usageStatsTimezoneAsync())
  const rows = await client.query<AccountUsageStatsSourceRow>(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      CAST(usage_window.cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      usage_window.cache_write_tokens,
      usage_window.cache_write_1h_tokens,
      CAST(usage_window.cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      usage_window.thinking_tokens,
      usage_window.input_image_tokens,
      usage_window.output_image_tokens,
      CAST(usage_window.total_cost_usd AS double precision) AS total_cost,
      usage_window.last_used_at
    FROM ${accountUsageStatsTable(client, 'usage_scope_range_windows')} usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.window_key = ?
      AND (
        usage_window.request_count > 0
        OR usage_window.input_tokens > 0
        OR usage_window.output_tokens > 0
        OR usage_window.cache_read_tokens > 0
        OR usage_window.total_cost_usd > 0
        OR usage_window.last_used_at IS NOT NULL
      )
      ${filter.sql}
    ORDER BY usage_window.request_count DESC, usage_window.total_cost_usd DESC, (usage_window.input_tokens + usage_window.output_tokens) DESC, usage_window.last_used_at DESC, usage_window.scope_id ASC
    LIMIT ? OFFSET ?
  `, [
    usageScope.systemAccountId,
    usageScope.scopeType,
    windowKey,
    ...filter.params,
    pageSize + 1,
    (page - 1) * pageSize
  ])
  const pageRows = takePageRows(rows, pageSize)
  const selectedRows = await loadSelectedAccountUsageRowsAsync({
    client,
    excludeAccountIds: pageRows.rows.map((row) => row.scope_id),
    input,
    usageScope
  })
  const sourceRows = mergeAccountUsageSourceRows(pageRows.rows, selectedRows)
  const metadataRows = await loadAccountUsageMetadataRowsAsync(client, input.access, sourceRows.map((row) => row.scope_id), usageScope.scopeType)
  const metadataById = new Map(metadataRows.map((row) => [row.id, row]))
  const summary = await loadAccountUsageOverviewSummaryAsync(client, input.access, input.range)
  const overviewRows = sourceRows.flatMap((row): AccountUsageStatsRow[] => {
    const metadata = metadataById.get(row.scope_id)
    if (!metadata) return []
    const rangeUsage = usageSummaryFromAggregate(row)
    return [{
      id: metadata.id,
      systemAccountId: canAccessAll(input.access) ? metadata.system_account_id : undefined,
      systemAccountName: canAccessAll(input.access) ? metadata.system_account_name ?? undefined : undefined,
      ownerSystemAccountId: metadata.system_account_id,
      ownerSystemAccountName: metadata.system_account_name ?? undefined,
      providerCode: metadata.provider_code,
      name: metadata.name,
      type: metadata.type,
      status: metadata.status,
      accessType: metadata.access_type,
      rangeUsage,
      dailyUsage: [],
      authorizationUsageAvailable: false,
      authorizationCount: 0,
      authorizationTeamCount: 0
    }]
  })

  return {
    range: input.range,
    summary,
    rows: overviewRows,
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: Math.max(pagedTotalUpperBound(page, pageSize, pageRows.rows.length, pageRows.hasMore), (page - 1) * pageSize + overviewRows.length),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function getAccountUsageStatsTrendAsync(
  access: AccessScope | undefined,
  range: AccountUsageStatsRange,
  accountIds: string[]
): Promise<AccountUsageStatsTrendOverview> {
  const ids = [...new Set(accountIds.filter(Boolean))].slice(0, 10)
  if (!ids.length) return { range, rows: [] }
  const input: AccountUsageStatsPageOptions = { access, range, page: 1, pageSize: 10, accountIds: ids }
  const usageScope = accountUsageListScope(access)

  if (runtimeConfig.databaseDriver !== 'postgres') {
    const sourceRows = loadSelectedAccountUsageRows({
      database: getStatsDatabase(),
      excludeAccountIds: [],
      input,
      usageScope
    })
    const metadataRows = loadAccountUsageMetadataRows(access, sourceRows.map((row) => row.scope_id), usageScope.scopeType)
    return accountUsageTrendOverview(range, usageScope, metadataRows, loadUsageDailySeriesForScopeRequests(
      accountUsageTrendScopes(usageScope, metadataRows),
      range
    ), access)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const sourceRows = await loadSelectedAccountUsageRowsAsync({ client, excludeAccountIds: [], input, usageScope })
  const metadataRows = await loadAccountUsageMetadataRowsAsync(client, access, sourceRows.map((row) => row.scope_id), usageScope.scopeType)
  const dailySeries = await loadUsageDailySeriesForScopeRequestsAsync(accountUsageTrendScopes(usageScope, metadataRows), range)
  return accountUsageTrendOverview(range, usageScope, metadataRows, dailySeries, access)
}

function accountUsageTrendScopes(
  usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType },
  metadataRows: AccountUsageMetadataRow[]
): UsageScopeRequest[] {
  return metadataRows.map((metadata) => ({
    rowKey: metadata.id,
    systemAccountId: usageScope.systemAccountId,
    scopeType: usageScope.scopeType,
    scopeId: metadata.id
  }))
}

function accountUsageTrendOverview(
  range: AccountUsageStatsRange,
  _usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType },
  metadataRows: AccountUsageMetadataRow[],
  dailySeries: Map<string, UsageStatsDailySeries>,
  access: AccessScope | undefined
): AccountUsageStatsTrendOverview {
  return {
    range,
    rows: metadataRows.map((metadata) => ({
      id: metadata.id,
      systemAccountId: canAccessAll(access) ? metadata.system_account_id : undefined,
      systemAccountName: canAccessAll(access) ? metadata.system_account_name ?? undefined : undefined,
      ownerSystemAccountId: metadata.system_account_id,
      ownerSystemAccountName: metadata.system_account_name ?? undefined,
      providerCode: metadata.provider_code,
      name: metadata.name,
      accessType: metadata.access_type,
      dailyUsage: dailySeries.get(metadata.id)?.dailyUsage ?? []
    }))
  }
}

function loadSelectedAccountUsageRows(input: {
  database: ReturnType<typeof getStatsDatabase>
  excludeAccountIds: string[]
  input: AccountUsageStatsPageOptions
  usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType }
}): AccountUsageStatsSourceRow[] {
  const excludedIds = new Set(input.excludeAccountIds)
  const accountIds = [...new Set((input.input.accountIds ?? []).filter((id) => id && !excludedIds.has(id)))].slice(0, accountUsageSelectedAccountLimit)
  if (!accountIds.length) return []
  const accountFilter = buildAccountUsageScopeIdFilter(accountIds)
  const windowKey = rangeWindowKey(input.input.range)
  return input.database.prepare(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      usage_window.cache_read_cost_usd,
      usage_window.cache_write_tokens,
      usage_window.cache_write_1h_tokens,
      usage_window.cache_write_cost_usd,
      usage_window.thinking_tokens,
      usage_window.input_image_tokens,
      usage_window.output_image_tokens,
      usage_window.total_cost_usd AS total_cost,
      usage_window.last_used_at
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.window_key = ?
      AND ${accountFilter.sql}
    ORDER BY usage_window.request_count DESC, usage_window.total_cost_usd DESC, (usage_window.input_tokens + usage_window.output_tokens) DESC, usage_window.last_used_at DESC, usage_window.scope_id ASC
  `).all(
    input.usageScope.systemAccountId,
    input.usageScope.scopeType,
    windowKey,
    ...accountFilter.params
  ) as unknown as AccountUsageStatsSourceRow[]
}

async function loadSelectedAccountUsageRowsAsync(input: {
  client: DatabaseClient
  excludeAccountIds: string[]
  input: AccountUsageStatsPageOptions
  usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType }
}): Promise<AccountUsageStatsSourceRow[]> {
  const excludedIds = new Set(input.excludeAccountIds)
  const accountIds = [...new Set((input.input.accountIds ?? []).filter((id) => id && !excludedIds.has(id)))].slice(0, accountUsageSelectedAccountLimit)
  if (!accountIds.length) return []
  const accountFilter = buildAccountUsageScopeIdFilter(accountIds)
  const windowKey = rangeWindowKey(input.input.range)
  return await input.client.query<AccountUsageStatsSourceRow>(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      CAST(usage_window.cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
      usage_window.cache_write_tokens,
      usage_window.cache_write_1h_tokens,
      CAST(usage_window.cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
      usage_window.thinking_tokens,
      usage_window.input_image_tokens,
      usage_window.output_image_tokens,
      CAST(usage_window.total_cost_usd AS double precision) AS total_cost,
      usage_window.last_used_at
    FROM ${accountUsageStatsTable(input.client, 'usage_scope_range_windows')} usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.window_key = ?
      AND ${accountFilter.sql}
    ORDER BY usage_window.request_count DESC, usage_window.total_cost_usd DESC, (usage_window.input_tokens + usage_window.output_tokens) DESC, usage_window.last_used_at DESC, usage_window.scope_id ASC
  `, [
    input.usageScope.systemAccountId,
    input.usageScope.scopeType,
    windowKey,
    ...accountFilter.params
  ])
}

function mergeAccountUsageSourceRows(pageRows: AccountUsageStatsSourceRow[], selectedRows: AccountUsageStatsSourceRow[]): AccountUsageStatsSourceRow[] {
  const seen = new Set<string>()
  const merged: AccountUsageStatsSourceRow[] = []
  for (const row of [...pageRows, ...selectedRows]) {
    if (seen.has(row.scope_id)) continue
    seen.add(row.scope_id)
    merged.push(row)
  }
  return merged
}

function emptyAccountUsageStatsOverview(input: AccountUsageStatsPageOptions, page: number, pageSize: number): AccountUsageStatsOverview {
  return {
    range: input.range,
    summary: loadAccountUsageOverviewSummary(input.access, input.range),
    rows: [],
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: 0,
    hasMore: false,
    page,
    pageSize
  }
}

function loadAccountUsageOverviewSummary(access: AccessScope | undefined, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>) {
  const scope = accountUsageOverviewSummaryScope(access)
  const windowKey = rangeWindowKey(range)
  const row = getStatsDatabase().prepare(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd AS total_cost, last_used_at
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND window_key = ?
  `).get(scope.systemAccountId, scope.scopeId, windowKey) as unknown as {
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
  } | undefined
  return row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
}

async function loadAccountUsageOverviewSummaryAsync(client: DatabaseClient, access: AccessScope | undefined, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>) {
  const scope = accountUsageOverviewSummaryScope(access)
  const windowKey = rangeWindowKey(range)
  const row = await client.one<{
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
  }>(`
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
    FROM ${accountUsageStatsTable(client, 'usage_scope_range_windows')}
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND window_key = ?
  `, [scope.systemAccountId, scope.scopeId, windowKey])
  return row ? usageSummaryFromAggregate(row) : emptyAccountUsageSummary()
}

function accountUsageOverviewSummaryScope(access?: AccessScope): { systemAccountId: string; scopeId: string } {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeId: scopedId }
  }
  if (canAccessAll(access)) {
    return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID }
  }
  const systemAccountId = currentSystemAccountId(access)
  return { systemAccountId, scopeId: systemAccountId }
}

function registerColdUsageScopeRangeWindowRequest(
  input: AccountUsageStatsPageOptions,
  usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType },
  timezone: string
): void {
  if (isCurrentHotUsageRange(input.range, timezone)) return
  registerUsageRangeWindowRequest({
    domain: 'usage_scope',
    systemAccountId: usageScope.systemAccountId,
    scopeType: usageScope.scopeType,
    scopeId: '*',
    startDate: input.range.startDate,
    endDate: input.range.endDate
  })
}

async function registerColdUsageScopeRangeWindowRequestAsync(
  client: DatabaseClient,
  input: AccountUsageStatsPageOptions,
  usageScope: { systemAccountId: string; scopeType: AccountUsageScopeType },
  timezone: string
): Promise<void> {
  if (isCurrentHotUsageRange(input.range, timezone)) return
  await registerUsageRangeWindowRequestAsync(client, {
    domain: 'usage_scope',
    systemAccountId: usageScope.systemAccountId,
    scopeType: usageScope.scopeType,
    scopeId: '*',
    startDate: input.range.startDate,
    endDate: input.range.endDate
  })
}

function isCurrentHotUsageRange(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>, timezone: string): boolean {
  const todayKey = dateKey(new Date(), timezone)
  return hotUsageStatsRanges(timezone, todayKey).some((hotRange) => hotRange.startDate === range.startDate && hotRange.endDate === range.endDate)
}

export interface UsageScopeRequest {
  rowKey: string
  systemAccountId: string
  scopeType: string
  scopeId: string
}

export function accountUsageStatsRowKey(account: Pick<AccountSummary, 'id' | 'accountAuthorizationId'>): string {
  return `${account.id}:${account.accountAuthorizationId ?? 'owner'}`
}

function accountUsageScope(account: AccountSummary, access?: AccessScope): UsageScopeRequest {
  const callerSystemAccountId = scopedSystemAccountId(access)
  if (callerSystemAccountId) {
    return {
      rowKey: accountUsageStatsRowKey(account),
      systemAccountId: callerSystemAccountId,
      scopeType: 'caller_account',
      scopeId: account.id
    }
  }

  const systemAccountId = account.accessType === 'authorized' && account.accountAuthorizationId
    ? requiredAuthorizedAccountUsageSystemAccountId(account)
    : requiredAccountUsageOwnerSystemAccountId(account)
  return {
    rowKey: accountUsageStatsRowKey(account),
    systemAccountId,
    scopeType: account.accessType === 'authorized' && account.accountAuthorizationId ? 'account_authorization' : 'account',
    scopeId: account.accessType === 'authorized' && account.accountAuthorizationId ? account.accountAuthorizationId : account.id
  }
}

function requiredAccountUsageOwnerSystemAccountId(account: AccountSummary): string {
  const ownerSystemAccountId = account.ownerSystemAccountId?.trim()
  if (ownerSystemAccountId) return ownerSystemAccountId
  if (account.accessType !== 'authorized') {
    const systemAccountId = account.systemAccountId?.trim()
    if (systemAccountId) return systemAccountId
  }
  throw new Error('账户归属数据异常，无法统计用量')
}

function requiredAuthorizedAccountUsageSystemAccountId(account: AccountSummary): string {
  const systemAccountId = account.systemAccountId?.trim()
  if (systemAccountId) return systemAccountId
  throw new Error('授权账户归属数据异常，无法统计用量')
}

function accountUsageListScope(access?: AccessScope): { systemAccountId: string; scopeType: AccountUsageScopeType } {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeType: 'caller_account' }
  }
  return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account' }
}

function accountUsageFilterPredicate(
  input: Pick<AccountUsageStatsPageOptions, 'keyword' | 'type' | 'access'>,
  scopeType: AccountUsageScopeType,
  database: ReturnType<typeof getStatsDatabase>
): { sql: string; params: string[] } {
  const type = input.type?.trim()
  const normalizedType = type && type !== 'all' ? type : undefined
  const keyword = input.keyword?.trim()
  if (scopeType === 'account' && !normalizedType && !keyword) {
    return { sql: '', params: [] }
  }
  if (keyword) {
    const accountIds = loadAccountUsageKeywordAccountIds({
      access: input.access,
      keyword,
      scopeType,
      type: normalizedType
    })
    if (!accountIds.length) {
      return { sql: 'AND 0 = 1', params: [] }
    }
    const scopeFilter = buildAccountUsageScopeIdFilter(accountIds)
    return {
      sql: `AND ${scopeFilter.sql}`,
      params: scopeFilter.params
    }
  }

  const clauses: string[] = []
  const params: string[] = []
  if (!normalizedType) {
    return { sql: '', params }
  }
  const accountsTable = `${accountUsageBusinessDatabaseAlias}.accounts`
  ensureAccountUsageBusinessDatabaseAttached(database)
  clauses.push('accounts.id = usage_window.scope_id')
  if (normalizedType) {
    clauses.push('accounts.type = ?')
    params.push(normalizedType)
  }
  return {
    sql: `AND EXISTS (SELECT 1 FROM ${accountsTable} accounts WHERE ${clauses.join(' AND ')})`,
    params
  }
}

async function accountUsageFilterPredicateAsync(
  input: Pick<AccountUsageStatsPageOptions, 'keyword' | 'type' | 'access'>,
  scopeType: AccountUsageScopeType,
  client: DatabaseClient
): Promise<{ sql: string; params: string[] }> {
  const type = input.type?.trim()
  const normalizedType = type && type !== 'all' ? type : undefined
  const keyword = input.keyword?.trim()
  if (scopeType === 'account' && !normalizedType && !keyword) {
    return { sql: '', params: [] }
  }
  if (keyword) {
    const accountIds = await loadAccountUsageKeywordAccountIdsAsync(client, {
      access: input.access,
      keyword,
      scopeType,
      type: normalizedType
    })
    if (!accountIds.length) {
      return { sql: 'AND 0 = 1', params: [] }
    }
    const scopeFilter = buildAccountUsageScopeIdFilter(accountIds)
    return {
      sql: `AND ${scopeFilter.sql}`,
      params: scopeFilter.params
    }
  }

  if (!normalizedType) {
    return { sql: '', params: [] }
  }
  return {
    sql: `AND EXISTS (
      SELECT 1
      FROM ${accountUsageBusinessTable(client, 'accounts')} accounts
      WHERE accounts.id = usage_window.scope_id
        AND accounts.type = ?
    )`,
    params: [normalizedType]
  }
}

function loadAccountUsageKeywordAccountIds(input: {
  access?: AccessScope
  keyword: string
  scopeType: AccountUsageScopeType
  type?: string
}): string[] {
  const keyword = input.keyword.trim()
  if (!keyword) return []
  const database = getBusinessDatabase()
  const ids: string[] = []
  const clauses: string[] = []
  const params: string[] = []
  const viewerSystemAccountId = scopedSystemAccountId(input.access) ?? currentSystemAccountId(input.access)
  const keywordUpperBound = accountUsageKeywordUpperBound(keyword)
  clauses.push(`(
    (accounts.name >= ? AND accounts.name < ?)
    OR (accounts.provider_code >= ? AND accounts.provider_code < ?)
    OR (accounts.type >= ? AND accounts.type < ?)
    OR EXISTS (
      SELECT 1
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND (groups.name >= ? AND groups.name < ?)
    )
  )`)
  params.push(
    keyword,
    keywordUpperBound,
    keyword,
    keywordUpperBound,
    keyword,
    keywordUpperBound,
    viewerSystemAccountId,
    keyword,
    keywordUpperBound
  )
  if (input.type) {
    clauses.push('accounts.type = ?')
    params.push(input.type)
  }
  if (input.scopeType === 'caller_account') {
    clauses.push(`(
      accounts.system_account_id = ?
      OR EXISTS (
        SELECT 1
        FROM resource_authorizations visible_authorization
        WHERE visible_authorization.resource_type = 'account'
          AND visible_authorization.resource_id = accounts.id
          AND visible_authorization.grantee_system_account_id = ?
          AND visible_authorization.status = 'active'
          AND (visible_authorization.expires_at IS NULL OR visible_authorization.expires_at > ?)
      )
      OR EXISTS (
        SELECT 1
        FROM group_accounts visible_group_account
        INNER JOIN resource_authorizations visible_group_authorization
          ON visible_group_authorization.resource_type = 'group'
          AND visible_group_authorization.resource_id = visible_group_account.group_id
          AND visible_group_authorization.grantee_system_account_id = ?
          AND visible_group_authorization.status = 'active'
          AND (visible_group_authorization.expires_at IS NULL OR visible_group_authorization.expires_at > ?)
        WHERE visible_group_account.account_id = accounts.id
          AND visible_group_account.enabled = 1
      )
    )`)
    params.push(viewerSystemAccountId, viewerSystemAccountId, nowIso(), viewerSystemAccountId, nowIso())
  }
  appendAccountUsageAccountIds(ids, database
    .prepare(`
      SELECT accounts.id
      FROM accounts
      WHERE ${clauses.join(' AND ')}
      ORDER BY accounts.name ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(...params, accountUsageSelectedAccountLimit) as unknown as Array<{ id?: string }>)
  appendAccountUsageAccountIds(ids, loadAccountUsageAuthorizedInstanceIdsForSourceKeyword(database, {
    keyword,
    keywordUpperBound,
    scopeType: input.scopeType,
    type: input.type,
    viewerSystemAccountId
  }))
  if (input.scopeType === 'caller_account') {
    appendAccountUsageAccountIds(ids, loadAccountUsageGroupAuthorizedAccountIdsForKeyword(database, {
      keyword,
      keywordUpperBound,
      type: input.type,
      viewerSystemAccountId
    }))
  }
  return ids
}

async function loadAccountUsageKeywordAccountIdsAsync(client: DatabaseClient, input: {
  access?: AccessScope
  keyword: string
  scopeType: AccountUsageScopeType
  type?: string
}): Promise<string[]> {
  const keyword = input.keyword.trim()
  if (!keyword) return []
  const ids: string[] = []
  const clauses: string[] = []
  const params: string[] = []
  const viewerSystemAccountId = scopedSystemAccountId(input.access) ?? currentSystemAccountId(input.access)
  const keywordPrefix = normalizeAccountUsageKeyword(keyword)
  const keywordUpperBound = accountUsageKeywordUpperBound(keywordPrefix)
  clauses.push(`(
    (accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?))
    OR (accounts.provider_code COLLATE "C" >= ? AND accounts.provider_code COLLATE "C" < ? AND starts_with(accounts.provider_code, ?))
    OR (accounts.type COLLATE "C" >= ? AND accounts.type COLLATE "C" < ? AND starts_with(accounts.type, ?))
    OR EXISTS (
      SELECT 1
      FROM ${accountUsageBusinessTable(client, 'group_accounts')} group_accounts
      INNER JOIN ${accountUsageBusinessTable(client, 'groups')} groups
        ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND groups.name COLLATE "C" >= ? AND groups.name COLLATE "C" < ? AND starts_with(groups.name, ?)
    )
  )`)
  params.push(
    keywordPrefix,
    keywordUpperBound,
    keywordPrefix,
    keywordPrefix,
    keywordUpperBound,
    keywordPrefix,
    keywordPrefix,
    keywordUpperBound,
    keywordPrefix,
    viewerSystemAccountId,
    keywordPrefix,
    keywordUpperBound,
    keywordPrefix
  )
  if (input.type) {
    clauses.push('accounts.type = ?')
    params.push(input.type)
  }
  if (input.scopeType === 'caller_account') {
    clauses.push(`(
      accounts.system_account_id = ?
      OR EXISTS (
        SELECT 1
        FROM ${accountUsageBusinessTable(client, 'resource_authorizations')} visible_authorization
        WHERE visible_authorization.resource_type = 'account'
          AND visible_authorization.resource_id = accounts.id
          AND visible_authorization.grantee_system_account_id = ?
          AND visible_authorization.status = 'active'
          AND (visible_authorization.expires_at IS NULL OR visible_authorization.expires_at > ?)
      )
      OR EXISTS (
        SELECT 1
        FROM ${accountUsageBusinessTable(client, 'group_accounts')} visible_group_account
        INNER JOIN ${accountUsageBusinessTable(client, 'resource_authorizations')} visible_group_authorization
          ON visible_group_authorization.resource_type = 'group'
          AND visible_group_authorization.resource_id = visible_group_account.group_id
          AND visible_group_authorization.grantee_system_account_id = ?
          AND visible_group_authorization.status = 'active'
          AND (visible_group_authorization.expires_at IS NULL OR visible_group_authorization.expires_at > ?)
        WHERE visible_group_account.account_id = accounts.id
          AND visible_group_account.enabled = 1
      )
    )`)
    params.push(viewerSystemAccountId, viewerSystemAccountId, nowIso(), viewerSystemAccountId, nowIso())
  }
  appendAccountUsageAccountIds(ids, await client.query<{ id?: string }>(`
    SELECT accounts.id
    FROM ${accountUsageBusinessTable(client, 'accounts')} accounts
    WHERE ${clauses.join(' AND ')}
    ORDER BY accounts.name COLLATE "C" ASC, accounts.id ASC
    LIMIT ?
  `, [...params, accountUsageSelectedAccountLimit]))
  appendAccountUsageAccountIds(ids, await loadAccountUsageAuthorizedInstanceIdsForSourceKeywordAsync(client, {
    keywordPrefix,
    keywordUpperBound,
    scopeType: input.scopeType,
    type: input.type,
    viewerSystemAccountId
  }))
  if (input.scopeType === 'caller_account') {
    appendAccountUsageAccountIds(ids, await loadAccountUsageGroupAuthorizedAccountIdsForKeywordAsync(client, {
      keywordPrefix,
      keywordUpperBound,
      type: input.type,
      viewerSystemAccountId
    }))
  }
  return ids
}

function loadAccountUsageAuthorizedInstanceIdsForSourceKeyword(
  database: ReturnType<typeof getBusinessDatabase>,
  input: {
    keyword: string
    keywordUpperBound: string
    scopeType: AccountUsageScopeType
    type?: string
    viewerSystemAccountId: string
  }
): Array<{ id?: string }> {
  const clauses = ['source_accounts.name >= ? AND source_accounts.name < ?']
  const params: string[] = [input.keyword, input.keywordUpperBound]
  if (input.scopeType === 'caller_account') {
    clauses.push('instance_accounts.system_account_id = ?')
    params.push(input.viewerSystemAccountId)
  }
  if (input.type) {
    clauses.push('instance_accounts.type = ?')
    params.push(input.type)
  }
  return database
    .prepare(`
      SELECT instance_accounts.id
      FROM accounts source_accounts
      INNER JOIN accounts instance_accounts
        ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
      WHERE ${clauses.map((clause) => `(${clause})`).join(' AND ')}
      ORDER BY source_accounts.name ASC, instance_accounts.id ASC
      LIMIT ?
    `)
    .all(...params, accountUsageSelectedAccountLimit) as unknown as Array<{ id?: string }>
}

async function loadAccountUsageAuthorizedInstanceIdsForSourceKeywordAsync(
  client: DatabaseClient,
  input: {
    keywordPrefix: string
    keywordUpperBound: string
    scopeType: AccountUsageScopeType
    type?: string
    viewerSystemAccountId: string
  }
): Promise<Array<{ id?: string }>> {
  const clauses = ['source_accounts.name COLLATE "C" >= ? AND source_accounts.name COLLATE "C" < ? AND starts_with(source_accounts.name, ?)']
  const params: string[] = [input.keywordPrefix, input.keywordUpperBound, input.keywordPrefix]
  if (input.scopeType === 'caller_account') {
    clauses.push('instance_accounts.system_account_id = ?')
    params.push(input.viewerSystemAccountId)
  }
  if (input.type) {
    clauses.push('instance_accounts.type = ?')
    params.push(input.type)
  }
  return await client.query<{ id?: string }>(`
    SELECT instance_accounts.id
    FROM ${accountUsageBusinessTable(client, 'accounts')} source_accounts
    INNER JOIN ${accountUsageBusinessTable(client, 'accounts')} instance_accounts
      ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
    WHERE ${clauses.map((clause) => `(${clause})`).join(' AND ')}
    ORDER BY source_accounts.name COLLATE "C" ASC, instance_accounts.id ASC
    LIMIT ?
  `, [...params, accountUsageSelectedAccountLimit])
}

function loadAccountUsageGroupAuthorizedAccountIdsForKeyword(
  database: ReturnType<typeof getBusinessDatabase>,
  input: {
    keyword: string
    keywordUpperBound: string
    type?: string
    viewerSystemAccountId: string
  }
): Array<{ id?: string }> {
  const clauses = ['accounts.name >= ? AND accounts.name < ?']
  const params: string[] = [input.viewerSystemAccountId, nowIso(), input.keyword, input.keywordUpperBound]
  if (input.type) {
    clauses.push('accounts.type = ?')
    params.push(input.type)
  }
  return database
    .prepare(`
      SELECT accounts.id
      FROM accounts
      INNER JOIN group_accounts
        ON group_accounts.account_id = accounts.id
        AND group_accounts.enabled = 1
      INNER JOIN resource_authorizations group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = group_accounts.group_id
        AND group_authorization.grantee_system_account_id = ?
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      WHERE ${clauses.map((clause) => `(${clause})`).join(' AND ')}
      ORDER BY accounts.name ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(...params, accountUsageSelectedAccountLimit) as unknown as Array<{ id?: string }>
}

async function loadAccountUsageGroupAuthorizedAccountIdsForKeywordAsync(
  client: DatabaseClient,
  input: {
    keywordPrefix: string
    keywordUpperBound: string
    type?: string
    viewerSystemAccountId: string
  }
): Promise<Array<{ id?: string }>> {
  const clauses = ['accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?)']
  const params: string[] = [input.viewerSystemAccountId, nowIso(), input.keywordPrefix, input.keywordUpperBound, input.keywordPrefix]
  if (input.type) {
    clauses.push('accounts.type = ?')
    params.push(input.type)
  }
  return await client.query<{ id?: string }>(`
    SELECT accounts.id
    FROM ${accountUsageBusinessTable(client, 'accounts')} accounts
    INNER JOIN ${accountUsageBusinessTable(client, 'group_accounts')} group_accounts
      ON group_accounts.account_id = accounts.id
      AND group_accounts.enabled = 1
    INNER JOIN ${accountUsageBusinessTable(client, 'resource_authorizations')} group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = group_accounts.group_id
      AND group_authorization.grantee_system_account_id = ?
      AND group_authorization.status = 'active'
      AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
    WHERE ${clauses.map((clause) => `(${clause})`).join(' AND ')}
    ORDER BY accounts.name COLLATE "C" ASC, accounts.id ASC
    LIMIT ?
  `, [...params, accountUsageSelectedAccountLimit])
}

function appendAccountUsageAccountIds(target: string[], rows: Array<{ id?: string }>): void {
  const seen = new Set(target)
  for (const row of rows) {
    if (!row.id || seen.has(row.id) || target.length >= accountUsageSelectedAccountLimit) continue
    target.push(row.id)
    seen.add(row.id)
  }
}

function normalizeAccountUsageKeyword(value: string): string {
  return value.normalize('NFKC').trim()
}

function accountUsageKeywordUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\uffff`
}

function buildAccountUsageScopeIdFilter(accountIds: string[]): { sql: string; params: string[] } {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) {
    return { sql: '0 = 1', params: [] }
  }
  const chunks = chunkValues(ids, 400)
  const [firstChunk] = chunks
  return {
    sql: chunks.length === 1 && firstChunk
      ? `usage_window.scope_id IN (${sqlPlaceholders(firstChunk.length)})`
      : `(${chunks.map((chunk) => `usage_window.scope_id IN (${sqlPlaceholders(chunk.length)})`).join(' OR ')})`,
    params: chunks.flat()
  }
}

function ensureAccountUsageBusinessDatabaseAttached(database: ReturnType<typeof getStatsDatabase>): void {
  const rows = database.prepare('PRAGMA database_list').all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === accountUsageBusinessDatabaseAlias)) return
  database.prepare(`ATTACH DATABASE ? AS ${accountUsageBusinessDatabaseAlias}`).run(runtimeConfig.databasePath)
}

function loadAccountUsageMetadataRows(access: AccessScope | undefined, accountIds: string[], scopeType: AccountUsageScopeType): AccountUsageMetadataRow[] {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return []
  const viewerSystemAccountId = scopedSystemAccountId(access) ?? currentSystemAccountId(access)
  const authorizationJoin = scopeType === 'caller_account'
    ? `LEFT JOIN resource_authorizations usage_authorization
        ON usage_authorization.resource_type = 'account'
        AND usage_authorization.grantee_system_account_id = ?
        AND usage_authorization.status = 'active'
        AND (usage_authorization.expires_at IS NULL OR usage_authorization.expires_at > ?)
        AND (
          usage_authorization.id = accounts.authorization_instance_authorization_id
          OR (
            accounts.authorization_instance_authorization_id IS NULL
            AND usage_authorization.resource_id = accounts.id
          )
        )`
    : ''
  const rows: AccountUsageMetadataRow[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const queryParams = scopeType === 'caller_account'
      ? [viewerSystemAccountId, viewerSystemAccountId, nowIso(), ...chunk]
      : chunk
    rows.push(...database.prepare(`
      SELECT
        accounts.id,
        accounts.system_account_id,
        COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name,
        accounts.provider_code,
        accounts.name,
        accounts.type,
        accounts.status,
        ${scopeType === 'caller_account' ? "CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' WHEN accounts.system_account_id = ? THEN 'owner' ELSE 'authorized' END" : "'owner'"} AS access_type,
        ${scopeType === 'caller_account' ? 'COALESCE(accounts.authorization_instance_authorization_id, usage_authorization.id)' : 'NULL'} AS authorization_id
      FROM accounts
      LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
      ${authorizationJoin}
      WHERE accounts.id IN (${sqlPlaceholders(chunk.length)})
    `).all(...queryParams) as unknown as AccountUsageMetadataRow[])
  }
  const order = new Map(ids.map((id, index) => [id, index]))
  return rows.sort((left, right) => (order.get(left.id) ?? 0) - (order.get(right.id) ?? 0))
}

async function loadAccountUsageMetadataRowsAsync(
  client: DatabaseClient,
  access: AccessScope | undefined,
  accountIds: string[],
  scopeType: AccountUsageScopeType
): Promise<AccountUsageMetadataRow[]> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return []
  const viewerSystemAccountId = scopedSystemAccountId(access) ?? currentSystemAccountId(access)
  const authorizationJoin = scopeType === 'caller_account'
    ? `LEFT JOIN ${accountUsageBusinessTable(client, 'resource_authorizations')} usage_authorization
        ON usage_authorization.resource_type = 'account'
        AND usage_authorization.grantee_system_account_id = ?
        AND usage_authorization.status = 'active'
        AND (usage_authorization.expires_at IS NULL OR usage_authorization.expires_at > ?)
        AND (
          usage_authorization.id = accounts.authorization_instance_authorization_id
          OR (
            accounts.authorization_instance_authorization_id IS NULL
            AND usage_authorization.resource_id = accounts.id
          )
        )`
    : ''
  const rows: AccountUsageMetadataRow[] = []
  for (const chunk of chunkValues(ids, 900)) {
    const queryParams = scopeType === 'caller_account'
      ? [viewerSystemAccountId, viewerSystemAccountId, nowIso(), ...chunk]
      : chunk
    rows.push(...await client.query<AccountUsageMetadataRow>(`
      SELECT
        accounts.id,
        accounts.system_account_id,
        COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name,
        accounts.provider_code,
        accounts.name,
        accounts.type,
        accounts.status,
        ${scopeType === 'caller_account' ? "CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' WHEN accounts.system_account_id = ? THEN 'owner' ELSE 'authorized' END" : "'owner'"} AS access_type,
        ${scopeType === 'caller_account' ? 'COALESCE(accounts.authorization_instance_authorization_id, usage_authorization.id)' : 'NULL'} AS authorization_id
      FROM ${accountUsageBusinessTable(client, 'accounts')} accounts
      LEFT JOIN ${accountUsageBusinessTable(client, 'system_accounts')} system_accounts
        ON system_accounts.id = accounts.system_account_id
      ${authorizationJoin}
      WHERE accounts.id IN (${client.dialect.bindPlaceholders(chunk.length)})
    `, queryParams))
  }
  const order = new Map(ids.map((id, index) => [id, index]))
  return rows.sort((left, right) => (order.get(left.id) ?? 0) - (order.get(right.id) ?? 0))
}

function accountUsageStatsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}

function accountUsageBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}
