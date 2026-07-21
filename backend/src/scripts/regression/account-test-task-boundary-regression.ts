import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const backendRoot = resolve('src')
const frontendRoot = resolve('..', 'frontend', 'src')

const dispatchRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-dispatch.routes.ts')
const sessionRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-session.routes.ts')
const statusRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-status.routes.ts')
const taskRepository = source(backendRoot, 'storage', 'account-test-tasks.repository.ts')
const postgresTaskRepository = taskRepository.slice(
  taskRepository.indexOf('export async function createAccountTestSessionAsync'),
  taskRepository.indexOf('function getAccountTestTaskRow(')
)
const testService = source(backendRoot, 'modules', 'accounts', 'account-test.service.ts')
const testOptionsService = source(backendRoot, 'modules', 'accounts', 'account-test-options.service.ts')
const gatewayRoutes = source(backendRoot, 'modules', 'gateway', 'routes.ts')
const failureDispatch = source(backendRoot, 'modules', 'gateway', 'response', 'failure-dispatch.ts')
const usageRepository = source(backendRoot, 'storage', 'usage-records.repository.ts')
const accountApi = source(frontendRoot, 'api', 'domains', 'accounts.ts')
const accountsView = source(frontendRoot, 'views', 'accounts', 'AccountsView.vue')
const accountTestModal = source(frontendRoot, 'views', 'accounts', 'useAccountTestModal.ts')
const accountTestModels = source(frontendRoot, 'views', 'accounts', 'useAccountTestModels.ts')
const accountTestModalComponent = source(frontendRoot, 'views', 'accounts', 'AccountTestModal.vue')
const accountTestRunSession = source(frontendRoot, 'views', 'accounts', 'accountTestRunSession.ts')
const accountEditSaveFlow = source(frontendRoot, 'views', 'accounts', 'useAccountEditSaveFlow.ts')
const batchToolbar = source(frontendRoot, 'views', 'accounts', 'AccountBatchToolbar.vue')

