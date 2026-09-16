import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myOpenaiOAuthApi, openaiOAuthApi } from './openaiOAuth'

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

describe('openaiOAuthApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await openaiOAuthApi.authUrl({})
    await openaiOAuthApi.createFromCode({ code: 'c-1' })
    await openaiOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await openaiOAuthApi.refreshToken('a-1', rotationPayload)
    await openaiOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await openaiOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/openai-oauth/auth-url'],
      ['POST', '/openai-oauth/create-from-code'],
      ['POST', '/openai-oauth/create-from-refresh-token'],
      ['POST', '/openai-oauth/accounts/a-1/refresh-token'],
      ['POST', '/openai-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/openai-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
  })

  it('refreshToken 使用 130s 长超时', async () => {
    await openaiOAuthApi.refreshToken('a-1', rotationPayload)
    expect(requests[0].timeout).toBe(130000)
  })

  it('reauthorizeFromCode 透传 params', async () => {
    await openaiOAuthApi.reauthorizeFromCode('a-1', rotationPayload, { viewScope: 'admin' })
    expect(requests[0].params).toEqual({ viewScope: 'admin' })
  })
})

describe('myOpenaiOAuthApi 请求形状', () => {
  it('各方法命中 /my-openai-oauth 前缀路径', async () => {
    await myOpenaiOAuthApi.authUrl({})
    await myOpenaiOAuthApi.createFromCode({ code: 'c-1' })
    await myOpenaiOAuthApi.createFromRefreshToken({ refreshToken: 'rt-1' })
    await myOpenaiOAuthApi.refreshToken('a-1', rotationPayload)
    await myOpenaiOAuthApi.reauthorizeFromCode('a-1', rotationPayload)
    await myOpenaiOAuthApi.reauthorizeFromRefreshToken('a-1', rotationPayload)
    expect(requestShapes()).toEqual([
      ['POST', '/my-openai-oauth/auth-url'],
      ['POST', '/my-openai-oauth/create-from-code'],
      ['POST', '/my-openai-oauth/create-from-refresh-token'],
      ['POST', '/my-openai-oauth/accounts/a-1/refresh-token'],
      ['POST', '/my-openai-oauth/accounts/a-1/reauthorize-from-code'],
      ['POST', '/my-openai-oauth/accounts/a-1/reauthorize-from-refresh-token']
    ])
    expect(requests[3].timeout).toBe(130000)
  })
})
