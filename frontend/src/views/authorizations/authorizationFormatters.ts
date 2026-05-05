import axios from 'axios'

import {
  formatCompactUsageAmount,
  formatDateTime,
  formatNumber,
  formatServerDateTimeInput,
  formatUsd,
  parseDatePickerValue
} from '@/shared/formatters'
import type {
  AccountUsageSummary,
  AuthorizationSourceSummary,
  AuthorizationStatus,
  AuthorizationUserUsageDetail,
  ResourceAuthorizationSummary,
  SystemTeamSummary
} from '@/types/domain'

export interface AuthorizationUsageResponseDetail {
  systemAccountId?: string
  systemAccountName?: string
  username?: string
  usage?: Partial<AccountUsageSummary>
  requestCount?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  totalTokens?: number
  totalCost?: number
  lastUsedAt?: string
}

export interface AuthorizationUsageResponseShape {
  authorization?: ResourceAuthorizationSummary
  usage?: Partial<AccountUsageSummary>
  details?: AuthorizationUsageResponseDetail[]
}

export interface TeamUsageSummary {
  teamId: string
  teamName: string
  usage: AccountUsageSummary
  memberCount: number
  members: Array<{
    key: string
    teamId: string
    teamName: string
    systemAccountId: string
    systemAccountName: string
    usage: AccountUsageSummary
  }>
}

export function statusLabel(status: AuthorizationStatus): string {
  if (status === 'active') return '生效中'
  if (status === 'paused') return '已暂停'
  if (status === 'expired') return '授权到期'
  if (status === 'revoked') return '已收回'
  return status
}

export function statusTagColor(status: AuthorizationStatus): string {
  if (status === 'active') return 'green'
  if (status === 'paused') return 'orange'
  if (status === 'expired') return 'default'
  if (status === 'revoked') return 'default'
  return 'default'
}

export function sourceLabel(source: AuthorizationSourceSummary): string {
  const baseLabel = source.sourceType === 'manual'
    ? '个人'
    : '团队'
  if (source.status === 'active') return baseLabel
  if (source.status === 'superseded') return `${baseLabel}（已被团队覆盖）`
  return `${baseLabel}（已收回）`
}

export function sourceTagColor(source: AuthorizationSourceSummary): string {
  if (source.status !== 'active') return 'default'
  return source.sourceType === 'manual' ? 'cyan' : 'gold'
}

export function granteeSourceLabel(item: ResourceAuthorizationSummary): string | undefined {
  if (item.effectiveSourceType === 'manual') return '个人'
  if (item.effectiveSourceType === 'team') {
    return '团队'
  }
  const activeSources = item.authorizationSources?.filter((source) => source.status === 'active') ?? []
  const manual = activeSources.some((source) => source.sourceType === 'manual')
  const team = activeSources.find((source) => source.sourceType === 'team')
  if (manual && team) return '个人+团队'
  if (manual) return '个人'
  if (team) return '团队'
  return undefined
}

export function granteeSourceTagColor(item: ResourceAuthorizationSummary): string {
  if (item.effectiveSourceType === 'team') return 'gold'
  if (item.effectiveSourceType === 'manual') return 'cyan'
  return activeTeamSources(item).length > 0 ? 'gold' : 'cyan'
}

export function hasManualSource(item: ResourceAuthorizationSummary): boolean {
  return item.authorizationSources?.some((source) => source.sourceType === 'manual' && source.status === 'active') ?? false
}

export function activeTeamSources(item: ResourceAuthorizationSummary): AuthorizationSourceSummary[] {
  return item.authorizationSources?.filter((source) => source.sourceType === 'team' && source.status === 'active' && source.sourceTeamId) ?? []
}

export function hasTeamSource(item: ResourceAuthorizationSummary, teamId: string): boolean {
  return item.authorizationSources?.some((source) => source.sourceType === 'team' && source.sourceTeamId === teamId && source.status === 'active') ?? false
}

export function relatedTeamSources(
  item: ResourceAuthorizationSummary,
  teams: SystemTeamSummary[],
  filteredTeamId?: string
): Array<{ teamId: string; teamName: string }> {
  const sourceMap = new Map<string, string>()
  for (const source of item.authorizationSources ?? []) {
    if (source.sourceType !== 'team' || !source.sourceTeamId) {
      continue
    }
    sourceMap.set(source.sourceTeamId, source.sourceTeamName || teams.find((team) => team.id === source.sourceTeamId)?.name || source.sourceTeamId)
  }
  if (filteredTeamId && !sourceMap.has(filteredTeamId)) {
    sourceMap.set(filteredTeamId, teams.find((team) => team.id === filteredTeamId)?.name || filteredTeamId)
  }
  return [...sourceMap.entries()].map(([teamId, teamName]) => ({ teamId, teamName }))
}

export function buildTeamUsageSummaries(
  authorization: ResourceAuthorizationSummary,
  resourceAuthorizations: ResourceAuthorizationSummary[],
  teams: SystemTeamSummary[],
  filteredTeamId?: string
): TeamUsageSummary[] {
  return relatedTeamSources(authorization, teams, filteredTeamId).map((teamSource) => {
    const members = resourceAuthorizations
      .filter((item) => item.resourceType === authorization.resourceType && item.resourceId === authorization.resourceId && hasTeamSource(item, teamSource.teamId))
      .map((item) => ({
        key: `${teamSource.teamId}:${item.granteeSystemAccountId}`,
        teamId: teamSource.teamId,
        teamName: teamSource.teamName,
        systemAccountId: item.granteeSystemAccountId,
        systemAccountName: item.granteeSystemAccountName || item.granteeUsername || '未命名成员',
        usage: normalizeUsageSummary(item.usage)
      }))
      .sort((left, right) => left.systemAccountName.localeCompare(right.systemAccountName, 'zh-CN'))
    return {
      teamId: teamSource.teamId,
      teamName: teamSource.teamName,
      usage: sumUsageSummaries(members.map((member) => member.usage)),
      memberCount: members.length,
      members
    }
  }).filter((summary) => summary.memberCount > 0)
}

