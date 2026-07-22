import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountSummary } from '../../src/types/domain'
import { buildAccountTestPayload } from '../../src/views/accounts/accountTestFlow'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountApiPath = resolve(frontendRoot, 'src/api/domains/accounts.ts')
const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestModelsPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModels.ts')
const accountTestOptionsCachePath = resolve(frontendRoot, 'src/views/accounts/accountTestOptionsCache.ts')
const accountDetailCachePath = resolve(frontendRoot, 'src/views/accounts/accountDetailCache.ts')
const accountRemovalActionsPath = resolve(frontendRoot, 'src/views/accounts/useAccountRemovalActions.ts')
const accountTestComponentPath = resolve(frontendRoot, 'src/views/accounts/AccountTestModal.vue')
const accountTestFlowPath = resolve(frontendRoot, 'src/views/accounts/accountTestFlow.ts')
const accountTestRunSessionPath = resolve(frontendRoot, 'src/views/accounts/accountTestRunSession.ts')
const accountBatchToolbarPath = resolve(frontendRoot, 'src/views/accounts/AccountBatchToolbar.vue')
const accountBatchActionsPath = resolve(frontendRoot, 'src/views/accounts/useAccountBatchActions.ts')
const accountsViewPath = resolve(frontendRoot, 'src/views/accounts/AccountsView.vue')
const retiredSaveQueuePath = resolve(frontendRoot, 'src/views/accounts/accountDefaultTestModelSaveQueue.ts')

const accountApiSource = readFileSync(accountApiPath, 'utf8')
const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestModelsSource = readFileSync(accountTestModelsPath, 'utf8')
const accountTestOptionsCacheSource = readFileSync(accountTestOptionsCachePath, 'utf8')
const accountDetailCacheSource = readFileSync(accountDetailCachePath, 'utf8')
const accountRemovalActionsSource = readFileSync(accountRemovalActionsPath, 'utf8')
const accountTestComponentSource = readFileSync(accountTestComponentPath, 'utf8')
const accountTestFlowSource = readFileSync(accountTestFlowPath, 'utf8')
const accountTestRunSessionSource = readFileSync(accountTestRunSessionPath, 'utf8')
const accountBatchToolbarSource = readFileSync(accountBatchToolbarPath, 'utf8')
const accountBatchActionsSource = readFileSync(accountBatchActionsPath, 'utf8')
const accountsViewSource = readFileSync(accountsViewPath, 'utf8')
const accountManualTestModelOptionSource = sourceSection(
  accountApiSource,
  'export interface AccountManualTestModelOption',
  'export interface AccountTestModelCapabilities'
)
const openTestModalSource = sourceSection(
  accountTestModalSource,
  'async function openTestModal',
  'function openDraftTestModal'
)
const detachCurrentTestViewSource = sourceSection(
  accountTestModalSource,
  'function detachCurrentTestView',
  'function finishAccountTestRun'
)
const stopAccountTestSource = sourceSection(
  accountTestModalSource,
  'function stopAccountTest',
  'function closeTestModal'
)
const terminateAttachedTestRunSource = sourceSection(
  accountTestModalSource,
  'function terminateAttachedTestRun',
  'function stopAccountTest'
)
const closeTestModalSource = sourceSection(
  accountTestModalSource,
  'function closeTestModal',
  'onBeforeUnmount'
)
const updateSelectableTestModelSource = sourceSection(
  accountTestModelsSource,
  'function updateSelectableTestModel',
  'function resetTestModels'
)

