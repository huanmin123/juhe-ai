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
  const publicWrite = await fetch(`${baseUrl}/__aipublic__/anything`, { method: 'POST' })
  assert.equal(publicWrite.status, 503, 'Public API 非读取方法必须使用同一只读门禁')

  console.log('System/Public API 临时只读门禁回归通过：非读取方法统一 503，读取方法与 /v1 边界保持可用')
} finally {
  runtimeConfig.systemApi.readOnly = false
  await new Promise<void>((resolve) => server.close(() => resolve()))
}
