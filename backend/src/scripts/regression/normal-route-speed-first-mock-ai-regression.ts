import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import type { RouteStrategySpeedFirstConfig } from '../../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import { tryAcquireAccountConcurrencyAsync } from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'

type MockAccountKey =
  | 'sk-speed-primary'
  | 'sk-speed-secondary'
  | 'sk-cost-primary'
  | 'sk-cost-secondary'
  | 'sk-priority-super'
  | 'sk-priority-normal'
  | 'sk-stale-a'
  | 'sk-stale-b'
  | 'sk-stale-c'

type MockPhase = 'transport_reset' | 'slow_first_byte' | 'slow_response_headers' | 'fast'

interface MockUpstreamHit {
  authorization: string
  accountKey: string
  path: string
  bodyText: string
  stream: boolean
  phase: MockPhase
}

interface SpeedFirstScenario {
  apiKey: string
  routeStrategyId: string
  groupId: string
  primaryAccountId: string
  primaryAccountName: string
  secondaryAccountId: string
  secondaryAccountName: string
}

interface CostFirstScenario {
  apiKey: string
}

interface StaleCutoverScenario {
  apiKey: string
  routeStrategyId: string
  groupId: string
  firstAccountId: string
  slowAccountId: string
  healthyAccountId: string
}

const model = 'gpt-5.5'
const slowBodyDelayMs = 12_000
const firstByteDeadlineMs = 10_000
const speedFirstConfig: RouteStrategySpeedFirstConfig = {
  slowTriggerCount: 2,
  slowWindowSeconds: 60,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 2
}
const speedFirstRuntimeConfig = { ...speedFirstConfig, firstByteDeadlineMs }

