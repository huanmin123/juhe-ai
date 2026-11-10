export type TimeScheduleMode = 'allow_windows'
export type TimeScheduleExceptionAction = 'allow' | 'deny'

export interface TimeScheduleWindow {
  daysOfWeek: number[]
  start: string
  end: string
}

export type TimeScheduleException =
  | {
    date: string
    action: 'allow'
    windows: Array<Pick<TimeScheduleWindow, 'start' | 'end'>>
  }
  | {
    date: string
    action: 'deny'
    windows?: never
  }

export interface TimeSchedule {
  enabled: boolean
  timezone: string
  mode: TimeScheduleMode
  windows: TimeScheduleWindow[]
  dateRange?: {
    startDate?: string
    endDate?: string
  }
  exceptions?: TimeScheduleException[]
}

export interface TimeScheduleWindowFormRow {
  key: string
  daysOfWeek: number[]
  start?: string
  end?: string
}

export interface TimeScheduleForm<TSchedule extends TimeSchedule = TimeSchedule> {
  enabled: boolean
  timezone: string
  windows: TimeScheduleWindowFormRow[]
  dateRange?: TSchedule['dateRange']
  exceptions?: TSchedule['exceptions']
}

interface TimeScheduleOptions {
  label?: string
  keyPrefix?: string
}

interface TimeScheduleSummaryOptions extends TimeScheduleOptions {
  active?: boolean
  showActiveState?: boolean
}

const defaultScheduleLabel = '时间计划'
const defaultScheduleWindowKeyPrefix = 'time_schedule_window'
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

// ---- 快捷预设（动态数据源，见时间计划闸门设计 §6） ----

export interface TimeSchedulePreset {
  key: string
  label: string
  description: string
  timezone: string
  /**
   * 预设携带的时段定义：`invertOnApply: false`（默认）时直接作为调度窗口；
   * `invertOnApply: true` 时 windows 是该供应商的**高峰定义**，应用时取反
   * 生成"避开高峰的其余全部时间"（周末/非高峰天自然得到全天时段）。
   */
  invertOnApply?: boolean
  windows: TimeScheduleWindow[]
}

interface ProviderPeakWindowsShape {
  timezone: string
  windows: TimeScheduleWindow[]
}

function formatPresetWindowRange(windows: readonly TimeScheduleWindow[]): string {
  return windows.map((window) => (window.start > window.end ? `${window.start}-次日${window.end}` : `${window.start}-${window.end}`)).join('、')
}

// peakWindowsToTimeSchedulePreset 把供应商目录的 peakWindows 定义（后端
// providers 包 peak_windows.go 单一数据源，经 /providers/definitions 透出）
// 转换为时间计划快捷预设：windows 即高峰定义，应用时反向生成非高峰调度时段
//（用户口径：账户只会配置低峰时段，标签按低峰语义命名）。
export function peakWindowsToTimeSchedulePreset(providerName: string, peak: ProviderPeakWindowsShape): TimeSchedulePreset {
  return {
    key: `provider_peak_${providerName}`,
    label: `${providerName} 低峰时段`,
    description: `按高峰（${formatPresetWindowRange(peak.windows)}）反向生成全部非高峰时段`,
    timezone: peak.timezone,
    invertOnApply: true,
    windows: peak.windows.map((window) => ({ daysOfWeek: [...window.daysOfWeek], start: window.start, end: window.end }))
  }
}

// 反向时段短于该分钟数的零头（如高峰 22:00-23:55 反向后 23:55-24:00 的 5
// 分钟尾巴）不生成，避免出现无调度意义的碎段。
const invertMinSegmentMinutes = 15

function parseScheduleMinutes(value: string): number {
  const match = /^([01]\d|2[0-3]):([0-5]\d)$/.exec(value)
  if (!match) throw new Error(`时间格式无效：${value}`)
  return Number(match[1]) * 60 + Number(match[2])
}

function formatScheduleMinutes(minutes: number): string {
  const normalized = Math.min(Math.max(minutes, 0), 1439)
  return `${String(Math.floor(normalized / 60)).padStart(2, '0')}:${String(normalized % 60).padStart(2, '0')}`
}

