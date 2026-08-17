import {
  compareServerDateTime,
  formatCompactUsageAmount,
  formatDateTime,
  formatNumber,
  formatRequestCountTag,
  formatServerDateTimeInput,
  formatUsd,
  parseStrictDatePickerValue,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type {
  AccountUsageSummary,
  AuthorizationSourceSummary,
  AuthorizationStatus,
  ResourceAuthorizationListItem,
  ResourceAuthorizationSummary
} from '@/types/domain'
export { extractApiErrorMessage } from '@/shared/apiError'
export { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'

export function statusLabel(status: AuthorizationStatus): string {
  if (status === 'active') return '生效中'
  if (status === 'paused') return '已暂停'
  if (status === 'expired') return '授权到期'
  if (status === 'revoked') return '已回收'
  if (status === 'returned') return '已归还'
  return status
}

export function statusTagColor(status: AuthorizationStatus): string {
  if (status === 'active') return 'green'
  if (status === 'paused') return 'orange'
  if (status === 'expired') return 'default'
  if (status === 'revoked') return 'default'
  if (status === 'returned') return 'default'
  return 'default'
}

export function sourceLabel(source: AuthorizationSourceSummary): string {
  const baseLabel = source.sourceType === 'manual'
    ? '个人'
    : '团队'
  if (source.status === 'active') return baseLabel
  if (source.status === 'superseded') return `${baseLabel}（已被团队覆盖）`
  return `${baseLabel}（已回收）`
}

export function sourceTagColor(source: AuthorizationSourceSummary): string {
  if (source.status !== 'active') return 'default'
  return source.sourceType === 'manual' ? 'cyan' : 'gold'
}

export function granteeSourceLabel(item: ResourceAuthorizationListItem): string | undefined {
  if (item.granteeType === 'team') return '团队'
  if (item.effectiveSourceType === 'manual') return '个人'
  if (item.effectiveSourceType === 'team') {
    return '团队'
  }
  const sourceSummary = authorizationSourceSummary(item)
  const manual = sourceSummary.hasManual
  const team = sourceSummary.hasTeam
  if (manual && team) return '个人+团队'
  if (manual) return '个人'
  if (team) return '团队'
  return undefined
}

export function granteeSourceTagColor(item: ResourceAuthorizationListItem): string {
  if (item.granteeType === 'team') return 'gold'
  if (item.effectiveSourceType === 'team') return 'gold'
  if (item.effectiveSourceType === 'manual') return 'cyan'
  return activeTeamSources(item).length > 0 ? 'gold' : 'cyan'
}

export function granteeTargetName(item: ResourceAuthorizationListItem): string {
  if (item.granteeType === 'team') {
    return item.granteeTeamName
      ?? item.effectiveSourceTeamName
      ?? '团队'
  }
  if (item.effectiveSourceType === 'team') {
    const teamSource = authorizationSourceSummary(item).teamSources[0]
    return item.effectiveSourceTeamName
      ?? teamSource?.sourceTeamName
      ?? item.granteeSystemAccountName
      ?? '-'
  }
  return item.granteeSystemAccountName ?? '-'
}

export function authorizationDirection(item: ResourceAuthorizationListItem, currentSystemAccountId?: string): 'outbound' | 'inbound' {
  if (currentSystemAccountId && item.resourceOwnerSystemAccountId !== currentSystemAccountId) {
    return 'inbound'
  }
  return 'outbound'
}

export function authorizationDirectionText(item: ResourceAuthorizationListItem, currentSystemAccountId?: string): string {
  return authorizationDirection(item, currentSystemAccountId) === 'inbound' ? '授权给我' : '我授权出去'
}

export function authorizationDirectionColor(item: ResourceAuthorizationListItem, currentSystemAccountId?: string): string {
  return authorizationDirection(item, currentSystemAccountId) === 'inbound' ? 'blue' : 'purple'
}

export function hasManualSource(item: ResourceAuthorizationListItem): boolean {
  if (item.granteeType === 'team') return false
  return authorizationSourceSummary(item).hasManual
}

export function canRevokeAuthorization(item: ResourceAuthorizationListItem): boolean {
  return item.status !== 'revoked' && item.status !== 'returned'
}

export function activeTeamSources(item: ResourceAuthorizationListItem): Array<Pick<AuthorizationSourceSummary, 'sourceTeamId' | 'sourceTeamName'>> {
  if (item.granteeType === 'team') return []
  return authorizationSourceSummary(item).teamSources
}

export function authorizationRevokeActionCount(item: ResourceAuthorizationListItem): number {
  if (!canRevokeAuthorization(item)) return 0
  if (item.granteeType === 'team') return 1
  const sourceActionCount = (hasManualSource(item) ? 1 : 0) + activeTeamSources(item).length
  return Math.max(1, sourceActionCount)
}

export function usageSummaryText(usage?: {
  requestCount?: number
  totalTokens?: number
  totalCost?: number
}): string {
  return `${formatRequestCountTag(usage?.requestCount)} / ${formatUsageAmount(usage?.totalTokens)} / ${formatCost(usage?.totalCost)}`
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
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

export function normalizeUsageSummary(usage?: Partial<AccountUsageSummary>): AccountUsageSummary {
  return {
    requestCount: numberValue(usage?.requestCount),
    inputTokens: numberValue(usage?.inputTokens),
    outputTokens: numberValue(usage?.outputTokens),
    cacheReadTokens: numberValue(usage?.cacheReadTokens),
    cacheReadCost: numberValue(usage?.cacheReadCost),
    cacheWriteTokens: numberValue(usage?.cacheWriteTokens),
    cacheWrite1hTokens: numberValue(usage?.cacheWrite1hTokens),
    cacheWriteCost: numberValue(usage?.cacheWriteCost),
    thinkingTokens: numberValue(usage?.thinkingTokens),
    inputImageTokens: numberValue(usage?.inputImageTokens),
    outputImageTokens: numberValue(usage?.outputImageTokens),
    totalTokens: numberValue(usage?.totalTokens),
    totalCost: numberValue(usage?.totalCost),
    lastUsedAt: usage?.lastUsedAt
  }
}

export function sumUsageSummaries(items: Array<Partial<AccountUsageSummary> | undefined>): AccountUsageSummary {
  return items.reduce<AccountUsageSummary>((summary, usage) => {
    const current = normalizeUsageSummary(usage)
    const lastUsedAt = [summary.lastUsedAt, current.lastUsedAt]
      .filter((value): value is string => Boolean(value) && serverDateTimeTimestamp(value) !== undefined)
      .sort((left, right) => compareServerDateTime(right, left))[0]
    return {
      requestCount: summary.requestCount + current.requestCount,
      inputTokens: summary.inputTokens + current.inputTokens,
      outputTokens: summary.outputTokens + current.outputTokens,
      cacheReadTokens: summary.cacheReadTokens + current.cacheReadTokens,
      cacheReadCost: summary.cacheReadCost + current.cacheReadCost,
      cacheWriteTokens: summary.cacheWriteTokens + current.cacheWriteTokens,
      cacheWrite1hTokens: summary.cacheWrite1hTokens + current.cacheWrite1hTokens,
      cacheWriteCost: summary.cacheWriteCost + current.cacheWriteCost,
      thinkingTokens: summary.thinkingTokens + current.thinkingTokens,
      inputImageTokens: summary.inputImageTokens + current.inputImageTokens,
      outputImageTokens: summary.outputImageTokens + current.outputImageTokens,
      totalTokens: summary.totalTokens + current.totalTokens,
      totalCost: summary.totalCost + current.totalCost,
      lastUsedAt
    }
  }, emptyUsageSummary())
}

export { formatDateTime, formatNumber, formatServerDateTimeInput, parseStrictDatePickerValue }

function numberValue(value: unknown): number {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : 0
}

function authorizationSourceSummary(item: ResourceAuthorizationListItem | ResourceAuthorizationSummary) {
  if (item.sourceSummary) return item.sourceSummary
  const activeSources = 'authorizationSources' in item
    ? item.authorizationSources?.filter((source) => source.status === 'active') ?? []
    : []
  return {
    activeSourceCount: activeSources.length,
    hasManual: activeSources.some((source) => source.sourceType === 'manual'),
    hasTeam: activeSources.some((source) => source.sourceType === 'team'),
    teamSources: activeSources
      .filter((source) => source.sourceType === 'team' && Boolean(source.sourceTeamId))
      .map((source) => ({
        sourceTeamId: source.sourceTeamId!,
        sourceTeamName: source.sourceTeamName
      }))
  }
}
