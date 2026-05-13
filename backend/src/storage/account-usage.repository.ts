import type {
  AccountSummary,
  AccountUsageStatsRange,
  AccountUsageStatsOverview,
  AccountUsageStatsRow
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getRecordDatabase } from './database.js'
import { latestUsageStatsLagSeconds } from './usage-stats.repository.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { GLOBAL_STATS_SCOPE_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import type { UsageStatsDailySeries } from './usage-window-loaders.js'

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

function loadAccountUsageOverviewSummary(access: AccessScope | undefined, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>) {
  const scope = accountUsageOverviewSummaryScope(access)
  const row = getRecordDatabase().prepare(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost, last_used_at
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
