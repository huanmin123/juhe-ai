import type { UsageRecordListResult, UsageRecordSummary } from '@/types/domain'
import type { ListParams, UsageRecordListParams } from '../contracts'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

export const usageRecordsApi = {
  list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/usage-records', { params })),
  detail: (id: string, params?: ListParams) => unwrap<UsageRecordSummary>(http.get(`/usage-records/${id}`, { params }))
}

export const myUsageRecordsApi = {
  list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/my-usage-records', { params: stripSystemAccountParam(params) })),
  detail: (id: string) => unwrap<UsageRecordSummary>(http.get(`/my-usage-records/${id}`))
}
