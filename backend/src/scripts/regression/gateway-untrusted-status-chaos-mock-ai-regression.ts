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
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import {
  clearAccountConcurrency,
  getAccountCurrentConcurrency
} from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'

const untrustedStatuses = [400, 401, 403, 404, 408, 409, 422, 429, 500, 502, 503, 504] as const
const completeBodyShapes = ['json', 'text', 'malformed_json', 'empty'] as const
const concurrentRequestCount = 64
const mixedHealthyRequestCount = 32
const model = 'gpt-5.5'
const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-untrusted-status-chaos-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-untrusted-status-chaos-secret'
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
  apiKeyFailureGuard,
  clientIpAvoidance,
  usageRecordQueue,
  auditLogQueue,
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
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('./f3-audit-direct-input-test-support.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

type CompleteBodyShape = typeof completeBodyShapes[number]

interface UpstreamHit {
  requestId: string
  authorization: string
  status: number
  shape: CompleteBodyShape | 'truncated'
}

interface FailoverScenario {
  apiKey: string
  primaryGroupId: string
  primaryAccountId: string
  primaryKeys: string[]
  backupAccountId: string
  backupKey: string
}

type MixedSessionFailureMode = 'opaque' | 'truncated'

interface MixedSessionStormScenario {
  apiKey: string
  groupId: string
  accountId: string
  accountKeys: string[]
  mode: MixedSessionFailureMode
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const hits: UpstreamHit[] = []
const app = express()
// The mixed-session scenario models independently routed callers. Express
// must therefore accept the test proxy's forwarded client address; arbitrary
// session headers remain untrusted for generic OpenAI traffic.
app.set('trust proxy', 1)
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    textFirstResponseTimeoutSeconds: 10,
    textStreamIdleTimeoutSeconds: 10,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
  const completeScenario = createFailoverScenario({
    label: '状态码与完整正文混沌',
    upstreamBaseUrl,
    primaryKeys: ['sk-chaos-primary-a', 'sk-chaos-primary-b'],
    backupKey: 'sk-chaos-backup'
  })
  const truncatedScenarios = untrustedStatuses.map((status) => createFailoverScenario({
    label: `状态码${status}断流`,
    upstreamBaseUrl,
    primaryKeys: [`sk-chaos-truncated-${status}`],
    backupKey: `sk-chaos-truncated-backup-${status}`
  }))
  const mixedOpaqueScenario = createMixedSessionStormScenario({
    label: '同账户多Key完整异常混合风暴',
    upstreamBaseUrl,
    keyPrefix: 'sk-mixed-opaque',
    mode: 'opaque'
  })
  const mixedTruncatedScenario = createMixedSessionStormScenario({
    label: '同账户多Key断流混合风暴',
    upstreamBaseUrl,
    keyPrefix: 'sk-mixed-truncated',
    mode: 'truncated'
  })
  const confirmationRotationScenario = createMixedSessionStormScenario({
    label: '独立确认多Key轮换',
    upstreamBaseUrl,
    keyPrefix: 'sk-confirmation-rotation',
    mode: 'truncated',
    apiKeyStrategy: 'failover'
  })
  const confirmationAllKeysFailScenario = createMixedSessionStormScenario({
    label: '独立确认多Key全失败',
    upstreamBaseUrl,
    keyPrefix: 'sk-confirmation-all-fail',
    mode: 'truncated',
    apiKeyStrategy: 'failover'
  })

  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`

  await assertCompleteStatusBodyMatrix(gatewayBaseUrl, completeScenario)
  await assertCompleteFailuresDoNotPolluteSharedState(completeScenario)
  await assertConcurrentBadSessionStorm(gatewayBaseUrl, completeScenario)
  await assertSameAndNewSessionReconsiderRecoveredPrimary(gatewayBaseUrl, completeScenario)
  await assertTruncatedResponseMatrix(gatewayBaseUrl, truncatedScenarios)
  await assertTruncatedFailuresStayBounded(truncatedScenarios)
  await assertInterleavedMixedSessionStorm(gatewayBaseUrl, mixedOpaqueScenario)
  await assertInterleavedMixedSessionStorm(gatewayBaseUrl, mixedTruncatedScenario)
  await assertConfirmationKeyRotation(gatewayBaseUrl, confirmationRotationScenario)
  await assertConfirmationAllKeysFail(gatewayBaseUrl, confirmationAllKeysFailScenario)

  console.log(JSON.stringify({
    message: 'gateway untrusted status chaos mock ai regression passed',
    statusCount: untrustedStatuses.length,
    completeBodyShapeCount: completeBodyShapes.length,
    completeMatrixCases: untrustedStatuses.length * completeBodyShapes.length,
    truncatedCases: truncatedScenarios.length,
    concurrentBadSessionRequests: concurrentRequestCount,
    mixedHealthyRequestsPerMode: mixedHealthyRequestCount,
    confirmationKeyRotationHits: hitsForRequest('confirmation-key-rotation').length,
    confirmationAllKeysFailHits: hitsForRequest('confirmation-all-keys-fail').length,
    upstreamHits: hits.length
  }))
} finally {
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertCompleteStatusBodyMatrix(
  baseUrl: string,
  scenario: FailoverScenario
): Promise<void> {
  for (const status of untrustedStatuses) {
    for (const shape of completeBodyShapes) {
      const requestId = `complete-${status}-${shape}`
      const result = await postChat(baseUrl, scenario.apiKey, {
        requestId,
        status,
        shape,
        sessionId: `session-${requestId}`
      })
      assert.equal(result.status, 200, `${requestId} 应隐藏切换成功：${result.text}`)
      assert.match(result.text, /chaos backup success/, `${requestId} 应只返回健康后备结果`)
      assert.doesNotMatch(result.text, /chaos[_ -]error|invalid[_ -]api[_ -]key|rate[_ -]limit|content[_ -]policy/i, `${requestId} 不得泄露误导性上游语义`)

      const requestHits = hitsForRequest(requestId)
      assert.equal(requestHits.length, 3, `${requestId} 应尝试两个 Key 后跨组切到健康账户：${JSON.stringify(requestHits)}`)
      assert.deepEqual(
        new Set(requestHits.slice(0, 2).map((hit) => bearerKey(hit.authorization))),
        new Set(scenario.primaryKeys),
        `${requestId} 应在请求内穷尽当前账户两个 Key`
      )
      assert.equal(bearerKey(requestHits[2]!.authorization), scenario.backupKey, `${requestId} 最终应命中跨组健康账户`)
    }
  }
}

async function assertCompleteFailuresDoNotPolluteSharedState(scenario: FailoverScenario): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const primary = requireDispatchAccount(scenario.primaryGroupId, scenario.primaryAccountId)
  const primarySummary = repositories.findAccountForTest(scenario.primaryAccountId, access)
  const backupSummary = repositories.findAccountForTest(scenario.backupAccountId, access)
  assert.equal(primarySummary?.status, 'active', '任意完整 HTTP 状态码不得写死主账户')
  assert.equal(primarySummary?.schedulable, true, '任意完整 HTTP 状态码不得取消主账户调度')
  assert.equal(primarySummary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '任意完整 HTTP 状态码不得持久化 Key 临时不可用')
  assert.equal(primarySummary?.apiKeyRuntime?.allUnavailable ?? false, false, '任意完整 HTTP 状态码不得写成 Key 池全死')
  assert.equal(backupSummary?.status, 'active')
  assert.equal(backupSummary?.schedulable, true)
  assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.primaryAccountId], undefined, '完整未知响应不得写账户共享运行态')
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '完整未知响应不得写客户端 IP 账户避让')

  const circuitState = await accountCircuit.getGatewayAccountCircuitStore().get(
    accountCircuit.gatewayAccountProtocolModelScope(primary, 'text', model)
  )
  assert.equal(circuitState.phase, 'CLOSED', '完整未知响应不得进入 transport circuit')

  const qualitySnapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotQualityScope(primary))
  assert(qualitySnapshot, '完整未知响应应保留可观测诊断计数')
  assert(qualitySnapshot.window5m.attempts > 0, '完整未知响应应记录 attempt 便于诊断')
  assert(qualitySnapshot.window5m.upstreamResponseFailures > 0, '完整未知响应应只记录中性 upstream response failure')
  assert.equal(qualitySnapshot.window5m.qualityAttempts, 0, '完整未知响应不得进入质量可靠性分母')
  assert.equal(qualitySnapshot.window5m.firstByteSampleCount, 0, '完整未知响应不得污染速度样本')
  assert.equal(qualitySnapshot.window5m.lastFailureAtMs, undefined, '完整未知响应不得成为质量失败时间')
}

async function assertConcurrentBadSessionStorm(
  baseUrl: string,
  scenario: FailoverScenario
): Promise<void> {
  const requestIds = Array.from({ length: concurrentRequestCount }, (_, index) => `storm-${index}`)
  const responses = await Promise.all(requestIds.map((requestId, index) => postChat(baseUrl, scenario.apiKey, {
    requestId,
    status: untrustedStatuses[index % untrustedStatuses.length]!,
    shape: completeBodyShapes[index % completeBodyShapes.length]!,
    sessionId: 'shared-bad-session-chaos'
  })))
  assert(responses.every((response) => response.status === 200), '64 并发坏会话请求必须全部由健康账户完成')
  assert(responses.every((response) => /chaos backup success/.test(response.text)), '64 并发坏会话请求不得把中间错误返回客户端')
  assert(responses.every((response) => !/chaos[_ -]error|invalid[_ -]api[_ -]key|rate[_ -]limit|content[_ -]policy/i.test(response.text)), '64 并发坏会话不得泄露上游错误语义')
  for (const requestId of requestIds) {
    const requestHits = hitsForRequest(requestId)
    assert.equal(requestHits.filter((hit) => bearerKey(hit.authorization) === scenario.backupKey).length, 1, `${requestId} 最终只能由后备账户执行一次`)
    assert.equal(requestHits.length, 3, `${requestId} 必须保持有界的两 Key + 一后备执行次数`)
  }

  const primary = requireDispatchAccount(scenario.primaryGroupId, scenario.primaryAccountId)
  const circuitState = await accountCircuit.getGatewayAccountCircuitStore().get(
    accountCircuit.gatewayAccountProtocolModelScope(primary, 'text', model)
  )
  assert.notEqual(circuitState.phase, 'OPEN', '同一坏会话完整 HTTP 风暴不得熔断正常账户')
  assert.equal(repositories.findAccountForTest(scenario.primaryAccountId, access)?.status, 'active', '坏会话风暴后数据库账户仍应 active')
  assert.equal(repositories.findAccountForTest(scenario.primaryAccountId, access)?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '坏会话风暴不得把 Key 写成共享不可用')
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '坏会话完整 HTTP 风暴不得形成 IP 级跨请求避让')
}

async function assertInterleavedMixedSessionStorm(
  baseUrl: string,
  scenario: MixedSessionStormScenario
): Promise<void> {
  const account = requireDispatchAccount(scenario.groupId, scenario.accountId)
  const qualityScope = hotQualityScope(account)
  const beforeQuality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope)
  assert.equal(beforeQuality, undefined, `${scenario.mode} 混合风暴账户必须从独立的冷质量 scope 开始`)

  const badRequests = Array.from({ length: concurrentRequestCount }, (_, index) => ({
    kind: 'bad' as const,
    requestId: `mixed-${scenario.mode}-bad-${index}`,
    sessionId: `mixed-${scenario.mode}-one-damaged-session`,
    clientIp: '198.51.100.180',
    status: scenario.mode === 'opaque' ? untrustedStatuses[index % untrustedStatuses.length]! : 200,
    shape: scenario.mode === 'opaque'
      ? completeBodyShapes[index % completeBodyShapes.length]!
      : 'truncated' as const
  }))
  const healthyRequests = Array.from({ length: mixedHealthyRequestCount }, (_, index) => ({
    kind: 'healthy' as const,
    requestId: `mixed-${scenario.mode}-healthy-${index}`,
    sessionId: `mixed-${scenario.mode}-healthy-session-${index}`,
    clientIp: `203.0.113.${index + 1}`,
    status: 200,
    shape: 'json' as const
  }))
  const interleavedRequests = healthyRequests.flatMap((healthyRequest, index) => [
    badRequests[index * 2]!,
    healthyRequest,
    badRequests[index * 2 + 1]!
  ])
  assert.equal(interleavedRequests.filter((request) => request.kind === 'bad').length, concurrentRequestCount)
  assert.equal(interleavedRequests.filter((request) => request.kind === 'healthy').length, mixedHealthyRequestCount)

  let maxObservedQueueSize = 0
  let maxObservedConcurrency = 0
  const sampler = setInterval(() => {
    maxObservedQueueSize = Math.max(
      maxObservedQueueSize,
      ...highConcurrencyGroupQueueSnapshot().map((snapshot) => snapshot.queueSize)
    )
    maxObservedConcurrency = Math.max(maxObservedConcurrency, getAccountCurrentConcurrency(scenario.accountId))
  }, 2)
  sampler.unref()
  const startedAtMs = Date.now()
  const results = await Promise.all(interleavedRequests.map(async (request) => ({
    request,
    response: await postChat(baseUrl, scenario.apiKey, {
      requestId: request.requestId,
      status: request.status,
      shape: request.shape,
      sessionId: request.sessionId,
      clientIp: request.clientIp,
      mixedOutcome: request.kind,
      mixedMode: scenario.mode
    })
  }))).finally(() => clearInterval(sampler))
  const elapsedMs = Date.now() - startedAtMs

  const healthyResults = results.filter((result) => result.request.kind === 'healthy')
  const badResults = results.filter((result) => result.request.kind === 'bad')
  const healthySuccessCount = healthyResults.filter(({ response }) => (
    response.status === 200 && response.text.includes(`mixed ${scenario.mode} healthy success`)
  )).length
  assert.equal(healthyResults.length, mixedHealthyRequestCount)
  assert.equal(badResults.length, concurrentRequestCount)
  assert(
    badResults.every(({ response }) => response.status === 503 && response.text.includes('upstream_retryable_error')),
    `${scenario.mode} 坏会话必须有界收口为稳定网关 503：${JSON.stringify(badResults.filter(({ response }) => response.status !== 503).slice(0, 3))}`
  )
  assert(
    badResults.every(({ response }) => !/aborted|chaos[_ -]error|ECONN|hang up|invalid[_ -]api[_ -]key|rate[_ -]limit|content[_ -]policy|partial|socket|upstream_transport_error/i.test(response.text)),
    `${scenario.mode} 坏会话不得泄露上游正文或 transport 细节`
  )
  assert(elapsedMs < 12_000, `${scenario.mode} 96 路混合风暴必须受 10 秒 no-available 墙钟约束，实际 ${elapsedMs}ms`)
  assert(maxObservedQueueSize > 0, `${scenario.mode} 混合风暴必须真实进入 high_concurrency 短队列`)
  assert(maxObservedConcurrency > 0 && maxObservedConcurrency <= 8, `${scenario.mode} 账户并发槽观测值必须位于 1..8，实际 ${maxObservedConcurrency}`)

  for (const { request, response } of healthyResults) {
    const requestHits = hitsForRequest(request.requestId)
    assert.equal(
      requestHits.length,
      response.status === 200 ? 1 : 0,
      `${request.requestId} 健康会话只能直达同一账户一次，或在共享状态挡住时完全不应发往上游`
    )
    assert(requestHits.every((hit) => scenario.accountKeys.includes(bearerKey(hit.authorization))), `${request.requestId} 不得命中被测多 Key 账户之外的凭据`)
  }
  const badHitCount = badResults.reduce((count, { request }) => count + hitsForRequest(request.requestId).length, 0)
  assert(badHitCount > 0, `${scenario.mode} 坏会话必须真实进入上游 Mock`)
  for (const { request } of badResults) {
    const requestHits = hitsForRequest(request.requestId)
    if (scenario.mode === 'opaque') {
      assert.equal(requestHits.length, scenario.accountKeys.length, `${request.requestId} 完整 opaque 异常必须有界穷尽同账户三个物理 Key`)
    }
    assert(requestHits.length <= scenario.accountKeys.length, `${request.requestId} 的物理 Key 尝试必须有界`)
    assert.equal(new Set(requestHits.map((hit) => hit.authorization)).size, requestHits.length, `${request.requestId} 内不得重复同一物理 Key`)
    assert(requestHits.every((hit) => scenario.accountKeys.includes(bearerKey(hit.authorization))), `${request.requestId} 不得切出唯一被测账户`)
  }

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  await waitUntil(() => getAccountCurrentConcurrency(scenario.accountId) === 0, 2_000)
  await waitUntil(() => highConcurrencyGroupQueueSnapshot().length === 0, 2_000)
  assert.equal(getAccountCurrentConcurrency(scenario.accountId), 0, `${scenario.mode} 混合风暴结束后不得泄漏账户并发槽`)
  assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, `${scenario.mode} 混合风暴结束后不得残留短队列、timer 或索引`)
  const sideEffectState = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(sideEffectState.queueLength, 0, `${scenario.mode} 混合风暴结束后持久副作用队列必须归零`)
  assert.equal(sideEffectState.processing, false, `${scenario.mode} 混合风暴结束后副作用 drain 必须停止`)
  assert.equal(sideEffectState.precheckPendingAccountCount, 0, `${scenario.mode} 混合风暴结束后不得残留 precheck 任务`)
  assert.equal(sideEffectState.recoveryProbePendingAccountCount, 0, `${scenario.mode} 混合风暴结束后不得残留 recovery probe`)
  assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, `${scenario.mode} 混合风暴结束后 precheck lease 必须归零`)
  const waitSnapshot = accountSideEffects.recoverableUnavailableWaitCoordinatorSnapshotForTest()
  assert.equal(waitSnapshot.scopeCount, 0, `${scenario.mode} 混合风暴结束后不得残留可恢复等待 scope`)
  assert.equal(waitSnapshot.waiterCount, 0, `${scenario.mode} 混合风暴结束后不得残留可恢复等待者`)
  assert.equal(waitSnapshot.timerCount, 0, `${scenario.mode} 混合风暴结束后不得残留可恢复等待 timer`)

  const summary = repositories.findAccountForTest(scenario.accountId, access)
  assert.equal(summary?.status, 'active', `${scenario.mode} 同源坏会话不得把健康账户写死`)
  assert.equal(summary?.schedulable, true, `${scenario.mode} 同源坏会话不得取消账户调度`)
  assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${scenario.mode} 同源坏会话不得把物理 Key 写成共享不可用`)
  assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, `${scenario.mode} 同源坏会话不得把多 Key 池写成全死`)
  assert.equal(
    apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest().filter((entry) => entry.accountId === scenario.accountId).length,
    0,
    `${scenario.mode} 同源坏会话不得残留进程级 Key failure guard`
  )
  assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountId], undefined, `${scenario.mode} 同源坏会话不得残留账户共享屏障`)

  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  const protocolCircuit = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model))
  const accountCircuitState = await circuitStore.get({ kind: 'account', accountRuntimeKey: gatewayAccountRuntimeKey(account) })
  assert.notEqual(protocolCircuit.phase, 'OPEN', `${scenario.mode} 同一坏会话不得把 protocol/model circuit 确认为 OPEN`)
  assert.notEqual(accountCircuitState.phase, 'OPEN', `${scenario.mode} 同一坏会话不得升级为全账户 OPEN`)
  assert.equal(protocolCircuit.lease, undefined, `${scenario.mode} 混合风暴结束后 protocol/model circuit lease 必须归零`)
  assert.equal(accountCircuitState.lease, undefined, `${scenario.mode} 混合风暴结束后账户 circuit lease 必须归零`)

  const quality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope)
  assert(quality, `${scenario.mode} 混合风暴必须留下热质量观测`)
  assert.equal(quality.window5m.completedResponses, healthySuccessCount, `${scenario.mode} 热质量成功计数必须与真实健康响应一致`)
  assert.equal(quality.window5m.clientCancellations, 0, `${scenario.mode} 上游异常不得误记为客户端取消`)
  if (scenario.mode === 'opaque') {
    assert.equal(quality.window5m.upstreamResponseFailures, badHitCount, '完整 opaque attempt 只能进入中性诊断计数')
    assert.equal(quality.window5m.qualityAttempts, mixedHealthyRequestCount, '完整 opaque 坏会话不得进入共享质量可靠性分母')
    assert.equal(transportQualityFailureCount(quality.window5m), 0, '完整 opaque 坏会话不得伪装成 transport 质量失败')
  } else {
    const transportFailures = transportQualityFailureCount(quality.window5m)
    assert.equal(quality.window5m.upstreamResponseFailures, 0, '200 短正文断流不得伪装成完整上游响应失败')
    assert.equal(
      transportFailures,
      0,
      `本场景没有独立失败 confirmation，同一 transport 坏会话 evidence 不得进入共享质量失败，实际 ${transportFailures}，健康成功 ${healthySuccessCount}/${mixedHealthyRequestCount}：${JSON.stringify(quality.window5m)}`
    )
    assert.equal(
      quality.window5m.qualityAttempts,
      mixedHealthyRequestCount,
      `共享质量分母只能包含 32 个健康会话，实际 ${quality.window5m.qualityAttempts}：${JSON.stringify(quality.window5m)}`
    )
    const terminalOutcomeCount = quality.window5m.completedResponses
      + quality.window5m.upstreamResponseFailures
      + quality.window5m.explicitPolicyFailures
      + quality.window5m.localTransportFailures
      + quality.window5m.unknownOutcomes
      + quality.window5m.clientCancellations
    assert.equal(
      quality.window5m.attempts,
      terminalOutcomeCount,
      `每个热质量 attempt 必须且只能结算一个终态：${JSON.stringify(quality.window5m)}`
    )
    assert.equal(
      quality.window5m.unknownOutcomes,
      quality.window5m.attempts - quality.window5m.completedResponses,
      `transport 风暴中除健康完成外的物理 attempt 必须保持中性 unknown：${JSON.stringify(quality.window5m)}`
    )
    assert(
      quality.window5m.unknownOutcomes >= badHitCount,
      `Mock handler 命中的坏请求都必须有中性终态；连接在 handler 前失败的已开始 attempt 可额外计数：${JSON.stringify(quality.window5m)}`
    )
    assert(
      quality.window5m.attempts <= interleavedRequests.length * scenario.accountKeys.length,
      `多 Key attempt 总数必须受请求数和物理 Key 数量约束：${JSON.stringify(quality.window5m)}`
    )
  }
  assert(
    healthySuccessCount === mixedHealthyRequestCount,
    `${scenario.mode} 风暴中的 32 个独立健康会话必须全部由同一多 Key 账户完成，实际 ${healthySuccessCount}/${mixedHealthyRequestCount}：${JSON.stringify(healthyResults.filter(({ response }) => response.status !== 200).slice(0, 3))}`
  )
}

