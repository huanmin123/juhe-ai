import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendSrc = resolve(currentDir, '../..')
const projectRoot = resolve(backendSrc, '../..')

const accountsRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/accounts.routes.ts'), 'utf8')
const accountDetailRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-detail.routes.ts'), 'utf8')
const accountTestDispatchRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-dispatch.routes.ts'), 'utf8')
const accountTestSessionRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-session.routes.ts'), 'utf8')
const accountTestStatusRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-status.routes.ts'), 'utf8')
const accountTestTaskQueueSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-task-queue.service.ts'), 'utf8')
const accountTestTaskRepositorySource = readFileSync(resolve(backendSrc, 'storage/account-test-tasks.repository.ts'), 'utf8')
const workerSource = readFileSync(resolve(backendSrc, 'worker.ts'), 'utf8')
const backgroundIpcSource = readFileSync(resolve(backendSrc, 'modules/background/background-ipc.ts'), 'utf8')
const frontendAccountTestModalSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/useAccountTestModal.ts'), 'utf8')
const frontendAccountTestTaskPollingSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/accountTestTaskPolling.ts'), 'utf8')
const frontendAccountTestSessionClientSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/accountTestSessionClient.ts'), 'utf8')
const frontendAccountBatchExecutionSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/accountBatchExecution.ts'), 'utf8')
const frontendAccountTestModalComponentSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/AccountTestModal.vue'), 'utf8')
const frontendAccountTestDisplayFormattersSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/accountTestDisplayFormatters.ts'), 'utf8')
const runOpenAIAccountTestWithSideEffectsSource = accountTestTaskQueueSource.slice(
  accountTestTaskQueueSource.indexOf('async function runOpenAIAccountTestWithSideEffects'),
  accountTestTaskQueueSource.indexOf('function enqueueManualAccountTestFailurePrecheck')
)

assert.equal(
  accountTestDispatchRoutesSource.includes('testOpenAIAccount('),
  false,
  '账户路由不应直接等待 OpenAI 测试，应只创建后台任务'
)
assert(
  accountTestDispatchRoutesSource.includes('createAccountTestTask({'),
  'POST /accounts/:id/test 应创建账号测试任务'
)
assert(
  accountTestDispatchRoutesSource.includes('dispatchAccountTestTasks([task.id])'),
  'POST /accounts/:id/test 应把测试任务投递到后台 worker'
)
assert(
  accountTestDispatchRoutesSource.includes('res.status(202).json(ok(task))'),
  'POST /accounts/:id/test 应返回 202 和任务对象，而不是同步测试结果'
)
assert(
  accountsRoutesSource.includes('registerAccountTestDispatchRoutes(accountsRouter)'),
  '账户主路由应注册账号测试调度子路由'
)
assert(
  accountTestDispatchRoutesSource.includes("router.post('/:id/test'")
    && accountTestDispatchRoutesSource.includes('parseRequestScopeQuery(req.query)')
    && accountTestDispatchRoutesSource.includes("res.status(403).json({ message: '缺少系统账户上下文' })")
    && accountTestDispatchRoutesSource.includes('accountTestSchema.safeParse(req.body)')
    && accountTestDispatchRoutesSource.includes('findAccountForTest(req.params.id, requestAccess)')
    && accountTestDispatchRoutesSource.includes('isOpenAIProtocolProfile(account)')
    && accountTestDispatchRoutesSource.includes('accountTestUnavailableMessage(account)')
    && accountTestDispatchRoutesSource.includes("isAdminRole(requestAccess?.role) || account.accessType !== 'authorized' ? 'full' : 'limited'")
    && accountTestDispatchRoutesSource.includes('savedAccountDraftTestSnapshot(account, accountSnapshot, requestAccess)')
    && accountTestDispatchRoutesSource.includes("failAccountTestTask(task.id, '后台 worker 暂不可用，账号测试任务未能投递')")
    && accountTestDispatchRoutesSource.includes("res.status(503).json({ message: '后台 worker 暂不可用，账号测试任务未能投递' })"),
  '账号测试调度子路由应保留 scope、权限、协议校验、诊断范围、草稿快照、任务失败和 503 边界'
)
assert(
  accountsRoutesSource.includes("accountsRouter.post('/test-draft'"),
  '应提供创建 / 编辑账户弹窗使用的草稿测试任务接口'
)
assert(
  accountsRoutesSource.includes('draftAccount')
    && accountsRoutesSource.includes('createAccountTestTask({'),
  '草稿测试接口应创建携带草稿快照的后台任务'
)

