import dayjs from 'dayjs'
import {
  formatCompactUsageAmount,
  formatRequestCountTag,
  formatUsd,
  parseStrictServerDateTime,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import { canCreateOAuthAccount, type AccountProviderProfileLike } from './accountProviderCapabilities'

/** 用量显示仅依赖账户的类型、OAuth 用量投影与 provider/协议画像字段。 */
export type OAuthUsageDisplayAccount = Pick<AccountSummary, 'type' | 'oauthUsage'> & AccountProviderProfileLike

export interface OAuthUsageBar {
  key: string
  label: string
  percent: number
  displayPercent: string
  resetText: string
  color: string
  tone: string
  /** 可选整行 title 提示（Grok：套餐/周期/绝对重置时间/分产品明细）。 */
  tooltip?: string
}

export function formatAccountUsageSummary(usage: AccountUsageSummary): string {
  return `${formatRequestCountTag(usage.requestCount)} / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

export function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

export function formatCost(value?: number): string {
  return formatUsd(value)
}

export function oauthUsageBars(account: OAuthUsageDisplayAccount): OAuthUsageBar[] {
  const grok = grokOAuthUsageBar(account)
  if (grok) return [grok]
  if (account.type !== 'oauth' || !canCreateOAuthAccount({ profile: account })) return []
  const usage = account.oauthUsage
  if (!usage || usage.kind !== 'openai_codex') return []
  return [
    oauthUsageBar('5h', '5h', usage.fiveHour),
    oauthUsageBar('7d', '7d', usage.sevenDay)
  ].filter((bar): bar is OAuthUsageBar => Boolean(bar))
}

/** Grok 订阅周期用量条：与 GPT 窗口条同构（徽章=周期、条=已用百分比、尾部=重置倒计时）。 */
export function grokOAuthUsageBar(account: OAuthUsageDisplayAccount): OAuthUsageBar | undefined {
  if (account.type !== 'oauth') return undefined
  const usage = account.oauthUsage
  if (!usage || usage.kind !== 'xai_grok' || usage.usedPercent === undefined) return undefined
  const rawPercent = Math.max(0, usage.usedPercent)
  const tooltipSegments: string[] = []
  if (usage.subscriptionTier) tooltipSegments.push(usage.subscriptionTier)
  tooltipSegments.push(`${grokPeriodLabel(usage.periodType)}已用 ${Math.round(rawPercent)}%`)
  if (usage.periodEnd) tooltipSegments.push(`${formatGrokPeriodReset(usage.periodEnd)} 重置`)
  const productSummary = grokProductUsageSummary(usage.productUsage)
  if (productSummary) tooltipSegments.push(productSummary)
  return {
    key: 'grok',
    label: grokPeriodBadge(usage.periodType),
    percent: Math.min(Math.round(rawPercent), 100),
    displayPercent: rawPercent > 999 ? '>999%' : `${Math.round(rawPercent)}%`,
    resetText: usage.periodEnd ? formatRelativeReset(usage.periodEnd) : '—',
    color: rawPercent >= 100 ? '#ef4444' : rawPercent >= 80 ? '#f59e0b' : '#22c55e',
    tone: rawPercent >= 100 ? 'danger' : rawPercent >= 80 ? 'warning' : 'normal',
    tooltip: tooltipSegments.join(' · ')
  }
}

/** 周期徽章（24px 内单字）：WEEKLY→周、MONTHLY→月，其他回退“期”。 */
function grokPeriodBadge(periodType?: string): string {
  if (periodType === 'USAGE_PERIOD_TYPE_WEEKLY') return '周'
  if (periodType === 'USAGE_PERIOD_TYPE_MONTHLY') return '月'
  return '期'
}

/** 周期类型规范化：WEEKLY→本周、MONTHLY→本月，其他显示原文；缺失回退“本周期”。 */
export function grokPeriodLabel(periodType?: string): string {
  if (periodType === 'USAGE_PERIOD_TYPE_WEEKLY') return '本周'
  if (periodType === 'USAGE_PERIOD_TYPE_MONTHLY') return '本月'
  return periodType || '本周期'
}

/** 重置时间本地化短格式（MM-DD HH:mm）；非法服务端时间保持显式异常文案。 */
export function formatGrokPeriodReset(value: string): string {
  const date = parseStrictServerDateTime(value)
  if (!date) return '时间格式异常'
  return dayjs(date).format('MM-DD HH:mm')
}

/** 分产品明细摘要：`GrokBuild 13% / GrokChat 1%`；无百分比项只显示产品名。 */
export function grokProductUsageSummary(productUsage?: string): string | undefined {
  if (!productUsage) return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(productUsage)
  } catch {
    return undefined
  }
  if (!Array.isArray(parsed)) return undefined
  const parts: string[] = []
  for (const item of parsed) {
    if (!item || typeof item !== 'object') continue
    const product = (item as { product?: unknown }).product
    const usagePercent = (item as { usagePercent?: unknown }).usagePercent
    if (typeof product !== 'string' || !product) continue
    parts.push(typeof usagePercent === 'number' ? `${product} ${Math.round(usagePercent)}%` : product)
  }
  return parts.length ? parts.join(' / ') : undefined
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
