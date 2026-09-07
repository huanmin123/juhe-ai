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
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import type { HotQualityScope, HotQualityTerminalOutcomeClass } from '../../modules/gateway/runtime/hot-quality-store.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const model = 'gpt-5.5'
const concurrentStormSize = 24
const speedConfig: RouteStrategySpeedFirstConfig = {
  slowTriggerCount: 2,
  slowWindowSeconds: 60,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 60,
  maxFirstByteRetriesPerRequest: 1
}
const speedRuntimeConfig = { ...speedConfig, firstByteDeadlineMs: 10_000 }
const tempRoot = resolve(tmpdir(), `juhe-ai-hot-quality-reselection-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-hot-quality-reselection-mock-secret'
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
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

type MockKey = 'sk-quality-a1' | 'sk-quality-a2' | 'sk-quality-b' | 'sk-speed-high' | 'sk-speed-low'

interface UpstreamHit {
  requestLabel: string
  accountKey: MockKey
  sessionId?: string
}

interface QualityScenario {
  apiKey: string
  groupId: string
  accountAId: string
  accountBId: string
}

interface SpeedScenario {
  apiKey: string
  routeStrategyId: string
  groupId: string
  highAccountId: string
  lowAccountId: string
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
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
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
  const qualityScenario = createQualityScenario(upstreamBaseUrl)
  const speedScenario = createSpeedScenario(upstreamBaseUrl)

  gatewayServer = http.createServer(app)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`

  await assertSameTierQualityReselection(gatewayBaseUrl, qualityScenario)
  await assertBadSessionAndOpaqueResponseStayNeutral(gatewayBaseUrl, qualityScenario)
  await assertConcurrentOpaqueStormCannotMisorderOrKillPool(gatewayBaseUrl, qualityScenario)
  await assertSpeedFirstCrossPriorityAndRecovery(gatewayBaseUrl, speedScenario)

  console.log(JSON.stringify({
    message: 'gateway hot quality reselection mock ai regression passed',
    concurrentOpaqueRequests: concurrentStormSize,
    upstreamHits: upstreamHits.length
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
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSameTierQualityReselection(baseUrl: string, scenario: QualityScenario): Promise<void> {
  const accountA = requireDispatchAccount(scenario.groupId, scenario.accountAId)
  const accountB = requireDispatchAccount(scenario.groupId, scenario.accountBId)
  const scopeA = qualityScope(accountA)
  const scopeB = qualityScope(accountB)

  await seedQuality(scopeA, 'completed_response', 10, 600)
  await seedQuality(scopeB, 'transport_failure', 10)
  const first = await postChat(baseUrl, scenario.apiKey, 'quality-initial-primary', 'quality-session-initial')
  assert.equal(first.status, 200, `初始质量请求应成功：${first.text}`)
  assert.match(first.text, /sk-quality-a[12]/, '同优先级下应选择可靠的多 Key 账户 A')
  const firstHits = hitsFor('quality-initial-primary')
  assert.equal(firstHits.length, 1, `初始质量请求只能派发一次：${JSON.stringify(firstHits)}`)
  assert(new Set(['sk-quality-a1', 'sk-quality-a2']).has(firstHits[0]!.accountKey), '账户 A 应使用其当前轮转 Key')

  await seedQuality(scopeA, 'transport_failure', 12)
  await seedQuality(scopeB, 'completed_response', 12, 90)
  const snapshotA = await requireQualitySnapshot(scopeA)
  const snapshotB = await requireQualitySnapshot(scopeB)
  assert(snapshotB.effectiveReliability > snapshotA.effectiveReliability, '动态样本后账户 B 的有效可靠性应高于 A')

  const next = await postChat(baseUrl, scenario.apiKey, 'quality-next-request-reselect', 'quality-session-next')
  assert.equal(next.status, 200, `动态质量变化后的下一请求应成功：${next.text}`)
  assert.match(next.text, /sk-quality-b/, '下一客户端请求必须按当前热质量重新选择账户 B')
  assert.deepEqual(hitsFor('quality-next-request-reselect').map(hit => hit.accountKey), ['sk-quality-b'])
}

async function assertBadSessionAndOpaqueResponseStayNeutral(baseUrl: string, scenario: QualityScenario): Promise<void> {
  const accountA = requireDispatchAccount(scenario.groupId, scenario.accountAId)
  const accountB = requireDispatchAccount(scenario.groupId, scenario.accountBId)
  const scopeA = qualityScope(accountA)
  const scopeB = qualityScope(accountB)

  await seedQuality(scopeA, 'completed_response', 20, 50)
  const beforeA = await requireQualitySnapshot(scopeA)
  const beforeB = await requireQualitySnapshot(scopeB)
  assert(beforeA.effectiveReliability > beforeB.effectiveReliability, '坏会话前应由账户 A 重新成为当前质量首选')

  const badSession = await postChat(baseUrl, scenario.apiKey, 'bad-session-account-a-opaque', 'damaged-session-fixed')
  assert.equal(badSession.status, 200, `坏会话命中 A 的完整 opaque HTTP 后应隐藏切换到 B：${badSession.text}`)
  assert.match(badSession.text, /sk-quality-b/, '坏会话不得把 A 的原始错误直接返回客户端')
  const badHits = hitsFor('bad-session-account-a-opaque')
  assert.equal(badHits.length, 2, `瞬态 503 在同账户重试预算为 0 时应从 A 当前 Key 切到 B：${JSON.stringify(badHits)}`)
  assert(new Set(['sk-quality-a1', 'sk-quality-a2']).has(badHits[0]!.accountKey), 'A 必须先尝试一个当前轮转 Key')
  assert.equal(badHits[1]!.accountKey, 'sk-quality-b')
  assert(badHits.every(hit => hit.sessionId === 'damaged-session-fixed'), '同一坏会话 ID 应贯穿全部请求内切号尝试')

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const afterA = await requireQualitySnapshot(scopeA)
  const afterB = await requireQualitySnapshot(scopeB)
  assert.equal(afterA.window5m.qualityAttempts, beforeA.window5m.qualityAttempts, 'A 的完整 opaque HTTP 不得进入共享质量分母')
  assert.equal(afterA.window5m.localTransportFailures, beforeA.window5m.localTransportFailures, 'A 的完整 opaque HTTP 不得伪装成 transport failure')
  assert.equal(afterA.window5m.upstreamResponseFailures, beforeA.window5m.upstreamResponseFailures + 1, 'A 当前 Key 的瞬态 opaque HTTP 只增加一份中性诊断计数')
  assert.equal(afterB.window5m.completedResponses, beforeB.window5m.completedResponses + 1, 'B 的真实成功应正常进入质量样本')
  await assertAccountRemainsSchedulable(scenario.accountAId)
  await assertCircuitClosed(accountA)
}

async function assertConcurrentOpaqueStormCannotMisorderOrKillPool(baseUrl: string, scenario: QualityScenario): Promise<void> {
  const accountA = requireDispatchAccount(scenario.groupId, scenario.accountAId)
  const accountB = requireDispatchAccount(scenario.groupId, scenario.accountBId)
  const scopeA = qualityScope(accountA)
  const scopeB = qualityScope(accountB)
  const beforeA = await requireQualitySnapshot(scopeA)
  const beforeB = await requireQualitySnapshot(scopeB)
  const preferredBefore = beforeA.effectiveReliability > beforeB.effectiveReliability ? 'a' : 'b'

  const responses = await Promise.all(Array.from({ length: concurrentStormSize }, (_, index) => {
    const requestLabel = `opaque-storm-${index + 1}`
    return postChat(baseUrl, scenario.apiKey, requestLabel, 'one-damaged-session-storm')
      .then(response => ({ requestLabel, response }))
  }))
  for (const { requestLabel, response } of responses) {
    assert.equal(response.status, 503, `${requestLabel} 全池只有完整 opaque HTTP 时应返回有界 503：${response.text}`)
    assert.doesNotMatch(response.text, /sk-quality-a1|sk-quality-a2|sk-quality-b/, `${requestLabel} 不得向客户端泄漏上游凭据`)
    const requestHits = hitsFor(requestLabel)
    assert.equal(requestHits.length, 2, `${requestLabel} 的瞬态 503 应从 A 当前 Key 切到 B：${JSON.stringify(requestHits)}`)
    assert(new Set(['sk-quality-a1', 'sk-quality-a2']).has(requestHits[0]!.accountKey), `${requestLabel} 必须先尝试 A 的当前 Key`)
    assert.equal(requestHits[1]!.accountKey, 'sk-quality-b', `${requestLabel} 必须随后切到 B`)
  }

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const afterA = await requireQualitySnapshot(scopeA)
  const afterB = await requireQualitySnapshot(scopeB)
  assert.equal(afterA.window5m.qualityAttempts, beforeA.window5m.qualityAttempts, '并发 opaque 风暴不得改变账户 A 质量分母')
  assert.equal(afterB.window5m.qualityAttempts, beforeB.window5m.qualityAttempts, '并发 opaque 风暴不得改变账户 B 质量分母')
  assert.equal(afterA.window5m.localTransportFailures, beforeA.window5m.localTransportFailures, '并发 opaque 风暴不得把账户 A 误判为传输死亡')
  assert.equal(afterB.window5m.localTransportFailures, beforeB.window5m.localTransportFailures, '并发 opaque 风暴不得把账户 B 误判为传输死亡')
  assert.equal(afterA.window5m.upstreamResponseFailures, beforeA.window5m.upstreamResponseFailures + concurrentStormSize, '账户 A 每请求一个当前 Key 只应增加中性诊断计数')
  assert.equal(afterB.window5m.upstreamResponseFailures, beforeB.window5m.upstreamResponseFailures + concurrentStormSize, '账户 B 每请求只应增加中性诊断计数')
  assert.equal(afterA.effectiveReliability > afterB.effectiveReliability ? 'a' : 'b', preferredBefore, '中性并发样本不得颠倒账户池质量顺序')

  await assertAccountRemainsSchedulable(scenario.accountAId)
  await assertAccountRemainsSchedulable(scenario.accountBId)
  await assertCircuitClosed(accountA)
  await assertCircuitClosed(accountB)
  assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountAId], undefined, '并发 opaque 风暴不得创建账户 A 共享抑制')
  assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountBId], undefined, '并发 opaque 风暴不得创建账户 B 共享抑制')

  const afterStorm = await postChat(baseUrl, scenario.apiKey, 'after-opaque-storm-reselect', 'fresh-session-after-storm')
  assert.equal(afterStorm.status, 200, `并发 opaque 风暴后的新请求应立即恢复服务：${afterStorm.text}`)
  const afterStormHits = hitsFor('after-opaque-storm-reselect')
  assert.equal(afterStormHits.length, 1, '并发风暴后下一请求应直接命中当前质量首选，不得继续切号')
  assert.equal(afterStormHits[0]!.accountKey, preferredBefore === 'a' ? 'sk-quality-a1' : 'sk-quality-b', '下一请求必须沿用风暴前未被污染的质量排序')
}

