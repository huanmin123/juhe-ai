import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-generic-upstream-opaque-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-generic-upstream-opaque-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  readWorkerPool,
  repositories,
  settingsRepository,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const hits: string[] = []
let upstreamServer: http.Server | undefined
let appServer: http.Server | undefined

try {
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  gatewayCache.clearGatewayRuntimeCache()

  upstreamServer = http.createServer((req, res) => {
    hits.push(req.url ?? '')
    const path = req.url?.split('?', 1)[0] ?? ''
    if (req.url?.includes('mock_sse_wait_non_stream=1')) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"id":"non_stream_after_wait","choices":[{"message":{"role":"assistant","content":"must not be written into sse transport"}}]}')
      return
    }
    if (path === '/v1/responses') {
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end([
        'event: response.failed',
        'data: {"type":"response.failed","response":{"error":{"code":"vendor_invented_stream_error","message":"opaque stream failure"}}}',
        '',
        ''
      ].join('\n'))
      return
    }
    res.writeHead(418, {
      'content-type': 'application/json; charset=utf-8',
      'x-vendor-error': 'invented'
    })
    res.end('{"error":{"type":"vendor_invented_error","code":"made_up_418","message":"opaque non-stream failure"}}')
  })
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

  const group = repositories.createGroup({ name: '通用响应透传分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '通用响应透传账号',
    type: 'api_key',
    credentials: { api_key: 'sk-generic-opaque', base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '通用响应透传 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)

  appServer = http.createServer(app)
  await listen(appServer)
  const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  const nonStream = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'opaque status' }], stream: false })
  })
  const nonStreamText = await nonStream.text()
  assert.equal(nonStream.status, 418, `通用客户端必须收到上游原始状态，实际 ${nonStream.status}: ${nonStreamText}`)
  assert.equal(nonStream.headers.get('x-vendor-error'), 'invented')
  assert.equal(nonStreamText, '{"error":{"type":"vendor_invented_error","code":"made_up_418","message":"opaque non-stream failure"}}')
  assert.equal(hits.length, 1, '通用非流式响应不得因状态码再次派发')

  const heldSlot = tryAcquireAccountConcurrency(account.id, 1)
  assert.equal(heldSlot.acquired, true, 'SSE 心跳回归前应占用账号并发槽')
  const releaseTimer = setTimeout(() => heldSlot.release(), 250)
  const stream = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'opaque stream event', stream: true })
  })
  const streamText = await stream.text()
  clearTimeout(releaseTimer)
  heldSlot.release()
  assert.equal(stream.status, 200)
  assert.match(streamText, /^: juhe-ai waiting for upstream capacity\n\n/, '并发槽暂不可用时应先发送 SSE 注释心跳')
  assert.equal(hits.length, 2, `SSE 心跳后必须继续派发上游，实际响应：${streamText}`)
  assert.match(streamText, /vendor_invented_stream_error/, '通用 SSE 必须原样保留上游失败事件')
  assert.doesNotMatch(streamText, /upstream_retryable_error/, '通用 SSE 不得改写成专用客户端错误码')

  const heldConflictSlot = tryAcquireAccountConcurrency(account.id, 1)
  assert.equal(heldConflictSlot.acquired, true, 'SSE/非流式传输冲突回归前应占用账号并发槽')
  const conflictReleaseTimer = setTimeout(() => heldConflictSlot.release(), 250)
  const transportConflict = await fetch(`${baseUrl}/v1/chat/completions?mock_sse_wait_non_stream=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'wait then non-stream' }], stream: true })
  })
  assert.equal(transportConflict.status, 200, '等待阶段已发 SSE 心跳后，HTTP 状态已固定为 200')
  await assert.rejects(
    () => transportConflict.text(),
    /terminated|aborted|socket|closed/i,
    '上游改为非流式响应时应断开 SSE 连接交给通用客户端重试，不得把 JSON 拼进 SSE'
  )
  clearTimeout(conflictReleaseTimer)
  heldConflictSlot.release()
  assert.equal(hits.length, 3, 'SSE/非流式传输冲突不得按响应类型切换上游账户')

  const accountAfter = repositories.findAccountForTest(account.id, access)
  assert.equal(accountAfter?.status, 'active', '通用响应状态和错误类型不得修改账号状态')
  assert.equal(accountAfter?.schedulable, true, '通用响应不得把账号改为不可调度')

  const codexStream = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-generic-opaque-${Date.now()}` })
    },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'known codex stream event', stream: true })
  })
  const codexStreamText = await codexStream.text()
  assert.equal(codexStream.status, 200)
  assert.match(codexStreamText, /upstream_retryable_error/, '明确 Codex 画像应继续使用专用协议可重试信号')
  assert.equal(hits.length, 4, '明确客户端语义处理也不得在账号耗尽后无限重派')

  console.log('gateway generic upstream opaque regression passed')
} finally {
  await closeServer(appServer)
  await closeServer(upstreamServer)
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1')
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null)
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
