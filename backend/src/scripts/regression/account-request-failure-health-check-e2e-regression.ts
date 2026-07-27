import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-request-failure-health-check-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'request-failure-health-check-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  { triggerAccountHealthCheckNow },
  { getAccountHealthCheckQueueSnapshot },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/background/background-jobs.js'),
  import('../../modules/background/account-health-check.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const regressionModel = 'gpt-5.5'
const concurrentRequestCount = 32
const originalProcessSend = process.send
const triggerPromises: Array<Promise<boolean>> = []
let requestFailureDispatchCount = 0
let upstreamGatewayHits = 0
let upstreamCatalogHits = 0
let upstreamProbeHits = 0
let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  updateSystemSettingsForTest({
    temporaryUnschedulableRetryAttempts: 0,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    accountHealthCheckBatchSize: 10,
    accountHealthCheckIntervalHours: 1,
    accountHealthCheckJitterMinutes: 0,
    accountHealthCheckFailureThreshold: 3,
    defaultTemporaryUnschedulableMinutes: 5,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  gatewayCache.clearGatewayRuntimeCache()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

  const group = repositories.createGroup({
    name: '请求失败健康确认 E2E 分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '请求失败健康确认 E2E 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-request-failure-health-check-e2e',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'responses_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [regressionModel],
    healthCheckModel: regressionModel,
    healthCheckEndpointMode: 'responses_json'
  }, access)
  activateFixtureAccount(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '请求失败健康确认 E2E Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'E2E API Key 未返回明文密钥')

  process.send = ((message: unknown, ...args: unknown[]) => {
    const callback = args.find((item): item is (error: Error | null) => void => typeof item === 'function')
    callback?.(null)
    if (
      message
      && typeof message === 'object'
      && 'type' in message
      && message.type === 'background_worker_account_health_check_trigger'
      && 'accountId' in message
      && message.accountId === account.id
      && 'reason' in message
      && message.reason === 'request_failure'
    ) {
      requestFailureDispatchCount += 1
      triggerPromises.push(triggerAccountHealthCheckNow(account.id, 'request_failure'))
    }
    return true
  }) as typeof process.send

  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  const responses = await Promise.all(Array.from({ length: concurrentRequestCount }, (_value, index) =>
    requestChatCompletion(gatewayBaseUrl, apiKey.key!, `request-failure-storm-${index}`)))
  assert.equal(responses.every((response) => response.status === 503), true, '并发失败请求必须统一返回可重试 503')
  assert.equal(responses.every((response) => /upstream_retryable_error/.test(response.text)), true, '并发失败响应必须保持统一脱敏错误')
  assert.equal(requestFailureDispatchCount, 1, '32 路并发失败在 5 分钟窗口内只能投递一次账户健康确认')

  const triggerResults = await Promise.all(triggerPromises)
  assert.deepEqual(triggerResults, [true], '唯一请求失败任务必须成功进入健康检查队列')
  const transitioned = await waitForCondition(() => {
    const current = repositories.findAccountForTest(account.id, access)
    const queue = getAccountHealthCheckQueueSnapshot()
    return current?.status === 'temporary_unavailable' && queue.pendingCount === 0 && queue.runningCount === 0
  }, 10_000)

  if (!transitioned) {
    const current = repositories.findAccountForTest(account.id, access)
    const candidate = repositories.findOpenAIAccountForGroup(
      group.id,
      account.id,
      access.systemAccountId,
      { includeUnavailable: true, ignoreAvailability: true }
    )
    assert.fail(`请求失败独立探针未完成状态收敛：${JSON.stringify({
      requestFailureDispatchCount,
      triggerResults,
      upstreamGatewayHits,
      upstreamCatalogHits,
      upstreamProbeHits,
      queue: getAccountHealthCheckQueueSnapshot(),
      account: current && {
        status: current.status,
        schedulable: current.schedulable,
        lastHealthCheckAt: current.lastHealthCheckAt,
        lastHealthSuccessAt: current.lastHealthSuccessAt,
        healthCheckFailureCount: current.healthCheckFailureCount,
        lastHealthCheckStatusCode: current.lastHealthCheckStatusCode,
        lastHealthCheckErrorCode: current.lastHealthCheckErrorCode,
        lastHealthCheckErrorMessage: current.lastHealthCheckErrorMessage,
        cooldownUntil: current.cooldownUntil
      },
      candidate: candidate && {
        providerCode: candidate.providerCode,
        providerProtocolProfileId: candidate.providerProtocolProfileId,
        protocolCode: candidate.protocolCode,
        protocolVersion: candidate.protocolVersion,
        type: candidate.type,
        clientCompatibility: candidate.clientCompatibility,
        supportedEndpointModes: candidate.supportedEndpointModes,
        supportedModels: candidate.supportedModels,
        healthCheckModel: candidate.healthCheckModel,
        healthCheckEndpointMode: candidate.healthCheckEndpointMode,
        baseUrl: candidate.baseUrl,
        boundGroupId: candidate.boundGroupId
      }
    })}`)
  }

  const failedAccount = repositories.findAccountForTest(account.id, access)
  assert.equal(failedAccount?.status, 'temporary_unavailable', '独立探针确认失败后账户必须进入 temporary_unavailable')
  assert.equal(failedAccount?.schedulable, true, 'temporary_unavailable 应保留自动恢复资格，不能改成管理员停用')
  assert.equal(upstreamCatalogHits, 1, '独立探针只应执行一次模型目录预检')
  assert.equal(upstreamProbeHits, 3, '独立探针应按统一三档诊断执行三次固定健康端点请求')
  assert(upstreamGatewayHits > 0, '并发风暴必须真实命中 mock 上游业务端点')

  gatewayCache.clearGatewayRuntimeCache()
  assert.equal(
    repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId),
    undefined,
    'temporary_unavailable 账户必须从普通路由候选中排除'
  )
  assert(
    repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { includeUnavailable: true }),
    '诊断和恢复入口必须仍能显式读取 temporary_unavailable 账户'
  )

  console.log(JSON.stringify({
    message: 'request failure health check e2e passed',
    concurrentRequestCount,
    requestFailureDispatchCount,
    upstreamGatewayHits,
    upstreamCatalogHits,
    upstreamProbeHits,
    finalStatus: failedAccount?.status,
    schedulable: failedAccount?.schedulable
  }))
} finally {
  process.send = originalProcessSend
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const pathname = new URL(req.url ?? '/', 'http://127.0.0.1').pathname
    if (req.method === 'GET' && pathname === '/v1/models') {
      upstreamCatalogHits += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ object: 'list', data: [{ id: regressionModel, object: 'model' }] }))
      return
    }
    if (req.method === 'POST' && pathname === '/v1/responses') {
      upstreamProbeHits += 1
    } else if (req.method === 'POST' && pathname === '/v1/chat/completions') {
      upstreamGatewayHits += 1
    }
    res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      error: {
        message: 'mock upstream generic failure',
        code: 'provider_defined_error',
        type: 'provider_defined_error'
      }
    }))
  })
}

function activateFixtureAccount(accountId: string): void {
  assert(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 1,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), 'E2E 账户应能通过初始健康检查激活')
}

async function requestChatCompletion(baseUrl: string, apiKey: string, content: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: regressionModel,
      messages: [{ role: 'user', content }],
      stream: false
    })
  })
  return { status: response.status, text: await response.text() }
}

function updateSystemSettingsForTest(settings: Record<string, unknown>): void {
  const now = new Date().toISOString()
  const statement = databaseModule.getBusinessDatabase().prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES ('sys_admin', ?, ?, ?)
    ON CONFLICT(system_account_id, key) DO UPDATE SET
      value_json = excluded.value_json,
      updated_at = excluded.updated_at
  `)
  for (const [key, value] of Object.entries(settings)) {
    statement.run(key, JSON.stringify(value), now)
  }
}

async function waitForCondition(predicate: () => boolean, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return true
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 20))
  }
  return false
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return address.port
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
