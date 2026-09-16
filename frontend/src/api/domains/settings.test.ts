import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { settingsApi } from './settings'

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

describe('settingsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await settingsApi.public()
    await settingsApi.global()
    await settingsApi.updateGlobal({ siteName: '站名' } as never)
    await settingsApi.get()
    await settingsApi.update({ retentionDays: 30 } as never)
    await settingsApi.section('brand')
    await settingsApi.updateSection('brand', { siteName: '新站名' })
    expect(requestShapes()).toEqual([
      ['GET', '/settings/public'],
      ['GET', '/settings/global'],
      ['PATCH', '/settings/global'],
      ['GET', '/settings'],
      ['PATCH', '/settings'],
      ['GET', '/settings/sections/brand'],
      ['PATCH', '/settings/sections/brand']
    ])
  })

  it('updateSection 携带分区键值 payload', async () => {
    await settingsApi.updateSection('data-retention', { retentionDays: 30 })
    expect(requests[0].url).toBe('/settings/sections/data-retention')
    expect(payloadOf(requests[0])).toEqual({ retentionDays: 30 })
  })

  it('public 返回解包后的公开设置', async () => {
    installCaptureAdapter({ siteName: '聚合' })
    await expect(settingsApi.public()).resolves.toEqual({ siteName: '聚合' })
  })
})
