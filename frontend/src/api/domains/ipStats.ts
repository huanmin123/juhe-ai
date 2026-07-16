import type { ClientIpPolicySummary, ClientIpStatsDetailResult, ClientIpStatsListResult } from '@/types/domain'
import type { ClientIpPolicyPayload, ClientIpStatsDetailParams, ClientIpStatsListParams } from '../contracts'
import { http, unwrap } from '../http'

const clientIpStatsPath = (
  ipHash: string,
  action: 'detail' | 'blacklist' | 'allowlist' | 'unblock' | 'unallowlist'
): string => `/ip-stats/${encodeURIComponent(ipHash)}/${action}`

export const ipStatsApi = {
  list: (params?: ClientIpStatsListParams) => unwrap<ClientIpStatsListResult>(http.get('/ip-stats', { params })),
  detail: (ipHash: string, params?: ClientIpStatsDetailParams) => unwrap<ClientIpStatsDetailResult>(http.get(clientIpStatsPath(ipHash, 'detail'), { params })),
  blacklist: (ipHash: string, payload: ClientIpPolicyPayload) => unwrap<ClientIpPolicySummary>(http.post(clientIpStatsPath(ipHash, 'blacklist'), payload)),
  allowlist: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<ClientIpPolicySummary>(http.post(clientIpStatsPath(ipHash, 'allowlist'), payload)),
  unblock: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<{ disabledCount: number }>(http.post(clientIpStatsPath(ipHash, 'unblock'), payload)),
  unallowlist: (ipHash: string, payload: Pick<ClientIpPolicyPayload, 'reason'>) => unwrap<{ disabledCount: number }>(http.post(clientIpStatsPath(ipHash, 'unallowlist'), payload))
}