async function assertSpeedFirstCrossPriorityAndRecovery(baseUrl: string, scenario: SpeedScenario): Promise<void> {
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: scenario.routeStrategyId,
    groupId: scenario.groupId
  })
  assert(scope, '跨优先级速度测试必须生成有效 route scope')
  const highAccount = requireDispatchAccount(scenario.groupId, scenario.highAccountId)

  const initial = await postChat(baseUrl, scenario.apiKey, 'speed-initial-high-priority', 'speed-session')
  assert.equal(initial.status, 200, `速度池初始请求应成功：${initial.text}`)
  assert.deepEqual(hitsFor('speed-initial-high-priority').map(hit => hit.accountKey), ['sk-speed-high'], '未降级时高优先级账户必须胜出')

  for (let index = 0; index < speedConfig.slowTriggerCount; index += 1) {
    await latencyDegradation.recordNormalRouteFirstByteSlowAsync({ id: scenario.highAccountId }, scope, speedRuntimeConfig)
  }
  const crossed = await postChat(baseUrl, scenario.apiKey, 'speed-next-cross-priority', 'speed-session')
  assert.equal(crossed.status, 200, `高优先级账户降级后的下一请求应成功：${crossed.text}`)
  assert.deepEqual(hitsFor('speed-next-cross-priority').map(hit => hit.accountKey), ['sk-speed-low'], 'speed_first 应基于当前降级状态跨优先级直接选择低层健康账户')

  for (let index = 1; index <= speedConfig.recoverySuccessCount; index += 1) {
    const recovery = await latencyDegradation.recordNormalRouteFirstByteSuccessAsync(highAccount, scope, speedRuntimeConfig, 80)
    assert.equal(recovery?.cleared, index === speedConfig.recoverySuccessCount, `第 ${index} 个恢复样本清理结果不符合阈值`)
  }
  const recovered = await postChat(baseUrl, scenario.apiKey, 'speed-recovered-high-priority', 'speed-session')
  assert.equal(recovered.status, 200, `高优先级恢复后的下一请求应成功：${recovered.text}`)
  assert.deepEqual(hitsFor('speed-recovered-high-priority').map(hit => hit.accountKey), ['sk-speed-high'], '恢复后相同会话也必须重新按当前 speed/priority 选择高优先级账户')
}