const tempRoot = resolve(tmpdir(), `juhe-ai-normal-route-speed-first-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'normal-route-speed-first-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'normal-route-speed-first-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter, setNormalRouteSpeedFirstDecisionOperationsForTest },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  latencyDegradation,
  gatewayHotQuality,
  accountCircuit,
  cutoverReservations,
  accountTestService
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/speed-first-cutover-reservation.service.js'),
  import('../../modules/accounts/account-test.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: MockUpstreamHit[] = []
const accountPhases = new Map<MockAccountKey, MockPhase>([
  ['sk-speed-primary', 'slow_first_byte'],
  ['sk-speed-secondary', 'fast'],
  ['sk-cost-primary', 'fast'],
  ['sk-cost-secondary', 'fast'],
  ['sk-priority-super', 'fast'],
  ['sk-priority-normal', 'fast'],
  ['sk-stale-a', 'fast'],
  ['sk-stale-b', 'fast'],
  ['sk-stale-c', 'fast']
])

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    textFirstResponseTimeoutSeconds: 30,
    noAvailableAccountWaitTimeoutSeconds: 60
  })
  gatewayCache.clearGatewayRuntimeCache()

  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
    const speedScenario = createSpeedFirstScenario(upstreamBaseUrl)
    const costScenario = createCostFirstScenario(upstreamBaseUrl)
    const priorityScenario = createPriorityTierScenario(upstreamBaseUrl)
    const staleCutoverScenario = createStaleCutoverScenario(upstreamBaseUrl)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertTransientSlowThenFastDoesNotDegrade(baseUrl, speedScenario)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertNonStreamSlowFirstByteRetriesAndDegrades(baseUrl, speedScenario)
    await assertCostFirstRouteUnaffected(baseUrl, costScenario)
    await assertSpeedFirstCanCrossPriorityPreference(baseUrl, priorityScenario)
    await assertBackgroundProbeRestoresPrimary(baseUrl, speedScenario)
    await assertBulkFastTrafficAfterRecovery(baseUrl, speedScenario)
    await assertSpeedFirstCutoverDoesNotPersistSubstituteAffinity(baseUrl, speedScenario)
    await assertResponsesSlowFirstByteUsesObservationAndConfirmedCutover(baseUrl, speedScenario)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertResponseHeaderDeadlineUsesReservedCutover(baseUrl, speedScenario)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertLocalDecisionFailuresKeepCurrentUpstream(baseUrl, speedScenario)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertAlreadyAttemptedCandidateIsNotReserved(baseUrl, staleCutoverScenario)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertSpeedFirstDoesNotCutoverToAlreadyDegradedCandidate(baseUrl, speedScenario)
    await assertAllDegradedBypassKeepsOriginalOrder(baseUrl, speedScenario)
    await assertStreamSlowFirstByteRetriesBeforeDownstreamOutput(baseUrl, speedScenario)

    console.log('普通路由速度优先 Mock AI 回归通过：偶发慢后快样本清理、Chat/Responses 首字慢延迟切号、跨账户偏好覆盖、替补亲和回归、批量混合请求、降级后置、成本优先隔离、后台探针恢复、本地状态/样本/并发预占异常 fail-open、请求内旧候选不再预占、已降级候选不切换、全部降级旁路和流式首字确认切号均生效')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  setNormalRouteSpeedFirstDecisionOperationsForTest(undefined)
  cutoverReservations.setSpeedFirstCutoverSlotAcquirerForTest(undefined)
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  await removeTempRootWithRetry(tempRoot)
}

process.exit(0)

async function assertTransientSlowThenFastDoesNotDegrade(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')

  const firstSlowHitStart = upstreamHits.length
  const firstSlowStartedAt = Date.now()
  const firstSlowResponse = await postChat(baseUrl, scenario.apiKey, 'transient slow sample', false)
  assert.equal(firstSlowResponse.status, 200, `偶发慢请求应成功，实际 HTTP ${firstSlowResponse.status}: ${firstSlowResponse.text}`)
  assert(Date.now() - firstSlowStartedAt >= firstByteDeadlineMs - 2_500, '偶发慢请求应真实等待接近首字阈值')
  assert.match(firstSlowResponse.text, /late mock ai body/, '偶发慢未确认退化前应继续等待主号返回')
  const firstSlowHits = upstreamHits.slice(firstSlowHitStart)
  assert.equal(countHits(firstSlowHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '偶发慢应命中主号')
  assert.equal(countHits(firstSlowHits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '偶发慢未确认退化前不应切到副号')
  assert.equal((await listSpeedProbeCandidates(scenario)).length, 0, '单次偶发慢不应产生恢复探针候选')

  setAccountPhase('sk-speed-primary', 'fast')
  const fastHitStart = upstreamHits.length
  const fastResponse = await postChat(baseUrl, scenario.apiKey, 'transient slow recovered fast', false)
  assert.equal(fastResponse.status, 200, `偶发慢后的快请求应成功，实际 HTTP ${fastResponse.status}: ${fastResponse.text}`)
  assert.match(fastResponse.text, /mock ai chat sk-speed-primary/, '偶发慢后的快请求应回到主号')
  const fastHits = upstreamHits.slice(fastHitStart)
  assert.equal(countHits(fastHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '偶发慢后的快请求应命中主号')
  assert.equal(countHits(fastHits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '偶发慢后的快请求不应误切副号')

  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  const secondSlowHitStart = upstreamHits.length
  const secondSlowResponse = await postChat(baseUrl, scenario.apiKey, 'transient second slow after fast reset', false)
  assert.equal(secondSlowResponse.status, 200, `快样本清理后的再次慢请求应成功，实际 HTTP ${secondSlowResponse.status}: ${secondSlowResponse.text}`)
  assert.match(secondSlowResponse.text, /late mock ai body/, '快样本清理后再次慢应重新作为第一次观察，不应立即切号')
  const secondSlowHits = upstreamHits.slice(secondSlowHitStart)
  assert.equal(countHits(secondSlowHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '快样本清理后的再次慢应命中主号')
  assert.equal(countHits(secondSlowHits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '快样本清理后的再次慢不应切到副号')
  assert.equal((await listSpeedProbeCandidates(scenario)).length, 0, '快样本清理后再次单次慢不应产生恢复探针候选')

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
}

async function assertNonStreamSlowFirstByteRetriesAndDegrades(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')

  for (let attempt = 1; attempt <= speedFirstConfig.slowTriggerCount; attempt += 1) {
    const hitStart = upstreamHits.length
    const startedAt = Date.now()
    const response = await postChat(baseUrl, scenario.apiKey, `non stream slow sample ${attempt}`, false)
    assert.equal(response.status, 200, `第 ${attempt} 次慢首字请求应成功，实际 HTTP ${response.status}: ${response.text}`)
    assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, '慢首字样本应真实等待接近首字阈值')

    const hits = upstreamHits.slice(hitStart)
    assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, `第 ${attempt} 次请求应先命中主号`)
    if (attempt < speedFirstConfig.slowTriggerCount) {
      assert.match(response.text, /late mock ai body/, '未达到慢触发次数前应继续等待当前主号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 0, `第 ${attempt} 次请求未确认退化前不应切到副号`)
    } else {
      assert.match(response.text, /mock ai chat sk-speed-secondary/, '达到慢触发次数后应隐藏切到副号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 1, `第 ${attempt} 次请求确认退化后应切到副号`)
    }
    const candidates = await listSpeedProbeCandidates(scenario)
    assert.equal(
      candidates.some((candidate) => candidate.accountId === scenario.primaryAccountId),
      attempt >= speedFirstConfig.slowTriggerCount,
      `第 ${attempt} 次慢首字后的探针候选状态不符合触发次数`
    )
  }

  const candidate = await requireSpeedProbeCandidate(scenario)
  assert.equal(candidate.scope.groupId, scenario.groupId, '速度优先慢样本 scope 应绑定当前普通路由分组')
  assert.equal(candidate.runtimeKey, scenario.primaryAccountId, `速度优先慢样本 runtimeKey 应使用主号账号 ID，实际 ${candidate.runtimeKey}`)
  const runtimeAccounts = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
  const latencyOrder = await latencyDegradation.orderGatewayAccountsByNormalRouteLatencyDegradationAsync(runtimeAccounts, candidate.scope, speedFirstRuntimeConfig)
  assert.equal(latencyOrder.applied, true, `速度优先运行态排序应在达到触发次数后生效，candidate=${JSON.stringify({
    accountId: candidate.accountId,
    runtimeKey: candidate.runtimeKey,
    stateKey: candidate.stateKey,
    degradedUntil: candidate.degradedUntil,
    nextProbeAt: candidate.nextProbeAt,
    runtimeAccounts: runtimeAccounts.map((account) => ({
      id: account.id,
      runtimeKey: gatewayAccountRuntimeKey(account),
      accountAccessType: account.accountAccessType,
      accessType: (account as { accessType?: unknown }).accessType,
      boundGroupId: account.boundGroupId,
      bindingSystemAccountId: account.bindingSystemAccountId,
      accountAuthorizationId: account.accountAuthorizationId
    }))
  })}`)
  assert.deepEqual(
    latencyOrder.accounts.map((account) => account.id),
    [scenario.secondaryAccountId, scenario.primaryAccountId],
    '速度优先运行态排序应把主号后置到副号之后'
  )

  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'non stream after degradation', false)
  assert.equal(response.status, 200, `达到慢触发次数后请求应直接命中副号，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-speed-secondary/, '速度降级后应优先返回副号响应')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 0, '速度降级后不应再先打慢主号')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 1, '速度降级后应直接命中副号')

  const bulkHitStart = upstreamHits.length
  const bulkResponses = await runBulkChatRequests(baseUrl, scenario.apiKey, 'bulk after degradation', 60, 4)
  for (const [index, bulkResponse] of bulkResponses.entries()) {
    assert.equal(bulkResponse.status, 200, `降级后批量请求 ${index + 1} 应成功，实际 HTTP ${bulkResponse.status}: ${bulkResponse.text}`)
    assert.match(bulkResponse.text, /sk-speed-secondary/, `降级后批量请求 ${index + 1} 应稳定命中副号`)
    assertChatTransportShape(bulkResponse, `降级后批量请求 ${index + 1}`)
  }
  const bulkHits = upstreamHits.slice(bulkHitStart)
  assert.equal(countHits(bulkHits, 'sk-speed-primary', '/v1/chat/completions'), 0, '速度降级后批量请求不应回打慢主号')
  assert.equal(countHits(bulkHits, 'sk-speed-secondary', '/v1/chat/completions'), 60, '速度降级后批量请求应全部命中副号')
}

