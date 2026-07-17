import assert from 'node:assert/strict'

import { computed, createApp } from 'vue'
import { routeLocationKey } from 'vue-router'

import { api } from '../../api/client.js'
import { message } from '../../lib/antd.js'
import type { AccountSummary, ProviderDefinition } from '../../types/domain/index.js'
import { useAccountListData } from '../../views/accounts/useAccountListData.js'

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
  let resolveProviderOptions: ((value: ProviderDefinition[]) => void) | undefined
  let listStarted = false
  mutableApi.providers.options = () => new Promise((resolve) => {
    resolveProviderOptions = resolve
  })
  mutableApi.proxies.options = async () => []
  mutableApi.myAccounts.list = async () => {
    listStarted = true
    return accountPage(accountFixture('account_parallel', '并行账户'))
  }

  const listData = await createListData()
  const firstLoad = listData.loadData()
  await flushPromises()

  assert.equal(listStarted, true, '账户列表必须在 provider / proxy options 完成前发起')
  assert.equal(listData.accounts.value[0]?.name, '并行账户', 'options 尚未完成时列表结果必须已经可见')

  resolveProviderOptions?.([])
  assert.equal(await firstLoad, true)
  assert.equal(listData.accounts.value[0]?.name, '并行账户')

  mutableApi.providers.options = async () => {
    throw new Error('provider options unavailable')
  }
  mutableApi.myAccounts.list = async () => accountPage(accountFixture('account_options_failed', '选项失败后账户'))

  const loadedAfterOptionsFailure = await listData.loadData({ forceOptions: true })
  await flushPromises()

  assert.equal(loadedAfterOptionsFailure, true, 'options 失败不能让账户列表加载失败')
  assert.equal(listData.accounts.value[0]?.id, 'account_options_failed', 'options 失败不能清空或阻断新列表结果')
  assert.equal(listData.accounts.value[0]?.permissions?.canEdit, true, '渐进加载不能删除操作权限字段')
  assert.equal(optionErrors.at(-1), '加载账户筛选选项失败', 'options 失败必须显示独立的中文错误')

  const current = listData.accounts.value[0]
  assert(current)
  assert.equal(listData.updateLoadedAccount({ ...current, name: '账户仍可操作' }), true)
  assert.equal(listData.accounts.value[0]?.name, '账户仍可操作', 'options 失败后行级操作仍应可用')
} finally {
  mutableApi.providers.options = originalProviderOptions
  mutableApi.proxies.options = originalProxyOptions
  mutableApi.myAccounts.list = originalAccountList
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