async function seedQuality(
  scope: HotQualityScope,
  outcomeClass: HotQualityTerminalOutcomeClass,
  count: number,
  firstByteMs?: number
): Promise<void> {
  const store = hotQuality.getGatewayHotQualityRuntime().hotQualityStore
  for (let index = 0; index < count; index += 1) {
    const attemptId = `seed-${scope.accountRuntimeKey}-${outcomeClass}-${Date.now()}-${index}-${Math.random().toString(16).slice(2)}`
    const attempt = await store.recordAttempt({ attemptId, scope })
    assert(attempt.status === 'applied' || attempt.status === 'degraded_to_protocol', `热质量 attempt 写入失败：${attempt.status}`)
    const terminal = await store.recordTerminal({
      attemptId,
      scope: attempt.effectiveScope,
      terminalOutcomeId: `${attemptId}:terminal`,
      outcomeClass,
      failureScope: outcomeClass === 'completed_response' ? 'none' : 'protocol_model',
      source: outcomeClass === 'completed_response' ? 'request_lifecycle' : 'gateway_transport',
      firstByteMs
    })
    assert.equal(terminal.status, 'applied', `热质量 terminal 写入失败：${terminal.status}`)
  }
}

function createQualityScenario(upstreamBaseUrl: string): QualityScenario {
  const group = repositories.createGroup({ name: '热质量动态重选 Mock 分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const accountA = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-同层多Key账户A',
    type: 'api_key',
    credentials: {
      api_keys: ['sk-quality-a1', 'sk-quality-a2'],
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: [model],
    healthCheckModel: model
  })
  const accountB = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-同层单Key账户B',
    type: 'api_key',
    credentials: {
      api_key: 'sk-quality-b',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: [model],
    healthCheckModel: model
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '热质量动态重选网关Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: { schedulingPreference: 'cost_first' },
    status: 'active'
  }, access)
  assert(apiKey.key, '热质量动态重选场景应返回网关 Key')
  return { apiKey: apiKey.key, groupId: group.id, accountAId: accountA.id, accountBId: accountB.id }
}

