import dayjs, { type Dayjs } from 'dayjs'

export type DateRangeKeys = {
  startDate?: string
  endDate?: string
}

type DateRangeDefaults = [Dayjs, Dayjs] | (() => [Dayjs, Dayjs])

type NormalizeDateRangeOptions = {
  defaultRange: DateRangeDefaults
  maxDays?: number
}

const dateKeyPattern = /^\d{4}-\d{2}-\d{2}$/
const monthKeyPattern = /^\d{4}-\d{2}$/

export function todayDateRange(): [Dayjs, Dayjs] {
  const today = dayjs().startOf('day')
  return [today, today]
}

export function recentDateRange(days: number): [Dayjs, Dayjs] {
  const today = dayjs().startOf('day')
  const safeDays = Math.max(1, Math.floor(days))
  return [today.subtract(safeDays - 1, 'day'), today]
}

export function isDateKey(value?: string): value is string {
  return Boolean(value && dateKeyPattern.test(value))
}

export function isMonthKey(value?: string): value is string {
  return Boolean(value && monthKeyPattern.test(value))
}

export function parseDateKey(value?: string): Dayjs | undefined {
  if (!isDateKey(value)) return undefined
  const [year, month, day] = value.split('-').map((part) => Number(part))
  const parsed = dayjs(new Date(year, month - 1, day)).startOf('day')
  return parsed.year() === year && parsed.month() === month - 1 && parsed.date() === day ? parsed : undefined
}

export function formatDateKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

export function formatDateLabel(value: string): string {
  const parsed = parseDateKey(value)
  return parsed ? parsed.format('M月D日') : value
}

export function formatDateShortLabel(value: string): string {
  const parsed = parseDateKey(value)
  return parsed ? parsed.format('MM-DD') : value
}

export function parseDateRangeKeys(value: DateRangeKeys | undefined, options: NormalizeDateRangeOptions): [Dayjs, Dayjs] {
  const [defaultStart, defaultEnd] = resolveDefaultRange(options.defaultRange)
  const start = parseDateKey(value?.startDate) ?? defaultStart
  const end = parseDateKey(value?.endDate) ?? defaultEnd
  const [normalizedStart, normalizedEnd] = normalizeDateRangeKeys([start, end], options)
  return [dayjs(normalizedStart), dayjs(normalizedEnd)]
}

export function normalizeDateRangeKeys(value: [Dayjs, Dayjs], options: NormalizeDateRangeOptions): [string, string] {
  const [defaultStart, defaultEnd] = resolveDefaultRange(options.defaultRange)
  let start = (value[0] ?? defaultStart).startOf('day')
  const end = (value[1] ?? defaultEnd).startOf('day')
  if (start.isAfter(end, 'day')) {
    start = end
  }
  if (options.maxDays && end.diff(start, 'day') > options.maxDays - 1) {
    start = end.subtract(options.maxDays - 1, 'day')
  }
  return [formatDateKey(start), formatDateKey(end)]
}

export function normalizeDayjsDateRange(value?: [Dayjs, Dayjs]): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end, 'day') ? [end.startOf('day'), start.startOf('day')] : [start.startOf('day'), end.startOf('day')]
}

export function isRecentWindowDateDisabled(current: Dayjs | null | undefined, calendarRange: readonly [Dayjs | null, Dayjs | null], maxDays: number, referenceEndDate?: Dayjs): boolean {
  if (!current) return false
  const today = (referenceEndDate?.isValid() ? referenceEndDate : dayjs()).startOf('day')
  if (current.isAfter(today, 'day')) return true
  if (current.isBefore(today.subtract(maxDays - 1, 'day'), 'day')) return true
  const anchor = calendarRange[0] ?? calendarRange[1]
  if (!anchor) return false
  return Math.abs(current.startOf('day').diff(anchor.startOf('day'), 'day')) > maxDays - 1
}

function resolveDefaultRange(defaultRange: DateRangeDefaults): [Dayjs, Dayjs] {
  return typeof defaultRange === 'function' ? defaultRange() : defaultRange
}
