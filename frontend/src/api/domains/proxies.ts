import type { ProxyOptionItem, ProxyPoolDetail, ProxyPoolListResult, ProxyPoolMemberOptionSummary, ProxyProfileListResult, ProxyProfileMutationResult, ProxyProfileSummary, ProxyTestReport } from '@/types/domain'
import type { ProxyPoolCreatePayload, ProxyPoolPatchPayload, ProxyListParams, ProxyOptionParams, ProxyPoolListParams, ProxyPoolMemberOptionsParams } from '../contracts'
import { proxyOptionParams } from '../params'
import { http, unwrap } from '../http'

export const proxiesApi = {
  list: (params?: ProxyListParams) => unwrap<ProxyProfileListResult>(http.get('/proxies', { params })),
  options: (params?: ProxyOptionParams) => unwrap<ProxyOptionItem[]>(http.get('/proxies/options', {
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

// 代理池管理面（代理池设计 §8.1）：PATCH 必须带 expectedUpdatedAt；成员集合完整替换。
export const proxyPoolsApi = {
  list: (params?: ProxyPoolListParams) => unwrap<ProxyPoolListResult>(http.get('/proxy-pools', { params })),
  memberOptions: (params?: ProxyPoolMemberOptionsParams) => unwrap<ProxyPoolMemberOptionSummary[]>(http.get('/proxy-pools/member-options', {
    params: proxyPoolMemberOptionParams(params),
    paramsSerializer: {
      // selectedIds=a&selectedIds=b (bare repeated keys)
      indexes: null
    }
  })),
  detail: (id: string) => unwrap<ProxyPoolDetail>(http.get(`/proxy-pools/${id}`)),
  create: (payload: ProxyPoolCreatePayload) => unwrap<ProxyPoolDetail>(http.post('/proxy-pools', payload)),
  update: (id: string, payload: ProxyPoolPatchPayload) => unwrap<ProxyPoolDetail>(http.patch(`/proxy-pools/${id}`, payload)),
  delete: (id: string) => http.delete(`/proxy-pools/${id}`)
}

function proxyPoolMemberOptionParams(params?: ProxyPoolMemberOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.limit) output.limit = params.limit
  const selectedIds = [...new Set(params.selectedIds?.map((id) => id.trim()).filter(Boolean) ?? [])].sort()
  if (selectedIds.length) output.selectedIds = selectedIds
  return Object.keys(output).length ? output : undefined
}