assertIncludes(accountApiSource, '`/accounts/${id}/test-options`', '管理端列表测试应调用账户 test-options')
assertIncludes(accountApiSource, '`/my-accounts/${id}/test-options`', '个人端列表测试应调用个人账户 test-options')
assertIncludes(accountApiSource, 'export type AccountTestOptions = AccountManualTestModelOption[]', 'test-options 契约应只返回轻量模型摘要数组')
assertIncludes(accountManualTestModelOptionSource, 'id: string', 'test-options 模型摘要必须返回模型 ID')
assertIncludes(accountManualTestModelOptionSource, 'name: string', 'test-options 模型摘要必须返回展示名称')
assertNotIncludes(accountManualTestModelOptionSource, 'supportedApiProtocols', 'test-options 模型摘要不得返回支持协议数组')
assertNotIncludes(accountManualTestModelOptionSource, 'testEndpointModes', 'test-options 模型摘要不得返回请求形态数组')
assertNotIncludes(accountApiSource, 'defaultModel: string', 'test-options 不得重复返回账户默认模型')
assertNotIncludes(accountApiSource, 'defaultTestEndpointMode: AccountSupportedEndpointMode', 'test-options 不得重复返回账户默认请求形态')
assertIncludes(accountApiSource, 'testModelCapabilities', '模型切换应通过独立能力 API 按模型读取请求形态')
assertIncludes(accountApiSource, 'test-options/models/${encodeURIComponent(modelId)}', '模型能力 API 路径必须包含 URL 编码后的模型 ID')
assertNotIncludes(accountApiSource, 'healthCheckModel: string', '前端 test-options 契约不应猜测后端返回 healthCheckModel 字段')
assertNotIncludes(accountApiSource, 'setDefaultTestModel', '账户 API 不应保留人工测试成功后的默认模型写接口')
assertNotIncludes(accountApiSource, 'default-test-model', '账户 API 不应保留默认测试模型路径')

assertIncludes(accountTestModelsSource, 'loadAccountTestOptionsCached({', '保存账户测试应通过短时缓存加载 test-options')
assertIncludes(accountTestModelsSource, 'loadTestModelOptions', '候选模型列表必须提供独立的按需加载入口')
assertNotIncludes(openTestModalSource, 'loadSavedAccountTestOptions(', '打开测试弹窗时不得请求候选模型列表')
assertIncludes(openTestModalSource, 'account.healthCheckModel', '测试弹窗默认模型必须直接使用当前账户检查模型')
assertIncludes(openTestModalSource, 'account.healthCheckEndpointMode', '测试弹窗默认请求形态必须直接使用当前账户检查形态')
assertIncludes(accountTestComponentSource, '@dropdown-visible-change', '模型选择器首次展开时才应触发候选模型列表加载')
assertIncludes(accountTestOptionsCacheSource, 'api.accounts.testOptions(', '管理端测试模型应来自账户 test-options')
assertIncludes(accountTestOptionsCacheSource, 'api.myAccounts.testOptions(input.account.id, input.params, input.options)', '个人端测试模型应来自个人账户 test-options 并传递查询参数与取消信号')
assertIncludes(accountTestOptionsCacheSource, 'api.accounts.testModelCapabilities(', '管理端模型能力应走独立能力接口')
assertIncludes(accountTestOptionsCacheSource, 'api.myAccounts.testModelCapabilities(', '个人端模型能力应走独立能力接口')
assertIncludes(accountTestOptionsCacheSource, 'input.options?.signal', '测试选项缓存 loader 必须接收 AbortSignal')
assertIncludes(accountTestModelsSource, 'limit: 50', '测试模型下拉每次最多请求 50 条')
assertIncludes(accountTestModelsSource, 'selectedIds', '测试模型搜索必须保留检查模型和当前选中模型')
assertIncludes(accountTestComponentSource, "@search=\"$emit('search-model-options', $event)\"", '模型选择器搜索必须触发服务端按需加载')
assertIncludes(accountTestModelsSource, 'optionsAbortController?.abort()', '关闭或切换账户时必须取消候选模型请求')
assertIncludes(accountTestModelsSource, 'modelAbortController?.abort()', '关闭或切换账户时必须取消模型能力请求')
assertNotIncludes(accountTestOptionsCacheSource, 'pageData', '账户测试选项不得依赖已移除的页面缓存')
assertIncludes(accountTestOptionsCacheSource, 'createShortLivedRequestCache', '移除页面缓存后应使用有界短时请求缓存')
assertIncludes(accountTestOptionsCacheSource, 'accountTestModelCapabilitiesCache', '模型能力应与模型列表分开缓存')
assertIncludes(accountTestOptionsCacheSource, 'authState.currentUser.value', '账户测试缓存应按当前用户隔离')
assertIncludes(accountTestOptionsCacheSource, 'normalizedConfigRevision', '缺少有效配置版本时不应缓存账户测试选项')
assertIncludes(accountTestOptionsCacheSource, 'cacheGeneration', '缓存失效后旧请求不得回填当前代次')
assertIncludes(accountDetailCacheSource, 'invalidateAccountTestOptionsCache()', '账户写操作清理详情缓存时应同步清理测试选项')
assertIncludes(accountRemovalActionsSource, 'invalidateAccountTestOptionsCache()', '删除或归还账户后应清理测试选项')
assertIncludes(accountTestModelsSource, 'testModelCapabilities', '切换非默认模型时应按模型 ID 请求能力')
assertNotIncludes(accountTestModelsSource, 'supportedApiProtocols', '前端不得从轻量模型摘要读取支持协议数组')
assertIncludes(updateSelectableTestModelSource, 'testModelCapabilities', '切换模型必须按模型 ID 重新读取请求形态')
assertNotIncludes(accountTestModelsSource, 'accountTestEndpointModesForAccount', '保存账户测试不得从裁剪后的列表账户推导请求形态')
assertNotIncludes(accountTestModelsSource, 'endpointModesForProtocol', '模型协议标签不得决定保存账户可测试请求形态')
assertIncludes(accountTestModelsSource, 'let modelRequestToken = 0', '模型请求应使用独立 token 隔离旧结果')
assertIncludes(accountTestModelsSource, 'requestToken === modelRequestToken', '只有当前模型请求可以更新弹窗')
assertIncludes(accountTestModelsSource, 'testModelReadonly.value = true', '草稿测试应进入只读模型模式')
assertNotIncludes(accountTestModelsSource, 'supportedModels', '列表人工测试模型不应受账户 supportedModels 限制')
assertNotIncludes(accountTestModelsSource, 'api.providers.options', '列表人工测试不应再加载完整供应商选项')
assertNotIncludes(accountTestModelsSource, 'api.providers.models', '列表人工测试不应自行拼供应商模型目录')

