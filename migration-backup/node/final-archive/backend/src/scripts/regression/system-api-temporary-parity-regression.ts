import { strict as assert } from 'node:assert'
import type { AddressInfo } from 'node:net'

import { runtimeConfig } from '../../config/runtime.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'

const legacySystemApiConfig = runtimeConfig.systemApi as { readOnly?: boolean }
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

  // A legacy temporary-release environment may still set this variable while
  // rollout configuration converges. It must not alter any API semantics.
  legacySystemApiConfig.readOnly = true

  const login = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{}'
  })
  assert.notEqual(login.status, 503, '临时发布不得按 HTTP 方法拦截正常管理请求')
  const loginPayload = await login.json() as { code?: string }
  assert.notEqual(loginPayload.code, 'system_api_read_only', '临时发布不得返回遗留只读门禁错误')

  const publicWrite = await fetch(`${baseUrl}/__aipublic__/group/add`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: '{}'
  })
  assert.equal(publicWrite.status, 401, '公共 API 仍必须进入自身认证链')

  console.log('System API 临时发布等价回归通过：遗留配置不改变管理或公共 API 语义')
} finally {
  legacySystemApiConfig.readOnly = false
  await new Promise<void>((resolve) => server.close(() => resolve()))
}
