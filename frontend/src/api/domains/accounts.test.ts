import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { http } from '../http'
import { accountsApi, myAccountsApi } from './accounts'

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

function lastRequest(): CapturedRequest {
  return requests[requests.length - 1]
}

function payloadOf(request: CapturedRequest): Record<string, unknown> {
  return JSON.parse(String(request.data)) as Record<string, unknown>
}

beforeEach(() => installCaptureAdapter())
afterEach(() => {
  http.defaults.adapter = originalAdapter
})

describe('accountsApi 请求形状', () => {
  it('列表、选项与标签方法发出正确的 method 与 URL', async () => {
    await accountsApi.list()
    await accountsApi.options()
    await accountsApi.tags()
    await accountsApi.deleteTag('tag-1')
    expect(requestShapes()).toEqual([
      ['GET', '/accounts'],
      ['GET', '/accounts/options'],
      ['GET', '/accounts/tags'],
      ['DELETE', '/accounts/tags/tag-1']
    ])
  })

  it('详情类方法逐个命中对应资源路径', async () => {
    await accountsApi.editBasicDetail('a-1')
    await accountsApi.advancedDetail('a-1')
    await accountsApi.cloneContext('a-1')
    await accountsApi.oauthReauthorizationContext('a-1')
    await accountsApi.apiKeyRuntime('a-1')
    await accountsApi.balanceDetails('a-1')
    expect(requestShapes()).toEqual([
      ['GET', '/accounts/a-1/edit-basic'],
      ['GET', '/accounts/a-1/advanced'],
      ['GET', '/accounts/a-1/clone-context'],
      ['GET', '/accounts/a-1/oauth-reauthorization-context'],
      ['GET', '/accounts/a-1/api-key-runtime'],
      ['GET', '/accounts/a-1/balance/details']
    ])
  })

  it('运行时变更与锁相关方法命中 POST/PATCH 路径', async () => {
    await accountsApi.revalidateApiKeyRuntime('a-1', { expectedConfigRevision: 2 })
    await accountsApi.runtimeReset('a-1', { expectedConfigRevision: 2 })
    await accountsApi.lock('a-1')
    await accountsApi.unlock('a-1')
    await accountsApi.updateLockConfig('a-1', { lockDeathTimeoutSeconds: 60 })
    await accountsApi.forceActivate('a-1')
    await accountsApi.refreshBalance('a-1')
    await accountsApi.returnAuthorization('a-1')
    await accountsApi.delete('a-1')
    expect(requestShapes()).toEqual([
      ['POST', '/accounts/a-1/api-key-runtime/revalidate'],
      ['POST', '/accounts/a-1/runtime-reset'],
      ['POST', '/accounts/a-1/lock'],
      ['POST', '/accounts/a-1/unlock'],
      ['POST', '/accounts/a-1/lock-config'],
      ['POST', '/accounts/a-1/force-activate'],
      ['POST', '/accounts/a-1/balance/refresh'],
      ['POST', '/accounts/a-1/return-authorization'],
      ['DELETE', '/accounts/a-1']
    ])
  })

  it('导入导出、批处理与更新类方法命中正确路径', async () => {
    await accountsApi.export({ accountIds: ['a-1'] })
    await accountsApi.importPreview({ data: {} })
    await accountsApi.importConfirm({ data: {} })
    await accountsApi.create({ name: '新账户' })
    await accountsApi.update('a-1', { expectedConfigRevision: 1, name: '改名' })
    await accountsApi.batchEditContext(['a-1'], ['supportedModels'])
    await accountsApi.batchUpdate({ items: [] } as never)
    await accountsApi.updateTags('a-1', { tags: ['t'], expectedConfigRevision: 1 })
    await accountsApi.updateAuthorizedDispatch('a-1', { expectedConfigRevision: 1, priority: 5 })
    await accountsApi.bindGroup('a-1', { groupId: 'g-1', expectedConfigRevision: 1 })
    await accountsApi.migrateTraffic('a-1', { targetAccountId: 'a-2' })
    await accountsApi.testBalanceDraft({ account: {} } as never)
    expect(requestShapes()).toEqual([
      ['POST', '/accounts/export'],
      ['POST', '/accounts/import/preview'],
      ['POST', '/accounts/import/confirm'],
      ['POST', '/accounts'],
      ['PATCH', '/accounts/a-1'],
      ['POST', '/accounts/batch-edit-context'],
      ['POST', '/accounts/batch-update'],
      ['PATCH', '/accounts/a-1/tags'],
      ['PATCH', '/accounts/a-1/authorized-dispatch'],
      ['POST', '/accounts/a-1/group'],
      ['POST', '/accounts/a-1/traffic-migration'],
      ['POST', '/accounts/balance/test-draft']
    ])
  })

  it('测试会话与测试任务方法命中正确路径', async () => {
    await accountsApi.testOptions('a-1')
    await accountsApi.test('a-1')
    await accountsApi.testDraft({ model: 'gpt-4o' } as never)
    await accountsApi.createTestSession()
    await accountsApi.heartbeatTestSession('sess-1')
    await accountsApi.completeTestSession('sess-1')
    await accountsApi.cancelTestSession('sess-1')
    await accountsApi.testTask('task-1')
    await accountsApi.cancelTestTask('task-1')
    expect(requestShapes()).toEqual([
      ['GET', '/accounts/a-1/test-options'],
      ['POST', '/accounts/a-1/test'],
      ['POST', '/accounts/test-draft'],
      ['POST', '/accounts/test-sessions'],
      ['POST', '/accounts/test-sessions/sess-1/heartbeat'],
      ['POST', '/accounts/test-sessions/sess-1/complete'],
      ['POST', '/accounts/test-sessions/sess-1/cancel'],
      ['GET', '/accounts/test-tasks/task-1'],
      ['POST', '/accounts/test-tasks/task-1/cancel']
    ])
  })

  it('list 归一化参数并透传 signal', async () => {
    const controller = new AbortController()
    await accountsApi.list(
      { systemAccountId: 'sa-1', providerCode: 'all', ids: ['x-1', 'x-2'], status: 'active' },
      { signal: controller.signal }
    )
    expect(lastRequest().params).toEqual({ systemAccountId: 'sa-1', ids: 'x-1,x-2', status: 'active' })
    expect(lastRequest().signal).toBe(controller.signal)
  })

  it('lock/unlock 未提供 payload 时发送空对象，forceActivate 携带确认标记', async () => {
    await accountsApi.lock('a-1')
    expect(payloadOf(lastRequest())).toEqual({})
    await accountsApi.unlock('a-1', { lockRetryIntervalSeconds: 30 })
    expect(payloadOf(lastRequest())).toEqual({ lockRetryIntervalSeconds: 30 })
    await accountsApi.forceActivate('a-1')
    expect(payloadOf(lastRequest())).toEqual({ acknowledgedAccountAvailable: true })
  })

  it('批处理上下文按 accountIds+fields 组装 payload', async () => {
    await accountsApi.batchEditContext(['a-1', 'a-2'], ['supportedModels', 'modelMappings'])
    expect(payloadOf(lastRequest())).toEqual({ accountIds: ['a-1', 'a-2'], fields: ['supportedModels', 'modelMappings'] })
  })

  it('test/testOptions/testTask 等长任务接口禁用超时并透传 signal', async () => {
    const controller = new AbortController()
    await accountsApi.test('a-1', { model: 'gpt-4o' } as never, undefined, { signal: controller.signal })
    expect(lastRequest().timeout).toBe(0)
    expect(lastRequest().signal).toBe(controller.signal)
    expect(payloadOf(lastRequest())).toEqual({ model: 'gpt-4o' })
    await accountsApi.testOptions('a-1')
    expect(lastRequest().timeout).toBe(0)
    await accountsApi.testTask('task-1')
    expect(lastRequest().timeout).toBe(0)
    await accountsApi.createTestSession()
    expect(lastRequest().timeout).toBe(0)
  })

  it('list 返回解包后的数据', async () => {
    installCaptureAdapter({ items: [{ id: 'a-1' }], total: 1 })
    await expect(accountsApi.list()).resolves.toEqual({ items: [{ id: 'a-1' }], total: 1 })
  })
})

