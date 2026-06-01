import type { AccountAvailabilitySchedule } from '@/types/domain'

export interface AccountScheduleWindowFormRow {
  key: string
  daysOfWeek: number[]
  start?: string
  end?: string
}

export interface AccountAvailabilityScheduleForm {
  enabled: boolean
  timezone: string
  windows: AccountScheduleWindowFormRow[]
  dateRange?: AccountAvailabilitySchedule['dateRange']
  exceptions?: AccountAvailabilitySchedule['exceptions']
}

export type AccountAvailabilitySchedulePayload = AccountAvailabilitySchedule

let scheduleWindowFormKeySeed = 0

export const weekdayOptions = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 7 }
]

export function createAccountAvailabilityScheduleForm(schedule?: AccountAvailabilitySchedule): AccountAvailabilityScheduleForm {
  if (!schedule) {
    return {
      enabled: false,
      timezone: defaultScheduleTimezone(),
      windows: [createAccountScheduleWindowFormRow()]
    }
  }
  assertAccountAvailabilitySchedule(schedule)
  return {
    enabled: true,
    timezone: schedule.timezone,
    windows: schedule.windows.map((window) => createAccountScheduleWindowFormRow(window.daysOfWeek, window.start, window.end)),
    dateRange: cloneScheduleDateRange(schedule.dateRange),
    exceptions: cloneScheduleExceptions(schedule.exceptions)
  }
}

export function createAccountScheduleWindowFormRow(daysOfWeek = [1, 2, 3, 4, 5, 6, 7], start = '22:00', end = '23:55'): AccountScheduleWindowFormRow {
  return {
    key: `account_schedule_window_${Date.now()}_${scheduleWindowFormKeySeed += 1}`,
    daysOfWeek: [...daysOfWeek],
    start,
    end
  }
}

export function validateAccountAvailabilityScheduleForm(schedule: AccountAvailabilityScheduleForm): string | undefined {
  if (!schedule.enabled) return undefined
  const invalidIndex = normalizedScheduleWindows(schedule).findIndex((window) => hasInvalidScheduleDays(window.daysOfWeek) || !window.start || !window.end || window.start === window.end)
  return invalidIndex >= 0 ? `请完整填写第 ${invalidIndex + 1} 个自动启停时段` : undefined
}

export function buildAccountAvailabilitySchedulePayload(schedule: AccountAvailabilityScheduleForm): AccountAvailabilitySchedulePayload | null {
  if (!schedule.enabled) return null
  return {
    enabled: true,
    timezone: schedule.timezone,
    mode: 'allow_windows',
    windows: normalizedScheduleWindows(schedule).map((window) => ({
      daysOfWeek: window.daysOfWeek,
      start: window.start as string,
      end: window.end as string
    })),
    ...(schedule.dateRange ? { dateRange: cloneScheduleDateRange(schedule.dateRange) } : {}),
    ...(schedule.exceptions?.length ? { exceptions: cloneScheduleExceptions(schedule.exceptions) } : {})
  }
}

export function accountAvailabilityScheduleFormFingerprint(schedule: AccountAvailabilityScheduleForm): string {
  if (!schedule.enabled) return JSON.stringify({ enabled: false })
  return JSON.stringify({
    enabled: true,
    timezone: schedule.timezone,
    windows: normalizedScheduleWindows(schedule).map((window) => ({
      daysOfWeek: window.daysOfWeek,
      start: window.start,
      end: window.end
    })),
    dateRange: cloneScheduleDateRange(schedule.dateRange),
    exceptions: cloneScheduleExceptions(schedule.exceptions)
  })
}

export function accountScheduleSummary(schedule?: AccountAvailabilitySchedule): string {
  if (!schedule?.enabled || !schedule.windows.length) return '未设置'
  try {
    assertAccountAvailabilitySchedule(schedule)
  } catch {
    return '计划数据异常'
  }
  const windows = schedule.windows
    .slice(0, 2)
    .map((window) => `${daysOfWeekText(window.daysOfWeek)} ${scheduleWindowText(window.start, window.end)}`)
  const suffix = schedule.windows.length > 2 ? ` 等 ${schedule.windows.length} 段` : ''
  const current = isScheduleCurrentlyAllowed(schedule) ? '当前可用' : '计划停用'
  return `${current}：${windows.join(' / ')}${suffix}`
}

export function accountScheduleTagColor(schedule?: AccountAvailabilitySchedule): string {
  if (!schedule?.enabled) return 'default'
  try {
    assertAccountAvailabilitySchedule(schedule)
    return isScheduleCurrentlyAllowed(schedule) ? 'green' : 'orange'
  } catch {
    return 'red'
  }
}

