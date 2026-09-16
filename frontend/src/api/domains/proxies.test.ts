import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { proxiesApi } from './proxies'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  timeout?: unknown
  paramsSerializer?: unknown
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
      timeout: config.timeout,
      paramsSerializer: config.paramsSerializer
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

describe('proxiesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await proxiesApi.list()
    await proxiesApi.options()
    await proxiesApi.create({ name: '代理' })
    await proxiesApi.update('p-1', { name: '改名' })
    await proxiesApi.test('p-1')
    await proxiesApi.delete('p-1')
    expect(requestShapes()).toEqual([
      ['GET', '/proxies'],
      ['GET', '/proxies/options'],
      ['POST', '/proxies'],
      ['PATCH', '/proxies/p-1'],
      ['POST', '/proxies/p-1/test'],
      ['DELETE', '/proxies/p-1']
    ])
  })

  it('options 归一化参数并使用裸重复键序列化 selectedIds', async () => {
    await proxiesApi.options({ keyword: ' 代理 ', limit: 5, selectedIds: [' b ', 'a', 'b'] })
    expect(requests[0].params).toEqual({ keyword: '代理', limit: 5, selectedIds: ['a', 'b'] })
    expect(requests[0].paramsSerializer).toEqual({ indexes: null })
  })

  it('test 使用 120s 长超时', async () => {
    await proxiesApi.test('p-1')
    expect(requests[0].timeout).toBe(120000)
  })

  it('list 透传查询参数', async () => {
    await proxiesApi.list({ page: 1, keyword: 'k' })
    expect(requests[0].params).toEqual({ page: 1, keyword: 'k' })
  })
})
