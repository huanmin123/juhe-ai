import type {
  AccountStatus,
  AccountSummary,
  AccountType,
  AccountUsageStatsRange,
  AccountUsageStatsOverview,
  AccountUsageStatsListResult,
  AccountUsageStatsOption,
  AccountUsageStatsRow,
  AccountUsageStatsTrendOverview
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { canAccessAll, currentSystemAccountId, includeSystemAccountFields, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { GLOBAL_STATS_SCOPE_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { loadUsageDailySeriesForScopeRequests, loadUsageDailySeriesForScopeRequestsAsync, type UsageStatsDailySeries } from './usage-window-loaders.js'

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

interface AccountUsageOptionRow {
  id: string
  system_account_id: string
  system_account_name: string | null
  owner_system_account_id: string
  owner_system_account_name: string | null
  provider_code: string
  provider_name: string
  name: string
  type: AccountType
  status: AccountStatus
  access_type: 'owner' | 'authorized'
}

export interface AccountUsageOptionListOptions {
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export function listAccountUsageOptions(access?: AccessScope, options: AccountUsageOptionListOptions = {}): AccountUsageStatsOption[] {
  const database = getBusinessDatabase()
  const scope = accountUsageOptionScope(access)
  const normalized = normalizeAccountUsageOptionListOptions(options)
  const searchRows = queryAccountUsageOptionRows(database, scope, normalized.keyword, [], normalized.limit)
  const selectedRows = normalized.selectedIds.length
    ? queryAccountUsageOptionRows(database, scope, undefined, normalized.selectedIds, normalized.selectedIds.length)
    : []
  return mapAccountUsageOptionRows(dedupeAccountUsageOptionRows([...selectedRows, ...searchRows]), access)
}

export async function listAccountUsageOptionsAsync(access?: AccessScope, options: AccountUsageOptionListOptions = {}): Promise<AccountUsageStatsOption[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') return listAccountUsageOptions(access, options)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const scope = accountUsageOptionScope(access)
  const normalized = normalizeAccountUsageOptionListOptions(options)
  const [searchRows, selectedRows] = await Promise.all([
    queryAccountUsageOptionRowsAsync(client, scope, normalized.keyword, [], normalized.limit),
    normalized.selectedIds.length
      ? queryAccountUsageOptionRowsAsync(client, scope, undefined, normalized.selectedIds, normalized.selectedIds.length)
      : Promise.resolve([])
  ])
  return mapAccountUsageOptionRows(dedupeAccountUsageOptionRows([...selectedRows, ...searchRows]), access)
}

function accountUsageOptionScope(access?: AccessScope): string | undefined {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) return scopedId
  return canAccessAll(access) ? undefined : currentSystemAccountId(access)
}

function normalizeAccountUsageOptionListOptions(options: AccountUsageOptionListOptions): { keyword?: string; limit: number; selectedIds: string[] } {
  const keyword = options.keyword?.trim()
  const rawLimit = Number(options.limit ?? 50)
  const limit = Number.isFinite(rawLimit) ? Math.min(50, Math.max(1, Math.trunc(rawLimit))) : 50
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].slice(0, 20)
  return { keyword: keyword || undefined, limit, selectedIds }
}

function queryAccountUsageOptionRows(
  database: ReturnType<typeof getBusinessDatabase>,
  ownerSystemAccountId: string | undefined,
  keyword: string | undefined,
  ids: string[],
  limit: number
): AccountUsageOptionRow[] {
  const clauses = ['accounts.deleted_at IS NULL']
  const params: Array<string | number> = []
  if (ownerSystemAccountId) {
    clauses.push('accounts.system_account_id = ?')
    params.push(ownerSystemAccountId)
  } else {
    clauses.push('accounts.authorization_instance_authorization_id IS NULL')
  }
  if (ids.length) {
    clauses.push(`accounts.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  } else if (keyword) {
    clauses.push('instr(accounts.name, ?) > 0')
    params.push(keyword)
  }
  return database.prepare(`
    SELECT
      accounts.id,
      accounts.system_account_id,
      COALESCE(system_accounts.display_name, system_accounts.username) AS system_account_name,
      COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id,
      COALESCE(owner_accounts.display_name, owner_accounts.username) AS owner_system_account_name,
      accounts.provider_code,
      COALESCE(providers.name, accounts.provider_code) AS provider_name,
      accounts.name,
      accounts.type,
      accounts.status,
      CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN 'owner' ELSE 'authorized' END AS access_type
    FROM accounts
    LEFT JOIN providers ON providers.code = accounts.provider_code
    LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
    LEFT JOIN system_accounts owner_accounts
      ON owner_accounts.id = COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id)
    WHERE ${clauses.join(' AND ')}
    ORDER BY accounts.name ASC, accounts.id ASC
    LIMIT ?
  `).all(...params, limit) as unknown as AccountUsageOptionRow[]
}

async function queryAccountUsageOptionRowsAsync(
  client: DatabaseClient,
  ownerSystemAccountId: string | undefined,
  keyword: string | undefined,
  ids: string[],
  limit: number
): Promise<AccountUsageOptionRow[]> {
  const clauses = ['accounts.deleted_at IS NULL']
  const params: Array<string | number> = []
  if (ownerSystemAccountId) {
    clauses.push('accounts.system_account_id = ?')
    params.push(ownerSystemAccountId)
  } else {
    clauses.push('accounts.authorization_instance_authorization_id IS NULL')
  }
  if (ids.length) {
    clauses.push(`accounts.id IN (${client.dialect.bindPlaceholders(ids.length)})`)
    params.push(...ids)
  } else if (keyword) {
    clauses.push(`accounts.name COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\'`)
    params.push(postgresSubstringLikePattern(keyword))
  }
  const accountsTable = accountUsageBusinessTable(client, 'accounts')
  const providersTable = accountUsageBusinessTable(client, 'providers')
  const systemAccountsTable = accountUsageBusinessTable(client, 'system_accounts')
  return client.query<AccountUsageOptionRow>(`
    SELECT
      accounts.id,
      accounts.system_account_id,
      COALESCE(system_accounts.display_name, system_accounts.username) AS system_account_name,
      COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id,
      COALESCE(owner_accounts.display_name, owner_accounts.username) AS owner_system_account_name,
      accounts.provider_code,
      COALESCE(providers.name, accounts.provider_code) AS provider_name,
      accounts.name,
      accounts.type,
      accounts.status,
      CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN 'owner' ELSE 'authorized' END AS access_type
    FROM ${accountsTable} accounts
    LEFT JOIN ${providersTable} providers ON providers.code = accounts.provider_code
    LEFT JOIN ${systemAccountsTable} system_accounts ON system_accounts.id = accounts.system_account_id
    LEFT JOIN ${systemAccountsTable} owner_accounts
      ON owner_accounts.id = COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id)
    WHERE ${clauses.join(' AND ')}
    ORDER BY accounts.name COLLATE "C" ASC, accounts.id ASC
    LIMIT ?
  `, [...params, limit])
}

function mapAccountUsageOptionRows(rows: AccountUsageOptionRow[], access?: AccessScope): AccountUsageStatsOption[] {
  const includeSystemAccount = includeSystemAccountFields(access)
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeSystemAccount ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccount ? row.system_account_name ?? undefined : undefined,
    ownerSystemAccountId: row.owner_system_account_id,
    ownerSystemAccountName: row.owner_system_account_name ?? undefined,
    providerCode: row.provider_code as AccountUsageStatsOption['providerCode'],
    providerName: row.provider_name,
    name: row.name,
    type: row.type,
    status: row.status,
    accessType: row.access_type
  }))
}

