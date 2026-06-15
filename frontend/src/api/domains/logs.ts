import type {
  AuditLogDetail,
  AuditLogHotSearchResult,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogRuntime,
  OperationLogDetail,
  OperationLogListResult,
  PublicApiLogDetail,
  PublicApiLogListResult,
  RuntimeLogFacets,
  RuntimeLogGrepResult,
  RuntimeLogSearchResult,
  RuntimeLogSummary
} from '@/types/domain'
import type {
  AuditLogHotSearchParams,
  AuditLogListParams,
  AuditLogPayloadParams,
  OperationLogListParams,
  PublicApiLogListParams,
  RuntimeLogGrepParams,
  RuntimeLogListParams
} from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { stripAdminOperationLogParams } from '../params'

export const auditLogsApi = {
  list: (params?: AuditLogListParams) => unwrap<AuditLogListResult>(http.get('/audit-logs', { params, ...noTimeout })),
  searchHot: (params?: AuditLogHotSearchParams) => unwrap<AuditLogHotSearchResult>(http.get('/audit-logs/search-hot', { params, ...noTimeout })),
  runtime: () => unwrap<AuditLogRuntime>(http.get('/audit-logs/runtime', noTimeout)),
  detail: (id: string) => unwrap<AuditLogDetail>(http.get(`/audit-logs/${id}`, noTimeout)),
  payload: (id: string, payloadId: string, params?: AuditLogPayloadParams) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, { params, ...noTimeout }))
}

export const runtimeLogsApi = {
  list: (params?: RuntimeLogListParams) => unwrap<RuntimeLogSearchResult>(http.get('/runtime-logs', { params })),
  facets: () => unwrap<RuntimeLogFacets>(http.get('/runtime-logs/facets')),
  detail: (id: string) => unwrap<RuntimeLogSummary>(http.get(`/runtime-logs/${id}`)),
  grep: (params?: RuntimeLogGrepParams) => unwrap<RuntimeLogGrepResult>(http.get('/runtime-logs/grep', { params, ...noTimeout }))
}

export const operationLogsApi = {
  list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/operation-logs', { params })),
  detail: (id: string) => unwrap<OperationLogDetail>(http.get(`/operation-logs/${id}`))
}

export const publicApiLogsApi = {
  list: (params?: PublicApiLogListParams) => unwrap<PublicApiLogListResult>(http.get('/public-api-logs', { params })),
  detail: (id: string) => unwrap<PublicApiLogDetail>(http.get(`/public-api-logs/${encodeURIComponent(id)}`))
}

export const myOperationLogsApi = {
  list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/my-operation-logs', { params: stripAdminOperationLogParams(params) })),
  detail: (id: string) => unwrap<OperationLogDetail>(http.get(`/my-operation-logs/${id}`))
}
