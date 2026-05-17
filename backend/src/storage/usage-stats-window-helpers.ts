import type { AccountUsageStatsRange } from '../domain/types.js'
import { dateKey, dateKeysInRange, hourKey } from './usage-stats-helpers.js'

export const HOUR_MS = 60 * 60 * 1000
export const DAY_MS = 24 * HOUR_MS
export const FIXED_RANGE_WINDOW_DAYS = 31

export function rowsByStatDate<T extends { stat_date: string }>(rows: T[]): Map<string, T[]> {
  const result = new Map<string, T[]>()
  for (const row of rows) {
    const rowsForDate = result.get(row.stat_date) ?? []
    rowsForDate.push(row)
    result.set(row.stat_date, rowsForDate)
  }
  return result
}

export function rowsByStatHourDate<T extends { stat_hour: string }>(rows: T[]): Map<string, T[]> {
  const result = new Map<string, T[]>()
  for (const row of rows) {
    const statDate = row.stat_hour.slice(0, 10)
    if (!statDate) continue
    const rowsForDate = result.get(statDate) ?? []
    rowsForDate.push(row)
    result.set(statDate, rowsForDate)
  }
  return result
}

export function rowsForDateRange<T>(rowsByDate: Map<string, T[]>, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): T[] {
  const rows: T[] = []
  for (const statDate of dateKeysInRange(range)) {
    rows.push(...(rowsByDate.get(statDate) ?? []))
  }
  return rows
}

export function fixedUsageStatsDateKeys(timezone: string, todayKey = dateKey(new Date(), timezone)): string[] {
  const endDate = parseDateKeyStrict(todayKey)
  if (!endDate) return []
  const earliestDate = addDays(endDate, -(FIXED_RANGE_WINDOW_DAYS - 1))
  return Array.from({ length: FIXED_RANGE_WINDOW_DAYS }, (_, index) => localDateKey(addDays(earliestDate, index)))
}

export function fixedUsageStatsRanges(timezone: string, todayKey = dateKey(new Date(), timezone)): AccountUsageStatsRange[] {
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  const ranges: AccountUsageStatsRange[] = []
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      ranges.push({
        startDate: dates[startIndex],
        endDate: dates[endIndex],
        days: endIndex - startIndex + 1,
        maxDays: FIXED_RANGE_WINDOW_DAYS
      })
    }
  }
  return ranges
}

export function hourBucketsUntilNow(hours: number, now = Date.now(), timezone?: string): string[] {
  const size = Math.max(1, Math.trunc(hours))
  return Array.from({ length: size }, (_, index) => hourKey(new Date(now - (size - 1 - index) * HOUR_MS), timezone))
}

export function hourBucketsForRange(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): string[] {
  const dates = dateKeysInRange(range)
  const buckets: string[] = []
  for (const date of dates) {
    for (let hour = 0; hour < 24; hour += 1) {
      buckets.push(`${date}T${String(hour).padStart(2, '0')}`)
    }
  }
  return buckets
}

export function rangeWindowKey(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): string {
  return `${range.startDate}:${range.endDate}`
}

export function trendBucketHours(range: Pick<AccountUsageStatsRange, 'days'>): number {
  if (range.days <= 1) return 1
  if (range.days <= 3) return 6
  return 24
}

export function trendBucketKey(statHour: string, bucketHours: number): string {
  if (bucketHours >= 24) {
    return statHour.slice(0, 10)
  }
  if (bucketHours <= 1) {
    return statHour
  }
  const hour = Number(statHour.slice(11, 13))
  if (!Number.isFinite(hour)) {
    return statHour
  }
  const bucketHour = Math.floor(hour / bucketHours) * bucketHours
  return `${statHour.slice(0, 11)}${String(bucketHour).padStart(2, '0')}`
}

export function sortedMapEntries<T>(map: Map<string, T>): Array<[string, T]> {
  return [...map.entries()].sort(([leftKey], [rightKey]) => compareText(leftKey, rightKey))
}

export function compareText(left: string, right: string): number {
  if (left < right) return -1
  if (left > right) return 1
  return 0
}

export function nextDateKey(statDate: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(statDate)
  if (!match) return statDate
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  date.setDate(date.getDate() + 1)
  return localDateKey(date)
}

function parseDateKeyStrict(value: string): Date | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  return date.getFullYear() === Number(match[1]) && date.getMonth() === Number(match[2]) - 1 && date.getDate() === Number(match[3]) ? date : undefined
}

function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function addDays(date: Date, days: number): Date {
  const next = new Date(date)
  next.setDate(next.getDate() + days)
  return next
}