async function assertCostFirstRouteUnaffected(baseUrl: string, scenario: CostFirstScenario): Promise<void> {
  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'cost first route should keep original primary', false)
  assert.equal(response.status, 200, `成本优先路由应继续正常返回，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-cost-primary/, '成本优先路由应保持原主号调度')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-cost-primary', '/v1/chat/completions'), 1, '成本优先路由应命中自己的主号')
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 0, '速度优先降级状态不应污染成本优先路由')
}

async function assertSpeedFirstCanCrossPriorityPreference(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '账户偏好覆盖测试需要有效普通路由速度优先 scope')
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  setAccountPhase('sk-priority-super', 'slow_first_byte')
  setAccountPhase('sk-priority-normal', 'fast')
  const hitStart = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'speed first may cross account preference', false)
  assert.equal(response.status, 200, `超级优先账号确认慢时请求应切号成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-priority-normal/, '超级优先账号确认慢后应切到未降级普通优先级账号')
  assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, '当前请求应在首字阈值确认慢后再切到普通优先级账号')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-priority-super', '/v1/chat/completions'), 1, '确认慢请求应先命中超级优先账号')
  assert.equal(countHits(hits, 'sk-priority-normal', '/v1/chat/completions'), 1, '速度优先应允许切到普通优先级账号')
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-priority-super', 'fast')
  setAccountPhase('sk-priority-normal', 'fast')
}

