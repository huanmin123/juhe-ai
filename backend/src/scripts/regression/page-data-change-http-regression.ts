import assert from 'node:assert/strict'
import http from 'node:http'

import express from 'express'

import { withRequestAuthContext } from '../../modules/auth/request-context.js'
import { createMemoryPageDataChangeStore } from '../../modules/page-data/page-data-change.service.js'
import { createPageDataChangesRouter } from '../../modules/page-data/page-data-change.routes.js'

const store = createMemoryPageDataChangeStore({ epoch: 'http-epoch' })
const app = express()
app.use(express.json())
app.use((req, _res, next) => {
  const role = req.header('x-test-role') === 'admin' ? 'admin' : 'user'
  withRequestAuthContext({
    systemAccountId: role === 'admin' ? 'admin-a' : 'user-a',
    username: role,
    displayName: role,
    role,
    mustChangePassword: false,
    sessionId: `session-${role}`
  }, next)
})
app.use('/data-changes', createPageDataChangesRouter({ store }))
app.use('/failing-data-changes', createPageDataChangesRouter({
  store: {
    confirm: async () => { throw new Error('redis unavailable') },
    publish: async () => undefined
  }
}))

const server = http.createServer(app)
await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
try {
  const address = server.address()
  assert(address && typeof address !== 'string')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const selfResponse = await postJson(baseUrl, {}, {
    viewScope: 'self',
    domains: { 'accounts.static': null, 'accounts.runtime': null }
  })
  assert.equal(selfResponse.status, 200)
  assert.equal(selfResponse.body.data.domains['accounts.static'].action, 'reload')
  assert.equal(selfResponse.body.data.domains['accounts.runtime'].action, 'reload')

  const forgedAdmin = await postJson(baseUrl, {}, {
    viewScope: 'admin',
    targetSystemAccountId: 'user-b',
    domains: { 'accounts.runtime': null }
  })
  assert.equal(forgedAdmin.status, 403)

  const adminTarget = await postJson(baseUrl, { 'x-test-role': 'admin' }, {
    viewScope: 'admin',
    targetSystemAccountId: 'user-a',
    domains: { 'accounts.runtime': null }
  })
  assert.equal(adminTarget.status, 200)

  const tooMany = await postJson(baseUrl, {}, {
    viewScope: 'self',
    domains: Object.fromEntries(Array.from({ length: 33 }, (_, index) => [`accounts.runtime.${index}`, null]))
  })
  assert.equal(tooMany.status, 400)
  assert.match(String(tooMany.body.message), /最多确认 32 个数据域/)

  const unavailable = await postJson(baseUrl, {}, {
    viewScope: 'self',
    domains: { 'accounts.runtime': null }
  }, '/failing-data-changes/confirm')
  assert.equal(unavailable.status, 503)
  assert.equal(unavailable.headers.get('retry-after'), '5', 'Redis confirm 异常必须保留 Retry-After: 5')
} finally {
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}

console.log('页面数据变更确认 HTTP 回归通过')

async function postJson(
  baseUrl: string,
  headers: Record<string, string>,
  body: object,
  path = '/data-changes/confirm'
): Promise<{ status: number; body: any; headers: Headers }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...headers },
    body: JSON.stringify(body)
  })
  return { status: response.status, body: await response.json(), headers: response.headers }
}
