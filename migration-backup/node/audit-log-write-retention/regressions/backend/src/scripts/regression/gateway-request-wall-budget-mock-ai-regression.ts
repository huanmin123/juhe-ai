import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { type AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import { gatewayAccountProtocolModelScope } from '../../modules/gateway/runtime/account-circuit.service.js'
import {
  GatewayRequestWallBudget,
  defaultGatewayFinalResponseReserveMs
} from '../../modules/gateway/routing/route-coordination.js'
import {
  RecoverableUnavailableWaitCoordinator,
  waitForRecoverableUnavailableState
} from '../../modules/gateway/runtime/recoverable-unavailable-wait.js'
import { logger } from '../../shared/logger.js'
import { getAccountCurrentConcurrency } from '../../shared/account-concurrency.js'

type MockMode =
  | 'budget_storm'
  | 'client_retry_all_failed'
  | 'client_retry_first_recovered'
  | 'client_retry_current_best_third'

interface WallBudgetScenario {
  apiKey: string
  routeStrategyId: string
  groupId: string
  primaryAccountId: string
  primaryKey: string
  multiKeyPool: readonly string[]
  lowerPriorityKey: string
  exhaustedTailKey: string
}

interface ClientRetryScenario {
  apiKey: string
  routeStrategyId: string
  groupId: string
  accountIds: readonly [string, string, string]
  accountKeys: readonly [string, string, string]
}

interface CommittedWallScenario {
  kind: 'raw_json' | 'stream_raw'
  apiKey: string
  groupId: string
  primaryAccountId: string
  backupAccountId: string
  primaryKey: string
  backupKey: string
}

interface PartialHttpResponse {
  status: number
  headers: http.IncomingHttpHeaders
  body: Buffer
  terminated: boolean
}

const clientRetryAccountKeys = [
  'sk-wall-client-retry-1',
  'sk-wall-client-retry-2',
  'sk-wall-client-retry-3'
] as const

const clientRetrySpeedFirstConfig = {
  firstByteDeadlineMs: 10_000,
  slowTriggerCount: 2,
  slowWindowSeconds: 300,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 2
}

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-request-wall-budget-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-request-wall-budget-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
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
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  latencyDegradation,
  accountCircuit,
  hotQuality
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

const upstreamHits: string[] = []
const intermediateMarkers = [
  'mock_slow_first_byte_body',
  'mock_disconnected_body',
  'mock_opaque_complete_body'
]
let mockMode: MockMode = 'budget_storm'
let fakeNowMs = Date.now()
let accelerateFirstByteTimer = false
let firstByteTimerIntercepted = false
let wallBodyFragmentSent = false
let wallBodyReaderClosed = false
let rawWallReaderClosed = false
let streamWallReaderClosed = false
let releaseRawWallTail: (() => void) | undefined
let releaseStreamWallTail: (() => void) | undefined
const originalDateNow = Date.now.bind(Date)
const originalSetTimeout = globalThis.setTimeout.bind(globalThis)
let upstreamServer: http.Server | undefined
let appServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    noAvailableAccountWaitTimeoutSeconds: 10,
    textFirstResponseTimeoutSeconds: 30,
    textStreamIdleTimeoutSeconds: 30
  })
  gatewayCache.clearGatewayRuntimeCache()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
  const scenario = createWallBudgetScenario(upstreamBaseUrl)
  const clientRetryScenario = createClientRetryScenario(upstreamBaseUrl)
  const rawCommittedWallScenario = createCommittedWallScenario('raw_json', upstreamBaseUrl)
  const streamCommittedWallScenario = createCommittedWallScenario('stream_raw', upstreamBaseUrl)
  const hardDeadApiKey = createHardDeadScenario(upstreamBaseUrl)
  await primePrimarySlowObservation(scenario)

  appServer = http.createServer(app)
  await listen(appServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  await assertCommittedRawJsonWallBoundary(gatewayBaseUrl, rawCommittedWallScenario)
  await assertCommittedStreamWallBoundary(gatewayBaseUrl, streamCommittedWallScenario)
  await assertClientRetryReevaluatesCurrentScheduling(gatewayBaseUrl, clientRetryScenario)
  await assertMixedAttemptsShareOneWallBudget(gatewayBaseUrl, scenario)
  await assertHardDeadPoolReturnsGatewayError(gatewayBaseUrl, hardDeadApiKey)
  await assertRecoverablePoolWaitIsClippedByWallBudget()

  console.log('gateway request wall budget mock ai regression passed')
} finally {
  Date.now = originalDateNow
  globalThis.setTimeout = originalSetTimeout
  await closeServer(appServer)
  await closeServer(upstreamServer)
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertCommittedRawJsonWallBoundary(
  gatewayBaseUrl: string,
  scenario: CommittedWallScenario
): Promise<void> {
  const traceId = `wall-raw-json-${originalDateNow()}`
  const hitOffset = upstreamHits.length
  const initialFakeNowMs = fakeNowMs
  rawWallReaderClosed = false
  Date.now = () => fakeNowMs
  try {
    let released = false
    const response = await postChatOverRawHttp(gatewayBaseUrl, scenario.apiKey, false, traceId, (bodyBytes) => {
      if (released || bodyBytes <= 1024 * 1024) return
      released = true
      const release = releaseRawWallTail
      assert(release, 'JSON 检查降级后真实字节到达客户端时必须有待释放的上游后续块')
      release()
    })
    await waitForCondition(() => rawWallReaderClosed, 'raw JSON 已提交后墙钟必须关闭真实上游 socket')
    assert.equal(response.status, 200, 'raw JSON 首块必须先提交上游 200 响应')
    assert.equal(response.terminated, true, 'raw JSON 已提交后越墙钟只能截断单连接')
    assert(response.body.byteLength > 1024 * 1024, '回归必须真实超过 1MiB JSON 检查窗口并写到客户端')
    assert.doesNotMatch(response.body.toString('utf8'), /wall-raw-late-tail/u, 'raw JSON 越墙钟的后续块不得再下发')
    assert.deepEqual(
      upstreamHits.slice(hitOffset),
      [scenario.primaryKey],
      'raw JSON 首个 { 已提交后绝不得 fallback 到第二账户'
    )
    await assertWallAuditAndNeutralState(traceId, scenario)
  } finally {
    Date.now = originalDateNow
    fakeNowMs = initialFakeNowMs
    releaseRawWallTail = undefined
  }
}

async function assertCommittedStreamWallBoundary(
  gatewayBaseUrl: string,
  scenario: CommittedWallScenario
): Promise<void> {
  const traceId = `wall-stream-raw-${originalDateNow()}`
  const hitOffset = upstreamHits.length
  const initialFakeNowMs = fakeNowMs
  streamWallReaderClosed = false
  Date.now = () => fakeNowMs
  try {
    let released = false
    const response = await postChatOverRawHttp(gatewayBaseUrl, scenario.apiKey, true, traceId, (bodyBytes) => {
      if (released || bodyBytes <= 256 * 1024) return
      released = true
      const release = releaseStreamWallTail
      assert(release, '流式原始字节超过预提交缓冲后必须有待释放的上游后续块')
      release()
    })
    await waitForCondition(() => streamWallReaderClosed, '流式原始字节已提交后墙钟必须关闭真实上游 socket')
    const responseText = response.body.toString('utf8')
    assert.equal(response.status, 200, '流式原始字节必须先提交上游 200 响应')
    assert.equal(response.terminated, true, '流式原始字节已提交后越墙钟只能截断当前连接')
    assert(response.body.byteLength > 256 * 1024, '回归必须真实超过 256KiB 预提交缓冲并写到客户端')
    assert.doesNotMatch(
      responseText,
      /wall-stream-backup|gateway_request_wall_budget_exhausted|upstream_retryable_error|response\.failed/u,
      '已提交的流不得拼接第二账户或网关错误尾包'
    )
    assert.deepEqual(
      upstreamHits.slice(hitOffset),
      [scenario.primaryKey],
      '流式原始字节已提交后第二账户 hit 必须为 0'
    )
    await assertWallAuditAndNeutralState(traceId, scenario)
  } finally {
    Date.now = originalDateNow
    fakeNowMs = initialFakeNowMs
    releaseStreamWallTail = undefined
  }
}

async function assertWallAuditAndNeutralState(traceId: string, scenario: CommittedWallScenario): Promise<void> {
  await waitForCondition(
    () => auditLogQueue.getAuditLogQueueRuntime().queueLength > 0,
    `${scenario.kind} 墙钟已提交回归必须生成完整审计`
  )
  auditLogQueue.flushAllAuditLogQueue()
  const audit = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(audit.total, 1, `${scenario.kind} 墙钟中断必须仅生成一条完整审计`)
  const detail = repositories.getAuditLogDetail(audit.items[0]?.id ?? '')
  assert(detail, `${scenario.kind} 墙钟审计详情必须可读`)
  assert(
    detail.attempts.some((attempt) => attempt.errorCode === 'gateway_request_wall_budget_exhausted'),
    `${scenario.kind} 已提交后中断必须归因 gateway_request_wall_budget_exhausted`
  )
  const metadata = await gatewayMetadataPayloads(detail.id)
  assert(metadata.some((item) => item.label === 'gateway_request_wall_budget_exhausted'), `${scenario.kind} 必须记录墙钟审计 metadata`)
  assert(!metadata.some((item) => item.label === 'stream_server_retry_dispatch'), `${scenario.kind} 已提交后不得产生服务端切号决策`)
  await waitForCondition(
    () => getAccountCurrentConcurrency(scenario.primaryAccountId) === 0
      && getAccountCurrentConcurrency(scenario.backupAccountId) === 0,
    `${scenario.kind} 墙钟收口后主备账户并发槽必须全部释放`
  )

  const account = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, {
    requestedModel: 'gpt-5.5'
  }).find((candidate) => candidate.id === scenario.primaryAccountId)
  assert(account, `${scenario.kind} 必须找到主账户运行凭据`)
  const circuitState = await accountCircuit.getGatewayAccountCircuitStore().get(
    gatewayAccountProtocolModelScope(account, 'text', 'gpt-5.5')
  )
  assert.equal(circuitState.phase, 'CLOSED', `${scenario.kind} 墙钟不得写 transport circuit`)
  assert.equal(circuitState.failureEvidenceKeys?.length ?? 0, 0, `${scenario.kind} 墙钟不得留下 transport evidence`)
  const hotScope = {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    requestLane: 'text',
    modelFamily: hotQuality.gatewayHotQualityModelFamily('gpt-5.5')
  } as const
  let hotSnapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotScope)
  const hotDeadlineAt = originalDateNow() + 1_000
  while ((hotSnapshot?.window10m.attempts ?? 0) < 1 && originalDateNow() < hotDeadlineAt) {
    await new Promise<void>((resolvePromise) => originalSetTimeout(resolvePromise, 5))
    hotSnapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(hotScope)
  }
  assert(hotSnapshot, `${scenario.kind} 必须留下一次 hot-quality 尝试观测`)
  assert.equal(hotSnapshot.window10m.attempts, 1)
  assert.equal(hotSnapshot.window10m.unknownOutcomes, 1, `${scenario.kind} 墙钟只能记 unknown`)
  assert.equal(hotSnapshot.window10m.completedResponses, 0, `${scenario.kind} 墙钟不得误记完整成功`)
  assert.equal(hotSnapshot.window10m.upstreamResponseFailures, 0, `${scenario.kind} 墙钟不得误记上游语义失败`)
  assert.equal(hotSnapshot.window10m.localTransportFailures, 0, `${scenario.kind} 墙钟不得误记 transport failure`)
}