async function assertConfirmationKeyRotation(
  baseUrl: string,
  scenario: MixedSessionStormScenario
): Promise<void> {
  const account = requireDispatchAccount(scenario.groupId, scenario.accountId)
  const scope = accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model)
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const seeded = await store.suspect({
    scope,
    dispatchRevision: accountCircuit.accountCircuitDispatchRevision(account),
    transitionId: 'confirmation-key-rotation-suspect',
    reason: 'transport:seed',
    confirmationFailuresRequired: 1,
    failureEvidenceKey: 'f'.repeat(64),
    nowMs: Date.now() - 3_001
  })
  assert.equal(seeded.status, 'applied')

  const result = await postChat(baseUrl, scenario.apiKey, {
    requestId: 'confirmation-key-rotation',
    status: 200,
    shape: 'json',
    sessionId: 'confirmation-key-rotation-independent-session',
    confirmationKeyRotation: true
  })
  assert.equal(result.status, 200, `confirmation 首 Key 失败后兄弟 Key 必须完成请求：${result.text}`)
  assert.match(result.text, /confirmation sibling key success/)
  const requestHits = hitsForRequest('confirmation-key-rotation')
  assert.equal(requestHits.length, 2, '同一 confirmation 请求必须且只能尝试失败 Key A 与成功 Key B')
  assert.equal(new Set(requestHits.map((hit) => hit.authorization)).size, 2, 'confirmation Key 轮换不得重复同一物理 Key')

  const state = await store.get(scope)
  assert.equal(state.phase, 'CLOSED', '兄弟 Key 完整 framing 必须关闭本次 SUSPECT')
  assert.equal(state.confirmationFailureCount ?? 0, 0, '关闭后 confirmation 失败计数必须清零')
  assert.equal(state.lease, undefined, '关闭后 confirmation 租约必须清零')

  const quality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotQualityScope(account))
  assert(quality, 'confirmation Key 轮换必须留下真实热质量样本')
  assert.equal(quality.window5m.localTransportFailures, 0, '尚有兄弟 Key 时的中间失败不得污染共享 transport 失败')
  assert.equal(quality.window5m.completedResponses, 1, '兄弟 Key 的完整成功必须保留 completed 质量样本')
  assert.equal(quality.window5m.qualityAttempts, 1, '共享质量分母必须只包含最终成功，不能包含中间 Key 失败')
  assert.equal(quality.window5m.unknownOutcomes, 1, '中间 Key 失败必须保留一份中性 unknown 诊断')

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const summary = repositories.findAccountForTest(scenario.accountId, access)
  assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '网关 transport 观测不得持久写入 Key 运行态')
  assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, '一个 Key 失败且兄弟 Key 成功不得写成 Key 池全死')
  const localKeySuppressions = apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()
    .filter((entry) => entry.accountId === scenario.accountId)
  assert.equal(localKeySuppressions.length, 1, '兄弟 Key 成功后必须只短期隔离已确认失败的 Key')
  assert.equal(localKeySuppressions[0]?.suppressed, true, '已确认失败 Key 必须进入短期 guard')
}

