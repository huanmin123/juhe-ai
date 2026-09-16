import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { auditLogsApi, myOperationLogsApi, operationLogsApi, publicApiLogsApi, runtimeLogsApi } from './logs'

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

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('auditLogsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL 且全部禁用超时', async () => {
    await auditLogsApi.list()
    await auditLogsApi.searchHot({ keyword: 'k' } as never)
    await auditLogsApi.detail('log-1')
    await auditLogsApi.payload('log-1', 'payload-1')
    expect(requestShapes()).toEqual([
      ['GET', '/audit-logs'],
      ['GET', '/audit-logs/search-hot'],
      ['GET', '/audit-logs/log-1'],
      ['GET', '/audit-logs/log-1/payloads/payload-1']
    ])
    expect(requests.map((request) => request.timeout)).toEqual([0, 0, 0, 0])
  })

  it('searchHot 透传查询参数', async () => {
    await auditLogsApi.searchHot({ keyword: '账号' } as never)
    expect(requests[0].params).toEqual({ keyword: '账号' })
  })
})

describe('runtimeLogsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await runtimeLogsApi.list()
    await runtimeLogsApi.facets()
    await runtimeLogsApi.grepOptions()
    await runtimeLogsApi.detail('log-1')
    await runtimeLogsApi.grep({ pattern: 'ERROR' } as never)
    await runtimeLogsApi.grepDetail({ id: 'log-1', fileName: 'a.log', lineNumber: 3 })
    expect(requestShapes()).toEqual([
      ['GET', '/runtime-logs'],
      ['GET', '/runtime-logs/facets'],
      ['GET', '/runtime-logs/grep-options'],
      ['GET', '/runtime-logs/log-1'],
      ['GET', '/runtime-logs/grep'],
      ['GET', '/runtime-logs/grep-detail']
    ])
  })

  it('list/facets/detail 用默认超时，grep 与 grepDetail 禁用超时', async () => {
    await runtimeLogsApi.list()
    expect(requests[0].timeout).toBe(15000)
    await runtimeLogsApi.grep()
    expect(requests[1].timeout).toBe(0)
    await runtimeLogsApi.grepDetail({ id: 'log-1', fileName: 'a.log', lineNumber: 3 })
    expect(requests[2].timeout).toBe(0)
    expect(requests[2].params).toEqual({ id: 'log-1', fileName: 'a.log', lineNumber: 3 })
  })
})

describe('operationLogsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL 并透传参数', async () => {
    await operationLogsApi.list({ page: 1, module: 'account' })
    await operationLogsApi.detail('log-1')
    expect(requestShapes()).toEqual([
      ['GET', '/operation-logs'],
      ['GET', '/operation-logs/log-1']
    ])
    expect(requests[0].params).toEqual({ page: 1, module: 'account' })
  })
})

describe('publicApiLogsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL，detail 路径段编码', async () => {
    await publicApiLogsApi.list()
    await publicApiLogsApi.detail('req/1?x')
    expect(requestShapes()).toEqual([
      ['GET', '/public-api-logs'],
      ['GET', '/public-api-logs/req%2F1%3Fx']
    ])
  })
})

describe('myOperationLogsApi 请求形状', () => {
  it('各方法命中 /my-operation-logs 前缀路径', async () => {
    await myOperationLogsApi.list()
    await myOperationLogsApi.detail('log-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-operation-logs'],
      ['GET', '/my-operation-logs/log-1']
    ])
  })

  it('list 剥离管理员专属过滤字段', async () => {
    await myOperationLogsApi.list({ page: 2, actorSystemAccountId: 'sa-1', module: 'account' } as never)
    expect(requests[0].params).toEqual({ page: 2, module: 'account' })
  })
})
