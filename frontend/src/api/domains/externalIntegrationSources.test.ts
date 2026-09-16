import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { externalIntegrationSourcesApi } from './externalIntegrationSources'

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

describe('externalIntegrationSourcesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await externalIntegrationSourcesApi.scopes()
    await externalIntegrationSourcesApi.apiDocs()
    await externalIntegrationSourcesApi.list()
    await externalIntegrationSourcesApi.detail('src-1')
    await externalIntegrationSourcesApi.create({ name: '来源' } as never)
    await externalIntegrationSourcesApi.update('src-1', { name: '改名' } as never)
    await externalIntegrationSourcesApi.delete('src-1', 'ts-1')
    await externalIntegrationSourcesApi.resetBuiltInTestToken()
    await externalIntegrationSourcesApi.createToken('src-1', { name: '令牌' } as never)
    await externalIntegrationSourcesApi.tokenSecret('src-1', 'tok-1')
    await externalIntegrationSourcesApi.updateToken('src-1', 'tok-1', { name: '改名' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/external-integration-sources/scopes'],
      ['GET', '/external-integration-sources/api-docs'],
      ['GET', '/external-integration-sources'],
      ['GET', '/external-integration-sources/src-1'],
      ['POST', '/external-integration-sources'],
      ['PATCH', '/external-integration-sources/src-1'],
      ['DELETE', '/external-integration-sources/src-1'],
      ['POST', '/external-integration-sources/built-in-test-token/reset'],
      ['POST', '/external-integration-sources/src-1/tokens'],
      ['GET', '/external-integration-sources/src-1/tokens/tok-1/secret'],
      ['PATCH', '/external-integration-sources/src-1/tokens/tok-1']
    ])
  })

  it('id 与 tokenId 路径段分别 encodeURIComponent 编码', async () => {
    await externalIntegrationSourcesApi.tokenSecret('src/1', 'tok/2?x')
    expect(requests[0].url).toBe('/external-integration-sources/src%2F1/tokens/tok%2F2%3Fx/secret')
  })

  it('delete 以请求体携带 expectedUpdatedAt', async () => {
    await externalIntegrationSourcesApi.delete('src-1', 'ts-1')
    expect(payloadOf(requests[0])).toEqual({ expectedUpdatedAt: 'ts-1' })
  })

  it('delete 成功时不返回业务数据（void）', async () => {
    await expect(externalIntegrationSourcesApi.delete('src-1', 'ts-1')).resolves.toBeUndefined()
  })

  it('list 透传查询参数', async () => {
    await externalIntegrationSourcesApi.list({ page: 1, status: 'active' } as never)
    expect(requests[0].params).toEqual({ page: 1, status: 'active' })
  })
})
