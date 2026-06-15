import type { ProxyProfileListResult, ProxyProfileOptionSummary, ProxyProfileSummary, ProxyTestReport } from '@/types/domain'
import type { ProxyListParams, ProxyOptionParams } from '../contracts'
import { http, unwrap } from '../http'

export const proxiesApi = {
  list: (params?: ProxyListParams) => unwrap<ProxyProfileListResult>(http.get('/proxies', { params })),
  options: (params?: ProxyOptionParams) => unwrap<ProxyProfileOptionSummary[]>(http.get('/proxies/options', { params })),
  create: (payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.post('/proxies', payload)),
  update: (id: string, payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.patch(`/proxies/${id}`, payload)),
  test: (id: string) => unwrap<ProxyTestReport>(http.post(`/proxies/${id}/test`, {}, { timeout: 120000 })),
  delete: (id: string) => http.delete(`/proxies/${id}`)
}