async function assertMixedAttemptsShareOneWallBudget(
  gatewayBaseUrl: string,
  scenario: WallBudgetScenario
): Promise<void> {
  const hitOffset = upstreamHits.length
  const realStartedAtMs = originalDateNow()
  accelerateFirstByteTimer = true
  firstByteTimerIntercepted = false
  wallBodyFragmentSent = false
  wallBodyReaderClosed = false
  Date.now = () => fakeNowMs
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const callStack = new Error().stack ?? ''
    if (
      accelerateFirstByteTimer
      && typeof delay === 'number'
      && delay >= 9_900
      && delay <= 10_000
      && /gateway[\\/]upstream[\\/]request/.test(callStack)
    ) {
      accelerateFirstByteTimer = false
      firstByteTimerIntercepted = true
      return originalSetTimeout(() => {
        fakeNowMs += 90_000
        callback(...args)
      }, 20)
    }
    return originalSetTimeout(callback, delay, ...args)
  }) as typeof globalThis.setTimeout

  const response = await postChat(gatewayBaseUrl, scenario.apiKey, 'single request must share one wall budget')
  assert.equal(
    wallBodyFragmentSent,
    true,
    `测试必须真实进入 200 JSON 碎片的 body 阶段墙钟交接；hits=${upstreamHits.slice(hitOffset).join(',')}; status=${response.status}`
  )
  await waitForCondition(
    () => wallBodyReaderClosed,
    '共享墙钟接管 JSON 碎片响应后必须关闭上游 reader'
  )
  const realElapsedMs = originalDateNow() - realStartedAtMs
  const requestHits = upstreamHits.slice(hitOffset)

  assert.equal(firstByteTimerIntercepted, true, '测试必须真实截获上游首字 deadline 计时器')
  assert.equal(response.status, 503, `墙钟尾窗耗尽后应返回网关自有 503，实际 ${response.status}: ${response.text}`)
  assert.match(response.text, /upstream_retryable_error|gateway_request_wall_budget_exhausted|网关请求处理时间已到/)
  for (const marker of intermediateMarkers) {
    assert.doesNotMatch(response.text, new RegExp(marker), `中间上游错误不得泄露给客户端：${marker}`)
  }
  assert.deepEqual(
    requestHits,
    [scenario.primaryKey, ...scenario.multiKeyPool, scenario.lowerPriorityKey],
    '慢首字、同账户多 Key、断流和低优先级账户必须共用一次请求的墙钟预算'
  )
  assert(!requestHits.includes(scenario.exhaustedTailKey), '墙钟耗尽后不得重置预算并继续请求下一个低优先级账户')
  assert(realElapsedMs < 2_000, `假时钟回归不得真实等待 270 秒，实际 ${realElapsedMs}ms`)

  globalThis.setTimeout = originalSetTimeout
}

