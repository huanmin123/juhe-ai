import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { authApi } from './auth'

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

describe('authApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await authApi.captcha()
    await authApi.login({ username: 'admin', password: 'pw' })
    await authApi.logout()
    await authApi.me()
    await authApi.profile()
    await authApi.updateProfile({ displayName: '管理员' })
    await authApi.changePassword({ oldPassword: 'old', newPassword: 'new' })
    expect(requestShapes()).toEqual([
      ['GET', '/auth/captcha'],
      ['POST', '/auth/login'],
      ['POST', '/auth/logout'],
      ['GET', '/auth/me'],
      ['GET', '/auth/profile'],
      ['PATCH', '/auth/me'],
      ['POST', '/auth/change-password']
    ])
  })

  it('login 携带用户名、密码与可选验证码字段', async () => {
    await authApi.login({ username: 'admin', password: 'pw', captchaId: 'cap-1', captchaCode: '1234' })
    expect(payloadOf(requests[0])).toEqual({ username: 'admin', password: 'pw', captchaId: 'cap-1', captchaCode: '1234' })
  })

  it('changePassword 的 oldPassword 可选', async () => {
    await authApi.changePassword({ newPassword: 'only-new' })
    expect(payloadOf(requests[0])).toEqual({ newPassword: 'only-new' })
  })

  it('me 返回解包后的当前用户', async () => {
    installCaptureAdapter({ username: 'admin', role: 'admin' })
    await expect(authApi.me()).resolves.toEqual({ username: 'admin', role: 'admin' })
  })
})
