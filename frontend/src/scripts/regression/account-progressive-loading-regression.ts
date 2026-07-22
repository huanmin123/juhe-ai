import assert from 'node:assert/strict'

import { computed, createApp } from 'vue'
import { routeLocationKey } from 'vue-router'

import { api } from '../../api/client.js'
import { authState } from '../../composables/useAuth.js'
import { message } from '../../lib/antd.js'
import type { AccountSummary, ProviderDefinition } from '../../types/domain/index.js'
import { useAccountListData } from '../../views/accounts/useAccountListData.js'

const mutableApi = api as unknown as {
  providers: { definitions: (...args: unknown[]) => Promise<ProviderDefinition[]> }
  proxies: { options: (...args: unknown[]) => Promise<never[]> }
  myAccounts: { list: (...args: unknown[]) => Promise<AccountListPage> }
}

interface AccountListPage {
  items: AccountSummary[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
  runtimeSnapshot?: {
    accountRuntimeAvailabilityAvailable?: boolean
  }
}

const originalProviderDefinitions = mutableApi.providers.definitions
const originalProxyOptions = mutableApi.proxies.options
const originalAccountList = mutableApi.myAccounts.list
const originalMessageError = message.error
const originalConsoleError = console.error
const originalConsoleWarn = console.warn

const optionErrors: string[] = []
message.error = ((content: unknown) => {
  optionErrors.push(String(content))
  return () => undefined
}) as typeof message.error
console.error = () => undefined
console.warn = () => undefined

try {
  authState.currentUser.value = {
    id: 'account-refresh-admin',
    username: 'account-refresh-admin',
    displayName: '账户刷新管理员',
    role: 'admin',
    mustChangePassword: false
  }
  let resolveProviderOptions: ((value: ProviderDefinition[]) => void) | undefined
  let listStarted = false
  mutableApi.providers.definitions = () => new Promise((resolve) => {
    resolveProviderOptions = resolve
  })
  mutableApi.proxies.options = async () => []
  mutableApi.myAccounts.list = async () => {
    listStarted = true
    return accountPage(accountFixture('account_parallel', '并行账户'))
  }

  const listData = await createListData()
  const firstLoad = listData.loadData()
  await waitFor(() => listStarted, '账户列表未在 options 完成前发起')
  await waitFor(() => listData.accounts.value[0]?.name === '并行账户', 'options 未完成时账户列表结果未及时可见')

  assert.equal(listStarted, true, '账户列表必须在 provider / proxy options 完成前发起')
  assert.equal(listData.accounts.value[0]?.name, '并行账户', 'options 尚未完成时列表结果必须已经可见')

  resolveProviderOptions?.([])
  assert.equal(await firstLoad, true)
  assert.equal(listData.accounts.value[0]?.name, '并行账户')

  mutableApi.providers.definitions = async () => {
    throw new Error('provider options unavailable')
  }
  mutableApi.myAccounts.list = async () => accountPage(accountFixture('account_options_failed', '选项失败后账户'))

  const loadedAfterOptionsFailure = await listData.loadData({ forceOptions: true, forceData: true })
  await flushPromises()

  assert.equal(loadedAfterOptionsFailure, true, 'options 失败不能让账户列表加载失败')
  assert.equal(listData.accounts.value[0]?.id, 'account_options_failed', 'options 失败不能清空或阻断新列表结果')
  assert.equal(listData.accounts.value[0]?.permissions?.canEdit, true, '渐进加载不能删除操作权限字段')
  assert.equal(optionErrors.at(-1), '加载账户筛选选项失败', 'options 失败必须显示独立的中文错误')

  const current = listData.accounts.value[0]
  assert(current)
  assert.equal(listData.updateLoadedAccount({ ...current, name: '账户仍可操作' }), true)
  assert.equal(listData.accounts.value[0]?.name, '账户仍可操作', 'options 失败后行级操作仍应可用')

  let refreshAccountCalls = 0
  let refreshProviderCalls = 0
  mutableApi.myAccounts.list = async () => {
    refreshAccountCalls += 1
    return accountPage(accountFixture('account_refresh', '手动刷新账户'))
  }
  mutableApi.providers.definitions = async () => {
    refreshProviderCalls += 1
    return []
  }
  await listData.loadAccountOptions(undefined, true)
  const providerCallsBeforeRefresh = refreshProviderCalls
  const accountCallsBeforeRefresh = refreshAccountCalls
  listData.refreshData()
  await waitFor(() => refreshAccountCalls > accountCallsBeforeRefresh, '手动刷新未发起账户列表请求')
  await flushPromises()
  assert.equal(refreshProviderCalls, providerCallsBeforeRefresh, '手动刷新列表不应失效并重查供应商筛选项')

  const oldProviders = deferred<ProviderDefinition[]>()
  const newProviders = deferred<ProviderDefinition[]>()
  const oldProxies = deferred<Array<{ id: string; name: string }>>()
  const newProxies = deferred<Array<{ id: string; name: string }>>()
  let providerRaceCalls = 0
  let proxyRaceCalls = 0
  mutableApi.providers.definitions = () => (++providerRaceCalls === 1 ? oldProviders.promise : newProviders.promise)
  mutableApi.proxies.options = () => (++proxyRaceCalls === 1 ? oldProxies.promise : newProxies.promise) as Promise<never[]>
  const oldOptionsLoad = listData.loadAccountOptions(undefined, true)
  const newOptionsLoad = listData.loadAccountOptions(undefined, true)
  newProviders.resolve([providerFixture('new-provider')])
  newProxies.resolve([{ id: 'proxy-new', name: '新代理' }])
  await newOptionsLoad
  oldProviders.resolve([providerFixture('old-provider')])
  oldProxies.resolve([{ id: 'proxy-old', name: '旧代理' }])
  await oldOptionsLoad
  assert.equal(listData.providers.value[0]?.code, 'new-provider', '旧强制 options 响应不得覆盖较新的供应商响应')
  assert.equal(listData.proxies.value[0]?.id, 'proxy-new', '旧强制 options 响应不得覆盖较新的代理响应')
} finally {
  mutableApi.providers.definitions = originalProviderDefinitions
  mutableApi.proxies.options = originalProxyOptions
  mutableApi.myAccounts.list = originalAccountList
  authState.currentUser.value = undefined
  message.error = originalMessageError
  console.error = originalConsoleError
  console.warn = originalConsoleWarn
}

console.log('账户渐进加载回归通过：列表不等待 options，options 失败独立提示且不影响列表与行级操作')

async function createListData(): Promise<ReturnType<typeof useAccountListData>> {
  const app = createApp({ render: () => null })
  app.provide(routeLocationKey, { path: '/my-accounts' } as never)
  return app.runWithContext(() => useAccountListData({
    isManagementView: computed(() => false),
    scopedSystemAccountId: () => undefined
  }))
}

function accountPage(account: AccountSummary): AccountListPage {
  return {
    items: [account],
    page: 1,
    pageSize: 20,
    total: 1,
    hasMore: false,
    runtimeSnapshot: {
      accountRuntimeAvailabilityAvailable: true
    }
  }
}

function accountFixture(id: string, name: string): AccountSummary {
  return {
    id,
    name,
    providerCode: 'openai',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 5,
    currentConcurrency: 1,
    currentConcurrencyAvailable: true,
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
    priority: 10,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckModel: 'gpt-5.2',
    healthCheckEndpointMode: 'responses_json',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    notes: '列表字段必须完整保留',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: true,
      canViewCredentials: true
    }
  }
}

function emptyUsage() {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

async function flushPromises(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

function providerFixture(code: string): ProviderDefinition {
  return { id: code, code, name: code } as ProviderDefinition
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return
    await flushPromises()
  }
  throw new Error(message)
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}