async function assertClientRetryReevaluatesCurrentScheduling(
  gatewayBaseUrl: string,
  scenario: ClientRetryScenario
): Promise<void> {
  const requestIdentity = {
    sessionId: 'wall-budget-client-retry-same-session',
    clientIp: '198.51.100.73'
  }

  mockMode = 'client_retry_all_failed'
  const exhaustedHitOffset = upstreamHits.length
  const exhausted = await postChat(
    gatewayBaseUrl,
    scenario.apiKey,
    'first client request must exhaust every candidate',
    requestIdentity
  )
  const exhaustedHits = upstreamHits.slice(exhaustedHitOffset)
  assert.equal(exhausted.status, 503, `首个客户端请求真实穷尽全部候选后应返回网关 503，实际 ${exhausted.status}: ${exhausted.text}`)
  assert.match(exhausted.text, /upstream_retryable_error/)
  assert.deepEqual(exhaustedHits, [...scenario.accountKeys], '首个请求必须按 1、2、3 当前优先级真实穷尽候选')
  assert.doesNotMatch(exhausted.text, /mock_client_retry_account_[123]_failed/, '网关不得向客户端泄露任一中间上游错误')
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()

  // Deliberately do not reset the gateway cache, circuit, suppression, quality, latency, or session/IP state.
  mockMode = 'client_retry_first_recovered'
  const recoveredHitOffset = upstreamHits.length
  const recovered = await postChat(
    gatewayBaseUrl,
    scenario.apiKey,
    'same client retry must start from current scheduling facts',
    requestIdentity
  )
  const recoveredHits = upstreamHits.slice(recoveredHitOffset)
  assert.equal(recovered.status, 200, `仅恢复 1 号后，同客户端重试应成功，实际 ${recovered.status}: ${recovered.text}`)
  assert.match(recovered.text, new RegExp(`mock recovered response from ${scenario.accountKeys[0]}`))
  assert.deepEqual(
    recoveredHits,
    [scenario.accountKeys[0]],
    '同 session/IP 客户端重试必须重新按当前优先级选择已恢复 1 号，不得从上一请求的穷尽游标继续到 2 或 3'
  )

  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '客户端重试速度状态回归必须取得普通路由 scope')
  for (const accountId of scenario.accountIds.slice(0, 2)) {
    await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: accountId }, scope, clientRetrySpeedFirstConfig)
    await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: accountId }, scope, clientRetrySpeedFirstConfig)
    assert.equal(
      await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: accountId }, scope),
      true,
      `账户 ${accountId} 必须保留真实速度降级状态`
    )
  }
  assert.equal(
    await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: scenario.accountIds[2] }, scope),
    false,
    '3 号必须保持为当前未降级最优候选'
  )

  mockMode = 'client_retry_current_best_third'
  const currentBestHitOffset = upstreamHits.length
  const currentBest = await postChat(
    gatewayBaseUrl,
    scenario.apiKey,
    'same client retry must honor current speed state over stale affinity',
    requestIdentity
  )
  const currentBestHits = upstreamHits.slice(currentBestHitOffset)
  assert.equal(currentBest.status, 200, `1、2 号仍降级时应由当前最优 3 号成功，实际 ${currentBest.status}: ${currentBest.text}`)
  assert.match(currentBest.text, new RegExp(`mock recovered response from ${scenario.accountKeys[2]}`))
  assert.deepEqual(
    currentBestHits,
    [scenario.accountKeys[2]],
    '同 session/IP 的下一请求必须按当前质量/速度状态直达 3 号，不得沿用 1 号亲和或上一请求游标'
  )
}