const taskReadRouteIndex = accountTestStatusRoutesSource.indexOf("router.get('/test-tasks/:taskId'")
const draftTestRouteIndex = accountsRoutesSource.indexOf("accountsRouter.post('/test-draft'")
const accountReadRouteIndex = accountsRoutesSource.indexOf('registerAccountDetailRoutes(accountsRouter)')
const accountCreateRouteIndex = accountsRoutesSource.indexOf("accountsRouter.post('/',")
const accountImportRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountImportRoutes(accountsRouter)')
const accountTrafficMigrationRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountTrafficMigrationRoutes(accountsRouter)')
const accountGroupBindingRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountGroupBindingRoutes(accountsRouter)')
const accountTestSessionRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountTestSessionRoutes(accountsRouter)')
const accountTestStatusRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountTestStatusRoutes(accountsRouter)')
const accountTestDispatchRouteRegistrationIndex = accountsRoutesSource.indexOf('registerAccountTestDispatchRoutes(accountsRouter)')
assert(taskReadRouteIndex >= 0, '应提供账号测试任务查询接口')
assert(draftTestRouteIndex >= 0, '应提供账号草稿测试任务创建接口')
assert(accountReadRouteIndex >= 0, '账户主路由应注册账号详情子路由')
assert(accountCreateRouteIndex >= 0, '应保留账号创建接口')
assert.equal(
  accountsRoutesSource.includes("accountsRouter.get('/:id'"),
  false,
  '账号详情 GET /:id 不应继续内联在账户主路由文件中'
)
assert(
  accountDetailRoutesSource.includes("router.get('/:id'")
    && accountDetailRoutesSource.includes('findAccountSummary')
    && accountDetailRoutesSource.includes('findAccountForTest')
    && accountDetailRoutesSource.includes('applyServerAccountRuntimeToAccount'),
  '账号详情子路由应保留可见性、凭据权限和运行态 hydrate 逻辑'
)
assert(
  accountTestSessionRouteRegistrationIndex >= 0,
  '账户路由应注册账号测试 session 写入子路由'
)
assert(
  accountTestStatusRouteRegistrationIndex >= 0,
  '账户路由应注册账号测试状态读取子路由'
)
assert(
  accountTestDispatchRouteRegistrationIndex >= 0,
  '账户路由应注册账号测试调度子路由'
)
assert(
  accountImportRouteRegistrationIndex >= 0,
  '账户路由应注册账号导入子路由'
)
assert(
  accountTrafficMigrationRouteRegistrationIndex >= 0,
  '账户路由应注册账号流量迁移子路由'
)
assert(
  accountGroupBindingRouteRegistrationIndex >= 0,
  '账户路由应注册账号分组绑定子路由'
)
assert(
  accountTestSessionRouteRegistrationIndex < accountReadRouteIndex,
  '账号测试 session 写入子路由必须注册在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountTestStatusRouteRegistrationIndex < accountReadRouteIndex,
  '账号测试状态读取子路由必须注册在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  draftTestRouteIndex < accountReadRouteIndex,
  'POST /test-draft 必须定义在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountImportRouteRegistrationIndex < accountReadRouteIndex,
  '账号导入子路由必须注册在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountTrafficMigrationRouteRegistrationIndex < accountReadRouteIndex,
  '账号流量迁移子路由必须注册在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountGroupBindingRouteRegistrationIndex < accountReadRouteIndex,
  '账号分组绑定子路由必须注册在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountReadRouteIndex < accountCreateRouteIndex,
  'GET /:id 详情子路由注册必须位于 POST / 创建路由之前'
)
assert(
  accountCreateRouteIndex < accountTestDispatchRouteRegistrationIndex,
  'POST /:id/test 账号测试调度子路由注册必须位于 POST / 创建路由之后'
)
assert(
  accountTestStatusRoutesSource.includes("router.get('/test-tasks',"),
  '应提供账号测试任务批量查询接口，避免批量测试逐个任务轮询'
)
assert(
  accountTestSessionRoutesSource.includes("router.post('/test-tasks/:taskId/cancel'"),
  '应提供账号测试任务取消接口'
)

