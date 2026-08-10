import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  installRecoverableUnavailableWaitCoordinatorForTest,
  recoverableUnavailableWaitCoordinatorSnapshotForTest
} from '../../modules/gateway/runtime/recoverable-unavailable-wait.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { logger } from '../../shared/logger.js'

interface GatewayKeyFixture {
  id: string
  key: string
}

interface GatewayPoolFixture {
  accountId: string
  groupId: string
  keys: GatewayKeyFixture[]
}

interface GatewayResponseResult {
  status: number
  text: string
  elapsedMs: number
}

interface AccountStateFingerprint {
  status: string
  schedulable: boolean
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  apiKeyRuntime?: unknown
  apiKeyRuntimeDetails?: unknown
  circuitSummary?: unknown
}

const requestCount = 64
const scopeWaiterLimit = 8
const globalWaiterLimit = 12
const sharedWaitUpperBoundMs = 6_000
const tempRoot = resolve(
  tmpdir(),
  `juhe-ai-gateway-recoverable-wait-storm-${Date.now()}-${Math.random().toString(16).slice(2)}`
)
runtimeConfig.databasePath = join(tempRoot, 'gateway-recoverable-wait-storm.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-recoverable-wait-storm-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  readWorkerPool,
  accountConcurrency
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../shared/account-concurrency.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
let upstreamHitCount = 0
let restoreCoordinator: (() => void) | undefined
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  settingsRepository.updateSettings({
    noAvailableAccountWaitTimeoutSeconds: 10,
    textStreamIdleTimeoutSeconds: 30,
    textFirstResponseTimeoutSeconds: 30,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
    const localSuppressionPool = createGatewayPool(
      '等待风暴单 scope 软屏蔽',
      'sk-wait-storm-local-suppression',
      upstreamBaseUrl,
      1
    )
    const cooldownPool = createGatewayPool(
      '等待风暴多 scope 可恢复冷却',
      'sk-wait-storm-cooldown',
      upstreamBaseUrl,
      4
    )

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertScopeLimitAndSingleTimer(baseUrl, localSuppressionPool)
    await assertGlobalLimitAcrossScopes(baseUrl, cooldownPool)
    assert.equal(upstreamHitCount, 0, '全池可恢复/软阻断等待风暴不得发起任何上游请求')
    assert.deepEqual(
      recoverableUnavailableWaitCoordinatorSnapshotForTest(),
      { scopeCount: 0, waiterCount: 0, timerCount: 0 },
      '所有批次完成后 waiter、timer 和 scope 必须清零'
    )
    assert.deepEqual(accountConcurrency.snapshotAccountConcurrency(), {}, '所有批次完成后账户并发槽必须清零')

    console.log('gateway recoverable unavailable wait storm mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  restoreCoordinator?.()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountConcurrency.clearAccountConcurrency()
  usageRecordQueue.clearUsageRecordQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertScopeLimitAndSingleTimer(baseUrl: string, fixture: GatewayPoolFixture): Promise<void> {
  const baselineAccount = accountFingerprint(fixture.accountId)
  const baselineKey = keyFingerprint(fixture.keys[0]!.id)
  const baselineUpstreamHits = upstreamHitCount
  accountSideEffects.suppressGatewayAccountLocallyForTest(
    fixture.accountId,
    30_000,
    'mock ai 单 scope 软屏蔽等待风暴'
  )
  restoreCoordinator = installRecoverableUnavailableWaitCoordinatorForTest({
    maxWaitersPerScope: scopeWaiterLimit,
    maxWaitersGlobal: requestCount
  })

  const admittedRequests = Array.from({ length: scopeWaiterLimit }, (_, index) => (
    postChat(baseUrl, fixture.keys[0]!.key, `scope wait storm ${index}`)
  ))
  let admittedResults: GatewayResponseResult[] = []
  let overflowResults: GatewayResponseResult[] = []
  let overflowRequests: Array<Promise<GatewayResponseResult>> = []
  try {
    const saturatedSnapshot = await waitForCoordinatorSnapshot(
      (snapshot) => snapshot.waiterCount === scopeWaiterLimit,
      '单 scope 等待者未达到测试上限'
    )
    assert.deepEqual(
      saturatedSnapshot,
      { scopeCount: 1, waiterCount: scopeWaiterLimit, timerCount: 1 },
      '同一 scope 即使有多个等待者，也只能保留一个 timer'
    )
    overflowRequests = Array.from({ length: requestCount - scopeWaiterLimit }, (_, index) => (
      postChat(baseUrl, fixture.keys[0]!.key, `scope wait storm overflow ${index}`)
    ))
    overflowResults = await Promise.all(overflowRequests)
    admittedResults = await Promise.all(admittedRequests)
  } catch (error) {
    await Promise.allSettled([...admittedRequests, ...overflowRequests])
    throw error
  } finally {
    restoreCoordinator()
    restoreCoordinator = undefined
  }

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  assertStableHandoffResults({ admittedResults, overflowResults, label: '单 scope 上限' })
  assert.equal(upstreamHitCount, baselineUpstreamHits, '单 scope 软屏蔽风暴不得命中上游')
  assert.deepEqual(accountFingerprint(fixture.accountId), baselineAccount, '单 scope 风暴不得改变账户状态')
  assert.deepEqual(keyFingerprint(fixture.keys[0]!.id), baselineKey, '单 scope 风暴不得禁用或改变网关 Key')
  assert.equal(accountConcurrency.getAccountCurrentConcurrency(fixture.accountId), 0, '单 scope 风暴不得遗留账户并发槽')
  assert.deepEqual(
    recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    '单 scope 风暴结束后协调资源必须清零'
  )
}

async function assertGlobalLimitAcrossScopes(baseUrl: string, fixture: GatewayPoolFixture): Promise<void> {
  const cooldownUntil = new Date(Date.now() + 2_900).toISOString()
  const limited = repositories.markAccountCooldown(
    fixture.accountId,
    cooldownUntil,
    'mock ai 多 scope 可恢复冷却等待风暴',
    'rate_limited'
  )
  assert.equal(limited?.status, 'rate_limited', '可恢复冷却测试账户应进入 rate_limited')
  gatewayCache.clearGatewayRuntimeCache()
  const baselineAccount = accountFingerprint(fixture.accountId)
  const baselineKeys = fixture.keys.map((key) => keyFingerprint(key.id))
  const baselineUpstreamHits = upstreamHitCount

  restoreCoordinator = installRecoverableUnavailableWaitCoordinatorForTest({
    maxWaitersPerScope: requestCount,
    maxWaitersGlobal: globalWaiterLimit
  })

  const admittedRequests = fixture.keys.map((key, index) => (
    postChat(baseUrl, key.key, `global wait storm seed ${index}`)
  ))
  let admittedResults: GatewayResponseResult[] = []
  let overflowResults: GatewayResponseResult[] = []
  let overflowRequests: Array<Promise<GatewayResponseResult>> = []
  try {
    await waitForCoordinatorSnapshot(
      (snapshot) => snapshot.scopeCount === fixture.keys.length && snapshot.waiterCount === fixture.keys.length,
      '多 scope 全局等待风暴未建立初始 scope'
    )
    admittedRequests.push(...Array.from({ length: globalWaiterLimit - admittedRequests.length }, (_, index) => {
      const key = fixture.keys[index % fixture.keys.length]!
      return postChat(baseUrl, key.key, `global wait storm admitted ${index}`)
    }))
    const saturatedSnapshot = await waitForCoordinatorSnapshot(
      (snapshot) => snapshot.waiterCount === globalWaiterLimit,
      '多 scope 等待者未达到全局测试上限'
    )
    assert.deepEqual(
      saturatedSnapshot,
      { scopeCount: fixture.keys.length, waiterCount: globalWaiterLimit, timerCount: fixture.keys.length },
      '全局容量饱和时每个活跃 scope 仍只能保留一个 timer'
    )
    overflowRequests = Array.from({ length: requestCount - globalWaiterLimit }, (_, index) => {
      const key = fixture.keys[index % fixture.keys.length]!
      return postChat(baseUrl, key.key, `global wait storm overflow ${index}`)
    })
    overflowResults = await Promise.all(overflowRequests)
    admittedResults = await Promise.all(admittedRequests)
  } catch (error) {
    await Promise.allSettled([...admittedRequests, ...overflowRequests])
    throw error
  } finally {
    restoreCoordinator()
    restoreCoordinator = undefined
  }

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  assertStableHandoffResults({ admittedResults, overflowResults, label: 'global 上限' })
  assert.equal(upstreamHitCount, baselineUpstreamHits, '多 scope 可恢复冷却风暴不得命中上游')
  assert.deepEqual(accountFingerprint(fixture.accountId), baselineAccount, '多 scope 风暴不得把可恢复账户升级为死亡状态')
  assert.deepEqual(
    fixture.keys.map((key) => keyFingerprint(key.id)),
    baselineKeys,
    '多 scope 风暴不得禁用或改变任一网关 Key'
  )
  assert.equal(accountConcurrency.getAccountCurrentConcurrency(fixture.accountId), 0, '多 scope 风暴不得遗留账户并发槽')
  assert.deepEqual(
    recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    '多 scope 风暴结束后协调资源必须清零'
  )
}

function assertStableHandoffResults(input: {
  admittedResults: GatewayResponseResult[]
  overflowResults: GatewayResponseResult[]
  label: string
}): void {
  const { admittedResults, overflowResults, label } = input
  const results = [...admittedResults, ...overflowResults]
  assert.equal(results.length, requestCount)
  for (const result of results) {
    assert.equal(result.status, 503, `${label}请求应统一交还客户端重试：${result.text}`)
    assert.match(result.text, /upstream_retryable_error/, `${label}请求应返回稳定可重试错误码`)
    assert(result.elapsedMs < sharedWaitUpperBoundMs, `${label}请求不得超过共享最大等待预算，实际 ${result.elapsedMs}ms`)
  }
  assert(admittedResults.length > 0, `${label}必须保留已准入等待者样本`)
  assert(overflowResults.length > 0, `${label}必须保留容量外快速返回样本`)
}

function createGatewayPool(
  label: string,
  upstreamApiKey: string,
  upstreamBaseUrl: string,
  keyCount: number
): GatewayPoolFixture {
  const group = repositories.createGroup({
    name: `${label}分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}账户`,
    type: 'api_key',
    credentials: {
      api_key: upstreamApiKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  activateAccountAfterBackgroundCheck(account.id)
  const keys = Array.from({ length: keyCount }, (_, index) => {
    const record = createApiKeyRecordWithRouteStrategy(repositories, {
      name: `${label}网关 Key ${index + 1}`,
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(record.key, `${label}网关 Key ${index + 1} 未返回明文密钥`)
    return { id: record.id, key: record.key }
  })
  return { accountId: account.id, groupId: group.id, keys }
}

function activateAccountAfterBackgroundCheck(accountId: string): void {
  const changed = repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  assert.equal(changed, true, `后台健康检查激活账户失败：${accountId}`)
}

function accountFingerprint(accountId: string): AccountStateFingerprint {
  const account = repositories.findAccountForTest(accountId, access)
  assert(account, `未找到测试账户：${accountId}`)
  return {
    status: account.status,
    schedulable: account.schedulable,
    cooldownUntil: account.cooldownUntil,
    lastErrorCode: account.lastErrorCode,
    lastErrorMessage: account.lastErrorMessage,
    apiKeyRuntime: account.apiKeyRuntime,
    apiKeyRuntimeDetails: account.apiKeyRuntimeDetails,
    circuitSummary: account.circuitSummary
  }
}

function keyFingerprint(apiKeyId: string): { status: string; routeStrategyId: string; routeStrategyStatus?: string } {
  const key = repositories.findApiKeySummary(apiKeyId, access)
  assert(key, `未找到测试网关 Key：${apiKeyId}`)
  return {
    status: key.status,
    routeStrategyId: key.routeStrategyId,
    routeStrategyStatus: key.routeStrategyStatus
  }
}

async function postChat(baseUrl: string, apiKey: string, content: string): Promise<GatewayResponseResult> {
  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false
    })
  })
  return {
    status: response.status,
    text: await response.text(),
    elapsedMs: Date.now() - startedAt
  }
}

async function waitForCoordinatorSnapshot(
  predicate: (snapshot: ReturnType<typeof recoverableUnavailableWaitCoordinatorSnapshotForTest>) => boolean,
  message: string
): Promise<ReturnType<typeof recoverableUnavailableWaitCoordinatorSnapshotForTest>> {
  const deadlineAt = Date.now() + 2_000
  while (Date.now() < deadlineAt) {
    const snapshot = recoverableUnavailableWaitCoordinatorSnapshotForTest()
    if (predicate(snapshot)) return snapshot
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
  assert.fail(`${message}：${JSON.stringify(recoverableUnavailableWaitCoordinatorSnapshotForTest())}`)
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((_req, res) => {
    upstreamHitCount += 1
    res.writeHead(500, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({ error: { message: '等待风暴期间不应命中上游' } }))
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
