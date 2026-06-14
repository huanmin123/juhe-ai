import type { AuditLogListParams } from '@/api/client'
import type { AuditOutcome, AuditTrafficSource } from '@/types/domain'
import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import { normalizedStatusCode } from './auditLogFormatters'

export interface AuditLogFilterValues {
  accountIdFilter: string
  outcomeFilter: AuditOutcome | 'all'
  pathFilter: string
  statusCodeFilter: string
  systemAccountFilter: string
  traceIdFilter: string
  trafficSourceFilter: AuditTrafficSource | 'all'
}

export interface AuditLogPageWindow {
  current: number
  pageSize: number
}

export interface AuditLogFilterCounts {
  active: number
  advanced: number
}

export function auditLogFilterCounts(filters: AuditLogFilterValues): AuditLogFilterCounts {
  const advanced = auditLogAdvancedFilterCount(filters)
  return {
    active: advanced + (filters.traceIdFilter.trim() ? 1 : 0),
    advanced
  }
}

export function auditLogHotSearchActiveFilterCount(value: string): number {
  return normalizeHotSearchKeywordInput(value) ? 1 : 0
}

export function auditLogListParams(filters: AuditLogFilterValues, pageState: AuditLogPageWindow): AuditLogListParams {
  return {
    page: pageState.current,
    pageSize: pageState.pageSize,
    traceId: filters.traceIdFilter.trim() || undefined,
    accountId: filters.accountIdFilter || undefined,
    outcome: filters.outcomeFilter,
    path: filters.pathFilter || undefined,
    statusCode: normalizedStatusCode(filters.statusCodeFilter),
    systemAccountId: selectedSystemAccountId(filters.systemAccountFilter, true),
    trafficSource: filters.trafficSourceFilter === 'all' ? undefined : filters.trafficSourceFilter
  }
}

function auditLogAdvancedFilterCount(filters: AuditLogFilterValues): number {
  let count = 0
  if (filters.accountIdFilter) count += 1
  if (filters.outcomeFilter !== 'all') count += 1
  if (filters.systemAccountFilter !== allSystemAccountsValue) count += 1
  if (filters.pathFilter.trim()) count += 1
  if (filters.statusCodeFilter.trim()) count += 1
  if (filters.trafficSourceFilter !== 'all') count += 1
  return count
}

export function normalizeHotSearchKeywordInput(value: string): string {
  return value.trim()
}