assert.match(
  dispatchRoutes,
  /router\.get\('\/:id\/test-options'/,
  '列表测试模型目录必须在打开具体账户测试时按需加载'
)
assert.match(
  dispatchRoutes,
  /findAccountManualTestListContextAsync\(req\.params\.id, requestAccess\)/,
  '列表测试选项必须读取最小可见账户上下文'
)
const listRouteSource = dispatchRoutes.slice(
  dispatchRoutes.indexOf("router.get('/:id/test-options'"),
  dispatchRoutes.indexOf("router.get('/:id/test-options/models/:modelId'")
)
assert.doesNotMatch(listRouteSource, /findAccountForTestAsync|credentials/, '列表测试选项不得读取或解密完整账户凭据')
assert.match(
  listRouteSource,
  /accountManualTestOptionsAsync\(account,\s*optionQuery\)/,
  '列表测试选项必须按最小账户上下文和规范化查询解析模型摘要'
)
const listOptionsFunctionSource = testOptionsService.slice(
  testOptionsService.indexOf('export async function accountManualTestOptionsAsync'),
  testOptionsService.indexOf('export async function resolveAccountManualTestSelectionAsync')
)
assert.match(listOptionsFunctionSource, /listProviderModelOptionRowsAsync/, '模型列表必须读取带窗口和已选项补齐的轻量模型目录投影')
assert.doesNotMatch(
  listOptionsFunctionSource,
  /listProviderModelCatalogAsync|accountManualTestEndpointModesForModel|accountManualTestEndpointModes\(/,
  '模型列表不得克隆完整目录或逐模型计算账户请求形态'
)
assert.match(
  dispatchRoutes,
  /router\.get\('\/:id\/test-options\/models\/:modelId'/,
  '切换测试模型时必须按模型 ID 独立加载请求形态'
)
assert.match(
  dispatchRoutes,
  /findAccountManualTestCapabilitiesContextAsync\(req\.params\.id, requestAccess\)/,
  '模型能力端点必须读取单行最小账户能力上下文'
)
assert.match(
  dispatchRoutes,
  /accountManualTestModelCapabilitiesAsync\(account, req\.params\.modelId\)/,
  '模型能力端点必须复用后端账户和模型能力计算'
)
const modelCapabilitiesFunctionSource = testOptionsService.slice(
  testOptionsService.indexOf('export async function accountManualTestModelCapabilitiesAsync'),
  testOptionsService.indexOf('export function accountManualTestEndpointModesForModel')
)
assert.match(modelCapabilitiesFunctionSource, /findProviderModelTestCatalogItemAsync/, '模型能力必须按 provider、owner 和 modelId 定点读取目录')
assert.doesNotMatch(modelCapabilitiesFunctionSource, /listProviderModelCatalogAsync/, '模型能力不得克隆完整模型目录')
assert.doesNotMatch(
  dispatchRoutes,
  /default-test-model|defaultTestModel/,
  '人工测试路由不得保存账户默认测试模型'
)
assert.match(
  dispatchRoutes,
  /accountTestSchema[\s\S]*accountSnapshot[\s\S]*savedAccountDraftTestSnapshotAsync/,
  '新增和编辑表单测试必须通过统一测试契约按需构建草稿账户快照'
)
assert.match(
  testService,
  /healthCheckModel/,
  '未显式选择人工测试模型时必须使用账户检查模型'
)
assert.match(
  testService,
  /disableAccountStateMutation/,
  '人工测试必须显式关闭账户运行态副作用'
)

assert.doesNotMatch(
  sessionRoutes,
  /AccountTestSessionConflictError/,
  'A/B 账户测试会话不得使用用户级全局冲突锁'
)
assert.doesNotMatch(
  statusRoutes,
  /test-sessions\/active/,
  '后端不得暴露用户级全局活动测试会话接口'
)
assert.doesNotMatch(
  taskRepository,
  /getActiveAccountTestSessionDetail/,
  '任务存储不得继续维护用户级全局活动会话查询'
)
assert.match(
  taskRepository,
  /账户测试会话只能包含一个账户任务/,
  '每个会话必须只承载一个账户任务，防止重新引入批量测试'
)
assert.doesNotMatch(
  postgresTaskRepository,
  /cancel_requested\s*=\s*[01]/,
  'PostgreSQL account test path must use boolean predicates after the Go schema upgrade'
)
assert.match(
  postgresTaskRepository,
  /cancel_requested\s*=\s*false/,
  'PostgreSQL runnable account test guards must use false'
)
assert.match(
  postgresTaskRepository,
  /cancel_requested\s*=\s*true/,
  'PostgreSQL cancellation writes must use true'
)

assert.match(
  gatewayRoutes,
  /disableAccountStateMutation/,
  '网关测试请求必须支持关闭账户状态写入'
)
assert.match(
  failureDispatch,
  /accountStateMutationEnabled !== false/,
  '人工测试失败不得写入账户、授权实例或 API Key 生产状态'
)
assert.match(
  usageRepository,
  /manual_account_test/,
  '人工测试使用记录必须与生产健康成功信号隔离'
)

assert.match(
  accountApi,
  /testOptions:/,
  '前端账户 API 必须提供按账户加载测试选项的方法'
)
assert.doesNotMatch(
  accountApi,
  /default-test-model|setDefaultTestModel/,
  '前端账户 API 不得保留人工测试模型持久化接口'
)
assert.match(
  accountTestModels,
  /loadTestModelOptions/,
  '列表测试弹窗必须提供候选模型按需加载入口'
)
assert.doesNotMatch(
  section(accountTestModal, 'function openTestModal', 'function openDraftTestModal'),
  /loadSavedAccountTestOptions|loadTestModelOptions/,
  '打开列表测试弹窗时不得请求候选模型列表'
)
assert.match(
  accountTestModalComponent,
  /@dropdown-visible-change/,
  '候选模型列表必须由模型选择器展开交互触发'
)
assert.match(
  accountTestModels,
  /useFixedTestModel/,
  '新增和编辑表单测试必须固定使用检查模型'
)
assert.doesNotMatch(
  accountTestModal,
  /SuccessfulDraftActivationTest|successfulDraftActivationTest|successfulSavedDraftUpdateTest/,
  '人工测试结果不得生成供账户保存消费的激活状态'
)
assert.doesNotMatch(
  accountEditSaveFlow,
  /activationTestTaskId|defaultTestModel|测试通过后再保存/,
  '新增和编辑保存不得依赖人工测试结果'
)
assert.match(
  accountTestRunSession,
  /accountId/,
  '测试恢复快照必须按账户隔离'
)
assert.doesNotMatch(
  batchToolbar,
  /批量测试/,
  '账户批量工具栏不得保留批量测试入口'
)
assert.doesNotMatch(
  accountsView,
  /batchTestSelected|openBatchTestModal/,
  '账户页不得保留批量测试入口或批量测试状态'
)

console.log('account-test-task-boundary-regression passed')

function source(root: string, ...segments: string[]): string {
  return readFileSync(resolve(root, ...segments), 'utf8')
}

function section(input: string, startMarker: string, endMarker: string): string {
  const start = input.indexOf(startMarker)
  const end = input.indexOf(endMarker, start)
  assert(start >= 0 && end > start, `无法提取源码片段：${startMarker} -> ${endMarker}`)
  return input.slice(start, end)
}
