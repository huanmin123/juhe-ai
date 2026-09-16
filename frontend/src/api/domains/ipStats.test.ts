import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { ipStatsApi } from './ipStats'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
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
      data: config.data
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

function requestShapes(): Array<[string, string]> {
  return requests.map((request) => [request.method, request.url])
}

function payloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('ipStatsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await ipStatsApi.list()
    await ipStatsApi.detail('hash-1')
    await ipStatsApi.blacklist('hash-1', { reason: '攻击行为' } as never)
    await ipStatsApi.allowlist('hash-1', { reason: '误报' })
    await ipStatsApi.unblock('hash-1', { reason: '解封' })
    await ipStatsApi.unallowlist('hash-1', { reason: '移除' })
    expect(requestShapes()).toEqual([
      ['GET', '/ip-stats'],
      ['GET', '/ip-stats/hash-1/detail'],
      ['POST', '/ip-stats/hash-1/blacklist'],
      ['POST', '/ip-stats/hash-1/allowlist'],
      ['POST', '/ip-stats/hash-1/unblock'],
      ['POST', '/ip-stats/hash-1/unallowlist']
    ])
  })

  it('ipHash 路径段进行 encodeURIComponent 编码', async () => {
    await ipStatsApi.detail('a/b?c')
    expect(requests[0].url).toBe('/ip-stats/a%2Fb%3Fc/detail')
  })

  it('策略变更方法携带 reason payload', async () => {
    await ipStatsApi.blacklist('hash-1', { reason: '攻击行为' } as never)
    expect(payloadOf(requests[0])).toEqual({ reason: '攻击行为' })
    await ipStatsApi.unblock('hash-1', { reason: '解封' })
    expect(payloadOf(requests[1])).toEqual({ reason: '解封' })
  })

  it('list 透传查询参数', async () => {
    await ipStatsApi.list({ page: 1, status: 'blocked' } as never)
    expect(requests[0].params).toEqual({ page: 1, status: 'blocked' })
  })
})