async function assertBackgroundProbeRestoresPrimary(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  setAccountPhase('sk-speed-primary', 'fast')
  for (let index = 1; index <= speedFirstConfig.recoverySuccessCount; index += 1) {
    const candidate = await requireSpeedProbeCandidate(scenario)
    const account = repositories.findAccountForTest(scenario.primaryAccountId, { systemAccountId: access.systemAccountId, role: 'user' })
    assert(account, '恢复探针等价测试应能读取主号账户摘要')
    const candidateAccount = repositories.findOpenAIAccountForGroup(scenario.groupId, scenario.primaryAccountId, access.systemAccountId, { ignoreAvailability: true })
    assert(candidateAccount, '恢复探针等价测试应能读取主号网关候选账户')
    const hitStart = upstreamHits.length
    const result = await accountTestService.testOpenAIAccount(account, {
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
    assert.equal(result.success, true, `第 ${index} 次恢复探针等价账号测试应成功：${result.message ?? result.errorCode ?? 'unknown error'}`)
    assert(
      result.firstTokenMs !== undefined && result.firstTokenMs <= firstByteDeadlineMs,
      `第 ${index} 次恢复探针首字应满足阈值，实际 ${result.firstTokenMs}ms`
    )
    await latencyDegradation.recordNormalRouteFirstByteSuccessAsync(candidateAccount, candidate.scope, candidate.config, result.firstTokenMs)
    const hits = upstreamHits.slice(hitStart)
    assert(
      hits.some((hit) => hit.accountKey === 'sk-speed-primary' && (hit.path === '/v1/responses' || hit.path === '/v1/chat/completions')),
      `第 ${index} 次恢复探针应通过账号测试链路真实命中主号，hits=${JSON.stringify(hits.map((hit) => ({
        accountKey: hit.accountKey,
        path: hit.path,
        stream: hit.stream,
        phase: hit.phase
      })))}`
    )
    const stillDegraded = (await listSpeedProbeCandidates(scenario)).some((item) => item.accountId === scenario.primaryAccountId)
    assert.equal(stillDegraded, index < speedFirstConfig.recoverySuccessCount, `第 ${index} 次恢复探针后的降级清理状态不符合恢复次数`)
  }

  // This regression owns latency-degradation recovery; isolate it from hot-quality
  // samples accumulated by the deliberately slow attempts above.
  gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'after background probe recovery', false)
  assert.equal(response.status, 200, `后台探针恢复后请求应成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-speed-primary/, `后台探针恢复后应回到主号正常调度，实际=${response.text}，hits=${JSON.stringify(upstreamHits.slice(hitStart).map((hit) => ({ accountKey: hit.accountKey, path: hit.path, phase: hit.phase })))}`)
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, '恢复后应真实命中主号')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '恢复后不应继续绕到副号')
}

async function assertBulkFastTrafficAfterRecovery(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
  const hitStart = upstreamHits.length
  const responses = await runBulkChatRequests(baseUrl, scenario.apiKey, 'bulk after recovery', 120, 5)
  for (const [index, response] of responses.entries()) {
    assert.equal(response.status, 200, `恢复后批量请求 ${index + 1} 应成功，实际 HTTP ${response.status}: ${response.text}`)
    assert.match(response.text, /sk-speed-(primary|secondary)/, `恢复后批量请求 ${index + 1} 应命中同层健康账户`)
    assertChatTransportShape(response, `恢复后批量请求 ${index + 1}`)
  }
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 119, '恢复后主号应承接除单次同层探索外的全部请求')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 1, '恢复后只允许 credit 驱动的一次同层探索命中副号')
}

async function assertSpeedFirstCutoverDoesNotPersistSubstituteAffinity(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '速度切号亲和回归测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')

  const sessionId = `speed-first-cutover-affinity-${Date.now()}`
  const cutoverHitStart = upstreamHits.length
  const startedAt = Date.now()
  const cutoverResponse = await postChat(baseUrl, scenario.apiKey, 'speed cutover should not persist substitute affinity', false, { sessionId })
  assert.equal(cutoverResponse.status, 200, `速度切号请求应成功，实际 HTTP ${cutoverResponse.status}: ${cutoverResponse.text}`)
  assert.match(cutoverResponse.text, /mock ai chat sk-speed-secondary/, '确认慢切号后应由副号返回')
  assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, '速度切号应等待首字阈值确认慢后发生')
  const cutoverHits = upstreamHits.slice(cutoverHitStart)
  assert.equal(countHits(cutoverHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '速度切号请求应先命中主号')
  assert.equal(countHits(cutoverHits, 'sk-speed-secondary', '/v1/chat/completions'), 1, '速度切号请求应再命中副号')

  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
  const candidateAccount = repositories.findOpenAIAccountForGroup(scenario.groupId, scenario.primaryAccountId, access.systemAccountId, { ignoreAvailability: true })
  assert(candidateAccount, '速度切号亲和回归应能读取主号网关候选账户')
  for (let index = 1; index <= speedFirstConfig.recoverySuccessCount; index += 1) {
  const recovery = await latencyDegradation.recordNormalRouteFirstByteSuccessAsync(candidateAccount, scope, speedFirstRuntimeConfig, 100)
    assert.equal(recovery?.cleared, index >= speedFirstConfig.recoverySuccessCount, `第 ${index} 次恢复成功后的清理状态不符合预期`)
  }

  const recoveryHitStart = upstreamHits.length
  const recoveryResponse = await postChat(baseUrl, scenario.apiKey, 'speed cutover affinity after recovery', false, { sessionId })
  assert.equal(recoveryResponse.status, 200, `速度降级恢复后同 session 请求应成功，实际 HTTP ${recoveryResponse.status}: ${recoveryResponse.text}`)
  assert.match(recoveryResponse.text, /mock ai chat sk-speed-primary/, '速度降级恢复后同 session 应回到账户配置主号')
  const recoveryHits = upstreamHits.slice(recoveryHitStart)
  assert.equal(countHits(recoveryHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '恢复后同 session 应命中主号')
  assert.equal(countHits(recoveryHits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '恢复后不应被速度切号副号亲和粘住')

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
}

async function assertResponsesSlowFirstByteUsesObservationAndConfirmedCutover(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')
  const primaryAccount = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
    .find((account) => account.id === scenario.primaryAccountId)
  assert(primaryAccount, 'Responses 首字截止测试需要找到主账户 runtime 快照')
  const primaryCircuitScope = accountCircuit.gatewayAccountProtocolModelScope(primaryAccount, 'text', model)

  for (let attempt = 1; attempt <= speedFirstConfig.slowTriggerCount; attempt += 1) {
    const hitStart = upstreamHits.length
    const startedAt = Date.now()
    const response = await postResponses(baseUrl, scenario.apiKey, `responses slow sample ${attempt}`)
    assert.equal(response.status, 200, `Responses 第 ${attempt} 次慢首字请求应成功，实际 HTTP ${response.status}: ${response.text}`)
    assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, 'Responses 慢首字样本应真实等待接近首字阈值')
    const hits = upstreamHits.slice(hitStart)
    assert.equal(countHits(hits, 'sk-speed-primary', '/v1/responses'), 1, `Responses 第 ${attempt} 次请求应先命中主号`)
    if (attempt < speedFirstConfig.slowTriggerCount) {
      assert.match(response.text, /late mock ai responses/, 'Responses 未确认退化前应继续等待当前主号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/responses'), 0, 'Responses 未确认退化前不应切到副号')
    } else {
      assert.match(response.text, /mock ai responses sk-speed-secondary/, 'Responses 确认退化后应隐藏切到副号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/responses'), 1, 'Responses 确认退化后应切到副号')
    }
    assert.equal(
      (await accountCircuit.getGatewayAccountCircuitStore().get(primaryCircuitScope)).phase,
      'CLOSED',
      `Responses 第 ${attempt} 次优化性首字截止不得污染账户传输熔断`
    )
  }

  const bulkHitStart = upstreamHits.length
  const responses = await runBulkResponsesRequests(baseUrl, scenario.apiKey, 'responses bulk after degradation', 30)
  for (const [index, response] of responses.entries()) {
    assert.equal(response.status, 200, `Responses 降级后批量请求 ${index + 1} 应成功，实际 HTTP ${response.status}: ${response.text}`)
    assert.match(response.text, /mock ai responses sk-speed-secondary/, `Responses 降级后批量请求 ${index + 1} 应直接命中副号`)
  }
  const bulkHits = upstreamHits.slice(bulkHitStart)
  assert.equal(countHits(bulkHits, 'sk-speed-primary', '/v1/responses'), 0, 'Responses 降级后批量请求不应回打慢主号')
  assert.equal(countHits(bulkHits, 'sk-speed-secondary', '/v1/responses'), 30, 'Responses 降级后批量请求应全部命中副号')

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
}

async function assertSpeedFirstDoesNotCutoverToAlreadyDegradedCandidate(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '已降级候选阻断切号测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstRuntimeConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstRuntimeConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  assert.equal(
    await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: scenario.secondaryAccountId }, scope),
    true,
    '已降级候选阻断切号测试必须先确认副号状态已持久化'
  )
  assert.equal(
    await latencyDegradation.isNormalRouteAccountLatencyDegradedAsync({ id: scenario.primaryAccountId }, scope),
    false,
    '当前主号在本场景只有一次慢样本，不得继承前序场景的降级状态'
  )
  const runtimeAccounts = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
  const directOrder = await latencyDegradation.orderGatewayAccountsByNormalRouteLatencyDegradationAsync(
    runtimeAccounts,
    scope,
    speedFirstRuntimeConfig
  )
  assert.deepEqual(
    directOrder.accounts.map((account) => account.id),
    [scenario.primaryAccountId, scenario.secondaryAccountId],
    '请求前生产延迟排序必须把已降级副号稳定放在主号之后'
  )
  const suppression = await accountSideEffects.filterGatewayAccountRuntimeSuppressionsAsync(runtimeAccounts)
  assert(
    suppression.accounts.some((account) => account.id === scenario.primaryAccountId),
    `优化性首字截止不得把主号写入共享抑制；suppressed=${JSON.stringify(suppression.suppressedAccountIds)}`
  )

  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')
  const hitStart = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'degraded remaining candidate should not receive speed cutover', false)
  assert.equal(response.status, 200, `剩余候选已降级时应继续等待当前主号成功，实际 HTTP ${response.status}: ${response.text}`)
  const hits = upstreamHits.slice(hitStart)
  assert.match(
    response.text,
    /late mock ai body/,
    `剩余候选已降级时不应切到已降级副号，应返回当前主号慢响应；hits=${JSON.stringify(hits)}`
  )
  assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, '已降级候选阻断切号应等待首字阈值确认后继续当前响应')
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, '已降级候选阻断切号请求应先命中主号')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '已降级副号不应作为当前请求速度切换目标')
  const candidates = await listSpeedProbeCandidates(scenario)
  assert(candidates.some((candidate) => candidate.accountId === scenario.primaryAccountId), '当前主号确认慢后应进入恢复探针候选')
  assert(candidates.some((candidate) => candidate.accountId === scenario.secondaryAccountId), '已降级副号应保持恢复探针候选状态')

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
}

async function assertResponseHeaderDeadlineUsesReservedCutover(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '响应头前速度切换测试需要有效普通路由 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync(
    { id: scenario.primaryAccountId },
    scope,
    speedFirstRuntimeConfig
  )
  const primaryAccount = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
    .find((account) => account.id === scenario.primaryAccountId)
  assert(primaryAccount, '响应头前速度切换测试需要找到主账户 runtime 快照')
  const primaryCircuitScope = accountCircuit.gatewayAccountProtocolModelScope(primaryAccount, 'text', model)

  setAccountPhase('sk-speed-primary', 'slow_response_headers')
  setAccountPhase('sk-speed-secondary', 'fast')
  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'response header deadline must consume reserved target', false)
  assert.equal(response.status, 200, `响应头前首字截止应隐藏切到已预占副号，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-speed-secondary/, '响应头前首字截止应返回副号完整响应')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, '响应头前切换应先命中主号一次')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 1, '预占槽必须由外层消费，不能反向阻塞副号')
  assert.equal(
    (await accountCircuit.getGatewayAccountCircuitStore().get(primaryCircuitScope)).phase,
    'CLOSED',
    '响应头前配置型首字截止不得推进账户电路'
  )

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
}

