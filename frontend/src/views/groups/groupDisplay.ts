import { formatCompactUsageAmount, formatDateTime, formatNumber, formatUsd, serverDateTimeTimestamp } from '@/shared/formatters'
import type { AccountUsageSummary, GroupAccountStats, GroupSummary } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'
import { isAuthorizedGroup } from './groupRowActions'

export function groupStats(group?: GroupSummary): GroupAccountStats {
  const stats = group?.accountStats
  return {
    total: normalizedNumber(stats?.total),
    available: normalizedNumber(stats?.available),
    active: normalizedNumber(stats?.active),
    disabled: normalizedNumber(stats?.disabled),
    error: normalizedNumber(stats?.error),
    rateLimited: normalizedNumber(stats?.rateLimited),
    currentConcurrency: normalizedNumber(stats?.currentConcurrency),
    currentConcurrencyAvailable: stats?.currentConcurrencyAvailable,
    concurrencyLimit: normalizedNumber(stats?.concurrencyLimit),
    todayUsage: stats?.todayUsage ?? emptyUsageSummary(),
    usage: stats?.usage ?? emptyUsageSummary()
  }
}

export function groupAccountStatsTooltip(group: GroupSummary): string {
  const stats = groupStats(group)
  return [
    `可用账号：${formatNumber(stats.available)}`,
    `总账号：${formatNumber(stats.total)}`,
    `正常：${formatNumber(stats.active)}`,
    `停用：${formatNumber(stats.disabled)}`,
    `异常：${formatNumber(stats.error)}`,
    `限流：${formatNumber(stats.rateLimited)}`
  ].join('\n')
}

export function groupConcurrencyAvailable(group: GroupSummary): boolean {
  return groupStats(group).currentConcurrencyAvailable !== false
}

export function groupConcurrencyText(group: GroupSummary): string {
  return groupConcurrencyAvailable(group) ? String(groupStats(group).currentConcurrency) : '暂不可用'
}

export function groupConcurrencyTooltip(group: GroupSummary): string {
  return groupConcurrencyAvailable(group) ? '当前正在转发的请求数' : '实时并发快照暂不可用'
}

export function groupStatusText(group: GroupSummary): string {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return '授权暂停'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return '授权到期'
  if (!group.enabled) return '停用'
  if (stats.total === 0) return '未绑定'
  if (stats.available === 0) return '无可用账户'
  return '启用'
}

export function groupStatusColor(group: GroupSummary): string {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return 'orange'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return 'default'
  if (!group.enabled || stats.total === 0) return 'default'
  if (stats.available === 0) return 'orange'
  return 'green'
}

export function groupSystemAccountText(group: GroupSummary): string {
  return systemAccountDisplayText(group)
}

export function groupInfoTooltip(group: GroupSummary): string {
  if (!isAuthorizedGroup(group)) return ''
  return authorizedGroupTooltip(group)
}

export function groupDisplayDescription(group: GroupSummary): string {
  return group.description?.trim() ?? ''
}

export function groupInfoIconClass(group: GroupSummary): string {
  return isAuthorizedGroup(group) ? authorizedGroupIconClass(group) : 'source-normal'
}

export function formatUsageSummary(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
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

function authorizedGroupTooltip(group: GroupSummary): string {
  const ownerName = group.ownerSystemAccountName || '其他用户'
  const expiresText = group.authorizationExpiresAt ? formatDateTime(group.authorizationExpiresAt) : '长期有效'
  const limitsText = quotaLimitSummaryText(group.authorizationLimits)
  const lines = [
    `授权自 ${ownerName}。`,
    `授权来源：${authorizedGroupSourceText(group)}`,
    `授权到期：${expiresText}`,
    `授权限额：${limitsText}`
  ]
  if (group.authorizationStatus === 'expired') {
    lines.push('授权已到期，当前不可用。')
  } else if (group.authorizationStatus === 'paused') {
    lines.push('授权已暂停，当前不可用。')
  }
  return lines.join('\n')
}

function authorizedGroupSourceText(group: GroupSummary): string {
  const activeSources = group.authorizationSources?.filter((source) => source.status === 'active') ?? []
  if (!activeSources.length && group.authorizationSources?.some((source) => source.sourceType === 'team')) {
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

function authorizedGroupIconClass(group: GroupSummary): string {
  return `source-${authorizedGroupSourceTone(group)}`
}

function authorizedGroupSourceTone(group: GroupSummary): 'normal' | 'warning' | 'danger' {
  if (group.authorizationStatus && group.authorizationStatus !== 'active') return 'danger'
  if (isAuthorizationExpiringSoon(group) || hasQuotaLimits(group.authorizationLimits)) return 'warning'
  return 'normal'
}

function isAuthorizationExpiringSoon(group: GroupSummary): boolean {
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
