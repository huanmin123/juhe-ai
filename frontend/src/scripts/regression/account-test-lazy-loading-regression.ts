import { strict as assert } from 'node:assert'

import { computed, createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

import { api } from '@/api/client'
import type { AccountTestOptions } from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import type { AccountSummary } from '@/types/domain'
import { useAccountTestModal } from '@/views/accounts/useAccountTestModal'

const originalManagementOptions = api.accounts.testOptions
const originalSelfOptions = api.myAccounts.testOptions

try {
  await verifyManagementLazyLoading()
  await verifySelfLazyLoading()
  await verifyServerFilteredSelectedModel()
  await verifyPendingOptionsAbortOnAccountSwitch()
  console.log('账户测试弹窗按需模型加载与请求形态联动回归通过')
} finally {
  api.accounts.testOptions = originalManagementOptions
  api.myAccounts.testOptions = originalSelfOptions
  authState.currentUser.value = undefined
}

async function verifyManagementLazyLoading(): Promise<void> {
  authState.currentUser.value = currentUser('lazy-admin')
  const account = accountFixture('management-account')
  const scopeParams = { systemAccountId: 'managed-owner' }
  let optionsCalls = 0
  api.accounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    assert.equal(accountId, account.id)
    assert.equal(params?.systemAccountId, scopeParams.systemAccountId)
    return testOptions()
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => scopeParams),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '管理端首开测试弹窗不得预取模型选项')
  assert.equal(modal.testForm.model, account.healthCheckModel, '首开默认模型必须使用当前账户检查模型')
  assert.equal(modal.testForm.testEndpointMode, account.healthCheckEndpointMode, '首开默认请求形态必须使用账户摘要中的检查形态')

  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '展开模型下拉应按需加载一次模型选项')
  assert.deepEqual(modal.testEndpointModes.value, ['images_json'])
  assert.equal(modal.testForm.testEndpointMode, 'images_json', '模型 options 加载后必须提交 Images API')

  modal.updateAccountTestModel('vendor/model-two')
  assert.deepEqual(modal.testEndpointModes.value, ['chat_json'])
  assert.equal(modal.testForm.testEndpointMode, 'chat_json', '切换模型必须立即选中该模型首个可用请求形态')
  assert.equal(optionsCalls, 1, '切换模型不得发起第二个模型能力请求')
}

async function verifySelfLazyLoading(): Promise<void> {
  authState.currentUser.value = currentUser('lazy-user')
  const account = accountFixture('self-account')
  let optionsCalls = 0
  api.myAccounts.testOptions = async (accountId) => {
    optionsCalls += 1
    assert.equal(accountId, account.id)
    return testOptions()
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => undefined),
    isManagementView: computed(() => false)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '个人端首开测试弹窗不得预取模型选项')
  await modal.loadAccountTestModelOptions(true)
  await modal.loadAccountTestModelOptions(true)
  assert.equal(optionsCalls, 1, '个人端重复展开模型选择器不得重复请求')
  modal.updateAccountTestModel('vendor/model-two')
  assert.equal(modal.testForm.testEndpointMode, 'chat_json', '个人端切换模型也必须立即联动请求形态')
}

async function verifyServerFilteredSelectedModel(): Promise<void> {
  authState.currentUser.value = currentUser('filtered-options-admin')
  const account = accountFixture('server-filtered-account')
  let optionsCalls = 0
  api.accounts.testOptions = async (accountId, params) => {
    optionsCalls += 1
    assert.equal(accountId, account.id)
    assert(params?.selectedIds?.includes(account.healthCheckModel), 'selectedIds 只能请求服务端保留当前检查模型')
    return [testOptions()[1]!]
  }

  const modal = await createModalHarness({
    accountScopeParams: computed(() => ({ systemAccountId: 'filtered-options-owner' })),
    isManagementView: computed(() => true)
  })
  await modal.openTestModal(account)
  assert.equal(optionsCalls, 0, '服务端筛选场景首开不得预取测试模型')
  await modal.loadAccountTestModelOptions(true)

  assert.equal(optionsCalls, 1, '服务端筛掉当前模型后仍只读取一次模型 options')
  assert.deepEqual(
    modal.testModelOptions.value.map((option) => option.value),
    ['vendor/model-two'],
    '前端不得用旧请求形态合成插回服务端已经过滤的模型'
  )
  assert.equal(modal.testForm.model, 'vendor/model-two', '服务端过滤当前模型后应回退到其返回的首个有效模型')
  assert.deepEqual(modal.testEndpointModes.value, ['chat_json'])
  assert.equal(modal.testForm.testEndpointMode, 'chat_json', '回退模型必须使用服务端声明的请求形态')
}

async function verifyPendingOptionsAbortOnAccountSwitch(): Promise<void> {
  authState.currentUser.value = currentUser('abort-options-user')
  const firstAccount = accountFixture('abort-options-first')
  const secondAccount = accountFixture('abort-options-second')
  let firstSignal: AbortSignal | undefined
  api.accounts.testOptions = async (accountId, _params, options) => {
    if (accountId === firstAccount.id) {
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
  const pendingOptions = modal.loadAccountTestModelOptions(true)
  await waitFor(() => Boolean(firstSignal), '候选模型请求未接收 AbortSignal')
  await modal.openTestModal(secondAccount)
  await pendingOptions
  assert.equal(firstSignal?.aborted, true, '切换账户必须取消旧账户候选模型请求')
  assert.equal(modal.testForm.model, secondAccount.healthCheckModel, '旧账户请求不得覆盖新账户默认模型')
}

function testOptions(): AccountTestOptions {
  return [
    {
      id: 'gpt-image-2',
      name: '图片模型',
      testEndpointModes: ['images_json']
    },
    {
      id: 'vendor/model-two',
      name: '模型二',
      testEndpointModes: ['chat_json']
    }
  ]
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
    healthCheckModel: 'gpt-image-2',
    healthCheckEndpointMode: 'responses_sse',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage()
  }
}

function currentUser(id: string) {
  return { id, username: id, displayName: id, role: 'admin' as const, mustChangePassword: false }
}

function emptyUsage() {
  return {
    requestCount: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheReadCost: 0,
    cacheWriteTokens: 0, cacheWrite1hTokens: 0, cacheWriteCost: 0, thinkingTokens: 0,
    inputImageTokens: 0, outputImageTokens: 0, totalTokens: 0, totalCost: 0
  }
}

async function createModalHarness(options: Parameters<typeof useAccountTestModal>[0]): Promise<ReturnType<typeof useAccountTestModal>> {
  let modal: ReturnType<typeof useAccountTestModal> | undefined
  const app = createSSRApp({ setup() { modal = useAccountTestModal(options); return () => h('div') } })
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
  return await new Promise<T>((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
  })
}