assertIncludes(accountTestModalSource, 'fixedHealthCheckModel: string', '草稿测试入口应要求调用者传入固定检查模型')
assertIncludes(accountTestModalSource, 'useFixedTestModel(model, accountTestEndpointModesForAccount(account, draftPayload))', '草稿测试应固定使用调用者传入的检查模型')
assertIncludes(accountTestModalSource, 'let testViewToken = 0', '测试弹窗应使用视图 token 隔离 A/B 账户')
assertIncludes(accountTestModalSource, 'run.viewToken === testViewToken', '旧运行结果只能更新其绑定视图')
assertIncludes(accountTestModalSource, 'readAccountTestRunSession(options.isManagementView.value, account.id)', '单任务恢复应按账户读取 session 快照')
assertIncludes(accountTestModalSource, 'fetchAccountTestTask(run, snapshot.activeTask.id)', '单任务恢复应直接读取 task，不依赖用户级 active session')
assertNotIncludes(accountTestModalSource, 'fetchActiveAccountTestSession', '人工测试恢复不能依赖 user-global active session')
assertNotIncludes(accountTestModalSource, 'loadAccountDetailCached', '列表人工测试不能补拉完整账户详情')
assertNotIncludes(accountTestModalSource, 'accountDefaultTestModelSaveQueue', '人工测试不应持有默认模型保存队列')
assertNotIncludes(accountTestModalSource, 'setDefaultTestModel', '人工测试成功不应写账户默认模型')
assertNotIncludes(accountTestModalSource, 'options.loadData', '人工测试成功或失败不应刷新并修改列表账户状态')
assertNotIncludes(accountTestModalSource, 'openBatchTestModal', '测试 composable 不应保留批量测试入口')
assertNotIncludes(accountTestModalSource, 'runBatchAccountTest', '测试 composable 不应保留批量执行编排')
assertNotIncludes(accountTestModalSource, 'batchTestItems', '测试 composable 不应保留批量结果状态')
assertNotIncludes(accountTestModalSource, 'message.error(accountTestErrorMessage(account, result))', '人工测试失败结果已经进入终端，不应重复弹出全局错误 toast')
assertNotIncludes(accountTestModalSource, 'message.error(`${account.name}: 测试失败`)', '人工测试异常已经转换为终端结果，不应重复弹出全局错误 toast')
assertNotIncludes(accountTestModalSource, "message.warning('测试进度恢复中断，后台任务仍会继续执行')", '测试恢复异常也应进入弹窗终端，不应弹出全局 warning toast')
assert.equal(
  accountTestModalSource.match(/failedAccountTestResult\(\{/g)?.length,
  2,
  '运行异常和恢复异常都必须转换为终端 AccountTestResult；候选列表失败使用局部加载状态'
)

assertIncludes(detachCurrentTestViewSource, 'persistAccountTestRunSession(run, true)', '切换账户或组件卸载时应保留当前账户单任务快照')
assertIncludes(detachCurrentTestViewSource, 'run.controller.abort()', '分离视图应只终止当前前端轮询绑定')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestRunBackend', '分离视图不能取消后台 session 或 task')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestTaskRequest', '分离视图不能取消后台 task')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestSessionRequest', '分离视图不能取消后台 session')
assertIncludes(stopAccountTestSource, 'terminateAttachedTestRun(true)', '用户显式停止应进入完整终止流程')
assertIncludes(terminateAttachedTestRunSource, 'cancelAccountTestRunBackend(run)', '完整终止流程应取消当前后台运行')
assertIncludes(closeTestModalSource, 'terminateAttachedTestRun(true)', '关闭弹窗必须终止正在运行的后台任务')
assertIncludes(closeTestModalSource, 'detachCurrentTestView()', '没有运行任务时关闭弹窗仍应清理视图绑定')

assertIncludes(accountTestComponentSource, 'v-if="modelReadonly"', '草稿测试模型应使用只读控件展示')
assertIncludes(accountTestComponentSource, ':mask-closable="true"', '运行中也应允许关闭并分离当前测试视图')
assertNotIncludes(accountTestComponentSource, 'isBatchMode', '测试弹窗不应保留批量模式')
assertNotIncludes(accountTestComponentSource, 'batchItems', '测试弹窗不应保留批量结果')

assertNotIncludes(accountTestFlowSource, 'AccountTestMode', '测试流程不应保留批量模式类型')
assertNotIncludes(accountTestFlowSource, 'AccountBatchTestItem', '测试流程不应保留批量测试项')
assertNotIncludes(accountTestFlowSource, 'successfulAccountDefaultTestModel', '人工测试结果不应转换为账户默认模型')
assertNotIncludes(accountTestFlowSource, 'batchTestSummary', '测试流程不应保留批量摘要')

assertIncludes(accountTestRunSessionSource, 'accountId: string', '单任务快照读写应显式按账户隔离')
assertIncludes(accountTestRunSessionSource, 'encodeURIComponent(accountId)', '账户 ID 应进入独立安全存储键')
assertNotIncludes(accountTestRunSessionSource, 'batchTestingAccounts', 'session 快照不应保留批量账户')
assertNotIncludes(accountTestRunSessionSource, 'batchTestItems', 'session 快照不应保留批量结果')
assertNotIncludes(accountTestRunSessionSource, 'mode: AccountTestMode', 'session 快照不应保留批量模式')

assertNotIncludes(accountBatchToolbarSource, '批量测试', '账户批量工具栏不应显示批量测试入口')
assertNotIncludes(accountBatchToolbarSource, "(event: 'test')", '账户批量工具栏不应暴露测试事件')
assertNotIncludes(accountBatchActionsSource, 'batchTestSelected', '账户批量 action 不应保留测试动作')
assertNotIncludes(accountBatchActionsSource, 'openBatchTestModal', '账户批量 action 不应依赖测试弹窗')
assertNotIncludes(accountsViewSource, '@test="batchTestSelected"', '账户页不应绑定批量测试事件')
assertNotIncludes(accountsViewSource, ':batch-items=', '账户页不应向测试弹窗传批量结果')
assertNotIncludes(accountsViewSource, ':accounts="batchTestingAccounts"', '账户页不应向测试弹窗传批量账户')
assertIncludes(accountsViewSource, 'openDraftTestModalWithHealthCheckModel(', '账户页应作为调用者传入草稿检查模型')
assertIncludes(accountsViewSource, 'draftHealthCheckModel(draftPayload)', '账户页应从当前草稿提取固定检查模型')
assert.equal(existsSync(retiredSaveQueuePath), false, '账户默认测试模型保存队列文件应删除')

const account = accountFixture()
assert.deepEqual(
  buildAccountTestPayload({
    model: 'gpt-catalog-only',
    testEndpointMode: 'responses_sse'
  }, account),
  {
    model: 'gpt-catalog-only',
    testEndpointMode: 'responses_sse'
  },
  '列表人工测试应允许提交不在账户 supportedModels 中的 test-options 模型'
)

console.log('账户人工测试解耦回归通过：模型级请求形态、兼容回退、切换清理、草稿固定模型和运行隔离均符合预期')

function accountFixture(): AccountSummary {
  return {
    id: 'acct_manual_test_options',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '人工测试账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-supported-only'],
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage()
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

function sourceSection(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start)
  if (start < 0 || end < 0) {
    throw new Error(`无法提取源码片段：${startMarker} -> ${endMarker}`)
  }
  return source.slice(start, end)
}

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
