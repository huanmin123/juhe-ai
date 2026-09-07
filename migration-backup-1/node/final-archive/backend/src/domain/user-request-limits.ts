import type { EffectiveUserRequestLimits, UserRequestLimits, UserRequestLimitWindow } from './types.js'

export const USER_REQUEST_LIMIT_WINDOWS = ['perMinute', 'perDay', 'perWeek', 'perMonth'] as const satisfies readonly UserRequestLimitWindow[]
export const MAX_USER_REQUEST_LIMIT = 1_000_000_000

export interface GlobalUserRequestLimitSettings {
  gatewayUserRequestLimitPerMinute: number
  gatewayUserRequestLimitPerDay: number
  gatewayUserRequestLimitPerWeek: number
  gatewayUserRequestLimitPerMonth: number
  usageStatsTimezone: string
}

export function normalizeUserRequestLimits(input: unknown): UserRequestLimits | undefined {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return undefined
  const source = input as Record<string, unknown>
  const normalized: UserRequestLimits = {}
  for (const window of USER_REQUEST_LIMIT_WINDOWS) {
    if (!Object.prototype.hasOwnProperty.call(source, window)) continue
    const value = source[window]
    if (!Number.isInteger(value) || Number(value) < 0 || Number(value) > MAX_USER_REQUEST_LIMIT) {
      throw new Error(`${window} 必须是 0 到 ${MAX_USER_REQUEST_LIMIT} 之间的整数`)
    }
    normalized[window] = Number(value)
  }
  const hasWindowOverride = USER_REQUEST_LIMIT_WINDOWS.some((window) => normalized[window] !== undefined)
  if (!hasWindowOverride) return undefined
  if (Object.prototype.hasOwnProperty.call(source, 'expiresOn') && source.expiresOn !== undefined && source.expiresOn !== null && source.expiresOn !== '') {
    normalized.expiresOn = normalizeUserRequestLimitExpiresOn(source.expiresOn)
  }
  return normalized
}

export function parseUserRequestLimitsJson(value: string | null | undefined): UserRequestLimits | undefined {
  if (!value) return undefined
  try {
    return normalizeUserRequestLimits(JSON.parse(value) as unknown)
  } catch {
    return undefined
  }
}

export function serializeUserRequestLimits(input: unknown): string | null {
  const normalized = normalizeUserRequestLimits(input)
  return normalized ? JSON.stringify(normalized) : null
}

export function resolveEffectiveUserRequestLimits(
  globalSettings: GlobalUserRequestLimitSettings,
  overrides: UserRequestLimits | undefined,
  nowMs = Date.now()
): EffectiveUserRequestLimits {
  const active = isUserRequestLimitOverrideActive(overrides, globalSettings.usageStatsTimezone, nowMs)
  const effectiveOverrides = active ? overrides : undefined
  return {
    perMinute: effectiveValue(globalSettings.gatewayUserRequestLimitPerMinute, effectiveOverrides, 'perMinute'),
    perDay: effectiveValue(globalSettings.gatewayUserRequestLimitPerDay, effectiveOverrides, 'perDay'),
    perWeek: effectiveValue(globalSettings.gatewayUserRequestLimitPerWeek, effectiveOverrides, 'perWeek'),
    perMonth: effectiveValue(globalSettings.gatewayUserRequestLimitPerMonth, effectiveOverrides, 'perMonth'),
    timezone: globalSettings.usageStatsTimezone,
    ...(overrides?.expiresOn ? { overrideExpiresOn: overrides.expiresOn } : {}),
    overrideActive: active
  }
}

export function isUserRequestLimitOverrideActive(
  overrides: UserRequestLimits | undefined,
  timezone: string,
  nowMs = Date.now()
): boolean {
  if (!overrides) return false
  if (!overrides.expiresOn) return true
  return localDateKey(timezone || 'UTC', nowMs) <= overrides.expiresOn
}

export function normalizeUserRequestLimitExpiresOn(value: unknown): string {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    throw new Error('expiresOn 必须是 YYYY-MM-DD 格式的有效日期')
  }
  const [year, month, day] = value.split('-').map(Number)
  const parsed = new Date(Date.UTC(year ?? 0, (month ?? 0) - 1, day ?? 0))
  if (parsed.getUTCFullYear() !== year || parsed.getUTCMonth() + 1 !== month || parsed.getUTCDate() !== day) {
    throw new Error('expiresOn 必须是 YYYY-MM-DD 格式的有效日期')
  }
  return value
}

function effectiveValue(globalLimit: number, overrides: UserRequestLimits | undefined, window: UserRequestLimitWindow) {
  const userLimit = overrides?.[window]
  return userLimit === undefined
    ? { limit: globalLimit, source: 'global' as const }
    : { limit: userLimit, source: 'user' as const }
}

function localDateKey(timezone: string, nowMs: number): string {
  const formatter = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  })
  let year = ''
  let month = ''
  let day = ''
  for (const part of formatter.formatToParts(nowMs)) {
    if (part.type === 'year') year = part.value
    else if (part.type === 'month') month = part.value
    else if (part.type === 'day') day = part.value
  }
  return `${year}-${month}-${day}`
}
