import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { mySecurityPoliciesApi, securityPoliciesApi } from './securityPolicies'

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

describe('securityPoliciesApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL，动态路径段编码', async () => {
    await securityPoliciesApi.list()
    await securityPoliciesApi.options()
    await securityPoliciesApi.detail('sp/1')
    await securityPoliciesApi.create({ name: '策略', rules: [] }, { systemAccountId: 'sa-1' })
    await securityPoliciesApi.update('sp/1', { expectedUpdatedAt: 'ts', name: '改名' })
    await securityPoliciesApi.delete('sp/1')
    expect(requestShapes()).toEqual([
      ['GET', '/security-policies'],
      ['GET', '/security-policies/options'],
      ['GET', '/security-policies/sp%2F1'],
      ['POST', '/security-policies'],
      ['PATCH', '/security-policies/sp%2F1'],
      ['DELETE', '/security-policies/sp%2F1']
    ])
  })

  it('list 透传分页、过滤与 owner 收窄参数', async () => {
    await securityPoliciesApi.list({ page: 2, pageSize: 50, keyword: 'k', systemAccountId: 'sa-1' })
    expect(requests[0].params).toEqual({ page: 2, pageSize: 50, keyword: 'k', systemAccountId: 'sa-1' })
  })

  it('options 透传 ids、keyword、limit', async () => {
    await securityPoliciesApi.options({ ids: ['a', 'b'], keyword: 'k', limit: 50 })
    expect(requests[0].params).toEqual({ ids: ['a', 'b'], keyword: 'k', limit: 50 })
  })

  it('create 携带目标 owner 查询参数与业务 payload', async () => {
    await securityPoliciesApi.create(
      {
        name: '策略',
        description: '说明',
        rules: [{ id: 'r1', type: 'phone', action: 'mask', maskKeep: 3 }]
      },
      { systemAccountId: 'sa-9' }
    )
    expect(requests[0].params).toEqual({ systemAccountId: 'sa-9' })
    expect(payloadOf(requests[0])).toEqual({
      name: '策略',
      description: '说明',
      rules: [{ id: 'r1', type: 'phone', action: 'mask', maskKeep: 3 }]
    })
  })

  it('update 携带 expectedUpdatedAt 与变更 payload（PATCH 白名单）', async () => {
    await securityPoliciesApi.update('sp-1', { expectedUpdatedAt: 'ts-1', rules: [{ id: 'r2', type: 'custom_keywords', action: 'placeholder', keywords: ['XX项目'], label: 'PROJECT_CODE' }] })
    expect(payloadOf(requests[0])).toEqual({
      expectedUpdatedAt: 'ts-1',
      rules: [{ id: 'r2', type: 'custom_keywords', action: 'placeholder', keywords: ['XX项目'], label: 'PROJECT_CODE' }]
    })
  })

  it('delete 解包响应 data 并返回 resetBindings 重置数', async () => {
    installCaptureAdapter({ resetBindings: 3 })
    expect(await securityPoliciesApi.delete('sp-1')).toEqual({ resetBindings: 3 })
    expect(await mySecurityPoliciesApi.delete('sp-1')).toEqual({ resetBindings: 3 })
  })
})

describe('mySecurityPoliciesApi 请求形状', () => {
  it('各方法指向 my- 前缀路径并剥离伪造 systemAccountId', async () => {
    await mySecurityPoliciesApi.list({ page: 1, keyword: 'k' } as Parameters<typeof mySecurityPoliciesApi.list>[0])
    await mySecurityPoliciesApi.options({ keyword: 'k', limit: 50 })
    await mySecurityPoliciesApi.detail('sp-1')
    await mySecurityPoliciesApi.create({ name: '策略', rules: [] })
    await mySecurityPoliciesApi.update('sp-1', { expectedUpdatedAt: 'ts', name: '改名' })
    await mySecurityPoliciesApi.delete('sp-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-security-policies'],
      ['GET', '/my-security-policies/options'],
      ['GET', '/my-security-policies/sp-1'],
      ['POST', '/my-security-policies'],
      ['PATCH', '/my-security-policies/sp-1'],
      ['DELETE', '/my-security-policies/sp-1']
    ])
    expect(requests[0].params).toEqual({ page: 1, keyword: 'k' })
    expect(requests[1].params).toEqual({ keyword: 'k', limit: 50 })
  })
})
