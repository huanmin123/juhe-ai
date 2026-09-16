import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { authorizationOptionsApi, myAuthorizationOptionsApi } from './authorizationOptions'

interface CapturedRequest {
  method: string
  url: string
  params?: unknown
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
      params: config.params
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

describe('authorizationOptionsApi 请求形状', () => {
  it('各方法发出正确的 method 与 URL', async () => {
    await authorizationOptionsApi.granteeAccounts()
    await authorizationOptionsApi.granteeTeams()
    await authorizationOptionsApi.granteeGroups({ granteeSystemAccountId: 'sa-1' })
    expect(requestShapes()).toEqual([
      ['GET', '/authorization-options/grantee-accounts'],
      ['GET', '/authorization-options/grantee-teams'],
      ['GET', '/authorization-options/grantee-groups']
    ])
  })

  it('granteeAccounts 归一化选项参数', async () => {
    await authorizationOptionsApi.granteeAccounts({ ids: ['sa-1'], keyword: ' 用户 ', limit: 5 })
    expect(requests[0].params).toEqual({ ids: 'sa-1', keyword: '用户', limit: 5 })
  })

  it('granteeGroups 强制携带 granteeSystemAccountId', async () => {
    await authorizationOptionsApi.granteeGroups({ granteeSystemAccountId: 'sa-9', providerCode: ' openai ', preferDefault: true })
    expect(requests[0].params).toEqual({
      granteeSystemAccountId: 'sa-9',
      providerCode: 'openai',
      preferDefault: true
    })
  })

  it('无参数时 granteeAccounts 不发送查询串', async () => {
    await authorizationOptionsApi.granteeAccounts()
    expect(requests[0].params).toBeUndefined()
  })
})

describe('myAuthorizationOptionsApi 请求形状', () => {
  it('各方法命中 /my-authorization-options 前缀路径并归一化参数', async () => {
    await myAuthorizationOptionsApi.granteeAccounts({ keyword: 'k', limit: 3 })
    await myAuthorizationOptionsApi.granteeTeams({ ids: ['t-1'] })
    await myAuthorizationOptionsApi.granteeGroups({ granteeSystemAccountId: 'sa-2' })
    expect(requestShapes()).toEqual([
      ['GET', '/my-authorization-options/grantee-accounts'],
      ['GET', '/my-authorization-options/grantee-teams'],
      ['GET', '/my-authorization-options/grantee-groups']
    ])
    expect(requests[0].params).toEqual({ keyword: 'k', limit: 3 })
    expect(requests[1].params).toEqual({ ids: 't-1' })
    expect(requests[2].params).toEqual({ granteeSystemAccountId: 'sa-2' })
  })
})
