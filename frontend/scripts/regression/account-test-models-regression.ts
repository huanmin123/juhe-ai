import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountSummary, AccountUsageSummary, ProviderDefinition, ProviderModelPricing } from '../../src/types/domain'
import {
  buildTestModelOptions,
  defaultTestModelForAccountSelection
} from '../../src/views/accounts/accountDerivedState'
import { createAccountDefaultTestModelSaveQueue } from '../../src/views/accounts/accountDefaultTestModelSaveQueue'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { useResponsivePagedList } from '../../src/composables/useResponsivePagedList'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestModelsPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModels.ts')
const accountsViewPath = resolve(frontendRoot, 'src/views/accounts/AccountsView.vue')
const accountListDataPath = resolve(frontendRoot, 'src/views/accounts/useAccountListData.ts')
const accountDraftTestPayloadPath = resolve(frontendRoot, 'src/views/accounts/accountDraftTestPayload.ts')

const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestModelsSource = readFileSync(accountTestModelsPath, 'utf8')
const accountsViewSource = readFileSync(accountsViewPath, 'utf8')
const accountListDataSource = readFileSync(accountListDataPath, 'utf8')
const accountDraftTestPayloadSource = readFileSync(accountDraftTestPayloadPath, 'utf8')
const detachActiveAccountTestRunStart = accountTestModalSource.indexOf('function detachActiveAccountTestRun')
const detachActiveAccountTestRunSource = accountTestModalSource.slice(
  detachActiveAccountTestRunStart,
  accountTestModalSource.indexOf('function cancelAccountTestRun(', detachActiveAccountTestRunStart)
)

