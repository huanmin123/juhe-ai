import type { AuditLogListParams } from '@/api/client'
import type { AuditLogListItem, AuditOutcome, AuditTrafficSource } from '@/types/domain'
import { selectedSystemAccountId } from '@/utils/systemAccountFilter'

export interface AuditLogFilterValues {
  accountIdFilter: string
  outcomeFilter: AuditOutcome | 'all'
  pathFilter: string
  sessionIdFilter: string
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
  if (filters.traceIdFilter.trim()) {
    return { active: 1, advanced: 0 }
  }
  const advanced = auditLogAdvancedFilterCount(filters)
  return {
    active: advanced,
    advanced
  }
}

export function auditLogHotSearchActiveFilterCount(value: string): number {
  return normalizeHotSearchKeywordInput(value) ? 1 : 0
}

export function auditLogListParams(filters: AuditLogFilterValues, pageState: AuditLogPageWindow): AuditLogListParams {
  const traceId = filters.traceIdFilter.trim()
  if (traceId) {
    return {
      page: pageState.current,
      pageSize: pageState.pageSize,
      traceId
    }
  }
  return {
    page: pageState.current,
    pageSize: pageState.pageSize,
    sessionId: filters.sessionIdFilter.trim() || undefined,
    accountId: filters.accountIdFilter || undefined,
    // The current Node API intentionally does not restore client_aborted to its
    // outcome whitelist. Request all rows and apply that legacy-only filter in
    // the UI instead of submitting an ignored server-side filter.
    outcome: filters.outcomeFilter === 'client_aborted' ? 'all' : filters.outcomeFilter,
    path: filters.pathFilter || undefined,
    systemAccountId: selectedSystemAccountId(filters.systemAccountFilter, true),
    trafficSource: filters.trafficSourceFilter === 'all' ? undefined : filters.trafficSourceFilter
  }
}

export function filterLegacyClientAbortedAuditRows(
  items: AuditLogListItem[],
  outcome: AuditOutcome | 'all'
): AuditLogListItem[] {
  return outcome === 'client_aborted'
    ? items.filter((item) => item.auditOutcome === 'client_aborted')
    : items
}

function auditLogAdvancedFilterCount(filters: AuditLogFilterValues): number {
  let count = 0
  if (filters.accountIdFilter) count += 1
  if (filters.outcomeFilter !== 'all') count += 1
  if (selectedSystemAccountId(filters.systemAccountFilter, true)) count += 1
  if (filters.pathFilter.trim()) count += 1
  if (filters.sessionIdFilter.trim()) count += 1
  if (filters.trafficSourceFilter !== 'all') count += 1
  return count
}

export function normalizeHotSearchKeywordInput(value: string): string {
  return value.trim()
}
