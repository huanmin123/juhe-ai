import type { AccountAvailabilitySchedule } from '@/types/domain'

export interface AccountScheduleWindowFormRow {
  key: string
  daysOfWeek: number[]
  start?: string
  end?: string
}

export interface AccountAvailabilityScheduleForm {
  enabled: boolean
  windows: AccountScheduleWindowFormRow[]
}

export type AccountAvailabilitySchedulePayload = Pick<AccountAvailabilitySchedule, 'enabled' | 'mode' | 'windows'>

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
  return {
    enabled: schedule?.enabled === true,
    windows: schedule?.windows?.length
      ? schedule.windows.map((window) => createAccountScheduleWindowFormRow(window.daysOfWeek, window.start, window.end))
      : [createAccountScheduleWindowFormRow()]
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
  const invalidIndex = normalizedScheduleWindows(schedule).findIndex((window) => !window.daysOfWeek.length || !window.start || !window.end || window.start === window.end)
  return invalidIndex >= 0 ? `请完整填写第 ${invalidIndex + 1} 个自动启停时段` : undefined
}

export function buildAccountAvailabilitySchedulePayload(schedule: AccountAvailabilityScheduleForm): AccountAvailabilitySchedulePayload | null {
  if (!schedule.enabled) return null
  return {
    enabled: true,
    mode: 'allow_windows',
    windows: normalizedScheduleWindows(schedule).map((window) => ({
      daysOfWeek: window.daysOfWeek,
      start: window.start as string,
      end: window.end as string
    }))
  }
}

export function accountAvailabilityScheduleFormFingerprint(schedule: AccountAvailabilityScheduleForm): string {
  if (!schedule.enabled) return JSON.stringify({ enabled: false })
  return JSON.stringify({
    enabled: true,
    windows: normalizedScheduleWindows(schedule).map((window) => ({
      daysOfWeek: window.daysOfWeek,
      start: window.start,
      end: window.end
    }))
  })
}

export function accountScheduleSummary(schedule?: AccountAvailabilitySchedule): string {
  if (!schedule?.enabled || !schedule.windows.length) return '未设置'
  const windows = schedule.windows
    .slice(0, 2)
    .map((window) => `${daysOfWeekText(window.daysOfWeek)} ${scheduleWindowText(window.start, window.end)}`)
  const suffix = schedule.windows.length > 2 ? ` 等 ${schedule.windows.length} 段` : ''
  const current = isScheduleCurrentlyAllowed(schedule) ? '当前可用' : '计划停用'
  return `${current}：${windows.join(' / ')}${suffix}`
}

export function accountScheduleTagColor(schedule?: AccountAvailabilitySchedule): string {
  if (!schedule?.enabled) return 'default'
  return isScheduleCurrentlyAllowed(schedule) ? 'green' : 'orange'
}

function normalizedScheduleWindows(schedule: AccountAvailabilityScheduleForm): Array<{ daysOfWeek: number[]; start?: string; end?: string }> {
  return schedule.windows.map((window) => ({
    daysOfWeek: [...new Set(window.daysOfWeek.map((day) => Number(day)))].filter((day) => Number.isInteger(day) && day >= 1 && day <= 7).sort((left, right) => left - right),
    start: window.start,
    end: window.end
  }))
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
    return (exception.windows ?? []).some((window) => isCurrentMinuteInScheduleWindow(current, { ...window, daysOfWeek: [current.dayOfWeek] }))
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
  try {
    const formatter = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone || defaultScheduleTimezone(),
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
  } catch {
    const day = date.getDay()
    const dateKey = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
    return {
      dateKey,
      dayOfWeek: day === 0 ? 7 : day,
      minuteOfDay: date.getHours() * 60 + date.getMinutes()
    }
  }
}

function defaultScheduleTimezone(): string {
  if (typeof Intl === 'undefined') return 'Asia/Shanghai'
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'
}