// invertTimeScheduleWindows 把"高峰时段"取反为"避开高峰的其余全部时间"：
// 输入现有时段（语义为允许调度的窗口，本函数将其视作高峰），输出按分钟
// 覆盖矩阵计算的补集。跨午夜窗口（start>end）在次日展开；每段终点直接用
// 下一高峰起点表达（高峰 22:00-23:55 全周 → 反向 00:00-22:00 全周），全天
// 尾段以 23:59 表达（与既有表单惯例一致）；短于 15 分钟的零头段丢弃；高峰
// 覆盖全天时返回空数组（调用方应提示且不覆盖）。
export function invertTimeScheduleWindows(windows: readonly TimeScheduleWindow[]): TimeScheduleWindow[] {
  const marked = Array.from({ length: 7 }, () => new Uint8Array(1440))
  for (const window of windows) {
    const start = parseScheduleMinutes(window.start)
    const end = parseScheduleMinutes(window.end)
    if (start === end) continue
    for (const day of window.daysOfWeek) {
      if (start < end) {
        for (let minute = start; minute < end; minute += 1) marked[day - 1][minute] = 1
      } else {
        for (let minute = start; minute < 1440; minute += 1) marked[day - 1][minute] = 1
        const nextDay = (day % 7) + 1
        for (let minute = 0; minute < end; minute += 1) marked[nextDay - 1][minute] = 1
      }
    }
  }
  // 每天提取反向连续段（段终点 = 下一高峰起点），再按相同 (start,end) 合并天。
  const segmentsByDay = new Map<number, Array<{ start: number; end: number }>>()
  for (let day = 1; day <= 7; day += 1) {
    const segments: Array<{ start: number; end: number }> = []
    let segmentStart = -1
    for (let minute = 0; minute <= 1440; minute += 1) {
      const inverted = minute < 1440 ? marked[day - 1][minute] === 0 : false
      if (inverted && segmentStart < 0) segmentStart = minute
      if (!inverted && segmentStart >= 0) {
        if (minute - segmentStart >= invertMinSegmentMinutes) segments.push({ start: segmentStart, end: minute })
        segmentStart = -1
      }
    }
    if (segments.length) segmentsByDay.set(day, segments)
  }
  // 按 (start,end) 归组：段终点相同的天合并进同一时段行，days 升序。
  const grouped = new Map<string, { days: number[]; start: number; end: number }>()
  for (const [day, segments] of segmentsByDay) {
    for (const segment of segments) {
      const key = `${segment.start}-${segment.end}`
      const entry = grouped.get(key)
      if (entry) entry.days.push(day)
      else grouped.set(key, { days: [day], start: segment.start, end: segment.end })
    }
  }
  return [...grouped.values()]
    .sort((left, right) => left.start - right.start)
    .map((entry) => ({
      daysOfWeek: [...entry.days].sort((left, right) => left - right),
      start: formatScheduleMinutes(entry.start),
      // 段尾到午夜时按表单惯例写 23:59（00:00-23:59 表达全天）；其余段终点
      // 就是下一高峰起点，原样表达。
      end: entry.end >= 1440 ? '23:59' : formatScheduleMinutes(entry.end)
    }))
}

export function createTimeScheduleForm<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule?: TSchedule,
  options: TimeScheduleOptions = {}
): TimeScheduleForm<TSchedule> {
  if (!schedule) {
    return {
      enabled: false,
      timezone: defaultScheduleTimezone(),
      windows: [createTimeScheduleWindowFormRow({ keyPrefix: options.keyPrefix })]
    }
  }
  assertTimeSchedule(schedule, options.label)
  return {
    enabled: true,
    timezone: schedule.timezone,
    windows: schedule.windows.map((window) => createTimeScheduleWindowFormRow({
      daysOfWeek: window.daysOfWeek,
      start: window.start,
      end: window.end,
      keyPrefix: options.keyPrefix
    })),
    dateRange: cloneScheduleDateRange(schedule.dateRange),
    exceptions: cloneScheduleExceptions(schedule.exceptions)
  }
}

export function createTimeScheduleWindowFormRow(options: {
  daysOfWeek?: number[]
  start?: string
  end?: string
  keyPrefix?: string
} = {}): TimeScheduleWindowFormRow {
  const daysOfWeek = options.daysOfWeek ?? [1, 2, 3, 4, 5, 6, 7]
  return {
    key: `${options.keyPrefix ?? defaultScheduleWindowKeyPrefix}_${Date.now()}_${scheduleWindowFormKeySeed += 1}`,
    daysOfWeek: [...daysOfWeek],
    start: options.start ?? '22:00',
    end: options.end ?? '23:55'
  }
}

export function validateTimeScheduleForm<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule: TimeScheduleForm<TSchedule>
): string | undefined {
  if (!schedule.enabled) return undefined
  const invalidIndex = normalizedScheduleWindows(schedule).findIndex((window) => hasInvalidScheduleDays(window.daysOfWeek) || !window.start || !window.end || window.start === window.end)
  return invalidIndex >= 0 ? `请完整填写第 ${invalidIndex + 1} 个时段` : undefined
}

export function buildTimeSchedulePayload<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule: TimeScheduleForm<TSchedule>
): TSchedule | null {
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
  } as TSchedule
}

export function timeScheduleFormFingerprint<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule: TimeScheduleForm<TSchedule>
): string {
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

export function timeScheduleSummary<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule?: TSchedule,
  options: TimeScheduleSummaryOptions = {}
): string {
  if (!schedule?.enabled || !schedule.windows.length) return '未设置'
  try {
    assertTimeSchedule(schedule, options.label)
  } catch {
    return '计划数据异常'
  }
  const windows = schedule.windows
    .slice(0, 2)
    .map((window) => `${daysOfWeekText(window.daysOfWeek)} ${scheduleWindowText(window.start, window.end)}`)
  const suffix = schedule.windows.length > 2 ? ` 等 ${schedule.windows.length} 段` : ''
  const text = `${windows.join(' / ')}${suffix}`
  if (!options.showActiveState) return text
  const state = options.active === true ? '计划窗口内' : '等待窗口开启'
  return `${state}：${text}`
}

