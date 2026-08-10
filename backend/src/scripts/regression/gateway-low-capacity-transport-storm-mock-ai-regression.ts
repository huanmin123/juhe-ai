import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { AccountCircuitRecoveryService } from '../../modules/background/account-circuit-recovery.service.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { gatewayAccountProtocolModelScope } from '../../modules/gateway/runtime/account-circuit.service.js'
import { accountCircuitScopeKey } from '../../modules/gateway/runtime/account-circuit-store.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import {
  clearAccountConcurrency,
  getAccountCurrentConcurrency,
  tryAcquireAccountConcurrency
} from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const requestCount = 64
const model = 'gpt-5.5'
const sharedSessionId = 'shared-broken-session-transport-storm'
const sharedClientIp = '198.51.100.81'
const badKeys = [
  'sk-storm-reset-a',
  'sk-storm-reset-b',
  'sk-storm-short-a',
  'sk-storm-short-b'
] as const
const healthyKeys = ['sk-storm-healthy-a', 'sk-storm-healthy-b'] as const
const tempRoot = resolve(tmpdir(), `juhe-ai-low-capacity-transport-storm-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'low-capacity-transport-storm-secret'
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
  accountCircuit,
  hotQuality,
  usageRecordQueue,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

interface UpstreamHit {
  requestId: string
  key: string
}

interface StormScenario {
  apiKey: string
  primaryGroupId: string
  badAccountIdsByKey: Map<string, string>
  healthyAccountIds: string[]
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const hits: UpstreamHit[] = []
const recoveredKeys = new Set<string>()
const healthyInFlight = new Map<string, number>()
const healthyMaxInFlight = new Map<string, number>()
let totalHealthyInFlight = 0
let totalHealthyMaxInFlight = 0
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    textFirstResponseTimeoutSeconds: 10,
    textStreamIdleTimeoutSeconds: 10,
    noAvailableAccountWaitTimeoutSeconds: 10,
    accountCircuitConfirmationFailuresRequired: 2
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  clearAccountConcurrency()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
  const scenario = createScenario(upstreamBaseUrl)

  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`

  const stormFailures = await assertSameSourceStormIsBounded(gatewayBaseUrl, scenario)
  const target = await assertSameSourceCannotSelfConfirmOpen(scenario)
  await assertQueuedClientCancellationReleasesEverything(gatewayBaseUrl, scenario)
  await assertIndependentBackgroundConfirmationOpensAndRecovers(target)
  await assertRecoveredAccountIsReselected(gatewayBaseUrl, scenario, target)
  assert.deepEqual(stormFailures, [], stormFailures.join('\n'))

  console.log(JSON.stringify({
    message: 'gateway low-capacity transport storm mock ai regression passed',
    concurrentRequests: requestCount,
    badAccounts: badKeys.length,
    healthyAccounts: healthyKeys.length,
    healthyConcurrencyLimit: 1,
    upstreamHits: hits.length,
    totalHealthyMaxInFlight
  }))
} finally {
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSameSourceStormIsBounded(baseUrl: string, scenario: StormScenario): Promise<string[]> {
  const startedAt = Date.now()
  let maxObservedQueueSize = 0
  const queueSampler = setInterval(() => {
    maxObservedQueueSize = Math.max(
      maxObservedQueueSize,
      ...highConcurrencyGroupQueueSnapshot().map((snapshot) => snapshot.queueSize)
    )
  }, 5)
  queueSampler.unref()
  const results = await Promise.all(Array.from({ length: requestCount }, (_, index) => postChat(
    baseUrl,
    scenario.apiKey,
    `storm-${index}`,
    sharedSessionId,
    sharedClientIp
  ))).finally(() => clearInterval(queueSampler))
  const elapsedMs = Date.now() - startedAt

  const failedResults = results.filter((result) => result.status !== 200)
  const stormFailures = failedResults.length === 0
    ? []
    : [`64 路风暴中 ${failedResults.length} 路在仍有健康低容量账户时过早返回客户端（观测到的最大短队列=${maxObservedQueueSize}）：${JSON.stringify(failedResults.slice(0, 3))}`]
  assert(results.filter((result) => result.status === 200).every((result) => /low-capacity healthy success/.test(result.text)), '成功响应只能来自最终健康账户')
  assert(results.every((result) => !/reset-before-headers|short-body|transport-storm-partial|ECONNRESET|socket hang up/i.test(result.text)), '任意中间 transport 错误不得泄露到客户端')
  assert(failedResults.every((result) => result.status === 503 && /upstream_retryable_error/.test(result.text)), '过早失败也必须保持稳定网关错误，不得泄露上游 transport 细节')
  assert(elapsedMs < 12_000, `chat/text 风暴总墙钟必须有界，实际 ${elapsedMs}ms`)
  assert(Math.max(...results.map((result) => result.durationMs)) >= 100, '低容量健康池必须出现真实排队等待，禁止无证据宣称队列已覆盖')
  assert(maxObservedQueueSize > 0, '低容量健康池必须进入真实 high_concurrency 短队列')
  assert(totalHealthyMaxInFlight <= healthyKeys.length, `健康池总并发不得越过 2，实际 ${totalHealthyMaxInFlight}`)
  for (const key of healthyKeys) {
    assert((healthyMaxInFlight.get(key) ?? 0) <= 1, `${key} 不得越过账户并发上限 1`)
  }

  await waitUntil(() => [
    ...scenario.badAccountIdsByKey.values(),
    ...scenario.healthyAccountIds
  ].every((accountId) => getAccountCurrentConcurrency(accountId) === 0), 2_000)
  for (const accountId of [...scenario.badAccountIdsByKey.values(), ...scenario.healthyAccountIds]) {
    assert.equal(getAccountCurrentConcurrency(accountId), 0, `请求结束后不得泄漏并发槽：${accountId}`)
  }

  for (let index = 0; index < requestCount; index += 1) {
    const requestHits = hits.filter((hit) => hit.requestId === `storm-${index}`)
    assert(requestHits.length >= 1, `storm-${index} 必须进入真实上游 Mock`)
    assert(requestHits.length <= badKeys.length + 1, `storm-${index} 尝试次数必须有界：${JSON.stringify(requestHits)}`)
    if (results[index]!.status === 200) {
      assert(healthyKeys.includes(requestHits.at(-1)!.key as typeof healthyKeys[number]), `storm-${index} 成功时最终必须落到健康账户`)
    }
  }
  const badHits = hits.filter((hit) => badKeys.includes(hit.key as typeof badKeys[number]))
  assert(badHits.length >= requestCount, `64 路请求必须至少产生 64 次真实 transport 断流，实际 ${badHits.length}`)
  assert(badHits.some((hit) => hit.key.includes('-reset-')), '风暴必须真实覆盖 headers 前 connection reset')
  assert(badHits.some((hit) => hit.key.includes('-short-')), '风暴必须真实覆盖 headers 后 Content-Length 短正文断流')
  return stormFailures
}

async function assertSameSourceCannotSelfConfirmOpen(scenario: StormScenario): Promise<{
  key: string
  accountId: string
  scope: ReturnType<typeof gatewayAccountProtocolModelScope>
}> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const candidates: Array<{
    key: string
    accountId: string
    scope: ReturnType<typeof gatewayAccountProtocolModelScope>
  }> = []

  for (const [key, accountId] of scenario.badAccountIdsByKey) {
    const account = requireDispatchAccount(scenario.primaryGroupId, accountId)
    const scope = gatewayAccountProtocolModelScope(account, 'text', model)
    const state = await store.get(scope)
    assert.notEqual(state.phase, 'OPEN', `同 session/IP 的 64 路失败不得让 ${key} 自证 OPEN`)
    assert.equal(repositories.findAccountForTest(accountId, access)?.status, 'active', `${key} 不得被写成账户死亡`)
    if (state.phase === 'SUSPECT') candidates.push({ key, accountId, scope })
  }
  assert(candidates.length > 0, '真实 transport 风暴至少应留下一个 SUSPECT 事故供独立确认')

  for (const accountId of scenario.healthyAccountIds) {
    const account = repositories.findAccountForTest(accountId, access)
    assert.equal(account?.status, 'active', '健康账户不得被坏会话风暴误杀')
    assert.equal(account?.schedulable, true, '健康账户必须保持可调度')
  }
  return candidates[0]!
}

