import assert from 'node:assert/strict'

import { computed, createApp } from 'vue'
import { routeLocationKey } from 'vue-router'

import { api, pageDataApi } from '../../api/client.js'
import type { PageDataConfirmRequest, PageDataConfirmResult } from '../../api/domains/pageData.js'
import { authState } from '../../composables/useAuth.js'
import { message } from '../../lib/antd.js'
import type { AccountSummary, ProviderDefinition } from '../../types/domain/index.js'
import { useAccountListData } from '../../views/accounts/useAccountListData.js'
import { accountProxyDisplay } from '../../views/accounts/accountProxyDisplay.js'

const mutableApi = api as unknown as {
  providers: { options: (...args: unknown[]) => Promise<ProviderDefinition[]> }
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

const originalProviderOptions = mutableApi.providers.options
const originalProxyOptions = mutableApi.proxies.options
const originalAccountList = mutableApi.myAccounts.list
const originalConfirm = pageDataApi.confirm
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
  const confirmDomains: string[][] = []
  pageDataApi.confirm = async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    confirmDomains.push(Object.keys(request.domains).sort())
    return confirmResult(request)
  }
  let resolveProviderOptions: ((value: ProviderDefinition[]) => void) | undefined
  let listStarted = false
  let proxyOptionCalls = 0
  mutableApi.providers.options = () => new Promise((resolve) => {
    resolveProviderOptions = resolve
  })
  mutableApi.proxies.options = async () => {
    proxyOptionCalls += 1
    return []
  }
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
  assert.equal(proxyOptionCalls, 0, '账户列表加载不得预拉代理 options')

  resolveProviderOptions?.([])
  assert.equal(await firstLoad, true)
  assert.equal(listData.accounts.value[0]?.name, '并行账户')

  const responseProxy = accountProxyDisplay({
    ...accountFixture('account_proxy_display', '代理展示账户'),
    proxyProfileId: 'proxy_from_response',
    proxyProfileName: '响应代理',
    proxyProfileType: 'http',
    proxyProfileEnabled: true
  }, { id: 'proxy_from_cache', name: '缓存代理', type: 'socks5', enabled: true })
  assert.equal(responseProxy?.id, 'proxy_from_response', '账户行代理展示字段必须优先于旧标签缓存')
  assert.equal(responseProxy?.name, '响应代理')
  const hiddenProxy = accountProxyDisplay({
    ...accountFixture('account_hidden_proxy', '不可见代理账户'),
    proxyProfileId: 'proxy_disabled',
    proxyProfileUnavailable: true
  }, { id: 'proxy_disabled', name: '不应泄露的停用代理', type: 'http', enabled: false })
  assert.equal(hiddenProxy, undefined, '普通用户不可见代理不得由旧标签缓存重新泄露')

  mutableApi.providers.options = async () => {
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
  mutableApi.providers.options = async () => {
    refreshProviderCalls += 1
    return []
  }
  await listData.loadAccountOptions(undefined, true)
  const confirmCountBeforeRefresh = confirmDomains.length
  const providerCallsBeforeRefresh = refreshProviderCalls
  const accountCallsBeforeRefresh = refreshAccountCalls
  listData.refreshData()
  await waitFor(() => refreshAccountCalls > accountCallsBeforeRefresh, '手动刷新未发起账户列表请求')
  await flushPromises()
  assert.equal(refreshProviderCalls, providerCallsBeforeRefresh, '手动刷新列表不应失效并重查供应商筛选项')
  assert.deepEqual(confirmDomains.slice(confirmCountBeforeRefresh), [['accounts.static']], '手动刷新只应为账户列表执行一次轻量确认')
} finally {
  mutableApi.providers.options = originalProviderOptions
  mutableApi.proxies.options = originalProxyOptions
  mutableApi.myAccounts.list = originalAccountList
  pageDataApi.confirm = originalConfirm
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

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return
    await flushPromises()
  }
  throw new Error(message)
}

function confirmResult(request: PageDataConfirmRequest): PageDataConfirmResult {
  return {
    serverTime: '2026-07-19T12:00:00.000Z',
    domains: Object.fromEntries(Object.entries(request.domains).map(([domain, known]) => [domain, {
      action: known ? 'unchanged' : 'reload',
      token: known ?? {
        protocolVersion: 2,
        epoch: 'account-refresh-epoch',
        scope: `scope:${request.viewScope}:${request.targetSystemAccountId ?? 'self'}`,
        domain,
        sequence: 1,
        resetSequence: 0
      }
    }])) as PageDataConfirmResult['domains']
  }
}