assert(
  accountTestTaskQueueSource.includes('testOpenAIAccountWithDiagnosticRetries(account, {'),
  '真实账号测试应在后台任务队列中执行，并使用诊断重试等待策略'
)
assert(
  accountTestTaskQueueSource.includes('testOpenAIDraftAccountWithDiagnosticRetries')
    && accountTestTaskQueueSource.includes('openAIDraftAccountSecret(draft, attemptSignal)'),
  '草稿账号测试应把 OAuth 刷新和候选账号生成纳入单次诊断 attempt 超时'
)
assert(
  accountTestTaskQueueSource.includes('accountTestTaskProgressReporter(task.id)')
    && accountTestTaskQueueSource.includes('updateAccountTestTaskMessage(taskId, accountDiagnosticAttemptMessage(progress))'),
  '后台账号测试任务应在每次 10/20/30s 真实请求 attempt 开始时更新进度消息'
)
assert(
  accountTestTaskQueueSource.includes('const defaultManualAccountTestConcurrency = 100')
    && accountTestTaskQueueSource.includes('manualAccountTestQueue.setConcurrency(accountTestTaskConcurrency())')
    && accountTestTaskQueueSource.includes('getSettings().accountTestTaskConcurrency'),
  '手动账号测试后台 worker 应使用系统设置控制并发，默认 100'
)
assert(
  accountTestTaskQueueSource.includes('listRunnableAccountTestTaskIds(manualAccountTestRefillBatchSize())')
    && accountTestTaskQueueSource.includes('manualAccountTestRefillMaxBatchSize = 1000'),
  '手动账号测试队列应持续从 DB 补拉 queued 任务，避免 worker 重启后只执行首批任务'
)
assert.equal(
  runOpenAIAccountTestWithSideEffectsSource.includes('markAccountTestTemporaryUnavailable'),
  false,
  '账号测试失败不应在主测试函数内直接写临时不可调用，应先进入事前确认'
)
assert(
  accountTestTaskQueueSource.includes('manualAccountTestFailurePrecheckQueue')
    && accountTestTaskQueueSource.includes('enqueueManualAccountTestFailurePrecheck(account, access, result')
    && accountTestTaskQueueSource.includes("trafficSource: 'cooldown_retest'")
    && accountTestTaskQueueSource.includes('getAccountPrecheckMutationState'),
  '账号测试失败应进入事前确认队列，确认失败后才允许写临时不可调用，并需要跳过已被更新的旧确认'
)
assert(
  accountTestSessionRoutesSource.includes("router.post('/test-sessions'")
    && accountTestSessionRoutesSource.includes("router.post('/test-sessions/:sessionId/heartbeat'")
    && accountTestSessionRoutesSource.includes("router.post('/test-sessions/:sessionId/cancel'"),
  '账号测试应提供 session 创建、心跳和批量取消接口'
)
assert(
  !accountsRoutesSource.includes("accountsRouter.post('/test-sessions'")
    && !accountsRoutesSource.includes("accountsRouter.post('/test-sessions/:sessionId/heartbeat'")
    && !accountsRoutesSource.includes("accountsRouter.post('/test-sessions/:sessionId/cancel'")
    && !accountsRoutesSource.includes("accountsRouter.post('/test-tasks/:taskId/cancel'"),
  '账号测试 session 写入路由不应继续放在账户主路由文件中'
)
assert(
  accountTestStatusRoutesSource.includes("router.get('/test-sessions/:sessionId'"),
  '账号测试状态子路由应提供 session 查询接口'
)
assert(
  accountTestTaskRepositorySource.includes('account_test_sessions')
    && accountTestTaskRepositorySource.includes('cancelExpiredAccountTestSessions')
    && accountTestTaskRepositorySource.includes('前端测试窗口已关闭，任务已取消'),
  '账号测试任务应支持前端关闭后的 session 过期取消'
)
assert(
  accountTestTaskRepositorySource.includes('AND cancel_requested = 1')
    && accountTestTaskRepositorySource.includes('AND cancel_requested = 0'),
  'worker 重启时应保留已请求取消的 running 任务，不应把它们重新排队'
)
assert(
  accountTestTaskRepositorySource.includes('failExpiredQueuedAccountTestTasks')
    && accountTestTaskRepositorySource.includes('accountTestQueuedWaitExpiredMessage')
    && accountTestTaskQueueSource.includes('failExpiredQueuedAccountTestTasks(manualAccountTestQueuedMaxWaitMs')
    && accountTestTaskQueueSource.includes('manualAccountTestQueuedMaxWaitMs = 10 * 60_000'),
  '未被 worker 消费的 queued 任务不应计算 60s 运行超时，但后台应按独立队列等待上限自动收口'
)
assert(
  accountTestTaskRepositorySource.includes('draft_account_encrypted')
    && accountTestTaskRepositorySource.includes('encryptJson(value)')
    && accountTestTaskRepositorySource.includes('decryptJson<unknown>(value)'),
  '草稿测试任务必须把未保存账户快照加密写入任务表'
)
assert(
  accountTestTaskQueueSource.includes('if (task.draftAccount)')
    && accountTestTaskQueueSource.includes('candidateAccount'),
  '草稿账户测试应在后台队列中使用候选账号执行，不应要求账户已保存'
)
assert(
  workerSource.includes('startAccountTestTaskQueue()'),
  'worker 启动时应启动手动账号测试队列'
)
assert(
  backgroundIpcSource.includes("type: 'background_worker_account_test_tasks'"),
  '主进程和 DB service 应能通过 IPC 投递账号测试任务'
)
assert(
  backgroundIpcSource.includes("type: 'background_worker_account_test_cancel'"),
  '主进程和 DB service 应能通过 IPC 取消账号测试任务'
)
assert.equal(
  frontendAccountTestModalSource.includes('submitAccountTest(account, payload, controller.signal)'),
  false,
  '前端创建测试任务的请求不应绑定停止用 AbortSignal，否则停止时可能拿不到 taskId 而无法取消后台任务'
)
assert(
  frontendAccountTestModalSource.includes('cancelCreatedAccountTestTask(task.id, account)'),
  '前端应在停止信号已触发但刚拿到 taskId 时立即取消后台测试任务'
)
assert(
  frontendAccountTestModalSource.includes('activeSingleTestTask')
    && frontendAccountTestModalSource.includes('waitForSubmittedAccountTestResult(task, account, controller.signal,')
    && frontendAccountTestModalSource.includes('activeSingleTestTask.value = latestTask'),
  '前端单账号测试应轮询活动任务并把任务状态传给测试终端'
)
assert(
  frontendAccountTestModalSource.includes('runBatchAccountTestItem(account, index, controller, session.id)')
    && frontendAccountTestModalSource.includes('const result = await waitForSubmittedAccountTestResult(task, account, controller.signal,')
    && frontendAccountTestTaskPollingSource.includes('accountTestTaskMaxWaitMs')
    && frontendAccountTestTaskPollingSource.includes('await cancelTask(task.id, account)')
    && frontendAccountTestModalSource.includes('cancelCreatedAccountTestTask(task.id, account)'),
  '前端批量测试应让每个任务独立完成提交、轮询和运行超时取消'
)
assert(
  frontendAccountBatchExecutionSource.includes('const accountBatchTestChunkSize = 10')
    && frontendAccountTestModalSource.includes('accountBatchTestChunkSize')
    && frontendAccountTestModalSource.includes('runInFixedBatches(accounts, accountBatchTestChunkSize'),
  '前端批量测试应固定每批最多提交 10 个任务，本批全部结束后再提交下一批'
)
assert(
  frontendAccountTestModalSource.includes('createAccountTestSession')
    && frontendAccountTestModalSource.includes('startAccountTestSessionHeartbeat')
    && frontendAccountTestModalSource.includes('cancelActiveAccountTestSession'),
  '前端测试弹窗应创建测试 session、保持心跳，并在停止或关闭时批量取消 session'
)
assert(
  frontendAccountTestModalSource.includes('beforeunload')
    && frontendAccountTestModalSource.includes('sendCancelAccountTestSessionOnUnload')
    && frontendAccountTestSessionClientSource.includes('navigator.sendBeacon')
    && frontendAccountTestSessionClientSource.includes('keepalive: true'),
  '前端刷新或关闭页面时应使用 sendBeacon / keepalive 兜底取消测试 session'
)
assert(
  frontendAccountTestSessionClientSource.includes('api.accounts.testDraft')
    && frontendAccountTestSessionClientSource.includes('api.myAccounts.testDraft')
    && frontendAccountTestSessionClientSource.includes('accountOperationScopeParams(input.account, input.accountScopeParams)'),
  '前端测试 session client 应集中管理管理端/个人端和草稿/已保存草稿的账号测试 API 分流'
)
assert(
  frontendAccountTestTaskPollingSource.includes("if (input.task.status !== 'running')")
    && frontendAccountTestTaskPollingSource.includes('parseTaskTime(input.task.startedAt)')
    && frontendAccountTestTaskPollingSource.includes('账号测试运行超过'),
  '前端 60s 超时只应从后台任务进入 running 且写入 startedAt 后开始计算'
)
assert.equal(
  frontendAccountTestModalSource.includes('pollBatchAccountTestTasks('),
  false,
  '前端批量测试不应保留旧的提交全部任务后统一轮询流程'
)
assert(
  frontendAccountTestModalComponentSource.includes('activeTask?: AccountTestTask')
    && frontendAccountTestDisplayFormattersSource.includes('当前窗口估计')
    && frontendAccountTestDisplayFormattersSource.includes('10s + 20s + 30s'),
  '前端测试终端应展示后台任务状态、等待策略和当前等待窗口'
)
assert(
  frontendAccountTestDisplayFormattersSource.includes('等待接收')
    && frontendAccountTestDisplayFormattersSource.includes("item.status === 'queued'"),
  '批量测试弹窗应区分等待接收和测试中，避免把 worker 未接任务误展示为真实测试中'
)
assert(
  frontendAccountTestDisplayFormattersSource.includes('每批最多 10 个账户'),
  '批量测试弹窗应明确展示固定小批次提交策略'
)

console.log('账号测试任务边界回归通过：手动测试由后台 worker 队列执行，前端通过任务接口查询结果')