async function assertQueuedClientCancellationReleasesEverything(
  baseUrl: string,
  scenario: StormScenario
): Promise<void> {
  const heldSlots = scenario.healthyAccountIds.map((accountId) => {
    const slot = tryAcquireAccountConcurrency(accountId, 1)
    assert.equal(slot.acquired, true, `取消测试必须先占满健康账户：${accountId}`)
    return slot
  })
  const controller = new AbortController()
  try {
    const pendingRequest = postChat(
      baseUrl,
      scenario.apiKey,
      'queued-client-cancel',
      sharedSessionId,
      sharedClientIp,
      controller.signal
    )
    await waitUntil(() => highConcurrencyGroupQueueSnapshot().some((snapshot) => snapshot.queueSize > 0), 2_000)
    assert(highConcurrencyGroupQueueSnapshot().some((snapshot) => snapshot.queueSize > 0), '客户端取消前请求必须已进入低容量短队列')
    const canceledAtMs = Date.now()
    controller.abort('mock-client-cancel')
    await assert.rejects(
      pendingRequest,
      (error: unknown) => error === 'mock-client-cancel' || (error instanceof Error && error.name === 'AbortError'),
      '客户端取消必须立即终止等待中的 HTTP 请求'
    )
    assert(Date.now() - canceledAtMs < 500, '客户端取消不得等待到队列超时后才生效')
    await waitUntil(() => highConcurrencyGroupQueueSnapshot().length === 0, 2_000)
    assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '客户端取消后必须清除短队列项和唤醒索引')
    assert.equal(
      hits.some((hit) => hit.requestId === 'queued-client-cancel' && healthyKeys.includes(hit.key as typeof healthyKeys[number])),
      false,
      '取消后的排队请求不得继续派发到健康账户'
    )

    const budgetResult = await postChat(
      baseUrl,
      scenario.apiKey,
      'queued-server-budget-timeout',
      `budget-session-${Date.now()}`,
      '198.51.100.82'
    )
    assert([429, 503].includes(budgetResult.status), `健康池长期满载后必须返回稳定容量错误：${budgetResult.text}`)
    assert.match(budgetResult.text, /rate_limit|service_unavailable|retryable_error|稍后重试|暂时不可用/, '容量等待超时不得泄露任意上游 transport 错误')
    assert(budgetResult.durationMs >= 9_000, `text 请求不得在 noAvailable 预算前过早交还客户端：${budgetResult.durationMs}ms`)
    assert(budgetResult.durationMs < 15_000, `text 请求必须裁剪到 10 秒 noAvailable 预算，不能等满 60 秒组队列：${budgetResult.durationMs}ms`)
    assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '服务器等待预算耗尽后必须清除短队列项')
  } finally {
    controller.abort('mock-client-cancel-cleanup')
    for (const slot of heldSlots) slot.release()
  }
  for (const accountId of scenario.healthyAccountIds) {
    assert.equal(getAccountCurrentConcurrency(accountId), 0, `客户端取消清理后不得泄漏健康账户槽：${accountId}`)
  }
}

