import { formatDateKey, parseDateKey } from '@/shared/dateRange'
import { formatDateTime } from '@/shared/formatters'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { AccountOptionSummary, AccountUsageStatsRow, AccountUsageSummary } from '@/types/domain'
import { formatCompactInteger, formatCost, formatInteger, formatPercent } from '@/views/stats/statsFormatters'
import type { StatsSummaryCardItem } from '@/views/stats/StatsSummaryCards.vue'
export { metricText, metricValue } from './usageTrendMetrics'
export type { UsageTrendMetric } from './usageTrendMetrics'

export function buildAccountUsageSummaryCards(options: {
  summary?: AccountUsageSummary
}): StatsSummaryCardItem[] {
  const summary = options.summary
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `最后使用 ${formatDateTime(summary?.lastUsedAt)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出 ${formatCompactInteger(summary?.outputTokens)}` },
    { key: 'cacheReadRate', label: '缓存读占比', value: formatPercent(cacheReadRate(summary)), extra: cacheReadSummaryExtra(summary) },
    { key: 'cost', label: '成本', value: formatCost(summary?.totalCost), extra: costEfficiencyExtra(summary) }
  ]
}

export function cacheReadRate(summary?: AccountUsageSummary, providerCode?: string): number {
  const inputTokens = positiveUsageNumber(summary?.inputTokens)
  const cacheReadTokens = positiveUsageNumber(summary?.cacheReadTokens)
  if (cacheReadTokens <= 0) return 0
  const cacheWriteTokens = positiveUsageNumber(summary?.cacheWriteTokens)
  const denominator = cacheReadRateDenominator({ inputTokens, cacheReadTokens, cacheWriteTokens }, providerCode)
  if (denominator <= 0) return 0
  return (cacheReadTokens / denominator) * 100
}

export function aggregateUsageSummaries(summaries: AccountUsageSummary[]): AccountUsageSummary {
  const summary = zeroUsageSummary()
  let lastUsedAt: string | undefined
  for (const item of summaries) {
    summary.requestCount += positiveUsageNumber(item.requestCount)
    summary.inputTokens += positiveUsageNumber(item.inputTokens)
    summary.outputTokens += positiveUsageNumber(item.outputTokens)
    summary.cacheReadTokens += positiveUsageNumber(item.cacheReadTokens)
    summary.cacheReadCost += positiveUsageNumber(item.cacheReadCost)
    summary.cacheWriteTokens += positiveUsageNumber(item.cacheWriteTokens)
    summary.cacheWrite1hTokens += positiveUsageNumber(item.cacheWrite1hTokens)
    summary.cacheWriteCost += positiveUsageNumber(item.cacheWriteCost)
    summary.thinkingTokens += positiveUsageNumber(item.thinkingTokens)
    summary.inputImageTokens += positiveUsageNumber(item.inputImageTokens)
    summary.outputImageTokens += positiveUsageNumber(item.outputImageTokens)
    summary.totalCost += positiveUsageNumber(item.totalCost)
    if (item.lastUsedAt && (!lastUsedAt || item.lastUsedAt > lastUsedAt)) {
      lastUsedAt = item.lastUsedAt
    }
  }
  summary.totalTokens = summary.inputTokens + summary.outputTokens
  summary.lastUsedAt = lastUsedAt
  return summary
}

