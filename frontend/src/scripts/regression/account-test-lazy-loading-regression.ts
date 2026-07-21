import { strict as assert } from 'node:assert'

import { computed, createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

import { api } from '@/api/client'
import type {
  AccountTestModelCapabilities,
  AccountTestOptions
} from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import type { AccountSummary } from '@/types/domain'
import { invalidateAccountTestOptionsCache } from '@/views/accounts/accountTestOptionsCache'
import { useAccountTestModal } from '@/views/accounts/useAccountTestModal'

const originalManagementOptions = api.accounts.testOptions
const originalSelfOptions = api.myAccounts.testOptions
const originalManagementCapabilities = api.accounts.testModelCapabilities
const originalSelfCapabilities = api.myAccounts.testModelCapabilities

try {
  await verifyManagementLazyLoading()
  await verifySelfLazyLoading()
  await verifyPendingOptionsAbortOnAccountSwitch()
  await verifyPendingCapabilitiesAbortOnClose()
  console.log('账户测试模型按需加载行为回归通过')
} finally {
  api.accounts.testOptions = originalManagementOptions
  api.myAccounts.testOptions = originalSelfOptions
  api.accounts.testModelCapabilities = originalManagementCapabilities
  api.myAccounts.testModelCapabilities = originalSelfCapabilities
  authState.currentUser.value = undefined
  invalidateAccountTestOptionsCache()
}

async function verifyManagementLazyLoading(): Promise<void> {
  invalidateAccountTestOptionsCache()
  authState.currentUser.value = currentUser('lazy-admin')
  const account = accountFixture('management-account')
  const scopeParams = { systemAccountId: 'managed-owner' }
  let optionsCalls = 0
  const optionQueries: Array<{ keyword?: string; limit?: number; selectedIds?: string[] }> = []
  const capabilityCalls: Array<{ accountId: string; modelId: string; systemAccountId?: string }> = []
  api.accounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    optionQueries.push({ keyword: params?.keyword, limit: params?.limit, selectedIds: params?.selectedIds })
    assert.equal(accountId, account.id)
    assert.equal(params?.systemAccountId, scopeParams.systemAccountId)
    return testOptions()
  }
  api.accounts.testModelCapabilities = async (accountId, modelId, params) => {
    capabilityCalls.push({ accountId, modelId, systemAccountId: params?.systemAccountId })
    return modelCapabilities(modelId)
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => scopeParams),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '管理端首开测试弹窗不得请求候选模型列表')
  assert.equal(modal.testForm.model, account.healthCheckModel, '首开默认模型必须使用当前账户检查模型')
  assert.equal(modal.testForm.testEndpointMode, account.healthCheckEndpointMode, '首开默认请求形态必须使用当前账户检查形态')

  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '管理端首次展开模型选择器应请求一次候选列表')
  assert.deepEqual(optionQueries[0], {
    keyword: undefined,
    limit: 50,
    selectedIds: [account.healthCheckModel]
  }, '首次展开必须限制 50 条并保留当前账户检查模型')
  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '管理端重复展开已加载的模型选择器不得重复请求')

  modal.updateAccountTestModel('vendor/model-two')
  await waitFor(() => modal.testForm.testEndpointMode === 'chat_json', '管理端模型能力请求未更新请求形态')
  assert.deepEqual(capabilityCalls, [{
    accountId: account.id,
    modelId: 'vendor/model-two',
    systemAccountId: scopeParams.systemAccountId
  }], '管理端切换模型必须携带账户 ID、原始模型 ID 和管理范围读取能力')
  assert.equal(modal.testForm.testEndpointMode, 'chat_json', '模型能力响应必须更新当前测试请求形态')
  await modal.loadAccountTestModelOptions(true, '模型二')
  assert.equal(optionsCalls, 2, '搜索模型时应按关键词重新请求候选列表')
  assert.deepEqual(optionQueries[1], {
    keyword: '模型二',
    limit: 50,
    selectedIds: [account.healthCheckModel, 'vendor/model-two']
  }, '模型搜索必须同时保留账户检查模型和当前选中模型')
  await modal.loadAccountTestModelOptions(true, '模型二')
  assert.equal(optionsCalls, 2, '相同关键词与选中模型的重复搜索应复用缓存')
}

async function verifySelfLazyLoading(): Promise<void> {
  invalidateAccountTestOptionsCache()
  authState.currentUser.value = currentUser('lazy-user')
  const account = accountFixture('self-account')
  let optionsCalls = 0
  const optionQueries: Array<{ keyword?: string; limit?: number; selectedIds?: string[] }> = []
  const capabilityCalls: Array<{ accountId: string; modelId: string }> = []
  api.myAccounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    optionQueries.push({ keyword: params?.keyword, limit: params?.limit, selectedIds: params?.selectedIds })
    assert.equal(accountId, account.id)
    return testOptions()
  }
  api.myAccounts.testModelCapabilities = async (accountId, modelId) => {
    capabilityCalls.push({ accountId, modelId })
    return modelCapabilities(modelId)
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => undefined),
    isManagementView: computed(() => false)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '个人端首开测试弹窗不得请求候选模型列表')
  await modal.loadAccountTestModelOptions(true)
  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '个人端候选模型列表应在首次交互加载且重复展开不重复请求')
  assert.deepEqual(optionQueries[0], {
    keyword: undefined,
    limit: 50,
    selectedIds: [account.healthCheckModel]
  }, '个人端首次展开也必须使用轻量查询参数')

  modal.updateAccountTestModel('vendor/model-two')
  await waitFor(() => capabilityCalls.length === 1, '个人端模型能力请求未完成')
  assert.deepEqual(capabilityCalls, [{ accountId: account.id, modelId: 'vendor/model-two' }], '个人端模型能力请求必须使用个人 API 并携带模型 ID')
}

