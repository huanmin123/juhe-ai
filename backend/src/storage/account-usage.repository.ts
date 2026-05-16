import type {
  AccountStatus,
  AccountSummary,
  AccountType,
  AccountUsageStatsRange,
  AccountUsageStatsOverview,
  AccountUsageStatsRow
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
import { latestUsageStatsLagSeconds } from './usage-stats.repository.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { GLOBAL_STATS_SCOPE_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { loadUsageDailySeriesForScopeRequests, type UsageStatsDailySeries } from './usage-window-loaders.js'

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
      ownerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? currentSystemAccountId(input.access),
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
    page: input.page ?? 1,
    pageSize: input.pageSize ?? input.accounts.length,
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export interface AccountUsageStatsPageOptions {
  access?: AccessScope
  range: AccountUsageStatsRange
  page: number
  pageSize: number
  keyword?: string
  type?: string
  defaultTrendAccountIds?: string[]
}

type AccountUsageScopeType = 'account' | 'caller_account'

interface AccountUsageStatsSourceRow {
  scope_id: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost: number
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

export function getAccountUsageStatsOverviewPageFromWindows(input: AccountUsageStatsPageOptions): AccountUsageStatsOverview {
  const page = Math.max(1, Math.trunc(input.page))
  const pageSize = Math.max(1, Math.min(Math.trunc(input.pageSize), 200))
  const usageScope = accountUsageListScope(input.access)
  const filter = accountUsageFilterPredicate(accountUsageFilterScopeIds(input, usageScope.scopeType))
  if (filter.empty) {
    return emptyAccountUsageStatsOverview(input, page, pageSize)
  }
  const database = getRecordDatabase()
  const countRow = database.prepare(`
    SELECT COUNT(*) AS total
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.start_date = ?
      AND usage_window.end_date = ?
      AND (
        usage_window.request_count > 0
        OR usage_window.input_tokens > 0
        OR usage_window.output_tokens > 0
        OR usage_window.cache_read_tokens > 0
        OR usage_window.total_cost_usd > 0
        OR usage_window.last_used_at IS NOT NULL
      )
      ${filter.sql}
  `).get(usageScope.systemAccountId, usageScope.scopeType, input.range.startDate, input.range.endDate, ...filter.params) as unknown as { total?: number } | undefined
  const rows = database.prepare(`
    SELECT
      usage_window.scope_id,
      usage_window.request_count,
      usage_window.input_tokens,
      usage_window.output_tokens,
      usage_window.cache_read_tokens,
      usage_window.cache_read_cost_usd AS cache_read_cost,
      usage_window.total_cost_usd AS total_cost,
      usage_window.last_used_at
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.start_date = ?
      AND usage_window.end_date = ?
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
    input.range.startDate,
    input.range.endDate,
    ...filter.params,
    pageSize,
    (page - 1) * pageSize
  ) as unknown as AccountUsageStatsSourceRow[]
  const metadataRows = loadAccountUsageMetadataRows(input.access, rows.map((row) => row.scope_id), usageScope.scopeType)
  const metadataById = new Map(metadataRows.map((row) => [row.id, row]))
  const scopes = rows.map((row): UsageScopeRequest => ({
    rowKey: accountUsageStatsRowKey({ id: row.scope_id, accountAuthorizationId: metadataById.get(row.scope_id)?.authorization_id ?? undefined }),
    systemAccountId: usageScope.systemAccountId,
    scopeType: usageScope.scopeType,
    scopeId: row.scope_id
  }))
  const dailySeriesByRowKey = loadUsageDailySeriesForScopeRequests(scopes, input.range)
  const overviewRows = rows.flatMap((row): AccountUsageStatsRow[] => {
    const metadata = metadataById.get(row.scope_id)
    if (!metadata) return []
    const accountAuthorizationId = metadata.authorization_id ?? undefined
    const rowKey = accountUsageStatsRowKey({ id: row.scope_id, accountAuthorizationId })
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
      dailyUsage: dailySeriesByRowKey.get(rowKey)?.dailyUsage ?? [],
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
    total: Number(countRow?.total ?? 0),
    page,
    pageSize,
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

function emptyAccountUsageStatsOverview(input: AccountUsageStatsPageOptions, page: number, pageSize: number): AccountUsageStatsOverview {
  return {
    range: input.range,
    summary: loadAccountUsageOverviewSummary(input.access, input.range),
    rows: [],
    defaultTrendAccountIds: input.defaultTrendAccountIds ?? [],
    total: 0,
    page,
    pageSize,
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

function loadAccountUsageOverviewSummary(access: AccessScope | undefined, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>) {
  const scope = accountUsageOverviewSummaryScope(access)
  const row = getRecordDatabase().prepare(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd AS cache_read_cost, total_cost_usd AS total_cost, last_used_at
    FROM usage_scope_range_windows
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND start_date = ?
      AND end_date = ?
  `).get(scope.systemAccountId, scope.scopeId, range.startDate, range.endDate) as unknown as {
    request_count: number
    input_tokens: number
    output_tokens: number
    cache_read_tokens: number
    cache_read_cost: number
    total_cost: number
    last_used_at: string | null
  } | undefined
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

  return {
    rowKey: accountUsageStatsRowKey(account),
    systemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? currentSystemAccountId(access),
    scopeType: account.accessType === 'authorized' && account.accountAuthorizationId ? 'account_authorization' : 'account',
    scopeId: account.accessType === 'authorized' && account.accountAuthorizationId ? account.accountAuthorizationId : account.id
  }
}

function accountUsageListScope(access?: AccessScope): { systemAccountId: string; scopeType: AccountUsageScopeType } {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeType: 'caller_account' }
  }
  return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account' }
}

