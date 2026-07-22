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
const scheduleNextCheckHorizonDays = 14
const scheduleNextCheckFallbackDays = 7
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

interface ScheduleWindowOccurrence {
  startDateKey: string
  minuteOfDay: number
  key: string
}

interface ScheduleWindowEvent {
  action: 'start' | 'end'
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
  return {
    enabled: true,
    allowed: isCurrentTimeAllowedBySchedule(schedule, current)
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
  const events = dueScheduleWindowEvents(schedule, current)
  if (!events.length) return undefined
  const starts = events.filter((event) => event.action === 'start')
  const selected = starts[0] ?? events[0]
  const action = selected.action
  const status = isCurrentTimeAllowedBySchedule(schedule, current) ? 'active' : 'disabled'
  return {
    action,
    status,
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

export function nextApiKeyAvailabilityScheduleCheckAt(
  schedule: ApiKeyAvailabilitySchedule | undefined,
  now = new Date()
): string | null {
  if (!schedule?.enabled || !Number.isFinite(now.getTime())) return null
  const candidates = scheduleBoundaryUtcTimes(schedule, now)
    .filter((time) => time > now.getTime())
    .sort((left, right) => left - right)
  const next = candidates[0]
  return next === undefined
    ? new Date(now.getTime() + scheduleNextCheckFallbackDays * 24 * 60 * 60 * 1000).toISOString()
    : new Date(next).toISOString()
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

function startEventCandidatesForCurrentAllowedTime(
  schedule: ApiKeyAvailabilitySchedule,
  current: ZonedDateTimeParts
): ApiKeyAvailabilityScheduleStartEventCandidate[] {
  return allowedScheduleWindowOccurrences(schedule, current).map((occurrence) => ({
    dateKey: occurrence.startDateKey,
    minuteOfDay: occurrence.minuteOfDay,
    key: occurrence.key
  }))
}

function minuteOfDay(value: string): number {
  const match = value.match(timePattern)
  if (!match) return 0
  return Number(match[1]) * 60 + Number(match[2])
}

function isCurrentTimeAllowedBySchedule(schedule: ApiKeyAvailabilitySchedule, current: ZonedDateTimeParts): boolean {
  return allowedScheduleWindowOccurrences(schedule, current).length > 0
}

function allowedScheduleWindowOccurrences(schedule: ApiKeyAvailabilitySchedule, current: ZonedDateTimeParts): ScheduleWindowOccurrence[] {
  const occurrences: ScheduleWindowOccurrence[] = []
  const startDateKeys = [current.dateKey, previousDateKey(current.dateKey)]
  for (const startDateKey of startDateKeys) {
    if (!isDateInScheduleRange(startDateKey, schedule)) continue
    const exception = schedule.exceptions?.find((item) => item.date === startDateKey)
    if (exception?.action === 'deny') continue
    if (exception?.action === 'allow') {
      occurrences.push(...exception.windows
        .map((window, index) => exceptionWindowOccurrence(current, startDateKey, window, `exception:${startDateKey}:${index}`))
        .filter((item): item is ScheduleWindowOccurrence => Boolean(item)))
      continue
    }
    occurrences.push(...schedule.windows
      .map((window, index) => scheduleWindowOccurrence(current, startDateKey, window, `window:${index}`))
      .filter((item): item is ScheduleWindowOccurrence => Boolean(item)))
  }
  return occurrences
}

function dueScheduleWindowEvents(schedule: ApiKeyAvailabilitySchedule, current: ZonedDateTimeParts): ScheduleWindowEvent[] {
  const events: ScheduleWindowEvent[] = []
  const startDateKeys = [current.dateKey, previousDateKey(current.dateKey)]
  for (const startDateKey of startDateKeys) {
    if (!isDateInScheduleRange(startDateKey, schedule)) continue
    const exception = schedule.exceptions?.find((item) => item.date === startDateKey)
    if (exception?.action === 'deny') continue
    if (exception?.action === 'allow') {
      events.push(...exception.windows.flatMap((window, index) => exceptionWindowEvents(current, startDateKey, window, `exception:${startDateKey}:${index}`)))
      continue
    }
    events.push(...schedule.windows.flatMap((window, index) => scheduleWindowEvents(current, startDateKey, window, `window:${index}`)))
  }
  return events
}

function scheduleBoundaryUtcTimes(schedule: ApiKeyAvailabilitySchedule, now: Date): number[] {
  const current = zonedDateTimeParts(now, schedule.timezone)
  const dateKeys = scheduleBoundaryCandidateDateKeys(current.dateKey)
  const times = new Set<number>()
  for (const dateKey of dateKeys) {
    if (isDateInScheduleRange(dateKey, schedule)) {
      const exception = schedule.exceptions?.find((item) => item.date === dateKey)
      if (exception?.action === 'allow') {
        for (const window of exception.windows) {
          addBoundaryUtcTime(times, dateKey, minuteOfDay(window.start), schedule.timezone)
          addBoundaryUtcTime(times, windowEndDateKey(dateKey, window.start, window.end), minuteOfDay(window.end), schedule.timezone)
        }
      } else if (exception?.action !== 'deny') {
        for (const window of schedule.windows) {
          if (!new Set(window.daysOfWeek).has(dayOfWeekForDateKey(dateKey))) continue
          addBoundaryUtcTime(times, dateKey, minuteOfDay(window.start), schedule.timezone)
          addBoundaryUtcTime(times, windowEndDateKey(dateKey, window.start, window.end), minuteOfDay(window.end), schedule.timezone)
        }
      }
    }
  }
  return [...times]
}

function scheduleBoundaryCandidateDateKeys(currentDateKey: string): string[] {
  const keys: string[] = []
  const start = new Date(`${currentDateKey}T00:00:00.000Z`)
  start.setUTCDate(start.getUTCDate() - 1)
  for (let offset = 0; offset <= scheduleNextCheckHorizonDays + 2; offset += 1) {
    const date = new Date(start)
    date.setUTCDate(start.getUTCDate() + offset)
    keys.push(date.toISOString().slice(0, 10))
  }
  return keys
}

function windowEndDateKey(startDateKey: string, startText: string, endText: string): string {
  return minuteOfDay(startText) < minuteOfDay(endText) ? startDateKey : nextDateKey(startDateKey)
}

function addBoundaryUtcTime(times: Set<number>, dateKey: string, minute: number, timezone: string): void {
  const utcTime = zonedLocalMinuteToUtcTime(dateKey, minute, timezone)
  if (utcTime !== undefined) times.add(utcTime)
}

function zonedLocalMinuteToUtcTime(dateKey: string, minute: number, timezone: string): number | undefined {
  const [yearText, monthText, dayText] = dateKey.split('-')
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  if (!Number.isFinite(year) || !Number.isFinite(month) || !Number.isFinite(day)) return undefined
  let guess = Date.UTC(year, month - 1, day, Math.floor(minute / 60), minute % 60)
  const targetSerial = localMinuteSerial(dateKey, minute)
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const parts = zonedDateTimeParts(new Date(guess), timezone)
    const currentSerial = localMinuteSerial(parts.dateKey, parts.minuteOfDay)
    const deltaMinutes = targetSerial - currentSerial
    if (deltaMinutes === 0) {
      return guess
    }
    guess += deltaMinutes * 60 * 1000
  }
  const verified = zonedDateTimeParts(new Date(guess), timezone)
  return verified.dateKey === dateKey && verified.minuteOfDay === minute ? guess : undefined
}

function localMinuteSerial(dateKey: string, minute: number): number {
  return Math.trunc(new Date(`${dateKey}T00:00:00.000Z`).getTime() / 60000) + minute
}

function scheduleWindowOccurrence(
  current: ZonedDateTimeParts,
  startDateKey: string,
  window: ApiKeyAvailabilityScheduleWindow,
  token: string
): ScheduleWindowOccurrence | undefined {
  const startDayOfWeek = dayOfWeekForDateKey(startDateKey)
  if (!new Set(window.daysOfWeek).has(startDayOfWeek)) return undefined
  return windowOccurrence(current, startDateKey, window.start, window.end, token)
}

function exceptionWindowOccurrence(
  current: ZonedDateTimeParts,
  startDateKey: string,
  window: Pick<ApiKeyAvailabilityScheduleWindow, 'start' | 'end'>,
  token: string
): ScheduleWindowOccurrence | undefined {
  return windowOccurrence(current, startDateKey, window.start, window.end, token)
}

function windowOccurrence(
  current: ZonedDateTimeParts,
  startDateKey: string,
  startText: string,
  endText: string,
  token: string
): ScheduleWindowOccurrence | undefined {
  const start = minuteOfDay(startText)
  const end = minuteOfDay(endText)
  if (start < end) {
    if (current.dateKey === startDateKey && current.minuteOfDay >= start && current.minuteOfDay < end) {
      return { startDateKey, minuteOfDay: start, key: `${token}:start:${startText}` }
    }
    return undefined
  }
  if (current.dateKey === startDateKey && current.minuteOfDay >= start) {
    return { startDateKey, minuteOfDay: start, key: `${token}:start:${startText}` }
  }
  if (current.dateKey === nextDateKey(startDateKey) && current.minuteOfDay < end) {
    return { startDateKey, minuteOfDay: start, key: `${token}:start:${startText}` }
  }
  return undefined
}

function scheduleWindowEvents(
  current: ZonedDateTimeParts,
  startDateKey: string,
  window: ApiKeyAvailabilityScheduleWindow,
  token: string
): ScheduleWindowEvent[] {
  const startDayOfWeek = dayOfWeekForDateKey(startDateKey)
  if (!new Set(window.daysOfWeek).has(startDayOfWeek)) return []
  return windowEvents(current, startDateKey, window.start, window.end, token)
}

function exceptionWindowEvents(
  current: ZonedDateTimeParts,
  startDateKey: string,
  window: Pick<ApiKeyAvailabilityScheduleWindow, 'start' | 'end'>,
  token: string
): ScheduleWindowEvent[] {
  return windowEvents(current, startDateKey, window.start, window.end, token)
}

function windowEvents(
  current: ZonedDateTimeParts,
  startDateKey: string,
  startText: string,
  endText: string,
  token: string
): ScheduleWindowEvent[] {
  const start = minuteOfDay(startText)
  const end = minuteOfDay(endText)
  const endDateKey = start < end ? startDateKey : nextDateKey(startDateKey)
  const events: ScheduleWindowEvent[] = []
  if (current.dateKey === startDateKey && current.minuteOfDay === start) {
    events.push({ action: 'start', key: `${token}:start:${startText}` })
  }
  if (current.dateKey === endDateKey && current.minuteOfDay === end) {
    events.push({ action: 'end', key: `${token}:end:${endText}` })
  }
  return events
}

function dayOfWeekForDateKey(dateKey: string): number {
  const utcDay = new Date(`${dateKey}T00:00:00.000Z`).getUTCDay()
  return utcDay === 0 ? 7 : utcDay
}

function previousDateKey(dateKey: string): string {
  const date = new Date(`${dateKey}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() - 1)
  return date.toISOString().slice(0, 10)
}

function nextDateKey(dateKey: string): string {
  const date = new Date(`${dateKey}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() + 1)
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

