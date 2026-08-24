import { createHash } from 'node:crypto'

export type QuotaRecoveryAccountType = 'api_key' | 'oauth' | 'google_oauth'
export type QuotaRecoveryStrategy = 'duration' | 'daily' | 'weekly'

export interface QuotaRecoverySchedule {
  reset_strategy: QuotaRecoveryStrategy
  duration_minutes?: number
  daily_reset_hour?: number
  weekly_reset_day?: number
  weekly_reset_hour?: number
  timezone?: string
  jitter_minutes?: number
}

export interface QuotaRecoveryPolicy {
  api_key?: QuotaRecoverySchedule
  oauth?: QuotaRecoverySchedule
  google_oauth?: QuotaRecoverySchedule
}

const FIXED_JITTER_MINUTES = 15
const MAX_POLICY_BYTES = 4096
const MAX_DURATION_MINUTES = 7 * 24 * 60
const MAX_JITTER_MINUTES = FIXED_JITTER_MINUTES

export const DEFAULT_API_KEY_QUOTA_RECOVERY_SCHEDULE: QuotaRecoverySchedule = {
  reset_strategy: 'duration',
  duration_minutes: 60,
  jitter_minutes: FIXED_JITTER_MINUTES,
  timezone: 'UTC'
}

export const DEFAULT_OAUTH_QUOTA_RECOVERY_SCHEDULE: QuotaRecoverySchedule = {
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  jitter_minutes: FIXED_JITTER_MINUTES,
  timezone: 'UTC'
}

export function normalizeQuotaRecoveryPolicy(value: unknown): QuotaRecoveryPolicy {
  if (value === undefined || value === null) return {}
  if (typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('额度恢复策略必须是对象')
  }
  const input = value as Record<string, unknown>
  const allowed = new Set(['api_key', 'oauth', 'google_oauth'])
  for (const key of Object.keys(input)) {
    if (!allowed.has(key)) throw new Error(`额度恢复策略字段 ${key} 不受支持`)
  }
  const output: QuotaRecoveryPolicy = {}
  for (const accountType of allowed) {
    if (Object.prototype.hasOwnProperty.call(input, accountType)) {
      output[accountType as keyof QuotaRecoveryPolicy] = normalizeQuotaRecoverySchedule(input[accountType])
    }
  }
  if (JSON.stringify(output).length > MAX_POLICY_BYTES) throw new Error('额度恢复策略过大')
  return output
}

export function quotaRecoveryScheduleForAccount(
  policy: QuotaRecoveryPolicy | undefined,
  accountType: QuotaRecoveryAccountType
): QuotaRecoverySchedule {
  const configured = policy?.[accountType]
  const fallback = accountType === 'api_key'
    ? DEFAULT_API_KEY_QUOTA_RECOVERY_SCHEDULE
    : DEFAULT_OAUTH_QUOTA_RECOVERY_SCHEDULE
  return {
    ...fallback,
    ...(configured ?? {})
  }
}

export function quotaRecoveryCooldownUntil(input: {
  policy?: QuotaRecoveryPolicy
  accountType: QuotaRecoveryAccountType
  seed: string
  now?: Date
}): string {
  const now = input.now ?? new Date()
  const schedule = quotaRecoveryScheduleForAccount(input.policy, input.accountType)
  const jitter = deterministicJitterMinutes(`${input.seed}:${schedule.reset_strategy}`, schedule.jitter_minutes ?? FIXED_JITTER_MINUTES)
  const boundary = scheduleBoundary(schedule, now)
  return new Date(boundary.getTime() + jitter * 60_000).toISOString()
}

export function deterministicJitterMinutes(seed: string, maxMinutes: number): number {
  const max = Math.max(0, Math.min(MAX_JITTER_MINUTES, Math.floor(maxMinutes)))
  if (max === 0) return 0
  const digest = createHash('sha256').update(seed).digest()
  return digest.readUInt32BE(0) % (max + 1)
}