assertIncludes(accountTestModalSource, "import { useAccountTestModels } from './useAccountTestModels'", '账户测试弹窗应通过模型 composable 获取测试模型能力')
assertIncludes(accountTestModelsSource, 'export function useAccountTestModels', '模型 composable 应导出 useAccountTestModels')
assertIncludes(accountTestModelsSource, 'api.providers.models(requestProviderCode, requestScopeParams)', '模型 composable 应按当前测试账户作用域加载供应商模型列表')
assertIncludes(accountTestModelsSource, 'api.providers.options(requestScopeParams)', '模型 composable 应按当前测试账户作用域加载用户默认和系统默认')
assertIncludes(accountTestModalSource, 'accountDefaultTestModelSaveQueue.enqueue(account, normalizedModel, {', '账户默认模型保存应使用跨组件共享的按账户串行队列')
assertNotIncludes(accountTestModalSource, 'accountDefaultModelSaveRequestIds', '账户默认模型不能只靠请求 ID 忽略乱序响应，后端写入也必须串行')
assertIncludes(accountTestModalSource, "testMode.value !== 'single'", '批量测试切换模型不能覆盖每个账户的默认偏好')
assertIncludes(accountTestModalSource, 'draftTestMode.value', '草稿测试切换模型不能写入已保存账户偏好')
assertIncludes(accountTestModalSource, 'api.myAccounts.setDefaultTestModel(targetAccount.id, targetModel)', '用户侧账户弹窗应调用个人账户默认模型接口')
assertNotIncludes(accountTestModalSource, 'api.providers.setDefaultTestModel', '账户测试弹窗不能修改供应商级个人默认模型')
assertIncludes(accountTestModalSource, 'interface AccountTestRunContext', '账户测试弹窗应为每次测试创建独立运行上下文')
assertIncludes(accountTestModalSource, 'const run = beginAccountTestRun()', '单账户和批量测试应绑定当前运行上下文')
assertIncludes(accountTestModalSource, 'activeTestRun === run', '旧账户测试结果只能在仍是当前运行时更新界面')
assertIncludes(accountTestModalSource, 'detachActiveAccountTestRun()', '关闭或切换测试弹窗时应立即脱离旧运行')
assertIncludes(accountTestModalSource, 'activeTestRun = undefined', '关闭旧账户弹窗后应立即释放全局运行门闩')
assertBefore(
  detachActiveAccountTestRunSource,
  'activeTestRun = undefined',
  'cancelAccountTestRun(run)',
  '关闭旧弹窗时应先释放当前运行，再异步取消旧会话和任务'
)
assertIncludes(accountTestModalSource, 'if (!isActiveAccountTestRun(run)) return', '旧账户请求完成后不得覆盖新账户测试结果')
assertIncludes(accountTestModalSource, 'run.tasks.set(task.id, account)', '测试任务必须保存在各自运行上下文中，不能跨账户共享')
assertIncludes(accountTestModalSource, 'shouldApply: () => isActiveAccountTestRun(run)', '账户测试完成后的列表刷新必须绑定当前运行，旧 A 刷新不能覆盖 B')
assertIncludes(accountDraftTestPayloadSource, 'defaultTestModel: input.accountDetail?.defaultTestModel', '编辑已有账户的草稿测试摘要必须保留账户级默认测试模型')
assertNotIncludes(accountTestModalSource, 'const activeAccountTestTasks = new Map', '账户 A 和 B 不能共享全局活动任务集合')
assertNotIncludes(accountTestModalSource, 'let accountTestAbortController', '账户 A 和 B 不能共享单个全局 AbortController')
assertNotIncludes(accountTestModelsSource, 'if (testModelsLoading.value && testModelsLoadingProviderCode.value === requestKey) return', '同作用域 A/B 切换不能复用旧目标的模型加载快照')
assertNotIncludes(accountTestModelsSource, 'api.providers.setDefaultTestModel', '模型加载 composable 不能修改供应商级个人默认模型')
assertIncludes(accountTestModelsSource, 'buildTestModelOptions', '模型 composable 应负责构建测试模型选项')
assertIncludes(accountTestModelsSource, 'providerDefaultTestModelForAccountSelection', '模型 composable 应负责供应商默认测试模型推导')
assertIncludes(accountTestModelsSource, 'providerSystemDefaultTestModelForAccountSelection', '模型 composable 应保留系统协议档案默认模型')
assertIncludes(accountTestModelsSource, 'nextTestModel', '模型 composable 应负责测试模型回落选择')
assertIncludes(accountTestModelsSource, 'providerModelsRequestKey.value === requestKey', '模型 composable 缓存必须按供应商和系统账户共同校验')
assertIncludes(accountTestModelsSource, 'if (!providerCode)', '模型 composable 应在没有唯一供应商时停止加载模型目录')
assertIncludes(accountTestModelsSource, 'testTargetRequestKey.value === requestKey', '模型 composable 应按当前测试目标和系统账户校验请求是否仍有效')
assertIncludes(accountTestModalSource, 'function updateAccountTestModel(model: string)', '账户测试弹窗应区分用户手动切换模型和程序默认赋值')
assertIncludes(accountsViewSource, '@update:model="updateAccountTestModel"', '账户页应把测试模型选择事件交给持久化处理')
assertNotIncludes(accountsViewSource, 'v-model:model="testForm.model"', '账户页不应继续仅通过临时 v-model 保存测试模型')
assertIncludes(accountsViewSource, 'applyAccountDefaultTestModel,', '账户页应把账户级乐观更新回写列表')
assertIncludes(accountListDataSource, 'account.id === id', '账户列表更新默认测试模型时必须只命中当前账户 ID')
assertIncludes(accountListDataSource, 'accountDefaultTestModelOverrides.set(id, {', '账户列表应记录本地默认模型覆盖，避免旧列表响应覆盖刚保存的选择')
assertIncludes(accountListDataSource, 'transformItems:', '账户列表只能在有效响应应用阶段确认本地默认模型覆盖')
assertIncludes(accountListDataSource, 'nextAccounts.map(applyAccountDefaultTestModelOverride)', '账户列表响应应合并仍未被服务端确认的本地默认模型')
assertIncludes(accountListDataSource, 'api.providers.options(systemAccountId ? { systemAccountId } : undefined)', '账户页应按当前系统账户作用域加载 provider 默认测试模型')
assertIncludes(accountListDataSource, 'loadAccountOptions(systemAccountId, Boolean(_loadOptions?.forceOptions))', '账户页刷新数据时应同步刷新 provider 选项')