async function assertHardDeadPoolReturnsGatewayError(gatewayBaseUrl: string, apiKey: string): Promise<void> {
  Date.now = originalDateNow
  const hitOffset = upstreamHits.length
  const startedAtMs = originalDateNow()
  const response = await postChat(gatewayBaseUrl, apiKey, 'hard dead pool must fail locally')
  const elapsedMs = originalDateNow() - startedAtMs

  assert.equal(response.status, 503, `全池硬死亡应稳定返回网关 503，实际 ${response.status}: ${response.text}`)
  assert.equal(upstreamHits.length, hitOffset, '全池硬死亡不得访问上游')
  assert(elapsedMs < 1_000, `全池硬死亡不得进入可恢复等待，实际 ${elapsedMs}ms`)
  for (const marker of intermediateMarkers) {
    assert.doesNotMatch(response.text, new RegExp(marker))
  }
}

async function assertRecoverablePoolWaitIsClippedByWallBudget(): Promise<void> {
  let nowMs = 10_000
  const timers: Array<{ callback: () => void; delayMs: number }> = []
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    now: () => nowMs,
    setTimer(callback, delayMs) {
      timers.push({ callback, delayMs })
      return callback
    },
    clearTimer() {}
  })
  const wallBudget = new GatewayRequestWallBudget({
    requestAcceptedAtMs: nowMs,
    budgetMs: 120,
    now: () => nowMs
  })
  const metadata: Array<{ label: string; metadata: Record<string, unknown> }> = []
  const auditCapture = {
    addGatewayMetadata(input: { label: string; metadata: Record<string, unknown> }) {
      metadata.push(input)
    }
  } as unknown as AuditCaptureContext
  let refreshCount = 0
  const waiting = waitForRecoverableUnavailableState({
    scopeKey: 'all-recoverable-wall-budget',
    reason: 'all_accounts_recoverable_later',
    initialState: { ready: false },
    refresh: () => {
      refreshCount += 1
      return { ready: false }
    },
    isReady: (state) => state.ready,
    nextRetryAfterMs: () => 50,
    auditCapture,
    maxWaitMs: 10_000,
    checkIntervalMs: 1_000,
    gatewayRequestWallBudget: wallBudget,
    finalResponseReserveMs: 20,
    coordinator,
    now: () => nowMs
  })

  let settled = false
  void waiting.then(() => { settled = true })
  for (let step = 0; step < 10 && !settled; step += 1) {
    for (let microtask = 0; microtask < 5 && !settled; microtask += 1) {
      await Promise.resolve()
    }
    if (settled) break
    const timer = timers.shift()
    assert(timer, `恢复等待第 ${step + 1} 轮必须安排有界假 timer`)
    nowMs += timer.delayMs
    timer.callback()
  }
  assert.equal(settled, true, '恢复等待必须在 10 轮假 timer 内按共享墙钟结束')
  const result = await waiting

  assert.equal(result.ready, false)
  assert.equal(result.timedOut, true, '仅有可恢复账户时必须在墙钟最终响应预留前停止等待')
  assert.equal(result.waitedMs, 100, '恢复等待必须裁剪到共享墙钟 120ms 减 20ms 最终响应尾窗')
  assert.equal(refreshCount, 1, '第二轮 timer 恰好到达绝对 deadline 时不得越界再刷新候选')
  assert(metadata.some((item) => item.label === 'recoverable_unavailable_wait_result'))
}

