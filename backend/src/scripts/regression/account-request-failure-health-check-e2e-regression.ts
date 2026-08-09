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
const concurrentRequestsPerFailureCase = 16
const completeHttpFailureCases = [
  {
    label: 'forbidden_balance',
    statusCode: 403,
    errorCode: 'insufficient_balance',
    message: 'mock insufficient balance'
  },
  {
    label: 'rate_limited',
    statusCode: 429,
    errorCode: 'provider_rate_limited',
    message: 'mock provider rate limited'
  },
  {
    label: 'upstream_unavailable',
    statusCode: 503,
    errorCode: 'provider_unavailable',
    message: 'mock upstream unavailable'
  }
] as const
type CompleteHttpFailureCase = (typeof completeHttpFailureCases)[number]
const originalProcessSend = process.send
const triggerPromises: Array<Promise<boolean>> = []
let requestFailureDispatchCount = 0
let upstreamGatewayHits = 0
let upstreamCatalogHits = 0
let upstreamProbeHits = 0
let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined
const trackedAccountIds = new Set<string>()
const upstreamFailureByAccountApiKey = new Map<string, CompleteHttpFailureCase>()

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
  process.send = ((message: unknown, ...args: unknown[]) => {
    const callback = args.find((item): item is (error: Error | null) => void => typeof item === 'function')
    callback?.(null)
    if (
      message
      && typeof message === 'object'
      && 'type' in message
      && message.type === 'background_worker_account_health_check_trigger'
      && 'accountId' in message
      && typeof message.accountId === 'string'
      && trackedAccountIds.has(message.accountId)
      && 'reason' in message
      && message.reason === 'request_failure'
    ) {
      requestFailureDispatchCount += 1
      triggerPromises.push(triggerAccountHealthCheckNow(message.accountId, 'request_failure'))
    }
    return true
  }) as typeof process.send

  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`
  const scenarios = []
  for (const failureCase of completeHttpFailureCases) {
    scenarios.push(await runCompleteHttpFailureScenario({
      failureCase,
      groupId: group.id,
      upstreamBaseUrl,
      gatewayBaseUrl,
      repositories,
      gatewayCache,
      getAccountHealthCheckQueueSnapshot
    }))
  }
  assert.equal(upstreamCatalogHits, 0, '独立探针不得请求上游模型目录')
  assert(upstreamProbeHits >= completeHttpFailureCases.length, '每种完整 HTTP 失败必须至少执行一次固定健康端点确认')
  assert(upstreamGatewayHits >= completeHttpFailureCases.length * concurrentRequestsPerFailureCase, '每种失败矩阵都必须真实命中 mock 上游业务端点')

  console.log(JSON.stringify({
    message: 'request failure health check e2e passed',
    concurrentRequestsPerFailureCase,
    requestFailureDispatchCount,
    upstreamGatewayHits,
    upstreamCatalogHits,
    upstreamProbeHits,
    scenarios
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
    const authorization = typeof req.headers.authorization === 'string' ? req.headers.authorization : ''
    const accountApiKey = authorization.replace(/^Bearer\s+/i, '')
    const failureCase = upstreamFailureByAccountApiKey.get(accountApiKey)
    assert(failureCase, `mock 上游未找到账户 API Key 的失败矩阵：${accountApiKey}`)
    res.writeHead(failureCase.statusCode, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      error: {
        message: failureCase.message,
        code: failureCase.errorCode,
        type: failureCase.errorCode
      }
    }))
  })
}

async function runCompleteHttpFailureScenario(input: {
  failureCase: CompleteHttpFailureCase
  groupId: string
  upstreamBaseUrl: string
  gatewayBaseUrl: string
  repositories: typeof import('../../storage/repositories.js')
  gatewayCache: typeof import('../../modules/gateway/runtime/runtime-cache.service.js')
  getAccountHealthCheckQueueSnapshot: typeof import('../../modules/background/account-health-check.service.js').getAccountHealthCheckQueueSnapshot
}): Promise<{ statusCode: number; accountStatus?: string; schedulable?: boolean; requestFailureDispatchCount: number }> {
  const { failureCase, groupId, upstreamBaseUrl, gatewayBaseUrl, repositories, gatewayCache, getAccountHealthCheckQueueSnapshot } = input
  const accountApiKey = `sk-request-failure-health-check-${failureCase.label}`
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: `请求失败健康确认 E2E ${failureCase.statusCode}`,
    type: 'api_key',
    credentials: {
      api_key: accountApiKey,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'responses_json']
    },
    groupId,
    status: 'active',
    schedulable: true,
    supportedModels: [regressionModel],
    healthCheckModel: regressionModel,
    healthCheckEndpointMode: 'responses_json'
  }, access)
  activateFixtureAccount(account.id)
  upstreamFailureByAccountApiKey.set(accountApiKey, failureCase)
  trackedAccountIds.add(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `请求失败健康确认 E2E Key ${failureCase.statusCode}`,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'E2E API Key 未返回明文密钥')

  const initialDispatchCount = requestFailureDispatchCount
  const initialTriggerCount = triggerPromises.length
  const responses = await Promise.all(Array.from({ length: concurrentRequestsPerFailureCase }, (_value, index) =>
    requestChatCompletion(gatewayBaseUrl, apiKey.key!, `${failureCase.label}-${index}`)))
  assert.equal(responses.every((response) => response.status === 503), true, `${failureCase.statusCode} 完整上游失败必须统一返回可重试 503`)
  assert.equal(responses.every((response) => /upstream_retryable_error/.test(response.text)), true, `${failureCase.statusCode} 失败响应必须保持统一脱敏错误`)

  const scenarioDispatchCount = requestFailureDispatchCount - initialDispatchCount
  assert(scenarioDispatchCount >= 1, `${failureCase.statusCode} 并发失败至少应投递一次独立账户健康确认`)
  const triggerResults = await Promise.all(triggerPromises.slice(initialTriggerCount))
  assert.equal(triggerResults.length, scenarioDispatchCount, `${failureCase.statusCode} 每条已投递消息都必须对应一个后台健康检查触发任务`)
  assert(triggerResults.some(Boolean), `${failureCase.statusCode} 首条请求失败任务必须成功进入健康检查队列`)
  const transitioned = await waitForCondition(() => {
    const current = repositories.findAccountForTest(account.id, access)
    const queue = getAccountHealthCheckQueueSnapshot()
    return current?.status === 'temporary_unavailable' && queue.pendingCount === 0 && queue.runningCount === 0
  }, 10_000)
  if (!transitioned) {
    const current = repositories.findAccountForTest(account.id, access)
    assert.fail(`${failureCase.statusCode} 请求失败独立探针未完成状态收敛：${JSON.stringify({
      failureCase,
      scenarioDispatchCount,
      triggerResults,
      queue: getAccountHealthCheckQueueSnapshot(),
      account: current && {
        status: current.status,
        schedulable: current.schedulable,
        healthCheckFailureCount: current.healthCheckFailureCount,
        lastHealthCheckStatusCode: current.lastHealthCheckStatusCode,
        lastHealthCheckErrorCode: current.lastHealthCheckErrorCode,
        lastHealthCheckErrorMessage: current.lastHealthCheckErrorMessage
      }
    })}`)
  }
  const failedAccount = repositories.findAccountForTest(account.id, access)
  assert.equal(failedAccount?.status, 'temporary_unavailable', `${failureCase.statusCode} 独立探针确认失败后账户必须进入 temporary_unavailable`)
  assert.equal(failedAccount?.schedulable, true, `${failureCase.statusCode} temporary_unavailable 应保留自动恢复资格，不能改成管理员停用`)
  gatewayCache.clearGatewayRuntimeCache()
  assert.equal(
    repositories.findOpenAIAccountForGroup(groupId, account.id, access.systemAccountId),
    undefined,
    `${failureCase.statusCode} temporary_unavailable 账户必须从普通路由候选中排除`
  )
  assert(
    repositories.findOpenAIAccountForGroup(groupId, account.id, access.systemAccountId, { includeUnavailable: true }),
    `${failureCase.statusCode} 诊断和恢复入口必须仍能显式读取 temporary_unavailable 账户`
  )
  return {
    statusCode: failureCase.statusCode,
    accountStatus: failedAccount?.status,
    schedulable: failedAccount?.schedulable,
    requestFailureDispatchCount: scenarioDispatchCount
  }
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