function createSpeedScenario(upstreamBaseUrl: string): SpeedScenario {
  const group = repositories.createGroup({ name: '速度跨优先级动态重选 Mock 分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const high = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-速度高优先级账户',
    type: 'api_key',
    credentials: { api_key: 'sk-speed-high', base_url: upstreamBaseUrl, supported_endpoint_modes: ['responses_sse', 'chat_json'] },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    superPriorityEnabled: true,
    priority: 0,
    supportedModels: [model],
    healthCheckModel: model
  })
  const low = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-速度低优先级账户',
    type: 'api_key',
    credentials: { api_key: 'sk-speed-low', base_url: upstreamBaseUrl, supported_endpoint_modes: ['responses_sse', 'chat_json'] },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10,
    supportedModels: [model],
    healthCheckModel: model
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '速度跨优先级动态重选网关Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    normalRoutingConfig: { schedulingPreference: 'speed_first', firstByteDeadlineMs: 10_000, speedFirstConfig: speedConfig },
    status: 'active'
  }, access)
  assert(apiKey.key, '速度动态重选场景应返回网关 Key')
  assert(apiKey.routeStrategyId, '速度动态重选场景应绑定路由策略')
  return { apiKey: apiKey.key, routeStrategyId: apiKey.routeStrategyId, groupId: group.id, highAccountId: high.id, lowAccountId: low.id }
}

