import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { requestContextMiddleware } from '../../shared/request-context.js'
import {
  createSystemApiApp,
  systemApiDbServiceAdmissionControl,
  systemApiDbServiceMaxInFlight,
  chatSystemApiJsonBodyLimit,
  systemApiJsonBodyLimit
} from '../../modules/system-api/system-api-app.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-api-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const databasePath = join(tempRoot, 'system-api.sqlite3')
const chatDatabasePath = join(tempRoot, 'chat.sqlite3')
const datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
const usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
const statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databasePath = databasePath
runtimeConfig.chatDatabasePath = chatDatabasePath
runtimeConfig.datasetDatabasePath = datasetDatabasePath
runtimeConfig.usageCatalogDatabasePath = usageCatalogDatabasePath
runtimeConfig.statsDatabasePath = statsDatabasePath
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_DATABASE_PATH = databasePath
process.env.JUHE_AI_CHAT_DATABASE_PATH = chatDatabasePath
process.env.JUHE_AI_DATASET_DATABASE_PATH = datasetDatabasePath
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = usageCatalogDatabasePath
process.env.JUHE_AI_STATS_DATABASE_PATH = statsDatabasePath
runtimeConfig.secret = 'system-api-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = process.env.DEBUG_SYSTEM_API_REGRESSION === '1' ? 'debug' : 'silent'

const databaseModule = await import('../../storage/database.js')
const dbServiceHandlers = await import('../../modules/db-service/db-service-handlers.js')
const sqliteReadWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
databaseModule.getBusinessDatabase()
const repositories = await import('../../storage/repositories.js')
const authenticatedCookie = `juhe_ai_session=${repositories.createSession('sys_admin', 1).token}`

let server: http.Server | undefined
let admissionServer: http.Server | undefined
try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const health = await getJson<{ status: string; service: string }>(`${baseUrl}/__aisys__/api/health`)
  assert.equal(health.status, 'ok', 'DB service system API health 应返回 ok')
  assert.equal(health.service, 'juhe-ai-db-service', 'DB service system API health 应标识内部服务')

  const publicSettings = await getJson<{ data: { appName?: string } }>(`${baseUrl}/__aisys__/api/settings/public`)
  assert.equal(publicSettings.data.appName, '聚合 AI', '公开设置应由 DB service system API 直接读取')

  assert.equal(systemApiJsonBodyLimit, '256kb', 'DB service system API JSON 请求体上限应保持 256KB')
  assert.equal(chatSystemApiJsonBodyLimit, '24mb', 'AI 问答图片请求使用独立且有界的 24MB 上限')
  const largeBodyResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'secret', padding: 'x'.repeat(280 * 1024) })
  })
  assert.equal(largeBodyResponse.status, 413, 'DB service system API 应拒绝超过 256KB 的 JSON 请求体')
  const largeBodyError = await largeBodyResponse.json() as { message?: string }
  assert.equal(largeBodyError.message, '请求体过大', '超限 JSON 应返回中文请求体过大错误')

  const chatImageBodyResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/missing/stream`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ padding: 'x'.repeat(300 * 1024) })
  })
  assert.equal(chatImageBodyResponse.status, 401, 'AI 问答专用 parser 应接受超过通用 256KB、仍在 24MB 内的图片请求体')

  const malformedChatJsonResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/missing/stream`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', cookie: authenticatedCookie },
    body: '{'
  })
  assert.equal(malformedChatJsonResponse.status, 400, 'AI 问答专用 parser 应拒绝损坏的 JSON')
  assert.deepEqual(await malformedChatJsonResponse.json(), { message: '请求体无效' }, '只有 body-parser 错误才能返回请求体无效')

  const chatCleanup = await dbServiceHandlers.handleDbServiceOperation({
    type: 'cleanup_chat_retention',
    now: '2026-07-14T00:00:00.000Z',
    interruptedBefore: '2026-07-13T23:40:00.000Z',
    limit: 1000,
    retentionDays: 7
  })
  assert.equal(chatCleanup.recoveredCompactions, 0, '会话保留清理的 1000 批次不应越过上下文维护的 500 上限')
  assert.equal(chatCleanup.deletedCheckpoints, 0, '空聊天库的上下文 checkpoint 清理应正常完成')
  assert.equal(chatCleanup.claimedAssets, 0, '空聊天库的资产清理应正常完成')

  admissionServer = await startAdmissionProbeServer()
  await assertAdmissionControlRejectsOverloadAndReleases(`http://127.0.0.1:${serverAddress(admissionServer).port}`)

  console.log('DB service system API HTTP 回归通过：请求体边界与 AI 问答维护批次隔离可用')
} finally {
  await closeServer(admissionServer)
  await closeServer(server)
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function startAdmissionProbeServer(): Promise<http.Server> {
  const app = express()
  const releaseHolders: Array<() => void> = []
  app.use(requestContextMiddleware)
  app.get('/hold', systemApiDbServiceAdmissionControl, (_req, res) => {
    releaseHolders.push(() => {
      if (!res.writableEnded) {
        res.status(200).json({ ok: true })
      }
    })
  })
  app.get('/__release-count', (_req, res) => {
    res.json({ count: releaseHolders.length })
  })
  app.post('/__release-all', (_req, res) => {
    const holders = releaseHolders.splice(0, releaseHolders.length)
    holders.forEach((release) => release())
    res.json({ released: holders.length })
  })
  const probeServer = app.listen(0, '127.0.0.1')
  await listen(probeServer)
  return probeServer
}

async function assertAdmissionControlRejectsOverloadAndReleases(baseUrl: string): Promise<void> {
  const heldRequests = Array.from({ length: systemApiDbServiceMaxInFlight }, () => fetch(`${baseUrl}/hold`))
  await waitForReleaseCount(baseUrl, systemApiDbServiceMaxInFlight)

  const overloaded = await fetch(`${baseUrl}/hold`)
  assert.equal(overloaded.status, 503, 'system API admission control 达到上限后第 65 个请求应返回 503')
  assert.equal(overloaded.headers.get('retry-after'), '1', 'system API admission control 过载响应应带 Retry-After: 1')
  const overloadedBody = await overloaded.json() as { code?: string; message?: string }
  assert.equal(overloadedBody.code, 'system_api_busy', 'system API admission control 过载响应应返回稳定错误码')

  await postJson(`${baseUrl}/__release-all`)
  const heldResponses = await Promise.all(heldRequests)
  assert(heldResponses.every((response) => response.status === 200), '释放挂起请求后，已进入 admission 的请求应正常结束')

  const postReleaseRequest = fetch(`${baseUrl}/hold`)
  await waitForReleaseCount(baseUrl, 1)
  await postJson(`${baseUrl}/__release-all`)
  const postReleaseResponse = await postReleaseRequest
  assert.equal(postReleaseResponse.status, 200, 'finish/close 释放 admission 计数后，新请求应可进入')
}

async function waitForReleaseCount(baseUrl: string, expected: number): Promise<void> {
  const deadline = Date.now() + 5000
  while (Date.now() < deadline) {
    const response = await getJson<{ count: number }>(`${baseUrl}/__release-count`)
    if (response.count >= expected) {
      return
    }
    await delay(10)
  }
  throw new Error(`等待 admission probe 挂起请求数量超时：${expected}`)
}

async function postJson(url: string): Promise<unknown> {
  const response = await fetch(url, { method: 'POST' })
  assert.equal(response.status, 200, `${url} 应返回 200`)
  return await response.json()
}

async function delay(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}

async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url)
  assert.equal(response.status, 200, `${url} 应返回 200`)
  return await response.json() as T
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}
