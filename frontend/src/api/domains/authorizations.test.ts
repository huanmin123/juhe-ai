import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { authorizationsApi, myAuthorizationsApi } from './authorizations'

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

describe('authorizationsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await authorizationsApi.list()
    await authorizationsApi.listPage({ page: 1 })
    await authorizationsApi.detail('auth-1')
    await authorizationsApi.create({ resourceType: 'group', resourceId: 'g-1', granteeType: 'system_account', granteeId: 'sa-1' } as never)
    await authorizationsApi.update('auth-1', { expectedUpdatedAt: 'ts', status: 'paused' } as never)
    await authorizationsApi.updateExpire('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await authorizationsApi.revoke('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await authorizationsApi.returnAuthorization('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await authorizationsApi.usage('auth-1')
    await authorizationsApi.teamUsage()
    await authorizationsApi.userUsage()
    await authorizationsApi.teamUsageSummary()
    await authorizationsApi.userUsageSummary()
    expect(requestShapes()).toEqual([
      ['GET', '/authorizations'],
      ['GET', '/authorizations'],
      ['GET', '/authorizations/auth-1'],
      ['POST', '/authorizations'],
      ['PATCH', '/authorizations/auth-1'],
      ['PATCH', '/authorizations/auth-1/expire'],
      ['DELETE', '/authorizations/auth-1'],
      ['DELETE', '/authorizations/auth-1/return'],
      ['GET', '/authorizations/auth-1/usage'],
      ['GET', '/authorizations/usage/team-details'],
      ['GET', '/authorizations/usage/user-details'],
      ['GET', '/authorizations/usage/team-summary'],
      ['GET', '/authorizations/usage/user-summary']
    ])
  })

  it('list 补默认分页 page=1、pageSize=500 并直接返回 items', async () => {
    installCaptureAdapter({ items: [{ id: 'auth-1' }], total: 1 })
    await expect(authorizationsApi.list()).resolves.toEqual([{ id: 'auth-1' }])
    expect(requests[0].params).toEqual({ page: 1, pageSize: 500 })
  })

  it('list 保留调用方分页参数', async () => {
    await authorizationsApi.list({ page: 3, pageSize: 20 })
    expect(requests[0].params).toEqual({ page: 3, pageSize: 20 })
  })

  it('listPage 原样透传参数不做默认分页', async () => {
    await authorizationsApi.listPage({ page: 2, status: 'active' } as never)
    expect(requests[0].params).toEqual({ page: 2, status: 'active' })
  })

  it('revoke 与 returnAuthorization 以请求体携带乐观锁 payload', async () => {
    await authorizationsApi.revoke('auth-1', { expectedUpdatedAt: 'ts-1' } as never)
    expect(payloadOf(requests[0])).toEqual({ expectedUpdatedAt: 'ts-1' })
    await authorizationsApi.returnAuthorization('auth-1', { expectedUpdatedAt: 'ts-2' } as never)
    expect(payloadOf(requests[1])).toEqual({ expectedUpdatedAt: 'ts-2' })
  })
})

describe('myAuthorizationsApi 请求形状', () => {
  it('各方法命中 /my-authorizations 前缀路径', async () => {
    await myAuthorizationsApi.list()
    await myAuthorizationsApi.listPage()
    await myAuthorizationsApi.detail('auth-1')
    await myAuthorizationsApi.create({ resourceType: 'group', resourceId: 'g-1', granteeType: 'team', granteeId: 't-1' } as never)
    await myAuthorizationsApi.update('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await myAuthorizationsApi.updateExpire('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await myAuthorizationsApi.revoke('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await myAuthorizationsApi.returnAuthorization('auth-1', { expectedUpdatedAt: 'ts' } as never)
    await myAuthorizationsApi.usage('auth-1')
    await myAuthorizationsApi.teamUsage()
    await myAuthorizationsApi.userUsage()
    await myAuthorizationsApi.teamUsageSummary()
    await myAuthorizationsApi.userUsageSummary()
    expect(requestShapes()).toEqual([
      ['GET', '/my-authorizations'],
      ['GET', '/my-authorizations'],
      ['GET', '/my-authorizations/auth-1'],
      ['POST', '/my-authorizations'],
      ['PATCH', '/my-authorizations/auth-1'],
      ['PATCH', '/my-authorizations/auth-1/expire'],
      ['DELETE', '/my-authorizations/auth-1'],
      ['DELETE', '/my-authorizations/auth-1/return'],
      ['GET', '/my-authorizations/auth-1/usage'],
      ['GET', '/my-authorizations/usage/team-details'],
      ['GET', '/my-authorizations/usage/user-details'],
      ['GET', '/my-authorizations/usage/team-summary'],
      ['GET', '/my-authorizations/usage/user-summary']
    ])
  })

  it('个人视角 list 补默认分页且剥离 systemAccountId', async () => {
    await myAuthorizationsApi.list({ systemAccountId: 'sa-1', page: 2 } as never)
    expect(requests[0].params).toEqual({ page: 2, pageSize: 500 })
  })

  it('个人视角 listPage 与 usage 剥离 systemAccountId', async () => {
    await myAuthorizationsApi.listPage({ systemAccountId: 'sa-1', page: 1 } as never)
    expect(requests[0].params).toEqual({ page: 1 })
    await myAuthorizationsApi.usage('auth-1', { systemAccountId: 'sa-1', startDate: '2026-01-01' } as never)
    expect(requests[1].params).toEqual({ startDate: '2026-01-01' })
  })
})
