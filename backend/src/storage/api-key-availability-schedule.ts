import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyAvailabilityScheduleException,
  ApiKeyAvailabilityScheduleWindow
} from '../domain/types.js'
import { availabilityScheduleCacheTtlMs } from './availability-schedule-cache.js'
import { DEFAULT_USAGE_STATS_TIMEZONE, usageStatsTimezone } from './usage-stats-helpers.js'

const allDaysOfWeek = [1, 2, 3, 4, 5, 6, 7]
const maxScheduleWindows = 32
const maxScheduleExceptions = 128
const timePattern = /^([01]\d|2[0-3]):([0-5]\d)$/
const datePattern = /^\d{4}-\d{2}-\d{2}$/
const scheduleInputKeys = ['enabled', 'timezone', 'mode', 'windows', 'dateRange', 'exceptions'] as const
const scheduleWindowInputKeys = ['daysOfWeek', 'start', 'end'] as const
const scheduleExceptionWindowInputKeys = ['start', 'end'] as const
const scheduleDateRangeInputKeys = ['startDate', 'endDate'] as const
const scheduleExceptionInputKeys = ['date', 'action', 'windows'] as const

interface ZonedDateTimeParts {
  dateKey: string
  dayOfWeek: number
  minuteOfDay: number
}

export interface ApiKeyAvailabilityScheduleDecision {
  enabled: boolean
  allowed: boolean
}

export type ApiKeyAvailabilityScheduleStatus = 'active' | 'disabled'

export interface ApiKeyAvailabilityScheduleDueEvent {
  eventKey: string
  status: ApiKeyAvailabilityScheduleStatus
  action: 'start' | 'end'
}

interface ApiKeyAvailabilityScheduleStartEventCandidate {
  dateKey: string
  minuteOfDay: number
  key: string
}

