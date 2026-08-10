import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-client-ip-account-avoidance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'client-ip-account-avoidance.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'client-ip-account-avoidance-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  clientIpAvoidance,
  clientIpErrorCircuit,
  proxyHealth,
  gatewayHotQuality,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('./f3-audit-direct-input-test-support.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/runtime/client-ip-error-circuit.service.js'),
  import('../../modules/gateway/runtime/proxy-health.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  firstAccountId: string
  secondAccountId: string
  firstUpstreamKey: string
  secondUpstreamKey: string
}

interface MockRequestRecord {
  accountKey: string
  scenario: string
}

interface MockUpstreamState {
  requests: MockRequestRecord[]
}

async function main(): Promise<void> {
  let gatewayServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamState: MockUpstreamState = { requests: [] }

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    assertSourceAvoidsPendingFailureArrayRebuilds()
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      defaultTemporaryUnschedulableMinutes: 5
    })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const seeded = seedTwoAccountGateway(upstreamBaseUrl)

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertOpaqueHttpFailureRemainsRequestScoped(baseUrl, seeded, upstreamState)
    await assertOpaqueHttpFinalFailureDoesNotPersistAvoidance(baseUrl, seeded, upstreamState)
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    proxyHealth.clearGatewayProxyHealthForTest()
    gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()
    await assertStreamFailureRemainsSessionScopedAcrossClientRetries(baseUrl, seeded, upstreamState)
    await assertGenericSuccessInterruptsClientIpErrorCircuit(baseUrl, seeded)
    assertServiceBypassesWhenAllCandidatesAvoided()
    assertServiceSharesAvoidanceAcrossGroupsForSameApiKey()
    assertServicePreservesDispatchPriorityBoundary()
    assertServicePreservesModelPriorityBoundary()
    assertServiceConfirmsFinalFailuresWithoutSuccess()
    assertPendingFailureTrackerIsBoundedAndTransferSafe()

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertAccountsStillActive(seeded)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '测试清理后不应残留进程级本地账号屏蔽')

    console.log('IP 级账号回避回归通过：opaque HTTP 与单会话流式断尾均不写共享回避，请求内切号和独立服务边界保持可测')
  } finally {
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
    proxyHealth.clearGatewayProxyHealthForTest()
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool()
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  }
}

async function assertGenericSuccessInterruptsClientIpErrorCircuit(
  baseUrl: string,
  seeded: SeededGateway
): Promise<void> {
  const clientIp = '198.51.100.8'
  clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()

  for (let index = 0; index < 4; index += 1) {
    const invalidResponse = await requestInvalidJson(baseUrl, seeded.apiKey, clientIp)
    assert.equal(invalidResponse.status, 400, `generic 第 ${index + 1} 次无效 JSON 应返回 400`)
    const failureEntry = clientIpErrorCircuit.getGatewayClientIpSecuritySnapshotForTest().clientIpErrors
      .find((entry) => entry.key.includes(clientIp))
    assert.equal(failureEntry?.failureCount, 1, `generic 第 ${index + 1} 次错误应从新窗口的第 1 个样本开始`)
    assert.equal(failureEntry?.blocked, false, `generic 第 ${index + 1} 次错误不得 OPEN`)

    const successText = await requestChatCompletion(baseUrl, seeded.apiKey, clientIp, `generic-success-${index + 1}`)
    assert.match(successText, /ok from (?:first|second)/, `generic 第 ${index + 1} 次完整成功响应应正常返回：${successText}`)
    assert.equal(
      clientIpErrorCircuit.getGatewayClientIpSecuritySnapshotForTest().clientIpErrors.some((entry) => entry.key.includes(clientIp)),
      false,
      `generic 第 ${index + 1} 次完整成功应清除当前 IP 的本地错误样本`
    )
  }

  const fifthInvalidResponse = await requestInvalidJson(baseUrl, seeded.apiKey, clientIp)
  assert.equal(fifthInvalidResponse.status, 400, '4 次错误各夹一次 generic 完整成功后，第 5 次错误仍应按普通 400 处理')
  const fifthFailureEntry = clientIpErrorCircuit.getGatewayClientIpSecuritySnapshotForTest().clientIpErrors
    .find((entry) => entry.key.includes(clientIp))
  assert.equal(fifthFailureEntry?.failureCount, 1, '第 5 次错误应是新窗口的第 1 个样本')
  assert.equal(fifthFailureEntry?.blocked, false, '第 5 次错误不得因已被 generic 成功打断的旧样本而 OPEN')
  clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
}

