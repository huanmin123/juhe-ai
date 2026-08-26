import { formatUsd } from '@/shared/formatters'
import type { RequestQuotaLimits } from '@/types/domain'
import { hasQuotaLimits } from './requestQuotaForm'

export function quotaLimitSummaryText(limits?: RequestQuotaLimits): string {
  if (!hasQuotaLimits(limits)) return '未限制'
  const safeLimits = limits as RequestQuotaLimits
  const items: string[] = []
  if (safeLimits.hourly?.enabled) items.push(`${safeLimits.hourly.hours}小时 ${formatUsd(safeLimits.hourly.limit)}`)
  if (safeLimits.daily?.enabled) items.push(`日 ${formatUsd(safeLimits.daily.limit)}`)
  if (safeLimits.weekly?.enabled) items.push(`周 ${formatUsd(safeLimits.weekly.limit)}`)
  if (safeLimits.monthly?.enabled) items.push(`月 ${formatUsd(safeLimits.monthly.limit)}`)
  if (safeLimits.total?.enabled) items.push(`总 ${formatUsd(safeLimits.total.limit)}`)
  return items.join(' / ')
}
