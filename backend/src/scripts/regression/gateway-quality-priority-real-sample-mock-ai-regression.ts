import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { RouteStrategySpeedFirstConfig } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { decideHotQualityCandidate } from '../../modules/gateway/routing/hot-quality-candidate-selection.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import { recoverableUnavailableWaitCoordinatorSnapshotForTest } from '../../modules/gateway/runtime/recoverable-unavailable-wait.js'
import { speedFirstCutoverBudgetSnapshot } from '../../modules/gateway/runtime/speed-first-cutover-reservation.service.js'
import { speedFirstBodyAdmissionSnapshot } from '../../modules/gateway/runtime/speed-first-body-admission.service.js'
import type { UpstreamAttempt } from '../../modules/gateway/upstream/attempt.js'
import { clearAccountConcurrency, snapshotAccountConcurrency } from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

type MockPhase = 'fast' | 'opaque' | 'transport_reset' | 'slow_first_byte'

interface UpstreamHit {
  accountKey: string
  path: string
  requestLabel: string
  phase: MockPhase
}

interface ScenarioAccount {
  id: string
  key: string
}

interface Scenario {
  apiKey: string
  groupId: string
  routeStrategyId: string
  accounts: ScenarioAccount[]
}

const model = 'gpt-5.5'
const firstByteDeadlineMs = 10_000
const slowFirstByteDelayMs = 12_000
const speedConfig: RouteStrategySpeedFirstConfig = {
  slowTriggerCount: 2,
  slowWindowSeconds: 60,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 1
}
const tempRoot = resolve(
  tmpdir(),
  `juhe-ai-quality-priority-real-sample-${Date.now()}-${Math.random().toString(16).slice(2)}`
)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'quality-priority-real-sample-mock-secret'
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
  latencyDegradation,
  accountTestService,
  circuitRecovery,
  accountProbeOutcome,
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
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/background/account-circuit-recovery.service.js'),
  import('../../modules/accounts/automatic-account-probe-outcome.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const phases = new Map<string, MockPhase>()
const hits: UpstreamHit[] = []
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    textFirstResponseTimeoutSeconds: 30,
    textStreamIdleTimeoutSeconds: 30,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

  const sameTier = createScenario({
    name: '真实样本同层重选',
    upstreamBaseUrl,
    schedulingPreference: 'cost_first',
    accounts: [
      { key: 'sk-real-quality-a', priority: 0 },
      { key: 'sk-real-quality-b', priority: 0 }
    ]
  })
  const clientRetry = createScenario({
    name: '新客户端请求重新计算',
    upstreamBaseUrl,
    schedulingPreference: 'cost_first',
    accounts: [
      { key: 'sk-real-retry-1', priority: 0 },
      { key: 'sk-real-retry-2', priority: 0 },
      { key: 'sk-real-retry-3', priority: 0 }
    ]
  })
  const costPriority = createScenario({
    name: '成本质量硬优先级',
    upstreamBaseUrl,
    schedulingPreference: 'cost_first',
    accounts: [
      { key: 'sk-real-cost-high', priority: 0, superPriorityEnabled: true },
      { key: 'sk-real-cost-low', priority: 10 }
    ]
  })
  const speedPriority = createScenario({
    name: '速度优先跨层与恢复',
    upstreamBaseUrl,
    schedulingPreference: 'speed_first',
    accounts: [
      { key: 'sk-real-speed-high', priority: 0, superPriorityEnabled: true },
      { key: 'sk-real-speed-low', priority: 10 }
    ]
  })

  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`

  await assertSameTierRealSampleReselection(gatewayBaseUrl, sameTier)
  await assertClientRetryStartsFromCurrentPool(gatewayBaseUrl, clientRetry)
  await assertCostQualityCannotCrossPriority(gatewayBaseUrl, costPriority)
  await assertSpeedFirstCrossPriorityAndRecovery(gatewayBaseUrl, speedPriority)
  await assertFinalStateClean([sameTier, clientRetry, costPriority, speedPriority])

  console.log(JSON.stringify({
    message: 'gateway quality priority real sample mock ai regression passed',
    upstreamHits: hits.length,
    elapsedSlowSamplesMs: speedConfig.slowTriggerCount * slowFirstByteDelayMs
  }))
} finally {
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSameTierRealSampleReselection(baseUrl: string, scenario: Scenario): Promise<void> {
  const [accountA, accountB] = scenario.accounts
  assert(accountA && accountB)
  setPhase(accountA.key, 'opaque')
  setPhase(accountB.key, 'fast')

  const initial = await postChat(baseUrl, scenario.apiKey, 'quality-opaque-then-success', 'quality-session-1')
  assert.equal(initial.status, 200, `同层首请求应隐藏 A 的完整异常并由 B 成功：${initial.text}`)
  assert.deepEqual(hitKeys('quality-opaque-then-success'), [accountA.key, accountB.key])
  const neutralA = await qualitySnapshot(scenario, accountA)
  const successfulB = await qualitySnapshot(scenario, accountB)
  assert.equal(neutralA.window5m.qualityAttempts, 0, '完整 opaque HTTP 不得进入 A 的共享质量分母')
  assert.equal(neutralA.window5m.upstreamResponseFailures, 1, 'A 的完整 opaque HTTP 只增加中性诊断')
  assert.equal(successfulB.window5m.completedResponses, 1, 'B 的真实协议成功必须形成热质量样本')

  const selectedBySample = await postChat(baseUrl, scenario.apiKey, 'quality-current-sample-reselects-b', 'quality-session-2')
  assert.equal(selectedBySample.status, 200)
  assert.deepEqual(hitKeys('quality-current-sample-reselects-b'), [accountB.key], '下一请求必须按当前真实样本直接重选 B')

  const beforeTransportB = await qualitySnapshot(scenario, accountB)
  setPhase(accountA.key, 'fast')
  setPhase(accountB.key, 'transport_reset')
  const transportFallback = await postChat(baseUrl, scenario.apiKey, 'quality-b-transport-a-recovers', 'quality-session-3')
  assert.equal(transportFallback.status, 200, `B transport 失败后应切到已恢复 A：${transportFallback.text}`)
  assert.deepEqual(hitKeys('quality-b-transport-a-recovers'), [accountB.key, accountA.key])
  const suspectB = await protocolCircuit(scenario, accountB)
  assert.equal(suspectB.phase, 'SUSPECT', 'B 首个真实 transport 失败只能进入 SUSPECT')
  assert.equal(suspectB.lease, undefined, '前台首个 transport 失败结束后不得遗留 confirmation lease')
  const afterTransportB = await qualitySnapshot(scenario, accountB)
  assert.equal(
    afterTransportB.window5m.localTransportFailures,
    beforeTransportB.window5m.localTransportFailures,
    '没有独立 confirmation 的首个 transport 失败不得污染共享质量失败'
  )

  setPhase(accountB.key, 'fast')
  const recoveryHitStart = hits.length
  const recovery = await createCircuitRecoveryService(
    scenario,
    suspectB.retryAtMs ?? Date.now() + 3_000
  ).sweep()
  assert.equal(recovery.framingCompleteCount, 1, '后台 due sweep 必须通过真实 framing 探针主动恢复无业务流量的 B')
  assert(
    hits.slice(recoveryHitStart).some(hit => hit.accountKey === accountB.key),
    '后台恢复必须真实命中 B，而不是依赖客户端流量碰巧重选'
  )
  assert.equal((await protocolCircuit(scenario, accountB)).phase, 'CLOSED', 'B 的真实 framing 成功必须关闭 SUSPECT')

  const afterRecovery = await postChat(baseUrl, scenario.apiKey, 'quality-after-recovery-current-best', 'quality-session-5')
  assert.equal(afterRecovery.status, 200)
  assert.deepEqual(
    hitKeys('quality-after-recovery-current-best'),
    [await expectedCurrentQualityAccountKey(scenario)],
    '恢复后的新请求必须按当前质量/速度重新选择，不能继承失败位置或旧切号游标'
  )
}

async function assertClientRetryStartsFromCurrentPool(baseUrl: string, scenario: Scenario): Promise<void> {
  for (const account of scenario.accounts) setPhase(account.key, 'opaque')
  const identity = { sessionId: 'same-client-retry-session', clientIp: '198.51.100.71' }
  const exhausted = await postChat(baseUrl, scenario.apiKey, 'client-retry-all-opaque', identity.sessionId, identity.clientIp)
  assert.equal(exhausted.status, 503, `首请求应有界穷尽后返回稳定网关 503：${exhausted.text}`)
  assert.deepEqual(hitKeys('client-retry-all-opaque'), scenario.accounts.map(account => account.key))
  assert.doesNotMatch(exhausted.text, /sk-real-retry|mock opaque/i, '客户端不得收到上游凭据或原始异常正文')

  for (const account of scenario.accounts) {
    const snapshot = await qualitySnapshot(scenario, account)
    assert.equal(snapshot.window5m.qualityAttempts, 0, '完整 HTTP 异常不得制造跨请求质量游标')
  }

  setPhase(scenario.accounts[0]!.key, 'fast')
  const retried = await postChat(baseUrl, scenario.apiKey, 'client-retry-first-recovered', identity.sessionId, identity.clientIp)
  assert.equal(retried.status, 200, `同客户端下一请求应从当前池重新计算：${retried.text}`)
  assert.deepEqual(
    hitKeys('client-retry-first-recovered'),
    [scenario.accounts[0]!.key],
    '上次穷尽到 3 不得让下次请求从旧游标继续；恢复的 1 应直接命中'
  )
}

async function assertCostQualityCannotCrossPriority(baseUrl: string, scenario: Scenario): Promise<void> {
  const [high, low] = scenario.accounts
  assert(high && low)
  setPhase(high.key, 'opaque')
  setPhase(low.key, 'fast')

  for (let index = 1; index <= 2; index += 1) {
    const label = `cost-hard-priority-${index}`
    const response = await postChat(baseUrl, scenario.apiKey, label, `cost-session-${index}`)
    assert.equal(response.status, 200)
    assert.deepEqual(
      hitKeys(label),
      [high.key, low.key],
      '低优先级即使已有更好真实质量样本，cost/质量策略也必须先尝试高业务优先级'
    )
  }
  const highQuality = await qualitySnapshot(scenario, high)
  const lowQuality = await qualitySnapshot(scenario, low)
  assert.equal(highQuality.window5m.qualityAttempts, 0, '高层完整 HTTP 异常必须保持质量中性')
  assert.equal(lowQuality.window5m.completedResponses, 2, '低层必须已由真实网关成功形成更充分样本')
  assert.equal((await protocolCircuit(scenario, high)).phase, 'CLOSED', '完整 HTTP 异常不得借状态码打开高层电路')

  setPhase(high.key, 'fast')
  const recovered = await postChat(baseUrl, scenario.apiKey, 'cost-high-recovered', 'cost-session-recovered')
  assert.equal(recovered.status, 200)
  assert.deepEqual(hitKeys('cost-high-recovered'), [high.key], '高层恢复后新请求必须回到主层，不继承低层 fallback')
}

async function assertSpeedFirstCrossPriorityAndRecovery(baseUrl: string, scenario: Scenario): Promise<void> {
  const [high, low] = scenario.accounts
  assert(high && low)
  setPhase(high.key, 'slow_first_byte')
  setPhase(low.key, 'fast')
  const sessionId = 'speed-cross-priority-stable-session'

  for (let index = 1; index <= speedConfig.slowTriggerCount; index += 1) {
    const label = `speed-real-slow-${index}`
    const startedAt = Date.now()
    const response = await postChat(baseUrl, scenario.apiKey, label, sessionId)
    assert.equal(response.status, 200, `第 ${index} 个真实慢首字请求应成功：${response.text}`)
    assert(Date.now() - startedAt >= firstByteDeadlineMs - 1_500, '慢首字样本必须真实等待到配置阈值附近')
    assert.deepEqual(
      hitKeys(label),
      index < speedConfig.slowTriggerCount ? [high.key] : [high.key, low.key],
      `第 ${index} 个真实慢样本的跨优先级命中顺序错误`
    )
  }

  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope)
  assert.equal(
    await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: high.id }, scope),
    true,
    '两个真实慢首字样本必须确认高层速度降级'
  )
  const crossed = await postChat(baseUrl, scenario.apiKey, 'speed-degraded-direct-low', sessionId)
  assert.equal(crossed.status, 200)
  assert.deepEqual(hitKeys('speed-degraded-direct-low'), [low.key], 'speed_first 确认降级后应直接跨优先级选择低层快号')
  assert.equal((await protocolCircuit(scenario, high)).phase, 'CLOSED', '配置型首字截止不得推进高层 transport 电路')

  setPhase(high.key, 'fast')
  const accountSummary = repositories.findAccountForTest(high.id, { systemAccountId: access.systemAccountId, role: 'user' })
  assert(accountSummary)
  const candidateAccount = repositories.findOpenAIAccountForGroup(
    scenario.groupId,
    high.id,
    access.systemAccountId,
    { ignoreAvailability: true }
  )
  assert(candidateAccount)

  for (let index = 1; index <= speedConfig.recoverySuccessCount; index += 1) {
    const candidate = (await latencyDegradation.listNormalRouteLatencyProbeCandidatesAsync(100, Date.now() + 20_000))
      .find(item => item.accountId === high.id && item.scope.routeStrategyId === scenario.routeStrategyId)
    assert(candidate, `第 ${index} 次恢复前必须保留高层探针候选`)
    const hitStart = hits.length
    const result = await accountTestService.testOpenAIAccount(accountSummary, {
      model,
      diagnostics: 'limited',
      groupId: scenario.groupId,
      systemAccountId: access.systemAccountId,
      trafficSource: 'runtime_recovery_probe',
      candidateAccount,
      disableAccountStateMutation: true,
      gatewaySettingsOverride: {
        temporaryUnschedulableRetryAttempts: 0,
        temporaryUnschedulableRetryIntervalSeconds: 0,
        textFirstResponseTimeoutSeconds: 20,
        noAvailableAccountWaitTimeoutSeconds: 20,
        textUncommittedAttemptMaxLifetimeSeconds: 60
      }
    })
    assert.equal(result.success, true, `第 ${index} 个真实恢复探针必须成功：${result.message ?? result.errorCode ?? 'unknown'}`)
    assert(result.firstTokenMs !== undefined && result.firstTokenMs <= firstByteDeadlineMs)
    const recovery = await latencyDegradation.recordNormalRouteFirstByteSuccessAsync(
      candidateAccount,
      candidate.scope,
      candidate.config,
      result.firstTokenMs
    )
    assert.equal(recovery?.cleared, index === speedConfig.recoverySuccessCount)
    assert(
      hits.slice(hitStart).some(hit => hit.accountKey === high.key),
      `第 ${index} 个恢复证据必须真实请求高层账户`
    )
  }

  assert.equal(
    await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: high.id }, scope),
    false,
    '达到真实恢复成功阈值后必须清理高层速度降级'
  )
  const backToPrimary = await postChat(baseUrl, scenario.apiKey, 'speed-after-real-recovery', sessionId)
  assert.equal(backToPrimary.status, 200)
  assert.deepEqual(
    hitKeys('speed-after-real-recovery'),
    [high.key],
    '恢复后的同 session 新请求必须回主层，不得继承上次跨层替补或旧游标'
  )
}

async function assertFinalStateClean(scenarios: Scenario[]): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  assert.deepEqual(snapshotAccountConcurrency(), {}, '所有账户并发槽必须清零')
  assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '高并发短队列、timer 和索引必须清零')
  assert.deepEqual(speedFirstCutoverBudgetSnapshot(), [], '速度切换 rescue budget 必须清零')
  assert.deepEqual(speedFirstBodyAdmissionSnapshot(), [], '速度优先 body admission 必须清零')
  assert.deepEqual(
    recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    '可恢复等待 scope、waiter 和 timer 必须清零'
  )
  const sideEffects = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(sideEffects.queueLength, 0, '持久副作用队列必须清零')
  assert.equal(sideEffects.processing, false, '副作用 drain 必须停止')
  assert.equal(sideEffects.precheckPendingAccountCount, 0, 'precheck 队列必须清零')
  assert.equal(sideEffects.recoveryProbePendingAccountCount, 0, 'recovery probe 队列必须清零')
  assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, 'precheck half-open lease 必须清零')

  for (const scenario of scenarios) {
    for (const account of scenario.accounts) {
      const summary = repositories.findAccountForTest(account.id, access)
      assert.equal(summary?.status, 'active', `${account.key} 最终必须保持 active`)
      assert.equal(summary?.schedulable, true, `${account.key} 最终必须保持 schedulable`)
      assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${account.key} Key 不得被内部错误语义禁用`)
      assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, `${account.key} Key 池不得被写成全不可用`)
      const protocol = await protocolCircuit(scenario, account)
      assert.notEqual(protocol.phase, 'OPEN', `${account.key} protocol/model circuit 不得最终 OPEN`)
      assert.equal(protocol.lease, undefined, `${account.key} protocol/model lease 必须清零`)
      const runtimeAccount = dispatchAccount(scenario, account)
      const parent = await accountCircuit.getGatewayAccountCircuitStore().get({
        kind: 'account',
        accountRuntimeKey: gatewayAccountRuntimeKey(runtimeAccount)
      })
      assert.notEqual(parent.phase, 'OPEN', `${account.key} account circuit 不得最终 OPEN`)
      assert.equal(parent.lease, undefined, `${account.key} account lease 必须清零`)
    }
  }
}