function assertSourceAvoidsPendingFailureArrayRebuilds(): void {
  const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
  assert(!routesSource.includes('[...currentPreflight.clientIpAccountAvoidanceTracker.pendingFailures]'), 'fallback 切组不能复制待确认账号失败数组')
  assert(!routesSource.includes('pendingFailures.unshift(...'), 'fallback 切组不能通过 unshift 搬移待确认账号失败数组')
  assert(routesSource.includes('transferClientIpAccountPendingFailures('), 'fallback 切组应使用有界转移函数传递待确认账号失败')
  assert(routesSource.includes('await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure('), '路由最终失败响应应等待确认 pending 的 IP 级账号回避')
  const failureDispatchSource = readFileSync(new URL('../../modules/gateway/response/failure-dispatch.ts', import.meta.url), 'utf8')
  assert(!failureDispatchSource.includes('handleOpaqueFailedUpstreamResponse'), '未知 HTTP 失败不得保留独立的切号接管处理器')
  assert(/const gatewayFailoverEnabled = usageContext\.trafficSource === 'gateway'[\s\S]*if \(!gatewayFailoverEnabled\)[\s\S]*return \{ action: 'return_response', response \}/.test(failureDispatchSource), '只有非客户网关流和诊断流可以保留真实上游响应')
  assert(failureDispatchSource.includes("failureKind: explicitPolicyDecision ? 'explicit_policy' : 'opaque_http'"), '完整 HTTP 失败必须统一进入账户级 skip_account 切号')
  assert(!failureDispatchSource.includes('rememberClientIpAccountPendingFailure('), '未知 HTTP 非 2xx 不得记录任何可被路由收口确认的跨请求 pending 回避')
  const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  assert(!finalizationSource.includes('rememberClientIpAccountPendingFailure('), '流式断尾可能由单会话触发，不得写 IP×账号 pending 回避')
  assert(!finalizationSource.includes('confirmClientIpAccountAvoidanceAfterFinalFailureAsync('), '流式失败返回客户端时不得确认跨请求 IP×账号回避')
  assert(!finalizationSource.includes('confirmClientIpAccountAvoidanceAfterSuccessAsync('), '普通成功请求不得顺带改写无 provenance 的 IP×账号状态')

  const serviceSource = readFileSync(new URL('../../modules/gateway/runtime/client-ip-account-avoidance.service.ts', import.meta.url), 'utf8')
  assert(serviceSource.includes('pendingFailureIndexByAccountId'), '待确认账号失败应维护按账号去重索引')
  assert(serviceSource.includes('clientIpAccountAvoidanceMaxPendingFailures = 256'), '待确认账号失败应有固定上限')
  assert(serviceSource.includes('clientIpAccountAvoidanceActivationFailureThreshold = 2'), 'IP 级账号回避应先给失败账号一次重试机会，第三次请求才换号')
  assert(serviceSource.includes('confirmClientIpAccountAvoidanceAfterFinalFailure'), 'IP 级账号回避服务应支持最终失败时确认待回避账号')
  assert(serviceSource.includes("createRuntimeStateStore('gateway-client-ip-account-avoidance')"), 'Redis runtime state 下 IP 级账号回避应写共享运行态')
  assert(!/withRedisClientIpAccountAvoidanceLock|clientIpAccountAvoidanceLock|acquireLock|releaseLock|运行态锁等待超时/.test(serviceSource), 'Redis IP 级账号回避确认不能在请求路径引入分布式锁等待')
  assert(/confirmTrackerPendingFailuresAsync[\s\S]*getRedisClientIpAccountAvoidanceEntry[\s\S]*setRedisClientIpAccountAvoidanceEntry/.test(serviceSource), 'Redis IP 级账号回避确认应直接读写共享状态')
}

