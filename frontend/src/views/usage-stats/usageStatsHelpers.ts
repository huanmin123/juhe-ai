import { formatDateKey, parseDateKey } from '@/shared/dateRange'
import { formatDateTime } from '@/shared/formatters'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { AccountOptionSummary, AccountUsageStatsRow, AccountUsageSummary } from '@/types/domain'
import { formatCompactInteger, formatCost, formatInteger, formatPercent, formatSeconds } from '@/views/stats/statsFormatters'
import type { StatsSummaryCardItem } from '@/views/stats/StatsSummaryCards.vue'
export { metricText, metricValue } from './usageTrendMetrics'
export type { UsageTrendMetric } from './usageTrendMetrics'

export function buildAccountUsageSummaryCards(options: {
  summary?: AccountUsageSummary
  statsLagSeconds?: number
}): StatsSummaryCardItem[] {
  const summary = options.summary
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `统计滞后 ${formatSeconds(options.statsLagSeconds)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出 ${formatCompactInteger(summary?.outputTokens)} / 缓存读 ${formatCompactInteger(summary?.cacheReadTokens)} / 缓存写 ${formatCompactInteger(summary?.cacheWriteTokens)}` },
    { key: 'cacheRate', label: '缓存率', value: formatPercent(cacheReadRate(summary)), extra: `写入 1h ${formatCompactInteger(summary?.cacheWrite1hTokens)} / 思考 ${formatCompactInteger(summary?.thinkingTokens)} / 图片 ${formatCompactInteger((summary?.inputImageTokens ?? 0) + (summary?.outputImageTokens ?? 0))}` },
    { key: 'cost', label: '成本', value: formatCost(summary?.totalCost), extra: `最后使用 ${formatDateTime(summary?.lastUsedAt)}` }
  ]
}

export function cacheReadRate(summary?: AccountUsageSummary): number {
  const inputTokens = summary?.inputTokens ?? 0
  if (inputTokens <= 0) return 0
  return ((summary?.cacheReadTokens ?? 0) / inputTokens) * 100
}

export function aggregateUsageSummaries(summaries: AccountUsageSummary[]): AccountUsageSummary {
  const summary = zeroUsageSummary()
  let lastUsedAt: string | undefined
  for (const item of summaries) {
    summary.requestCount += item.requestCount
    summary.inputTokens += item.inputTokens
    summary.outputTokens += item.outputTokens
    summary.cacheReadTokens += item.cacheReadTokens
    summary.cacheReadCost += item.cacheReadCost
    summary.cacheWriteTokens += item.cacheWriteTokens
    summary.cacheWrite1hTokens += item.cacheWrite1hTokens
    summary.cacheWriteCost += item.cacheWriteCost
    summary.thinkingTokens += item.thinkingTokens
    summary.inputImageTokens += item.inputImageTokens
    summary.outputImageTokens += item.outputImageTokens
    summary.totalCost += item.totalCost
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
