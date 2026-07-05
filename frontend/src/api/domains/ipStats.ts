import type { ClientIpPolicySummary, ClientIpStatsDetailResult, ClientIpStatsListResult } from '@/types/domain'
import type { ClientIpPolicyPayload, ClientIpStatsDetailParams, ClientIpStatsListParams } from '../contracts'
import { http, unwrap } from '../http'

export const ipStatsApi = {
  list: (params?: ClientIpStatsListParams) => unwrap<ClientIpStatsListResult>(http.get('/ip-stats', { params })),
  detail: (ipHash: string, params?: ClientIpStatsDetailParams) => unwrap<ClientIpStatsDetailResult>(http.get(`/ip-stats/${ipHash}/detail`, { params })),
  blacklist: (ipHash: string, payload: ClientIpPolicyPayload) => unwrap<ClientIpPolicySummary>(http.post(`/ip-stats/${ipHash}/blacklist`, payload)),
  allowlist: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<ClientIpPolicySummary>(http.post(`/ip-stats/${ipHash}/allowlist`, payload)),
  unblock: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<{ disabledCount: number }>(http.post(`/ip-stats/${ipHash}/unblock`, payload)),
  unallowlist: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<{ disabledCount: number }>(http.post(`/ip-stats/${ipHash}/unallowlist`, payload))
}
