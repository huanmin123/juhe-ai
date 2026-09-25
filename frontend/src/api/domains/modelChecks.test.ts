import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { http } from '../http'
import { modelChecksApi, myModelChecksApi } from './modelChecks'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
  data?: unknown
  timeout?: unknown
  signal?: unknown
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
      timeout: config.timeout,
      signal: config.signal
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

/** 构造只含一个 complete 事件的 SSE fetch Response。 */
function completeStreamResponse(detail: unknown): Response {
  const encoder = new TextEncoder()
  const chunk = encoder.encode(`event: complete\ndata: ${JSON.stringify(detail)}\n\n`)
  let served = false
  return {
    ok: true,
    status: 200,
    text: async () => '',
    clone: () => completeStreamResponse(detail),
    body: {
      getReader: () => ({
        read: async () => {
          if (served) return { done: true, value: undefined }
          served = true
          return { done: false, value: chunk }
        },
        cancel: async () => undefined,
        releaseLock: () => undefined
      })
    }
  } as unknown as Response
}

beforeEach(() => {
  installCaptureAdapter()
})

afterEach(() => {
  http.defaults.adapter = originalAdapter
  vi.unstubAllGlobals()
})

describe('modelChecksApi 请求形状', () => {
  it('常规方法发出正确的 method 与 URL', async () => {
    await modelChecksApi.options()
    await modelChecksApi.accountOptions({ purpose: 'run', limit: 10 })
    await modelChecksApi.active()
    await modelChecksApi.run({ model: 'gpt-4o' } as never)
    await modelChecksApi.stop()
    await modelChecksApi.list()
    await modelChecksApi.detail('run-1')
    await modelChecksApi.qualityPolicy()
    await modelChecksApi.saveQualityPolicy({ policy: {} } as never)
    await modelChecksApi.qualitySchedules()
    await modelChecksApi.saveQualitySchedule({ accountId: 'a-1', model: 'gpt-4o', cron: '* * * * *' } as never)
    await modelChecksApi.patchQualitySchedule('qs-1', { cron: '0 * * * *' } as never)
    await modelChecksApi.deleteQualitySchedule('qs-1')
    expect(requestShapes()).toEqual([
      ['GET', '/model-checks/options'],
      ['GET', '/model-checks/account-options'],
      ['GET', '/model-checks/run/active'],
      ['POST', '/model-checks/run'],
      ['POST', '/model-checks/run/stop'],
      ['GET', '/model-checks/runs'],
      ['GET', '/model-checks/runs/run-1'],
      ['GET', '/model-checks/quality-policy'],
      ['PATCH', '/model-checks/quality-policy'],
      ['GET', '/model-checks/quality-schedules'],
      ['POST', '/model-checks/quality-schedules'],
      ['PATCH', '/model-checks/quality-schedules/qs-1'],
      ['DELETE', '/model-checks/quality-schedules/qs-1']
    ])
  })

  it('run 禁用超时，stop 携带空 body', async () => {
    await modelChecksApi.run({ model: 'gpt-4o' } as never)
    expect(requests[0].timeout).toBe(0)
    await modelChecksApi.stop()
    expect(payloadOf(requests[1])).toEqual({})
  })

  it('accountOptions 透传参数与 signal', async () => {
    const controller = new AbortController()
    await modelChecksApi.accountOptions({ purpose: 'history', limit: 5, keyword: 'k' }, { signal: controller.signal })
    expect(requests[0].params).toEqual({ purpose: 'history', limit: 5, keyword: 'k' })
    expect(requests[0].signal).toBe(controller.signal)
  })

  it('个人视角 accountOptions 透传 signal', async () => {
    const controller = new AbortController()
    await myModelChecksApi.accountOptions({ purpose: 'run', limit: 5 }, { signal: controller.signal })
    expect(requests[0].signal).toBe(controller.signal)
  })

  it('list 归一化运行列表参数', async () => {
    await modelChecksApi.list({ systemAccountId: ' sa-1 ', page: 1, status: 'failed' })
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1', page: 1, status: 'failed' })
  })

  it('runStream 走 fetch SSE 并在 complete 后返回详情', async () => {
    const fetchMock = vi.fn().mockResolvedValue(completeStreamResponse({ id: 'run-9', status: 'completed' }))
    vi.stubGlobal('fetch', fetchMock)
    const onProgress = vi.fn()
    await expect(modelChecksApi.runStream({ model: 'gpt-4o' } as never, { onProgress }, { systemAccountId: 'sa-1' }))
      .resolves.toEqual({ id: 'run-9', status: 'completed' })
    expect(fetchMock.mock.calls[0][0]).toBe('/__aisys__/api/model-checks/run/stream?systemAccountId=sa-1')
    expect(requests).toHaveLength(0)
  })
})

describe('myModelChecksApi 请求形状', () => {
  it('各方法命中 /my-model-checks 前缀路径', async () => {
    await myModelChecksApi.options()
    await myModelChecksApi.accountOptions({ purpose: 'run', limit: 10 })
    await myModelChecksApi.active()
    await myModelChecksApi.run({ model: 'gpt-4o' } as never)
    await myModelChecksApi.stop()
    await myModelChecksApi.list()
    await myModelChecksApi.detail('run-1')
    await myModelChecksApi.qualityPolicy()
    await myModelChecksApi.saveQualityPolicy({ policy: {} } as never)
    await myModelChecksApi.qualitySchedules()
    await myModelChecksApi.saveQualitySchedule({ accountId: 'a-1', model: 'gpt-4o', cron: '* * * * *' } as never)
    await myModelChecksApi.patchQualitySchedule('qs-1', { cron: '0 * * * *' } as never)
    await myModelChecksApi.deleteQualitySchedule('qs-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-model-checks/options'],
      ['GET', '/my-model-checks/account-options'],
      ['GET', '/my-model-checks/run/active'],
      ['POST', '/my-model-checks/run'],
      ['POST', '/my-model-checks/run/stop'],
      ['GET', '/my-model-checks/runs'],
      ['GET', '/my-model-checks/runs/run-1'],
      ['GET', '/my-model-checks/quality-policy'],
      ['PATCH', '/my-model-checks/quality-policy'],
      ['GET', '/my-model-checks/quality-schedules'],
      ['POST', '/my-model-checks/quality-schedules'],
      ['PATCH', '/my-model-checks/quality-schedules/qs-1'],
      ['DELETE', '/my-model-checks/quality-schedules/qs-1']
    ])
    expect(requests[3].timeout).toBe(0)
  })

  it('runStream 走 fetch SSE 的个人视角路径', async () => {
    const fetchMock = vi.fn().mockResolvedValue(completeStreamResponse({ id: 'run-10' }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(myModelChecksApi.runStream({ model: 'gpt-4o' } as never)).resolves.toEqual({ id: 'run-10' })
    expect(fetchMock.mock.calls[0][0]).toBe('/__aisys__/api/my-model-checks/run/stream')
  })
})