function createCommittedWallScenario(
  kind: CommittedWallScenario['kind'],
  upstreamBaseUrl: string
): CommittedWallScenario {
  const label = kind === 'raw_json' ? 'raw JSON已提交墙钟' : '流式原始字节已提交墙钟'
  const primaryKey = kind === 'raw_json' ? 'sk-wall-raw-primary' : 'sk-wall-stream-primary'
  const backupKey = kind === 'raw_json' ? 'sk-wall-raw-backup' : 'sk-wall-stream-backup'
  const group = repositories.createGroup({
    name: `${label}分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}主账户`,
    type: 'api_key',
    credentials: { api_key: primaryKey, base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    superPriorityEnabled: true,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }, access)
  const backup = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}备用账户`,
    type: 'api_key',
    credentials: { api_key: backupKey, base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 1,
    supportedModels: ['gpt-5.5']
  }, access)
  activateAccount(primary.id)
  activateAccount(backup.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${label}网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)
  gatewayCache.clearGatewayRuntimeCache()
  return {
    kind,
    apiKey: apiKey.key,
    groupId: group.id,
    primaryAccountId: primary.id,
    backupAccountId: backup.id,
    primaryKey,
    backupKey
  }
}

function createClientRetryScenario(upstreamBaseUrl: string): ClientRetryScenario {
  const group = repositories.createGroup({
    name: '客户端重试当前状态重选分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const accounts = clientRetryAccountKeys.map((accountKey, index) => repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `客户端重试重选 ${index + 1} 号账户`,
    type: 'api_key',
    credentials: { api_key: accountKey, base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    superPriorityEnabled: index === 0,
    priority: index,
    supportedModels: ['gpt-5.5']
  }, access))
  for (const account of accounts) activateAccount(account.id)

  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '客户端重试当前状态重选网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: clientRetrySpeedFirstConfig.firstByteDeadlineMs,
      speedFirstConfig: {
        slowTriggerCount: clientRetrySpeedFirstConfig.slowTriggerCount,
        slowWindowSeconds: clientRetrySpeedFirstConfig.slowWindowSeconds,
        recoverySuccessCount: clientRetrySpeedFirstConfig.recoverySuccessCount,
        probeIntervalSeconds: clientRetrySpeedFirstConfig.probeIntervalSeconds,
        degradedTtlSeconds: clientRetrySpeedFirstConfig.degradedTtlSeconds,
        maxFirstByteRetriesPerRequest: clientRetrySpeedFirstConfig.maxFirstByteRetriesPerRequest
      }
    },
    status: 'active'
  }, access)
  assert(apiKey.key && apiKey.routeStrategyId)
  return {
    apiKey: apiKey.key,
    routeStrategyId: apiKey.routeStrategyId,
    groupId: group.id,
    accountIds: [accounts[0]!.id, accounts[1]!.id, accounts[2]!.id],
    accountKeys: clientRetryAccountKeys
  }
}

function createWallBudgetScenario(upstreamBaseUrl: string): WallBudgetScenario {
  const primaryGroup = repositories.createGroup({
    name: '墙钟预算主分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primaryKey = 'sk-wall-slow-primary'
  const multiKeyPool = ['sk-wall-multi-a', 'sk-wall-multi-b'] as const
  const lowerPriorityKey = 'sk-wall-lower-priority'
  const exhaustedTailKey = 'sk-wall-exhausted-tail'
  const accounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '墙钟预算慢首字超级优先账户',
      type: 'api_key',
      credentials: { api_key: primaryKey, base_url: upstreamBaseUrl },
      groupId: primaryGroup.id,
      status: 'active',
      schedulable: true,
      superPriorityEnabled: true,
      priority: 0,
      supportedModels: ['gpt-5.5']
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '墙钟预算多 Key 普通优先账户',
      type: 'api_key',
      credentials: {
        api_key: multiKeyPool[0],
        api_keys: [...multiKeyPool],
        api_key_strategy: 'round_robin',
        base_url: upstreamBaseUrl
      },
      groupId: primaryGroup.id,
      status: 'active',
      schedulable: true,
      priority: 1,
      supportedModels: ['gpt-5.5']
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '墙钟预算低优先账户',
      type: 'api_key',
      credentials: { api_key: lowerPriorityKey, base_url: upstreamBaseUrl },
      groupId: primaryGroup.id,
      status: 'active',
      schedulable: true,
      priority: 2,
      supportedModels: ['gpt-5.5']
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '墙钟预算尾部低优先账户',
      type: 'api_key',
      credentials: { api_key: exhaustedTailKey, base_url: upstreamBaseUrl },
      groupId: primaryGroup.id,
      status: 'active',
      schedulable: true,
      priority: 3,
      supportedModels: ['gpt-5.5']
    }, access)
  ]
  for (const account of accounts) activateAccount(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '墙钟预算网关 Key',
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: 10_000,
      speedFirstConfig: {
        slowTriggerCount: 2,
        slowWindowSeconds: 300,
        recoverySuccessCount: 3,
        probeIntervalSeconds: 10,
        degradedTtlSeconds: 60,
        maxFirstByteRetriesPerRequest: 2
      }
    },
    status: 'active'
  }, access)
  assert(apiKey.key && apiKey.routeStrategyId)
  return {
    apiKey: apiKey.key,
    routeStrategyId: apiKey.routeStrategyId,
    groupId: primaryGroup.id,
    primaryAccountId: accounts[0]!.id,
    primaryKey,
    multiKeyPool,
    lowerPriorityKey,
    exhaustedTailKey
  }
}

async function primePrimarySlowObservation(scenario: WallBudgetScenario): Promise<void> {
  const account = repositories.findOpenAIAccountForGroup(
    scenario.groupId,
    scenario.primaryAccountId,
    access.systemAccountId
  )
  assert(account, '慢首字预观察必须能读取主账户运行凭据')
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  const result = await latencyDegradation.recordNormalRouteFirstByteSlowAsync(account, scope, {
    firstByteDeadlineMs: 10_000,
    slowTriggerCount: 2,
    slowWindowSeconds: 300,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 10,
    degradedTtlSeconds: 60,
    maxFirstByteRetriesPerRequest: 2
  }, '墙钟预算 Mock 预置一个慢首字样本')
  assert.equal(result?.degraded, false, '单个预置慢样本不得提前降级账户')
}

function createHardDeadScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '墙钟预算全池硬死亡分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '墙钟预算禁用账户',
    type: 'api_key',
    credentials: { api_key: 'sk-wall-disabled', base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'disabled',
    schedulable: false,
    supportedModels: ['gpt-5.5']
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '墙钟预算全池硬死亡网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)
  return apiKey.key
}

function activateAccount(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true)
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    req.resume()
    req.once('end', () => {
      const key = bearerKey(req.headers.authorization)
      upstreamHits.push(key)
      if (key === 'sk-wall-raw-primary') {
        const firstChunk = `{"padding":"${'x'.repeat(1_100_000)}`
        const tail = 'wall-raw-late-tail","choices":[]}'
        res.once('close', () => { rawWallReaderClosed = true })
        res.writeHead(200, {
          'content-type': 'application/json; charset=utf-8',
          'content-length': String(Buffer.byteLength(`${firstChunk}${tail}`))
        })
        res.flushHeaders()
        res.write(firstChunk)
        releaseRawWallTail = () => {
          fakeNowMs += 300_000
          if (res.destroyed || res.writableEnded) return
          res.write(tail)
          const timer = originalSetTimeout(() => {
            if (!res.destroyed && !res.writableEnded) res.end()
          }, 25)
          timer.unref()
        }
        return
      }
      if (key === 'sk-wall-raw-backup') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ marker: 'wall-raw-backup-must-not-run' }))
        return
      }
      if (key === 'sk-wall-stream-primary') {
        res.once('close', () => { streamWallReaderClosed = true })
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.flushHeaders()
        res.write(`data: ${'x'.repeat(320 * 1024)}`)
        releaseStreamWallTail = () => {
          fakeNowMs += 300_000
          if (res.destroyed || res.writableEnded) return
          res.write('\n\n')
          const timer = originalSetTimeout(() => {
            if (!res.destroyed && !res.writableEnded) res.end()
          }, 25)
          timer.unref()
        }
        return
      }
      if (key === 'sk-wall-stream-backup') {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.end('data: {"marker":"wall-stream-backup-must-not-run"}\n\n')
        return
      }
      if (clientRetryAccountKeys.includes(key as typeof clientRetryAccountKeys[number])) {
        respondToClientRetryScenario(res, key)
        return
      }
      if (key === 'sk-wall-slow-primary') {
        const timer = originalSetTimeout(() => {
          if (res.destroyed || res.writableEnded) return
          res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
          res.end(JSON.stringify({ marker: intermediateMarkers[0] }))
        }, 1_000)
        timer.unref()
        return
      }
      if (key === 'sk-wall-multi-a') {
        fakeNowMs += 60_000
        res.writeHead(418, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ marker: intermediateMarkers[0] }))
        return
      }
      if (key === 'sk-wall-multi-b') {
        fakeNowMs += 60_000
        res.writeHead(200, {
          'content-type': 'application/json; charset=utf-8',
          'content-length': '4096'
        })
        res.flushHeaders()
        res.write(JSON.stringify({ marker: intermediateMarkers[1] }))
        const timer = originalSetTimeout(() => res.destroy(), 5)
        timer.unref()
        return
      }
      if (key === 'sk-wall-lower-priority') {
        fakeNowMs += 44_000
        res.once('close', () => { wallBodyReaderClosed = true })
        res.writeHead(200, {
          'content-type': 'application/json; charset=utf-8',
          'content-length': '4096'
        })
        res.flushHeaders()
        wallBodyFragmentSent = true
        res.write(`{"marker":"${intermediateMarkers[2]}","choices":[`)
        const timer = originalSetTimeout(() => {
          fakeNowMs += 15_500
          if (!res.destroyed && !res.writableEnded) res.write(' ')
        }, 5)
        timer.unref()
        return
      }
      sendSuccess(res, key)
    })
  })
}

