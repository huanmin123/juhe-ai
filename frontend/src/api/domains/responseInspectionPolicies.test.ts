import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { responseInspectionPoliciesApi } from './responseInspectionPolicies'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  signal?: unknown
}

const originalAdapter = http.defaults.adapter
let requests: CapturedRequest[] = []
let responseData: unknown = {}

/** 替换 axios adapter：捕获请求形状并返回可配置的 `{ data: { data } }` 响应。 */
function installCaptureAdapter(data: unknown = {}): void {
  responseData = data
  requests = []
  http.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    requests.push({
      method: String(config.method ?? '').toUpperCase(),
      url: String(config.url ?? ''),
      params: config.params,
      data: config.data,
      signal: config.signal
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

function requestShapes(): Array<[string, string]> {
  return requests.map((request) => [request.method, request.url])
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('responseInspectionPoliciesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await responseInspectionPoliciesApi.list()
    await responseInspectionPoliciesApi.detail('rip-1')
    await responseInspectionPoliciesApi.providerOptions({ providerCode: 'openai' } as never)
    await responseInspectionPoliciesApi.create({ name: '策略' } as never)
    await responseInspectionPoliciesApi.update('rip-1', { name: '改名' } as never)
    await responseInspectionPoliciesApi.delete('rip-1')
    expect(requestShapes()).toEqual([
      ['GET', '/response-inspection-policies'],
      ['GET', '/response-inspection-policies/rip-1'],
      ['GET', '/response-inspection-policies/provider-options'],
      ['POST', '/response-inspection-policies'],
      ['PATCH', '/response-inspection-policies/rip-1'],
      ['DELETE', '/response-inspection-policies/rip-1']
    ])
  })

  it('id 路径段进行 encodeURIComponent 编码', async () => {
    await responseInspectionPoliciesApi.detail('id/1?x')
    expect(requests[0].url).toBe('/response-inspection-policies/id%2F1%3Fx')
  })

  it('list/detail 直接以 RequestControlOptions 作为请求配置（透传 signal）', async () => {
    const controller = new AbortController()
    await responseInspectionPoliciesApi.list({ signal: controller.signal })
    expect(requests[0].signal).toBe(controller.signal)
    await responseInspectionPoliciesApi.detail('rip-1', { signal: controller.signal })
    expect(requests[1].signal).toBe(controller.signal)
  })

  it('providerOptions 携带参数与 signal', async () => {
    const controller = new AbortController()
    await responseInspectionPoliciesApi.providerOptions({ providerCode: 'openai' } as never, { signal: controller.signal })
    expect(requests[0].params).toEqual({ providerCode: 'openai' })
    expect(requests[0].signal).toBe(controller.signal)
  })
})
