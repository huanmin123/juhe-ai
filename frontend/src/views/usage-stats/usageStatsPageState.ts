import type { Dayjs } from 'dayjs'

import { isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys, recentDateRange } from '@/shared/dateRange'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { UsageTrendMetric } from './usageTrendMetrics'

export interface UsageStatsFilters {
  systemAccountId: string
  systemAccount?: PrincipalSelection
}

export type UsageStatsPageState = {
  filters: UsageStatsFilters
  metric: UsageTrendMetric
  range?: {
    startDate: string
    endDate: string
  }
}

export const maxUsageStatsRangeDays = 31
export const accountUsagePageSize = 10
export const maxAddedTrendAccounts = 20
export const usageStatsMetricOptions: Array<{ label: string; value: UsageTrendMetric }> = [
  { label: '成本', value: 'cost' },
  { label: 'Token', value: 'tokens' },
  { label: '请求', value: 'requests' }
]

export function defaultUsageStatsDateRange(): [Dayjs, Dayjs] {
  return recentDateRange(maxUsageStatsRangeDays)
}

export function defaultUsageStatsPageState(): UsageStatsPageState {
  return {
    filters: { systemAccountId: allSystemAccountsValue, systemAccount: undefined },
    metric: 'cost'
  }
}

export function initialUsageStatsMetric(value: UsageTrendMetric | undefined): UsageTrendMetric {
  if (value && usageStatsMetricOptions.some((item) => item.value === value)) {
    return value
  }
  return 'cost'
}

export function parseUsageStatsDateRange(value?: { startDate?: string; endDate?: string }): [Dayjs, Dayjs] {
  return parseDateRangeKeys(value, { defaultRange: defaultUsageStatsDateRange, maxDays: maxUsageStatsRangeDays })
}

export function normalizeUsageStatsDateRange(value: [Dayjs, Dayjs]): [string, string] {
  return normalizeDateRangeKeys(value, { defaultRange: defaultUsageStatsDateRange, maxDays: maxUsageStatsRangeDays })
}

export function responseUsageStatsDateRange(value?: { startDate?: string; endDate?: string }): [Dayjs, Dayjs] | undefined {
  const start = parseDateKey(value?.startDate)
  const end = parseDateKey(value?.endDate)
  if (!start || !end || start.isAfter(end, 'day')) return undefined
  return [start.startOf('day'), end.startOf('day')]
}

export function isUsageStatsDateDisabled(current: Dayjs, calendarRange: readonly [Dayjs | null, Dayjs | null]): boolean {
  return isRecentWindowDateDisabled(current, calendarRange, maxUsageStatsRangeDays)
}
