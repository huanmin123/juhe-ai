import { strict as assert } from 'node:assert'

import { computed, createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

import { api } from '@/api/client'
import type { AccountTestModelCapabilities, AccountTestOptions } from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import type { AccountSummary } from '@/types/domain'
import { useAccountTestModal } from '@/views/accounts/useAccountTestModal'

const originalManagementOptions = api.accounts.testOptions
const originalSelfOptions = api.myAccounts.testOptions
const originalManagementCapabilities = api.accounts.testModelCapabilities
const originalSelfCapabilities = api.myAccounts.testModelCapabilities

try {
  await verifyManagementLazyLoading()
  await verifySelfLazyLoading()
  await verifyPendingOptionsAbortOnAccountSwitch()
  await verifyPendingCapabilitiesAbortOnAccountSwitch()
  await verifyPendingCapabilitiesAbortOnModelSwitch()
  console.log('账户测试弹窗两级按需加载与切换绑定行为回归通过')
} finally {
  api.accounts.testOptions = originalManagementOptions
  api.myAccounts.testOptions = originalSelfOptions
  api.accounts.testModelCapabilities = originalManagementCapabilities
  api.myAccounts.testModelCapabilities = originalSelfCapabilities
  authState.currentUser.value = undefined
}

async function verifyManagementLazyLoading(): Promise<void> {
  authState.currentUser.value = currentUser('lazy-admin')
  const account = accountFixture('management-account')
  const scopeParams = { systemAccountId: 'managed-owner' }
  let optionsCalls = 0
  let capabilitiesCalls = 0
  const optionQueries: Array<{ keyword?: string; limit?: number; selectedIds?: string[] }> = []
  api.accounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    optionQueries.push({ keyword: params?.keyword, limit: params?.limit, selectedIds: params?.selectedIds })
    assert.equal(accountId, account.id)
    assert.equal(params?.systemAccountId, scopeParams.systemAccountId)
    return testOptions()
  }
  api.accounts.testModelCapabilities = async (accountId, modelId, params) => {
    capabilitiesCalls += 1
    assert.equal(accountId, account.id)
    assert.ok([account.healthCheckModel, 'vendor/model-two'].includes(modelId))
    assert.equal(params?.systemAccountId, scopeParams.systemAccountId)
    return modelCapabilities(modelId, modelId === account.healthCheckModel
      ? ['responses_json', 'responses_sse']
      : ['chat_json'])
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => scopeParams),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '管理端首开测试弹窗不得请求候选模型列表')
  assert.equal(capabilitiesCalls, 0, '管理端首开测试弹窗不得请求当前模型能力')
  assert.equal(modal.testForm.model, account.healthCheckModel, '首开默认模型必须使用当前账户检查模型')
  assert.equal(modal.testForm.testEndpointMode, account.healthCheckEndpointMode, '首开默认请求形态必须直接使用账户列表字段')

  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(optionsCalls, 0, '展开请求形态下拉不得加载完整模型列表')
  assert.equal(capabilitiesCalls, 1, '展开请求形态下拉应只定点加载当前模型能力')
  assert.deepEqual(modal.testEndpointModes.value, ['responses_json', 'responses_sse'])
  assert.equal(modal.testForm.testEndpointMode, account.healthCheckEndpointMode, '能力补齐后应保留仍然有效的账户默认请求形态')
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 1, '重复展开已加载的请求形态下拉不得重复请求')

  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '管理端展开已加载的模型选择器不得重复请求')
  assert.deepEqual(optionQueries[0], {
    keyword: undefined,
    limit: 50,
    selectedIds: [account.healthCheckModel]
  }, '首次展开必须限制 50 条并保留当前账户检查模型')
  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '管理端重复展开已加载的模型选择器不得重复请求')

  modal.updateAccountTestModel('vendor/model-two')
  assert.equal(modal.testForm.testEndpointMode, 'account_default', '切换模型必须清空前一模型的请求形态')
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 2, '展开新模型的请求形态下拉必须定点读取该模型能力')
  assert.deepEqual(modal.testEndpointModes.value, ['chat_json'])
  modal.updateAccountTestModel(account.healthCheckModel)
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 2, '同一账户版本与模型的能力结果必须复用会话缓存')
  await modal.loadAccountTestModelOptions(true, '模型二')
  assert.equal(optionsCalls, 2, '搜索模型时应按关键词重新请求候选列表')
  assert.deepEqual(optionQueries[1], {
    keyword: '模型二',
    limit: 50,
    selectedIds: [account.healthCheckModel]
  }, '模型搜索必须保留账户默认检查模型和当前选中模型')
  await modal.loadAccountTestModelOptions(true, '模型二')
  assert.equal(optionsCalls, 2, '相同关键词与选中模型的重复搜索应复用缓存')

  modal.closeTestModal()
  await modal.openTestModal(account)
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 2, '重开同一账户版本的测试弹窗必须复用能力会话缓存')
  const revisedAccount = { ...account, configRevision: (account.configRevision ?? 0) + 1 }
  modal.closeTestModal()
  await modal.openTestModal(revisedAccount)
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 3, '账户版本变化后必须重新读取当前模型能力')
}