describe('myAccountsApi 请求形状', () => {
  it('各方法命中 /my-accounts 前缀路径', async () => {
    await myAccountsApi.list()
    await myAccountsApi.tags()
    await myAccountsApi.deleteTag('tag-1')
    await myAccountsApi.editBasicDetail('a-1')
    await myAccountsApi.create({ name: 'x' })
    await myAccountsApi.update('a-1', { expectedConfigRevision: 1 })
    await myAccountsApi.delete('a-1')
    await myAccountsApi.test('a-1')
    await myAccountsApi.testTask('task-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-accounts'],
      ['GET', '/my-accounts/tags'],
      ['DELETE', '/my-accounts/tags/tag-1'],
      ['GET', '/my-accounts/a-1/edit-basic'],
      ['POST', '/my-accounts'],
      ['PATCH', '/my-accounts/a-1'],
      ['DELETE', '/my-accounts/a-1'],
      ['POST', '/my-accounts/a-1/test'],
      ['GET', '/my-accounts/test-tasks/task-1']
    ])
  })

  it('其余方法同样命中 /my-accounts 前缀的对应资源路径', async () => {
    await myAccountsApi.options()
    await myAccountsApi.advancedDetail('a-1')
    await myAccountsApi.cloneContext('a-1')
    await myAccountsApi.oauthReauthorizationContext('a-1')
    await myAccountsApi.apiKeyRuntime('a-1')
    await myAccountsApi.revalidateApiKeyRuntime('a-1', { expectedConfigRevision: 1 })
    await myAccountsApi.runtimeReset('a-1', { expectedConfigRevision: 1 })
    await myAccountsApi.export({ accountIds: ['a-1'] })
    await myAccountsApi.importPreview({ data: {} })
    await myAccountsApi.importConfirm({ data: {} })
    await myAccountsApi.lock('a-1')
    await myAccountsApi.unlock('a-1')
    await myAccountsApi.updateLockConfig('a-1', { lockDeathTimeoutSeconds: 60 })
    await myAccountsApi.forceActivate('a-1')
    await myAccountsApi.refreshBalance('a-1')
    await myAccountsApi.balanceDetails('a-1')
    await myAccountsApi.testBalanceDraft({ account: {} } as never)
    await myAccountsApi.refreshModelCatalog({ account: {} } as never)
    await myAccountsApi.batchEditContext(['a-1'], ['supportedModels'])
    await myAccountsApi.batchUpdate({ items: [] } as never)
    await myAccountsApi.updateTags('a-1', { tags: ['t'], expectedConfigRevision: 1 })
    await myAccountsApi.updateAuthorizedDispatch('a-1', { expectedConfigRevision: 1 })
    await myAccountsApi.testOptions('a-1')
    await myAccountsApi.bindGroup('a-1', { groupId: 'g-1', expectedConfigRevision: 1 })
    await myAccountsApi.migrateTraffic('a-1', { targetAccountId: 'a-2' })
    await myAccountsApi.testDraft({ model: 'gpt-4o' } as never)
    await myAccountsApi.createTestSession()
    await myAccountsApi.heartbeatTestSession('sess-1')
    await myAccountsApi.completeTestSession('sess-1')
    await myAccountsApi.cancelTestSession('sess-1')
    await myAccountsApi.cancelTestTask('task-1')
    await myAccountsApi.returnAuthorization('a-1')
    expect(requestShapes()).toEqual([
      ['GET', '/my-accounts/options'],
      ['GET', '/my-accounts/a-1/advanced'],
      ['GET', '/my-accounts/a-1/clone-context'],
      ['GET', '/my-accounts/a-1/oauth-reauthorization-context'],
      ['GET', '/my-accounts/a-1/api-key-runtime'],
      ['POST', '/my-accounts/a-1/api-key-runtime/revalidate'],
      ['POST', '/my-accounts/a-1/runtime-reset'],
      ['POST', '/my-accounts/export'],
      ['POST', '/my-accounts/import/preview'],
      ['POST', '/my-accounts/import/confirm'],
      ['POST', '/my-accounts/a-1/lock'],
      ['POST', '/my-accounts/a-1/unlock'],
      ['POST', '/my-accounts/a-1/lock-config'],
      ['POST', '/my-accounts/a-1/force-activate'],
      ['POST', '/my-accounts/a-1/balance/refresh'],
      ['GET', '/my-accounts/a-1/balance/details'],
      ['POST', '/my-accounts/balance/test-draft'],
      ['POST', '/my-accounts/model-catalog/refresh'],
      ['POST', '/my-accounts/batch-edit-context'],
      ['POST', '/my-accounts/batch-update'],
      ['PATCH', '/my-accounts/a-1/tags'],
      ['PATCH', '/my-accounts/a-1/authorized-dispatch'],
      ['GET', '/my-accounts/a-1/test-options'],
      ['POST', '/my-accounts/a-1/group'],
      ['POST', '/my-accounts/a-1/traffic-migration'],
      ['POST', '/my-accounts/test-draft'],
      ['POST', '/my-accounts/test-sessions'],
      ['POST', '/my-accounts/test-sessions/sess-1/heartbeat'],
      ['POST', '/my-accounts/test-sessions/sess-1/complete'],
      ['POST', '/my-accounts/test-sessions/sess-1/cancel'],
      ['POST', '/my-accounts/test-tasks/task-1/cancel'],
      ['POST', '/my-accounts/a-1/return-authorization']
    ])
  })

  it('个人视角 list 始终剥离 systemAccountId 参数', async () => {
    await myAccountsApi.list({ systemAccountId: 'sa-1', keyword: 'k', providerCode: 'all' })
    expect(lastRequest().params).toEqual({ keyword: 'k' })
  })

  it('个人视角 options 同样剥离 systemAccountId', async () => {
    await myAccountsApi.options({ systemAccountId: 'sa-1', limit: 5 })
    expect(lastRequest().params).toEqual({ limit: 5 })
  })

  it('个人视角运行时方法 payload 形状与管理面一致', async () => {
    await myAccountsApi.forceActivate('a-1')
    expect(payloadOf(lastRequest())).toEqual({ acknowledgedAccountAvailable: true })
    await myAccountsApi.lock('a-1')
    expect(payloadOf(lastRequest())).toEqual({})
  })
})