function accountUsageFilterPredicate(scopeIds: string[] | undefined): { sql: string; params: Array<string>; empty: boolean } {
  if (!scopeIds) {
    return { sql: '', params: [], empty: false }
  }
  const ids = [...new Set(scopeIds.filter(Boolean))]
  if (!ids.length) {
    return { sql: '', params: [], empty: true }
  }
  const chunks = []
  const params: string[] = []
  for (let index = 0; index < ids.length; index += 400) {
    const chunk = ids.slice(index, index + 400)
    chunks.push(`usage_window.scope_id IN (${sqlPlaceholders(chunk.length)})`)
    params.push(...chunk)
  }
  return {
    sql: `AND (${chunks.join(' OR ')})`,
    params,
    empty: false
  }
}

function accountUsageFilterScopeIds(input: Pick<AccountUsageStatsPageOptions, 'keyword' | 'type' | 'access'>, scopeType: AccountUsageScopeType): string[] | undefined {
  const type = input.type?.trim()
  const keyword = input.keyword?.trim()
  if (scopeType === 'account' && (!type || type === 'all') && !keyword) {
    return undefined
  }
  const clauses: string[] = []
  const params: string[] = []
  if (type && type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(type)
  }
  if (keyword) {
    const viewerSystemAccountId = scopedSystemAccountId(input.access) ?? currentSystemAccountId(input.access)
    clauses.push(`(
      accounts.name LIKE ?
      OR COALESCE(accounts.notes, '') LIKE ?
      OR accounts.provider_code LIKE ?
      OR accounts.type LIKE ?
      OR accounts.id LIKE ?
      OR EXISTS (
        SELECT 1
        FROM group_accounts
        INNER JOIN groups ON groups.id = group_accounts.group_id
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = ?
          AND group_accounts.enabled = 1
          AND groups.name LIKE ?
      )
    )`)
    params.push(...Array.from({ length: 5 }, () => `%${keyword}%`), viewerSystemAccountId, `%${keyword}%`)
  }
  if (scopeType === 'caller_account') {
    const viewerSystemAccountId = scopedSystemAccountId(input.access) ?? currentSystemAccountId(input.access)
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
    )`)
    params.push(viewerSystemAccountId, viewerSystemAccountId, nowIso())
  }
  const where = clauses.length ? `WHERE ${clauses.join(' AND ')}` : ''
  const rows = getDatabase()
    .prepare(`SELECT accounts.id FROM accounts ${where} ORDER BY accounts.id ASC`)
    .all(...params) as unknown as Array<{ id?: string }>
  return rows.map((row) => row.id).filter((id): id is string => Boolean(id))
}

function loadAccountUsageMetadataRows(access: AccessScope | undefined, accountIds: string[], scopeType: AccountUsageScopeType): AccountUsageMetadataRow[] {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return []
  const viewerSystemAccountId = scopedSystemAccountId(access) ?? currentSystemAccountId(access)
  const authorizationJoin = scopeType === 'caller_account'
    ? `LEFT JOIN resource_authorizations usage_authorization
        ON usage_authorization.resource_type = 'account'
        AND usage_authorization.resource_id = accounts.id
        AND usage_authorization.grantee_system_account_id = ?
        AND usage_authorization.status = 'active'
        AND (usage_authorization.expires_at IS NULL OR usage_authorization.expires_at > ?)`
    : ''
  const queryParams = scopeType === 'caller_account'
    ? [viewerSystemAccountId, viewerSystemAccountId, nowIso(), ...ids]
    : ids
  const rows = getDatabase().prepare(`
    SELECT
      accounts.id,
      accounts.system_account_id,
      COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name,
      accounts.provider_code,
      accounts.name,
      accounts.type,
      accounts.status,
      ${scopeType === 'caller_account' ? "CASE WHEN accounts.system_account_id = ? THEN 'owner' ELSE 'authorized' END" : "'owner'"} AS access_type,
      ${scopeType === 'caller_account' ? 'usage_authorization.id' : 'NULL'} AS authorization_id
    FROM accounts
    LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
    ${authorizationJoin}
    WHERE accounts.id IN (${sqlPlaceholders(ids.length)})
  `).all(...queryParams) as unknown as AccountUsageMetadataRow[]
  const order = new Map(ids.map((id, index) => [id, index]))
  return rows.sort((left, right) => (order.get(left.id) ?? 0) - (order.get(right.id) ?? 0))
}