async function verifyPendingOptionsAbortOnAccountSwitch(): Promise<void> {
  invalidateAccountTestOptionsCache()
  authState.currentUser.value = currentUser('abort-options-user')
  const firstAccount = accountFixture('abort-options-first')
  const secondAccount = accountFixture('abort-options-second')
  let firstSignal: AbortSignal | undefined
  let firstAccountCalls = 0
  api.accounts.testOptions = async (accountId, _params, options) => {
    if (accountId === firstAccount.id) {
      firstAccountCalls += 1
      if (firstAccountCalls > 1) return testOptions()
      firstSignal = options?.signal
      return await rejectWhenAborted(options?.signal)
    }
    return testOptions()
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => ({ systemAccountId: 'abort-options-owner' })),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(firstAccount)
  const pending = modal.loadAccountTestModelOptions(true)
  await waitFor(() => Boolean(firstSignal), '候选模型请求未接收 AbortSignal')
  await modal.openTestModal(secondAccount)
  assert.equal(firstSignal?.aborted, true, '切换账户必须取消旧账户候选模型请求')
  await pending
  await modal.loadAccountTestModelOptions(true)
  assert.equal(modal.testForm.model, secondAccount.healthCheckModel, '旧账户请求不得覆盖新账户默认模型')
  modal.closeTestModal()
  await modal.openTestModal(firstAccount)
  await modal.loadAccountTestModelOptions(true)
  assert.equal(firstAccountCalls, 2, '已取消的候选模型请求不得写入缓存，重新打开必须重新请求')
}

async function verifyPendingCapabilitiesAbortOnClose(): Promise<void> {
  invalidateAccountTestOptionsCache()
  authState.currentUser.value = currentUser('abort-capabilities-user')
  const account = accountFixture('abort-capabilities-account')
  let capabilitySignal: AbortSignal | undefined
  let capabilityCalls = 0
  api.accounts.testOptions = async () => testOptions()
  api.accounts.testModelCapabilities = async (_accountId, modelId, _params, options) => {
    assert.equal(modelId, 'vendor/model-two')
    capabilityCalls += 1
    if (capabilityCalls > 1) return modelCapabilities(modelId)
    capabilitySignal = options?.signal
    return await rejectWhenAborted(options?.signal)
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => ({ systemAccountId: 'abort-capabilities-owner' })),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  await modal.loadAccountTestModelOptions(true)
  modal.updateAccountTestModel('vendor/model-two')
  await waitFor(() => Boolean(capabilitySignal), '模型能力请求未接收 AbortSignal')
  modal.closeTestModal()
  assert.equal(capabilitySignal?.aborted, true, '关闭弹窗必须取消进行中的模型能力请求')
  await modal.openTestModal(account)
  await modal.loadAccountTestModelOptions(true)
  modal.updateAccountTestModel('vendor/model-two')
  await waitFor(() => modal.testForm.testEndpointMode === 'chat_json', '重新打开后模型能力请求未完成')
  assert.equal(capabilityCalls, 2, '已取消的模型能力请求不得写入缓存，重新选择必须重新请求')
}

function testOptions(): AccountTestOptions {
  return [
    { id: 'vendor/model-one', name: '模型一' },
    { id: 'vendor/model-two', name: '模型二' }
  ]
}

function modelCapabilities(modelId: string): AccountTestModelCapabilities {
  return {
    id: modelId,
    name: modelId === 'vendor/model-two' ? '模型二' : '模型一',
    testEndpointModes: ['chat_json']
  }
}

function accountFixture(id: string): AccountSummary {
  return {
    id,
    configRevision: 1,
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: `按需加载账户 ${id}`,
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'codex_responses',
    healthCheckModel: 'vendor/model-one',
    healthCheckEndpointMode: 'responses_sse',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage()
  }
}

function currentUser(id: string) {
  return {
    id,
    username: id,
    displayName: id,
    role: 'admin' as const,
    mustChangePassword: false
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

async function createModalHarness(
  options: Parameters<typeof useAccountTestModal>[0]
): Promise<ReturnType<typeof useAccountTestModal>> {
  let modal: ReturnType<typeof useAccountTestModal> | undefined
  const app = createSSRApp({
    setup() {
      modal = useAccountTestModal(options)
      return () => h('div')
    }
  })
  await renderToString(app)
  assert(modal, '账户测试 modal composable 未在 SSR 测试组件中初始化')
  return modal
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (predicate()) return
    await Promise.resolve()
  }
  throw new Error(message)
}

async function rejectWhenAborted<T>(signal?: AbortSignal): Promise<T> {
  if (!signal) throw new Error('请求缺少 AbortSignal')
  if (signal.aborted) throw abortError()
  return await new Promise<T>((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(abortError()), { once: true })
  })
}

function abortError(): Error {
  const error = new Error('aborted')
  error.name = 'AbortError'
  return error
}