function respondToClientRetryScenario(res: http.ServerResponse, key: string): void {
  const accountIndex = clientRetryAccountKeys.indexOf(key as typeof clientRetryAccountKeys[number])
  assert(accountIndex >= 0, `客户端重试 Mock 收到未知账户 Key：${key}`)
  if (
    mockMode === 'client_retry_current_best_third'
    || (mockMode === 'client_retry_first_recovered' && accountIndex === 0)
  ) {
    sendSuccess(res, key)
    return
  }
  const untrustedStatuses = [429, 401, 500] as const
  res.writeHead(untrustedStatuses[accountIndex]!, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ marker: `mock_client_retry_account_${accountIndex + 1}_failed` }))
}

function sendSuccess(res: http.ServerResponse, key: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl_${key}`,
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content: `mock recovered response from ${key}` }, finish_reason: 'stop' }]
  }))
}

function bearerKey(authorization: string | undefined): string {
  return String(authorization ?? '').replace(/^Bearer\s+/i, '')
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  content: string,
  identity: { sessionId?: string; clientIp?: string } = {}
): Promise<{ status: number; text: string }> {
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json'
  }
  if (identity.sessionId) headers['x-session-id'] = identity.sessionId
  if (identity.clientIp) headers['x-forwarded-for'] = identity.clientIp
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false,
      ...(identity.sessionId ? { session_id: identity.sessionId } : {})
    })
  })
  return { status: response.status, text: await response.text() }
}

function postChatOverRawHttp(
  baseUrl: string,
  apiKey: string,
  stream: boolean,
  traceId: string,
  onData: (totalBodyBytes: number) => void
): Promise<PartialHttpResponse> {
  return new Promise((resolvePromise, rejectPromise) => {
    const body = JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: `committed wall ${stream ? 'stream' : 'raw json'}` }],
      stream
    })
    let responseStarted = false
    let settled = false
    const timer = originalSetTimeout(() => finishError(new Error(`等待已提交墙钟响应超时：${traceId}`)), 5_000)
    timer.unref()
    const request = http.request(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json',
        accept: stream ? 'text/event-stream' : 'application/json',
        'content-length': String(Buffer.byteLength(body)),
        'x-trace-id': traceId
      }
    })
    const finishError = (error: Error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      request.destroy()
      rejectPromise(error)
    }
    request.once('error', (error) => {
      if (!responseStarted) finishError(error)
    })
    request.once('response', (response) => {
      responseStarted = true
      const chunks: Buffer[] = []
      let totalBodyBytes = 0
      let ended = false
      const finish = (terminated: boolean) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        resolvePromise({
          status: response.statusCode ?? 0,
          headers: response.headers,
          body: Buffer.concat(chunks),
          terminated
        })
      }
      response.on('data', (chunk: Buffer) => {
        const buffer = Buffer.from(chunk)
        chunks.push(buffer)
        totalBodyBytes += buffer.length
        onData(totalBodyBytes)
      })
      response.once('end', () => {
        ended = true
        finish(false)
      })
      response.once('aborted', () => finish(true))
      response.once('error', () => finish(true))
      response.once('close', () => {
        if (!ended) finish(true)
      })
    })
    request.end(body)
  })
}

async function gatewayMetadataPayloads(auditLogId: string): Promise<Array<{
  label?: string
  metadata?: Record<string, unknown>
}>> {
  const detail = repositories.getAuditLogDetail(auditLogId)
  if (!detail) return []
  const payloads = await Promise.all(detail.payloads
    .filter((payload) => payload.partType === 'gateway_metadata')
    .map((payload) => repositories.getAuditLogPayload(auditLogId, payload.id)))
  return payloads
    .map((payload) => parseJsonObject(payload?.bodyText ?? ''))
    .filter((payload) => payload.type === 'gateway_metadata')
    .map((payload) => ({
      label: typeof payload.label === 'string' ? payload.label : undefined,
      metadata: isRecord(payload.metadata) ? payload.metadata : undefined
    }))
}

function parseJsonObject(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value) as unknown
    return isRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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

async function waitForCondition(predicate: () => boolean, message: string, timeoutMs = 1_000): Promise<void> {
  const deadlineAt = originalDateNow() + timeoutMs
  while (!predicate() && originalDateNow() < deadlineAt) {
    await new Promise<void>((resolvePromise) => originalSetTimeout(resolvePromise, 5))
  }
  assert.equal(predicate(), true, message)
}
