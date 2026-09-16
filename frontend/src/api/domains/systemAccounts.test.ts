import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { systemAccountsApi } from './systemAccounts'

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

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('systemAccountsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    installCaptureAdapter([])
    await systemAccountsApi.list()
    await systemAccountsApi.listPage({ page: 2 })
    await systemAccountsApi.options()
    await systemAccountsApi.create({ username: 'admin2' })
    await systemAccountsApi.update('sa-1', { expectedUpdatedAt: 'ts' } as never)
    expect(requestShapes()).toEqual([
      ['GET', '/system-accounts'],
      ['GET', '/system-accounts'],
      ['GET', '/system-accounts/options'],
      ['POST', '/system-accounts'],
      ['PATCH', '/system-accounts/sa-1']
    ])
  })

  it('list 固定拉取第一页 100 条并直接返回 items', async () => {
    installCaptureAdapter({ items: [{ id: 'sa-1' }], total: 1 })
    await expect(systemAccountsApi.list()).resolves.toEqual([{ id: 'sa-1' }])
    expect(requests[0].params).toEqual({ page: 1, pageSize: 100 })
  })

  it('listPage 归一化分页与关键字参数', async () => {
    await systemAccountsApi.listPage({ page: 2, pageSize: 50, keyword: ' adm ' })
    expect(requests[0].params).toEqual({ page: 2, pageSize: 50, keyword: 'adm' })
  })

  it('options 把后端选项映射为主选项结构', async () => {
    installCaptureAdapter([
      { id: 'sa-1', name: '管理员', disabledReason: '' },
      { id: 'sa-2', name: '被禁用户', disabledReason: 'banned' }
    ])
    await expect(systemAccountsApi.options()).resolves.toEqual([
      { id: 'sa-1', username: '管理员', displayName: '管理员', status: 'active' },
      { id: 'sa-2', username: '被禁用户', displayName: '被禁用户', status: 'disabled' }
    ])
    expect(requests[0].url).toBe('/system-accounts/options')
  })

  it('options 归一化查询参数', async () => {
    installCaptureAdapter([])
    await systemAccountsApi.options({ ids: ['sa-1'], keyword: ' k ', limit: 4 })
    expect(requests[0].params).toEqual({ ids: 'sa-1', keyword: 'k', limit: 4 })
  })
})
