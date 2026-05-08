import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-stream-first-output-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'stream-first-output.sqlite3')
runtimeConfig.secret = 'stream-first-output-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  apiKeyRepository,
  settingsRepository,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../modules/gateway/openai-gateway.routes.js'),
  import('../shared/request-context.js'),
  import('../storage/database.js'),
  import('../storage/repositories.js'),
  import('../storage/api-key.repository.js'),
  import('../storage/settings.repository.js'),
  import('../modules/gateway/gateway-runtime-cache.service.js'),
  import('../modules/gateway/usage-record-queue.service.js'),
  import('../modules/audit-logs/audit-log-queue.service.js')
])

type RawBodyRequest = Request & { rawBody?: Buffer }

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    settingsRepository.updateSettings({
      streamCircuitBreakerEnabled: true,
      streamRequestTimeoutSeconds: 10,
      streamIdleTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createSlowNonOutputStreamUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({ name: '首输出超时回归分组', providerCode: 'openai', enabled: true })
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: '首输出超时回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-stream-first-output-timeout',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const apiKey = apiKeyRepository.createApiKeyRecord({
      name: '首输出超时回归 Key',
      groupId: group.id,
      status: 'active'
    })
    assert(apiKey.key, '临时 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const startedAt = Date.now()
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json',
        accept: 'text/event-stream'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        input: 'hi',
        stream: true
      })
    })
    assert.equal(response.status, 200)
    assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')

    const streamText = await response.text()
    const durationMs = Date.now() - startedAt
    assert(streamText.includes('response.created'), `客户端未收到上游首个非输出事件：${streamText}`)
    assert(streamText.includes('response.failed'), `客户端未收到网关失败事件：${streamText}`)
    assert(streamText.includes('未返回首个有效输出'), `失败事件未说明首输出超时：${streamText}`)
    assert(durationMs < 15000, `首输出超时没有及时结束，耗时 ${durationMs}ms`)

    usageRecordQueue.flushAllUsageRecordQueue()
    await waitForFailedUsageRecord(account.id)

    console.log('流式首输出超时回归通过：非输出事件已透传，首个有效输出超时后返回 response.failed')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function createSlowNonOutputStreamUpstream(): http.Server {
  return http.createServer((req, res) => {
    if (req.url !== '/v1/responses') {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ object: 'list', data: [] }))
      return
    }

    res.writeHead(200, {
      'content-type': 'text/event-stream; charset=utf-8',
      'cache-control': 'no-cache',
      connection: 'keep-alive'
    })
    res.write('event: response.created\n')
    res.write('data: {"type":"response.created","response":{"id":"resp_regression","status":"in_progress"}}\n\n')

    const interval = setInterval(() => {
      res.write(': keep-alive\n\n')
    }, 100)
    res.on('close', () => clearInterval(interval))
  })
}

function captureGatewayRawBody(req: RawBodyRequest, _res: ExpressResponse, next: NextFunction): void {
  const rawBody = Buffer.isBuffer(req.body) ? Buffer.from(req.body) : Buffer.alloc(0)
  req.rawBody = rawBody
  const contentType = req.headers['content-type'] ?? ''
  if (rawBody.length > 0 && String(contentType).toLowerCase().includes('json')) {
    try {
      req.body = JSON.parse(rawBody.toString('utf8')) as unknown
    } catch {
      req.body = undefined
    }
  } else {
    req.body = undefined
  }
  next()
}

async function waitForFailedUsageRecord(accountId: string): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const records = repositories.listUsageRecords(undefined, { result: 'failed', page: 1, pageSize: 20 })
    if (records.items.some((record) => (
      record.accountId === accountId
      && record.success === false
      && record.errorMessage?.includes('首个有效输出')
    ))) {
      return
    }
    await sleep(100)
  }
  throw new Error('未找到首输出超时失败使用记录')
}

function listen(server: http.Server): Promise<void> {
  if (!server.listening) {
    server.listen(0, '127.0.0.1')
  }
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

main().catch((error) => {
  console.error('\n流式首输出超时回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