function normalizeQuotaRecoverySchedule(value: unknown): QuotaRecoverySchedule {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('额度恢复策略项必须是对象')
  }
  const input = value as Record<string, unknown>
  const strategy = input.reset_strategy
  if (strategy !== 'duration' && strategy !== 'daily' && strategy !== 'weekly') {
    throw new Error('额度恢复策略 reset_strategy 必须是 duration、daily 或 weekly')
  }
  const output: QuotaRecoverySchedule = { reset_strategy: strategy }
  if (strategy === 'duration') {
    output.duration_minutes = integerInRange(input.duration_minutes, 30, MAX_DURATION_MINUTES, 'duration_minutes')
  }
  if (strategy === 'daily') {
    output.daily_reset_hour = integerInRange(input.daily_reset_hour, 0, 23, 'daily_reset_hour')
  }
  if (strategy === 'weekly') {
    output.weekly_reset_day = integerInRange(input.weekly_reset_day, 0, 6, 'weekly_reset_day')
    output.weekly_reset_hour = integerInRange(input.weekly_reset_hour, 0, 23, 'weekly_reset_hour')
  }
  const jitter = input.jitter_minutes === undefined ? FIXED_JITTER_MINUTES : input.jitter_minutes
  if (jitter !== FIXED_JITTER_MINUTES) throw new Error('额度恢复策略 jitter_minutes固定15、实际0–15')
  output.jitter_minutes = FIXED_JITTER_MINUTES
  const timezone = input.timezone === undefined ? 'UTC' : input.timezone
  if (typeof timezone !== 'string' || !timezone.trim()) throw new Error('额度恢复策略 timezone 无效')
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: timezone }).format()
  } catch {
    throw new Error(`额度恢复策略 timezone 无效：${timezone}`)
  }
  output.timezone = timezone.trim()
  return output
}

function integerInRange(value: unknown, min: number, max: number, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    throw new Error(`额度恢复策略 ${label} 必须是 ${min}-${max} 的整数`)
  }
  return value
}

function scheduleBoundary(schedule: QuotaRecoverySchedule, now: Date): Date {
  if (schedule.reset_strategy === 'duration') {
    return new Date(now.getTime() + (schedule.duration_minutes ?? 60) * 60_000)
  }
  const timezone = schedule.timezone ?? 'UTC'
  const local = localDateParts(now, timezone)
  const targetHour = schedule.reset_strategy === 'weekly'
    ? schedule.weekly_reset_hour ?? 0
    : schedule.daily_reset_hour ?? 0
  let dayDelta = schedule.reset_strategy === 'weekly'
    ? ((schedule.weekly_reset_day ?? 0) - local.weekday + 7) % 7
    : 0
  let candidate = zonedLocalDate(local.year, local.month, local.day + dayDelta, targetHour, timezone)
  if (candidate.getTime() <= now.getTime()) {
    dayDelta += schedule.reset_strategy === 'weekly' ? 7 : 1
    candidate = zonedLocalDate(local.year, local.month, local.day + dayDelta, targetHour, timezone)
  }
  return candidate
}

function localDateParts(date: Date, timezone: string): { year: number; month: number; day: number; weekday: number } {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    year: 'numeric', month: '2-digit', day: '2-digit', weekday: 'short'
  }).formatToParts(date)
  const get = (type: string) => parts.find((part) => part.type === type)?.value ?? ''
  const weekday = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(get('weekday'))
  return { year: Number(get('year')), month: Number(get('month')), day: Number(get('day')), weekday: weekday < 0 ? 0 : weekday }
}

function zonedLocalDate(year: number, month: number, day: number, hour: number, timezone: string): Date {
  const guess = new Date(Date.UTC(year, month - 1, day, hour, 0, 0, 0))
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: timezone, timeZoneName: 'longOffset' }).formatToParts(guess)
  const offset = parts.find((part) => part.type === 'timeZoneName')?.value ?? 'GMT'
  const match = /^GMT(?:(\+|-)(\d{2}):(\d{2}))?$/.exec(offset)
  const offsetMinutes = match?.[1]
    ? (Number(match[2]) * 60 + Number(match[3])) * (match[1] === '-' ? -1 : 1)
    : 0
  return new Date(guess.getTime() - offsetMinutes * 60_000)
}
