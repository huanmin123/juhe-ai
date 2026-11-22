import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myAuthorizationCenterApi } from './authorizationGovernance'

interface CapturedRequest {
  method: string
  url: string
  data?: unknown
}

const originalAdapter = http.defaults.adapter
let requests: CapturedRequest[] = []

/** 替换 axios adapter：捕获请求形状并返回固定 `{ data: { data } }` 响应。 */
function installCaptureAdapter(): void {
  requests = []
  http.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    requests.push({
      method: String(config.method ?? '').toUpperCase(),
      url: String(config.url ?? ''),
      data: config.data
    })
    return { data: { data: {} }, status: 200, statusText: 'OK', headers: {}, config }
  }
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('myAuthorizationCenterApi accept/changeGroup 请求形状', () => {
  it('分组领取不传 targetGroupId 时请求无 body', async () => {
    await myAuthorizationCenterApi.accept('src-1')
    expect(requests).toHaveLength(1)
    expect(requests[0].method).toBe('POST')
    expect(requests[0].url).toBe('/my-authorization-sources/src-1/accept')
    expect(requests[0].data).toBeUndefined()
  })

  it('账户领取携带 targetGroupId 请求体', async () => {
    await myAuthorizationCenterApi.accept('src-2', 'g-9')
    expect(requests).toHaveLength(1)
    expect(requests[0].url).toBe('/my-authorization-sources/src-2/accept')
    expect(JSON.parse(String(requests[0].data))).toEqual({ targetGroupId: 'g-9' })
  })

  it('changeGroup 指向新端点并携带 targetGroupId 请求体', async () => {
    await myAuthorizationCenterApi.changeGroup('src-3', 'g-7')
    expect(requests).toHaveLength(1)
    expect(requests[0].method).toBe('POST')
    expect(requests[0].url).toBe('/my-authorization-sources/src-3/change-group')
    expect(JSON.parse(String(requests[0].data))).toEqual({ targetGroupId: 'g-7' })
  })
})