function normalizedScheduleWindows(schedule: AccountAvailabilityScheduleForm): Array<{ daysOfWeek: number[]; start?: string; end?: string }> {
  return schedule.windows.map((window) => ({
    daysOfWeek: normalizedScheduleDays(window.daysOfWeek),
    start: window.start,
    end: window.end
  }))
}

function normalizedScheduleDays(days: number[]): number[] {
  return [...new Set(days.map((day) => typeof day === 'number' ? day : Number.NaN))].sort((left, right) => left - right)
}

function assertAccountAvailabilitySchedule(schedule: AccountAvailabilitySchedule): void {
  assertObjectKeys(schedule, ['enabled', 'timezone', 'mode', 'windows', 'dateRange', 'exceptions'], '账户自动启停计划')
  if (schedule.enabled !== true) throw new Error('账户自动启停计划启用状态异常，请清理后再编辑')
  if (schedule.mode !== 'allow_windows') throw new Error('账户自动启停计划模式异常，请清理后再编辑')
  if (typeof schedule.timezone !== 'string' || !schedule.timezone.trim()) throw new Error('账户自动启停计划时区异常，请清理后再编辑')
  assertScheduleTimezone(schedule.timezone, '账户自动启停计划时区')
  if (!Array.isArray(schedule.windows) || schedule.windows.length === 0) throw new Error('账户自动启停计划时段异常，请清理后再编辑')
  for (const window of schedule.windows) {
    assertObjectKeys(window, ['daysOfWeek', 'start', 'end'], '账户自动启停计划时段')
    assertScheduleDays(window.daysOfWeek, '账户自动启停计划重复日期')
    assertScheduleTime(window.start, '账户自动启停计划开始时间')
    assertScheduleTime(window.end, '账户自动启停计划停止时间')
    if (window.start === window.end) throw new Error('账户自动启停计划开始时间和停止时间不能相同')
  }
  if (schedule.dateRange) {
    assertScheduleDateRange(schedule.dateRange)
  }
  if (schedule.exceptions !== undefined) {
    if (!Array.isArray(schedule.exceptions)) throw new Error('账户自动启停计划例外日期异常，请清理后再编辑')
    for (const exception of schedule.exceptions) {
      assertObjectKeys(exception, ['date', 'action', 'windows'], '账户自动启停计划例外日期')
      assertScheduleDate(exception.date, '账户自动启停计划例外日期')
      if (exception.action === 'allow') {
        if (!Array.isArray(exception.windows) || exception.windows.length === 0) throw new Error('账户自动启停计划允许例外时段异常，请清理后再编辑')
        for (const window of exception.windows) {
          assertObjectKeys(window, ['start', 'end'], '账户自动启停计划例外时段')
          assertScheduleTime(window.start, '账户自动启停计划例外开始时间')
          assertScheduleTime(window.end, '账户自动启停计划例外停止时间')
          if (window.start === window.end) throw new Error('账户自动启停计划例外开始时间和停止时间不能相同')
        }
      } else if (exception.action === 'deny') {
        if ('windows' in exception) throw new Error('账户自动启停计划拒绝例外不能带允许时段')
      } else {
        throw new Error('账户自动启停计划例外动作异常，请清理后再编辑')
      }
    }
  }
}

function assertScheduleDateRange(dateRange: NonNullable<AccountAvailabilitySchedule['dateRange']>): void {
  assertObjectKeys(dateRange, ['startDate', 'endDate'], '账户自动启停计划日期范围')
  if (dateRange.startDate !== undefined) assertScheduleDate(dateRange.startDate, '账户自动启停计划开始日期')
  if (dateRange.endDate !== undefined) assertScheduleDate(dateRange.endDate, '账户自动启停计划结束日期')
  if (dateRange.startDate && dateRange.endDate && dateRange.startDate > dateRange.endDate) {
    throw new Error('账户自动启停计划开始日期不能晚于结束日期')
  }
}

function assertScheduleDays(days: unknown, label: string): void {
  if (!Array.isArray(days) || hasInvalidScheduleDays(days as number[])) throw new Error(`${label}异常，请清理后再编辑`)
}

function assertScheduleTime(value: unknown, label: string): void {
  if (typeof value !== 'string' || !/^([01]\d|2[0-3]):([0-5]\d)$/.test(value)) throw new Error(`${label}异常，请清理后再编辑`)
}

function assertScheduleDate(value: unknown, label: string): void {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) throw new Error(`${label}异常，请清理后再编辑`)
  const parsed = new Date(`${value}T00:00:00.000Z`)
  if (!Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== value) throw new Error(`${label}无效，请清理后再编辑`)
}

