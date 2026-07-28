import { formatCompactUsageAmount, formatDateTime, formatNumber, formatRequestCountTag, formatUsd, serverDateTimeTimestamp } from '@/shared/formatters'
import type { AccountUsageSummary, GroupAccountStats, GroupListItem, GroupSummary } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'
import { isAuthorizedGroup } from './groupRowActions'

type GroupRow = GroupListItem | GroupSummary

export function groupStats(group?: GroupRow): GroupAccountStats & { currentConcurrency: number; todayUsage: AccountUsageSummary } {
  const stats = group?.accountStats
  return {
    total: normalizedNumber(stats?.total),
    available: normalizedNumber(stats?.available),
    active: normalizedNumber(stats?.active),
    disabled: normalizedNumber(stats?.disabled),
    error: normalizedNumber(stats?.error),
    rateLimited: normalizedNumber(stats?.rateLimited),
    currentConcurrency: normalizedNumber(stats?.currentConcurrency),
    concurrencyLimit: normalizedNumber(stats?.concurrencyLimit),
    todayUsage: stats?.todayUsage ?? emptyUsageSummary(),
    usage: stats && 'usage' in stats ? stats.usage : emptyUsageSummary()
  }
}

export function groupAccountStatsTooltip(group: GroupRow): string {
  const stats = groupStats(group)
  return [
    `可用账号：${formatNumber(stats.available)}`,
    `总账号：${formatNumber(stats.total)}`,
    `可调度：${formatNumber(stats.active)}`,
    `停用：${formatNumber(stats.disabled)}`,
    `异常：${formatNumber(stats.error)}`,
    `限流：${formatNumber(stats.rateLimited)}`
  ].join('\n')
}

export function groupConcurrencyText(group: GroupRow): string {
  return String(groupStats(group).currentConcurrency)
}

export function groupConcurrencyTooltip(group: GroupRow): string {
  return '当前正在转发的请求数'
}

export function groupStatusText(group: GroupRow): string {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return '授权暂停'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return '授权到期'
  if (!group.enabled) return '停用'
  if (stats.total === 0) return '未绑定'
  if (stats.available === 0) return '无可用账户'
  return '启用'
}

export function groupStatusColor(group: GroupRow): string {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return 'orange'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return 'default'
  if (!group.enabled || stats.total === 0) return 'default'
  if (stats.available === 0) return 'orange'
  return 'green'
}

export function groupSystemAccountText(group: GroupRow): string {
  return systemAccountDisplayText(group)
}

export function groupInfoTooltip(group: GroupRow): string {
  if (!isAuthorizedGroup(group)) return ''
  return authorizedGroupTooltip(group)
}

export function groupDisplayDescription(group: GroupRow): string {
  return group.description?.trim() ?? ''
}

export function groupInfoIconClass(group: GroupRow): string {
  return isAuthorizedGroup(group) ? authorizedGroupIconClass(group) : 'source-normal'
}

export function formatUsageSummary(usage: AccountUsageSummary): string {
  return `${formatRequestCountTag(usage.requestCount)}/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
}

function normalizedNumber(value: unknown): number {
  const numberValue = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}

function emptyUsageSummary(): AccountUsageSummary {
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

function authorizedGroupTooltip(group: GroupRow): string {
  const ownerName = group.ownerSystemAccountName || '其他用户'
  const expiresText = group.authorizationExpiresAt ? formatDateTime(group.authorizationExpiresAt) : '长期有效'
  const lines = [
    `授权自 ${ownerName}。`,
    `授权来源：${authorizedGroupSourceText(group)}`,
    `授权到期：${expiresText}`
  ]
  if ('authorizationLimits' in group) {
    lines.push(`授权限额：${quotaLimitSummaryText(group.authorizationLimits)}`)
  }
  if (group.authorizationStatus === 'expired') {
    lines.push('授权已到期，当前不可用。')
  } else if (group.authorizationStatus === 'paused') {
    lines.push('授权已暂停，当前不可用。')
  }
  return lines.join('\n')
}

function authorizedGroupSourceText(group: GroupRow): string {
  const authorizationSources = 'authorizationSources' in group ? group.authorizationSources : undefined
  const activeSources = authorizationSources?.filter((source) => source.status === 'active') ?? []
  const sourceSummary = group.authorizationSourceSummary
  if (!activeSources.length && sourceSummary) {
    if (sourceSummary.hasManual && sourceSummary.hasTeam) {
      return sourceSummary.teamNames.length ? `个人授权 + 团队授权（${sourceSummary.teamNames.join('、')}）` : '个人授权 + 团队授权'
    }
    if (sourceSummary.hasTeam) {
      return sourceSummary.teamNames.length ? `团队授权（${sourceSummary.teamNames.join('、')}）` : '团队授权'
    }
    if (sourceSummary.hasManual) {
      return '个人授权'
    }
  }
  if (!activeSources.length && authorizationSources?.some((source) => source.sourceType === 'team')) {
    return '团队授权'
  }
  const hasManual = activeSources.some((source) => source.sourceType === 'manual')
  const teamSources = activeSources.filter((source) => source.sourceType === 'team')
  const teamNames = teamSources.map((source) => source.sourceTeamName).filter((name): name is string => Boolean(name))
  if (hasManual && teamSources.length) {
    return teamNames.length ? `个人授权 + 团队授权（${teamNames.join('、')}）` : '个人授权 + 团队授权'
  }
  if (teamSources.length) {
    return teamNames.length ? `团队授权（${teamNames.join('、')}）` : '团队授权'
  }
  return '个人授权'
}

function authorizedGroupIconClass(group: GroupRow): string {
  return `source-${authorizedGroupSourceTone(group)}`
}

function authorizedGroupSourceTone(group: GroupRow): 'normal' | 'warning' | 'danger' {
  if (group.authorizationStatus && group.authorizationStatus !== 'active') return 'danger'
  if (isAuthorizationExpiringSoon(group)) return 'warning'
  if ('authorizationLimits' in group && hasQuotaLimits(group.authorizationLimits)) return 'warning'
  return 'normal'
}

function isAuthorizationExpiringSoon(group: GroupRow): boolean {
  if (!group.authorizationExpiresAt) return false
  const timestamp = serverDateTimeTimestamp(group.authorizationExpiresAt)
  if (timestamp === undefined) return false
  const remainingMs = timestamp - Date.now()
  return remainingMs > 0 && remainingMs <= 3 * 24 * 60 * 60 * 1000
}

function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

function formatCost(value?: number): string {
  return formatUsd(value)
}
