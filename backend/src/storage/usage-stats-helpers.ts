import type { AccountUsageSummary, UsageByWindow, UsageStatsWindowDefinition } from '../domain/types.js'

export const USAGE_STATS_WINDOWS: UsageStatsWindowDefinition[] = [
  { key: 'last1d', label: '近1天', days: 1 },
  { key: 'last3d', label: '近3天', days: 3 },
  { key: 'last7d', label: '近一周', days: 7 },
  { key: 'last15d', label: '近半月', days: 15 },
  { key: 'last30d', label: '近一月', days: 30 },
  { key: 'total', label: '总用量' }
]

export function emptyAccountUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

export function usageSummaryFromAggregate(row: {
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
}): AccountUsageSummary {
  const inputTokens = Number(row.input_tokens ?? 0)
  const outputTokens = Number(row.output_tokens ?? 0)
  const cacheReadTokens = Number(row.cache_read_tokens ?? 0)
  return {
    requestCount: Number(row.request_count ?? 0),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row.total_cost ?? 0),
    lastUsedAt: row.last_used_at ?? undefined
  }
}

export function emptyUsageByWindow(): UsageByWindow {
  return Object.fromEntries(USAGE_STATS_WINDOWS.map((window) => [window.key, emptyAccountUsageSummary()])) as UsageByWindow
}

export function addUsageSummaries(left: AccountUsageSummary | undefined, right: AccountUsageSummary | undefined): AccountUsageSummary {
  const leftUsage = left ?? emptyAccountUsageSummary()
  const rightUsage = right ?? emptyAccountUsageSummary()
  const lastUsedAt = [leftUsage.lastUsedAt, rightUsage.lastUsedAt]
    .filter((value): value is string => Boolean(value))
    .sort((leftValue, rightValue) => Date.parse(rightValue) - Date.parse(leftValue))[0]
  return {
    requestCount: leftUsage.requestCount + rightUsage.requestCount,
    inputTokens: leftUsage.inputTokens + rightUsage.inputTokens,
    outputTokens: leftUsage.outputTokens + rightUsage.outputTokens,
    cacheReadTokens: leftUsage.cacheReadTokens + rightUsage.cacheReadTokens,
    totalTokens: leftUsage.totalTokens + rightUsage.totalTokens,
    totalCost: leftUsage.totalCost + rightUsage.totalCost,
    lastUsedAt
  }
}

export function mergeUsageSummaryMaps(...maps: Array<Map<string, AccountUsageSummary>>): Map<string, AccountUsageSummary> {
  const result = new Map<string, AccountUsageSummary>()
  for (const usageMap of maps) {
    for (const [id, usage] of usageMap) {
      result.set(id, addUsageSummaries(result.get(id), usage))
    }
  }
  return result
}

export function todayDateKey(): string {
  return dateKey()
}

export function dateKey(date = new Date()): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

export function hourKey(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hour = String(date.getHours()).padStart(2, '0')
  return `${year}-${month}-${day}T${hour}`
}

export function numberFromUnknown(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

export function averageFromSum(sum: unknown, count: unknown): number | undefined {
  const numericSum = Number(sum ?? 0)
  const numericCount = Number(count ?? 0)
  return numericCount > 0 ? Math.round(numericSum / numericCount) : undefined
}