async function assertLocalDecisionFailuresKeepCurrentUpstream(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '本地速度决策异常测试需要有效普通路由 scope')
  const primaryAccount = repositories.listOpenAIAccountsForGroup(scenario.groupId, access.systemAccountId, { requestedModel: model })
    .find((account) => account.id === scenario.primaryAccountId)
  assert(primaryAccount, '本地速度决策异常测试需要找到主账户 runtime 快照')
  const primaryCircuitScope = accountCircuit.gatewayAccountProtocolModelScope(primaryAccount, 'text', model)

  const failureCases = [
    {
      name: 'latency_state_read',
      install: (onInvoked: () => void) => setNormalRouteSpeedFirstDecisionOperationsForTest({
        isAccountLatencyDegradedAsync: async () => {
          onInvoked()
          throw new Error('回归注入：速度状态 Redis 读取失败')
        }
      }),
      preseedSlowSample: false
    },
    {
      name: 'slow_sample_write',
      install: (onInvoked: () => void) => setNormalRouteSpeedFirstDecisionOperationsForTest({
        recordFirstByteSlowAsync: async () => {
          onInvoked()
          throw new Error('回归注入：慢样本 Redis 写入失败')
        }
      }),
      preseedSlowSample: false
    },
    {
      name: 'cutover_slot_reservation',
      install: (onInvoked: () => void) => setNormalRouteSpeedFirstDecisionOperationsForTest({
        reserveCutoverTarget: async () => {
          onInvoked()
          throw new Error('回归注入：并发预占状态异常')
        }
      }),
      preseedSlowSample: true
    }
  ] as const

  for (const failureCase of failureCases) {
    await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    accountCircuit.resetGatewayAccountCircuitStoreForTest()
    if (failureCase.preseedSlowSample) {
      await latencyDegradation.recordNormalRouteFirstByteSlowAsync(
        { id: scenario.primaryAccountId },
        scope,
        speedFirstRuntimeConfig
      )
    }
    setAccountPhase('sk-speed-primary', 'slow_first_byte')
    setAccountPhase('sk-speed-secondary', 'fast')
    let invocationCount = 0
    failureCase.install(() => {
      invocationCount += 1
    })

    const hitStart = upstreamHits.length
    const startedAt = Date.now()
    let response: Awaited<ReturnType<typeof postChat>>
    try {
      response = await postChat(baseUrl, scenario.apiKey, `local speed decision failure ${failureCase.name}`, false)
    } finally {
      setNormalRouteSpeedFirstDecisionOperationsForTest(undefined)
    }
    const hits = upstreamHits.slice(hitStart)
    assert(invocationCount > 0, `${failureCase.name} 故障注入必须真实命中本地决策操作`)
    assert.equal(response.status, 200, `${failureCase.name} 不得中断健康上游，实际 HTTP ${response.status}: ${response.text}`)
    assert.match(response.text, /late mock ai body/, `${failureCase.name} 应继续返回当前慢但健康的主号响应`)
    assert(Date.now() - startedAt >= firstByteDeadlineMs - 2_500, `${failureCase.name} 应继续等待当前上游而非立即错误切换`)
    assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, `${failureCase.name} 应只命中当前主号一次`)
    assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 0, `${failureCase.name} 不得切到副号`)
    assert.equal(
      (await accountCircuit.getGatewayAccountCircuitStore().get(primaryCircuitScope)).phase,
      'CLOSED',
      `${failureCase.name} 属于本地优化异常，不得分类为上游 transport failure`
    )
  }

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync(
    { id: scenario.primaryAccountId },
    scope,
    speedFirstRuntimeConfig
  )
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
  let recoveryWriteInvocations = 0
  setNormalRouteSpeedFirstDecisionOperationsForTest({
    recordFirstByteSuccessAsync: async () => {
      recoveryWriteInvocations += 1
      throw new Error('回归注入：速度恢复样本 Redis 写入失败')
    }
  })
  const recoveryHitStart = upstreamHits.length
  let recoveryResponse: Awaited<ReturnType<typeof postChat>>
  try {
    recoveryResponse = await postChat(baseUrl, scenario.apiKey, 'local speed recovery observation failure', false)
  } finally {
    setNormalRouteSpeedFirstDecisionOperationsForTest(undefined)
  }
  const recoveryHits = upstreamHits.slice(recoveryHitStart)
  assert.equal(recoveryWriteInvocations, 1, '恢复样本故障注入必须在已完成响应后命中一次')
  assert.equal(recoveryResponse.status, 200, `恢复样本写入失败不得丢弃健康响应，实际 HTTP ${recoveryResponse.status}: ${recoveryResponse.text}`)
  assert.match(recoveryResponse.text, /mock ai chat sk-speed-primary/, '恢复样本写入失败仍应返回主号完整响应')
  assert.equal(countHits(recoveryHits, 'sk-speed-primary', '/v1/chat/completions'), 1, '恢复样本写入失败应只命中主号一次')
  assert.equal(countHits(recoveryHits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '恢复样本写入失败不得误切副号')
  assert.equal(
    (await accountCircuit.getGatewayAccountCircuitStore().get(primaryCircuitScope)).phase,
    'CLOSED',
    '恢复样本写入失败不得推进账户传输电路'
  )

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
}

