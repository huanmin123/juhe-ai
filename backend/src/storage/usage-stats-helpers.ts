import type { AccountUsageDailyPoint, AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { getDatabase } from './database.js'

const hourMs = 60 * 60 * 1000
const dayMs = 24 * hourMs
export const ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS = 31
export const DEFAULT_USAGE_STATS_TIMEZONE = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const usageStatsTimezoneCacheTtlMs = 60_000
let cachedUsageStatsTimezone: { value: string; expiresAtMs: number } | undefined

interface DateParts {
  year: number
  month: number
  day: number
  hour: number
  minute: number
}

interface DateKeyParts {
  year: number
  month: number
  day: number
}

export interface UsageStatsBucketPlan {
  monthly: string[]
  weekly: string[]
  daily: string[]
}

export function emptyAccountUsageSummary(): AccountUsageSummary {
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
  cache_read_cost?: number
  cache_read_cost_usd?: number
  total_cost: number
  last_used_at: string | null
}): AccountUsageSummary {
  const inputTokens = Number(row.input_tokens ?? 0)
  const outputTokens = Number(row.output_tokens ?? 0)
  const cacheReadTokens = Number(row.cache_read_tokens ?? 0)
  const cacheReadCost = Number(row.cache_read_cost ?? row.cache_read_cost_usd ?? 0)
  return {
    requestCount: Number(row.request_count ?? 0),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCost,
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row.total_cost ?? 0),
    lastUsedAt: row.last_used_at ?? undefined
  }
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
    cacheReadCost: leftUsage.cacheReadCost + rightUsage.cacheReadCost,
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

export function todayDateKey(timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  return dateKey(undefined, timezone)
}

export function dateKey(date = new Date(), timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  const { year, month, day } = zonedDateParts(date, timezone)
  return `${year}-${two(month)}-${two(day)}`
}

export function startOfZonedDateKeyIso(dateKey: string, timezone = DEFAULT_USAGE_STATS_TIMEZONE): string | undefined {
  const target = parseDateKeyParts(dateKey)
  if (!target) return undefined

  const normalizedTimezone = normalizeUsageStatsTimezone(timezone)
  const formatter = new Intl.DateTimeFormat('en-CA', {
    timeZone: normalizedTimezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  })
  const targetMidnightUtc = utcDateStartMs(target.year, target.month, target.day)
  let low = targetMidnightUtc - 48 * hourMs
  let high = targetMidnightUtc + 48 * hourMs

  for (let guard = 0; guard < 8 && compareZonedDateKeyAt(formatter, low, dateKey) >= 0; guard += 1) {
    high = low
    low -= 48 * hourMs
  }
  for (let guard = 0; guard < 8 && compareZonedDateKeyAt(formatter, high, dateKey) < 0; guard += 1) {
    low = high + 1
    high += 48 * hourMs
  }
  if (compareZonedDateKeyAt(formatter, high, dateKey) < 0) return undefined

  while (low < high) {
    const mid = Math.floor((low + high) / 2)
    if (compareZonedDateKeyAt(formatter, mid, dateKey) >= 0) {
      high = mid
    } else {
      low = mid + 1
    }
  }

  return new Date(low).toISOString()
}

export function normalizeAccountUsageStatsRange(input: { startDate?: string; endDate?: string } = {}, timezone = DEFAULT_USAGE_STATS_TIMEZONE): AccountUsageStatsRange {
  const todayKey = dateKey(undefined, timezone)
  const today = parseDateKey(todayKey) ?? startOfLocalDay(new Date())
  const earliestSupportedDate = addDays(today, -(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS - 1))
  const defaultStart = today
  let end = parseDateKey(input.endDate) ?? today
  if (end > today) {
    end = today
  }
  if (end < earliestSupportedDate) {
    end = earliestSupportedDate
  }
  let start = parseDateKey(input.startDate) ?? defaultStart
  if (start > today) {
    start = today
  }
  if (start < earliestSupportedDate) {
    start = earliestSupportedDate
  }
  if (start > end) {
    start = end
  }
  const earliestStart = addDays(end, -(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS - 1))
  if (start < earliestStart) {
    start = earliestStart
  }
  return {
    startDate: localDateKey(start),
    endDate: localDateKey(end),
    days: daysBetweenInclusive(start, end),
    maxDays: ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS
  }
}

export function dateKeysInRange(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): string[] {
  const start = parseDateKey(range.startDate)
  const end = parseDateKey(range.endDate)
  if (!start || !end || start > end) return []
  const days = Math.min(ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS, daysBetweenInclusive(start, end))
  return Array.from({ length: days }, (_, index) => localDateKey(addDays(start, index)))
}

export function hourKey(date: Date, timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  const { year, month, day, hour } = zonedDateParts(date, timezone)
  return `${year}-${two(month)}-${two(day)}T${two(hour)}`
}

export function minuteKey(date: Date, timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  const { year, month, day, hour, minute } = zonedDateParts(date, timezone)
  return `${year}-${two(month)}-${two(day)}T${two(hour)}:${two(minute)}`
}

export function weekKey(date: Date, timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  const { year, month, day } = zonedDateParts(date, timezone)
  return localDateKey(startOfWeekMonday(new Date(year, month - 1, day)))
}

export function monthKey(date: Date, timezone = DEFAULT_USAGE_STATS_TIMEZONE): string {
  const { year, month } = zonedDateParts(date, timezone)
  return `${year}-${two(month)}`
}