async function verifySelfLazyLoading(): Promise<void> {
  authState.currentUser.value = currentUser('lazy-user')
  const account = accountFixture('self-account')
  let optionsCalls = 0
  let capabilitiesCalls = 0
  const optionQueries: Array<{ keyword?: string; limit?: number; selectedIds?: string[] }> = []
  api.myAccounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    optionQueries.push({ keyword: params?.keyword, limit: params?.limit, selectedIds: params?.selectedIds })
    assert.equal(accountId, account.id)
    return testOptions()
  }
  api.myAccounts.testModelCapabilities = async (accountId, modelId) => {
    capabilitiesCalls += 1
    assert.equal(accountId, account.id)
    assert.ok([account.healthCheckModel, 'vendor/model-two'].includes(modelId))
    return modelCapabilities(modelId, modelId === account.healthCheckModel
      ? ['responses_json', 'responses_sse']
      : ['chat_json'])
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => undefined),
    isManagementView: computed(() => false)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '个人端首开测试弹窗不得请求候选模型列表')
  assert.equal(capabilitiesCalls, 0, '个人端首开测试弹窗不得请求当前模型能力')
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(optionsCalls, 0, '个人端展开请求形态下拉不得加载完整模型列表')
  assert.equal(capabilitiesCalls, 1, '个人端展开请求形态下拉应定点加载当前模型能力')
  await modal.loadAccountTestModelOptions(true)
  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '个人端候选模型列表应在首次交互加载且重复展开不重复请求')
  assert.deepEqual(optionQueries[0], {
    keyword: undefined,
    limit: 50,
    selectedIds: [account.healthCheckModel]
  }, '个人端首次展开也必须使用轻量查询参数')

  modal.updateAccountTestModel('vendor/model-two')
  assert.equal(modal.testForm.testEndpointMode, 'account_default', '个人端切换模型必须清空前一模型请求形态')
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(capabilitiesCalls, 2, '个人端展开新模型请求形态时必须读取定点能力详情')
}

async function verifyPendingOptionsAbortOnAccountSwitch(): Promise<void> {
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
  const firstRequest = modal.loadAccountTestModelOptions(true)
  await waitFor(() => Boolean(firstSignal), '候选模型请求未接收 AbortSignal')
  await modal.openTestModal(secondAccount)
  await firstRequest
  assert.equal(firstSignal?.aborted, true, '切换账户必须取消旧账户候选模型请求')
  assert.equal(modal.testForm.model, secondAccount.healthCheckModel, '旧账户请求不得覆盖新账户默认模型')
  modal.closeTestModal()
  await modal.openTestModal(firstAccount)
  await modal.loadAccountTestModelOptions(true)
  assert.equal(firstAccountCalls, 2, '已取消的候选模型请求不得标记为已完成，重新打开必须重新请求')
}

