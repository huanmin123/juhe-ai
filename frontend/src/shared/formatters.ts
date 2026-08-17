import dayjs, { type Dayjs } from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'

dayjs.extend(relativeTime)

const serverDateTimePattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/
const localDateTimeFormatterOptions: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23'
}

export function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = parseStrictServerDateTime(value)
  if (!date) return '时间格式异常'
  return formatLocalDateTime(date)
}

export function serverDateTimeTimestamp(value?: string): number | undefined {
  if (!value) return undefined
  return parseStrictServerDateTime(value)?.getTime()
}

export function compareServerDateTime(left?: string | null, right?: string | null): number {
  const leftTimestamp = serverDateTimeTimestamp(left ?? undefined)
  const rightTimestamp = serverDateTimeTimestamp(right ?? undefined)
  if (leftTimestamp === undefined) return rightTimestamp === undefined ? 0 : 1
  if (rightTimestamp === undefined) return -1
  return leftTimestamp - rightTimestamp
}

export function formatRelativeDateTime(value?: string): string {
  if (!value) return '-'
  const date = parseStrictServerDateTime(value)
  if (!date) return '时间格式异常'
  const parsed = dayjs(date)
  return `${parsed.fromNow()} ${parsed.format('YYYY-MM-DD HH:mm')}`
}

export function formatNumber(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

export function formatInteger(value?: number): string {
  return formatNumber(Math.round(value ?? 0))
}

export function formatUngroupedInteger(value?: number): string {
  return `${Math.round(value ?? 0)}`
}

export function formatRequestCountTag(value?: number): string {
  return `${formatUngroupedInteger(value)}req`
}

export function formatCompactUsageAmount(value?: number): string {
  const amount = value ?? 0
  const absoluteValue = Math.abs(amount)
  if (absoluteValue >= 1_000_000_000) {
    return `${(amount / 1_000_000_000).toFixed(1)}B`
  }
  if (absoluteValue >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(1)}M`
  }
  if (absoluteValue >= 1_000) {
    return `${(amount / 1_000).toFixed(1)}K`
  }
  return formatNumber(amount)
}

export function formatUsd(value?: number, digits = 2): string {
  return `$${(value ?? 0).toFixed(digits)}`
}

export function formatMillisecondsAsSeconds(value?: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-'
  const seconds = Math.max(0, value) / 1000
  if (seconds === 0) return '0s'
  if (seconds < 1) return `${seconds.toFixed(2)}s`
  if (seconds < 10) return `${seconds.toFixed(1)}s`
  return `${Math.round(seconds)}s`
}

export function parseStrictDatePickerValue(value?: string, label = '时间'): Dayjs | undefined {
  if (!value) return undefined
  const parsedDate = parseStrictServerDateTime(value)
  if (!parsedDate) {
    throw new Error(`${label}格式异常，请清理后再编辑`)
  }
  return dayjs(parsedDate)
}

export function formatServerDateTimeInput(value?: Dayjs | null): string | null {
  return value ? value.toDate().toISOString() : null
}

export function parseStrictServerDateTime(value?: string): Date | undefined {
  if (!value) return undefined
  const match = serverDateTimePattern.exec(value)
  if (!match) return undefined
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, fractionText, timezone] = match
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const hour = Number(hourText)
  const minute = Number(minuteText)
  const second = Number(secondText)
  if (
    month < 1 || month > 12
    || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate()
    || hour > 23 || minute > 59 || second > 59
  ) return undefined
  if (timezone && timezone !== 'Z') {
    const offsetHour = Number(timezone.slice(1, 3))
    const offsetMinute = Number(timezone.slice(4, 6))
    if (offsetHour > 23 || offsetMinute > 59) return undefined
  }
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date
}

function formatLocalDateTime(value: Date): string {
  // 未指定 timeZone 时由浏览器运行时使用用户本地时区；每次创建 formatter 也能响应运行时 TZ 变化。
  const formatter = new Intl.DateTimeFormat('en-CA', localDateTimeFormatterOptions)
  const parts = Object.fromEntries(formatter.formatToParts(value)
    .filter((part) => part.type !== 'literal')
    .map((part) => [part.type, part.value]))
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}.${String(value.getMilliseconds()).padStart(3, '0')}`
}

export function formatLocalTime(value?: string): string {
  const date = parseStrictServerDateTime(value)
  if (!date) return '-'
  const formatter = new Intl.DateTimeFormat('en-CA', {
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23'
  })
  const parts = Object.fromEntries(formatter.formatToParts(date)
    .filter((part) => part.type !== 'literal')
    .map((part) => [part.type, part.value]))
  return `${parts.hour}:${parts.minute}`
}