async function assertConfirmationAllKeysFail(
  baseUrl: string,
  scenario: MixedSessionStormScenario
): Promise<void> {
  const account = requireDispatchAccount(scenario.groupId, scenario.accountId)
  const scope = accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model)
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const seeded = await store.suspect({
    scope,
    dispatchRevision: accountCircuit.accountCircuitDispatchRevision(account),
    transitionId: 'confirmation-all-keys-fail-suspect',
    reason: 'transport:seed',
    confirmationFailuresRequired: 1,
    failureEvidenceKey: 'a'.repeat(64),
    nowMs: Date.now() - 3_001
  })
  assert.equal(seeded.status, 'applied')

  const startedAtMs = Date.now()
  const result = await postChat(baseUrl, scenario.apiKey, {
    requestId: 'confirmation-all-keys-fail',
    status: 503,
    shape: 'truncated',
    sessionId: 'confirmation-all-keys-fail-independent-session',
    confirmationAllKeysFail: true
  })
  assert.equal(result.status, 503, `全 Key 失败必须收口为稳定网关 503：${result.text}`)
  assert.match(result.text, /upstream_retryable_error/)
  assert.doesNotMatch(result.text, /socket|hang up|ECONN|confirmation/i, '客户端不得收到原始 transport 或内部确认细节')
  assert(Date.now() - startedAtMs < 12_000, '全 Key 失败必须受 no-available 等待上限约束')

  const requestHits = hitsForRequest('confirmation-all-keys-fail')
  assert.equal(requestHits.length, scenario.accountKeys.length, '全 Key 失败必须恰好尝试每个配置 Key 一次')
  assert.equal(new Set(requestHits.map((hit) => hit.authorization)).size, scenario.accountKeys.length, '全 Key 失败不得重复 fingerprint')

  const state = await store.get(scope)
  assert.equal(state.phase, 'OPEN', '阈值为 1 时只有 Key 池最终耗尽才允许打开 confirmation circuit')
  assert.equal(state.confirmationFailureCount, 1, '一个请求跨多个 Key 只能贡献一次 confirmation 失败')
  assert.equal(state.lease, undefined, '最终 confirmation 失败后不得残留租约')

  const quality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotQualityScope(account))
  assert(quality, '全 Key 失败必须留下有界热质量结果')
  assert.equal(transportQualityFailureCount(quality.window5m), 1, '全 Key 失败只能贡献最后一份共享 transport 失败')
  assert.equal(quality.window5m.qualityAttempts, 1, '共享质量分母只能包含最终确认失败')
  assert.equal(quality.window5m.unknownOutcomes, scenario.accountKeys.length - 1, '中间 Key 失败必须逐次记为中性 unknown')

  const summary = repositories.findAccountForTest(scenario.accountId, access)
  assert.equal(summary?.status, 'active', '全 Key transport 失败不得持久写死账户')
  assert.equal(summary?.schedulable, true, '全 Key transport 失败不得取消账户持久调度')
  assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '没有兄弟 Key 成功证据时不得持久写 Key 失败')
  assert.equal(
    apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()
      .filter((entry) => entry.accountId === scenario.accountId).length,
    0,
    '没有兄弟 Key 成功证据时不得批量隔离全部 Key'
  )
}

