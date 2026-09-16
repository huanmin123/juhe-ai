import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { myTeamsApi, systemTeamsApi } from './systemTeams'

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

describe('systemTeamsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await systemTeamsApi.list()
    await systemTeamsApi.detail('t-1')
    await systemTeamsApi.members('t-1')
    await systemTeamsApi.memberHistory('t-1')
    await systemTeamsApi.create({ name: '团队' })
    await systemTeamsApi.update('t-1', { expectedUpdatedAt: 'ts', name: '改名' } as never)
    await systemTeamsApi.addMembers('t-1', { systemAccountIds: ['sa-1'], expectedUpdatedAt: 'ts' } as never)
    await systemTeamsApi.removeMember('t-1', 'm-1', { expectedUpdatedAt: 'ts' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/system-teams'],
      ['GET', '/system-teams/t-1'],
      ['GET', '/system-teams/t-1/members'],
      ['GET', '/system-teams/t-1/members/history'],
      ['POST', '/system-teams'],
      ['PATCH', '/system-teams/t-1'],
      ['POST', '/system-teams/t-1/members'],
      ['DELETE', '/system-teams/t-1/members/m-1']
    ])
  })

  it('list 归一化分页与关键字参数', async () => {
    await systemTeamsApi.list({ page: 2, pageSize: 20, keyword: ' 团 ' })
    expect(requests[0].params).toEqual({ page: 2, pageSize: 20, keyword: '团' })
  })

  it('无作用域参数时 create/update 的请求配置为空', async () => {
    await systemTeamsApi.create({ name: '团队' })
    expect(requests[0].params).toBeUndefined()
    await systemTeamsApi.create({ name: '团队' }, { systemAccountId: 'sa-1', page: 1 })
    expect(requests[1].params).toEqual({ systemAccountId: 'sa-1', page: 1 })
    await systemTeamsApi.update('t-1', { expectedUpdatedAt: 'ts' } as never)
    expect(requests[2].params).toBeUndefined()
    await systemTeamsApi.update('t-1', { expectedUpdatedAt: 'ts' } as never, { systemAccountId: 'sa-1' })
    expect(requests[3].params).toEqual({ systemAccountId: 'sa-1' })
  })

  it('removeMember 以请求体携带乐观锁 payload 并支持作用域参数', async () => {
    await systemTeamsApi.removeMember('t-1', 'm-1', { expectedUpdatedAt: 'ts-1' } as never, { systemAccountId: 'sa-1' })
    expect(payloadOf(requests[0])).toEqual({ expectedUpdatedAt: 'ts-1' })
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-1' })
  })
})

describe('myTeamsApi 请求形状', () => {
  it('各方法命中 /my-teams 前缀路径', async () => {
    await myTeamsApi.list()
    await myTeamsApi.detail('t-1')
    await myTeamsApi.members('t-1')
    await myTeamsApi.memberHistory('t-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-teams'],
      ['GET', '/my-teams/t-1'],
      ['GET', '/my-teams/t-1/members'],
      ['GET', '/my-teams/t-1/members/history']
    ])
  })

  it('个人视角 list 剥离 systemAccountId', async () => {
    await myTeamsApi.list({ systemAccountId: 'sa-1', page: 1 } as never)
    expect(requests[0].params).toEqual({ page: 1 })
  })
})
