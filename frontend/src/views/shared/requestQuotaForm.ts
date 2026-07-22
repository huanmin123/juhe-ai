import type { RequestQuotaLimits } from '@/types/domain'

export type QuotaPeriodKey = 'daily' | 'weekly' | 'monthly' | 'total'
type QuotaLimitSection = { enabled: boolean; limit: number }
type HourlyQuotaLimitSection = QuotaLimitSection & { hours: number }

const maxHourlyQuotaWindowHours = 24 * 30
const maxQuotaAmount = Number.MAX_SAFE_INTEGER
const quotaAmountPrecision = 1_000_000

export const quotaLimitItems: Array<{ key: QuotaPeriodKey; label: string }> = [
  { key: 'daily', label: '日美元额度（每日 0 点重置）' },
  { key: 'weekly', label: '周美元额度（每周一 0 点重置）' },
  { key: 'monthly', label: '月美元额度（每月 1 号 0 点重置）' },
  { key: 'total', label: '总美元额度（累计）' }
]

export function createQuotaLimitForm(source?: RequestQuotaLimits) {
  return {
    hourly: normalizeHourlyQuotaLimitForm(source?.hourly),
    daily: normalizeQuotaLimitForm(source?.daily, '日额度'),
    weekly: normalizeQuotaLimitForm(source?.weekly, '周额度'),
    monthly: normalizeQuotaLimitForm(source?.monthly, '月额度'),
    total: normalizeQuotaLimitForm(source?.total, '总额度')
  }
}

export type RequestQuotaFormModel = ReturnType<typeof createQuotaLimitForm>

export function quotaLimitsPayload(form: RequestQuotaFormModel): RequestQuotaLimits {
  return {
    ...(form.hourly.enabled ? { hourly: enabledHourlyQuotaLimitPayload(form.hourly) } : {}),
    ...(form.daily.enabled ? { daily: enabledQuotaLimitPayload(form.daily, '日额度') } : {}),
    ...(form.weekly.enabled ? { weekly: enabledQuotaLimitPayload(form.weekly, '周额度') } : {}),
    ...(form.monthly.enabled ? { monthly: enabledQuotaLimitPayload(form.monthly, '月额度') } : {}),
    ...(form.total.enabled ? { total: enabledQuotaLimitPayload(form.total, '总额度') } : {})
  }
}

export function hasQuotaLimits(limits?: RequestQuotaLimits): boolean {
  return Boolean(limits?.hourly?.enabled || limits?.daily?.enabled || limits?.weekly?.enabled || limits?.monthly?.enabled || limits?.total?.enabled)
}

function normalizeQuotaLimitForm(value: unknown, label: string): QuotaLimitSection {
  if (value === undefined) return { enabled: false, limit: 1 }
  const record = quotaLimitRecord(value, label)
  assertQuotaLimitKeys(record, ['enabled', 'limit'], label)
  if (record.enabled !== true) {
    throw new Error(`${label}启用状态必须为 true`)
  }
  return {
    enabled: true,
    limit: quotaAmount(record.limit, label)
  }
}

function normalizeHourlyQuotaLimitForm(value: unknown): HourlyQuotaLimitSection {
  if (value === undefined) return { enabled: false, hours: 1, limit: 1 }
  const record = quotaLimitRecord(value, '小时额度')
  assertQuotaLimitKeys(record, ['enabled', 'hours', 'limit'], '小时额度')
  if (record.enabled !== true) {
    throw new Error('小时额度启用状态必须为 true')
  }
  return {
    enabled: true,
    hours: hourlyWindowHours(record.hours),
    limit: quotaAmount(record.limit, '小时额度')
  }
}

function enabledQuotaLimitPayload(section: QuotaLimitSection, label: string): QuotaLimitSection {
  return {
    enabled: true,
    limit: quotaAmount(section.limit, label)
  }
}

function enabledHourlyQuotaLimitPayload(section: HourlyQuotaLimitSection): HourlyQuotaLimitSection {
  return {
    enabled: true,
    hours: hourlyWindowHours(section.hours),
    limit: quotaAmount(section.limit, '小时额度')
  }
}

function quotaLimitRecord(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(`${label}参数无效`)
  }
  return value as Record<string, unknown>
}

function assertQuotaLimitKeys(record: Record<string, unknown>, allowedKeys: string[], label: string): void {
  const allowed = new Set(allowedKeys)
  const unknownKeys = Object.keys(record).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含不支持字段：${unknownKeys.join('、')}`)
  }
}

function hourlyWindowHours(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > maxHourlyQuotaWindowHours) {
    throw new Error(`小时额度窗口必须在 1-${maxHourlyQuotaWindowHours} 之间`)
  }
  return value
}

function quotaAmount(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0 || value > maxQuotaAmount) {
    throw new Error(`${label}金额必须是大于 0 的数字`)
  }
  const scaled = value * quotaAmountPrecision
  if (Math.round(scaled) !== scaled) {
    throw new Error(`${label}金额最多支持 6 位小数`)
  }
  return Math.round(scaled) / quotaAmountPrecision
}
