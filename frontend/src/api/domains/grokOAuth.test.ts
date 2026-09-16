import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { grokOAuthApi, myGrokOAuthApi } from './grokOAuth'

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

const rotationPayload = { refreshToken: 'rt-1', expectedRevision: 'rev-1' } as never

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('grokOAuthApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await grokOAuthApi.authUrl({})
    await grokOAuthApi.createFromCode({ code: 'c-1' })
    await grokOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await grokOAuthApi.ssoToOAuth({ cookies: [] })
    await grokOAuthApi.refreshToken('a-1', rotationPayload)
    await grokOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await grokOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/grok-oauth/auth-url'],
      ['POST', '/grok-oauth/create-from-code'],
      ['POST', '/grok-oauth/create-from-refresh-token'],
      ['POST', '/grok-oauth/sso-to-oauth'],
      ['POST', '/grok-oauth/accounts/a-1/refresh-token'],
      ['POST', '/grok-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/grok-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
  })

  it('ssoToOAuth 使用 600s 超时、refreshToken 使用 130s 超时', async () => {
    await grokOAuthApi.ssoToOAuth({ cookies: [] })
    expect(requests[0].timeout).toBe(600000)
    await grokOAuthApi.refreshToken('a-1', rotationPayload)
    expect(requests[1].timeout).toBe(130000)
  })

  it('ssoToOAuth 透传 params 并返回导入结果', async () => {
    installCaptureAdapter({ createdCount: 2, failed: [] })
    await expect(grokOAuthApi.ssoToOAuth({ cookies: [] }, { systemAccountId: 'sa-1' })).resolves.toEqual({ createdCount: 2, failed: [] })
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1' })
  })
})

describe('myGrokOAuthApi 请求形状', () => {
  it('各方法命中 /my-grok-oauth 前缀路径', async () => {
    await myGrokOAuthApi.authUrl({})
    await myGrokOAuthApi.createFromCode({ code: 'c-1' })
    await myGrokOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await myGrokOAuthApi.ssoToOAuth({ cookies: [] })
    await myGrokOAuthApi.refreshToken('a-1', rotationPayload)
    await myGrokOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await myGrokOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/my-grok-oauth/auth-url'],
      ['POST', '/my-grok-oauth/create-from-code'],
      ['POST', '/my-grok-oauth/create-from-refresh-token'],
      ['POST', '/my-grok-oauth/sso-to-oauth'],
      ['POST', '/my-grok-oauth/accounts/a-1/refresh-token'],
      ['POST', '/my-grok-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/my-grok-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
    expect(requests[3].timeout).toBe(600000)
    expect(requests[4].timeout).toBe(130000)
  })
})