async function assertOpaqueHttpFailureRemainsRequestScoped(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const ipA = '198.51.100.10'
  const ipB = '198.51.100.20'

  const firstText = await requestChatCompletion(baseUrl, seeded.apiKey, ipA, 'ip-a-prime')
  assert.match(firstText, /ok from second/, `IP A 首次请求应通过切号由第二账号救成功：${firstText}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-prime'), 1, 'IP A 首次请求应先命中第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-prime'), 1, 'IP A 首次请求应切到第二账号成功')

  const snapshotAfterPrime = clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest()
  assert.equal(snapshotAfterPrime.length, 0, 'opaque HTTP 非 2xx 不应在切号成功后写入跨请求 IP 级账号回避')
  assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, 'opaque HTTP 非 2xx 不应写入进程级账号屏障')
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

  const followupText = await requestChatCompletion(baseUrl, seeded.apiKey, ipA, 'ip-a-followup')
  assert.match(followupText, /ok from second/, `IP A 第二次请求应按当前路由状态选到可用账号：${followupText}`)
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-followup'), 1, 'IP A 第二次请求应命中可用的第二账号')
  assert(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-followup') <= 1, '同一请求内不应重复尝试已返回 opaque HTTP 失败的第一账号')
  assert.equal(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest().length, 0, '连续 opaque HTTP 失败不应累积为跨请求回避阈值')
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

  const thirdText = await requestChatCompletion(baseUrl, seeded.apiKey, ipA, 'ip-a-third')
  assert.match(thirdText, /ok from second/, `IP A 第三次请求应继续按当前路由状态选到可用账号：${thirdText}`)
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-third'), 1, 'IP A 第三次请求应命中可用的第二账号')
  assert(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-third') <= 1, '同一请求内应保持已失败账号去重')
  assert.equal(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest().length, 0, '第三次独立请求后仍不得出现 opaque HTTP 共享回避状态')

  const ipBText = await requestChatCompletion(baseUrl, seeded.apiKey, ipB, 'ip-b-control')
  assert.match(ipBText, /ok from (?:first|second)/, `IP B 控制请求应正常完成：${ipBText}`)
  assert.equal(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest().length, 0, '不同 IP 的请求后也不得出现 opaque HTTP 共享回避状态')
}

async function assertOpaqueHttpFinalFailureDoesNotPersistAvoidance(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const clientIp = '198.51.100.40'
  const scenario = 'opaque-all-fail'
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${seeded.apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: scenario }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 503, `所有候选都返回 opaque HTTP 非 2xx 时应返回稳定的 503：${text}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, scenario), 1, '最终失败前应在当前请求内尝试第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, scenario), 1, '最终失败前应在当前请求内尝试第二账号')
  assert.equal(
    clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest().filter((entry) => entry.clientIp === clientIp).length,
    0,
    '路由最终失败收口不得把 opaque HTTP 非 2xx 确认为跨请求 IP 级账号回避'
  )
}

