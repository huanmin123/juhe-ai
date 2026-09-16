import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myRouteStrategiesApi, routeStrategiesApi } from './routeStrategies'

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

function payloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('routeStrategiesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await routeStrategiesApi.list()
    await routeStrategiesApi.options()
    await routeStrategiesApi.detail('rs-1')
    await routeStrategiesApi.speedFirstRuntime('rs-1')
    await routeStrategiesApi.editBasicDetail('rs-1')
    await routeStrategiesApi.create({ name: '策略' })
    await routeStrategiesApi.update('rs-1', { expectedUpdatedAt: 'ts', name: '改名' })
    await routeStrategiesApi.delete('rs-1')
    expect(requestShapes()).toEqual([
      ['GET', '/route-strategies'],
      ['GET', '/route-strategies/options'],
      ['GET', '/route-strategies/rs-1'],
      ['GET', '/route-strategies/rs-1/speed-first-runtime'],
      ['GET', '/route-strategies/rs-1/edit-basic'],
      ['POST', '/route-strategies'],
      ['PATCH', '/route-strategies/rs-1'],
      ['DELETE', '/route-strategies/rs-1']
    ])
  })

  it('list 透传列表过滤参数', async () => {
    await routeStrategiesApi.list({ page: 1, keyword: 'k', status: 'active', mode: 'normal' })
    expect(requests[0].params).toEqual({ page: 1, keyword: 'k', status: 'active', mode: 'normal' })
  })

  it('update 携带乐观锁与变更 payload', async () => {
    await routeStrategiesApi.update('rs-1', { expectedUpdatedAt: 'ts-1', status: 'disabled' })
    expect(payloadOf(requests[0])).toEqual({ expectedUpdatedAt: 'ts-1', status: 'disabled' })
  })

  it('speedFirstRuntime 透传 signal', async () => {
    const controller = new AbortController()
    await routeStrategiesApi.speedFirstRuntime('rs-1', undefined, { signal: controller.signal })
    expect(requests[0].signal).toBe(controller.signal)
    await myRouteStrategiesApi.speedFirstRuntime('rs-1', { signal: controller.signal })
    expect(requests[1].url).toBe('/my-route-strategies/rs-1/speed-first-runtime')
    expect(requests[1].signal).toBe(controller.signal)
  })
})

describe('myRouteStrategiesApi 请求形状', () => {
  it('各方法命中 /my-route-strategies 前缀路径', async () => {
    await myRouteStrategiesApi.list()
    await myRouteStrategiesApi.options()
    await myRouteStrategiesApi.detail('rs-1')
    await myRouteStrategiesApi.speedFirstRuntime('rs-1')
    await myRouteStrategiesApi.editBasicDetail('rs-1')
    await myRouteStrategiesApi.create({ name: 'x' })
    await myRouteStrategiesApi.update('rs-1', { expectedUpdatedAt: 'ts' })
    await myRouteStrategiesApi.delete('rs-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-route-strategies'],
      ['GET', '/my-route-strategies/options'],
      ['GET', '/my-route-strategies/rs-1'],
      ['GET', '/my-route-strategies/rs-1/speed-first-runtime'],
      ['GET', '/my-route-strategies/rs-1/edit-basic'],
      ['POST', '/my-route-strategies'],
      ['PATCH', '/my-route-strategies/rs-1'],
      ['DELETE', '/my-route-strategies/rs-1']
    ])
  })

  it('个人视角 list/options 剥离 systemAccountId', async () => {
    await myRouteStrategiesApi.list({ systemAccountId: 'sa-1', page: 1 } as never)
    expect(requests[0].params).toEqual({ page: 1 })
    await myRouteStrategiesApi.options({ systemAccountId: 'sa-1', keyword: 'k' } as never)
    expect(requests[1].params).toEqual({ keyword: 'k' })
  })
})
