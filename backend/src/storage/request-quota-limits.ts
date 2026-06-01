import type { RequestHourlyQuotaLimit, RequestQuotaLimit, RequestQuotaLimits } from '../domain/types.js'

export const maxRequestQuotaHourlyWindowHours = 24 * 30
export const defaultRequestQuotaHourlyWindowHours = [1, 3, 6, 12, 24, 72, 168, 720] as const
export const maxRequestQuotaAmountUsd = Number.MAX_SAFE_INTEGER
const QUOTA_AMOUNT_PRECISION = 1_000_000
const quotaLimitKeys = ['hourly', 'daily', 'weekly', 'monthly', 'total'] as const

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
  if (!isRecord(value)) {
    throw new Error('请求额度限制参数无效')
  }
  assertOnlyKeys(value, quotaLimitKeys, '请求额度限制')
  return stripDisabledQuotaLimits({
    hourly: normalizeHourlyQuotaLimit(value.hourly),
    daily: normalizeQuotaLimit(value.daily, '日额度'),
    weekly: normalizeQuotaLimit(value.weekly, '周额度'),
    monthly: normalizeQuotaLimit(value.monthly, '月额度'),
    total: normalizeQuotaLimit(value.total, '总额度')
  })
}

export function parseRequestQuotaLimitsJson(value: string | null | undefined): RequestQuotaLimits {
  if (!value?.trim()) {
    return emptyRequestQuotaLimits()
  }
  return normalizeRequestQuotaLimits(JSON.parse(value) as unknown)
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

function normalizeQuotaLimit(value: unknown, label: string): RequestQuotaLimit | undefined {
  if (value === undefined) return undefined
  if (!isRecord(value)) {
    throw new Error(`${label}参数无效`)
  }
  assertOnlyKeys(value, ['enabled', 'limit'], label)
  if (value.enabled !== true) {
    throw new Error(`${label}启用状态必须为 true`)
  }
  const limit = positiveAmount(value.limit, label)
  return {
    enabled: true,
    limit
  }
}

function normalizeHourlyQuotaLimit(value: unknown): RequestHourlyQuotaLimit | undefined {
  if (value === undefined) return undefined
  if (!isRecord(value)) {
    throw new Error('小时额度参数无效')
  }
  assertOnlyKeys(value, ['enabled', 'limit', 'hours'], '小时额度')
  if (value.enabled !== true) {
    throw new Error('小时额度启用状态必须为 true')
  }
  const limit = positiveAmount(value.limit, '小时额度')
  const hours = positiveInteger(value.hours, '小时额度窗口')
  return {
    enabled: true,
    hours,
    limit
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

function positiveInteger(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`${label}必须是数字`)
  }
  if (value <= 0 || value > maxRequestQuotaHourlyWindowHours) {
    throw new Error(`${label}必须在 1-${maxRequestQuotaHourlyWindowHours} 之间`)
  }
  return value
}

function positiveAmount(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0 || value > maxRequestQuotaAmountUsd) {
    throw new Error(`${label}金额必须是大于 0 的数字`)
  }
  const scaled = value * QUOTA_AMOUNT_PRECISION
  if (Math.round(scaled) !== scaled) {
    throw new Error(`${label}金额最多支持 6 位小数`)
  }
  return Math.round(scaled) / QUOTA_AMOUNT_PRECISION
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