function createScenario(input: {
  name: string
  upstreamBaseUrl: string
  schedulingPreference: 'cost_first' | 'speed_first'
  accounts: Array<{ key: string; priority: number; superPriorityEnabled?: boolean }>
}): Scenario {
  const group = repositories.createGroup({
    name: `${input.name} Mock 分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const accounts = input.accounts.map((fixture, index) => {
    phases.set(fixture.key, 'fast')
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `${String(index + 1).padStart(2, '0')}-${input.name}-${fixture.key}`,
      type: 'api_key',
      credentials: {
        api_key: fixture.key,
        base_url: input.upstreamBaseUrl,
        supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      priority: fixture.priority,
      superPriorityEnabled: fixture.superPriorityEnabled,
      supportedModels: [model],
      healthCheckModel: model,
      healthCheckEndpointMode: 'chat_sse'
    }, access)
    assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
      intervalHours: 24,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true)
    return { id: account.id, key: fixture.key }
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${input.name} Mock 网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: input.schedulingPreference === 'speed_first'
      ? { schedulingPreference: 'speed_first', firstByteDeadlineMs, speedFirstConfig: speedConfig }
      : { schedulingPreference: 'cost_first' },
    status: 'active'
  }, access)
  assert(apiKey.key && apiKey.routeStrategyId)
  return { apiKey: apiKey.key, routeStrategyId: apiKey.routeStrategyId, groupId: group.id, accounts }
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.once('end', () => {
      const accountKey = bearerKey(req.headers.authorization)
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const requestLabel = requestLabelFromBody(bodyText)
      const stream = requestStreamFlag(bodyText)
      const phase = phases.get(accountKey) ?? 'fast'
      hits.push({ accountKey, path: req.url ?? '', requestLabel, phase })
      if (phase === 'transport_reset') {
        res.destroy()
        return
      }
      if (phase === 'opaque') {
        res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { code: 'mock opaque', message: `mock opaque ${accountKey}` } }))
        return
      }
      if (phase === 'slow_first_byte') {
        const timer = setTimeout(() => {
          if (res.destroyed || res.writableEnded) return
          sendSuccess(res, accountKey, req.url ?? '', requestLabel, stream)
        }, slowFirstByteDelayMs)
        timer.unref()
        return
      }
      sendSuccess(res, accountKey, req.url ?? '', requestLabel, stream)
    })
  })
}

function createCircuitRecoveryService(
  scenario: Scenario,
  nowMs: number
): InstanceType<typeof circuitRecovery.AccountCircuitRecoveryService> {
  const resolver = circuitRecovery.createScheduledAccountCircuitRecoveryResolver({
    findAccountForTest: async (accountId, scopeAccess) => repositories.findAccountForTest(accountId, scopeAccess),
    findOpenAIAccountForGroup: async (groupId, accountId, systemAccountId) => repositories.findOpenAIAccountForGroup(
      groupId,
      accountId,
      systemAccountId,
      { includeUnavailable: true, ignoreAvailability: true }
    ),
    probe: async (input) => {
      let upstreamAttempt: UpstreamAttempt | undefined
      const result = await accountTestService.testOpenAIAccount(input.account, {
        diagnostics: 'limited',
        groupId: input.groupId,
        systemAccountId: input.systemAccountId,
        model: input.model,
        signal: input.signal,
        trafficSource: 'runtime_recovery_probe',
        testEndpointMode: input.account.healthCheckEndpointMode,
        candidateAccount: input.candidateAccount,
        disableAccountStateMutation: true,
        onUpstreamAttempt: attempt => { upstreamAttempt = attempt }
      })
      return accountProbeOutcome.transportProbeOutcomeFromAccountTestResult(result, {
        upstreamAttempt,
        canceled: input.signal.aborted
      })
    }
  })
  return new circuitRecovery.AccountCircuitRecoveryService(
    accountCircuit.getGatewayAccountCircuitStore(),
    resolver,
    { batchSize: 10, leaseDurationMs: 30_000, now: () => nowMs }
  )
}

function sendSuccess(
  res: http.ServerResponse,
  accountKey: string,
  path: string,
  requestLabel: string,
  stream: boolean
): void {
  if (stream && path.includes('/chat/completions')) {
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    res.write(`data: ${JSON.stringify({
      id: `chatcmpl_${accountKey}`,
      object: 'chat.completion.chunk',
      model,
      choices: [{ index: 0, delta: { role: 'assistant', content: `mock success ${accountKey}` }, finish_reason: null }]
    })}\n\n`)
    res.write(`data: ${JSON.stringify({
      id: `chatcmpl_${accountKey}`,
      object: 'chat.completion.chunk',
      model,
      choices: [{ index: 0, delta: {}, finish_reason: 'stop' }]
    })}\n\n`)
    res.end('data: [DONE]\n\n')
    return
  }
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  if (path.includes('/responses')) {
    res.end(JSON.stringify({
      id: `resp_${accountKey}`,
      object: 'response',
      status: 'completed',
      model,
      output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: `mock success ${accountKey}` }] }],
      usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 }
    }))
    return
  }
  res.end(JSON.stringify({
    id: `chatcmpl_${accountKey}`,
    object: 'chat.completion',
    model,
    choices: [{ index: 0, message: { role: 'assistant', content: `mock success ${accountKey} ${requestLabel}` }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }))
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  requestLabel: string,
  sessionId: string,
  clientIp?: string
): Promise<{ status: number; text: string }> {
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    'x-session-id': sessionId
  }
  if (clientIp) headers['x-forwarded-for'] = clientIp
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: requestLabel }],
      session_id: sessionId,
      stream: false,
      max_tokens: 16
    })
  })
  return { status: response.status, text: await response.text() }
}

function dispatchAccount(scenario: Scenario, account: ScenarioAccount) {
  const result = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
    .find(candidate => candidate.id === account.id)
  assert(result, `找不到调度账户 ${account.key}`)
  return result
}

function qualityScope(scenario: Scenario, account: ScenarioAccount) {
  const runtimeAccount = dispatchAccount(scenario, account)
  return {
    accountRuntimeKey: gatewayAccountRuntimeKey(runtimeAccount),
    protocolProfile: runtimeAccount.providerProtocolProfileId || `${runtimeAccount.protocolCode}:${runtimeAccount.protocolVersion}`,
    requestLane: 'text' as const,
    modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
  }
}

async function qualitySnapshot(scenario: Scenario, account: ScenarioAccount) {
  const snapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope(scenario, account))
  assert(snapshot, `缺少 ${account.key} 热质量快照`)
  return snapshot
}

async function expectedCurrentQualityAccountKey(scenario: Scenario): Promise<string> {
  const routeScopeKey = `real-sample:${scenario.routeStrategyId}:${scenario.groupId}`
  const candidates = await Promise.all(scenario.accounts.map(async (account, index) => {
    const runtimeAccount = dispatchAccount(scenario, account)
    return {
      accountId: account.id,
      accountRuntimeKey: gatewayAccountRuntimeKey(runtimeAccount),
      routeScopeKey,
      configurationTier: {
        modelMatchRank: 0,
        fallbackEnabled: runtimeAccount.fallbackEnabled,
        superPriorityEnabled: runtimeAccount.superPriorityEnabled,
        priority: runtimeAccount.priority
      },
      stableBindingOrder: index,
      hotQuality: await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope(scenario, account))
    }
  }))
  const selectedId = decideHotQualityCandidate({ mode: 'cost_first', routeScopeKey, candidates }).selectedCandidate?.accountId
  const selected = scenario.accounts.find(account => account.id === selectedId)
  assert(selected, '当前质量快照必须选出一个同层账户')
  return selected.key
}

async function protocolCircuit(scenario: Scenario, account: ScenarioAccount) {
  return accountCircuit.getGatewayAccountCircuitStore().get(
    accountCircuit.gatewayAccountProtocolModelScope(dispatchAccount(scenario, account), 'text', model)
  )
}

function hitKeys(requestLabel: string): string[] {
  return hits.filter(hit => hit.requestLabel === requestLabel).map(hit => hit.accountKey)
}

function setPhase(accountKey: string, phase: MockPhase): void {
  phases.set(accountKey, phase)
}

function requestLabelFromBody(bodyText: string): string {
  try {
    const body = JSON.parse(bodyText) as {
      messages?: Array<{ content?: unknown }>
      input?: unknown
    }
    const message = body.messages?.[0]?.content
    if (typeof message === 'string') return message
    if (typeof body.input === 'string') return body.input
    return 'account-test-probe'
  } catch {
    return 'unparseable'
  }
}

function requestStreamFlag(bodyText: string): boolean {
  try {
    return (JSON.parse(bodyText) as { stream?: unknown }).stream === true
  } catch {
    return false
  }
}

function bearerKey(value: string | undefined): string {
  return String(value ?? '').replace(/^Bearer\s+/i, '')
}

function listen(server: http.Server): Promise<void> {
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
    server.close(error => error ? rejectPromise(error) : resolvePromise())
  })
}
