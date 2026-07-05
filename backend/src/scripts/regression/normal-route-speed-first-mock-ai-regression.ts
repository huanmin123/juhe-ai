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
import { logger } from '../../shared/logger.js'

type MockAccountKey =
  | 'sk-speed-primary'
  | 'sk-speed-secondary'
  | 'sk-cost-primary'
  | 'sk-cost-secondary'
  | 'sk-priority-super'
  | 'sk-priority-normal'

type MockPhase = 'slow_first_byte' | 'fast'

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

const model = 'gpt-5.5'
const slowBodyDelayMs = 12_000
const speedFirstConfig: RouteStrategySpeedFirstConfig = {
  firstByteThresholdMs: 10_000,
  slowTriggerCount: 2,
  slowWindowSeconds: 60,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 2
}

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
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  latencyDegradation,
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
  ['sk-priority-normal', 'fast']
])

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    streamRequestTimeoutSeconds: 30,
    streamClientTotalWaitTimeoutSeconds: 60
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

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertTransientSlowThenFastDoesNotDegrade(baseUrl, speedScenario)
    await assertNonStreamSlowFirstByteRetriesAndDegrades(baseUrl, speedScenario)
    await assertCostFirstRouteUnaffected(baseUrl, costScenario)
    await assertSpeedFirstDoesNotCrossPriorityTier(baseUrl, priorityScenario)
    await assertBackgroundProbeRestoresPrimary(baseUrl, speedScenario)
    await assertBulkFastTrafficAfterRecovery(baseUrl, speedScenario)
    await assertResponsesSlowFirstByteUsesObservationAndConfirmedCutover(baseUrl, speedScenario)
    await assertAllDegradedBypassKeepsOriginalOrder(baseUrl, speedScenario)
    await assertStreamSlowFirstByteRetriesBeforeDownstreamOutput(baseUrl, speedScenario)

    console.log('普通路由速度优先 Mock AI 回归通过：偶发慢后快样本清理、Chat/Responses 首字慢延迟切号、批量混合请求、降级后置、成本优先隔离、后台探针恢复、全部降级旁路和流式首字确认切号均生效')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
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
  assert(Date.now() - firstSlowStartedAt >= speedFirstConfig.firstByteThresholdMs - 2_500, '偶发慢请求应真实等待接近首字阈值')
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
    assert(Date.now() - startedAt >= speedFirstConfig.firstByteThresholdMs - 2_500, '慢首字样本应真实等待接近首字阈值')

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
  const latencyOrder = await latencyDegradation.orderGatewayAccountsByNormalRouteLatencyDegradationAsync(runtimeAccounts, candidate.scope, speedFirstConfig)
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

async function assertSpeedFirstDoesNotCrossPriorityTier(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '优先级层切号保护测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstConfig)
  setAccountPhase('sk-priority-super', 'slow_first_byte')
  setAccountPhase('sk-priority-normal', 'fast')
  const hitStart = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'speed first must not cross priority tier', false)
  assert.equal(response.status, 200, `唯一超级优先账号慢时请求仍应成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /late mock ai body/, '唯一超级优先账号慢时不应切到普通优先级账号')
  assert(Date.now() - startedAt >= speedFirstConfig.firstByteThresholdMs - 2_500, '唯一超级优先账号慢时应继续等待当前账号而不是立即跨层切号')
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-priority-super', '/v1/chat/completions'), 1, '唯一超级优先账号慢时应命中超级优先账号')
  assert.equal(countHits(hits, 'sk-priority-normal', '/v1/chat/completions'), 0, '速度优先不得跨到普通优先级账号')
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
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
        streamRequestTimeoutSeconds: 20,
        streamClientTotalWaitTimeoutSeconds: 20,
        streamMaxLifetimeSeconds: 60
      }
    })
    assert.equal(result.success, true, `第 ${index} 次恢复探针等价账号测试应成功：${result.message ?? result.errorCode ?? 'unknown error'}`)
    assert(
      result.firstTokenMs !== undefined && result.firstTokenMs <= speedFirstConfig.firstByteThresholdMs,
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

  const hitStart = upstreamHits.length
  const response = await postChat(baseUrl, scenario.apiKey, 'after background probe recovery', false)
  assert.equal(response.status, 200, `后台探针恢复后请求应成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai chat sk-speed-primary/, '后台探针恢复后应回到主号正常调度')
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
    assert.match(response.text, /sk-speed-primary/, `恢复后批量请求 ${index + 1} 应稳定命中主号`)
    assertChatTransportShape(response, `恢复后批量请求 ${index + 1}`)
  }
  const hits = upstreamHits.slice(hitStart)
  assert.equal(countHits(hits, 'sk-speed-primary', '/v1/chat/completions'), 120, '恢复后批量请求应全部命中主号')
  assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/chat/completions'), 0, '恢复后批量请求不应误切副号')
}

