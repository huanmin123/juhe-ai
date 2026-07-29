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

  const created = await request<{
    item: {
      id: string
      name: string
      status: string
      scopes: string[]
      rateLimits: Array<{ windowSeconds: number; maxRequests: number }>
      notes?: string
      updatedAt: string
      primaryToken?: { id: string; tokenPrefix: string; tokenSuffix: string }
      isBuiltIn: boolean
    }
    token: { id: string; token: string; tokenPrefix: string; tokenSuffix: string }
  }>(baseUrl, '', 'POST', {
    name: '轻量响应来源',
    status: 'active',
    scopes: ['juhe_ai_public:group_list:read'],
    rateLimits: [{ windowSeconds: 60, maxRequests: 10 }],
    notes: '详情字段不应跟随 mutation 返回'
  }, 201)
  assert.deepEqual(Object.keys(created).sort(), ['item', 'token'], '创建响应只应返回页面可本地插入的窄列表行和一次性 Token')
  assert(created.token.token, '创建响应必须保留只显示一次的明文 Token')
  assert.equal(created.item.id.startsWith('extsrc_'), true, '创建响应必须返回来源列表行 ID')
  assert.equal(created.item.name, '轻量响应来源')
  assert.equal(created.item.updatedAt.length > 0, true, '创建响应列表行必须携带分页排序版本')
  assert.deepEqual(created.item.primaryToken, {
    id: created.token.id,
    tokenPrefix: created.token.tokenPrefix,
    tokenSuffix: created.token.tokenSuffix
  }, '创建响应列表行必须直接携带安全主 Token 预览')
  assert.equal('createdAt' in created.item, false, '创建响应窄列表行不应返回详情专用 createdAt')
  assert.equal('tokens' in created.item, false, '创建响应窄列表行不应返回详情 Token 数组')
  assert.equal('tokenCount' in created.item, false, '创建响应窄列表行不应返回详情 Token 统计')

  const list = await request<{ items: Array<{ id: string; updatedAt: string }> }>(baseUrl, '?keyword=轻量响应来源', 'GET', undefined, 200)
  const sourceId = list.items[0]?.id
  const sourceUpdatedAt = list.items[0]?.updatedAt
  assert(sourceId, '新建来源必须能通过列表读取')
  assert(sourceUpdatedAt, '列表必须返回 PATCH 所需的 updatedAt 版本')

  const updated = await request<{ id: string; updatedAt: string }>(baseUrl, `/${sourceId}`, 'PATCH', {
    status: 'disabled',
    expectedUpdatedAt: sourceUpdatedAt
  }, 200)
  assert.deepEqual(Object.keys(updated).sort(), ['id', 'updatedAt'], '更新响应只应返回资源 id 与新版本')
  assert.equal(updated.id, sourceId)
  assert.notEqual(updated.updatedAt, sourceUpdatedAt, '实际 PATCH 必须推进版本')
  const patchedSource = await request<{
    status: string
    notes?: string
    updatedAt: string
    tokens: Array<{ id: string; name: string; updatedAt: string }>
  }>(baseUrl, `/${sourceId}`, 'GET', undefined, 200)
  assert.equal(patchedSource.status, 'disabled', 'PATCH 应更新指定字段')
  assert.equal(patchedSource.notes, '详情字段不应跟随 mutation 返回', '单字段 PATCH 不得覆盖无关字段')
  const sourceNoop = await request<{ id: string; updatedAt: string }>(baseUrl, `/${sourceId}`, 'PATCH', {
    status: 'disabled',
    expectedUpdatedAt: patchedSource.updatedAt
  }, 200)
  assert.equal(sourceNoop.updatedAt, patchedSource.updatedAt, 'HTTP 同值来源 PATCH 不得推进版本')
  await requestError(baseUrl, `/${sourceId}`, 'PATCH', {
    notes: '过期请求',
    expectedUpdatedAt: sourceUpdatedAt
  }, 409)

  const tokenCreated = await request<{ token: { id: string; token: string } }>(baseUrl, `/${sourceId}/tokens`, 'POST', {
    name: '轻量响应新增 Token',
    status: 'active',
    scopes: ['juhe_ai_public:group_list:read']
  }, 201)
  assert.deepEqual(Object.keys(tokenCreated).sort(), ['token'], '生成 Token 响应不应附带完整来源详情')
  assert(tokenCreated.token.token, '生成 Token 响应必须保留只显示一次的明文 Token')
  const sourceWithToken = await request<{
    tokens: Array<{ id: string; updatedAt: string }>
  }>(baseUrl, `/${sourceId}`, 'GET', undefined, 200)
  const tokenVersion = sourceWithToken.tokens.find((item) => item.id === tokenCreated.token.id)?.updatedAt
  assert(tokenVersion, 'Token 详情必须提供 PATCH 所需版本')
  const tokenUpdated = await request<{ id: string; updatedAt: string }>(baseUrl, `/${sourceId}/tokens/${tokenCreated.token.id}`, 'PATCH', {
    name: '轻量响应改名 Token',
    expectedUpdatedAt: tokenVersion
  }, 200)
  assert.deepEqual(Object.keys(tokenUpdated).sort(), ['id', 'updatedAt'], 'Token PATCH 响应只应返回 id 与新版本')
  await requestError(baseUrl, `/${sourceId}/tokens/${tokenCreated.token.id}`, 'PATCH', {
    status: 'disabled',
    expectedUpdatedAt: tokenVersion
  }, 409)

  const reset = await request<{ token: { token: string } }>(baseUrl, '/built-in-test-token/reset', 'POST', {}, 200)
  assert.deepEqual(Object.keys(reset).sort(), ['token'], '重置 Token 响应不应附带完整来源详情')
  assert(reset.token.token, '重置响应必须保留新明文 Token')

  const deleted = await fetch(`${baseUrl}/${sourceId}`, { method: 'DELETE' })
  assert.equal(deleted.status, 204, '删除来源必须保持 204 空响应契约')
  assert.equal(await deleted.text(), '', '删除来源不得为了前端本地协调扩展 HTTP 响应体')
  await requestError(baseUrl, `/${sourceId}`, 'GET', undefined, 404)

  console.log('外部来源 mutation 轻量 HTTP 回归通过：create 返回窄列表行与 Token，delete 保持 204 空响应')
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

async function requestError(baseUrl: string, path: string, method: string, body: unknown, expectedStatus: number): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: body === undefined ? undefined : { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const payload = await response.json() as { message?: string }
  assert.equal(response.status, expectedStatus, payload.message ?? `${method} ${path} 状态码不符合预期`)
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