async function assertStreamFailureRemainsSessionScopedAcrossClientRetries(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const clientIp = '198.51.100.30'

  const failureResult = await requestCodexResponsesStream(baseUrl, seeded.apiKey, clientIp, 'ip-a-stream-final-failure', 'turn-final-failure')
  assert(
    failureResult.connectionTerminated || failureResult.text.includes('response.failed'),
    `已输出的流式断尾应返回失败事件或中断连接：${failureResult.text}`
  )
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-stream-final-failure'), 1, '首次流式断尾应命中第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-stream-final-failure'), 0, '首次流式断尾不应在已输出后服务端拼接第二账号')

  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '首次流式断尾不得写跨请求 IP×账号状态')

  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()

  const secondChanceResult = await requestCodexResponsesStream(baseUrl, seeded.apiKey, clientIp, 'ip-a-stream-second-chance', 'turn-final-failure-second-chance')
  assert(
    secondChanceResult.connectionTerminated || secondChanceResult.text.includes('response.failed'),
    `同 IP 第二次流式断尾应返回失败事件或中断连接：${secondChanceResult.text}`
  )
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-stream-second-chance'), 1, '同 IP 第二次请求应仍命中第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-stream-second-chance'), 0, '同 IP 第二次请求不应提前切到第二账号')
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '同 IP 第二次流式断尾仍不得把账号达到共享回避阈值')

  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  gatewayHotQuality.resetGatewayHotQualityRuntimeForTest()

  const thirdResult = await requestCodexResponsesStream(baseUrl, seeded.apiKey, clientIp, 'ip-a-stream-third-attempt', 'turn-final-failure-third-attempt')
  assert.equal(thirdResult.connectionTerminated, false, '同 IP 新 turn 应正常完成')
  assert(thirdResult.text.includes('response.completed'), `同 IP 新 turn 应重新按当前路由选号：${thirdResult.text}`)
  assert(thirdResult.text.includes('ok from first'), `无共享回避时新 turn 应可重新选到正常的第一账号：${thirdResult.text}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-stream-third-attempt'), 1, '新 turn 应重新考虑第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-stream-third-attempt'), 0, '无共享失败证据时不应强制跳到第二账号')
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '多次单会话失败后 IP×账号共享状态必须仍为空')
}

function assertServiceBypassesWhenAllCandidatesAvoided(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scope = {
    systemAccountId: 'sys_bypass',
    groupId: 'grp_bypass',
    apiKeyId: 'key_bypass',
    clientIp: '203.0.113.9'
  }
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const accounts = [
    createTestAccount('avoid-a'),
    createTestAccount('avoid-b')
  ]
  for (const account of accounts) {
    rememberPendingFailureTwice(tracker, account, {
      errorPhase: 'upstream_request',
      errorMessage: '测试回避旁路'
    })
  }
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, 'external-success')
  const ordered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope)
  assert.equal(ordered.applied, false, '全部候选都被回避时不应应用过滤排序')
  assert.equal(ordered.bypassedAllAvoided, true, '全部候选都被回避时应标记旁路')
  assert.deepEqual(ordered.accounts.map((account) => account.id), accounts.map((account) => account.id), '全部回避旁路时应保留原候选顺序')
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertServiceSharesAvoidanceAcrossGroupsForSameApiKey(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scopeGroupA = {
    systemAccountId: 'sys_cross_group',
    groupId: 'grp_a',
    apiKeyId: 'key_cross_group',
    clientIp: '203.0.113.19'
  }
  const scopeGroupB = {
    ...scopeGroupA,
    groupId: 'grp_b'
  }
  const scopeOtherApiKey = {
    ...scopeGroupA,
    apiKeyId: 'key_other'
  }
  const accounts = [
    createTestAccount('avoid-cross-group-a'),
    createTestAccount('avoid-cross-group-b')
  ]
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scopeGroupA)
  rememberPendingFailureTwice(tracker, accounts[0], {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'mock_cross_group'
  })
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, accounts[1].id)

  const sameApiKeyDifferentGroupOrder = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scopeGroupB)
  assert.equal(sameApiKeyDifferentGroupOrder.applied, true, '同一 API Key 下不同分组应共享来源账号回避状态')
  assert.deepEqual(sameApiKeyDifferentGroupOrder.accounts.map((account) => account.id), [accounts[1].id, accounts[0].id])

  const otherApiKeyOrder = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scopeOtherApiKey)
  assert.equal(otherApiKeyOrder.applied, false, '不同 API Key 不应共享来源账号回避状态')
  assert.deepEqual(otherApiKeyOrder.accounts.map((account) => account.id), accounts.map((account) => account.id))
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertServicePreservesDispatchPriorityBoundary(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scope = {
    systemAccountId: 'sys_priority_boundary',
    groupId: 'grp_priority_boundary',
    apiKeyId: 'key_priority_boundary',
    clientIp: '203.0.113.59'
  }
  const accounts = [
    createTestAccount('priority-primary', { priority: 0 }),
    createTestAccount('priority-backup', { priority: 10 })
  ]
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  rememberPendingFailureTwice(tracker, accounts[0], {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'priority_boundary'
  })
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, accounts[1].id)
  const ordered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope)
  assert.equal(ordered.applied, true, '命中回避状态时仍应报告排序规则已参与')
  assert.deepEqual(
    ordered.accounts.map((account) => account.id),
    [accounts[0].id, accounts[1].id],
    'IP 级回避不能让低优先级账号越过高优先级账号'
  )
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertServicePreservesModelPriorityBoundary(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scope = {
    systemAccountId: 'sys_model_priority_boundary',
    groupId: 'grp_model_priority_boundary',
    apiKeyId: 'key_model_priority_boundary',
    clientIp: '203.0.113.60'
  }
  const accounts = [
    createTestAccount('model-direct-avoided', { priority: 0 }),
    createTestAccount('model-unrestricted-fresh', { priority: 0 })
  ]
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  rememberPendingFailureTwice(tracker, accounts[0], {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'model_priority_boundary'
  })
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, accounts[1].id)
  const ordered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope, {
    requestedModel: 'gpt-4.1',
    rankByAccountId: new Map([
      [accounts[0].id, 0],
      [accounts[1].id, 2]
    ])
  })
  assert.equal(ordered.applied, true, '命中回避状态时仍应报告排序规则已参与')
  assert.deepEqual(
    ordered.accounts.map((account) => account.id),
    [accounts[0].id, accounts[1].id],
    'IP 级回避不能让低模型匹配等级账号越过直连匹配账号'
  )
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertServiceConfirmsFinalFailuresWithoutSuccess(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scope = {
    systemAccountId: 'sys_final_failure',
    groupId: 'grp_final_failure',
    apiKeyId: 'key_final_failure',
    clientIp: '203.0.113.39'
  }
  const accounts = [
    createTestAccount('final-failure-a'),
    createTestAccount('final-failure-b')
  ]
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, accounts[0], {
    errorPhase: 'stream',
    statusCode: 200,
    errorCode: 'upstream_stream_interrupted',
    errorMessage: '上游流在 OpenAI 终止事件前结束'
  })
  const confirmed = clientIpAvoidance.confirmClientIpAccountAvoidanceAfterFinalFailure(tracker)
  assert.deepEqual(confirmed.confirmedAccountIds, [accounts[0].id], '最终失败返回客户端时应立即确认当前失败账号')
  assert.equal(tracker.pendingFailures.length, 0, '最终失败确认后应清空 pending failures')
  const firstOrdered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope)
  assert.equal(firstOrdered.applied, false, '最终失败第一次确认后不应立刻回避，应给同账号一次机会')

  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, accounts[0], {
    errorPhase: 'stream',
    statusCode: 200,
    errorCode: 'upstream_stream_interrupted',
    errorMessage: '上游流在 OpenAI 终止事件前结束'
  })
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterFinalFailure(tracker)
  const ordered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope)
  assert.equal(ordered.applied, true, '最终失败第二次确认后第三次同 IP 请求应应用账号回避')
  assert.deepEqual(ordered.accounts.map((account) => account.id), [accounts[1].id, accounts[0].id], '达到阈值的最终失败账号应排到可用备选之后')
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertPendingFailureTrackerIsBoundedAndTransferSafe(): void {
  const scope = {
    systemAccountId: 'sys_pending_boundary',
    groupId: 'grp_pending_boundary',
    apiKeyId: 'key_pending_boundary',
    clientIp: '203.0.113.29'
  }
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const repeated = createTestAccount('repeat-pending')
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, repeated, {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'stale'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, repeated, {
    errorPhase: 'upstream_response',
    statusCode: 503,
    errorCode: 'latest'
  })
  assert.equal(tracker.pendingFailures.length, 1, '同一请求内同账号失败应按账号去重')
  assert.equal(tracker.pendingFailures[0]?.statusCode, 503, '重复账号失败应保留最新失败信息')
  assert.equal(tracker.pendingFailureIndexByAccountId.size, 1, '待确认失败索引应同步去重')

  for (let index = 0; index < 300; index += 1) {
    clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, createTestAccount(`overflow-${index}`), {
      errorPhase: 'upstream_request',
      errorMessage: `overflow-${index}`
    })
  }
  assert.equal(tracker.pendingFailures.length, 256, '单请求待确认账号失败数量应固定封顶')
  assert.equal(tracker.pendingFailureIndexByAccountId.size, 256, '待确认失败索引大小应跟随固定上限')
  assert(!tracker.pendingFailureIndexByAccountId.has('overflow-299'), '超过上限的待确认失败不应继续扩容')

  const source = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const target = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const shared = createTestAccount('transfer-shared')
  clientIpAvoidance.rememberClientIpAccountPendingFailure(target, shared, {
    errorPhase: 'upstream_response',
    statusCode: 500,
    errorCode: 'target-old'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, shared, {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'source-new'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, createTestAccount('transfer-a'), {
    errorPhase: 'upstream_request',
    errorMessage: 'transfer-a'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, createTestAccount('transfer-b'), {
    errorPhase: 'upstream_request',
    errorMessage: 'transfer-b'
  })
  clientIpAvoidance.transferClientIpAccountPendingFailures(source, target)
  assert.equal(source.pendingFailures.length, 0, '转移后来源 tracker 应清空待确认失败')
  assert.equal(source.pendingFailureIndexByAccountId.size, 0, '转移后来源 tracker 索引应同步清空')
  assert.equal(target.pendingFailures.length, 3, '转移到目标 tracker 时应去重追加而不是数组头插')
  const sharedIndex = target.pendingFailureIndexByAccountId.get(shared.id)
  assert.notEqual(sharedIndex, undefined, '目标 tracker 应保留共享账号索引')
  assert.equal(target.pendingFailures[sharedIndex ?? -1]?.errorCode, 'source-new', '转移时同账号失败应更新为来源最新信息')
}

function rememberPendingFailureTwice(
  tracker: ReturnType<typeof clientIpAvoidance.createClientIpAccountAvoidanceTracker>,
  account: Parameters<typeof clientIpAvoidance.rememberClientIpAccountPendingFailure>[1],
  failure: Parameters<typeof clientIpAvoidance.rememberClientIpAccountPendingFailure>[2]
): void {
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, account, failure)
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterFinalFailure(tracker)
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, account, failure)
}

function seedTwoAccountGateway(upstreamBaseUrl: string): SeededGateway {
  const group = repositories.createGroup({
    name: 'IP 级账号回避回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const firstUpstreamKey = 'sk-client-ip-avoidance-first'
  const secondUpstreamKey = 'sk-client-ip-avoidance-second'
  const firstAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '01-IP回避首选账号',
    type: 'api_key',
    credentials: {
      api_key: firstUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5', 'gpt-5.3-codex']
  }, access)
  activateFixtureAccount(firstAccount)
  const secondAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '02-IP回避备用账号',
    type: 'api_key',
    credentials: {
      api_key: secondUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5', 'gpt-5.3-codex']
  }, access)
  activateFixtureAccount(secondAccount)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'IP 级账号回避回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    firstAccountId: firstAccount.id,
    secondAccountId: secondAccount.id,
    firstUpstreamKey,
    secondUpstreamKey
  }
}

function activateFixtureAccount(account: ReturnType<typeof repositories.createAccount>): void {
  assert(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    expectedConfigRevision: account.configRevision
  }), `Mock AI 测试账户 ${account.id} 应能通过健康检查激活`)
}

function createGatewayServer(): http.Server {
  const app = express()
  app.set('trust proxy', 1)
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(state: MockUpstreamState): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const accountKey = bearerToken(req.headers.authorization)
      const body = parseJsonObject(Buffer.concat(chunks).toString('utf8'))
      const scenario = requestScenario(body)
      state.requests.push({ accountKey, scenario })

      if ((scenario === 'ip-a-prime' || scenario === 'ip-a-followup' || scenario === 'ip-a-third') && accountKey === 'sk-client-ip-avoidance-first') {
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'temporary upstream failure for IP A', type: 'mock_error', code: 'mock_unconfirmed_failure' } }))
        return
      }

      if (scenario === 'opaque-all-fail') {
        const statusCode = accountKey === 'sk-client-ip-avoidance-first' ? 429 : 500
        res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'opaque upstream failure', type: 'untrusted_type', code: 'untrusted_code' } }))
        return
      }

      if ((scenario === 'ip-a-stream-final-failure' || scenario === 'ip-a-stream-second-chance') && accountKey === 'sk-client-ip-avoidance-first') {
        sendIncompleteResponsesStream(res)
        return
      }

      const from = accountKey === 'sk-client-ip-avoidance-first' ? 'first' : 'second'
      if (body.stream === true) {
        sendCompletedResponsesStream(res, `ok from ${from}`)
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: `chatcmpl-client-ip-${from}`,
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: `ok from ${from}` },
            finish_reason: 'stop'
          }
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
  })
}

function sendIncompleteResponsesStream(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.created\ndata: ${JSON.stringify({ type: 'response.created', response: { id: 'resp-stream-final-failure', status: 'in_progress' } })}\n\n`)
  res.write(`event: response.in_progress\ndata: ${JSON.stringify({ type: 'response.in_progress', response: { id: 'resp-stream-final-failure', status: 'in_progress' } })}\n\n`)
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: 'response.output_text.delta', delta: 'partial from first' })}\n\n`)
  res.end()
}

function sendCompletedResponsesStream(res: http.ServerResponse, text: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.created\ndata: ${JSON.stringify({ type: 'response.created', response: { id: 'resp-stream-success', status: 'in_progress' } })}\n\n`)
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: 'response.output_text.delta', delta: text })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({ type: 'response.completed', response: { id: 'resp-stream-success', status: 'completed', usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 } } })}\n\n`)
  res.end()
}

async function requestChatCompletion(baseUrl: string, apiKey: string, clientIp: string, scenario: string): Promise<string> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: scenario }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `请求 ${scenario} 应成功，实际 HTTP ${response.status}: ${text}`)
  return text
}

async function requestInvalidJson(baseUrl: string, apiKey: string, clientIp: string): Promise<Response> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp
    },
    body: '{"model":'
  })
  await response.text()
  return response
}

async function requestCodexResponsesStream(
  baseUrl: string,
  apiKey: string,
  clientIp: string,
  scenario: string,
  turnId: string
): Promise<{ text: string; connectionTerminated: boolean }> {
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json',
        accept: 'text/event-stream',
        'x-forwarded-for': clientIp,
        'x-codex-turn-metadata': JSON.stringify({
          turn_id: turnId,
          session_id: 'client-ip-account-avoidance-regression',
          thread_id: 'thread-client-ip-account-avoidance'
        })
      },
      body: JSON.stringify({
        model: 'gpt-5.3-codex',
        input: scenario,
        stream: true
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Codex 流请求 ${scenario} 应保持 HTTP 200 SSE，实际 HTTP ${response.status}: ${text}`)
    return { text, connectionTerminated: false }
  } catch (error) {
    if (isTransientFetchResetError(error)) {
      return { text: '', connectionTerminated: true }
    }
    throw error
  }
}

