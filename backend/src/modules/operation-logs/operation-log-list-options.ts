import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import type { OperationLogListOptions } from '../../storage/repositories.js'

const managementDefaultOperationLogWindowDays = 31

export function parseOperationLogListOptions(
  query: Record<string, unknown>,
  includeAdminFilters: boolean,
  now = new Date()
): OperationLogListOptions {
  const traceId = optionalQueryText(query.traceId)
  const requestedRange = dateTimeRangeQueryValue(query.startAt, query.endAt)
  const createdAtRange = includeAdminFilters && !isExactTraceId(traceId) && !requestedRange.startAt && !requestedRange.endAt
    ? defaultManagementOperationLogDateRange(now)
    : requestedRange
  return {
    page: finiteNumberQueryValue(query.page),
    pageSize: finiteNumberQueryValue(query.pageSize),
    summaryKeyword: optionalQueryText(query.summaryKeyword),
    module: optionalQueryText(query.module),
    action: optionalQueryText(query.action),
    resourceType: optionalQueryText(query.resourceType),
    resourceId: optionalQueryText(query.resourceId),
    traceId,
    startAt: createdAtRange.startAt,
    endAt: createdAtRange.endAt,
    actorSystemAccountId: includeAdminFilters ? optionalQueryText(query.actorSystemAccountId) : undefined,
    affectedSystemAccountId: includeAdminFilters ? optionalQueryText(query.affectedSystemAccountId) : undefined,
    operationScopeSystemAccountId: includeAdminFilters ? optionalQueryText(query.operationScopeSystemAccountId) : undefined
  }
}

export function defaultManagementOperationLogDateRange(now = new Date()): { startAt: string; endAt: string } {
  const endAt = new Date(now)
  endAt.setHours(23, 59, 59, 999)
  const startAt = new Date(endAt)
  startAt.setDate(startAt.getDate() - (managementDefaultOperationLogWindowDays - 1))
  startAt.setHours(0, 0, 0, 0)
  return { startAt: startAt.toISOString(), endAt: endAt.toISOString() }
}

function dateTimeRangeQueryValue(startValue: unknown, endValue: unknown): { startAt?: string; endAt?: string } {
  const startAt = dateTimeQueryValue(startValue)
  const endAt = dateTimeQueryValue(endValue)
  if (startAt && endAt && startAt > endAt) {
    return { startAt: endAt, endAt: startAt }
  }
  return { startAt, endAt }
}

function dateTimeQueryValue(value: unknown): string | undefined {
  const text = optionalQueryText(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isNaN(time) ? undefined : new Date(time).toISOString()
}

function isExactTraceId(value: string | undefined): boolean {
  return Boolean(value && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value))
}
