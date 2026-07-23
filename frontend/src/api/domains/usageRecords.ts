import type { UsageRecordListResult } from '@/types/domain'
import type { UsageRecordListParams } from '../contracts'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

export const usageRecordsApi = {
  list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/usage-records', { params }))
}

export const myUsageRecordsApi = {
  list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/my-usage-records', { params: stripSystemAccountParam(params) }))
}