assertNotIncludes(accountTestModalSource, 'api.providers.models', '账户测试弹窗不应直接加载供应商模型列表')
assertNotIncludes(accountTestModalSource, 'ProviderModelPricing', '账户测试弹窗不应持有供应商模型列表类型')
assertNotIncludes(accountTestModalSource, 'providerModelsProviderCode', '账户测试弹窗不应持有供应商模型缓存归属状态')
assertNotIncludes(accountTestModalSource, 'buildTestModelOptions', '账户测试弹窗不应直接构建测试模型选项')
assertNotIncludes(accountTestModalSource, 'providerDefaultTestModelForAccountSelection', '账户测试弹窗不应直接推导供应商默认测试模型')
assertNotIncludes(accountTestModalSource, 'isGatewaySupportedTestSelection', '账户测试弹窗不应直接判断测试目标协议兼容')
assertNotIncludes(accountTestModalSource, 'nextTestModel', '账户测试弹窗不应直接处理测试模型回落')
assertNotIncludes(accountTestModalSource, 'GPT_VENDOR_CODE', '账户测试弹窗不应直接持有 OpenAI 默认供应商回落')
assertNotIncludes(accountTestModelsSource, 'GPT_VENDOR_CODE', '模型 composable 不应直接持有 GPT 供应商常量')
assertNotIncludes(accountTestModelsSource, 'preferredDefaultProviderCode', '模型 composable 不应在混合供应商选择时回落到默认供应商模型目录')

assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5')
  ], accountFixture(), 'gpt-5.4-mini')),
  ['gpt-5.4-mini', 'gpt-5.5'],
  '未限制模型的账户测试下拉应合并供应商默认模型和模型目录'
)

const limitedAccount = accountFixture({
  supportedModels: ['gpt-5.5', 'gpt-5.4'],
  defaultTestModel: 'gpt-5.4'
})
assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5'),
    providerModel('gpt-4.1')
  ], limitedAccount, 'gpt-5.5', 'gpt-5.4')),
  ['gpt-5.4', 'gpt-5.5'],
  '账户默认测试模型应在该账户支持模型列表中优先显示'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccount, 'gpt-5.5', 'gpt-5.4'),
  'gpt-5.4',
  '账户默认测试模型优先级应高于个人默认和系统默认'
)

const limitedAccountWithoutPreference = accountFixture({
  supportedModels: ['gpt-5.4', 'gpt-5.5']
})
assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-4.1')
  ], limitedAccountWithoutPreference, 'gpt-5.5', 'gpt-5.4')),
  ['gpt-5.5', 'gpt-5.4'],
  '账户没有偏好时应先使用当前用户默认，再使用系统默认'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccountWithoutPreference, 'gpt-5.5', 'gpt-5.4'),
  'gpt-5.5',
  '账户没有偏好时应使用当前用户默认测试模型'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccountWithoutPreference, 'gpt-user-unsupported', 'gpt-5.4'),
  'gpt-5.4',
  '个人默认不在账户支持列表时应继续使用系统协议档案默认'
)
assert.equal(
  defaultTestModelForAccountSelection(limitedAccountWithoutPreference, 'gpt-user-unsupported', 'gpt-system-unsupported'),
  'gpt-5.4',
  '账户、个人和系统默认均不可用时才使用账户支持模型首项兜底'
)

assertDeepEqual(
  defaultAccountForm('gpt', 'api_key', [
    providerFixture({
      defaultTestModel: 'gpt-personal-default',
      defaultSupportedModels: ['gpt-system-default', 'gpt-personal-default']
    })
  ]).supportedModels,
  ['gpt-personal-default', 'gpt-system-default'],
  '新建账户默认支持模型应优先包含当前用户的默认测试模型并去重'
)

assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('gpt-5.4-mini'),
    providerModel('gpt-5.5')
  ], [
    accountFixture({ id: 'acct_batch_limited_a', supportedModels: ['gpt-5.5', 'gpt-5.4'], defaultTestModel: 'gpt-5.5' }),
    accountFixture({ id: 'acct_batch_limited_b', supportedModels: ['gpt-5.5', 'gpt-4.1'], defaultTestModel: 'gpt-5.5' }),
    accountFixture({ id: 'acct_batch_unrestricted' })
  ], 'gpt-5.4-mini')),
  ['gpt-5.5'],
  '批量测试包含模型限制账户时，下拉应只展示所有受限账户共同支持的模型'
)

