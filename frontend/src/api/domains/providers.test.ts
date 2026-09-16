import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { providersApi } from './providers'

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

describe('providersApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await providersApi.listItems()
    await providersApi.list()
    await providersApi.detail('openai')
    await providersApi.options()
    await providersApi.definitions()
    await providersApi.modelOptions()
    await providersApi.modelCapabilities('openai', 'gpt-4o')
    await providersApi.models('openai')
    await providersApi.setDefaultHealthCheckModel('openai', 'gpt-4o-mini')
    await providersApi.createModel('openai', { model: 'gpt-4o' } as never)
    await providersApi.updateModel('openai', 'm-1', { pricing: {} } as never)
    await providersApi.deleteModel('openai', 'm-1')
    expect(requestShapes()).toEqual([
      ['GET', '/providers/list'],
      ['GET', '/providers'],
      ['GET', '/providers/openai'],
      ['GET', '/providers/options'],
      ['GET', '/providers/definitions'],
      ['GET', '/providers/models/options'],
      ['GET', '/providers/openai/models/gpt-4o/capabilities'],
      ['GET', '/providers/openai/models'],
      ['PUT', '/providers/openai/default-health-check-model'],
      ['POST', '/providers/openai/models'],
      ['PATCH', '/providers/openai/models/m-1'],
      ['DELETE', '/providers/openai/models/m-1']
    ])
  })

  it('modelId 路径段进行 encodeURIComponent 编码，providerCode 不编码', async () => {
    await providersApi.modelCapabilities('openai', 'm/o?del')
    expect(requests[0].url).toBe('/providers/openai/models/m%2Fo%3Fdel/capabilities')
  })

  it('setDefaultHealthCheckModel 使用 PUT 并携带 model', async () => {
    await providersApi.setDefaultHealthCheckModel('openai', 'gpt-4o-mini')
    expect(payloadOf(requests[0])).toEqual({ model: 'gpt-4o-mini' })
  })

  it('modelOptions 直接透传选项参数（不做归一化）', async () => {
    await providersApi.modelOptions({ keyword: ' gpt ', limit: 10, selectedIds: ['a', 'b'] })
    expect(requests[0].params).toEqual({ keyword: ' gpt ', limit: 10, selectedIds: ['a', 'b'] })
  })

  it('models 透传查询参数', async () => {
    await providersApi.models('openai', { page: 1, keyword: 'gpt' } as never)
    expect(requests[0].params).toEqual({ page: 1, keyword: 'gpt' })
  })

  it('detail 返回解包后的供应商定义', async () => {
    installCaptureAdapter({ code: 'openai', name: 'OpenAI' })
    await expect(providersApi.detail('openai')).resolves.toEqual({ code: 'openai', name: 'OpenAI' })
  })
})
