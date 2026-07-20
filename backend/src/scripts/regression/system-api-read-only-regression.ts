import { strict as assert } from 'node:assert'
import type { AddressInfo } from 'node:net'

import { runtimeConfig } from '../../config/runtime.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { systemApiReadOnlyMessage } from '../../modules/system-api/system-api-read-only.middleware.js'

const app = createSystemApiApp({
  systemApiPrefix: '/__aisys__/api',
  publicApiPrefix: '/__aipublic__',
  bypassSystemApiRateLimitForTest: true
})
const server = app.listen(0, '127.0.0.1')
await new Promise<void>((resolve, reject) => {
  server.once('listening', resolve)
  server.once('error', reject)
})

try {
  const address = server.address() as AddressInfo
  const baseUrl = `http://127.0.0.1:${address.port}`

  runtimeConfig.systemApi.readOnly = false
  const disabled = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{}'
  })
  assert.notEqual(disabled.status, 503, '正式模式默认不能启用只读门禁')

  runtimeConfig.systemApi.readOnly = true
  for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
    const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, { method })
    assert.equal(response.status, 503, `${method} 必须被 System API 只读门禁拒绝`)
    assert.equal(response.headers.get('cache-control'), 'no-store')
    assert.equal(response.headers.get('retry-after'), '60')
    assert.deepEqual(await response.json(), { message: systemApiReadOnlyMessage, code: 'system_api_read_only' })
  }
  for (const method of ['GET', 'HEAD', 'OPTIONS']) {
    const response = await fetch(`${baseUrl}/__aisys__/api/health`, { method })
    assert.notEqual(response.status, 503, `${method} 必须允许继续进入读取链路`)
  }

  const readLikeAccountActions = [
    '/__aisys__/api/accounts/account-1/test',
    '/__aisys__/api/accounts/test-draft',
    '/__aisys__/api/accounts/test-sessions',
    '/__aisys__/api/accounts/test-sessions/session-1/heartbeat',
    '/__aisys__/api/accounts/test-sessions/session-1/complete',
    '/__aisys__/api/accounts/test-sessions/session-1/cancel',
    '/__aisys__/api/accounts/test-tasks/task-1/cancel',
    '/__aisys__/api/accounts/account-1/balance/refresh',
    '/__aisys__/api/accounts/balance/test-draft',
    '/__aisys__/api/my-accounts/account-1/test',
    '/__aisys__/api/my-accounts/test-draft',
    '/__aisys__/api/my-accounts/test-sessions',
    '/__aisys__/api/my-accounts/test-sessions/session-1/heartbeat',
    '/__aisys__/api/my-accounts/test-sessions/session-1/complete',
    '/__aisys__/api/my-accounts/test-sessions/session-1/cancel',
    '/__aisys__/api/my-accounts/test-tasks/task-1/cancel',
    '/__aisys__/api/my-accounts/account-1/balance/refresh',
    '/__aisys__/api/my-accounts/balance/test-draft'
  ]
  for (const path of readLikeAccountActions) {
    const response = await fetch(`${baseUrl}${path}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: '{}'
    })
    assert.notEqual(response.status, 503, `${path} 是账户查询/测试动作，不应被临时只读门禁拦截`)
  }

  const accountCreate = await fetch(`${baseUrl}/__aisys__/api/my-accounts`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{}'
  })
  assert.equal(accountCreate.status, 503, '账户新增仍属于管理写操作，必须被临时只读门禁拒绝')

  const publicWrite = await fetch(`${baseUrl}/__aipublic__/group/add`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{}'
  })
  assert.equal(publicWrite.status, 401, '非管理公共 API 应进入自身认证链，而不是被管理端只读门禁拦截')
  assert.equal((await publicWrite.json() as { code?: string }).code, 'external_source_token_missing')

  console.log('System API 临时只读门禁回归通过：管理写请求 503，Public API 进入自身认证链')
} finally {
  runtimeConfig.systemApi.readOnly = false
  await new Promise<void>((resolve) => server.close(() => resolve()))
}
