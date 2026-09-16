import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myUsageRecordsApi, usageRecordsApi } from './usageRecords'

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

describe('usageRecordsApi 请求形状', () => {
  it('list 命中 /usage-records 并透传查询参数', async () => {
    await usageRecordsApi.list({ page: 1, pageSize: 20 } as never)
    expect(requests[0].method).toBe('GET')
    expect(requests[0].url).toBe('/usage-records')
    expect(requests[0].params).toEqual({ page: 1, pageSize: 20 })
  })

  it('list 返回解包后的数据', async () => {
    installCaptureAdapter({ items: [], total: 0 })
    await expect(usageRecordsApi.list()).resolves.toEqual({ items: [], total: 0 })
  })
})

describe('myUsageRecordsApi 请求形状', () => {
  it('list 命中 /my-usage-records 并剥离 systemAccountId', async () => {
    await myUsageRecordsApi.list({ systemAccountId: 'sa-1', page: 2 } as never)
    expect(requests[0].method).toBe('GET')
    expect(requests[0].url).toBe('/my-usage-records')
    expect(requests[0].params).toEqual({ page: 2 })
  })
})
