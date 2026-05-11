import type { AccountUsageDailyPoint, AccountUsageStatsRange, AccountUsageSummary, UsageByWindow, UsageStatsWindowDefinition } from '../domain/types.js'

const dayMs = 24 * 60 * 60 * 1000
export const ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS = 31

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

export function emptyAccountUsageDailyPoint(statDate: string): AccountUsageDailyPoint {
  return {
    statDate,
    ...emptyAccountUsageSummary()
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
    totalTokens: inputTokens + outputTokens + cacheReadTokens,
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

export function normalizeAccountUsageStatsRange(input: { startDate?: string; endDate?: string } = {}): AccountUsageStatsRange {
  const today = startOfLocalDay(new Date())
  const defaultStart = addDays(today, -(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS - 1))
  let end = parseDateKey(input.endDate) ?? today
  if (end > today) {
    end = today
  }
  let start = parseDateKey(input.startDate) ?? defaultStart
  if (start > end) {
    start = end
  }
  const earliestStart = addDays(end, -(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS - 1))
  if (start < earliestStart) {
    start = earliestStart
  }
  return {
    startDate: dateKey(start),
    endDate: dateKey(end),
    days: daysBetweenInclusive(start, end),
    maxDays: ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS
  }
}

export function dateKeysInRange(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): string[] {
  const start = parseDateKey(range.startDate)
  const end = parseDateKey(range.endDate)
  if (!start || !end || start > end) return []
  const days = Math.min(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS, daysBetweenInclusive(start, end))
  return Array.from({ length: days }, (_, index) => dateKey(addDays(start, index)))
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

function parseDateKey(value?: string): Date | undefined {
  if (!value) return undefined
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const date = new Date(year, month - 1, day)
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    return undefined
  }
  return startOfLocalDay(date)
}

function startOfLocalDay(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate())
}

function addDays(date: Date, days: number): Date {
  const next = new Date(date)
  next.setDate(next.getDate() + days)
  return next
}

function daysBetweenInclusive(start: Date, end: Date): number {
  return Math.max(1, Math.floor((startOfLocalDay(end).getTime() - startOfLocalDay(start).getTime()) / dayMs) + 1)
}
