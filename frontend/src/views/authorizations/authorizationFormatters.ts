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

export function granteeSourceLabel(item: ResourceAuthorizationSummary): string | undefined {
  if (item.granteeType === 'team') return '团队'
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
  if (item.granteeType === 'team') return 'gold'
  if (item.effectiveSourceType === 'team') return 'gold'
  if (item.effectiveSourceType === 'manual') return 'cyan'
  return activeTeamSources(item).length > 0 ? 'gold' : 'cyan'
}

export function granteeTargetName(item: ResourceAuthorizationSummary): string {
  if (item.granteeType === 'team') {
    return item.granteeTeamName
      ?? item.effectiveSourceTeamName
      ?? '团队'
  }
  if (item.effectiveSourceType === 'team') {
    const teamSource = item.authorizationSources?.find((source) => source.sourceType === 'team' && source.status === 'active')
    return item.effectiveSourceTeamName
      ?? teamSource?.sourceTeamName
      ?? item.granteeSystemAccountName
      ?? '-'
  }
  return item.granteeSystemAccountName ?? '-'
}

export function authorizationDirection(item: ResourceAuthorizationSummary, currentSystemAccountId?: string): 'outbound' | 'inbound' {
  if (currentSystemAccountId && item.resourceOwnerSystemAccountId !== currentSystemAccountId) {
    return 'inbound'
  }
  return 'outbound'
}

export function authorizationDirectionText(item: ResourceAuthorizationSummary, currentSystemAccountId?: string): string {
  return authorizationDirection(item, currentSystemAccountId) === 'inbound' ? '授权给我' : '我授权出去'
}

export function authorizationDirectionColor(item: ResourceAuthorizationSummary, currentSystemAccountId?: string): string {
  return authorizationDirection(item, currentSystemAccountId) === 'inbound' ? 'blue' : 'purple'
}

export function hasManualSource(item: ResourceAuthorizationSummary): boolean {
  if (item.granteeType === 'team') return false
  return item.authorizationSources?.some((source) => source.sourceType === 'manual' && source.status === 'active') ?? false
}

export function canRevokeAuthorization(item: ResourceAuthorizationSummary): boolean {
  return item.status !== 'revoked' && item.status !== 'returned'
}

export function activeTeamSources(item: ResourceAuthorizationSummary): AuthorizationSourceSummary[] {
  if (item.granteeType === 'team') return []
  return item.authorizationSources?.filter((source) => source.sourceType === 'team' && source.status === 'active' && source.sourceTeamId) ?? []
}

export function authorizationRevokeActionCount(item: ResourceAuthorizationSummary): number {
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
    cacheReadCost: 0,
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
    cacheReadCost: usage?.cacheReadCost ?? 0,
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
      cacheReadCost: summary.cacheReadCost + current.cacheReadCost,
      totalTokens: summary.totalTokens + current.totalTokens,
      totalCost: summary.totalCost + current.totalCost,
      lastUsedAt
    }
  }, emptyUsageSummary())
}

export { formatDateTime, formatNumber, formatServerDateTimeInput, parseDatePickerValue }