export function timeScheduleTagColor<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule?: TSchedule,
  options: TimeScheduleSummaryOptions = {}
): string {
  if (!schedule?.enabled) return 'default'
  try {
    assertTimeSchedule(schedule, options.label)
    if (options.showActiveState && options.active === true) return 'green'
    return 'blue'
  } catch {
    return 'red'
  }
}

export function assertTimeSchedule<TSchedule extends TimeSchedule = TimeSchedule>(schedule: TSchedule, label = defaultScheduleLabel): void {
  assertObjectKeys(schedule, ['enabled', 'timezone', 'mode', 'windows', 'dateRange', 'exceptions'], label)
  if (schedule.enabled !== true) throw new Error(`${label}启用状态异常，请清理后再编辑`)
  if (schedule.mode !== 'allow_windows') throw new Error(`${label}模式异常，请清理后再编辑`)
  if (typeof schedule.timezone !== 'string' || !schedule.timezone.trim()) throw new Error(`${label}时区异常，请清理后再编辑`)
  assertScheduleTimezone(schedule.timezone, `${label}时区`)
  if (!Array.isArray(schedule.windows) || schedule.windows.length === 0) throw new Error(`${label}时段异常，请清理后再编辑`)
  for (const window of schedule.windows) {
    assertObjectKeys(window, ['daysOfWeek', 'start', 'end'], `${label}时段`)
    assertScheduleDays(window.daysOfWeek, `${label}重复日期`)
    assertScheduleTime(window.start, `${label}开始时间`)
    assertScheduleTime(window.end, `${label}结束时间`)
    if (window.start === window.end) throw new Error(`${label}开始时间和结束时间不能相同`)
  }
  if (schedule.dateRange) {
    assertScheduleDateRange(schedule.dateRange, label)
  }
  if (schedule.exceptions !== undefined) {
    if (!Array.isArray(schedule.exceptions)) throw new Error(`${label}例外日期异常，请清理后再编辑`)
    for (const exception of schedule.exceptions) {
      assertObjectKeys(exception, ['date', 'action', 'windows'], `${label}例外日期`)
      assertScheduleDate(exception.date, `${label}例外日期`)
      if (exception.action === 'allow') {
        if (!Array.isArray(exception.windows) || exception.windows.length === 0) throw new Error(`${label}允许例外时段异常，请清理后再编辑`)
        for (const window of exception.windows) {
          assertObjectKeys(window, ['start', 'end'], `${label}例外时段`)
          assertScheduleTime(window.start, `${label}例外开始时间`)
          assertScheduleTime(window.end, `${label}例外结束时间`)
          if (window.start === window.end) throw new Error(`${label}例外开始时间和结束时间不能相同`)
        }
      } else if (exception.action === 'deny') {
        if ('windows' in exception) throw new Error(`${label}拒绝例外不能带允许时段`)
      } else {
        throw new Error(`${label}例外动作异常，请清理后再编辑`)
      }
    }
  }
}

function normalizedScheduleWindows<TSchedule extends TimeSchedule = TimeSchedule>(
  schedule: TimeScheduleForm<TSchedule>
): Array<{ daysOfWeek: number[]; start?: string; end?: string }> {
  return schedule.windows.map((window) => ({
    daysOfWeek: normalizedScheduleDays(window.daysOfWeek),
    start: window.start,
    end: window.end
  }))
}

function normalizedScheduleDays(days: number[]): number[] {
  return [...new Set(days.map((day) => typeof day === 'number' ? day : Number.NaN))].sort((left, right) => left - right)
}

function assertScheduleDateRange(dateRange: NonNullable<TimeSchedule['dateRange']>, label: string): void {
  assertObjectKeys(dateRange, ['startDate', 'endDate'], `${label}日期范围`)
  if (dateRange.startDate !== undefined) assertScheduleDate(dateRange.startDate, `${label}开始日期`)
  if (dateRange.endDate !== undefined) assertScheduleDate(dateRange.endDate, `${label}结束日期`)
  if (dateRange.startDate && dateRange.endDate && dateRange.startDate > dateRange.endDate) {
    throw new Error(`${label}开始日期不能晚于结束日期`)
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

function defaultScheduleTimezone(): string {
  if (typeof Intl === 'undefined') return 'UTC'
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

function cloneScheduleDateRange<TDateRange extends TimeSchedule['dateRange']>(dateRange: TDateRange): TDateRange {
  return (dateRange
    ? {
        ...(dateRange.startDate !== undefined ? { startDate: dateRange.startDate } : {}),
        ...(dateRange.endDate !== undefined ? { endDate: dateRange.endDate } : {})
      }
    : undefined) as TDateRange
}

function cloneScheduleExceptions<TExceptions extends TimeSchedule['exceptions']>(exceptions: TExceptions): TExceptions {
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
  }) as TExceptions
}