function assertScheduleTimezone(value: string, label: string): void {
  try {
    new Intl.DateTimeFormat('en-CA', { timeZone: value }).format(new Date(0))
  } catch {
    throw new Error(`${label}无效，请清理后再编辑`)
  }
}

function assertObjectKeys(value: unknown, allowedKeys: string[], label: string): void {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label}必须是对象`)
  const allowed = new Set(allowedKeys)
  const unknownKeys = Object.keys(value).filter((key) => !allowed.has(key))
  if (unknownKeys.length) throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
}

function hasInvalidScheduleDays(days: number[]): boolean {
  return !days.length || days.some((day) => !Number.isInteger(day) || day < 1 || day > 7)
}

function scheduleWindowText(start: string, end: string): string {
  return start > end ? `${start}-次日 ${end}` : `${start}-${end}`
}

function daysOfWeekText(days: number[]): string {
  const normalized = [...new Set(days)].sort((left, right) => left - right).join(',')
  if (normalized === '1,2,3,4,5,6,7') return '每天'
  if (normalized === '1,2,3,4,5') return '工作日'
  if (normalized === '6,7') return '周末'
  const labels = new Map(weekdayOptions.map((item) => [item.value, item.label]))
  return [...new Set(days)].sort((left, right) => left - right).map((day) => labels.get(day) ?? `周${day}`).join('、')
}

function isScheduleCurrentlyAllowed(schedule: AccountAvailabilitySchedule): boolean {
  if (!schedule.enabled) return true
  const current = zonedScheduleParts(new Date(), schedule.timezone)
  if (schedule.dateRange?.startDate && current.dateKey < schedule.dateRange.startDate) return false
  if (schedule.dateRange?.endDate && current.dateKey > schedule.dateRange.endDate) return false
  const exception = schedule.exceptions?.find((item) => item.date === current.dateKey)
  if (exception?.action === 'deny') return false
  if (exception?.action === 'allow') {
    return (exception.windows ?? []).some((window) => isCurrentMinuteInScheduleWindow(current, { daysOfWeek: [current.dayOfWeek], start: window.start, end: window.end }))
  }
  return schedule.windows.some((window) => isCurrentMinuteInScheduleWindow(current, window))
}

function isCurrentMinuteInScheduleWindow(
  current: { dayOfWeek: number; minuteOfDay: number },
  window: { daysOfWeek: number[]; start: string; end: string }
): boolean {
  const start = scheduleMinuteOfDay(window.start)
  const end = scheduleMinuteOfDay(window.end)
  const days = new Set(window.daysOfWeek)
  if (start < end) {
    return days.has(current.dayOfWeek) && current.minuteOfDay >= start && current.minuteOfDay < end
  }
  return (days.has(current.dayOfWeek) && current.minuteOfDay >= start)
    || (days.has(previousScheduleDayOfWeek(current.dayOfWeek)) && current.minuteOfDay < end)
}

function scheduleMinuteOfDay(value: string): number {
  const [hour, minute] = value.split(':').map((item) => Number(item))
  return Math.max(0, Math.min(23, hour || 0)) * 60 + Math.max(0, Math.min(59, minute || 0))
}

function previousScheduleDayOfWeek(dayOfWeek: number): number {
  return dayOfWeek === 1 ? 7 : dayOfWeek - 1
}

function zonedScheduleParts(date: Date, timezone: string): { dateKey: string; dayOfWeek: number; minuteOfDay: number } {
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
  const dateKey = `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
  const utcDay = new Date(Date.UTC(year, month - 1, day)).getUTCDay()
  return {
    dateKey,
    dayOfWeek: utcDay === 0 ? 7 : utcDay,
    minuteOfDay: hour * 60 + minute
  }
}

function defaultScheduleTimezone(): string {
  if (typeof Intl === 'undefined') return 'Asia/Shanghai'
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'
}

function cloneScheduleDateRange(dateRange: AccountAvailabilitySchedule['dateRange']): AccountAvailabilitySchedule['dateRange'] {
  return dateRange
    ? {
        ...(dateRange.startDate !== undefined ? { startDate: dateRange.startDate } : {}),
        ...(dateRange.endDate !== undefined ? { endDate: dateRange.endDate } : {})
      }
    : undefined
}

function cloneScheduleExceptions(exceptions: AccountAvailabilitySchedule['exceptions']): AccountAvailabilitySchedule['exceptions'] {
  return exceptions?.map((exception) => {
    if (exception.action === 'allow') {
      return {
        date: exception.date,
        action: 'allow',
        windows: exception.windows.map((window) => ({ start: window.start, end: window.end }))
      }
    }
    return {
      date: exception.date,
      action: 'deny'
    }
  })
}
