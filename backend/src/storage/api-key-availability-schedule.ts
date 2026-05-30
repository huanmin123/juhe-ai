import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyAvailabilityScheduleException,
  ApiKeyAvailabilityScheduleWindow
} from '../domain/types.js'
import { DEFAULT_USAGE_STATS_TIMEZONE, usageStatsTimezone } from './usage-stats-helpers.js'

const allDaysOfWeek = [1, 2, 3, 4, 5, 6, 7]
const maxScheduleWindows = 32
const maxScheduleExceptions = 128
const timePattern = /^([01]\d|2[0-3]):([0-5]\d)$/
const datePattern = /^\d{4}-\d{2}-\d{2}$/

interface ZonedDateTimeParts {
  dateKey: string
  dayOfWeek: number
  minuteOfDay: number
}

export interface ApiKeyAvailabilityScheduleDecision {
  enabled: boolean
  allowed: boolean
}

export function normalizeApiKeyAvailabilitySchedule(input: unknown): ApiKeyAvailabilitySchedule | undefined {
  if (input === undefined || input === null || input === '') {
    return undefined
  }
  if (!isRecord(input)) {
    throw new Error('API Key 自动启停计划参数无效')
  }
  if (input.enabled !== true) {
    return undefined
  }
  const timezone = normalizeScheduleTimezone(input.timezone)
  const windows = normalizeScheduleWindows(input.windows, true)
  if (!windows.length) {
    throw new Error('API Key 自动启停计划至少需要一个允许时段')
  }
  return {
    enabled: true,
    timezone,
    mode: 'allow_windows',
    windows,
    dateRange: normalizeScheduleDateRange(input.dateRange),
    exceptions: normalizeScheduleExceptions(input.exceptions)
  }
}

export function parseApiKeyAvailabilityScheduleJson(value: string | null | undefined): ApiKeyAvailabilitySchedule | undefined {
  if (!value) return undefined
  try {
    return normalizeApiKeyAvailabilitySchedule(JSON.parse(value))
  } catch {
    return undefined
  }
}

export function apiKeyAvailabilityScheduleJson(schedule: ApiKeyAvailabilitySchedule | undefined): string | null {
  return schedule ? JSON.stringify(schedule) : null
}

export function apiKeyAvailabilityScheduleFromRequest(input: Record<string, unknown>): ApiKeyAvailabilitySchedule | undefined {
  return normalizeApiKeyAvailabilitySchedule(input.availabilitySchedule ?? input.availability_schedule)
}

export function isApiKeyAvailabilityScheduleInputPresent(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'availabilitySchedule')
    || Object.prototype.hasOwnProperty.call(input, 'availability_schedule')
}

export function evaluateApiKeyAvailabilitySchedule(
  schedule: ApiKeyAvailabilitySchedule | undefined,
  now = new Date()
): ApiKeyAvailabilityScheduleDecision {
  if (!schedule?.enabled) {
    return { enabled: false, allowed: true }
  }
  const current = zonedDateTimeParts(now, schedule.timezone)
  if (!isDateInScheduleRange(current.dateKey, schedule)) {
    return { enabled: true, allowed: false }
  }

  const exception = schedule.exceptions?.find((item) => item.date === current.dateKey)
  if (exception) {
    if (exception.action === 'deny') {
      return { enabled: true, allowed: false }
    }
    const exceptionWindows = exception.windows ?? []
    return {
      enabled: true,
      allowed: exceptionWindows.some((window) => isMinuteInWindow(current, { ...window, daysOfWeek: [current.dayOfWeek] }))
    }
  }

  return {
    enabled: true,
    allowed: schedule.windows.some((window) => isMinuteInWindow(current, window))
  }
}

export function apiKeyScheduleCacheTtlMs(now = Date.now()): number {
  const nextMinuteAt = Math.floor(now / 60_000) * 60_000 + 60_000
  return Math.max(1, nextMinuteAt - now)
}

function normalizeScheduleTimezone(value: unknown): string {
  const timezone = typeof value === 'string' && value.trim()
    ? value.trim()
    : defaultScheduleTimezone()
  try {
    new Intl.DateTimeFormat('en-CA', { timeZone: timezone }).format(new Date())
    return timezone
  } catch {
    throw new Error('API Key 自动启停计划时区无效')
  }
}

function defaultScheduleTimezone(): string {
  try {
    return usageStatsTimezone()
  } catch {
    return DEFAULT_USAGE_STATS_TIMEZONE
  }
}

function normalizeScheduleWindows(input: unknown, requireDays: boolean): ApiKeyAvailabilityScheduleWindow[] {
  if (!Array.isArray(input)) {
    throw new Error('API Key 自动启停计划时段无效')
  }
  if (input.length > maxScheduleWindows) {
    throw new Error(`API Key 自动启停计划最多支持 ${maxScheduleWindows} 个时段`)
  }
  return input.map((item) => normalizeScheduleWindow(item, requireDays))
}

function normalizeScheduleWindow(input: unknown, requireDays: boolean): ApiKeyAvailabilityScheduleWindow {
  if (!isRecord(input)) {
    throw new Error('API Key 自动启停计划时段无效')
  }
  const start = normalizeScheduleTime(input.start, '开始时间')
  const end = normalizeScheduleTime(input.end, '停止时间')
  if (start === end) {
    throw new Error('API Key 自动启停计划开始时间和停止时间不能相同')
  }
  return {
    daysOfWeek: requireDays ? normalizeDaysOfWeek(input.daysOfWeek) : allDaysOfWeek,
    start,
    end
  }
}