export function zeroUsageSummary(): AccountUsageSummary {
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

export function usageTrendDateKeys(range: readonly [string, string]): string[] {
  const [startDate, endDate] = range
  const start = parseDateKey(startDate)
  const end = parseDateKey(endDate)
  if (!start || !end || start.isAfter(end, 'day')) return []
  const keys: string[] = []
  for (let current = start.startOf('day'); current.isSame(end, 'day') || current.isBefore(end, 'day'); current = current.add(1, 'day')) {
    keys.push(formatDateKey(current))
  }
  return keys
}

export function placeholderTrendRow(id: string, options: {
  accountOptionById: Map<string, AccountOptionSummary>
  addedTrendSelectionById: Map<string, AccountSelection>
  dateKeys: string[]
}): AccountUsageStatsRow | undefined {
  const option = options.accountOptionById.get(id)
  if (!option?.name?.trim() || !option.providerCode || !option.type || !option.status) return undefined
  const ownerSystemAccountId = option.ownerSystemAccountId ?? option.systemAccountId
  if (!ownerSystemAccountId) return undefined
  const selection = options.addedTrendSelectionById.get(id)
  return {
    id,
    systemAccountId: option.systemAccountId,
    systemAccountName: option.systemAccountName,
    ownerSystemAccountId,
    ownerSystemAccountName: option.ownerSystemAccountName ?? selection?.ownerSystemAccountName,
    providerCode: option.providerCode,
    name: option.name.trim(),
    type: option.type,
    status: option.status,
    accessType: option.accessType ?? selection?.accessType,
    rangeUsage: zeroUsageSummary(),
    dailyUsage: options.dateKeys.map((statDate) => ({ ...zeroUsageSummary(), statDate })),
    authorizationUsageAvailable: false,
    authorizationCount: 0,
    authorizationTeamCount: 0
  }
}

export function dedupeRowsById<T extends { id: string }>(items: T[]): T[] {
  const seen = new Set<string>()
  const result: T[] = []
  for (const item of items) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    result.push(item)
  }
  return result
}

export function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

export function authorizationAccountTagText(account: Pick<AccountUsageStatsRow, 'ownerSystemAccountName'>): string {
  const ownerName = account.ownerSystemAccountName?.trim()
  return ownerName ? `来自：${ownerName}` : '来自授权'
}

function cacheReadSummaryExtra(summary?: AccountUsageSummary): string {
  const cacheReadTokens = positiveUsageNumber(summary?.cacheReadTokens)
  const cacheWrite1hTokens = positiveUsageNumber(summary?.cacheWrite1hTokens)
  const cacheWriteStandardTokens = Math.max(positiveUsageNumber(summary?.cacheWriteTokens) - cacheWrite1hTokens, 0)
  const parts: string[] = []
  if (cacheReadTokens > 0) parts.push(`缓存读 ${formatCompactInteger(cacheReadTokens)}`)
  if (cacheWriteStandardTokens > 0) parts.push(`写入 ${formatCompactInteger(cacheWriteStandardTokens)}`)
  if (cacheWrite1hTokens > 0) parts.push(`1h 写 ${formatCompactInteger(cacheWrite1hTokens)}`)
  return parts.length ? parts.join(' / ') : '暂无缓存读写'
}

function costEfficiencyExtra(summary?: AccountUsageSummary): string {
  const totalCost = positiveUsageNumber(summary?.totalCost)
  const requestCount = positiveUsageNumber(summary?.requestCount)
  const totalTokens = positiveUsageNumber(summary?.totalTokens)
  const averageRequestCost = requestCount > 0 ? totalCost / requestCount : undefined
  const costPerMillionTokens = totalTokens > 0 ? (totalCost / totalTokens) * 1_000_000 : undefined
  return `均次 ${formatOptionalCost(averageRequestCost)} / 每 1M Token ${formatOptionalCost(costPerMillionTokens)}`
}

function formatOptionalCost(value?: number): string {
  return typeof value === 'number' && Number.isFinite(value) ? formatCost(value) : '-'
}

function cacheReadRateDenominator(
  usage: Pick<AccountUsageSummary, 'inputTokens' | 'cacheReadTokens' | 'cacheWriteTokens'>,
  providerCode?: string
): number {
  const inputTokens = positiveUsageNumber(usage.inputTokens)
  const cacheReadTokens = positiveUsageNumber(usage.cacheReadTokens)
  const cacheWriteTokens = positiveUsageNumber(usage.cacheWriteTokens)
  const normalizedProvider = providerCode?.trim().toLowerCase()
  if (normalizedProvider === 'anthropic' || inputTokens <= 0 || cacheReadTokens > inputTokens) {
    return inputTokens + cacheReadTokens + cacheWriteTokens
  }
  return inputTokens
}

function positiveUsageNumber(value?: unknown): number {
  const numericValue = Number(value ?? 0)
  return Number.isFinite(numericValue) ? Math.max(numericValue, 0) : 0
}