async function assertAlreadyAttemptedCandidateIsNotReserved(
  baseUrl: string,
  scenario: StaleCutoverScenario
): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '请求内旧候选预占测试需要有效普通路由 scope')
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync(
    { id: scenario.slowAccountId },
    scope,
    speedFirstRuntimeConfig
  )
  setAccountPhase('sk-stale-a', 'transport_reset')
  setAccountPhase('sk-stale-b', 'slow_first_byte')
  setAccountPhase('sk-stale-c', 'fast')

  const reservationTargetIds: string[] = []
  cutoverReservations.setSpeedFirstCutoverSlotAcquirerForTest(async (accountId, concurrencyLimit, options) => {
    reservationTargetIds.push(accountId)
    return tryAcquireAccountConcurrencyAsync(accountId, concurrencyLimit, options)
  })
  const hitStart = upstreamHits.length
  let response: Awaited<ReturnType<typeof postChat>>
  try {
    response = await postChat(baseUrl, scenario.apiKey, 'already attempted account must not receive cutover reservation', false)
  } finally {
    cutoverReservations.setSpeedFirstCutoverSlotAcquirerForTest(undefined)
  }
  const hits = upstreamHits.slice(hitStart)
  assert.equal(response.status, 200, `A 传输失败、B 慢时应直接预占并切到 C，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-stale-c/, '请求内旧候选被排除后应返回 C 的完整响应')
  assert.equal(countHits(hits, 'sk-stale-a', '/v1/chat/completions'), 1, 'A 应只发生一次真实 transport 失败')
  assert.equal(countHits(hits, 'sk-stale-b', '/v1/chat/completions'), 1, 'B 应只等待一次首字截止')
  assert.equal(countHits(hits, 'sk-stale-c', '/v1/chat/completions'), 1, 'C 应只执行一次真实替补请求')
  assert.deepEqual(
    reservationTargetIds,
    [scenario.healthyAccountId],
    'reservation 前必须按 requestAttemptTracker 排除已尝试 A，只能为仍可派发的 C 预占'
  )

  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-stale-a', 'fast')
  setAccountPhase('sk-stale-b', 'fast')
  setAccountPhase('sk-stale-c', 'fast')
}

async function assertAllDegradedBypassKeepsOriginalOrder(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '全部降级旁路测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstRuntimeConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstRuntimeConfig)

  setAccountPhase('sk-speed-primary', 'fast')
  setAccountPhase('sk-speed-secondary', 'fast')
  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'all degraded should bypass reordering', false)
  assert.equal(response.status, 200, `全部候选降级时应保留原顺序兜底成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-speed-primary/, '全部候选降级旁路时应保留原主号顺序')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, '全部降级旁路应命中原主号')
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
}