async function assertSameAndNewSessionReconsiderRecoveredPrimary(
  baseUrl: string,
  scenario: FailoverScenario
): Promise<void> {
  for (const [label, sessionId] of [
    ['same-session', 'shared-bad-session-chaos'],
    ['new-session', `new-session-${Date.now()}`]
  ] as const) {
    const requestId = `recovered-${label}`
    const result = await postChat(baseUrl, scenario.apiKey, {
      requestId,
      status: 429,
      shape: 'json',
      sessionId,
      recoverPrimary: true
    })
    assert.equal(result.status, 200, `${label} 主账户恢复后请求应成功：${result.text}`)
    assert.match(result.text, /chaos primary recovered/, `${label} 应按当前事实重新命中高优先级主账户`)
    const requestHits = hitsForRequest(requestId)
    assert.equal(requestHits.length, 1, `${label} 恢复后不得继续访问旧后备账户`)
    assert(scenario.primaryKeys.includes(bearerKey(requestHits[0]!.authorization)), `${label} 恢复后应命中主账户 Key`)
  }
}

async function assertTruncatedResponseMatrix(
  baseUrl: string,
  scenarios: FailoverScenario[]
): Promise<void> {
  for (let index = 0; index < scenarios.length; index += 1) {
    const status = untrustedStatuses[index]!
    const scenario = scenarios[index]!
    const requestId = `truncated-${status}`
    const result = await postChat(baseUrl, scenario.apiKey, {
      requestId,
      status,
      shape: 'truncated',
      sessionId: `session-${requestId}`
    })
    assert.equal(result.status, 200, `${requestId} 下游未提交前断流应有界切到健康账户：${result.text}`)
    assert.match(result.text, /chaos backup success/)
    assert.deepEqual(
      hitsForRequest(requestId).map((hit) => bearerKey(hit.authorization)),
      [scenario.primaryKeys[0], scenario.backupKey],
      `${requestId} 只能执行一次失败 attempt 和一次健康后备 attempt`
    )
  }
}

