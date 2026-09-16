import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myUiBootstrapApi, uiBootstrapApi } from './userReferenceData'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
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
      params: config.params
    })
    return { data: { data: responseData }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('uiBootstrapApi 请求形状', () => {
  it('options 命中 /ui-bootstrap/options 并携带 systemAccountId', async () => {
    await uiBootstrapApi.options({ systemAccountId: 'sa-1' })
    expect(requests[0].method).toBe('GET')
    expect(requests[0].url).toBe('/ui-bootstrap/options')
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1' })
  })
})

describe('myUiBootstrapApi 请求形状', () => {
  it('options 命中 /my-ui-bootstrap/options 且无参数', async () => {
    await myUiBootstrapApi.options()
    expect(requests[0].method).toBe('GET')
    expect(requests[0].url).toBe('/my-ui-bootstrap/options')
    expect(requests[0].params).toBeUndefined()
  })

  it('options 返回解包后的引用数据', async () => {
    installCaptureAdapter({ systemAccounts: [] })
    await expect(myUiBootstrapApi.options()).resolves.toEqual({ systemAccounts: [] })
  })
})
