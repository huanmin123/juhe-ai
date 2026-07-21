import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-external-source-light-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'external-source-light-http-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { externalIntegrationSourcesRouter },
  { closeSqliteReadWorkerPool },
  databaseModule
] = await Promise.all([
  import('../../modules/external-integrations/external-integration-sources.routes.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js')
])

const app = express()
app.use(express.json({ limit: '1mb' }))
app.use('/external-integration-sources', externalIntegrationSourcesRouter)

let server: Server | undefined

try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/external-integration-sources`

  const created = await request<{ token: { id: string; token: string } }>(baseUrl, '', 'POST', {
    name: '轻量响应来源',
    status: 'active',
    scopes: ['juhe_ai_public:group_list:read'],
    rateLimits: [{ windowSeconds: 60, maxRequests: 10 }],
    notes: '详情字段不应跟随 mutation 返回'
  }, 201)
  assert.deepEqual(Object.keys(created).sort(), ['token'], '创建响应只应返回页面立即展示的明文 Token')
  assert(created.token.token, '创建响应必须保留只显示一次的明文 Token')

  const list = await request<{ items: Array<{ id: string }> }>(baseUrl, '?keyword=轻量响应来源', 'GET', undefined, 200)
  const sourceId = list.items[0]?.id
  assert(sourceId, '新建来源必须能通过列表读取')

  const updated = await request<{ id: string }>(baseUrl, `/${sourceId}`, 'PATCH', { status: 'disabled' }, 200)
  assert.deepEqual(Object.keys(updated).sort(), ['id'], '更新响应只应确认受影响资源 id')
  assert.equal(updated.id, sourceId)

  const tokenCreated = await request<{ token: { token: string } }>(baseUrl, `/${sourceId}/tokens`, 'POST', {
    name: '轻量响应新增 Token',
    status: 'active',
    scopes: ['juhe_ai_public:group_list:read']
  }, 201)
  assert.deepEqual(Object.keys(tokenCreated).sort(), ['token'], '生成 Token 响应不应附带完整来源详情')
  assert(tokenCreated.token.token, '生成 Token 响应必须保留只显示一次的明文 Token')

  const reset = await request<{ token: { token: string } }>(baseUrl, '/built-in-test-token/reset', 'POST', {}, 200)
  assert.deepEqual(Object.keys(reset).sort(), ['token'], '重置 Token 响应不应附带完整来源详情')
  assert(reset.token.token, '重置响应必须保留新明文 Token')

  console.log('外部来源 mutation 轻量 HTTP 回归通过：create/update/reset/createToken 均只返回页面消费字段')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function request<T>(baseUrl: string, path: string, method: string, body: unknown, expectedStatus: number): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: body === undefined ? undefined : { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const payload = await response.json() as { data?: T; message?: string }
  assert.equal(response.status, expectedStatus, payload.message ?? `${method} ${path} 状态码不符合预期`)
  assert(payload.data !== undefined, `${method} ${path} 缺少 data`)
  return payload.data
}

function listen(server: Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