export function usageStatsTimezone(): string {
  const nowMs = Date.now()
  if (cachedUsageStatsTimezone && cachedUsageStatsTimezone.expiresAtMs > nowMs) {
    return cachedUsageStatsTimezone.value
  }
  const row = getDatabase().prepare("SELECT value_json FROM system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone'").get() as unknown as { value_json?: string } | undefined
  if (!row?.value_json) {
    return cacheUsageStatsTimezone(DEFAULT_USAGE_STATS_TIMEZONE, nowMs)
  }
  try {
    const value = JSON.parse(row.value_json) as unknown
    return cacheUsageStatsTimezone(normalizeUsageStatsTimezone(value), nowMs)
  } catch {
    return cacheUsageStatsTimezone(DEFAULT_USAGE_STATS_TIMEZONE, nowMs)
  }
}

export function clearUsageStatsTimezoneCache(): void {
  cachedUsageStatsTimezone = undefined
}

function cacheUsageStatsTimezone(value: string, nowMs = Date.now()): string {
  cachedUsageStatsTimezone = {
    value,
    expiresAtMs: nowMs + usageStatsTimezoneCacheTtlMs
  }
  return value
}

export function normalizeUsageStatsTimezone(value: unknown): string {
  const timezone = typeof value === 'string' && value.trim() ? value.trim() : DEFAULT_USAGE_STATS_TIMEZONE
  try {
    new Intl.DateTimeFormat('en-CA', { timeZone: timezone }).format(new Date())
    return timezone
  } catch {
    return DEFAULT_USAGE_STATS_TIMEZONE
  }
}

export function usageStatsBucketPlan(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): UsageStatsBucketPlan {
  const start = parseDateKey(range.startDate)
  const end = parseDateKey(range.endDate)
  const plan: UsageStatsBucketPlan = { monthly: [], weekly: [], daily: [] }
  if (!start || !end || start > end) return plan

  let cursor = start
  while (cursor <= end) {
    if (isFirstDayOfMonth(cursor)) {
      const monthEnd = endOfMonth(cursor)
      if (monthEnd <= end) {
        plan.monthly.push(`${cursor.getFullYear()}-${two(cursor.getMonth() + 1)}`)
        cursor = addDays(monthEnd, 1)
        continue
      }
    }
    const weekStart = startOfWeekMonday(cursor)
    const weekEnd = addDays(weekStart, 6)
    if (cursor.getTime() === weekStart.getTime() && weekEnd <= end) {
      plan.weekly.push(localDateKey(weekStart))
      cursor = addDays(weekEnd, 1)
      continue
    }
    plan.daily.push(localDateKey(cursor))
    cursor = addDays(cursor, 1)
  }
  return plan
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
  const parts = parseDateKeyParts(value)
  if (!parts) return undefined
  const { year, month, day } = parts
  const date = new Date(year, month - 1, day)
  return startOfLocalDay(date)
}

function parseDateKeyParts(value?: string): DateKeyParts | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value ?? '')
  if (!match) return undefined
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const date = new Date(0)
  date.setUTCHours(0, 0, 0, 0)
  date.setUTCFullYear(year, month - 1, day)
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) {
    return undefined
  }
  return { year, month, day }
}

function zonedDateParts(date: Date, timezone: string): DateParts {
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone || DEFAULT_USAGE_STATS_TIMEZONE,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23'
    }).formatToParts(date)
    const value = (type: string) => parts.find((part) => part.type === type)?.value ?? '00'
    return {
      year: Number(value('year')),
      month: Number(value('month')),
      day: Number(value('day')),
      hour: Number(value('hour')),
      minute: Number(value('minute'))
    }
  } catch {
    return {
      year: date.getFullYear(),
      month: date.getMonth() + 1,
      day: date.getDate(),
      hour: date.getHours(),
      minute: date.getMinutes()
    }
  }
}

function startOfLocalDay(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate())
}

function addDays(date: Date, days: number): Date {
  const next = new Date(date)
  next.setDate(next.getDate() + days)
  return next
}

function startOfWeekMonday(date: Date): Date {
  const start = startOfLocalDay(date)
  const dayOfWeek = start.getDay()
  start.setDate(start.getDate() - (dayOfWeek === 0 ? 6 : dayOfWeek - 1))
  return start
}

function isFirstDayOfMonth(date: Date): boolean {
  return date.getDate() === 1
}

function endOfMonth(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth() + 1, 0)
}

function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${two(date.getMonth() + 1)}-${two(date.getDate())}`
}

function zonedDateKeyAt(formatter: Intl.DateTimeFormat, epochMs: number): string {
  const parts = formatter.formatToParts(new Date(epochMs))
  const value = (type: string) => parts.find((part) => part.type === type)?.value ?? '00'
  return `${value('year')}-${value('month')}-${value('day')}`
}

function compareZonedDateKeyAt(formatter: Intl.DateTimeFormat, epochMs: number, targetDateKey: string): number {
  const current = zonedDateKeyAt(formatter, epochMs)
  return current < targetDateKey ? -1 : current > targetDateKey ? 1 : 0
}

function utcDateStartMs(year: number, month: number, day: number): number {
  const date = new Date(0)
  date.setUTCHours(0, 0, 0, 0)
  date.setUTCFullYear(year, month - 1, day)
  return date.getTime()
}

function two(value: number): string {
  return String(value).padStart(2, '0')
}

function daysBetweenInclusive(start: Date, end: Date): number {
  return Math.max(1, Math.floor((startOfLocalDay(end).getTime() - startOfLocalDay(start).getTime()) / dayMs) + 1)
}
