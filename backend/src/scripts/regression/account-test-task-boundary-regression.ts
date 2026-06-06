import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendSrc = resolve(currentDir, '../..')

const accountsRoutesSource = readFileSync(resolve(backendSrc, 'modules/accounts/accounts.routes.ts'), 'utf8')
const accountTestTaskQueueSource = readFileSync(resolve(backendSrc, 'modules/accounts/account-test-task-queue.service.ts'), 'utf8')
const workerSource = readFileSync(resolve(backendSrc, 'worker.ts'), 'utf8')
const backgroundIpcSource = readFileSync(resolve(backendSrc, 'modules/background/background-ipc.ts'), 'utf8')

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

const taskReadRouteIndex = accountsRoutesSource.indexOf("accountsRouter.get('/test-tasks/:taskId'")
const accountReadRouteIndex = accountsRoutesSource.indexOf("accountsRouter.get('/:id'")
assert(taskReadRouteIndex >= 0, '应提供账号测试任务查询接口')
assert(accountReadRouteIndex >= 0, '应保留账号详情接口')
assert(
  taskReadRouteIndex < accountReadRouteIndex,
  'GET /test-tasks/:taskId 必须定义在 GET /:id 之前，避免被参数路由吞掉'
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
  accountTestTaskQueueSource.includes('await testOpenAIAccount(account, {'),
  '真实账号测试应在后台任务队列中执行'
)
assert(
  accountTestTaskQueueSource.includes('const manualAccountTestConcurrency = 3'),
  '手动账号测试后台队列应有明确并发上限'
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

console.log('账号测试任务边界回归通过：手动测试由后台 worker 队列执行，前端通过任务接口查询结果')
