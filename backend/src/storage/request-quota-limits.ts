import type { RequestHourlyQuotaLimit, RequestQuotaLimit, RequestQuotaLimits } from '../domain/types.js'

const MAX_HOURLY_WINDOW_HOURS = 24 * 30
const MAX_SAFE_QUOTA_LIMIT = Number.MAX_SAFE_INTEGER

export function emptyRequestQuotaLimits(): RequestQuotaLimits {
  return {}
}

export function normalizeRequestQuotaLimits(value: unknown, fallback: RequestQuotaLimits = emptyRequestQuotaLimits()): RequestQuotaLimits {
  if (value === undefined) {
    return fallback
  }
  if (value === null) {
    return emptyRequestQuotaLimits()
  }
  const source = typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  return stripDisabledQuotaLimits({
    hourly: normalizeHourlyQuotaLimit(source.hourly),
    daily: normalizeQuotaLimit(source.daily),
    weekly: normalizeQuotaLimit(source.weekly),
    monthly: normalizeQuotaLimit(source.monthly),
    total: normalizeQuotaLimit(source.total)
  })
}

export function parseRequestQuotaLimitsJson(value: string | null | undefined): RequestQuotaLimits {
  if (!value?.trim()) {
    return emptyRequestQuotaLimits()
  }
  try {
    return normalizeRequestQuotaLimits(JSON.parse(value) as unknown)
  } catch {
    return emptyRequestQuotaLimits()
  }
}

export function requestQuotaLimitsJson(value: RequestQuotaLimits): string | null {
  const normalized = normalizeRequestQuotaLimits(value)
  return hasEnabledRequestQuotaLimit(normalized) ? JSON.stringify(normalized) : null
}

export function hasEnabledRequestQuotaLimit(value: RequestQuotaLimits | undefined): boolean {
  return Boolean(
    value?.hourly?.enabled
    || value?.daily?.enabled
    || value?.weekly?.enabled
    || value?.monthly?.enabled
    || value?.total?.enabled
  )
}

function normalizeQuotaLimit(value: unknown): RequestQuotaLimit | undefined {
  const source = typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
  const limit = positiveInteger(source.limit)
  return {
    enabled: source.enabled === true && limit !== undefined,
    limit: limit ?? 0
  }
}

function normalizeHourlyQuotaLimit(value: unknown): RequestHourlyQuotaLimit | undefined {
  const source = typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
  const base = normalizeQuotaLimit(source)
  const hours = positiveInteger(source.hours)
  return {
    enabled: Boolean(base?.enabled && hours),
    hours: clampInteger(hours ?? 1, 1, MAX_HOURLY_WINDOW_HOURS),
    limit: base?.limit ?? 0
  }
}

function stripDisabledQuotaLimits(value: RequestQuotaLimits): RequestQuotaLimits {
  return {
    ...(value.hourly?.enabled ? { hourly: value.hourly } : {}),
    ...(value.daily?.enabled ? { daily: value.daily } : {}),
    ...(value.weekly?.enabled ? { weekly: value.weekly } : {}),
    ...(value.monthly?.enabled ? { monthly: value.monthly } : {}),
    ...(value.total?.enabled ? { total: value.total } : {})
  }
}

function positiveInteger(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' && value.trim() ? Number(value) : NaN
  if (!Number.isFinite(number)) return undefined
  const integer = Math.floor(number)
  return integer > 0 ? clampInteger(integer, 1, MAX_SAFE_QUOTA_LIMIT) : undefined
}

function clampInteger(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}