function isTransientFetchResetError(error: unknown): boolean {
  const cause = error instanceof Error
    ? error.cause as { code?: unknown } | undefined
    : undefined
  const code = cause?.code ?? (error as { code?: unknown } | undefined)?.code
  return code === 'ECONNRESET' || code === 'UND_ERR_SOCKET'
}

function assertAccountsStillActive(seeded: SeededGateway): void {
  const accounts = repositories.listAccounts(access)
  for (const accountId of [seeded.firstAccountId, seeded.secondAccountId]) {
    const account = accounts.find((item) => item.id === accountId)
    assert(account, `账号 ${accountId} 不存在`)
    assert.equal(account.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
    assert.equal(account.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
    assert.equal(account.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
    assert.equal(account.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
  }
}

function hitCount(state: MockUpstreamState, accountKey: string, scenario: string): number {
  return state.requests.filter((request) => request.accountKey === accountKey && request.scenario === scenario).length
}

function bearerToken(value: unknown): string {
  const text = Array.isArray(value) ? value[0] : String(value ?? '')
  return text.replace(/^Bearer\s+/i, '')
}

function requestScenario(body: Record<string, unknown>): string {
  const inputScenario = scenarioTextFromOpenAIInput(body.input)
  if (inputScenario) return inputScenario
  const messages = Array.isArray(body.messages) ? body.messages : []
  const firstMessage = messages.find((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
  return typeof firstMessage?.content === 'string' ? firstMessage.content : 'unknown'
}

function scenarioTextFromOpenAIInput(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) {
    for (const item of value) {
      const text = scenarioTextFromOpenAIInput(item)
      if (text) return text
    }
    return undefined
  }
  if (typeof value !== 'object' || value === null) return undefined
  const record = value as Record<string, unknown>
  if (typeof record.text === 'string') return record.text
  if (typeof record.content === 'string') return record.content
  return scenarioTextFromOpenAIInput(record.content)
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function createTestAccount(
  id: string,
  options: { priority?: number; superPriorityEnabled?: boolean; fallbackEnabled?: boolean } = {}
): Parameters<typeof clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance>[0][number] {
  return {
    id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_bypass',
    accountOwnerSystemAccountId: 'sys_bypass',
    groupOwnerSystemAccountId: 'sys_bypass',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: 'api_key',
    status: 'active',
    credentials: {},
    apiKey: '',
    baseUrl: 'http://127.0.0.1/v1',
    proxyProfileId: undefined,
    concurrencyLimit: 1,
    cooldownUntil: undefined,
    lastErrorMessage: undefined,
    streamFailureCount: 0,
    streamFailureWindowStartedAt: undefined,
    priority: options.priority ?? 0,
    superPriorityEnabled: options.superPriorityEnabled ?? false,
    fallbackEnabled: options.fallbackEnabled ?? false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'responses_sse',
    supportedModels: []
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

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

await main()
