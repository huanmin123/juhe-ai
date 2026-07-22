import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-request-failure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'upstream-request-failure.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'upstream-request-failure-secret'
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
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/response/failure-dispatch.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const timeoutError = Object.assign(new Error(''), { code: 'ETIMEDOUT' })
assert.equal(
  gatewayFailureDispatch.formatUpstreamRequestErrorMessage(timeoutError),
  '请求失败：ETIMEDOUT',
  '空错误消息应回退到错误码，避免最后一次尝试文案只剩空白'
)
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const opaqueFailureBody = JSON.stringify({
  error: {
    message: 'Invalid value for model level: expected one of low, medium, high.',
    type: 'invalid_request_error',
    code: null
  }
})

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  let closedTransportServer: http.Server | undefined
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)

    const upstreamAuthorizations: string[] = []
    upstreamServer = http.createServer((req, res) => {
      upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
      res.writeHead(422, {
        'content-type': 'application/json; charset=utf-8',
        'x-upstream-contract': 'opaque'
      })
      res.end(opaqueFailureBody)
    })
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const opaqueGroup = repositories.createGroup({ name: '不透明非幂等失败回归分组', providerCode: 'gpt', enabled: true }, access)
    const opaqueAccounts = [
      createAccount(opaqueGroup.id, '01-不透明失败账户', 'sk-opaque-failure-first', upstreamBaseUrl),
      createAccount(opaqueGroup.id, '02-不透明后备账户', 'sk-opaque-failure-fallback', upstreamBaseUrl)
    ]
    const opaqueApiKey = createRegressionApiKey(opaqueGroup.id, 'sk-opaque-request-failure-regression')

    closedTransportServer = http.createServer()
    await listen(closedTransportServer)
    const closedTransportBaseUrl = `http://127.0.0.1:${serverPort(closedTransportServer)}/v1`
    await closeServer(closedTransportServer)
    closedTransportServer = undefined
    const transportGroup = repositories.createGroup({ name: '不透明传输失败回归分组', providerCode: 'gpt', enabled: true }, access)
    const transportAccounts = [
      createAccount(transportGroup.id, '01-传输失败账户', 'sk-transport-failure-first', closedTransportBaseUrl),
      createAccount(transportGroup.id, '02-传输失败后备账户', 'sk-transport-failure-fallback', closedTransportBaseUrl)
    ]
    const transportApiKey = createRegressionApiKey(transportGroup.id, 'sk-opaque-transport-failure-regression')

    gatewayCache.clearGatewayRuntimeCache()
    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverPort(appServer)}`

    const invalidJsonHitsBefore = upstreamAuthorizations.length
    const invalidJsonResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${opaqueApiKey.key}`, 'content-type': 'application/json' },
      body: '{"model":"gpt-4o-mini",'
    })
    const invalidJsonText = await invalidJsonResponse.text()
    assert.equal(invalidJsonResponse.status, 400, `无效 JSON 应由网关直接拒绝：${invalidJsonText}`)
    assert.match(invalidJsonText, /请求体不是合法 JSON/)
    assert.equal(upstreamAuthorizations.length, invalidJsonHitsBefore, '无效 JSON 不应命中上游')

    const opaqueResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${opaqueApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'opaque non-idempotent failure must not replay' }],
        stream: false
      })
    })
    const opaqueResponseText = await opaqueResponse.text()
    assert.equal(opaqueResponse.status, 422, `通用 POST 应原样返回首个完整上游非 2xx：${opaqueResponseText}`)
    assert.equal(opaqueResponseText, opaqueFailureBody, '通用 POST 应保留不透明上游错误体')
    assert.equal(opaqueResponse.headers.get('x-upstream-contract'), 'opaque', '通用 POST 应保留不透明上游响应头')
    assert.deepEqual(upstreamAuthorizations, ['Bearer sk-opaque-failure-first'], '完整 HTTP 非 2xx 后不得换 Key 或跨账号重放 POST')
    assertAccountsRemainAvailable(opaqueAccounts, '不透明 HTTP 失败')

    const transportResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${transportApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'opaque transport failure must remain request scoped' }],
        stream: false
      })
    })
    const transportResponseText = await transportResponse.text()
    assert.equal(transportResponse.status, 503, `未收到上游响应头的传输失败应返回统一网关错误：${transportResponseText}`)
    assert.match(transportResponseText, /upstream_retryable_error/)
    assertAccountsRemainAvailable(transportAccounts, '不透明传输失败')

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assert.equal(Object.keys(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()).length, 0, '不透明用户请求失败不得写账户运行态屏障')
    console.log('上游请求失败回归通过：通用 POST 完整非 2xx 原样返回且不重放，传输失败保持请求级并返回统一可重试错误，失败不会污染账户状态')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    await closeServer(closedTransportServer)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    await removeTempRoot()
  }
}

function createAccount(groupId: string, name: string, apiKey: string, baseUrl: string) {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: { api_key: apiKey, base_url: baseUrl },
    groupId,
    supportedModels: ['gpt-4o-mini'],
    status: 'active',
    schedulable: true
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  return account
}

function createRegressionApiKey(groupId: string, key: string) {
  return createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${key}-name`,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active',
    description: 'upstream request failure regression'
  }, access)
}

function assertAccountsRemainAvailable(accounts: Array<{ id: string; name: string }>, reason: string): void {
  for (const account of accounts) {
    const current = repositories.findAccountForTest(account.id, access)
    assert.equal(current?.status, 'active', `${reason}：${account.name} 应保持 active`)
    assert.equal(current?.schedulable, true, `${reason}：${account.name} 应保持可调度`)
    assert.equal(current?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${reason}：${account.name} 的 Key 不应被写为临时不可用`)
  }
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) throw error
      await delay(200)
    }
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
