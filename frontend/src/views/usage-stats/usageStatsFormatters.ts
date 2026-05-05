import { formatInteger } from '@/shared/formatters'
import type { AccountUsageSummary, UsageStatsWindowDefinition, UsageStatsWindowKey } from '@/types/domain'

export const displayWindowKeys: UsageStatsWindowKey[] = ['last1d', 'last3d', 'last7d', 'last15d', 'last30d']
export const detailWindowKeys: UsageStatsWindowKey[] = ['last1d', 'last3d', 'last7d', 'last15d', 'last30d', 'total']

export function isUsageWindowColumn(value: unknown): value is UsageStatsWindowKey {
  return typeof value === 'string' && detailWindowKeys.includes(value as UsageStatsWindowKey)
}

export function defaultUsageWindows(): UsageStatsWindowDefinition[] {
  return [
    { key: 'last1d', label: '近1天', days: 1 },
    { key: 'last3d', label: '近3天', days: 3 },
    { key: 'last7d', label: '近一周', days: 7 },
    { key: 'last15d', label: '近半月', days: 15 },
    { key: 'last30d', label: '近一月', days: 30 },
    { key: 'total', label: '总用量' }
  ]
}

export function currentUsageWindows(source?: UsageStatsWindowDefinition[]) {
  const sourceWindows = source?.length ? source : defaultUsageWindows()
  return sourceWindows.filter((window) => detailWindowKeys.includes(window.key))
}

export function formatUsageBrief(usage?: AccountUsageSummary) {
  return `${formatInteger(usage?.requestCount)} 次 / ${formatInteger(usage?.totalTokens)} Token`
}

export function formatPrincipalName(name?: string, username?: string) {
  return name || username || '未知账户'
}
