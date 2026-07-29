import type {
  AuditLogDetailSupplement,
  AuditLogHotSearchResult,
  AuditLogListResult,
  AuditLogPayloadDetail,
  OperationLogDetailSupplement,
  OperationLogListResult,
  PublicApiLogDetailSupplement,
  PublicApiLogListResult,
  RuntimeLogFacets,
  RuntimeLogDetailDelta,
  RuntimeLogGrepDetail,
  RuntimeLogGrepResult,
  RuntimeLogGrepRuntime,
  RuntimeLogSearchResult,
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
  detail: (id: string) => unwrap<AuditLogDetailSupplement>(http.get(`/audit-logs/${id}`, noTimeout)),
  payload: (id: string, payloadId: string) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, noTimeout))
}

export const runtimeLogsApi = {
  list: (params?: RuntimeLogListParams) => unwrap<RuntimeLogSearchResult>(http.get('/runtime-logs', { params })),
  facets: () => unwrap<RuntimeLogFacets>(http.get('/runtime-logs/facets')),
  grepOptions: () => unwrap<RuntimeLogGrepRuntime>(http.get('/runtime-logs/grep-options')),
  detail: (id: string) => unwrap<RuntimeLogDetailDelta>(http.get(`/runtime-logs/${id}`)),
  grep: (params?: RuntimeLogGrepParams) => unwrap<RuntimeLogGrepResult>(http.get('/runtime-logs/grep', { params, ...noTimeout })),
  grepDetail: (item: { id: string; fileName: string; lineNumber: number }) => unwrap<RuntimeLogGrepDetail>(http.get('/runtime-logs/grep-detail', {
    params: item,
    ...noTimeout
  }))
}

export const operationLogsApi = {
  list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/operation-logs', { params })),
  detail: (id: string) => unwrap<OperationLogDetailSupplement>(http.get(`/operation-logs/${id}`))
}

export const publicApiLogsApi = {
  list: (params?: PublicApiLogListParams) => unwrap<PublicApiLogListResult>(http.get('/public-api-logs', { params })),
  detail: (id: string) => unwrap<PublicApiLogDetailSupplement>(http.get(`/public-api-logs/${encodeURIComponent(id)}`))
}

export const myOperationLogsApi = {
  list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/my-operation-logs', { params: stripAdminOperationLogParams(params) })),
  detail: (id: string) => unwrap<OperationLogDetailSupplement>(http.get(`/my-operation-logs/${id}`))
}
