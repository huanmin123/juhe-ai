import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { apiKeysApi, myApiKeysApi } from './apiKeys'

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

const rawApiKeyId = 'key/a?b#c% d'
const encodedApiKeyId = 'key%2Fa%3Fb%23c%25%20d'

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('apiKeysApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await apiKeysApi.list()
    await apiKeysApi.create({ name: '管理端 Key' })
    await apiKeysApi.update('key-1', { expectedRevision: 'rev-1', name: '改名' })
    await apiKeysApi.secret('key-1')
    await apiKeysApi.refreshKey('key-1')
    await apiKeysApi.delete('key-1')
    expect(requestShapes()).toEqual([
      ['GET', '/api-keys'],
      ['POST', '/api-keys'],
      ['PATCH', '/api-keys/key-1'],
      ['GET', '/api-keys/key-1/secret'],
      ['POST', '/api-keys/key-1/refresh-key'],
      ['DELETE', '/api-keys/key-1']
    ])
  })

  it('含特殊字符的动态路径段必须逐段 encodeURIComponent', async () => {
    await apiKeysApi.update(rawApiKeyId, { expectedRevision: 'rev-1' })
    await apiKeysApi.secret(rawApiKeyId)
    await apiKeysApi.refreshKey(rawApiKeyId)
    await apiKeysApi.delete(rawApiKeyId)
    expect(requestShapes()).toEqual([
      ['PATCH', `/api-keys/${encodedApiKeyId}`],
      ['GET', `/api-keys/${encodedApiKeyId}/secret`],
      ['POST', `/api-keys/${encodedApiKeyId}/refresh-key`],
      ['DELETE', `/api-keys/${encodedApiKeyId}`]
    ])
  })

  it('update 携带完整 payload', async () => {
    await apiKeysApi.update('key-1', { expectedRevision: 'rev-1', name: '新名称', status: 'disabled' })
    expect(payloadOf(requests[0])).toEqual({ expectedRevision: 'rev-1', name: '新名称', status: 'disabled' })
  })

  it('refreshKey 发送空对象 body', async () => {
    await apiKeysApi.refreshKey('key-1')
    expect(payloadOf(requests[0])).toEqual({})
  })

  it('list 返回解包后的数据', async () => {
    installCaptureAdapter({ items: [], total: 0 })
    await expect(apiKeysApi.list()).resolves.toEqual({ items: [], total: 0 })
  })
})

describe('myApiKeysApi 请求形状', () => {
  it('各方法命中 /my-api-keys 前缀路径', async () => {
    await myApiKeysApi.list()
    await myApiKeysApi.create({ name: '个人 Key' })
    await myApiKeysApi.update(rawApiKeyId, { expectedRevision: 'rev-1' })
    await myApiKeysApi.secret(rawApiKeyId)
    await myApiKeysApi.refreshKey(rawApiKeyId)
    await myApiKeysApi.delete(rawApiKeyId)
    expect(requestShapes()).toEqual([
      ['GET', '/my-api-keys'],
      ['POST', '/my-api-keys'],
      ['PATCH', `/my-api-keys/${encodedApiKeyId}`],
      ['GET', `/my-api-keys/${encodedApiKeyId}/secret`],
      ['POST', `/my-api-keys/${encodedApiKeyId}/refresh-key`],
      ['DELETE', `/my-api-keys/${encodedApiKeyId}`]
    ])
  })

  it('个人视角 list 剥离 systemAccountId 参数', async () => {
    await myApiKeysApi.list({ systemAccountId: 'sa-1', page: 2 } as never)
    expect(requests[0].params).toEqual({ page: 2 })
  })
})
