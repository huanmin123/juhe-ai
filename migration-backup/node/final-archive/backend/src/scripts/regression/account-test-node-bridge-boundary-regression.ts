import assert from 'node:assert/strict'
import { once } from 'node:events'
import { existsSync, readFileSync } from 'node:fs'
import type { AddressInfo } from 'node:net'

import express from 'express'

import {
  accountTestDispatchInternalPrefix,
  createAccountTestDispatchRouter,
  createAccountTestDispatchSignature
} from '../../modules/internal-api/account-test-dispatch.routes.js'

const routePath = 'src/modules/internal-api/account-test-dispatch.routes.ts'
const servicePath = 'src/modules/internal-api/account-test-dispatch.service.ts'
const backgroundIpcPath = 'src/modules/background/background-ipc.ts'
const dbServiceIpcPath = 'src/modules/db-service/db-service-ipc.ts'
const accountQueuePath = 'src/modules/accounts/account-test-task-queue.service.ts'
const serverSource = readFileSync('src/server.ts', 'utf8')

assert.equal(existsSync(routePath), true, 'Node 必须提供账户测试内部 dispatch route')
assert.equal(existsSync(servicePath), true, 'Node 必须提供账户测试内部 dispatch service')
assert.match(serverSource, /createAccountTestDispatchRouter/, 'Node server 必须挂载账户测试内部 dispatch router')
assert.match(serverSource, /dispatchAccountTestTask/, 'Node server 必须把内部 dispatch 交给账户测试队列')

const routeSource = readFileSync(routePath, 'utf8')
const serviceSource = readFileSync(servicePath, 'utf8')
const backgroundIpcSource = readFileSync(backgroundIpcPath, 'utf8')
const dbServiceIpcSource = readFileSync(dbServiceIpcPath, 'utf8')
const accountQueueSource = readFileSync(accountQueuePath, 'utf8')
assert.match(routeSource, /juhe-ai:account-test-dispatch:v1/, '账户测试内部 dispatch 必须使用独立 HMAC domain')
assert.match(routeSource, /\/v1\/account-test\/dispatch/, '账户测试内部 dispatch 必须使用固定大小写 path')
assert.match(routeSource, /isLoopbackRemoteAddress/, '账户测试内部 dispatch 必须限制 loopback 来源')
assert.match(routeSource, /timingSafeEqual/, '账户测试内部 dispatch 必须恒定时间校验签名')
assert.match(serviceSource, /dispatchAccountTestTasks\(\[normalizedId\]\)/, '内部 dispatch 只能复用 Node 既有任务队列，不得重复创建任务')
assert.match(serviceSource, /requestBackgroundWorkerDbService/, '内部 dispatch 投递失败时必须通过 DB service adapter 收口任务状态')
assert.match(serviceSource, /type: 'fail_account_test_task'/, '内部 dispatch 投递失败时必须把既有任务标记为 failed')
assert.match(
  backgroundIpcSource,
  /sendAccountTestTasksToWorker[\s\S]*opsWorkerProcess[\s\S]*opsWorkerReady[\s\S]*return false/,
  '账户测试 dispatch 必须在 ops-worker 未就绪时拒绝接收，避免任务永久停留 queued'
)
assert.match(dbServiceIpcSource, /requestAccountTestTasksToWorker/, 'DB service 账户测试 dispatch 必须等待父进程投递结果')
assert.match(dbServiceIpcSource, /background_worker_account_test_tasks_response/, '父进程必须把账户测试投递结果回传给 DB service')
assert.match(accountQueueSource, /await requestAccountTestTasksToWorker/, 'DB service 账户测试队列必须等待 worker 投递确认')

const calls: string[] = []
const app = express()
app.use(accountTestDispatchInternalPrefix, createAccountTestDispatchRouter({
  secret: 'bridge-secret',
  dispatch: async (taskId) => {
    calls.push(taskId)
    await Promise.resolve()
    return taskId !== 'unready_task'
  }
}))
const server = app.listen(0, '127.0.0.1')
await once(server, 'listening')
try {
  const port = (server.address() as AddressInfo).port
  const body = Buffer.from(JSON.stringify({ version: 1, taskId: ' accttest_1 ' }))
  const response = await fetch(`http://127.0.0.1:${port}${accountTestDispatchInternalPrefix}/v1/account-test/dispatch`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'content-encoding': 'identity',
      'x-juhe-ai-signature': createAccountTestDispatchSignature('bridge-secret', body)
    },
    body
  })
  assert.equal(response.status, 202, '合法签名账户测试 dispatch 应返回 202')
  assert.deepEqual(calls, ['accttest_1'])

  const unreadyBody = Buffer.from(JSON.stringify({ version: 1, taskId: 'unready_task' }))
  const unreadyResponse = await fetch(`http://127.0.0.1:${port}${accountTestDispatchInternalPrefix}/v1/account-test/dispatch`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'content-encoding': 'identity',
      'x-juhe-ai-signature': createAccountTestDispatchSignature('bridge-secret', unreadyBody)
    },
    body: unreadyBody
  })
  assert.equal(unreadyResponse.status, 503, 'worker 未就绪时内部账户测试 dispatch 应返回 503')

  const rejected = await fetch(`http://127.0.0.1:${port}${accountTestDispatchInternalPrefix}/v1/account-test/dispatch`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-juhe-ai-signature': `v1=${'0'.repeat(64)}` },
    body
  })
  assert.equal(rejected.status, 401, '错误签名账户测试 dispatch 应返回 401')
} finally {
  server.close()
  await once(server, 'close')
}

console.log('account-test-node-bridge-boundary-regression passed')
