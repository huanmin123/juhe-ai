import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { oauthApplicationsApi } from './oauthApplications'

interface CapturedRequest {
  method: string
  url: string
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

describe('oauthApplicationsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await oauthApplicationsApi.listClients()
    await oauthApplicationsApi.createClient({ name: '客户端' } as never)
    await oauthApplicationsApi.updateClientStatus('client-1', 'disabled')
    await oauthApplicationsApi.reissueClientSecret('client-1')
    await oauthApplicationsApi.integrationPackage('client-1')
    await oauthApplicationsApi.integrationInfo()
    expect(requestShapes()).toEqual([
      ['GET', '/oauth/clients'],
      ['POST', '/oauth/clients'],
      ['PATCH', '/oauth/clients/client-1'],
      ['POST', '/oauth/clients/client-1/secret/reissue'],
      ['GET', '/oauth/clients/client-1/integration-package'],
      ['GET', '/oauth/integration-info']
    ])
  })

  it('clientId 路径段进行 encodeURIComponent 编码', async () => {
    await oauthApplicationsApi.updateClientStatus('id/1?x', 'disabled')
    expect(requests[0].url).toBe('/oauth/clients/id%2F1%3Fx')
  })

  it('updateClientStatus 携带 status payload', async () => {
    await oauthApplicationsApi.updateClientStatus('client-1', 'disabled')
    expect(payloadOf(requests[0])).toEqual({ status: 'disabled' })
  })
})
