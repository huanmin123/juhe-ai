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
const accountTestComponentSource = readFileSync(accountTestComponentPath, 'utf8')
const accountTestFlowSource = readFileSync(accountTestFlowPath, 'utf8')
const accountTestRunSessionSource = readFileSync(accountTestRunSessionPath, 'utf8')
const accountBatchToolbarSource = readFileSync(accountBatchToolbarPath, 'utf8')
const accountBatchActionsSource = readFileSync(accountBatchActionsPath, 'utf8')
const accountsViewSource = readFileSync(accountsViewPath, 'utf8')
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

assertIncludes(accountApiSource, '`/accounts/${id}/test-options`', '管理端列表测试应调用账户 test-options')
assertIncludes(accountApiSource, '`/my-accounts/${id}/test-options`', '个人端列表测试应调用个人账户 test-options')
assertIncludes(accountApiSource, 'defaultModel: string', 'test-options 契约应返回默认模型')
assertIncludes(accountApiSource, 'models: AccountManualTestModelOption[]', 'test-options 契约应返回按模型划分的选项')
assertIncludes(accountApiSource, 'supportedApiProtocols: ProviderModelApiProtocol[]', 'test-options 模型应携带支持协议')
assertIncludes(accountApiSource, 'testEndpointModes: AccountSupportedEndpointMode[]', 'test-options 契约应返回完整账户可测试请求形态')
assertIncludes(accountApiSource, 'defaultTestEndpointMode: AccountSupportedEndpointMode', 'test-options 契约应返回稳定默认请求形态')
assertNotIncludes(accountApiSource, 'healthCheckModel: string', '前端 test-options 契约不应猜测后端返回 healthCheckModel 字段')
assertNotIncludes(accountApiSource, 'setDefaultTestModel', '账户 API 不应保留人工测试成功后的默认模型写接口')
assertNotIncludes(accountApiSource, 'default-test-model', '账户 API 不应保留默认测试模型路径')

assertIncludes(accountTestModelsSource, 'api.accounts.testOptions(', '管理端测试模型应来自账户 test-options')
assertIncludes(accountTestModelsSource, 'api.myAccounts.testOptions(account.id)', '个人端测试模型应来自个人账户 test-options')
assertIncludes(accountTestModelsSource, 'response.defaultModel.trim()', '列表测试默认模型应使用 test-options 返回默认模型')
assertIncludes(accountTestModelsSource, 'normalizeModelOptions(response.models)', '列表测试下拉应直接使用 test-options 返回模型')
assertIncludes(accountTestModelsSource, 'option.supportedApiProtocols', '列表模型应保留 test-options 返回的支持协议')
assertIncludes(accountTestModelsSource, 'normalizeEndpointModes(response.testEndpointModes)', '保存账户请求形态应直接采用 test-options 返回值')
assertIncludes(accountTestModelsSource, 'prioritizeAccountTestEndpointModes(', '保存账户请求形态应按账户精确健康检查 mode 排序')
assertIncludes(accountTestModelsSource, "input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'", '保存账户默认请求形态应使用账户健康检查精确 mode')
assertNotIncludes(accountTestModelsSource, 'accountTestEndpointModesForAccount', '保存账户测试不得从裁剪后的列表账户推导请求形态')
assertNotIncludes(accountTestModelsSource, 'endpointModesForModel', '保存账户测试不得把请求形态与模型协议标签取交集')
assertNotIncludes(accountTestModelsSource, 'endpointModesForProtocol', '模型协议标签不得决定保存账户可测试请求形态')
assertNotIncludes(accountTestModelsSource, 'refreshSelectableEndpointModes', '切换模型不得重新计算或隐藏账户请求形态')
assertIncludes(accountTestModelsSource, 'let modelRequestToken = 0', '模型请求应使用独立 token 隔离旧结果')
assertIncludes(accountTestModelsSource, 'requestToken === modelRequestToken', '只有当前模型请求可以更新弹窗')
assertIncludes(accountTestModelsSource, 'testModelReadonly.value = true', '草稿测试应进入只读模型模式')
assertNotIncludes(accountTestModelsSource, 'supportedModels', '列表人工测试模型不应受账户 supportedModels 限制')
assertNotIncludes(accountTestModelsSource, 'api.providers.options', '列表人工测试不应再加载完整供应商选项')
assertNotIncludes(accountTestModelsSource, 'api.providers.models', '列表人工测试不应自行拼供应商模型目录')

assertIncludes(accountTestModalSource, 'const testOptions = await loadSavedAccountTestOptions(account)', '列表点击测试后才应加载最小 test-options')
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

assertIncludes(detachCurrentTestViewSource, 'persistAccountTestRunSession(run, true)', '关闭视图前应保留当前账户单任务快照')
assertIncludes(detachCurrentTestViewSource, 'run.controller.abort()', '关闭视图应只终止当前前端轮询绑定')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestRunBackend', '关闭视图不能取消后台 session 或 task')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestTaskRequest', '关闭视图不能取消后台 task')
assertNotIncludes(detachCurrentTestViewSource, 'cancelAccountTestSessionRequest', '关闭视图不能取消后台 session')
assertIncludes(stopAccountTestSource, 'cancelAccountTestRunBackend(run)', '用户显式停止时才应取消当前后台运行')

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

console.log('账户人工测试解耦回归通过：按需选项、草稿固定模型、A/B 隔离、关闭不取消和批量删除均符合预期')

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
