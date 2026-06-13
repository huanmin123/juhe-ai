import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-compatible-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'openai-compatible-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'openai-compatible-gateway-e2e-secret'
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
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
let upstreamHitCount = 0
let upstreamAuthorization = ''
let upstreamPath = ''
let upstreamRequestBody = ''

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createOpenAICompatibleUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({
      name: '通用 OpenAI 兼容网关 E2E 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '通用 OpenAI 兼容网关 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-openai-compatible-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: '通用 OpenAI 兼容网关 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{ role: 'user', content: 'hello generic openai provider' }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `通用 openai 供应商网关请求应成功，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
    assert.equal(body.choices?.[0]?.message?.content, 'generic openai provider ok')
    assert.equal(upstreamHitCount, 1, '通用 openai 供应商应命中一次 mock 上游')
    assert.equal(upstreamPath, '/v1/chat/completions')
    assert.equal(upstreamAuthorization, 'Bearer sk-openai-compatible-upstream')
    assert.match(upstreamRequestBody, /generic openai provider/)

    const updated = repositories.findAccountSummary(account.id, access)
    assert.equal(updated?.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, '命中账号应保持通用 openai providerCode')
    assert.equal(updated?.status, 'active', '成功请求不应改写账号状态')
    assert.equal(updated?.schedulable, true, '成功请求不应关闭账号调度')

    console.log('openai compatible gateway e2e regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createOpenAICompatibleUpstream(): http.Server {
  return http.createServer((req, res) => {
    upstreamHitCount += 1
    upstreamPath = req.url ?? ''
    upstreamAuthorization = String(req.headers.authorization ?? '')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      upstreamRequestBody = Buffer.concat(chunks).toString('utf8')
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-openai-compatible-e2e',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'generic openai provider ok' },
            finish_reason: 'stop'
          }
        ],
        usage: {
          input_tokens: 3,
          output_tokens: 4,
          total_tokens: 7
        }
      }))
    })
  })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
