import type { AuditErrorGroupListOptions, AuditLogListOptions } from './audit-log-f3-types.js'
import type { AuditPayloadBlobStorageStatus } from './audit-log-f3-types.js'
export type { AuditPayloadBlobStorageStatus } from './audit-log-f3-types.js'
export interface AuditPayloadBlobWindow { bytes?: Buffer; offset: number; limit: number; totalBytes: number; nextOffset?: number; truncated: boolean; storageStatus: AuditPayloadBlobStorageStatus }
export function auditPayloadBodyDetail(bytes?: Buffer): { bodyText?: string; bodyBase64?: string } { if (!bytes) return {}; const text = bytes.toString('utf8'); return Buffer.from(text, 'utf8').equals(bytes) ? { bodyText: text } : { bodyBase64: bytes.toString('base64') } }

export const auditLogDefaultPageSize = 100
export const auditLogMaxPageSize = 100
export const errorGroupDefaultPageSize = 100
export const errorGroupMaxPageSize = 100
const nonPersistedSources = ['account_health_check', 'runtime_recovery_probe', 'cooldown_retest'] as const
export const persistedAuditTrafficSourceParams = (): string[] => [...nonPersistedSources]
export const persistedTrafficClause = (alias: string): string => `${alias}.traffic_source NOT IN (${nonPersistedSources.map(() => '?').join(', ')})`
export const listColumns = (alias: string): string => ['id', 'trace_id', 'session_id', 'session_client_type', 'traffic_source', 'system_account_id', 'api_key_id', 'group_id', 'account_id', 'method', 'path', 'model', 'upstream_model', 'model_mapping_applied', 'stream', 'audit_outcome', 'success', 'lifecycle_status', 'final_status_code', 'duration_ms', 'http_duration_ms', 'created_at'].map((column) => `${alias}.${column}`).join(', ')
export const errorGroupColumns = (alias: string): string => ['id', 'fingerprint', 'window_started_at', 'window_ended_at', 'system_account_id', 'api_key_id', 'group_id', 'account_id', 'provider_code', 'path', 'model', 'status_code', 'error_phase', 'error_code', 'error_type', 'request_fingerprint', 'error_fingerprint', 'count', 'first_event_id', 'last_event_id', 'sample_event_id', 'last_message', 'created_at', 'updated_at'].map((column) => `${alias}.${column}`).join(', ')
export function normalizePageSize(value: unknown, fallback: number, max: number): number { return typeof value === 'number' && Number.isInteger(value) ? Math.min(max, Math.max(1, value)) : fallback }
export function normalizePage(value: unknown, pageSize: number): number { const max = Math.max(1, Math.floor((1001 - 1) / Math.max(1, Math.trunc(pageSize)))); return typeof value === 'number' && Number.isInteger(value) ? Math.min(max, Math.max(1, value)) : 1 }
export function normalizeAuditLogPage(value: unknown, pageSize: number, sessionId?: string): number { return sessionId?.trim() ? (typeof value === 'number' && Number.isInteger(value) ? Math.max(1, value) : 1) : normalizePage(value, pageSize) }
export function pagedTotalUpperBound(page: number, pageSize: number, itemCount: number, hasMore: boolean): number { return hasMore ? page * pageSize + 1 : Math.max(0, (page - 1) * pageSize + itemCount) }
export function takePageRows<T>(rows: T[], pageSize: number): { rows: T[]; hasMore: boolean } { return { rows: rows.slice(0, pageSize), hasMore: rows.length > pageSize } }
export function textPrefixUpperBound(value: string): string { return value + '\uffff' }

export function auditLogFilters(options: AuditLogListOptions, mode: 'sqlite' | 'postgres'): { clause: string; params: Array<string | number> } {
  const clauses: string[] = [persistedTrafficClause('al')]; const params: Array<string | number> = persistedAuditTrafficSourceParams()
  prefix(clauses, params, 'al.trace_id', options.traceId, mode); exact(clauses, params, 'al.session_id', options.sessionId); exact(clauses, params, 'al.session_client_type', options.sessionClientType); path(clauses, params, 'al.path', options.path); exact(clauses, params, 'al.model', options.model); prefix(clauses, params, 'al.client_ip', options.clientIp, mode)
  if (options.outcome && options.outcome !== 'all') { clauses.push('al.audit_outcome = ?'); params.push(options.outcome) }
  if (Number.isInteger(options.statusCode) && options.statusCode! < 600 && options.statusCode! >= 100) { clauses.push('al.final_status_code = ?'); params.push(options.statusCode!) }
  if (options.trafficSource) { clauses.push('al.traffic_source = ?'); params.push(options.trafficSource) }
  if (options.startAt?.trim()) { clauses.push('al.created_at >= ?'); params.push(options.startAt.trim()) }
  if (options.endAt?.trim()) { clauses.push('al.created_at <= ?'); params.push(options.endAt.trim()) }
  for (const [column, value] of [['al.system_account_id', options.systemAccountId], ['al.api_key_id', options.apiKeyId], ['al.group_id', options.groupId], ['al.account_id', options.accountId], ['al.error_group_id', options.errorGroupId]] as const) if (value?.trim()) { clauses.push(`${column} = ?`); params.push(value.trim()) }
  return { clause: `WHERE ${clauses.join(' AND ')}`, params }
}
export function errorGroupFilters(options: AuditErrorGroupListOptions): { clause: string; params: Array<string | number> } {
  const clauses: string[] = []; const params: Array<string | number> = []; path(clauses, params, 'aeg.path', options.path); exact(clauses, params, 'aeg.model', options.model)
  if (Number.isInteger(options.statusCode) && options.statusCode! < 600 && options.statusCode! >= 100) { clauses.push('aeg.status_code = ?'); params.push(options.statusCode!) }
  for (const [column, value] of [['aeg.system_account_id', options.systemAccountId], ['aeg.api_key_id', options.apiKeyId], ['aeg.group_id', options.groupId], ['aeg.account_id', options.accountId]] as const) if (value?.trim()) { clauses.push(`${column} = ?`); params.push(value.trim()) }
  return { clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '', params }
}
function exact(clauses: string[], params: Array<string | number>, column: string, value?: string): void { if (value?.trim()) { clauses.push(`${column} = ?`); params.push(value.trim()) } }
function prefix(clauses: string[], params: Array<string | number>, column: string, value: string | undefined, mode: 'sqlite' | 'postgres'): void { if (!value?.trim()) return; const text = value.trim(); const expression = mode === 'postgres' ? `${column} COLLATE "C"` : column; clauses.push(`${expression} >= ? AND ${expression} < ?`); params.push(text, textPrefixUpperBound(text)) }
function path(clauses: string[], params: Array<string | number>, column: string, value?: string): void { const text = value?.trim(); if (!text) return; const normalized = text.replace(/^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+/i, '').split('?')[0]?.trim(); if (normalized) { clauses.push(`${column} = ?`); params.push(normalized) } }
