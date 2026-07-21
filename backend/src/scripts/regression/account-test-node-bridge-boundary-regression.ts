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
const serverSource = readFileSync('src/server.ts', 'utf8')

assert.equal(existsSync(routePath), true, 'Node 必须提供账户测试内部 dispatch route')
assert.equal(existsSync(servicePath), true, 'Node 必须提供账户测试内部 dispatch service')
assert.match(serverSource, /createAccountTestDispatchRouter/, 'Node server 必须挂载账户测试内部 dispatch router')
assert.match(serverSource, /dispatchAccountTestTask/, 'Node server 必须把内部 dispatch 交给账户测试队列')

const routeSource = readFileSync(routePath, 'utf8')
const serviceSource = readFileSync(servicePath, 'utf8')
assert.match(routeSource, /juhe-ai:account-test-dispatch:v1/, '账户测试内部 dispatch 必须使用独立 HMAC domain')
assert.match(routeSource, /\/v1\/account-test\/dispatch/, '账户测试内部 dispatch 必须使用固定大小写 path')
assert.match(routeSource, /isLoopbackRemoteAddress/, '账户测试内部 dispatch 必须限制 loopback 来源')
assert.match(routeSource, /timingSafeEqual/, '账户测试内部 dispatch 必须恒定时间校验签名')
assert.match(serviceSource, /dispatchAccountTestTasks\(\[normalizedId\]\)/, '内部 dispatch 只能复用 Node 既有任务队列，不得重复创建任务')

const calls: string[] = []
const app = express()
app.use(accountTestDispatchInternalPrefix, createAccountTestDispatchRouter({
  secret: 'bridge-secret',
  dispatch: (taskId) => {
    calls.push(taskId)
    return true
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
