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
  RuntimeLogGrepRuntime,
  RuntimeLogRuntime,
  RuntimeLogSearchResult,
  RuntimeLogSummary
} from '@/types/domain'
import type {
  AuditLogHotSearchParams,
  AuditLogListParams,
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
  payload: (id: string, payloadId: string) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, noTimeout))
}

export const runtimeLogsApi = {
  list: (params?: RuntimeLogListParams) => unwrap<RuntimeLogSearchResult>(http.get('/runtime-logs', { params })),
  facets: () => unwrap<RuntimeLogFacets>(http.get('/runtime-logs/facets')),
  grepOptions: () => unwrap<RuntimeLogGrepRuntime>(http.get('/runtime-logs/grep-options')),
  runtime: () => unwrap<RuntimeLogRuntime>(http.get('/runtime-logs/runtime')),
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
