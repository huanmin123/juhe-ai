import type { ProxyProfileListResult, ProxyProfileMutationResult, ProxyProfileOptionSummary, ProxyProfileSummary, ProxyTestReport } from '@/types/domain'
import type { ProxyListParams, ProxyOptionParams } from '../contracts'
import { proxyOptionParams } from '../params'
import { http, unwrap } from '../http'

export const proxiesApi = {
  list: (params?: ProxyListParams) => unwrap<ProxyProfileListResult>(http.get('/proxies', { params })),
  options: (params?: ProxyOptionParams) => unwrap<ProxyProfileOptionSummary[]>(http.get('/proxies/options', {
    params: proxyOptionParams(params),
    paramsSerializer: {
      // selectedIds=a&selectedIds=b (bare repeated keys)
      indexes: null
    }
  })),
  create: (payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.post('/proxies', payload)),
  update: (id: string, payload: Record<string, unknown>) => unwrap<ProxyProfileMutationResult>(http.patch(`/proxies/${id}`, payload)),
  test: (id: string) => unwrap<ProxyTestReport>(http.post(`/proxies/${id}/test`, {}, { timeout: 120000 })),
  delete: (id: string) => http.delete(`/proxies/${id}`)
}
