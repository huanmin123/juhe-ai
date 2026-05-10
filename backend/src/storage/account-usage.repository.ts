import type {
  AccountAuthorizationUsageOverview,
  AccountSummary,
  AccountUsageStatsOverview,
  AccountUsageStatsRow,
  AuthorizationTeamMemberUsageDetail,
  AuthorizationTeamUsageDetail,
  ProviderCode,
  ResourceAuthorizationSummary,
  UsageByWindow
} from '../domain/types.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { latestUsageStatsLagSeconds } from './usage-stats.repository.js'
import { addUsageSummaries, emptyUsageByWindow, USAGE_STATS_WINDOWS } from './usage-stats-helpers.js'

export function getAccountUsageStatsOverview(input: {
  access?: AccessScope
  accounts: AccountSummary[]
  total?: number
  page?: number
  pageSize?: number
  loadUsageByWindow: (scopes: UsageScopeRequest[]) => Map<string, UsageByWindow>
}): AccountUsageStatsOverview {
  const scopes = input.accounts.map((account) => ({
    rowKey: accountUsageStatsRowKey(account),
    systemAccountId: account.accessType === 'authorized' && account.accountAuthorizationId
      ? account.ownerSystemAccountId ?? account.systemAccountId ?? currentSystemAccountId(input.access)
      : account.ownerSystemAccountId ?? account.systemAccountId ?? currentSystemAccountId(input.access),
    scopeType: account.accessType === 'authorized' && account.accountAuthorizationId ? 'account_authorization' : 'account',
    scopeId: account.accessType === 'authorized' && account.accountAuthorizationId ? account.accountAuthorizationId : account.id
  }))
  const usageByRowKey = input.loadUsageByWindow(scopes)
  return {
    windows: USAGE_STATS_WINDOWS,
    rows: input.accounts.map((account): AccountUsageStatsRow => ({
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
      usageByWindow: usageByRowKey.get(accountUsageStatsRowKey(account)) ?? emptyUsageByWindow(),
      authorizationUsageAvailable: account.authorizationUsageAvailable === true,
      authorizationCount: account.authorizationCount ?? 0,
      authorizationTeamCount: account.authorizationTeamCount ?? 0
    })),
    total: input.total ?? input.accounts.length,
    page: input.page ?? 1,
    pageSize: input.pageSize ?? input.accounts.length,
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export function getAccountAuthorizationUsageOverview(input: {
  account: {
    id: string
    systemAccountId: string
    name: string
    providerCode: ProviderCode
  }
  authorizations: ResourceAuthorizationSummary[]
  ownerName?: string
  loadUsageByWindow: (scopes: UsageScopeRequest[]) => Map<string, UsageByWindow>
}): AccountAuthorizationUsageOverview {
  const usageScopes = input.authorizations.map((authorization) => ({
    rowKey: authorization.id,
    systemAccountId: authorization.resourceOwnerSystemAccountId,
    scopeType: 'account_authorization',
    scopeId: authorization.id
  }))
  const usageByAuthorizationId = input.loadUsageByWindow(usageScopes)
  const users = input.authorizations.map((authorization) => ({
    ...authorization,
    usageByWindow: usageByAuthorizationId.get(authorization.id) ?? emptyUsageByWindow()
  }))
  const teams = buildAuthorizationTeamUsageDetails(users)
  return {
    resourceType: 'account',
    resourceId: input.account.id,
    resourceName: input.account.name,
    resourceOwnerSystemAccountId: input.account.systemAccountId,
    resourceOwnerSystemAccountName: input.ownerName,
    windows: USAGE_STATS_WINDOWS,
    users,
    teams,
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

function buildAuthorizationTeamUsageDetails(authorizations: Array<ResourceAuthorizationSummary & { usageByWindow: UsageByWindow }>): AuthorizationTeamUsageDetail[] {
  const teams = new Map<string, AuthorizationTeamUsageDetail>()
  for (const authorization of authorizations) {
    const activeTeamSources = (authorization.authorizationSources ?? authorization.sources ?? [])
      .filter((source) => source.sourceType === 'team' && source.status === 'active' && source.sourceTeamId)
    for (const source of activeTeamSources) {
      const teamId = source.sourceTeamId
      if (!teamId) continue
      const existing = teams.get(teamId) ?? {
        teamId,
        teamName: source.sourceTeamName,
        usageByWindow: emptyUsageByWindow(),
        memberUsage: []
      }
      existing.teamName = existing.teamName ?? source.sourceTeamName
      existing.usageByWindow = addUsageByWindow(existing.usageByWindow, authorization.usageByWindow)
      existing.memberUsage.push({
        authorizationId: authorization.id,
        systemAccountId: authorization.granteeSystemAccountId,
        systemAccountName: authorization.granteeSystemAccountName,
        username: authorization.granteeUsername,
        usageByWindow: authorization.usageByWindow
      })
      teams.set(teamId, existing)
    }
  }

  return [...teams.values()].map((team) => ({
    ...team,
    memberUsage: team.memberUsage.sort(compareAuthorizationTeamMemberUsage)
  })).sort((left, right) => {
    const requestDelta = right.usageByWindow.total.requestCount - left.usageByWindow.total.requestCount
    if (requestDelta !== 0) return requestDelta
    const nameDelta = (left.teamName ?? left.teamId).localeCompare(right.teamName ?? right.teamId)
    return nameDelta !== 0 ? nameDelta : left.teamId.localeCompare(right.teamId)
  })
}

function addUsageByWindow(left: UsageByWindow, right: UsageByWindow): UsageByWindow {
  return Object.fromEntries(USAGE_STATS_WINDOWS.map((window) => [window.key, addUsageSummaries(left[window.key], right[window.key])])) as UsageByWindow
}

function compareAuthorizationTeamMemberUsage(left: AuthorizationTeamMemberUsageDetail, right: AuthorizationTeamMemberUsageDetail): number {
  const requestDelta = right.usageByWindow.total.requestCount - left.usageByWindow.total.requestCount
  if (requestDelta !== 0) return requestDelta
  const nameDelta = (left.systemAccountName ?? left.username ?? left.systemAccountId).localeCompare(right.systemAccountName ?? right.username ?? right.systemAccountId)
  return nameDelta !== 0 ? nameDelta : left.systemAccountId.localeCompare(right.systemAccountId)
}