async function assertTruncatedFailuresStayBounded(scenarios: FailoverScenario[]): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const ipSnapshot = clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest()
  assert(ipSnapshot.every((entry) => entry.active === false), '每个独立账户的一次断流不得激活 IP 级长期避让')

  for (const scenario of scenarios) {
    const primary = requireDispatchAccount(scenario.primaryGroupId, scenario.primaryAccountId)
    const summary = repositories.findAccountForTest(scenario.primaryAccountId, access)
    assert.equal(summary?.status, 'active', '单次可观察断流不得写成数据库永久死亡')
    assert.equal(summary?.schedulable, true, '单次可观察断流不得取消数据库调度')
    assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '断流不得按 HTTP 状态码持久化 Key 语义')
    assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.primaryAccountId], undefined, '用户流量断流不得写账户本地共享死亡态')

    const circuitState = await accountCircuit.getGatewayAccountCircuitStore().get(
      accountCircuit.gatewayAccountProtocolModelScope(primary, 'text', model)
    )
    assert.equal(circuitState.phase, 'SUSPECT', 'generic 客户端的客观断流必须进入 SUSPECT，但单次独立证据不得直接 OPEN')

    const qualitySnapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotQualityScope(primary))
    assert(qualitySnapshot, '断流应留下可观察但质量中性的生命周期结果')
    const observedFailureCount = qualitySnapshot.window5m.upstreamResponseFailures
      + qualitySnapshot.window5m.localTransportFailures
      + qualitySnapshot.window5m.readInterruptions
      + qualitySnapshot.window5m.incompleteResponses
      + qualitySnapshot.window5m.clientCancellations
    assert.equal(observedFailureCount, 0, `普通前台断流不得写共享 transport 失败：${JSON.stringify(qualitySnapshot.window5m)}`)
    assert.equal(qualitySnapshot.window5m.unknownOutcomes, 1, '普通前台断流必须以 unknown 结算，不能污染账户质量')
    assert.equal(qualitySnapshot.window5m.qualityAttempts, 0, '普通前台断流不得进入共享质量样本分母')
    assert.equal(
      qualitySnapshot.window5m.clientCancellations,
      0,
      `上游 Content-Length 不足断流不得误归因成客户端取消：${JSON.stringify(qualitySnapshot.window5m)}`
    )
  }
}

