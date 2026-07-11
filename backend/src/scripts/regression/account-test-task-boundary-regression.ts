import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const backendRoot = resolve('src')
const frontendRoot = resolve('..', 'frontend', 'src')

const dispatchRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-dispatch.routes.ts')
const sessionRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-session.routes.ts')
const statusRoutes = source(backendRoot, 'modules', 'accounts', 'account-test-status.routes.ts')
const taskRepository = source(backendRoot, 'storage', 'account-test-tasks.repository.ts')
const testService = source(backendRoot, 'modules', 'accounts', 'account-test.service.ts')
const gatewayRoutes = source(backendRoot, 'modules', 'gateway', 'routes.ts')
const failureDispatch = source(backendRoot, 'modules', 'gateway', 'response', 'failure-dispatch.ts')
const usageRepository = source(backendRoot, 'storage', 'usage-records.repository.ts')
const accountApi = source(frontendRoot, 'api', 'domains', 'accounts.ts')
const accountsView = source(frontendRoot, 'views', 'accounts', 'AccountsView.vue')
const accountTestModal = source(frontendRoot, 'views', 'accounts', 'useAccountTestModal.ts')
const accountTestModels = source(frontendRoot, 'views', 'accounts', 'useAccountTestModels.ts')
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
  /accountManualTestOptionsAsync\(account\)/,
  '列表测试选项必须由后端按账户供应商和用户作用域解析'
)
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
  /loadSavedAccountTestOptions/,
  '列表测试弹窗必须在打开时加载当前账户测试选项'
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
