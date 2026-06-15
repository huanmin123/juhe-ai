import type { UsageRecordListParams } from '@/api/client'
import type { UsageRecordTrafficSource } from '@/types/domain'

import type { UsageRecordSortField } from './usageRecordPageState'

export interface UsageRecordFilterCountInput {
  accountName: string
  clientIp: string
  dateRangeSelected: boolean
  groupId?: string
  model: string
  result: 'all' | 'success' | 'failed'
  statusCode: string
  systemAccountId: string
  allSystemAccountsValue: string
  traceId: string
  trafficSource: UsageRecordTrafficSource | 'all'
}

export interface UsageRecordListParamsInput {
  page: number
  pageSize: number
  accountName: string
  clientIp: string
  dateRange?: [string, string]
  groupId?: string
  model: string
  result: 'all' | 'success' | 'failed'
  sortBy: UsageRecordSortField
  sortOrder: 'asc' | 'desc'
  statusCode: string
  systemAccountId?: string
  traceId: string
  trafficSource: UsageRecordTrafficSource | 'all'
}

export function usageRecordActiveFilterCount(input: UsageRecordFilterCountInput): number {
  let count = 0
  if (input.accountName.trim()) count += 1
  if (input.clientIp.trim()) count += 1
  if (input.dateRangeSelected) count += 1
  if (input.groupId) count += 1
  if (input.model.trim()) count += 1
  if (input.result !== 'all') count += 1
  if (input.statusCode) count += 1
  if (input.systemAccountId !== input.allSystemAccountsValue) count += 1
  if (input.traceId.trim()) count += 1
  if (input.trafficSource !== 'all') count += 1
  return count
}

export function usageRecordAdvancedFilterCount(input: UsageRecordFilterCountInput): number {
  let count = 0
  if (input.dateRangeSelected) count += 1
  if (input.result !== 'all') count += 1
  if (input.systemAccountId !== input.allSystemAccountsValue) count += 1
  if (input.groupId) count += 1
  if (input.clientIp.trim()) count += 1
  if (input.model.trim()) count += 1
  if (input.statusCode) count += 1
  if (input.traceId.trim()) count += 1
  if (input.trafficSource !== 'all') count += 1
  return count
}

export function normalizedUsageRecordStatusCode(value: string): number | undefined {
  const text = value.trim()
  if (!text) return undefined
  const statusCode = Number(text)
  return Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 599 ? statusCode : undefined
}

export function usageRecordListParams(input: UsageRecordListParamsInput): UsageRecordListParams {
  return {
    page: input.page,
    pageSize: input.pageSize,
    accountKeyword: input.accountName.trim() || undefined,
    clientIp: input.clientIp.trim() || undefined,
    startDate: input.dateRange?.[0],
    endDate: input.dateRange?.[1],
    groupId: input.groupId,
    model: input.model.trim() || undefined,
    result: input.result,
    statusCode: normalizedUsageRecordStatusCode(input.statusCode),
    systemAccountId: input.systemAccountId,
    traceId: input.traceId.trim() || undefined,
    trafficSource: input.trafficSource === 'all' ? undefined : input.trafficSource,
    sortBy: input.sortBy,
    sortOrder: input.sortOrder
  }
}