function createActiveAccount(input: Parameters<typeof repositories.createAccount>[0]) {
  const account = repositories.createAccount(input, access)
  assert.equal(repositories.projectAccountHealthFixtureSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `激活 Mock 账户失败：${account.id}`)
  return account
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const accountKey = bearerKey(req.headers.authorization) as MockKey
      const { requestLabel, sessionId } = requestMetadata(bodyText)
      upstreamHits.push({ requestLabel, accountKey, sessionId })
      if (requestLabel.startsWith('opaque-storm-') || (requestLabel === 'bad-session-account-a-opaque' && accountKey.startsWith('sk-quality-a'))) {
        res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { code: 'upstream_session_rejected', message: 'opaque mock session failure' } }))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: `chatcmpl_${requestLabel}`,
        object: 'chat.completion',
        model,
        choices: [{ index: 0, message: { role: 'assistant', content: `mock success ${accountKey}` }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
  })
}

async function postChat(baseUrl: string, apiKey: string, requestLabel: string, sessionId: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey}`, 'content-type': 'application/json', 'x-session-id': sessionId },
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

function requestMetadata(bodyText: string): { requestLabel: string; sessionId?: string } {
  try {
    const body = JSON.parse(bodyText) as { messages?: Array<{ content?: unknown }>; session_id?: unknown }
    return {
      requestLabel: typeof body.messages?.[0]?.content === 'string' ? body.messages[0].content : 'unknown',
      sessionId: typeof body.session_id === 'string' ? body.session_id : undefined
    }
  } catch {
    return { requestLabel: 'unparseable' }
  }
}

function requireDispatchAccount(groupId: string, accountId: string) {
  const account = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId, { requestedModel: model })
    .find(candidate => candidate.id === accountId)
  assert(account, `找不到调度账户 ${accountId}`)
  return account
}

function qualityScope(account: ReturnType<typeof requireDispatchAccount>): HotQualityScope {
  return {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    requestLane: 'text',
    modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
  }
}

async function requireQualitySnapshot(scope: HotQualityScope) {
  const snapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(scope)
  assert(snapshot, `缺少热质量快照 ${scope.accountRuntimeKey}`)
  return snapshot
}

async function assertAccountRemainsSchedulable(accountId: string): Promise<void> {
  const summary = repositories.findAccountForTest(accountId, access)
  assert.equal(summary?.status, 'active', `账户 ${accountId} 不得被完整 opaque HTTP 写死`)
  assert.equal(summary?.schedulable, true, `账户 ${accountId} 不得被完整 opaque HTTP 取消调度`)
  assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `账户 ${accountId} 的 Key 不得被内部状态码规则禁用`)
  assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, `账户 ${accountId} 的 Key 池不得被并发坏会话打死`)
}

async function assertCircuitClosed(account: ReturnType<typeof requireDispatchAccount>): Promise<void> {
  const state = await accountCircuit.getGatewayAccountCircuitStore().get(
    accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model)
  )
  assert.equal(state.phase, 'CLOSED', `完整 opaque HTTP 不得打开账户 circuit：${account.id}`)
}

function hitsFor(requestLabel: string): UpstreamHit[] {
  return upstreamHits.filter(hit => hit.requestLabel === requestLabel)
}

function bearerKey(value: unknown): string {
  const text = typeof value === 'string' ? value.trim() : ''
  return text.replace(/^Bearer\s+/i, '')
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