async function verifyPendingCapabilitiesAbortOnAccountSwitch(): Promise<void> {
  authState.currentUser.value = currentUser('abort-capabilities-user')
  const firstAccount = accountFixture('abort-capabilities-first')
  const secondAccount = accountFixture('abort-capabilities-second')
  let firstSignal: AbortSignal | undefined
  let firstAccountCalls = 0
  api.accounts.testModelCapabilities = async (accountId, modelId, _params, options) => {
    assert.equal(modelId, firstAccount.healthCheckModel)
    if (accountId === firstAccount.id) {
      firstAccountCalls += 1
      if (firstAccountCalls > 1) return modelCapabilities(modelId, ['responses_sse'])
      firstSignal = options?.signal
      return await rejectWhenAborted(options?.signal)
    }
    return modelCapabilities(modelId, ['responses_sse'])
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => ({ systemAccountId: 'abort-capabilities-owner' })),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(firstAccount)
  const firstRequest = modal.loadAccountTestEndpointModeOptions(true)
  await waitFor(() => Boolean(firstSignal), '当前模型能力请求未接收 AbortSignal')
  await modal.openTestModal(secondAccount)
  await firstRequest
  assert.equal(firstSignal?.aborted, true, '切换账户必须取消旧账户当前模型能力请求')
  assert.equal(modal.testForm.model, secondAccount.healthCheckModel, '旧模型能力响应不得覆盖新账户默认模型')
  modal.closeTestModal()
  await modal.openTestModal(firstAccount)
  await modal.loadAccountTestEndpointModeOptions(true)
  assert.equal(firstAccountCalls, 2, '已取消的能力请求不得标记为已完成，重新展开必须重新请求')
}

async function verifyPendingCapabilitiesAbortOnModelSwitch(): Promise<void> {
  authState.currentUser.value = currentUser('abort-capabilities-model-user')
  const account = accountFixture('abort-capabilities-model-account')
  let pendingSignal: AbortSignal | undefined
  api.accounts.testOptions = async () => testOptions()
  api.accounts.testModelCapabilities = async (_accountId, modelId, _params, options) => {
    assert.equal(modelId, account.healthCheckModel)
    pendingSignal = options?.signal
    return await rejectWhenAborted(options?.signal)
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => ({ systemAccountId: 'abort-capabilities-model-owner' })),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  const pendingRequest = modal.loadAccountTestEndpointModeOptions(true)
  await waitFor(() => Boolean(pendingSignal), '切换模型场景的能力请求未接收 AbortSignal')
  await modal.loadAccountTestModelOptions(true)
  modal.updateAccountTestModel('vendor/model-two')
  await pendingRequest

  assert.equal(pendingSignal?.aborted, true, '切换模型必须取消旧模型能力请求')
  assert.equal(modal.testEndpointModesLoading.value, false, '切换模型后不得遗留旧能力请求的加载态')
  assert.equal(modal.testForm.model, 'vendor/model-two', '旧模型能力响应不得覆盖新模型')
  assert.equal(modal.testForm.testEndpointMode, 'account_default', '切换模型后不得保留候选目录以外的能力状态')
}

function testOptions(): AccountTestOptions {
  return [
    {
      id: 'vendor/model-one',
      name: '模型一'
    },
    {
      id: 'vendor/model-two',
      name: '模型二'
    }
  ]
}

function modelCapabilities(model: string, testEndpointModes: AccountTestModelCapabilities['testEndpointModes']): AccountTestModelCapabilities {
  return {
    id: model,
    name: model,
    supportedApiProtocols: ['responses'],
    testEndpointModes
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
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 0))
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

function abortError(): DOMException {
  return new DOMException('aborted', 'AbortError')
}
