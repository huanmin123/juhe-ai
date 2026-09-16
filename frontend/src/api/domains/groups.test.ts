import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { groupsApi, myGroupsApi } from './groups'

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

describe('groupsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    installCaptureAdapter([])
    await groupsApi.list()
    await groupsApi.listPage()
    await groupsApi.detail('g-1')
    await groupsApi.editBasicDetail('g-1')
    await groupsApi.options()
    await groupsApi.authorizationOptions()
    await groupsApi.accountOptions()
    await groupsApi.routeStrategyOptions()
    await groupsApi.create({ name: '分组' })
    await groupsApi.update('g-1', { expectedUpdatedAt: 'ts', name: '改名' } as never)
    await groupsApi.returnAuthorization('g-1')
    await groupsApi.delete('g-1')
    expect(requestShapes()).toEqual([
      ['GET', '/groups'],
      ['GET', '/groups'],
      ['GET', '/groups/g-1'],
      ['GET', '/groups/g-1/edit-basic'],
      ['GET', '/groups/options'],
      ['GET', '/groups/authorization-options'],
      ['GET', '/groups/account-options'],
      ['GET', '/groups/route-strategy-options'],
      ['POST', '/groups'],
      ['PATCH', '/groups/g-1'],
      ['POST', '/groups/g-1/return-authorization'],
      ['DELETE', '/groups/g-1']
    ])
  })

  it('list 默认拉取第一页 500 条并直接返回 items', async () => {
    installCaptureAdapter({ items: [{ id: 'g-1' }], total: 1 })
    await expect(groupsApi.list()).resolves.toEqual([{ id: 'g-1' }])
    expect(requests[0].params).toEqual({ page: 1, pageSize: 500 })
  })

  it('list 允许调用方覆盖默认分页', async () => {
    await groupsApi.list({ page: 2, pageSize: 20 })
    expect(requests[0].params).toEqual({ page: 2, pageSize: 20 })
  })

  it('authorizationOptions 把 canAuthorize 映射为 permissions 结构', async () => {
    installCaptureAdapter([
      { id: 'g-1', name: '可授权分组', canAuthorize: true },
      { id: 'g-2', name: '只读分组', canAuthorize: false }
    ])
    await expect(groupsApi.authorizationOptions()).resolves.toEqual([
      { id: 'g-1', name: '可授权分组', permissions: { canAuthorize: true } },
      { id: 'g-2', name: '只读分组', permissions: { canAuthorize: false } }
    ])
  })

  it('options 归一化选项参数', async () => {
    await groupsApi.options({ keyword: ' 关键 ', manageableOnly: true, limit: 5 } as never)
    expect(requests[0].params).toEqual({ keyword: '关键', manageableOnly: true, limit: 5 })
  })

  it('returnAuthorization 发送空对象 body', async () => {
    await groupsApi.returnAuthorization('g-1')
    expect(payloadOf(requests[0])).toEqual({})
  })
})

describe('myGroupsApi 请求形状', () => {
  it('各方法命中 /my-groups 前缀路径', async () => {
    installCaptureAdapter([])
    await myGroupsApi.list()
    await myGroupsApi.listPage()
    await myGroupsApi.detail('g-1')
    await myGroupsApi.editBasicDetail('g-1')
    await myGroupsApi.options()
    await myGroupsApi.authorizationOptions()
    await myGroupsApi.accountOptions()
    await myGroupsApi.routeStrategyOptions()
    await myGroupsApi.create({ name: 'x' })
    await myGroupsApi.update('g-1', { expectedUpdatedAt: 'ts' } as never)
    await myGroupsApi.returnAuthorization('g-1')
    await myGroupsApi.delete('g-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-groups'],
      ['GET', '/my-groups'],
      ['GET', '/my-groups/g-1'],
      ['GET', '/my-groups/g-1/edit-basic'],
      ['GET', '/my-groups/options'],
      ['GET', '/my-groups/authorization-options'],
      ['GET', '/my-groups/account-options'],
      ['GET', '/my-groups/route-strategy-options'],
      ['POST', '/my-groups'],
      ['PATCH', '/my-groups/g-1'],
      ['POST', '/my-groups/g-1/return-authorization'],
      ['DELETE', '/my-groups/g-1']
    ])
  })

  it('个人视角 list 默认分页且剥离 systemAccountId', async () => {
    await myGroupsApi.list({ page: 2 } as never)
    expect(requests[0].params).toEqual({ page: 2, pageSize: 500 })
    await myGroupsApi.listPage({ systemAccountId: 'sa-1', page: 1 } as never)
    expect(requests[1].params).toEqual({ page: 1 })
  })

  it('个人视角 routeStrategyOptions 不携带 systemAccountId', async () => {
    await myGroupsApi.routeStrategyOptions({ keyword: 'k' })
    expect(requests[0].params).toEqual({ keyword: 'k' })
  })
})