export function usageSummaryText(usage?: {
  requestCount?: number
  totalTokens?: number
  totalCost?: number
}): string {
  return `${formatNumber(usage?.requestCount)}req / ${formatUsageAmount(usage?.totalTokens)} / ${formatCost(usage?.totalCost)}`
}

export function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

export function formatCost(value?: number): string {
  return formatUsd(value)
}

export function emptyUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

export function normalizeUsageSummary(usage?: Partial<AccountUsageSummary>): AccountUsageSummary {
  return {
    requestCount: usage?.requestCount ?? 0,
    inputTokens: usage?.inputTokens ?? 0,
    outputTokens: usage?.outputTokens ?? 0,
    cacheReadTokens: usage?.cacheReadTokens ?? 0,
    totalTokens: usage?.totalTokens ?? 0,
    totalCost: usage?.totalCost ?? 0,
    lastUsedAt: usage?.lastUsedAt
  }
}

export function sumUsageSummaries(items: Array<Partial<AccountUsageSummary> | undefined>): AccountUsageSummary {
  return items.reduce<AccountUsageSummary>((summary, usage) => {
    const current = normalizeUsageSummary(usage)
    const lastUsedAt = [summary.lastUsedAt, current.lastUsedAt]
      .filter((value): value is string => Boolean(value))
      .sort((left, right) => Date.parse(right) - Date.parse(left))[0]
    return {
      requestCount: summary.requestCount + current.requestCount,
      inputTokens: summary.inputTokens + current.inputTokens,
      outputTokens: summary.outputTokens + current.outputTokens,
      cacheReadTokens: summary.cacheReadTokens + current.cacheReadTokens,
      totalTokens: summary.totalTokens + current.totalTokens,
      totalCost: summary.totalCost + current.totalCost,
      lastUsedAt
    }
  }, emptyUsageSummary())
}

export function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return error instanceof Error ? error.message : fallback
}

export function normalizeUsageDetail(detail: AuthorizationUsageResponseDetail): AuthorizationUserUsageDetail | undefined {
  if (!detail.systemAccountId) {
    return undefined
  }
  const usage = normalizeUsageSummary(detail.usage ?? detail)
  return {
    systemAccountId: detail.systemAccountId,
    systemAccountName: detail.systemAccountName || detail.username || '未知账户',
    requestCount: usage.requestCount,
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    totalTokens: usage.totalTokens,
    totalCost: usage.totalCost,
    lastUsedAt: usage.lastUsedAt
  }
}

export function normalizeAuthorizationUsageResponse(payload: unknown, fallback: ResourceAuthorizationSummary): ResourceAuthorizationSummary {
  if (payload && typeof payload === 'object' && 'authorization' in payload) {
    const response = payload as AuthorizationUsageResponseShape
    const authorization = response.authorization ?? fallback
    const usageBySystemAccount = Array.isArray(response.details)
      ? response.details.map(normalizeUsageDetail).filter((detail): detail is AuthorizationUserUsageDetail => Boolean(detail))
      : authorization.usageBySystemAccount ?? []
    return {
      ...fallback,
      ...authorization,
      usage: normalizeUsageSummary(response.usage ?? authorization.usage ?? fallback.usage),
      usageBySystemAccount
    }
  }
  const authorization = (payload as Partial<ResourceAuthorizationSummary>) ?? {}
  return {
    ...fallback,
    ...authorization,
    usage: normalizeUsageSummary(authorization.usage ?? fallback.usage),
    usageBySystemAccount: Array.isArray(authorization.usageBySystemAccount) ? authorization.usageBySystemAccount : fallback.usageBySystemAccount ?? []
  }
}

export function aggregateUsageBySystemAccount(items: ResourceAuthorizationSummary[]): AuthorizationUserUsageDetail[] {
  const summaryMap = new Map<string, AuthorizationUserUsageDetail>()
  for (const item of items) {
    const current = summaryMap.get(item.granteeSystemAccountId)
    const mergedUsage = sumUsageSummaries([current, item.usage])
    summaryMap.set(item.granteeSystemAccountId, {
      systemAccountId: item.granteeSystemAccountId,
      systemAccountName: item.granteeSystemAccountName || item.granteeUsername || '未知账户',
      requestCount: mergedUsage.requestCount,
      inputTokens: mergedUsage.inputTokens,
      outputTokens: mergedUsage.outputTokens,
      cacheReadTokens: mergedUsage.cacheReadTokens,
      totalTokens: mergedUsage.totalTokens,
      totalCost: mergedUsage.totalCost,
      lastUsedAt: mergedUsage.lastUsedAt
    })
  }
  return [...summaryMap.values()].sort((left, right) => {
    const leftName = left.systemAccountName || '未知账户'
    const rightName = right.systemAccountName || '未知账户'
    return leftName.localeCompare(rightName, 'zh-CN')
  })
}

export { formatDateTime, formatNumber, formatServerDateTimeInput, parseDatePickerValue }
