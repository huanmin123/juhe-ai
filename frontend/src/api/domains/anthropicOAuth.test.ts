import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { anthropicOAuthApi, myAnthropicOAuthApi } from './anthropicOAuth'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  timeout?: unknown
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
      timeout: config.timeout
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

const rotationPayload = { refreshToken: 'rt-1', expectedRevision: 'rev-1' } as never

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('anthropicOAuthApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await anthropicOAuthApi.authUrl({ redirectUri: 'https://x' })
    await anthropicOAuthApi.createFromCode({ code: 'c-1' })
    await anthropicOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await anthropicOAuthApi.refreshToken('a-1', rotationPayload)
    await anthropicOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await anthropicOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/anthropic-oauth/auth-url'],
      ['POST', '/anthropic-oauth/create-from-code'],
      ['POST', '/anthropic-oauth/create-from-refresh-token'],
      ['POST', '/anthropic-oauth/accounts/a-1/refresh-token'],
      ['POST', '/anthropic-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/anthropic-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
  })

  it('refreshToken 使用 130s 长超时，其余方法用默认超时', async () => {
    await anthropicOAuthApi.refreshToken('a-1', rotationPayload)
    expect(requests[0].timeout).toBe(130000)
    await anthropicOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    expect(requests[1].timeout).toBe(15000)
  })

  it('createFromCode 透传 params 并携带 payload', async () => {
    await anthropicOAuthApi.createFromCode({ code: 'c-1' }, { systemAccountId: 'sa-1' })
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1' })
    expect(payloadOf(requests[0])).toEqual({ code: 'c-1' })
  })
})

describe('myAnthropicOAuthApi 请求形状', () => {
  it('各方法命中 /my-anthropic-oauth 前缀路径', async () => {
    await myAnthropicOAuthApi.authUrl({})
    await myAnthropicOAuthApi.createFromCode({ code: 'c-1' })
    await myAnthropicOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await myAnthropicOAuthApi.refreshToken('a-1', rotationPayload)
    await myAnthropicOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await myAnthropicOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/my-anthropic-oauth/auth-url'],
      ['POST', '/my-anthropic-oauth/create-from-code'],
      ['POST', '/my-anthropic-oauth/create-from-refresh-token'],
      ['POST', '/my-anthropic-oauth/accounts/a-1/refresh-token'],
      ['POST', '/my-anthropic-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/my-anthropic-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
    expect(requests[3].timeout).toBe(130000)
  })
})