function createFailoverScenario(input: {
  label: string
  upstreamBaseUrl: string
  primaryKeys: string[]
  backupKey: string
}): FailoverScenario {
  const primaryGroup = repositories.createGroup({
    name: `${input.label}-主分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const backupGroup = repositories.createGroup({
    name: `${input.label}-后备分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${input.label}-主账户`,
    type: 'api_key',
    credentials: {
      api_key: input.primaryKeys[0],
      ...(input.primaryKeys.length > 1
        ? { api_keys: input.primaryKeys, api_key_strategy: 'round_robin' }
        : {}),
      base_url: input.upstreamBaseUrl,
      error_handling_rules: [{
        enabled: true,
        name: '用户配置状态预筛选但正文不匹配',
        priority: 1,
        status_codes: [...untrustedStatuses],
        keywords: ['configured-never-match-chaos-marker'],
        action: 'retry_next'
      }]
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 256,
    priority: 0,
    supportedModels: [model]
  }, access)
  const backup = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${input.label}-后备账户`,
    type: 'api_key',
    credentials: { api_key: input.backupKey, base_url: input.upstreamBaseUrl },
    groupId: backupGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 256,
    priority: 0,
    fallbackEnabled: true,
    supportedModels: [model]
  }, access)
  activate(primary.id)
  activate(backup.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${input.label}-网关Key`,
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
    primaryAccountId: primary.id,
    primaryKeys: input.primaryKeys,
    backupAccountId: backup.id,
    backupKey: input.backupKey
  }
}

function createMixedSessionStormScenario(input: {
  label: string
  upstreamBaseUrl: string
  keyPrefix: string
  mode: MixedSessionFailureMode
  apiKeyStrategy?: 'round_robin' | 'failover'
}): MixedSessionStormScenario {
  const group = repositories.createGroup({
    name: `${input.label}-单组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 8,
      maxQueueWaitMs: 5_000,
      clientIpConcurrencyLimit: 128,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 0
    }
  }, access)
  const accountKeys = Array.from({ length: 3 }, (_, index) => `${input.keyPrefix}-${index + 1}`)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${input.label}-唯一三Key账户`,
    type: 'api_key',
    credentials: {
      api_key: accountKeys[0],
      api_keys: accountKeys,
      api_key_strategy: input.apiKeyStrategy ?? 'round_robin',
      base_url: input.upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 8,
    priority: 0,
    supportedModels: [model]
  }, access)
  activate(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${input.label}-网关Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    accountId: account.id,
    accountKeys,
    mode: input.mode
  }
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const requestId = url.searchParams.get('chaos_request_id') ?? 'missing-request-id'
    const status = Number(url.searchParams.get('chaos_status') ?? 503)
    const shape = (url.searchParams.get('chaos_shape') ?? 'json') as UpstreamHit['shape']
    const mixedOutcome = url.searchParams.get('chaos_mixed_outcome')
    const mixedMode = url.searchParams.get('chaos_mixed_mode') as MixedSessionFailureMode | null
    const confirmationKeyRotation = url.searchParams.get('chaos_confirmation_key_rotation') === '1'
    const confirmationAllKeysFail = url.searchParams.get('chaos_confirmation_all_keys_fail') === '1'
    const authorization = String(req.headers.authorization ?? '')
    hits.push({ requestId, authorization, status, shape })
    req.resume()

    if (confirmationKeyRotation) {
      if (hitsForRequest(requestId).length === 1) {
        res.destroy()
      } else {
        sendSuccess(res, 'confirmation sibling key success')
      }
      return
    }
    if (confirmationAllKeysFail) {
      res.destroy()
      return
    }

    if (mixedOutcome === 'healthy' && mixedMode) {
      setTimeout(() => sendSuccess(res, `mixed ${mixedMode} healthy success`), 12)
      return
    }
    if (url.searchParams.get('chaos_recover_primary') === '1' || bearerKey(authorization).includes('backup')) {
      sendSuccess(res, url.searchParams.get('chaos_recover_primary') === '1' ? 'chaos primary recovered' : 'chaos backup success')
      return
    }
    if (shape === 'truncated') {
      res.writeHead(status, {
        'content-type': 'application/json; charset=utf-8',
        'content-length': '4096',
        connection: 'close'
      })
      res.flushHeaders()
      res.write('{"error":{"code":"chaos_truncated_invalid_api_key","message":"partial')
      setTimeout(() => res.destroy(), 5)
      return
    }
    if (shape === 'empty') {
      res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'content-length': '0' })
      res.end()
      return
    }
    if (shape === 'text') {
      res.writeHead(status, { 'content-type': 'text/plain; charset=utf-8' })
      res.end('chaos error says invalid api key, rate limit, and content policy at once')
      return
    }
    if (shape === 'malformed_json') {
      res.writeHead(status, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"error":[{"code":"chaos_error"}')
      return
    }
    res.writeHead(status, {
      'content-type': 'application/json; charset=utf-8',
      'x-chaos-error-type': status % 2 === 0 ? 'invalid_api_key' : 'rate_limit'
    })
    res.end(JSON.stringify({
      error: {
        type: status % 3 === 0 ? 'authentication_error' : 'server_error',
        code: status === 401 ? 'rate_limit' : status === 429 ? 'invalid_api_key' : 'content_policy',
        message: `chaos error status ${status} deliberately contradicts its body`
      }
    }))
  })
}

function sendSuccess(res: http.ServerResponse, content: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'chatcmpl_chaos_success',
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }))
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  input: {
    requestId: string
    status: number
    shape: UpstreamHit['shape']
    sessionId: string
    recoverPrimary?: boolean
    clientIp?: string
    mixedOutcome?: 'bad' | 'healthy'
    mixedMode?: MixedSessionFailureMode
    confirmationKeyRotation?: boolean
    confirmationAllKeysFail?: boolean
  }
): Promise<{ status: number; text: string }> {
  const query = new URLSearchParams({
    chaos_request_id: input.requestId,
    chaos_status: String(input.status),
    chaos_shape: input.shape,
    ...(input.recoverPrimary ? { chaos_recover_primary: '1' } : {}),
    ...(input.confirmationKeyRotation ? { chaos_confirmation_key_rotation: '1' } : {}),
    ...(input.confirmationAllKeysFail ? { chaos_confirmation_all_keys_fail: '1' } : {}),
    ...(input.mixedOutcome ? {
      chaos_mixed_outcome: input.mixedOutcome,
      chaos_mixed_mode: input.mixedMode ?? 'opaque'
    } : {})
  })
  const response = await fetch(`${baseUrl}/v1/chat/completions?${query}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-session-id': input.sessionId,
      'x-forwarded-for': input.clientIp ?? '198.51.100.77'
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: `chaos request ${input.requestId}` }],
      stream: false
    })
  })
  return { status: response.status, text: await response.text() }
}

function transportQualityFailureCount(window: {
  localTransportFailures: number
  timeouts: number
  readInterruptions: number
  incompleteResponses: number
}): number {
  return window.localTransportFailures + window.timeouts + window.readInterruptions + window.incompleteResponses
}

function requireDispatchAccount(groupId: string, accountId: string) {
  const account = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === accountId)
  assert(account, `找不到调度账户 ${accountId}`)
  return account
}

function hotQualityScope(account: ReturnType<typeof requireDispatchAccount>) {
  return {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    requestLane: 'text' as const,
    modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
  }
}

function activate(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `激活 Mock 账户失败：${accountId}`)
}

function hitsForRequest(requestId: string): UpstreamHit[] {
  return hits.filter((hit) => hit.requestId === requestId)
}

function bearerKey(value: string): string {
  return value.replace(/^Bearer\s+/i, '')
}

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadlineAtMs = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadlineAtMs) return
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
