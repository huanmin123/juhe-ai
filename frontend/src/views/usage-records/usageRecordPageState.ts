import dayjs, { type Dayjs } from 'dayjs'

import type { UsageRecordListParams } from '@/api/client'
import { formatDateKey, normalizeDayjsDateRange, parseDateKey } from '@/shared/dateRange'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { UsageRecordTrafficSource } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

export type UsageRecordSortField = NonNullable<UsageRecordListParams['sortBy']>
export type UsageRecordTableSortOrder = 'ascend' | 'descend' | null
export type UsageRecordDateMode = 'auto' | 'manual'

export type UsageRecordsPageState = {
  accountNameFilter: string
  clientIpFilter: string
  dateRangeFilter?: [string, string]
  dateMode: UsageRecordDateMode
  groupFilter?: GroupSelection
  modelFilter: string
  pagination: { current: number; pageSize: number }
  resultFilter: 'all' | 'success' | 'failed'
  sortState: { field: UsageRecordSortField; order: UsageRecordTableSortOrder }
  statusCodeFilter: string
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
  traceIdFilter: string
  trafficSourceFilter: UsageRecordTrafficSource | 'all'
}

export const usageRecordsPageSize = 20

export function defaultUsageRecordsPageState(trafficSourceFilter: UsageRecordTrafficSource | 'all' = 'all'): UsageRecordsPageState {
  const today = formatDateKey(dayjs())
  return {
    accountNameFilter: '',
    clientIpFilter: '',
    dateRangeFilter: [today, today],
    dateMode: 'auto',
    groupFilter: undefined,
    modelFilter: '',
    pagination: { current: 1, pageSize: usageRecordsPageSize },
    resultFilter: 'all',
    sortState: { field: 'createdAt', order: 'descend' },
    statusCodeFilter: '',
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined,
    traceIdFilter: '',
    trafficSourceFilter
  }
}

export function defaultUsageRecordTrafficSourceFilter(isManagementView: boolean): UsageRecordTrafficSource | 'all' {
  return isManagementView ? 'all' : 'gateway'
}

export function parseUsageRecordDateRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = parseDateKey(value[0])
  const end = parseDateKey(value[1])
  return start && end ? normalizeDayjsDateRange([start, end]) : undefined
}

export function usageRecordDateRangeParam(value?: [Dayjs, Dayjs]): [string, string] | undefined {
  const normalized = normalizeDayjsDateRange(value)
  return normalized ? [formatDateKey(normalized[0]), formatDateKey(normalized[1])] : undefined
}
