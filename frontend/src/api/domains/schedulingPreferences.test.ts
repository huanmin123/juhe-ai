import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import type { RouteStrategySpeedFirstConfig } from '@/types/domain'
import { http } from '../http'
import { mySchedulingPreferencesApi, schedulingPreferencesApi } from './schedulingPreferences'

function fullSpeedFirstConfig(): RouteStrategySpeedFirstConfig {
  return {
    slowTriggerCount: 3,
    slowWindowSeconds: 120,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 30,
    degradedTtlSeconds: 300,
    maxFirstByteRetriesPerRequest: 2,
    totalTimeDeadlineSeconds: 120,
    compactionTotalTimeDeadlineSeconds: 300
  }
}

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

describe('schedulingPreferencesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL，动态路径段编码', async () => {
    await schedulingPreferencesApi.list()
    await schedulingPreferencesApi.options()
    await schedulingPreferencesApi.detail('sp/1')
    await schedulingPreferencesApi.create({ name: '偏好', chatMode: 'cost_first' }, { systemAccountId: 'sa-1' })
    await schedulingPreferencesApi.update('sp/1', { expectedUpdatedAt: 'ts', name: '改名' })
    await schedulingPreferencesApi.delete('sp/1')
    expect(requestShapes()).toEqual([
      ['GET', '/scheduling-preferences'],
      ['GET', '/scheduling-preferences/options'],
      ['GET', '/scheduling-preferences/sp%2F1'],
      ['POST', '/scheduling-preferences'],
      ['PATCH', '/scheduling-preferences/sp%2F1'],
      ['DELETE', '/scheduling-preferences/sp%2F1']
    ])
  })

  it('list 透传分页、过滤与 owner 收窄参数', async () => {
    await schedulingPreferencesApi.list({ page: 2, pageSize: 50, keyword: 'k', chatMode: 'speed_first', status: 'enabled', systemAccountId: 'sa-1' })
    expect(requests[0].params).toEqual({ page: 2, pageSize: 50, keyword: 'k', chatMode: 'speed_first', status: 'enabled', systemAccountId: 'sa-1' })
  })

  it('options 透传 ids、keyword、limit、activeOnly', async () => {
    await schedulingPreferencesApi.options({ ids: ['a', 'b'], keyword: 'k', limit: 50, activeOnly: false })
    expect(requests[0].params).toEqual({ ids: ['a', 'b'], keyword: 'k', limit: 50, activeOnly: false })
  })

  it('create 携带目标 owner 查询参数与业务 payload', async () => {
    await schedulingPreferencesApi.create(
      { name: '偏好', chatMode: 'speed_first', chatSpeedFirstConfig: { firstByteDeadlineMs: 30000, speedFirstConfig: fullSpeedFirstConfig() } },
      { systemAccountId: 'sa-9' }
    )
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-9' })
    expect(payloadOf(requests[0])).toEqual({
      name: '偏好',
      chatMode: 'speed_first',
      chatSpeedFirstConfig: { firstByteDeadlineMs: 30000, speedFirstConfig: fullSpeedFirstConfig() }
    })
  })

  it('update 携带 expectedUpdatedAt 与变更 payload', async () => {
    await schedulingPreferencesApi.update('sp-1', { expectedUpdatedAt: 'ts-1', enabled: false })
    expect(payloadOf(requests[0])).toEqual({ expectedUpdatedAt: 'ts-1', enabled: false })
  })
})

describe('mySchedulingPreferencesApi 请求形状', () => {
  it('各方法指向 my- 前缀路径并剥离伪造 systemAccountId', async () => {
    await mySchedulingPreferencesApi.list({ page: 1, keyword: 'k', status: 'all', chatMode: 'all' } as Parameters<typeof mySchedulingPreferencesApi.list>[0])
    await mySchedulingPreferencesApi.options({ keyword: 'k', limit: 50, activeOnly: true } as Parameters<typeof mySchedulingPreferencesApi.options>[0])
    await mySchedulingPreferencesApi.detail('sp-1')
    await mySchedulingPreferencesApi.create({ name: '偏好', chatMode: 'cost_first' })
    await mySchedulingPreferencesApi.update('sp-1', { expectedUpdatedAt: 'ts', enabled: true })
    await mySchedulingPreferencesApi.delete('sp-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-scheduling-preferences'],
      ['GET', '/my-scheduling-preferences/options'],
      ['GET', '/my-scheduling-preferences/sp-1'],
      ['POST', '/my-scheduling-preferences'],
      ['PATCH', '/my-scheduling-preferences/sp-1'],
      ['DELETE', '/my-scheduling-preferences/sp-1']
    ])
    expect(requests[0].params).toEqual({ page: 1, keyword: 'k', status: 'all', chatMode: 'all' })
    expect(requests[1].params).toEqual({ keyword: 'k', limit: 50, activeOnly: true })
  })
})
