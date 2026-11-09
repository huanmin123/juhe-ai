import type {
  AuditLogDetailSupplement,
  AuditLogHotSearchResult,
  AuditLogListResult,
  AuditLogPayloadDetail,
  OperationLogDetailSupplement,
  OperationLogListResult,
  PublicApiLogDetailSupplement,
  PublicApiLogListResult,
} from '@/types/domain'
import type {
  AuditLogHotSearchParams,
  AuditLogListParams,
  OperationLogListParams,
  PublicApiLogListParams
} from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { stripAdminOperationLogParams } from '../params'

export const auditLogsApi = {
  list: (params?: AuditLogListParams) => unwrap<AuditLogListResult>(http.get('/audit-logs', { params, ...noTimeout })),
  searchHot: (params?: AuditLogHotSearchParams) => unwrap<AuditLogHotSearchResult>(http.get('/audit-logs/search-hot', { params, ...noTimeout })),
  detail: (id: string) => unwrap<AuditLogDetailSupplement>(http.get(`/audit-logs/${id}`, noTimeout)),
  payload: (id: string, payloadId: string) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, noTimeout))
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