async function assertStreamSlowFirstByteRetriesBeforeDownstreamOutput(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '流式首字慢切号测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstRuntimeConfig)
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')
  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'stream slow first byte should retry', true)
  assert.equal(response.status, 200, `流式首字慢请求应隐藏切到副号成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai stream sk-speed-secondary/, '流式首字慢隐藏重试后应返回副号 SSE 内容')
  assert.match(response.text, /data:\s*\[DONE\]/, '流式副号响应应正常收口为 [DONE]')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 1, '流式首字慢请求应先命中主号')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 1, '流式首字慢请求应在下游输出前切到副号')
}

function createSpeedFirstScenario(upstreamBaseUrl: string): SpeedFirstScenario {
  const group = repositories.createGroup({
    name: '普通路由速度优先 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-普通路由速度优先 Mock AI 主号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-speed-primary',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model
  }, access)
  activateFixtureAccount(primary.id)
  const secondary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-普通路由速度优先 Mock AI 副号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-speed-secondary',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model
  }, access)
  activateFixtureAccount(secondary.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由速度优先 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs,
      speedFirstConfig
    },
    status: 'active'
  }, access)
  assert(apiKey.key, '速度优先 Mock AI 网关 Key 未返回明文密钥')
  assert(apiKey.routeStrategyId, '速度优先 Mock AI 网关 Key 未绑定策略路由')
  const routeStrategy = repositories.findRouteStrategySummary(apiKey.routeStrategyId, access)
  assert.equal(routeStrategy?.normalRoutingConfig?.schedulingPreference, 'speed_first', '速度优先 Mock AI 策略应保存速度优先偏好')
  assert.equal(routeStrategy?.normalRoutingConfig?.speedFirstConfig?.slowTriggerCount, speedFirstConfig.slowTriggerCount, '速度优先 Mock AI 策略应保存慢速触发次数')
  return {
    apiKey: apiKey.key,
    routeStrategyId: apiKey.routeStrategyId,
    groupId: group.id,
    primaryAccountId: primary.id,
    primaryAccountName: primary.name,
    secondaryAccountId: secondary.id,
    secondaryAccountName: secondary.name
  }
}

function createStaleCutoverScenario(upstreamBaseUrl: string): StaleCutoverScenario {
  const group = repositories.createGroup({
    name: '普通路由速度优先旧候选 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const createAccount = (name: string, apiKey: MockAccountKey) => {
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name,
      type: 'api_key',
      credentials: {
        api_key: apiKey,
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [model],
      healthCheckModel: model
    }, access)
    activateFixtureAccount(account.id)
    return account
  }
  const first = createAccount('01-普通路由速度优先旧候选 A', 'sk-stale-a')
  const slow = createAccount('02-普通路由速度优先当前慢候选 B', 'sk-stale-b')
  const healthy = createAccount('03-普通路由速度优先健康替补 C', 'sk-stale-c')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由速度优先旧候选 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs,
      speedFirstConfig
    },
    status: 'active'
  }, access)
  assert(apiKey.key, '速度优先旧候选 Mock AI 网关 Key 未返回明文密钥')
  assert(apiKey.routeStrategyId, '速度优先旧候选 Mock AI 网关 Key 未绑定策略路由')
  return {
    apiKey: apiKey.key,
    routeStrategyId: apiKey.routeStrategyId,
    groupId: group.id,
    firstAccountId: first.id,
    slowAccountId: slow.id,
    healthyAccountId: healthy.id
  }
}

function createCostFirstScenario(upstreamBaseUrl: string): CostFirstScenario {
  const group = repositories.createGroup({
    name: '普通路由成本优先 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-普通路由成本优先 Mock AI 主号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cost-primary',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model
  }, access)
  activateFixtureAccount(primary.id)
  const secondary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-普通路由成本优先 Mock AI 副号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cost-secondary',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model
  }, access)
  activateFixtureAccount(secondary.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由成本优先 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '成本优先 Mock AI 网关 Key 未返回明文密钥')
  return { apiKey: apiKey.key }
}

function createPriorityTierScenario(upstreamBaseUrl: string): SpeedFirstScenario {
  const group = repositories.createGroup({
    name: '普通路由速度优先优先级层 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-普通路由速度优先唯一超级优先 Mock AI 主号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-super',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model,
    superPriorityEnabled: true,
    priority: 0
  }, access)
  activateFixtureAccount(primary.id)
  const secondary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-普通路由速度优先普通优先级 Mock AI 副号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-normal',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [model],
    healthCheckModel: model,
    priority: 1
  }, access)
  activateFixtureAccount(secondary.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由速度优先优先级层 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs,
      speedFirstConfig
    },
    status: 'active'
  }, access)
  assert(apiKey.key, '速度优先优先级层 Mock AI 网关 Key 未返回明文密钥')
  assert(apiKey.routeStrategyId, '速度优先优先级层 Mock AI 网关 Key 未绑定策略路由')
  return {
    apiKey: apiKey.key,
    routeStrategyId: apiKey.routeStrategyId,
    groupId: group.id,
    primaryAccountId: primary.id,
    primaryAccountName: primary.name,
    secondaryAccountId: secondary.id,
    secondaryAccountName: secondary.name
  }
}

interface ChatResponseResult {
  status: number
  text: string
  stream: boolean
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  content: string,
  stream: boolean,
  options: { sessionId?: string } = {}
): Promise<ChatResponseResult> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      accept: stream ? 'text/event-stream' : 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content }],
      stream,
      max_tokens: 16,
      ...(options.sessionId ? { session_id: options.sessionId } : {})
    })
  })
  return {
    status: response.status,
    text: await response.text(),
    stream
  }
}

async function postResponses(baseUrl: string, apiKey: string, content: string): Promise<ChatResponseResult> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model,
      input: [{ role: 'user', content: [{ type: 'input_text', text: content }] }],
      stream: true,
      max_output_tokens: 16
    })
  })
  return {
    status: response.status,
    text: await response.text(),
    stream: true
  }
}

async function runBulkChatRequests(
  baseUrl: string,
  apiKey: string,
  prefix: string,
  total: number,
  streamEvery: number
): Promise<ChatResponseResult[]> {
  const responses: ChatResponseResult[] = []
  const batchSize = 12
  for (let offset = 0; offset < total; offset += batchSize) {
    const count = Math.min(batchSize, total - offset)
    const batch = await Promise.all(Array.from({ length: count }, (_, index) => {
      const requestIndex = offset + index + 1
      return postChat(baseUrl, apiKey, `${prefix} ${requestIndex}`, requestIndex % streamEvery === 0)
    }))
    responses.push(...batch)
  }
  return responses
}

async function runBulkResponsesRequests(
  baseUrl: string,
  apiKey: string,
  prefix: string,
  total: number
): Promise<ChatResponseResult[]> {
  const responses: ChatResponseResult[] = []
  const batchSize = 10
  for (let offset = 0; offset < total; offset += batchSize) {
    const count = Math.min(batchSize, total - offset)
    const batch = await Promise.all(Array.from({ length: count }, (_, index) => {
      const requestIndex = offset + index + 1
      return postResponses(baseUrl, apiKey, `${prefix} ${requestIndex}`)
    }))
    responses.push(...batch)
  }
  return responses
}

function assertChatTransportShape(response: ChatResponseResult, label: string): void {
  if (response.stream) {
    assert.match(response.text, /data:\s*\{/, `${label} 的 stream 响应应包含 SSE data JSON 帧`)
    assert.match(response.text, /data:\s*\[DONE\]/, `${label} 的 stream 响应应包含 SSE DONE 帧`)
    return
  }
  assert.doesNotMatch(response.text, /data:\s*\{/, `${label} 的非流式响应不应返回 SSE 帧`)
  assert.doesNotThrow(() => JSON.parse(response.text), `${label} 的非流式响应应是 JSON`)
}

async function listSpeedProbeCandidates(scenario: SpeedFirstScenario) {
  return await latencyDegradation.listNormalRouteLatencyProbeCandidatesAsync(20, Date.now() + 20_000)
    .then((candidates) => candidates.filter((candidate) => candidate.scope.routeStrategyId === scenario.routeStrategyId))
}

async function requireSpeedProbeCandidate(scenario: SpeedFirstScenario) {
  const candidate = (await listSpeedProbeCandidates(scenario)).find((item) => item.accountId === scenario.primaryAccountId)
  assert(candidate, '速度优先主号应存在后台恢复探针候选')
  return candidate
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const path = req.url?.split('?', 1)[0] ?? ''
      const accountKey = bearerKey(req.headers.authorization)
      const phase = accountPhase(accountKey)
      const stream = requestStreamFlag(bodyText)
      const strictAccountTestOutput = strictAccountTestExpectedOutput(bodyText)
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        accountKey,
        path,
        bodyText,
        stream,
        phase
      })
      if (req.method !== 'POST' || (path !== '/v1/chat/completions' && path !== '/v1/responses')) {
        sendJsonError(res, 404, 'mock ai path not found')
        return
      }
      if (phase === 'transport_reset') {
        res.destroy(new Error('回归模拟上游传输连接重置'))
        return
      }
      if (phase === 'slow_first_byte') {
        sendSlowFirstByteResponse(res, path === '/v1/responses' ? 'responses' : stream ? 'chat_stream' : 'chat_json')
        return
      }
      if (phase === 'slow_response_headers') {
        sendSlowResponseHeaders(res, path === '/v1/responses' ? 'responses' : stream ? 'chat_stream' : 'chat_json')
        return
      }
      if (path === '/v1/responses') {
        sendResponsesCompleted(res, strictAccountTestOutput ?? `mock ai responses ${accountKey}`)
        return
      }
      if (stream) {
        sendChatCompletionSse(res, strictAccountTestOutput ?? `mock ai stream ${accountKey}`)
        return
      }
      sendChatCompletionJson(res, strictAccountTestOutput ?? `mock ai chat ${accountKey}`)
    })
  })
}

function sendSlowFirstByteResponse(res: http.ServerResponse, mode: 'chat_json' | 'chat_stream' | 'responses'): void {
  res.writeHead(200, { 'content-type': mode === 'chat_json' ? 'application/json; charset=utf-8' : 'text/event-stream; charset=utf-8' })
  res.flushHeaders()
  const timer = setTimeout(() => {
    if (res.destroyed || res.writableEnded) return
    try {
      if (mode === 'responses') {
        writeResponsesCompletedEvent(res, 'late mock ai responses')
      } else if (mode === 'chat_stream') {
        res.write(`data: ${JSON.stringify(chatSseChunk('late mock ai chunk'))}\n\n`)
        res.end('data: [DONE]\n\n')
      } else {
        res.end(JSON.stringify(chatJsonBody('late mock ai body')))
      }
    } catch {
    }
  }, slowBodyDelayMs)
  timer.unref()
}

function sendSlowResponseHeaders(res: http.ServerResponse, mode: 'chat_json' | 'chat_stream' | 'responses'): void {
  const timer = setTimeout(() => {
    if (res.destroyed || res.writableEnded) return
    if (mode === 'responses') {
      sendResponsesCompleted(res, 'late response headers')
    } else if (mode === 'chat_stream') {
      sendChatCompletionSse(res, 'late response headers')
    } else {
      sendChatCompletionJson(res, 'late response headers')
    }
  }, slowBodyDelayMs)
  timer.unref()
}

function sendChatCompletionJson(res: http.ServerResponse, content: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(chatJsonBody(content)))
}

function sendChatCompletionSse(res: http.ServerResponse, content: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`data: ${JSON.stringify(chatSseChunk(content))}\n\n`)
  res.end('data: [DONE]\n\n')
}

function sendResponsesCompleted(res: http.ServerResponse, outputText: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeResponsesCompletedEvent(res, outputText)
}

function writeResponsesCompletedEvent(res: http.ServerResponse, outputText: string): void {
  const messageId = 'msg_normal_route_speed_first_probe'
  const completedEvent = {
    type: 'response.completed',
    response: {
      id: 'resp_normal_route_speed_first_probe',
      object: 'response',
      status: 'completed',
      output: [
        {
          id: messageId,
          type: 'message',
          role: 'assistant',
          status: 'completed',
          content: [{ type: 'output_text', text: outputText }]
        }
      ],
      usage: {
        input_tokens: 1,
        output_tokens: 1,
        total_tokens: 2
      }
    }
  }
  res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function chatJsonBody(content: string): Record<string, unknown> {
  return {
    id: 'chatcmpl_normal_route_speed_first_mock_ai',
    object: 'chat.completion',
    model,
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 5,
      completion_tokens: 3,
      total_tokens: 8
    }
  }
}

function chatSseChunk(content: string): Record<string, unknown> {
  return {
    id: 'chatcmpl_normal_route_speed_first_mock_ai_stream',
    object: 'chat.completion.chunk',
    model,
    choices: [
      {
        index: 0,
        delta: { content },
        finish_reason: null
      }
    ]
  }
}

function activateFixtureAccount(accountId: string): void {
  assert(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), `Mock AI 测试账户 ${accountId} 应能通过后台健康检查激活`)
}

function countHits(hits: MockUpstreamHit[], accountKey: MockAccountKey, path: string): number {
  return hits.filter((hit) => hit.accountKey === accountKey && hit.path === path).length
}

function setAccountPhase(accountKey: MockAccountKey, phase: MockPhase): void {
  accountPhases.set(accountKey, phase)
}

function accountPhase(accountKey: string): MockPhase {
  return accountPhases.get(accountKey as MockAccountKey) ?? 'fast'
}

function bearerKey(value: unknown): string {
  const text = typeof value === 'string' ? value.trim() : ''
  return text.toLowerCase().startsWith('bearer ') ? text.slice(7).trim() : text
}

function requestStreamFlag(bodyText: string): boolean {
  try {
    const body = JSON.parse(bodyText) as { stream?: unknown }
    return body.stream === true
  } catch {
    return false
  }
}

function strictAccountTestExpectedOutput(bodyText: string): string | undefined {
  try {
    const body = JSON.parse(bodyText) as Record<string, unknown>
    const requestJson = JSON.stringify(body)
    const expectedOutput = requestJson.match(/你的回复必须且只能是：(juhe\d{3})/)?.[1]
    return expectedOutput && requestJson.includes(`除 ${expectedOutput} 外，不得输出任何字符`)
      ? expectedOutput
      : undefined
  } catch {
    return undefined
  }
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
    server.closeIdleConnections?.()
    server.closeAllConnections?.()
  })
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

async function removeTempRootWithRetry(path: string): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) {
        throw error
      }
      if (attempt === 7) return
      await delay(100 + attempt * 100)
    }
  }
}
