import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendSrc = resolve(currentDir, '../..')
const projectRoot = resolve(backendSrc, '../..')

const accountsRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/accounts.routes.ts'), 'utf8')
const accountTestTaskQueueSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-task-queue.service.ts'), 'utf8')
const accountTestTaskRepositorySource = readFileSync(resolve(backendSrc, 'storage/account-test-tasks.repository.ts'), 'utf8')
const workerSource = readFileSync(resolve(backendSrc, 'worker.ts'), 'utf8')
const backgroundIpcSource = readFileSync(resolve(backendSrc, 'modules/background/background-ipc.ts'), 'utf8')
const frontendAccountTestModalSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/useAccountTestModal.ts'), 'utf8')
const frontendAccountTestModalComponentSource = readFileSync(resolve(projectRoot, 'frontend/src/views/accounts/AccountTestModal.vue'), 'utf8')

assert.equal(
  accountsRoutesSource.includes('testOpenAIAccount('),
  false,
  '账户路由不应直接等待 OpenAI 测试，应只创建后台任务'
)
assert(
  accountsRoutesSource.includes('createAccountTestTask({'),
  'POST /accounts/:id/test 应创建账号测试任务'
)
assert(
  accountsRoutesSource.includes('dispatchAccountTestTasks([task.id])'),
  'POST /accounts/:id/test 应把测试任务投递到后台 worker'
)
assert(
  accountsRoutesSource.includes('res.status(202).json(ok(task))'),
  'POST /accounts/:id/test 应返回 202 和任务对象，而不是同步测试结果'
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

const taskReadRouteIndex = accountsRoutesSource.indexOf("accountsRouter.get('/test-tasks/:taskId'")
const draftTestRouteIndex = accountsRoutesSource.indexOf("accountsRouter.post('/test-draft'")
const accountReadRouteIndex = accountsRoutesSource.indexOf("accountsRouter.get('/:id'")
assert(taskReadRouteIndex >= 0, '应提供账号测试任务查询接口')
assert(draftTestRouteIndex >= 0, '应提供账号草稿测试任务创建接口')
assert(accountReadRouteIndex >= 0, '应保留账号详情接口')
assert(
  taskReadRouteIndex < accountReadRouteIndex,
  'GET /test-tasks/:taskId 必须定义在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  draftTestRouteIndex < accountReadRouteIndex,
  'POST /test-draft 必须定义在 GET /:id 之前，避免被参数路由吞掉'
)
assert(
  accountsRoutesSource.includes("accountsRouter.get('/test-tasks',"),
  '应提供账号测试任务批量查询接口，避免批量测试逐个任务轮询'
)
assert(
  accountsRoutesSource.includes("accountsRouter.post('/test-tasks/:taskId/cancel'"),
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
  accountTestTaskQueueSource.includes('const manualAccountTestConcurrency = 3'),
  '手动账号测试后台队列应有明确并发上限'
)
assert(
  accountTestTaskQueueSource.includes('listRunnableAccountTestTaskIds(manualAccountTestRefillBatchSize)'),
  '手动账号测试队列应持续从 DB 补拉 queued 任务，避免 worker 重启后只执行首批任务'
)
assert(
  accountTestTaskRepositorySource.includes('AND cancel_requested = 1')
    && accountTestTaskRepositorySource.includes('AND cancel_requested = 0'),
  'worker 重启时应保留已请求取消的 running 任务，不应把它们重新排队'
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
    && frontendAccountTestModalSource.includes('waitForAccountTestResult(task, account, controller.signal,'),
  '前端单账号测试应轮询活动任务并把任务状态传给测试终端'
)
assert(
  frontendAccountTestModalComponentSource.includes('activeTask?: AccountTestTask')
    && frontendAccountTestModalComponentSource.includes('当前窗口估计')
    && frontendAccountTestModalComponentSource.includes('10s + 20s + 30s'),
  '前端测试终端应展示后台任务状态、等待策略和当前等待窗口'
)

console.log('账号测试任务边界回归通过：手动测试由后台 worker 队列执行，前端通过任务接口查询结果')