async function assertResponsesSlowFirstByteUsesObservationAndConfirmedCutover(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(scenario.routeStrategyId)
  setAccountPhase('sk-speed-primary', 'slow_first_byte')
  setAccountPhase('sk-speed-secondary', 'fast')

  for (let attempt = 1; attempt <= speedFirstConfig.slowTriggerCount; attempt += 1) {
    const hitStart = upstreamHits.length
    const startedAt = Date.now()
    const response = await postResponses(baseUrl, scenario.apiKey, `responses slow sample ${attempt}`)
    assert.equal(response.status, 200, `Responses 第 ${attempt} 次慢首字请求应成功，实际 HTTP ${response.status}: ${response.text}`)
    assert(Date.now() - startedAt >= speedFirstConfig.firstByteThresholdMs - 2_500, 'Responses 慢首字样本应真实等待接近首字阈值')
    const hits = upstreamHits.slice(hitStart)
    assert.equal(countHits(hits, 'sk-speed-primary', '/v1/responses'), 1, `Responses 第 ${attempt} 次请求应先命中主号`)
    if (attempt < speedFirstConfig.slowTriggerCount) {
      assert.match(response.text, /late mock ai responses/, 'Responses 未确认退化前应继续等待当前主号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/responses'), 0, 'Responses 未确认退化前不应切到副号')
    } else {
      assert.match(response.text, /mock ai responses sk-speed-secondary/, 'Responses 确认退化后应隐藏切到副号返回')
      assert.equal(countHits(hits, 'sk-speed-secondary', '/v1/responses'), 1, 'Responses 确认退化后应切到副号')
    }
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

async function assertAllDegradedBypassKeepsOriginalOrder(baseUrl: string, scenario: SpeedFirstScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '全部降级旁路测试需要有效普通路由速度优先 scope')
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstConfig)
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.secondaryAccountId }, scope, speedFirstConfig)

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
  await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.primaryAccountId }, scope, speedFirstConfig)
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
    supportedModels: [model]
  }, access)
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
    supportedModels: [model]
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由速度优先 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
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

function createCostFirstScenario(upstreamBaseUrl: string): CostFirstScenario {
  const group = repositories.createGroup({
    name: '普通路由成本优先 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
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
    supportedModels: [model]
  }, access)
  repositories.createAccount({
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
    supportedModels: [model]
  }, access)
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
    superPriorityEnabled: true,
    priority: 0
  }, access)
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
    priority: 1
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '普通路由速度优先优先级层 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
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

async function postChat(baseUrl: string, apiKey: string, content: string, stream: boolean): Promise<ChatResponseResult> {
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
      max_tokens: 16
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
      if (phase === 'slow_first_byte') {
        sendSlowFirstByteResponse(res, path === '/v1/responses' ? 'responses' : stream ? 'chat_stream' : 'chat_json')
        return
      }
      if (path === '/v1/responses') {
        sendResponsesCompleted(res, `mock ai responses ${accountKey}`)
        return
      }
      if (stream) {
        sendChatCompletionSse(res, `mock ai stream ${accountKey}`)
        return
      }
      sendChatCompletionJson(res, `mock ai chat ${accountKey}`)
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
  const completedEvent = {
    type: 'response.completed',
    response: {
      id: 'resp_normal_route_speed_first_probe',
      object: 'response',
      status: 'completed',
      output: [
        {
          type: 'message',
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
