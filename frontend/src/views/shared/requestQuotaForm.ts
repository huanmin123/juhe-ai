import type { RequestQuotaLimits } from '@/types/domain'

export type QuotaPeriodKey = 'daily' | 'weekly' | 'monthly' | 'total'

export const quotaLimitItems: Array<{ key: QuotaPeriodKey; label: string }> = [
  { key: 'daily', label: '日额度（每日 0 点重置）' },
  { key: 'weekly', label: '周额度（每周一 0 点重置）' },
  { key: 'monthly', label: '月额度（每月 1 号 0 点重置）' },
  { key: 'total', label: '总额度（累计）' }
]

export function createQuotaLimitForm(source?: RequestQuotaLimits) {
  return {
    hourly: { enabled: Boolean(source?.hourly?.enabled), hours: source?.hourly?.hours ?? 1, limit: source?.hourly?.limit ?? 1 },
    daily: { enabled: Boolean(source?.daily?.enabled), limit: source?.daily?.limit ?? 1 },
    weekly: { enabled: Boolean(source?.weekly?.enabled), limit: source?.weekly?.limit ?? 1 },
    monthly: { enabled: Boolean(source?.monthly?.enabled), limit: source?.monthly?.limit ?? 1 },
    total: { enabled: Boolean(source?.total?.enabled), limit: source?.total?.limit ?? 1 }
  }
}

export type RequestQuotaFormModel = ReturnType<typeof createQuotaLimitForm>

export function quotaLimitsPayload(form: RequestQuotaFormModel): RequestQuotaLimits {
  return {
    ...(form.hourly.enabled ? { hourly: { enabled: true, hours: form.hourly.hours, limit: form.hourly.limit } } : {}),
    ...(form.daily.enabled ? { daily: { enabled: true, limit: form.daily.limit } } : {}),
    ...(form.weekly.enabled ? { weekly: { enabled: true, limit: form.weekly.limit } } : {}),
    ...(form.monthly.enabled ? { monthly: { enabled: true, limit: form.monthly.limit } } : {}),
    ...(form.total.enabled ? { total: { enabled: true, limit: form.total.limit } } : {})
  }
}

export function hasQuotaLimits(limits?: RequestQuotaLimits): boolean {
  return Boolean(limits?.hourly?.enabled || limits?.daily?.enabled || limits?.weekly?.enabled || limits?.monthly?.enabled || limits?.total?.enabled)
}