async function assertIndependentBackgroundConfirmationOpensAndRecovers(target: {
  key: string
  scope: ReturnType<typeof gatewayAccountProtocolModelScope>
}): Promise<void> {
  const store = accountCircuit.getGatewayAccountCircuitStore()
  let probeMode: 'transport_failure' | 'framing_complete' = 'transport_failure'
  let nowMs = Date.now()
  let nextId = 0
  const service = new AccountCircuitRecoveryService(
    store,
    async (state) => state.scopeKey !== accountCircuitScopeKey(target.scope)
      ? undefined
      : ({
          dispatchRevision: state.dispatchRevision,
          probe: async () => probeMode === 'transport_failure'
            ? { kind: 'transport_incomplete' as const, failureKind: 'connection' as const }
            : { kind: 'framing_complete' as const, statusCode: 503 }
        }),
    {
      now: () => nowMs,
      createId: () => `storm-background-${++nextId}`,
      batchSize: 16,
      concurrency: 1,
      leaseDurationMs: 2_000
    }
  )

  let state = await store.get(target.scope)
  assert.equal(state.phase, 'SUSPECT')
  for (let confirmationIndex = 0; confirmationIndex < 2; confirmationIndex += 1) {
    nowMs = Math.max(nowMs + 1, (state.retryAtMs ?? nowMs) + 1)
    const sweep = await service.sweep()
    assert.equal(sweep.transportIncompleteCount, 1, `第 ${confirmationIndex + 1} 个独立后台 transport 证据必须被消费`)
    state = await store.get(target.scope, nowMs)
  }
  assert.equal(state.phase, 'OPEN', '初始同源证据加两个独立后台 transport 确认后必须 SUSPECT -> OPEN')

  probeMode = 'framing_complete'
  nowMs = Math.max(nowMs + 1, (state.retryAtMs ?? nowMs) + 1)
  let sweep = await service.sweep()
  assert.equal(sweep.framingCompleteCount, 1, 'OPEN 到期后必须执行 framing canary')
  state = await store.get(target.scope, nowMs)
  assert.equal(state.phase, 'RECOVERING', '第一次 framing canary 必须 OPEN -> RECOVERING')

  for (let recoveryIndex = 1; recoveryIndex < 4; recoveryIndex += 1) {
    nowMs = Math.max(nowMs + 1, (state.retryAtMs ?? nowMs) + 1)
    sweep = await service.sweep()
    assert.equal(sweep.framingCompleteCount, 1, `第 ${recoveryIndex + 1} 次 framing 恢复证据必须被消费`)
    state = await store.get(target.scope, nowMs)
  }
  assert.equal(state.phase, 'CLOSED', '一次 half-open framing 加三次 RECOVERING framing 后必须关闭电路')
  recoveredKeys.add(target.key)
}