function normalizeScheduleTime(value: unknown, label: string): string {
  if (typeof value !== 'string' || !timePattern.test(value.trim())) {
    throw new Error(`API Key 自动启停计划${label}格式应为 HH:mm`)
  }
  return value.trim()
}

function normalizeDaysOfWeek(value: unknown): number[] {
  if (!Array.isArray(value)) {
    throw new Error('API Key 自动启停计划重复日期无效')
  }
  const rawDays = value.map((item) => Number(item))
  if (rawDays.some((item) => !Number.isInteger(item) || item < 1 || item > 7)) {
    throw new Error('API Key 自动启停计划重复日期无效')
  }
  const days = [...new Set(rawDays)].sort((left, right) => left - right)
  if (!days.length) {
    throw new Error('API Key 自动启停计划至少需要选择一个重复日期')
  }
  return days
}

function normalizeScheduleDateRange(input: unknown): ApiKeyAvailabilitySchedule['dateRange'] | undefined {
  if (input === undefined || input === null || input === '') {
    return undefined
  }
  if (!isRecord(input)) {
    throw new Error('API Key 自动启停计划生效日期范围无效')
  }
  const startDate = normalizeDateKey(input.startDate, '开始日期')
  const endDate = normalizeDateKey(input.endDate, '结束日期')
  if (startDate && endDate && startDate > endDate) {
    throw new Error('API Key 自动启停计划开始日期不能晚于结束日期')
  }
  return startDate || endDate ? { startDate, endDate } : undefined
}

function normalizeScheduleExceptions(input: unknown): ApiKeyAvailabilityScheduleException[] | undefined {
  if (input === undefined || input === null || input === '') {
    return undefined
  }
  if (!Array.isArray(input)) {
    throw new Error('API Key 自动启停计划例外日期无效')
  }
  if (input.length > maxScheduleExceptions) {
    throw new Error(`API Key 自动启停计划最多支持 ${maxScheduleExceptions} 个例外日期`)
  }
  const exceptions = input.map(normalizeScheduleException)
  return exceptions.length ? exceptions : undefined
}

function normalizeScheduleException(input: unknown): ApiKeyAvailabilityScheduleException {
  if (!isRecord(input)) {
    throw new Error('API Key 自动启停计划例外日期无效')
  }
  const date = normalizeDateKey(input.date, '例外日期')
  if (!date) {
    throw new Error('API Key 自动启停计划例外日期不能为空')
  }
  const action = input.action === 'allow' || input.action === 'deny' ? input.action : undefined
  if (!action) {
    throw new Error('API Key 自动启停计划例外动作无效')
  }
  const windows = action === 'allow' && input.windows !== undefined
    ? normalizeScheduleWindows(input.windows, false).map((window) => ({ start: window.start, end: window.end }))
    : undefined
  return { date, action, windows }
}

function normalizeDateKey(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (typeof value !== 'string' || !datePattern.test(value.trim())) {
    throw new Error(`API Key 自动启停计划${label}格式应为 YYYY-MM-DD`)
  }
  const date = value.trim()
  const parsed = new Date(`${date}T00:00:00.000Z`)
  if (!Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== date) {
    throw new Error(`API Key 自动启停计划${label}无效`)
  }
  return date
}

function isDateInScheduleRange(dateKey: string, schedule: ApiKeyAvailabilitySchedule): boolean {
  const { startDate, endDate } = schedule.dateRange ?? {}
  if (startDate && dateKey < startDate) return false
  if (endDate && dateKey > endDate) return false
  return true
}

function isMinuteInWindow(current: ZonedDateTimeParts, window: ApiKeyAvailabilityScheduleWindow): boolean {
  const start = minuteOfDay(window.start)
  const end = minuteOfDay(window.end)
  const days = new Set(window.daysOfWeek)
  if (start < end) {
    return days.has(current.dayOfWeek) && current.minuteOfDay >= start && current.minuteOfDay < end
  }
  return (days.has(current.dayOfWeek) && current.minuteOfDay >= start)
    || (days.has(previousDayOfWeek(current.dayOfWeek)) && current.minuteOfDay < end)
}

function minuteOfDay(value: string): number {
  const match = value.match(timePattern)
  if (!match) return 0
  return Number(match[1]) * 60 + Number(match[2])
}

function previousDayOfWeek(dayOfWeek: number): number {
  return dayOfWeek === 1 ? 7 : dayOfWeek - 1
}

function zonedDateTimeParts(date: Date, timezone: string): ZonedDateTimeParts {
  const formatter = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23'
  })
  const parts = Object.fromEntries(formatter.formatToParts(date).map((part) => [part.type, part.value]))
  const year = Number(parts.year)
  const month = Number(parts.month)
  const day = Number(parts.day)
  const hour = Number(parts.hour)
  const minute = Number(parts.minute)
  const dateKey = `${year}-${two(month)}-${two(day)}`
  const utcDay = new Date(Date.UTC(year, month - 1, day)).getUTCDay()
  return {
    dateKey,
    dayOfWeek: utcDay === 0 ? 7 : utcDay,
    minuteOfDay: hour * 60 + minute
  }
}

function two(value: number): string {
  return String(value).padStart(2, '0')
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