function dedupeAccountUsageOptionRows(rows: AccountUsageOptionRow[]): AccountUsageOptionRow[] {
  const seen = new Set<string>()
  return rows.filter((row) => {
    if (seen.has(row.id)) return false
    seen.add(row.id)
    return true
  })
}

export function getAccountUsageStatsOverviewPageFromWindows(input: AccountUsageStatsPageOptions): AccountUsageStatsListResult {
  const pageSize = Math.max(1, Math.min(Math.trunc(input.pageSize), 200))
  const maxPage = Math.max(1, Math.floor((accountUsageMaxListWindowRows - 1) / pageSize))
  const page = Math.min(maxPage, Math.max(1, Math.trunc(input.page)))
  const usageScope = accountUsageListScope(input.access)
  const database = getStatsDatabase()
  const filter = accountUsageFilterPredicate(input, usageScope.scopeType, database)
  const rows = database.prepare(`
    SELECT
      usage_window.scope_id,
      SUM(usage_window.request_count) AS request_count,
      SUM(usage_window.input_tokens) AS input_tokens,
      SUM(usage_window.output_tokens) AS output_tokens,
      SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
      SUM(usage_window.cache_read_cost_usd) AS cache_read_cost_usd,
      SUM(usage_window.total_cost_usd) AS total_cost,
      MAX(usage_window.last_used_at) AS last_used_at
    FROM usage_stats_daily usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.stat_date >= ?
      AND usage_window.stat_date <= ?
      ${filter.sql}
    GROUP BY usage_window.scope_id
    HAVING (
        SUM(usage_window.request_count) > 0
        OR SUM(usage_window.input_tokens) > 0
        OR SUM(usage_window.output_tokens) > 0
        OR SUM(usage_window.cache_read_tokens) > 0
        OR SUM(usage_window.total_cost_usd) > 0
        OR MAX(usage_window.last_used_at) IS NOT NULL
      )
    ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
    LIMIT ? OFFSET ?
  `).all(
    usageScope.systemAccountId,
    usageScope.scopeType,
    input.range.startDate,
    input.range.endDate,
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
    rows: overviewRows,
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: Math.max(pagedTotalUpperBound(page, pageSize, pageRows.rows.length, pageRows.hasMore), (page - 1) * pageSize + overviewRows.length),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export async function getAccountUsageStatsOverviewPageFromWindowsAsync(input: AccountUsageStatsPageOptions): Promise<AccountUsageStatsListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountUsageStatsOverviewPageFromWindows(input)
  }
  const pageSize = Math.max(1, Math.min(Math.trunc(input.pageSize), 200))
  const maxPage = Math.max(1, Math.floor((accountUsageMaxListWindowRows - 1) / pageSize))
  const page = Math.min(maxPage, Math.max(1, Math.trunc(input.page)))
  const usageScope = accountUsageListScope(input.access)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const filter = await accountUsageFilterPredicateAsync(input, usageScope.scopeType, client)
  const rows = await client.query<AccountUsageStatsSourceRow>(`
    SELECT
      usage_window.scope_id,
      SUM(usage_window.request_count) AS request_count,
      SUM(usage_window.input_tokens) AS input_tokens,
      SUM(usage_window.output_tokens) AS output_tokens,
      SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
      CAST(SUM(usage_window.cache_read_cost_usd) AS double precision) AS cache_read_cost_usd,
      CAST(SUM(usage_window.total_cost_usd) AS double precision) AS total_cost,
      MAX(usage_window.last_used_at) AS last_used_at
    FROM ${accountUsageStatsTable(client, 'usage_stats_daily')} usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.stat_date >= ?
      AND usage_window.stat_date <= ?
      ${filter.sql}
    GROUP BY usage_window.scope_id
    HAVING (
        SUM(usage_window.request_count) > 0
        OR SUM(usage_window.input_tokens) > 0
        OR SUM(usage_window.output_tokens) > 0
        OR SUM(usage_window.cache_read_tokens) > 0
        OR SUM(usage_window.total_cost_usd) > 0
        OR MAX(usage_window.last_used_at) IS NOT NULL
      )
    ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
    LIMIT ? OFFSET ?
  `, [
    usageScope.systemAccountId,
    usageScope.scopeType,
    input.range.startDate,
    input.range.endDate,
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

export function getAccountUsageStatsSummary(
  access: AccessScope | undefined,
  range: AccountUsageStatsRange
): { range: AccountUsageStatsRange; summary: ReturnType<typeof loadAccountUsageOverviewSummary> } {
  return { range, summary: loadAccountUsageOverviewSummary(access, range) }
}

export async function getAccountUsageStatsSummaryAsync(
  access: AccessScope | undefined,
  range: AccountUsageStatsRange
): Promise<{ range: AccountUsageStatsRange; summary: Awaited<ReturnType<typeof loadAccountUsageOverviewSummaryAsync>> }> {
  if (runtimeConfig.databaseDriver !== 'postgres') return getAccountUsageStatsSummary(access, range)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return { range, summary: await loadAccountUsageOverviewSummaryAsync(client, access, range) }
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
  return input.database.prepare(`
    SELECT
      usage_window.scope_id,
      SUM(usage_window.request_count) AS request_count,
      SUM(usage_window.input_tokens) AS input_tokens,
      SUM(usage_window.output_tokens) AS output_tokens,
      SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
      SUM(usage_window.cache_read_cost_usd) AS cache_read_cost_usd,
      SUM(usage_window.total_cost_usd) AS total_cost,
      MAX(usage_window.last_used_at) AS last_used_at
    FROM usage_stats_daily usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.stat_date >= ?
      AND usage_window.stat_date <= ?
      AND ${accountFilter.sql}
    GROUP BY usage_window.scope_id
    ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
  `).all(
    input.usageScope.systemAccountId,
    input.usageScope.scopeType,
    input.input.range.startDate,
    input.input.range.endDate,
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
  return await input.client.query<AccountUsageStatsSourceRow>(`
    SELECT
      usage_window.scope_id,
      SUM(usage_window.request_count) AS request_count,
      SUM(usage_window.input_tokens) AS input_tokens,
      SUM(usage_window.output_tokens) AS output_tokens,
      SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
      CAST(SUM(usage_window.cache_read_cost_usd) AS double precision) AS cache_read_cost_usd,
      CAST(SUM(usage_window.total_cost_usd) AS double precision) AS total_cost,
      MAX(usage_window.last_used_at) AS last_used_at
    FROM ${accountUsageStatsTable(input.client, 'usage_stats_daily')} usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.stat_date >= ?
      AND usage_window.stat_date <= ?
      AND ${accountFilter.sql}
    GROUP BY usage_window.scope_id
    ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
  `, [
    input.usageScope.systemAccountId,
    input.usageScope.scopeType,
    input.input.range.startDate,
    input.input.range.endDate,
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

function emptyAccountUsageStatsOverview(input: AccountUsageStatsPageOptions, page: number, pageSize: number): AccountUsageStatsListResult {
  return {
    range: input.range,
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
  const row = getStatsDatabase().prepare(`
    SELECT
      COALESCE(SUM(request_count), 0) AS request_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens,
      COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
      COALESCE(SUM(cache_read_cost_usd), 0) AS cache_read_cost_usd,
      COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
      COALESCE(SUM(cache_write_1h_tokens), 0) AS cache_write_1h_tokens,
      COALESCE(SUM(cache_write_cost_usd), 0) AS cache_write_cost_usd,
      COALESCE(SUM(thinking_tokens), 0) AS thinking_tokens,
      COALESCE(SUM(input_image_tokens), 0) AS input_image_tokens,
      COALESCE(SUM(output_image_tokens), 0) AS output_image_tokens,
      COALESCE(SUM(total_cost_usd), 0) AS total_cost,
      MAX(last_used_at) AS last_used_at
    FROM usage_stats_daily
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
  `).get(scope.systemAccountId, scope.scopeId, range.startDate, range.endDate) as unknown as {
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
      COALESCE(SUM(request_count), 0) AS request_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens,
      COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
      CAST(COALESCE(SUM(cache_read_cost_usd), 0) AS double precision) AS cache_read_cost_usd,
      COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
      COALESCE(SUM(cache_write_1h_tokens), 0) AS cache_write_1h_tokens,
      CAST(COALESCE(SUM(cache_write_cost_usd), 0) AS double precision) AS cache_write_cost_usd,
      COALESCE(SUM(thinking_tokens), 0) AS thinking_tokens,
      COALESCE(SUM(input_image_tokens), 0) AS input_image_tokens,
      COALESCE(SUM(output_image_tokens), 0) AS output_image_tokens,
      CAST(COALESCE(SUM(total_cost_usd), 0) AS double precision) AS total_cost,
      MAX(last_used_at) AS last_used_at
    FROM ${accountUsageStatsTable(client, 'usage_stats_daily')}
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
  `, [scope.systemAccountId, scope.scopeId, range.startDate, range.endDate])
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
  clauses.push(`(
    (instr(accounts.name, ?) > 0)
    OR (instr(accounts.provider_code, ?) > 0)
    OR (instr(accounts.type, ?) > 0)
    OR EXISTS (
      SELECT 1
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND instr(groups.name, ?) > 0
    )
  )`)
  params.push(
    keyword,
    keyword,
    keyword,
    viewerSystemAccountId,
    keyword
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
    scopeType: input.scopeType,
    type: input.type,
    viewerSystemAccountId
  }))
  if (input.scopeType === 'caller_account') {
    appendAccountUsageAccountIds(ids, loadAccountUsageGroupAuthorizedAccountIdsForKeyword(database, {
      keyword,
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
  const normalizedKeyword = normalizeAccountUsageKeyword(keyword)
  const substringLikePattern = postgresSubstringLikePattern(normalizedKeyword)
  clauses.push(`(
    (accounts.name COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\')
    OR (accounts.provider_code COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\')
    OR (accounts.type COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\')
    OR EXISTS (
      SELECT 1
      FROM ${accountUsageBusinessTable(client, 'group_accounts')} group_accounts
      INNER JOIN ${accountUsageBusinessTable(client, 'groups')} groups
        ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND groups.name COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\'
    )
  )`)
  params.push(
    substringLikePattern,
    substringLikePattern,
    substringLikePattern,
    viewerSystemAccountId,
    substringLikePattern
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
    substringLikePattern,
    scopeType: input.scopeType,
    type: input.type,
    viewerSystemAccountId
  }))
  if (input.scopeType === 'caller_account') {
    appendAccountUsageAccountIds(ids, await loadAccountUsageGroupAuthorizedAccountIdsForKeywordAsync(client, {
      substringLikePattern,
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
    scopeType: AccountUsageScopeType
    type?: string
    viewerSystemAccountId: string
  }
): Array<{ id?: string }> {
  const clauses = ['instr(source_accounts.name, ?) > 0']
  const params: string[] = [input.keyword]
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
    substringLikePattern: string
    scopeType: AccountUsageScopeType
    type?: string
    viewerSystemAccountId: string
  }
): Promise<Array<{ id?: string }>> {
  const clauses = [`source_accounts.name COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\'`]
  const params: string[] = [input.substringLikePattern]
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
    type?: string
    viewerSystemAccountId: string
  }
): Array<{ id?: string }> {
  const clauses = ['instr(accounts.name, ?) > 0']
  const params: string[] = [input.viewerSystemAccountId, nowIso(), input.keyword]
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
    substringLikePattern: string
    type?: string
    viewerSystemAccountId: string
  }
): Promise<Array<{ id?: string }>> {
  const clauses = [`accounts.name COLLATE "C" LIKE '%' || ? || '%' ESCAPE '\\'`]
  const params: string[] = [input.viewerSystemAccountId, nowIso(), input.substringLikePattern]
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

function postgresSubstringLikePattern(value: string): string {
  return value
    .replaceAll('\\', '\\\\')
    .replaceAll('%', '\\%')
    .replaceAll('_', '\\_')
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
