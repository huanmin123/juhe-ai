import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-request-failure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'upstream-request-failure.sqlite3')
runtimeConfig.secret = 'upstream-request-failure-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  cryptoModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../modules/gateway/openai-gateway.routes.js'),
  import('../shared/request-context.js'),
  import('../storage/database.js'),
  import('../storage/crypto.js'),
  import('../storage/repositories.js'),
  import('../storage/settings.repository.js'),
  import('../modules/gateway/gateway-runtime-cache.service.js'),
  import('../modules/gateway/gateway-account-side-effects.service.js'),
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
    settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createRejectedRequestUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({ name: '请求级失败回归分组', providerCode: 'openai', enabled: true })
    const firstAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '01-请求级失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-1',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const secondAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '02-请求级失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-2',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const apiKey = createRegressionApiKey(group.id)

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
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'bad tool state' }],
        stream: false
      })
    })
    const responseText = await response.text()

    assert.equal(response.status, 400, `请求级失败应把上游 400 返回客户端，实际 HTTP ${response.status}: ${responseText}`)
    assert.equal(responseText, rejectedRequestBody, `客户端收到的错误体应与上游原文一致：${responseText}`)
    assert.equal(response.headers.get('content-type'), 'application/json; charset=utf-8', '客户端错误响应应保留上游 content-type')
    assert.equal(upstreamHitCount, 2, `同一错误应只用两个账号确认后停止，实际上游命中 ${upstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '请求级失败不应本地屏蔽账号')

    usageRecordQueue.flushAllUsageRecordQueue()
    const accounts = repositories.listAccounts()
    for (const account of [firstAccount, secondAccount]) {
      const updated = accounts.find((item) => item.id === account.id)
      assert(updated, `账号 ${account.name} 不存在`)
      assert.equal(updated.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
      assert.equal(updated.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
      assert.equal(updated.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
      assert.equal(updated.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
    }

    console.log('请求级上游失败回归通过：两个账号返回一致错误时直接返回客户端，账号不冷却、不本地屏蔽、不继续扫池')
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

let upstreamHitCount = 0
const rejectedRequestMessage = 'No tool output found for function call fc_request_failure_regression.'
const rejectedRequestBody = JSON.stringify({
  error: {
    message: rejectedRequestMessage,
    type: 'invalid_request_error',
    code: null
  }
})

function createRejectedRequestUpstream(): http.Server {
  return http.createServer((_req, res) => {
    upstreamHitCount += 1
    res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
    res.end(rejectedRequestBody)
  })
}

function createRegressionApiKey(groupId: string): { id: string; key: string } {
  const key = 'sk-request-failure-regression'
  const id = databaseModule.newId('key')
  const now = databaseModule.nowIso()
  databaseModule.getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, system_account_id, name, description, key_hash, key_prefix, key_secret_encrypted, status, group_id, group_authorization_id, expires_at, quota_limits_json, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      'sys_admin',
      '请求级失败回归 Key',
      null,
      cryptoModule.hashSecret(key),
      key.slice(0, 8),
      cryptoModule.encryptJson({ key }),
      'active',
      groupId,
      null,
      null,
      null,
      '[]',
      now,
      now
    )
  return { id, key }
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

await main()