const accountA = accountFixture({
  id: 'acct_isolated_a',
  supportedModels: ['gpt-5.5', 'gpt-5.4'],
  defaultTestModel: 'gpt-5.4'
})
const accountB = accountFixture({
  id: 'acct_isolated_b',
  supportedModels: ['gpt-5.5', 'gpt-5.4'],
  defaultTestModel: 'gpt-5.5'
})
assert.equal(defaultTestModelForAccountSelection(accountA, 'gpt-5.5', 'gpt-5.5'), 'gpt-5.4', '账户 A 应读取自己的测试偏好')
assert.equal(defaultTestModelForAccountSelection(accountB, 'gpt-5.4', 'gpt-5.4'), 'gpt-5.5', '账户 B 应读取自己的测试偏好，不能共享账户 A 的值')
assert.equal(
  defaultTestModelForAccountSelection([accountA, accountB], 'gpt-5.5', 'gpt-5.4'),
  'gpt-5.5',
  '批量账户偏好不一致时不能任选某个账户偏好覆盖其他账户'
)

let resolveStalePage: ((value: {
  items: Array<{ id: string }>
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}) => void) | undefined
let staleTransformCount = 0
const delayedList = useResponsivePagedList<{ id: string }>({
  pageSize: 20,
  showTotal: (total) => String(total),
  transformItems: (items) => {
    staleTransformCount += 1
    return items
  },
  fetchPage: () => new Promise((resolvePromise) => {
    resolveStalePage = resolvePromise
  })
})
let staleRunActive = true
const staleLoad = delayedList.loadData({ shouldApply: () => staleRunActive })
staleRunActive = false
resolveStalePage?.({
  items: [{ id: 'stale-account-a' }],
  page: 1,
  pageSize: 20,
  total: 1,
  hasMore: false
})
assert.equal(await staleLoad, false, '旧账户测试运行结束后，其延迟列表请求应被丢弃')
assert.deepEqual(delayedList.items.value, [], '旧 A 的延迟列表结果不能覆盖当前 B 的账户状态')
assert.equal(staleTransformCount, 0, '失效响应不能执行会确认或删除本地覆盖的转换副作用')

const controlledListResolvers: Array<(value: {
  items: Array<{ id: string }>
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}) => void> = []
let controlledListFetchCount = 0
const controlledList = useResponsivePagedList<{ id: string }>({
  pageSize: 20,
  showTotal: (total) => String(total),
  requestSignature: () => 'same-account-list',
  fetchPage: () => {
    controlledListFetchCount += 1
    return new Promise((resolvePromise) => {
      controlledListResolvers.push(resolvePromise)
    })
  }
})
let accountARunActive = true
const accountALoad = controlledList.loadData({ shouldApply: () => accountARunActive })
const accountBLoad = controlledList.loadData({ shouldApply: () => true })
assert.equal(controlledListFetchCount, 2, '带运行身份守卫的 A/B 刷新不能复用同一个在途 Promise')
accountARunActive = false
controlledListResolvers[0]?.({
  items: [{ id: 'account-a' }],
  page: 1,
  pageSize: 20,
  total: 1,
  hasMore: false
})
controlledListResolvers[1]?.({
  items: [{ id: 'account-b' }],
  page: 1,
  pageSize: 20,
  total: 1,
  hasMore: false
})
assert.equal(await accountALoad, false, 'A 的旧刷新应失效')
assert.equal(await accountBLoad, true, 'B 应发起并应用自己的刷新')
assert.deepEqual(controlledList.items.value, [{ id: 'account-b' }], 'A 的旧刷新不能导致 B 没有列表结果')

