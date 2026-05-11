import type {
  AccountSummary,
  AccountUsageStatsRange,
  AccountUsageStatsOverview,
  AccountUsageStatsRow
} from '../domain/types.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { latestUsageStatsLagSeconds } from './usage-stats.repository.js'
import { addUsageSummaries, emptyAccountUsageSummary } from './usage-stats-helpers.js'
import type { UsageStatsDailySeries } from './usage-window-loaders.js'

export function getAccountUsageStatsOverview(input: {
  access?: AccessScope
  accounts: AccountSummary[]
  total?: number
  page?: number
  pageSize?: number
  range: AccountUsageStatsRange
  loadUsageDailySeries: (scopes: UsageScopeRequest[], range: AccountUsageStatsRange) => Map<string, UsageStatsDailySeries>
}): AccountUsageStatsOverview {
  const scopes = input.accounts.map((account) => accountUsageScope(account, input.access))
  const dailySeriesByRowKey = input.loadUsageDailySeries(scopes, input.range)
  let summary = emptyAccountUsageSummary()
  const rows = input.accounts.map((account): AccountUsageStatsRow => {
    const rowKey = accountUsageStatsRowKey(account)
    const dailySeries = dailySeriesByRowKey.get(rowKey)
    summary = addUsageSummaries(summary, dailySeries?.rangeUsage)
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
    summary,
    rows,
    total: input.total ?? input.accounts.length,
    page: input.page ?? 1,
    pageSize: input.pageSize ?? input.accounts.length,
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export interface UsageScopeRequest {
  rowKey: string
  systemAccountId: string
  scopeType: string
  scopeId: string
}

function accountUsageStatsRowKey(account: AccountSummary): string {
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
