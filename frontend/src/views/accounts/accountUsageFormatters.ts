import {
  formatCompactUsageAmount,
  formatNumber,
  formatUsd,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import { canCreateOAuthAccount } from './accountProviderCapabilities'

export interface OAuthUsageBar {
  key: string
  label: string
  percent: number
  displayPercent: string
  resetText: string
  color: string
  tone: string
}

export function formatAccountUsageSummary(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

export function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

export function formatCost(value?: number): string {
  return formatUsd(value)
}

export function oauthUsageBars(account: AccountSummary): OAuthUsageBar[] {
  if (account.type !== 'oauth' || !canCreateOAuthAccount({ profile: account })) return []
  const usage = account.oauthUsage
  if (!usage) return []
  return [
    oauthUsageBar('5h', '5h', usage.fiveHour),
    oauthUsageBar('7d', '7d', usage.sevenDay)
  ].filter((bar): bar is OAuthUsageBar => Boolean(bar))
}

export function formatRelativeReset(value: string): string {
  const time = serverDateTimeTimestamp(value)
  if (time === undefined) return '时间格式异常'
  const diffMs = time - Date.now()
  if (diffMs <= 0) return '现在'
  const totalMinutes = Math.ceil(diffMs / 60_000)
  const days = Math.floor(totalMinutes / 1440)
  const hours = Math.floor((totalMinutes % 1440) / 60)
  const minutes = totalMinutes % 60
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

function oauthUsageBar(key: string, label: string, window?: { utilization: number; resetsAt?: string; remainingSeconds: number }): OAuthUsageBar | undefined {
  if (!window) return undefined
  const rawPercent = Math.max(0, window.utilization)
  const percent = Math.min(Math.round(rawPercent), 100)
  return {
    key,
    label,
    percent,
    displayPercent: rawPercent > 999 ? '>999%' : `${Math.round(rawPercent)}%`,
    resetText: window.resetsAt ? formatRelativeReset(window.resetsAt) : '现在',
    color: rawPercent >= 100 ? '#ef4444' : rawPercent >= 80 ? '#f59e0b' : '#22c55e',
    tone: rawPercent >= 100 ? 'danger' : rawPercent >= 80 ? 'warning' : 'normal'
  }
}
