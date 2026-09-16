import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { geminiOAuthApi, myGeminiOAuthApi } from './geminiOAuth'

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

describe('geminiOAuthApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await geminiOAuthApi.capabilities()
    await geminiOAuthApi.authUrl({})
    await geminiOAuthApi.createFromCode({ code: 'c-1' })
    await geminiOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await geminiOAuthApi.refreshToken('a-1', rotationPayload)
    await geminiOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await geminiOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['GET', '/gemini-oauth/capabilities'],
      ['POST', '/gemini-oauth/auth-url'],
      ['POST', '/gemini-oauth/create-from-code'],
      ['POST', '/gemini-oauth/create-from-refresh-token'],
      ['POST', '/gemini-oauth/accounts/a-1/refresh-token'],
      ['POST', '/gemini-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/gemini-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
  })

  it('refreshToken 使用 130s 长超时', async () => {
    await geminiOAuthApi.refreshToken('a-1', rotationPayload)
    expect(requests[0].timeout).toBe(130000)
  })

  it('capabilities 返回解包后的能力声明', async () => {
    installCaptureAdapter({ defaultOAuthType: 'code_assist', oauthTypes: [] })
    await expect(geminiOAuthApi.capabilities()).resolves.toEqual({ defaultOAuthType: 'code_assist', oauthTypes: [] })
  })
})

describe('myGeminiOAuthApi 请求形状', () => {
  it('各方法命中 /my-gemini-oauth 前缀路径', async () => {
    await myGeminiOAuthApi.capabilities()
    await myGeminiOAuthApi.authUrl({})
    await myGeminiOAuthApi.createFromCode({ code: 'c-1' })
    await myGeminiOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await myGeminiOAuthApi.refreshToken('a-1', rotationPayload)
    await myGeminiOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await myGeminiOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['GET', '/my-gemini-oauth/capabilities'],
      ['POST', '/my-gemini-oauth/auth-url'],
      ['POST', '/my-gemini-oauth/create-from-code'],
      ['POST', '/my-gemini-oauth/create-from-refresh-token'],
      ['POST', '/my-gemini-oauth/accounts/a-1/refresh-token'],
      ['POST', '/my-gemini-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/my-gemini-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
  })
})
