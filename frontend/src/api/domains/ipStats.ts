import type { ClientIpPolicySummary, ClientIpStatsListResult } from '@/types/domain'
import type { ClientIpPolicyPayload, ClientIpStatsListParams } from '../contracts'
import { http, unwrap } from '../http'

export const ipStatsApi = {
  list: (params?: ClientIpStatsListParams) => unwrap<ClientIpStatsListResult>(http.get('/ip-stats', { params })),
  blacklist: (ipHash: string, payload: ClientIpPolicyPayload) => unwrap<ClientIpPolicySummary>(http.post(`/ip-stats/${ipHash}/blacklist`, payload)),
  unblock: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<{ disabledCount: number }>(http.post(`/ip-stats/${ipHash}/unblock`, payload))
}