async function assertRecoveredAccountIsReselected(
  baseUrl: string,
  scenario: StormScenario,
  target: { key: string }
): Promise<void> {
  const requestId = 'post-recovery-current-routing'
  const result = await postChat(baseUrl, scenario.apiKey, requestId, `new-session-${Date.now()}`, '198.51.100.199')
  assert.equal(result.status, 200, `恢复后的新请求必须成功：${result.text}`)
  assert.match(result.text, new RegExp(`recovered primary ${target.key}`), '客户端重试必须按当前状态重新选择已恢复的高优先级账户')
  const requestHits = hits.filter((hit) => hit.requestId === requestId)
  assert.equal(requestHits.at(-1)?.key, target.key, '恢复后不得粘滞到旧低优先级健康账户')
}

function createScenario(upstreamBaseUrl: string): StormScenario {
  const primaryGroup = repositories.createGroup({
    name: '传输风暴高优先级坏账户组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const backupGroup = repositories.createGroup({
    name: '传输风暴低容量健康组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 1,
      maxQueueWaitMs: 60_000,
      clientIpConcurrencyLimit: 128,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 0
    }
  }, access)
  const badAccountIdsByKey = new Map<string, string>()
  for (const key of badKeys) {
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `传输风暴坏账户 ${key}`,
      type: 'api_key',
      credentials: { api_key: key, base_url: upstreamBaseUrl },
      groupId: primaryGroup.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 128,
      priority: 0,
      supportedModels: [model]
    }, access)
    activate(account.id)
    badAccountIdsByKey.set(key, account.id)
  }

  const healthyAccountIds: string[] = []
  for (const key of healthyKeys) {
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `传输风暴健康账户 ${key}`,
      type: 'api_key',
      credentials: { api_key: key, base_url: upstreamBaseUrl },
      groupId: backupGroup.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 1,
      priority: 0,
      fallbackEnabled: true,
      supportedModels: [model]
    }, access)
    activate(account.id)
    healthyAccountIds.push(account.id)
  }

  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '传输风暴网关 Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: backupGroup.id, priority: 2, status: 'active' }
    ],
    status: 'active'
  }, access)
  assert(apiKey.key)
  return {
    apiKey: apiKey.key,
    primaryGroupId: primaryGroup.id,
    badAccountIdsByKey,
    healthyAccountIds
  }
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const requestId = url.searchParams.get('storm_request_id') ?? 'missing-request-id'
    const key = bearerKey(req.headers.authorization)
    hits.push({ requestId, key })
    req.resume()
    req.once('end', () => {
      if (recoveredKeys.has(key)) {
        sendSuccess(res, `recovered primary ${key}`)
        return
      }
      if (key.includes('-reset-')) {
        res.socket?.destroy(new Error('reset-before-headers'))
        return
      }
      if (key.includes('-short-')) {
        res.writeHead(200, {
          'content-type': 'application/json; charset=utf-8',
          'content-length': '4096',
          connection: 'close'
        })
        res.flushHeaders()
        res.write('{"error":{"message":"transport-storm-partial-short-body"')
        setTimeout(() => res.destroy(), 5).unref()
        return
      }
      if (healthyKeys.includes(key as typeof healthyKeys[number])) {
        const current = (healthyInFlight.get(key) ?? 0) + 1
        healthyInFlight.set(key, current)
        healthyMaxInFlight.set(key, Math.max(healthyMaxInFlight.get(key) ?? 0, current))
        totalHealthyInFlight += 1
        totalHealthyMaxInFlight = Math.max(totalHealthyMaxInFlight, totalHealthyInFlight)
        setTimeout(() => {
          healthyInFlight.set(key, Math.max(0, (healthyInFlight.get(key) ?? 1) - 1))
          totalHealthyInFlight = Math.max(0, totalHealthyInFlight - 1)
          sendSuccess(res, `low-capacity healthy success ${key}`)
        }, 20).unref()
        return
      }
      res.destroy(new Error(`unexpected mock key: ${key}`))
    })
  })
}

function sendSuccess(res: http.ServerResponse, content: string): void {
  if (res.destroyed || res.writableEnded) return
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'chatcmpl_transport_storm_success',
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }))
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  requestId: string,
  sessionId: string,
  clientIp: string,
  signal?: AbortSignal
): Promise<{ status: number; text: string; durationMs: number }> {
  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/chat/completions?storm_request_id=${encodeURIComponent(requestId)}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-session-id': sessionId,
      'x-forwarded-for': clientIp
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: `transport storm request ${requestId}` }],
      stream: false
    }),
    signal
  })
  return { status: response.status, text: await response.text(), durationMs: Date.now() - startedAt }
}

function requireDispatchAccount(groupId: string, accountId: string) {
  const account = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === accountId)
  assert(account, `找不到调度账户 ${accountId}`)
  return account
}

function activate(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `激活 Mock 账户失败：${accountId}`)
}

function bearerKey(value: string | undefined): string {
  return String(value ?? '').replace(/^Bearer\s+/i, '')
}

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
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
  assert(address && typeof address !== 'string')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
