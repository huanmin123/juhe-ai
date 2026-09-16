import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { announcementsApi } from './announcements'

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

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('announcementsApi 请求形状', () => {
  it('公开面方法命中 /announcements/public 路径', async () => {
    await announcementsApi.publicList()
    await announcementsApi.publicDetail('ann-1')
    await announcementsApi.markRead({ announcementIds: ['ann-1', 'ann-2'] })
    expect(requestShapes()).toEqual([
      ['GET', '/announcements/public'],
      ['GET', '/announcements/public/ann-1'],
      ['POST', '/announcements/public/read']
    ])
  })

  it('管理面方法发出正确的 method 与 URL', async () => {
    await announcementsApi.list()
    await announcementsApi.listPage({ page: 2, pageSize: 20 })
    await announcementsApi.detail('ann-1')
    await announcementsApi.create({ title: '公告' } as never)
    await announcementsApi.update('ann-1', { title: '改' } as never)
    await announcementsApi.publish('ann-1', { version: 2 } as never)
    await announcementsApi.unpublish('ann-1', { version: 2 } as never)
    await announcementsApi.delete('ann-1', { version: 3 } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/announcements'],
      ['GET', '/announcements'],
      ['GET', '/announcements/ann-1'],
      ['POST', '/announcements'],
      ['PATCH', '/announcements/ann-1'],
      ['POST', '/announcements/ann-1/publish'],
      ['POST', '/announcements/ann-1/unpublish'],
      ['DELETE', '/announcements/ann-1']
    ])
  })

  it('list 固定拉取第一页 100 条并直接返回 items', async () => {
    installCaptureAdapter({ items: [{ id: 'ann-1' }], total: 1 })
    await expect(announcementsApi.list()).resolves.toEqual([{ id: 'ann-1' }])
    expect(requests[0].params).toEqual({ page: 1, pageSize: 100 })
  })

  it('listPage 透传分页参数', async () => {
    await announcementsApi.listPage({ page: 3, pageSize: 15 })
    expect(requests[0].params).toEqual({ page: 3, pageSize: 15 })
  })

  it('markRead 携带公告 ID 列表', async () => {
    await announcementsApi.markRead({ announcementIds: ['ann-1'] })
    expect(payloadOf(requests[0])).toEqual({ announcementIds: ['ann-1'] })
  })

  it('delete 以请求体携带版本 payload', async () => {
    await announcementsApi.delete('ann-1', { version: 3 } as never)
    expect(payloadOf(requests[0])).toEqual({ version: 3 })
  })
})