export function normalizeApiKeyAvailabilitySchedule(input: unknown): ApiKeyAvailabilitySchedule | undefined {
  if (input === undefined || input === null) {
    return undefined
  }
  if (!isRecord(input)) {
    throw new Error('API Key 时间计划参数无效')
  }
  assertOnlyKeys(input, scheduleInputKeys, 'API Key 时间计划')
  if (input.enabled !== true) {
    throw new Error('API Key 时间计划启用状态必须为 true')
  }
  if (input.mode !== 'allow_windows') {
    throw new Error('API Key 时间计划模式必须为 allow_windows')
  }
  const timezone = normalizeScheduleTimezone(input.timezone)
  const windows = normalizeScheduleWindows(input.windows, true)
  if (!windows.length) {
    throw new Error('API Key 时间计划至少需要一个允许时段')
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
  return normalizeApiKeyAvailabilitySchedule(JSON.parse(value))
}

export function apiKeyAvailabilityScheduleJson(schedule: ApiKeyAvailabilitySchedule | undefined): string | null {
  return schedule ? JSON.stringify(schedule) : null
}

export function apiKeyAvailabilityScheduleFromRequest(input: Record<string, unknown>): ApiKeyAvailabilitySchedule | undefined {
  return normalizeApiKeyAvailabilitySchedule(input.availabilitySchedule)
}

export function isApiKeyAvailabilityScheduleInputPresent(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'availabilitySchedule')
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

export function apiKeyAvailabilityScheduleStatus(
  schedule: ApiKeyAvailabilitySchedule | undefined,
  now = new Date()
): ApiKeyAvailabilityScheduleStatus | undefined {
  if (!schedule?.enabled) return undefined
  return evaluateApiKeyAvailabilitySchedule(schedule, now).allowed ? 'active' : 'disabled'
}

export function dueApiKeyAvailabilityScheduleEvent(
  schedule: ApiKeyAvailabilitySchedule | undefined,
  now = new Date()
): ApiKeyAvailabilityScheduleDueEvent | undefined {
  if (!schedule?.enabled) return undefined
  const current = zonedDateTimeParts(now, schedule.timezone)
  if (!isDateInScheduleRange(current.dateKey, schedule)) return undefined
  const exception = schedule.exceptions?.find((item) => item.date === current.dateKey)
  if (exception?.action === 'deny') return undefined
  const windows = exception?.action === 'allow'
    ? exception.windows.map((window, index) => ({
      token: `exception:${current.dateKey}:${index}`,
      window: { ...window, daysOfWeek: [current.dayOfWeek] }
    }))
    : schedule.windows.map((window, index) => ({ token: `window:${index}`, window }))
  const events = windows.flatMap((item) => dueWindowEvents(current, item.window, item.token))
  if (!events.length) return undefined
  const starts = events.filter((event) => event.action === 'start')
  const selected = starts[0] ?? events[0]
  const action = selected.action
  return {
    action,
    status: action === 'start' ? 'active' : 'disabled',
    eventKey: `${current.dateKey}:${current.minuteOfDay}:${action}:${events.map((event) => event.key).sort().join('|')}`
  }
}

export function latestApiKeyAvailabilityScheduleStartEvent(
  schedule: ApiKeyAvailabilitySchedule | undefined,
  now = new Date()
): ApiKeyAvailabilityScheduleDueEvent | undefined {
  if (!schedule?.enabled) return undefined
  if (!evaluateApiKeyAvailabilitySchedule(schedule, now).allowed) return undefined
  const current = zonedDateTimeParts(now, schedule.timezone)
  const candidates = startEventCandidatesForCurrentAllowedTime(schedule, current)
  if (!candidates.length) return undefined
  const latest = candidates
    .sort((left, right) => right.dateKey.localeCompare(left.dateKey) || right.minuteOfDay - left.minuteOfDay)[0]
  if (!latest) return undefined
  const sameBoundaryCandidates = candidates.filter((candidate) => candidate.dateKey === latest.dateKey && candidate.minuteOfDay === latest.minuteOfDay)
  return {
    action: 'start',
    status: 'active',
    eventKey: `${latest.dateKey}:${latest.minuteOfDay}:start:${sameBoundaryCandidates.map((candidate) => candidate.key).sort().join('|')}`
  }
}

export function apiKeyScheduleCacheTtlMs(now = Date.now()): number {
  return availabilityScheduleCacheTtlMs(now)
}

function normalizeScheduleTimezone(value: unknown): string {
  const timezone = value === undefined
    ? defaultScheduleTimezone()
    : typeof value === 'string' && value.trim()
      ? value.trim()
      : undefined
  if (!timezone) {
    throw new Error('API Key 时间计划时区不能为空')
  }
  try {
    new Intl.DateTimeFormat('en-CA', { timeZone: timezone }).format(new Date())
    return timezone
  } catch {
    throw new Error('API Key 时间计划时区无效')
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
    throw new Error('API Key 时间计划时段无效')
  }
  if (input.length > maxScheduleWindows) {
    throw new Error(`API Key 时间计划最多支持 ${maxScheduleWindows} 个时段`)
  }
  return input.map((item) => normalizeScheduleWindow(item, requireDays))
}

function normalizeScheduleWindow(input: unknown, requireDays: boolean): ApiKeyAvailabilityScheduleWindow {
  if (!isRecord(input)) {
    throw new Error('API Key 时间计划时段无效')
  }
  assertOnlyKeys(input, requireDays ? scheduleWindowInputKeys : scheduleExceptionWindowInputKeys, 'API Key 时间计划时段')
  const start = normalizeScheduleTime(input.start, '开始时间')
  const end = normalizeScheduleTime(input.end, '停止时间')
  if (start === end) {
    throw new Error('API Key 时间计划开始时间和停止时间不能相同')
  }
  return {
    daysOfWeek: requireDays ? normalizeDaysOfWeek(input.daysOfWeek) : allDaysOfWeek,
    start,
    end
  }
}

function normalizeScheduleTime(value: unknown, label: string): string {
  if (typeof value !== 'string' || !timePattern.test(value.trim())) {
    throw new Error(`API Key 时间计划${label}格式应为 HH:mm`)
  }
  return value.trim()
}

function normalizeDaysOfWeek(value: unknown): number[] {
  if (!Array.isArray(value)) {
    throw new Error('API Key 时间计划重复日期无效')
  }
  const rawDays = value.map((item) => {
    if (typeof item !== 'number') {
      throw new Error('API Key 时间计划重复日期必须是数字')
    }
    return item
  })
  if (rawDays.some((item) => !Number.isInteger(item) || item < 1 || item > 7)) {
    throw new Error('API Key 时间计划重复日期无效')
  }
  const days = [...new Set(rawDays)].sort((left, right) => left - right)
  if (!days.length) {
    throw new Error('API Key 时间计划至少需要选择一个重复日期')
  }
  return days
}

function normalizeScheduleDateRange(input: unknown): ApiKeyAvailabilitySchedule['dateRange'] | undefined {
  if (input === undefined) {
    return undefined
  }
  if (!isRecord(input)) {
    throw new Error('API Key 时间计划生效日期范围无效')
  }
  assertOnlyKeys(input, scheduleDateRangeInputKeys, 'API Key 时间计划生效日期范围')
  const startDate = normalizeDateKey(input.startDate, '开始日期')
  const endDate = normalizeDateKey(input.endDate, '结束日期')
  if (startDate && endDate && startDate > endDate) {
    throw new Error('API Key 时间计划开始日期不能晚于结束日期')
  }
  return startDate || endDate ? { startDate, endDate } : undefined
}

function normalizeScheduleExceptions(input: unknown): ApiKeyAvailabilityScheduleException[] | undefined {
  if (input === undefined) {
    return undefined
  }
  if (!Array.isArray(input)) {
    throw new Error('API Key 时间计划例外日期无效')
  }
  if (input.length > maxScheduleExceptions) {
    throw new Error(`API Key 时间计划最多支持 ${maxScheduleExceptions} 个例外日期`)
  }
  const exceptions = input.map(normalizeScheduleException)
  return exceptions.length ? exceptions : undefined
}

function normalizeScheduleException(input: unknown): ApiKeyAvailabilityScheduleException {
  if (!isRecord(input)) {
    throw new Error('API Key 时间计划例外日期无效')
  }
  assertOnlyKeys(input, scheduleExceptionInputKeys, 'API Key 时间计划例外日期')
  const date = normalizeDateKey(input.date, '例外日期')
  if (!date) {
    throw new Error('API Key 时间计划例外日期不能为空')
  }
  const action = input.action === 'allow' || input.action === 'deny' ? input.action : undefined
  if (!action) {
    throw new Error('API Key 时间计划例外动作无效')
  }
  if (action === 'deny' && input.windows !== undefined) {
    throw new Error('API Key 时间计划拒绝例外不能配置允许时段')
  }
  if (action === 'allow' && input.windows === undefined) {
    throw new Error('API Key 时间计划允许例外至少需要一个允许时段')
  }
  if (action === 'allow') {
    const windows = normalizeScheduleWindows(input.windows, false).map((window) => ({ start: window.start, end: window.end }))
    if (!windows.length) {
      throw new Error('API Key 时间计划允许例外至少需要一个允许时段')
    }
    return { date, action, windows }
  }
  return { date, action, windows: undefined }
}

function normalizeDateKey(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || !datePattern.test(value.trim())) {
    throw new Error(`API Key 时间计划${label}格式应为 YYYY-MM-DD`)
  }
  const date = value.trim()
  const parsed = new Date(`${date}T00:00:00.000Z`)
  if (!Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== date) {
    throw new Error(`API Key 时间计划${label}无效`)
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

function dueWindowEvents(
  current: ZonedDateTimeParts,
  window: ApiKeyAvailabilityScheduleWindow,
  token: string
): Array<{ action: 'start' | 'end'; key: string }> {
  const start = minuteOfDay(window.start)
  const end = minuteOfDay(window.end)
  const days = new Set(window.daysOfWeek)
  const events: Array<{ action: 'start' | 'end'; key: string }> = []
  if (current.minuteOfDay === start && days.has(current.dayOfWeek)) {
    events.push({ action: 'start', key: `${token}:start:${window.start}` })
  }
  const endDay = start < end ? current.dayOfWeek : previousDayOfWeek(current.dayOfWeek)
  if (current.minuteOfDay === end && days.has(endDay)) {
    events.push({ action: 'end', key: `${token}:end:${window.end}` })
  }
  return events
}

function startEventCandidatesForCurrentAllowedTime(
  schedule: ApiKeyAvailabilitySchedule,
  current: ZonedDateTimeParts
): ApiKeyAvailabilityScheduleStartEventCandidate[] {
  const exception = schedule.exceptions?.find((item) => item.date === current.dateKey)
  if (exception?.action === 'allow') {
    return exception.windows.flatMap((window, index) => startEventCandidateForCurrentAllowedWindow(
      current,
      { ...window, daysOfWeek: [current.dayOfWeek] },
      `exception:${current.dateKey}:${index}`,
      false
    ))
  }
  if (exception?.action === 'deny') return []
  return schedule.windows.flatMap((window, index) => startEventCandidateForCurrentAllowedWindow(current, window, `window:${index}`, true))
}

function startEventCandidateForCurrentAllowedWindow(
  current: ZonedDateTimeParts,
  window: ApiKeyAvailabilityScheduleWindow,
  token: string,
  allowPreviousDate: boolean
): ApiKeyAvailabilityScheduleStartEventCandidate[] {
  const start = minuteOfDay(window.start)
  const end = minuteOfDay(window.end)
  const days = new Set(window.daysOfWeek)
  if (start < end) {
    if (days.has(current.dayOfWeek) && current.minuteOfDay >= start && current.minuteOfDay < end) {
      return [{ dateKey: current.dateKey, minuteOfDay: start, key: `${token}:start:${window.start}` }]
    }
    return []
  }
  if (days.has(current.dayOfWeek) && current.minuteOfDay >= start) {
    return [{ dateKey: current.dateKey, minuteOfDay: start, key: `${token}:start:${window.start}` }]
  }
  const previousDay = previousDayOfWeek(current.dayOfWeek)
  if (allowPreviousDate && days.has(previousDay) && current.minuteOfDay < end) {
    return [{ dateKey: previousDateKey(current.dateKey), minuteOfDay: start, key: `${token}:start:${window.start}` }]
  }
  return []
}

function minuteOfDay(value: string): number {
  const match = value.match(timePattern)
  if (!match) return 0
  return Number(match[1]) * 60 + Number(match[2])
}

function previousDayOfWeek(dayOfWeek: number): number {
  return dayOfWeek === 1 ? 7 : dayOfWeek - 1
}

function previousDateKey(dateKey: string): string {
  const date = new Date(`${dateKey}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() - 1)
  return date.toISOString().slice(0, 10)
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

function assertOnlyKeys(value: Record<string, unknown>, allowedKeys: readonly string[], label: string): void {
  const allowed = new Set(allowedKeys)
  const unexpected = Object.keys(value).find((key) => !allowed.has(key))
  if (unexpected) {
    throw new Error(`${label}包含不支持字段：${unexpected}`)
  }
}