const coalescedListResolvers: Array<(value: {
  items: Array<{ id: string }>
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}) => void> = []
let coalescedListFetchCount = 0
const coalescedList = useResponsivePagedList<{ id: string }>({
  pageSize: 20,
  showTotal: (total) => String(total),
  requestSignature: () => 'coalesced-account-list',
  fetchPage: () => {
    coalescedListFetchCount += 1
    return new Promise((resolvePromise) => {
      coalescedListResolvers.push(resolvePromise)
    })
  }
})
const oldOrdinaryLoad = coalescedList.loadData()
const guardedLoad = coalescedList.loadData({ shouldApply: () => false })
const newOrdinaryLoad = coalescedList.loadData()
assert.equal(coalescedListFetchCount, 3, '守卫请求使旧普通请求失效后，后续普通刷新必须重新发起请求')
coalescedListResolvers[0]?.({ items: [{ id: 'old' }], page: 1, pageSize: 20, total: 1, hasMore: false })
coalescedListResolvers[1]?.({ items: [{ id: 'guarded' }], page: 1, pageSize: 20, total: 1, hasMore: false })
coalescedListResolvers[2]?.({ items: [{ id: 'new' }], page: 1, pageSize: 20, total: 1, hasMore: false })
assert.equal(await oldOrdinaryLoad, false, '旧普通刷新应被后续请求失效')
assert.equal(await guardedLoad, false, '已失效守卫刷新不应应用')
assert.equal(await newOrdinaryLoad, true, '守卫请求之后的新普通刷新应独立应用')
assert.deepEqual(coalescedList.items.value, [{ id: 'new' }], '最终列表应来自新普通刷新')

const savedModels: string[] = []
const appliedModels: Array<string | undefined> = []
const pendingSaves: Array<{
  resolve: (model: string) => void
  reject: (error: Error) => void
}> = []
const saveQueue = createAccountDefaultTestModelSaveQueue({
  apply: (_accountId, model) => appliedModels.push(model),
  persist: async (_account, model) => await new Promise<string>((resolvePromise, rejectPromise) => {
    savedModels.push(model)
    pendingSaves.push({ resolve: resolvePromise, reject: rejectPromise })
  }),
  onLatestFailure: () => undefined
})
const queuedAccount = accountFixture({
  id: 'acct_serial_default_model',
  defaultTestModel: 'gpt-5.4',
  supportedModels: ['gpt-5.4', 'gpt-5.5', 'gpt-5.6']
})
saveQueue.enqueue(queuedAccount, 'gpt-5.5')
saveQueue.enqueue(queuedAccount, 'gpt-5.6')
assert.deepEqual(savedModels, ['gpt-5.5'], '同一账户快速切换时只能先发送第一笔写入')
pendingSaves[0]?.resolve('gpt-5.5')
await waitFor(() => savedModels.length === 2)
assert.deepEqual(savedModels, ['gpt-5.5', 'gpt-5.6'], '第一笔完成后才允许发送同账户最新选择')
pendingSaves[1]?.resolve('gpt-5.6')
await saveQueue.whenIdle(queuedAccount.id)
assert.equal(appliedModels.at(-1), 'gpt-5.6', '同一账户串行写入后，界面和后端应以最后选择为准')

const failedModels: Array<string | undefined> = []
const failedQueue = createAccountDefaultTestModelSaveQueue({
  apply: (_accountId, model) => failedModels.push(model),
  persist: async () => {
    throw new Error('mock save failed')
  },
  onLatestFailure: () => undefined
})
failedQueue.enqueue(queuedAccount, 'gpt-5.5')
await failedQueue.whenIdle(queuedAccount.id)
assert.equal(failedModels.at(-1), 'gpt-5.4', '最新选择保存失败时应回退最后已持久化模型')

const lostResponseSavedModels: string[] = []
const lostResponsePendingSaves: Array<{
  resolve: (model: string) => void
  reject: (error: Error) => void
}> = []
const lostResponseQueue = createAccountDefaultTestModelSaveQueue({
  apply: () => undefined,
  persist: async (_account, model) => await new Promise<string>((resolvePromise, rejectPromise) => {
    lostResponseSavedModels.push(model)
    lostResponsePendingSaves.push({ resolve: resolvePromise, reject: rejectPromise })
  }),
  onLatestFailure: () => undefined
})
lostResponseQueue.enqueue(queuedAccount, 'gpt-5.5')
lostResponseQueue.enqueue(queuedAccount, 'gpt-5.4')
lostResponsePendingSaves[0]?.reject(new Error('response lost after write'))
await waitFor(() => lostResponseSavedModels.length === 2)
assert.deepEqual(
  lostResponseSavedModels,
  ['gpt-5.5', 'gpt-5.4'],
  '被取代请求响应丢失时也必须补写最终选择，即使最终选择等于队列已知持久值'
)
lostResponsePendingSaves[1]?.resolve('gpt-5.4')
await lostResponseQueue.whenIdle(queuedAccount.id)

