import type { OperationLogListParams } from '@/api/client'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { normalizeCreatedAtRange, type CreatedAtRangeValue } from './operationLogPageState'

export interface OperationLogFilterValues {
  actionFilter: string
  actorSystemAccountFilter: string
  affectedSystemAccountFilter: string
  createdAtRange: CreatedAtRangeValue
  resourceIdFilter: string
  resourceTypeFilter: string
  summaryKeywordFilter: string
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  traceIdFilter: string
}

export interface OperationLogFilterCounts {
  active: number
  advanced: number
}

export interface OperationLogPageWindow {
  current: number
  pageSize: number
}

export function operationLogFilterCounts(filters: OperationLogFilterValues, isManagementView: boolean): OperationLogFilterCounts {
  const advanced = operationLogAdvancedFilterCount(filters, isManagementView)
  return {
    active: advanced + (filters.summaryKeywordFilter.trim() ? 1 : 0),
    advanced
  }
}

export function operationLogListParams(filters: OperationLogFilterValues, pageState: OperationLogPageWindow, isManagementView: boolean): OperationLogListParams {
  const range = normalizeCreatedAtRange(filters.createdAtRange)
  return {
    page: pageState.current,
    pageSize: pageState.pageSize,
    summaryKeyword: filters.summaryKeywordFilter.trim() || undefined,
    module: filters.moduleFilter === 'all' ? undefined : filters.moduleFilter,
    action: filters.actionFilter === 'all' ? undefined : filters.actionFilter,
    resourceType: filters.resourceTypeFilter === 'all' ? undefined : filters.resourceTypeFilter,
    resourceId: filters.resourceIdFilter.trim() || undefined,
    startAt: range?.[0].toISOString(),
    endAt: range?.[1].toISOString(),
    traceId: filters.traceIdFilter.trim() || undefined,
    actorSystemAccountId: operationLogAdminAccountFilter(filters.actorSystemAccountFilter, isManagementView),
    affectedSystemAccountId: operationLogAdminAccountFilter(filters.affectedSystemAccountFilter, isManagementView),
    operationScopeSystemAccountId: operationLogAdminAccountFilter(filters.operationScopeSystemAccountFilter, isManagementView)
  }
}

function operationLogAdvancedFilterCount(filters: OperationLogFilterValues, isManagementView: boolean): number {
  let count = 0
  if (filters.moduleFilter !== 'all') count += 1
  if (filters.actionFilter !== 'all') count += 1
  if (filters.resourceTypeFilter !== 'all') count += 1
  if (filters.resourceIdFilter.trim()) count += 1
  if (normalizeCreatedAtRange(filters.createdAtRange)) count += 1
  if (filters.traceIdFilter.trim()) count += 1
  if (operationLogAdminAccountFilter(filters.actorSystemAccountFilter, isManagementView)) count += 1
  if (operationLogAdminAccountFilter(filters.affectedSystemAccountFilter, isManagementView)) count += 1
  if (operationLogAdminAccountFilter(filters.operationScopeSystemAccountFilter, isManagementView)) count += 1
  return count
}

function operationLogAdminAccountFilter(value: string, isManagementView: boolean): string | undefined {
  return isManagementView && value !== allSystemAccountsValue ? value : undefined
}
