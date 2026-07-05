import type { AuditErrorGroupListOptions, AuditLogListOptions } from './audit-log-types.js'
import { runtimeConfig } from '../config/runtime.js'
import { textPrefixUpperBound } from './query-utils.js'

export type AuditLogFilterValue = string | number

export const auditLogDefaultPageSize = 100
export const auditLogMaxPageSize = 100
export const errorGroupDefaultPageSize = 100
export const errorGroupMaxPageSize = 100
export const auditLogMaxListWindowRows = 1001

export function buildAuditLogFilters(options: AuditLogListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []

  pushPrefixFilter(clauses, params, 'al.trace_id', options.traceId)
  pushPathExactFilter(clauses, params, 'al.path', options.path)
  pushExactFilter(clauses, params, 'al.model', options.model)
  pushPrefixFilter(clauses, params, 'al.client_ip', options.clientIp)
  if (options.outcome && options.outcome !== 'all') {
    clauses.push('al.audit_outcome = ?')
    params.push(options.outcome)
  }
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('al.final_status_code = ?')
    params.push(options.statusCode)
  }
  if (options.trafficSource) {
    clauses.push('al.traffic_source = ?')
    params.push(options.trafficSource)
  }
  const startAt = options.startAt?.trim()
  if (startAt) {
    clauses.push('al.created_at >= ?')
    params.push(startAt)
  }
  const endAt = options.endAt?.trim()
  if (endAt) {
    clauses.push('al.created_at <= ?')
    params.push(endAt)
  }
  for (const [column, value] of [
    ['al.system_account_id', options.systemAccountId],
    ['al.api_key_id', options.apiKeyId],
    ['al.group_id', options.groupId],
    ['al.account_id', options.accountId],
    ['al.error_group_id', options.errorGroupId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

export function buildAuditErrorGroupFilters(options: AuditErrorGroupListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []

  pushPathExactFilter(clauses, params, 'aeg.path', options.path)
  pushExactFilter(clauses, params, 'aeg.model', options.model)
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('aeg.status_code = ?')
    params.push(options.statusCode)
  }
  for (const [column, value] of [
    ['aeg.system_account_id', options.systemAccountId],
    ['aeg.api_key_id', options.apiKeyId],
    ['aeg.group_id', options.groupId],
    ['aeg.account_id', options.accountId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

export function auditErrorGroupListSelectColumns(alias: string): string {
  return [
    'id',
    'fingerprint',
    'window_started_at',
    'window_ended_at',
    'system_account_id',
    'api_key_id',
    'group_id',
    'account_id',
    'provider_code',
    'path',
    'model',
    'status_code',
    'error_phase',
    'error_code',
    'error_type',
    'request_fingerprint',
    'error_fingerprint',
    'count',
    'first_event_id',
    'last_event_id',
    'sample_event_id',
    'last_message',
    'created_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

export function normalizePage(value: unknown, pageSize: number): number {
  const maxPage = Math.max(1, Math.floor((auditLogMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(maxPage, Math.max(1, value))
    : 1
}

export function normalizePageSize(value: unknown, fallback: number, max: number): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(max, Math.max(1, value))
    : fallback
}

function pushExactFilter(clauses: string[], params: AuditLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPathExactFilter(clauses: string[], params: AuditLogFilterValue[], column: string, value?: string): void {
  const text = normalizePathFilter(value)
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPrefixFilter(clauses: string[], params: AuditLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  const columnExpression = runtimeConfig.databaseDriver === 'postgres' ? `${column} COLLATE "C"` : column
  clauses.push(`${columnExpression} >= ? AND ${columnExpression} < ?`)
  params.push(text, textPrefixUpperBound(text))
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function normalizePathFilter(value?: string): string | undefined {
  const text = value?.trim()
  if (!text) return undefined
  const withoutMethod = text.replace(/^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+/i, '')
  const path = withoutMethod.split('?')[0]?.trim()
  return path || undefined
}