const sameModelLoopWrites: string[] = []
const sameModelLoopPending: Array<{
  resolve: (model: string) => void
  reject: (error: Error) => void
}> = []
const sameModelLoopQueue = createAccountDefaultTestModelSaveQueue({
  apply: () => undefined,
  persist: async (_account, model) => await new Promise<string>((resolvePromise, rejectPromise) => {
    sameModelLoopWrites.push(model)
    sameModelLoopPending.push({ resolve: resolvePromise, reject: rejectPromise })
  }),
  onLatestFailure: () => undefined
})
sameModelLoopQueue.enqueue(queuedAccount, 'gpt-5.5')
sameModelLoopQueue.enqueue(queuedAccount, 'gpt-5.6')
sameModelLoopQueue.enqueue(queuedAccount, 'gpt-5.5')
sameModelLoopPending[0]?.reject(new Error('old same-model request failed'))
await waitFor(() => sameModelLoopWrites.length === 2)
assert.deepEqual(
  sameModelLoopWrites,
  ['gpt-5.5', 'gpt-5.5'],
  'X→Y→X 时旧 X 请求失败也不能清除最后一次 X 意图，必须按选择版本补写'
)
sameModelLoopPending[1]?.resolve('gpt-5.5')
await sameModelLoopQueue.whenIdle(queuedAccount.id)

const componentRebuildEvents: string[] = []
let resolveComponentRebuildSave: ((model: string) => void) | undefined
const componentRebuildQueue = createAccountDefaultTestModelSaveQueue()
const unsubscribeOldComponent = componentRebuildQueue.subscribe((_accountId, model, phase) => {
  componentRebuildEvents.push(`old:${phase}:${model ?? ''}`)
})
componentRebuildQueue.enqueue(queuedAccount, 'gpt-5.5', {
  persist: async () => await new Promise<string>((resolvePromise) => {
    resolveComponentRebuildSave = resolvePromise
  }),
  onLatestFailure: () => undefined
})
unsubscribeOldComponent()
componentRebuildQueue.subscribe((_accountId, model, phase) => {
  componentRebuildEvents.push(`new:${phase}:${model ?? ''}`)
})
assert.equal(
  componentRebuildEvents.at(-1),
  'new:optimistic:gpt-5.5',
  '账户页重建后，新组件应立即接管并重放在途账户选择'
)
resolveComponentRebuildSave?.('gpt-5.5')
await componentRebuildQueue.whenIdle(queuedAccount.id)
assert.equal(
  componentRebuildEvents.at(-1),
  'new:persisted:gpt-5.5',
  '旧组件卸载后，保存完成结果必须继续同步到新组件'
)

console.log('账户测试模型回归通过：账户偏好隔离、用户和系统默认回退、弹窗持久化边界正确')

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}

function assertBefore(source: string, first: string, second: string, message: string): void {
  const firstIndex = source.indexOf(first)
  const secondIndex = source.indexOf(second)
  if (firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex) {
    throw new Error(`${message}，预期 ${first} 位于 ${second} 之前`)
  }
}

async function waitFor(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (predicate()) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 0))
  }
  throw new Error('等待异步回归条件超时')
}

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'acct_test_models',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '测试模型账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function providerModel(model: string): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model,
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false
  }
}

function providerFixture(overrides: Partial<ProviderDefinition> = {}): ProviderDefinition {
  return {
    id: 'provider_gpt',
    code: 'gpt',
    name: 'OpenAI',
    enabled: true,
    defaultProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.openai.com/v1',
    defaultTestModel: 'gpt-system-default',
    defaultSupportedModels: ['gpt-system-default'],
    accountTypes: ['api_key'],
    capabilities: [],
    protocolProfiles: [{
      id: 'gpt-openai-v1',
      providerCode: 'gpt',
      name: 'OpenAI v1',
      enabled: true,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      baseUrl: 'https://api.openai.com/v1',
      defaultTestModel: 'gpt-system-default',
      accountTypes: ['api_key'],
      capabilities: [],
      endpointFamilies: []
    }],
    ...overrides
  }
}

function emptyUsage(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function optionValues(options: Array<{ value: string }>): string[] {
  return options.map((option) => option.value)
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
}
