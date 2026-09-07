import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { resolveGatewaySessionIdentity } from '../../modules/gateway/session-identity/index.js'
import { logger } from '../../shared/logger.js'
import type { UsageRecordSummary } from '../../storage/repositories.js'
import { requireUsageRecordDetails } from '../shared/usage-record-detail.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-turn-switch-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'codex-turn-switch.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'codex-turn-switch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const codexSwitchTestModel = 'gpt-5.3-codex'

assertCodexAccountScopedGuidanceIsNotClientRetryable()

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
  codexTurnRetry,
  sessionAffinity,
  accountCircuit,
  proxyHealth,
  readWorkerPool,
  clientStrategies
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
  import('../../modules/gateway/client-profiles/codex-turn-retry.service.js'),
  import('../../modules/gateway/runtime/session-affinity.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/proxy-health.service.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../modules/gateway/client-profiles/strategy.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  systemAccountId: string
  failedAccountId: string
  freshAccountId: string
  apiKeyId: string
  routeStrategyId: string
  failedUpstreamKey: string
  freshUpstreamKey: string
}

interface SeededThreeAccountGateway extends SeededGateway {
  latentFailedAccountId: string
  latentFailedUpstreamKey: string
}

interface SeededProbeFailureGateway extends Omit<SeededGateway, 'freshAccountId' | 'freshUpstreamKey'> {
  probeFailedAccountId: string
  probeFailedUpstreamKey: string
}

interface MockUpstreamState {
  responseHitsByUpstreamKey: Record<string, number>
  testProbeHitsByUpstreamKey: Record<string, number>
  requests: Array<{
    upstreamKey: string
    scenario: string
    turnMetadata?: string
  }>
}

let sequence = 0
let seedOwnerAccess: { systemAccountId: string; role: 'user' } | undefined

async function main(): Promise<void> {
  let gatewayServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamState: MockUpstreamState = {
    responseHitsByUpstreamKey: {},
    testProbeHitsByUpstreamKey: {},
    requests: []
  }

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    codexTurnRetry.clearCodexTurnRetryStateForTest()
    settingsRepository.updateSettings({
      textFirstResponseTimeoutSeconds: 10,
      textStreamIdleTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const codexSwitch = seedTwoAccountGateway(upstreamBaseUrl, 'codex-switch')
    const latentCodexSwitch = seedThreeAccountGateway(upstreamBaseUrl, 'codex-latent-switch')
    const concurrentBadSessionStorm = seedThreeAccountGateway(upstreamBaseUrl, 'codex-concurrent-bad-session-storm', {
      concurrencyLimit: 64
    })
    const probeFailCodexSwitch = seedProbeFailureGateway(upstreamBaseUrl, 'codex-probe-fail')
    const turnProbeFailCodexSwitch = seedProbeFailureGateway(upstreamBaseUrl, 'codex-turn-probe-fail')
    const coldHttpFailCodex = seedProbeFailureGateway(upstreamBaseUrl, 'codex-http-fail-cold')
    const httpFailCodex = seedProbeFailureGateway(upstreamBaseUrl, 'codex-http-fail-after-storm')
    const nonCodexHttpAllFail = seedProbeFailureGateway(upstreamBaseUrl, 'non-codex-http-all-fail')
    const contextWindow = seedTwoAccountGateway(upstreamBaseUrl, 'context-window')
    const cyberPolicy = seedTwoAccountGateway(upstreamBaseUrl, 'cyber-policy')
    const clientAbortAffinity = seedTwoAccountGateway(upstreamBaseUrl, 'client-abort-affinity', { freshPriority: 0 })
    const clientAbortBeforeHeadersAffinity = seedTwoAccountGateway(upstreamBaseUrl, 'client-abort-before-headers-affinity', { freshPriority: 0 })

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertCodexPreCommitFailureSwitchesAccountOnServer(baseUrl, codexSwitch, upstreamState)
    await assertCodexPreCommitFailureWalksCandidatesOnServer(baseUrl, latentCodexSwitch, upstreamState)
    await assertCodexHttpNon2xxAllCandidatesReturnRetryableSse(baseUrl, coldHttpFailCodex, upstreamState)
    await assertConcurrentBadSessionStormDoesNotOpenAccountCircuits(baseUrl, concurrentBadSessionStorm, upstreamState)
    await assertCodexPreCommitFailureReturnsRetryableWhenAllCandidatesFail(baseUrl, probeFailCodexSwitch, upstreamState)
    await assertCodexTurnAvoidanceUsesFormalRequestWithoutProbe(baseUrl, turnProbeFailCodexSwitch, upstreamState)
    await assertCodexHttpNon2xxAllCandidatesReturnRetryableSse(baseUrl, httpFailCodex, upstreamState)
    await assertGenericHttpNon2xxRetriesCandidates(baseUrl, nonCodexHttpAllFail, upstreamState)
    await assertCodexContextWindowSingleRequestSwitchesAccountOnServer(baseUrl, contextWindow, upstreamState)
    await assertCodexPostOutputFailureTerminatesWithoutReplay(baseUrl, cyberPolicy, upstreamState)
    await assertClientAbortClearsSessionAffinity(baseUrl, clientAbortAffinity, clientAbortBeforeHeadersAffinity, upstreamState)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecords(codexSwitch)
    assertAccountsStillActive([codexSwitch, latentCodexSwitch, concurrentBadSessionStorm, contextWindow, cyberPolicy, clientAbortAffinity, clientAbortBeforeHeadersAffinity, {
      ...probeFailCodexSwitch,
      freshAccountId: probeFailCodexSwitch.probeFailedAccountId,
      freshUpstreamKey: probeFailCodexSwitch.probeFailedUpstreamKey
    }, {
      ...turnProbeFailCodexSwitch,
      freshAccountId: turnProbeFailCodexSwitch.probeFailedAccountId,
      freshUpstreamKey: turnProbeFailCodexSwitch.probeFailedUpstreamKey
    }, {
      ...coldHttpFailCodex,
      freshAccountId: coldHttpFailCodex.probeFailedAccountId,
      freshUpstreamKey: coldHttpFailCodex.probeFailedUpstreamKey
    }, {
      ...httpFailCodex,
      freshAccountId: httpFailCodex.probeFailedAccountId,
      freshUpstreamKey: httpFailCodex.probeFailedUpstreamKey
    }, {
      ...nonCodexHttpAllFail,
      freshAccountId: nonCodexHttpAllFail.probeFailedAccountId,
      freshUpstreamKey: nonCodexHttpAllFail.probeFailedUpstreamKey
    }])

    console.log('Codex turn 切号 e2e 回归通过：服务端优先隐藏切号并扫完候选，同一坏会话 64 路并发不会误杀账户且独立会话会重新按当前优先级选号，Codex turn 避让直接用正式请求验证备用账号且不执行同步探针，HTTP 候选耗尽后返回稳定可重试错误，下游连接关闭会释放会话亲和且不继续切号')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    usageRecordQueue.clearUsageRecordQueueForTest()
    accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool()
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
      await delay(100)
      await readWorkerPool.closeSqliteReadWorkerPool()
    } catch {
    }
    await removeTempRoot()
  }
}

async function assertCodexPreCommitFailureSwitchesAccountOnServer(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeFreshProbeHits = testProbeHitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeState = await codexGatewayStateSnapshot(seeded)

  const result = await requestResponsesRaw(baseUrl, seeded.apiKey, {
    scenario: 'codex-retry-switch',
    turnId: 'turn-codex-switch',
    codex: true,
    retryTag: 'server-retry'
  })
  const afterState = await codexGatewayStateSnapshot(seeded)
  const diagnostic = JSON.stringify({
    response: result,
    hitDelta: {
      failed: hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits,
      fresh: hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits,
      freshProbe: testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits
    },
    beforeState,
    afterState
  })
  assert.equal(result.status, 200, `Codex 输出前失败切号必须保持 200 SSE：${diagnostic}`)
  assert(result.contentType.includes('text/event-stream'), `Codex 输出前失败切号必须保持 SSE content-type：${diagnostic}`)
  const streamText = result.text
  assert(streamText.includes('response.completed'), `Codex 输出前失败应由服务端切到备用账号并完成：${diagnostic}`)
  assert(!streamText.includes('response.failed'), `Codex 服务端切号成功时不应把中间失败交给客户端：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `Codex 服务端切号成功时不应消耗客户端重试次数：${streamText}`)
  assert(!streamText.includes('internal_server_error'), `Codex 服务端切号成功时不应透出首个账号错误码：${streamText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, 'Codex 本次请求应先命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'Codex 本次请求应隐藏切到备用账号')
  assert.equal(testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits, 0, '服务端隐藏重试不应消耗 Codex turn 探针')
}

async function assertCodexPreCommitFailureWalksCandidatesOnServer(
  baseUrl: string,
  seeded: SeededThreeAccountGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeLatentFailedHits = hitCount(upstreamState, seeded.latentFailedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeLatentProbeHits = testProbeHitCount(upstreamState, seeded.latentFailedUpstreamKey)
  const beforeFreshProbeHits = testProbeHitCount(upstreamState, seeded.freshUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-missing-terminal-switch',
    turnId: 'turn-codex-latent-switch',
    codex: true,
    retryTag: 'server-retry'
  })
  assert(streamText.includes('response.completed'), `Codex 应在同一次请求内扫过假正常死号并切到真可用账号完成：${streamText}`)
  assert(!streamText.includes('response.failed'), `Codex 服务端切号成功时不应把中间失败交给客户端：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `Codex 服务端切号成功时不应因假正常死号消耗客户端重试次数：${streamText}`)
  assert(!streamText.includes('resp_missing_terminal'), `Codex 服务端切号成功时不应下发预提交失败事件：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, 'Codex 本次请求应先命中首选死号')
  assert.equal(hitCount(upstreamState, seeded.latentFailedUpstreamKey) - beforeLatentFailedHits, 1, 'Codex 本次请求应隐藏重试到假正常死号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'Codex 本次请求应最终命中真可用账号')
  assert.equal(testProbeHitCount(upstreamState, seeded.latentFailedUpstreamKey) - beforeLatentProbeHits, 0, '服务端隐藏重试不应消耗 Codex turn 探针')
  assert.equal(testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits, 0, '服务端隐藏重试不应提前探针真可用账号')
}

async function assertConcurrentBadSessionStormDoesNotOpenAccountCircuits(
  baseUrl: string,
  seeded: SeededThreeAccountGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const turnId = `turn-concurrent-bad-session-${Date.now()}`
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeLatentFailedHits = hitCount(upstreamState, seeded.latentFailedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const responses = await Promise.all(Array.from({ length: 64 }, () => requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-missing-terminal-switch',
    turnId,
    codex: true,
    retryTag: 'concurrent-bad-session-storm'
  })))

  assert.equal(responses.length, 64, '坏会话并发风暴必须覆盖 50+ 并发请求')
  assert(responses.every((response) => response.includes('response.completed')), '同一坏会话 64 并发风暴应全部由健康账户完成')
  assert(responses.every((response) => !response.includes('response.failed')), '同一坏会话 64 并发风暴不应把中间失败交给客户端')
  assert(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits >= 1, '并发风暴应真实命中首选失败账户')
  assert(hitCount(upstreamState, seeded.latentFailedUpstreamKey) - beforeLatentFailedHits >= 1, '并发风暴应真实扫过第二个失败账户')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, responses.length, '每个并发请求最终都应由健康账户承接')

  const candidates = repositories.listOpenAIAccountsForGroup(seeded.groupId, seeded.systemAccountId, {
    requestedModel: codexSwitchTestModel
  })
  const failedAccount = candidates.find((account) => account.id === seeded.failedAccountId)
  const latentFailedAccount = candidates.find((account) => account.id === seeded.latentFailedAccountId)
  assert(failedAccount && latentFailedAccount, '并发风暴后应仍能读取两个失败账户候选')
  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  const failedState = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(failedAccount, 'text', codexSwitchTestModel))
  const latentFailedState = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(latentFailedAccount, 'text', codexSwitchTestModel))
  assert.notEqual(failedState.phase, 'OPEN', '同一坏会话并发不得把首选账户确认成 OPEN')
  assert.notEqual(latentFailedState.phase, 'OPEN', '同一坏会话并发不得把第二个账户确认成 OPEN')

  const beforeIndependentRetryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const independentRetry = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-turn-direct-formal-success',
    turnId: `${turnId}-independent`,
    codex: true,
    retryTag: 'independent-session-recovery'
  })
  assert(independentRetry.includes('response.completed'), '独立会话应能重新验证并恢复风暴中的首选账户')
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeIndependentRetryHits, 1, '独立会话应按当前优先级重新尝试首选账户')
  const recoveredState = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(failedAccount, 'text', codexSwitchTestModel))
  assert.notEqual(recoveredState.phase, 'OPEN', '独立会话成功后首选账户不得保持或进入 OPEN')
  assert(['CLOSED', 'RECOVERING'].includes(recoveredState.phase), `独立会话成功后首选账户应可正常使用或进入恢复态，实际 ${recoveredState.phase}`)
}

async function assertCodexPreCommitFailureReturnsRetryableWhenAllCandidatesFail(
  baseUrl: string,
  seeded: SeededProbeFailureGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeProbeFailedHits = hitCount(upstreamState, seeded.probeFailedUpstreamKey)
  const beforeProbeHits = testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-missing-terminal-switch',
    turnId: 'turn-codex-probe-fail',
    codex: true,
    retryTag: 'server-retry-exhausted'
  })
  assert(streamText.includes('response.failed'), `Codex 全部账号耗尽时应返回 SSE 失败事件：${streamText}`)
  assert(streamText.includes('upstream_retryable_error'), `Codex 全部账号耗尽时应返回客户端可重试错误：${streamText}`)
  assert(!streamText.includes('response.completed'), `全部备用号失败不应伪成功：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, '全部失败场景应先命中首选死号')
  assert.equal(hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits, 1, '全部失败场景应隐藏重试唯一备用号')
  assert.equal(testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeHits, 0, '服务端隐藏重试耗尽前不应消耗 Codex turn 探针')
}

async function assertCodexTurnAvoidanceUsesFormalRequestWithoutProbe(
  baseUrl: string,
  seeded: SeededProbeFailureGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  proxyHealth.clearGatewayProxyHealthForTest()
  const turnId = 'turn-codex-probe-visible-fail'
  rememberVisibleCodexTurnFailures(seeded, turnId, [
    seeded.failedAccountId,
    seeded.failedAccountId
  ])
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeProbeFailedHits = hitCount(upstreamState, seeded.probeFailedUpstreamKey)
  const beforeProbeHits = testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-turn-direct-formal-success',
    turnId,
    codex: true,
    retryTag: 'turn-direct-formal-request'
  })
  assert(streamText.includes('response.completed'), `Codex turn 避让后应直接用正式请求验证备用账号并成功：${streamText}`)
  assert(!streamText.includes('response.failed'), `正式请求成功时不应返回客户端失败事件：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `正式请求成功时不应消耗客户端重试：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 0, 'Codex turn 避让后不应再次命中已失败账号')
  assert.equal(hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits, 1, 'Codex turn 避让后应直接向备用账号发起正式请求')
  assert.equal(testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeHits, 0, 'Codex turn 正式请求热路径不应先执行同步账号探针')
}

async function assertCodexHttpNon2xxAllCandidatesReturnRetryableSse(
  baseUrl: string,
  seeded: SeededProbeFailureGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeProbeFailedHits = hitCount(upstreamState, seeded.probeFailedUpstreamKey)
  const beforeState = await codexGatewayStateSnapshot(seeded)

  const result = await requestResponsesRaw(baseUrl, seeded.apiKey, {
    scenario: 'codex-http-non2xx-all-fail',
    turnId: 'turn-codex-http-all-fail',
    codex: true,
    retryTag: 'server-retry-exhausted'
  })
  const afterState = await codexGatewayStateSnapshot(seeded)
  const transportDiagnostic = JSON.stringify({
    response: {
      status: result.status,
      contentType: result.contentType,
      headers: result.headers,
      text: result.text
    },
    hitDelta: {
      failed: hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits,
      fallback: hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits
    },
    beforeState,
    afterState
  })
  assert.equal(result.status, 200, `Codex 精确客户端全候选非 2xx 必须固定为 200 SSE retry event，不得因并发/心跳时序变成 503 JSON：${transportDiagnostic}`)
  assert(result.contentType.includes('text/event-stream'), `Codex 精确客户端全候选非 2xx 必须保持 SSE content-type：${transportDiagnostic}`)
  assert(result.text.includes('response.failed'), `Codex 精确客户端全候选非 2xx 必须返回 SSE 失败事件：${transportDiagnostic}`)
  const streamText = result.text
  assert(streamText.includes('upstream_retryable_error'), `Codex HTTP 非 2xx 全部账号耗尽时应返回客户端可重试错误：${streamText}`)
  assert(!streamText.includes('server_is_overloaded'), `Codex HTTP 非 2xx 全部账号耗尽时不应透出原始致命错误码：${streamText}`)
  assert(!streamText.includes('response.completed'), `HTTP 非 2xx 全部失败不应伪成功：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, 'HTTP 非 2xx 全部失败场景应先命中首选账号')
  assert.equal(hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits, 1, 'HTTP 非 2xx 全部失败场景应继续尝试唯一备用号')
}

async function codexGatewayStateSnapshot(seeded: Pick<SeededGateway, 'groupId' | 'systemAccountId'>) {
  const candidates = repositories.listOpenAIAccountsForGroup(seeded.groupId, seeded.systemAccountId, {
    requestedModel: codexSwitchTestModel
  })
  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  return Promise.all(candidates.map(async (candidate) => {
    const persisted = repositories.findAccountForTest(candidate.id, {
      systemAccountId: seeded.systemAccountId,
      role: 'user'
    })
    const circuit = await circuitStore.get(
      accountCircuit.gatewayAccountProtocolModelScope(candidate, 'text', codexSwitchTestModel)
    )
    return {
      accountId: candidate.id,
      accountName: candidate.name,
      status: persisted?.status,
      schedulable: persisted?.schedulable,
      cooldownUntil: persisted?.cooldownUntil,
      circuitPhase: circuit.phase,
      circuitGeneration: circuit.generation,
      circuitDispatchRevision: circuit.dispatchRevision,
      circuitLease: circuit.lease?.kind
    }
  }))
}

async function assertGenericHttpNon2xxRetriesCandidates(
  baseUrl: string,
  seeded: SeededProbeFailureGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeProbeFailedHits = hitCount(upstreamState, seeded.probeFailedUpstreamKey)

  const result = await requestResponsesRaw(baseUrl, seeded.apiKey, {
    scenario: 'codex-http-non2xx-all-fail',
    turnId: 'non-codex-http-all-fail',
    codex: false,
    retryTag: 'server-retry-exhausted'
  })
  assert.equal(result.status, 503, `普通客户端完整 HTTP 非 2xx 全部耗尽后应返回可重试状态：${result.status} ${result.text}`)
  assert(result.contentType.includes('application/json'), `普通客户端完整 HTTP 非 2xx 全部耗尽后应返回 JSON：${result.contentType}`)
  assert(!result.text.includes('server_is_overloaded'), `普通客户端不应看到上游原始致命错误码：${result.text}`)
  assert(!result.text.includes('response.failed'), `普通客户端完整 HTTP 非 2xx 不应伪造 SSE：${result.text}`)
  assert(result.text.includes('upstream_retryable_error'), `普通客户端全部候选耗尽后应收到稳定可重试错误码：${result.text}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, '普通客户端完整 HTTP 非 2xx 应先命中首选账号一次')
  assert.equal(hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits, 1, '普通推理请求完整 HTTP 非 2xx 后应继续尝试唯一备用号')
}

async function assertCodexContextWindowSingleRequestSwitchesAccountOnServer(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeFreshProbeHits = testProbeHitCount(upstreamState, seeded.freshUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'context-window-error',
    turnId: 'turn-context-window',
    codex: true,
    retryTag: 'server-retry'
  })
  assert(streamText.includes('response.completed'), `context_length_exceeded 应由服务端切到备用账号并完成：${streamText}`)
  assert(!streamText.includes('response.failed'), `context_length_exceeded 服务端切号成功时不应把中间失败交给客户端：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `context_length_exceeded 服务端切号成功时不应消耗客户端重试次数：${streamText}`)
  assert(!streamText.includes('context_length_exceeded'), `context_length_exceeded 服务端切号成功时不应透出原始错误码：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, 'context_length_exceeded 本次请求应命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'context_length_exceeded 本次请求应命中备用账号')
  assert.equal(testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits, 0, 'context_length_exceeded 服务端隐藏重试不应消耗 Codex turn 探针')
}

async function assertCodexPostOutputFailureTerminatesWithoutReplay(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeFreshProbeHits = testProbeHitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeState = await codexGatewayStateSnapshot(seeded)
  const beforeRuntimeAvailability = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'cyber-policy-after-output-error',
    turnId: 'turn-cyber-policy',
    codex: true,
    retryTag: 'server-retry'
  })
  assert(streamText.includes('partial output'), `已有语义输出必须保留，不得回滚或用备用账号覆盖：${streamText}`)
  assert(!streamText.includes('response.completed'), `输出后失败不得拼接备用账号完成事件：${streamText}`)
  assert.equal(
    (streamText.match(/event: response\.failed/g) ?? []).length,
    1,
    `Codex 精确客户端在输出后失败时必须收到且只收到一个网关受控失败事件：${streamText}`
  )
  assert(streamText.includes('upstream_retryable_error'), `输出后受控失败事件必须使用稳定的客户端可重试码：${streamText}`)
  assert(streamText.includes('上游流式响应在输出后中断'), `输出后受控失败事件必须使用网关脱敏文案：${streamText}`)
  assert(!streamText.includes('upstream_stream_interrupted'), `输出后精确客户端不得继续使用旧中断码：${streamText}`)
  assert(!streamText.includes('cyber_policy'), `输出后不得透出供应商自造错误码：${streamText}`)
  assert(!streamText.includes('possible cybersecurity risk'), `输出后不得透出供应商错误文案：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, '输出后失败本次请求只应命中首选账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, '已有语义输出后严禁重放到备用账号')
  assert.equal(testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits, 0, '输出后稳定结束不得触发 Codex turn 探针')
  assert.deepEqual(await codexGatewayStateSnapshot(seeded), beforeState, '输出后供应商失败结构不得改变账户持久状态或 circuit')
  assert.deepEqual(
    accountSideEffects.snapshotGatewayAccountRuntimeAvailability(),
    beforeRuntimeAvailability,
    '输出后供应商失败结构不得创建账户运行态抑制'
  )

  const beforeTurnRetryFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeTurnRetryFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const turnRetryText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-turn-direct-formal-success',
    turnId: 'turn-cyber-policy',
    codex: true,
    retryTag: 'committed-retry-signal-account-avoidance'
  })
  assert(turnRetryText.includes('response.completed'), `输出后强失败的同 turn 重试应由备用账号完成：${turnRetryText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeTurnRetryFailedHits, 0, '强失败证据应让同 turn 下一请求跨优先级避开原账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeTurnRetryFreshHits, 1, '强失败证据应直接选择同 turn 备用账号')
}

async function assertClientAbortClearsSessionAffinity(
  baseUrl: string,
  seeded: SeededGateway,
  headerSeeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const sessionId = `session-client-abort-affinity-${Date.now()}`
  const beforePrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  const sessionKey = sessionAffinity.resolveOpenAIGatewaySessionAffinityKey(createSessionIdentity(sessionId, seeded), {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId,
    routeStrategyId: seeded.routeStrategyId,
    providerProtocolProfileId: sessionAffinityProviderProfilePool(seeded)
  })
  assert(sessionKey, '测试应能生成会话亲和 key')
  sessionAffinity.rememberOpenAIAccountForSession(sessionKey, seeded.freshAccountId, {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
  })

  await requestResponsesStreamAndAbortAfterFirstChunk(baseUrl, seeded.apiKey, {
    scenario: 'client-abort-before-terminal',
    turnId: 'turn-client-abort-affinity',
    codex: true,
    sessionId
  })
  assert.equal(
    hitCount(upstreamState, seeded.failedUpstreamKey) - beforePrimaryHits,
    0,
    `下游连接关闭请求不应先走正常顺序主账号：${JSON.stringify(upstreamState.requests.slice(-4))}`
  )
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeStickyHits, 1, '下游连接关闭请求应命中已绑定的备用账号')

  const postAbortCandidates = repositories.listOpenAIAccountsForGroup(seeded.groupId, seeded.systemAccountId)
  const postAbortFreshAccount = postAbortCandidates.find((account) => account.id === seeded.freshAccountId)
  assert(postAbortFreshAccount, '客户端中断后应仍能读取亲和账户候选')
  const postAbortCircuitState = await accountCircuit.getGatewayAccountCircuitStore().get(
    accountCircuit.gatewayAccountProtocolModelScope(postAbortFreshAccount, 'text', codexSwitchTestModel)
  )
  assert.equal(postAbortCircuitState.phase, 'CLOSED', '下游连接关闭不得把账户电路推进到 SUSPECT/OPEN')
  const postAbortRuntimeFilter = await accountSideEffects.filterGatewayAccountRuntimeSuppressionsAsync(postAbortCandidates)
  assert(postAbortRuntimeFilter.accounts.some((account) => account.id === seeded.freshAccountId), '下游连接关闭不得把账户加入运行时抑制')

  const beforeRetryPrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeRetryStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const retryText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'after-client-abort',
    turnId: 'turn-client-abort-affinity',
    codex: true,
    sessionId
  })
  assert(retryText.includes('response.completed'), `下游连接关闭后下一次请求应完成：${retryText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeRetryPrimaryHits, 1, '下游连接关闭后应释放会话亲和并回到正常账号顺序')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeRetryStickyHits, 0, '下游连接关闭后不应继续粘住已断开的备用账号')

  const weakTurnId = `turn-client-abort-weak-threshold-${Date.now()}`
  const beforeWeakPrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeWeakFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  await requestResponsesStreamAndAbortAfterFirstChunk(baseUrl, seeded.apiKey, {
    scenario: 'client-abort-before-terminal',
    turnId: weakTurnId,
    codex: true
  })
  await requestResponsesStreamAndAbortAfterFirstChunk(baseUrl, seeded.apiKey, {
    scenario: 'client-abort-before-terminal',
    turnId: weakTurnId,
    codex: true
  })
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeWeakPrimaryHits, 2, '同账号第一次弱断流后第二次仍应按正常顺序尝试原账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeWeakFreshHits, 0, '弱证据达到两次前不得提前切号')
  const weakRetryText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'after-client-abort',
    turnId: weakTurnId,
    codex: true
  })
  assert(weakRetryText.includes('response.completed'), `两次弱断流后的第三次请求应完成：${weakRetryText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeWeakPrimaryHits, 2, '两次同账号弱断流后第三次请求不得再次命中原账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeWeakFreshHits, 1, '两次同账号弱断流后第三次请求应切到备用账号')

  const headerDelaySessionId = `session-client-abort-before-headers-${Date.now()}`
  const headerDelaySessionKey = sessionAffinity.resolveOpenAIGatewaySessionAffinityKey(createSessionIdentity(headerDelaySessionId, headerSeeded), {
    systemAccountId: headerSeeded.systemAccountId,
    apiKeyId: headerSeeded.apiKeyId,
    groupId: headerSeeded.groupId,
    routeStrategyId: headerSeeded.routeStrategyId,
    providerProtocolProfileId: sessionAffinityProviderProfilePool(headerSeeded)
  })
  assert(headerDelaySessionKey, '测试应能生成响应头前断开场景的会话亲和 key')
  sessionAffinity.rememberOpenAIAccountForSession(headerDelaySessionKey, headerSeeded.freshAccountId, {
    systemAccountId: headerSeeded.systemAccountId,
    apiKeyId: headerSeeded.apiKeyId,
    groupId: headerSeeded.groupId
  })
  const headerCandidates = repositories.listOpenAIAccountsForGroup(headerSeeded.groupId, headerSeeded.systemAccountId)
  const headerDelayAffinityOrder = sessionAffinity.orderOpenAIAccountsBySessionAffinity(headerCandidates, headerDelaySessionKey)
  assert.equal(headerDelayAffinityOrder[0]?.id, headerSeeded.freshAccountId, '新会话亲和应在同优先级候选中重新选择已绑定账户')
  const beforeHeaderAbortPrimaryHits = hitCount(upstreamState, headerSeeded.failedUpstreamKey)
  const beforeHeaderAbortStickyHits = hitCount(upstreamState, headerSeeded.freshUpstreamKey)
  await requestResponsesStreamAndAbortAfterUpstreamRequestStarted(baseUrl, headerSeeded.apiKey, {
    scenario: 'client-abort-before-upstream-headers',
    turnId: 'turn-client-abort-before-headers',
    codex: true,
    sessionId: headerDelaySessionId
  }, () => (
    hitCount(upstreamState, headerSeeded.freshUpstreamKey) - beforeHeaderAbortStickyHits >= 1
    || hitCount(upstreamState, headerSeeded.failedUpstreamKey) - beforeHeaderAbortPrimaryHits >= 1
  ))
  assert.equal(hitCount(upstreamState, headerSeeded.failedUpstreamKey) - beforeHeaderAbortPrimaryHits, 0, '响应头前下游连接关闭请求不应先走正常顺序主账号')
  assert.equal(hitCount(upstreamState, headerSeeded.freshUpstreamKey) - beforeHeaderAbortStickyHits, 1, '响应头前下游连接关闭请求应命中已绑定的备用账号')

  const beforeHeaderAbortRetryPrimaryHits = hitCount(upstreamState, headerSeeded.failedUpstreamKey)
  const beforeHeaderAbortRetryStickyHits = hitCount(upstreamState, headerSeeded.freshUpstreamKey)
  const headerAbortRetryText = await requestResponsesStream(baseUrl, headerSeeded.apiKey, {
    scenario: 'after-client-abort',
    turnId: 'turn-client-abort-before-headers',
    codex: true,
    sessionId: headerDelaySessionId
  })
  assert(headerAbortRetryText.includes('response.completed'), `响应头前下游连接关闭后下一次请求应完成：${headerAbortRetryText}`)
  assert.equal(hitCount(upstreamState, headerSeeded.failedUpstreamKey) - beforeHeaderAbortRetryPrimaryHits, 1, '响应头前下游连接关闭后应释放会话亲和并回到正常账号顺序')
  assert.equal(hitCount(upstreamState, headerSeeded.freshUpstreamKey) - beforeHeaderAbortRetryStickyHits, 0, '响应头前下游连接关闭后不应继续粘住已断开的备用账号')

  usageRecordQueue.flushAllUsageRecordQueue()
  const records = allUsageRecordsForRegression()
  assert(
    records.some((record) => (
      (record.accountId === seeded.freshAccountId || record.accountId === headerSeeded.freshAccountId)
      && record.errorCode === 'downstream_connection_closed'
      && record.errorMessage === '下游连接关闭'
    )),
    '下游连接关闭使用记录应使用统一文案，避免误导为用户手动取消'
  )
}

function seedTwoAccountGateway(upstreamBaseUrl: string, label: string, options: { freshPriority?: number } = {}): SeededGateway {
  sequence += 1
  const access = seedGatewayAccess()
  const group = repositories.createGroup({
    name: `Codex 切号 e2e 分组-${label}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const failedUpstreamKey = `sk-codex-switch-${sequence}-failed`
  const freshUpstreamKey = `sk-codex-switch-${sequence}-fresh`
  const failedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `A-Codex 切号 e2e 失败账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(failedAccount)
  if ((options.freshPriority ?? 10) === 0) {
    waitForClockTick()
  }
  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `B-Codex 切号 e2e 备用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: freshUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: options.freshPriority ?? 10,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(freshAccount)
  if ((options.freshPriority ?? 10) === 0) {
    forceGroupAccountOrder(group.id, failedAccount.id, freshAccount.id)
  }
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `Codex 切号 e2e Key-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    systemAccountId: access.systemAccountId,
    failedAccountId: failedAccount.id,
    freshAccountId: freshAccount.id,
    apiKeyId: apiKey.id,
    routeStrategyId: apiKey.routeStrategyId,
    failedUpstreamKey,
    freshUpstreamKey
  }
}

function seedThreeAccountGateway(
  upstreamBaseUrl: string,
  label: string,
  options: { concurrencyLimit?: number } = {}
): SeededThreeAccountGateway {
  sequence += 1
  const access = seedGatewayAccess()
  const group = repositories.createGroup({
    name: `Codex 切号 e2e 分组三账号-${label}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const failedUpstreamKey = `sk-codex-switch-${sequence}-failed`
  const latentFailedUpstreamKey = `sk-codex-switch-${sequence}-latent-failed`
  const freshUpstreamKey = `sk-codex-switch-${sequence}-fresh`
  const failedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `A-Codex 切号 e2e 死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: options.concurrencyLimit,
    priority: 0,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(failedAccount)
  const latentFailedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `B-Codex 切号 e2e 假正常死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: latentFailedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: options.concurrencyLimit,
    priority: 5,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(latentFailedAccount)
  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `C-Codex 切号 e2e 真可用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: freshUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: options.concurrencyLimit,
    priority: 10,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(freshAccount)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `Codex 切号 e2e Key-三账号-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    systemAccountId: access.systemAccountId,
    failedAccountId: failedAccount.id,
    latentFailedAccountId: latentFailedAccount.id,
    freshAccountId: freshAccount.id,
    apiKeyId: apiKey.id,
    routeStrategyId: apiKey.routeStrategyId,
    failedUpstreamKey,
    latentFailedUpstreamKey,
    freshUpstreamKey
  }
}

function seedProbeFailureGateway(upstreamBaseUrl: string, label: string): SeededProbeFailureGateway {
  sequence += 1
  const access = seedGatewayAccess()
  const group = repositories.createGroup({
    name: `Codex 切号 e2e 全部探针失败分组-${label}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const failedUpstreamKey = `sk-codex-switch-${sequence}-failed`
  const probeFailedUpstreamKey = `sk-codex-switch-${sequence}-latent-failed`
  const failedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `A-Codex 切号 e2e 死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(failedAccount)
  const probeFailedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `B-Codex 切号 e2e 探针失败账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: probeFailedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10,
    supportedModels: [codexSwitchTestModel],
    healthCheckModel: codexSwitchTestModel
  }, access)
  activateFixtureAccount(probeFailedAccount)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `Codex 切号 e2e Key-全部探针失败-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    systemAccountId: access.systemAccountId,
    failedAccountId: failedAccount.id,
    probeFailedAccountId: probeFailedAccount.id,
    apiKeyId: apiKey.id,
    routeStrategyId: apiKey.routeStrategyId,
    failedUpstreamKey,
    probeFailedUpstreamKey
  }
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(state: MockUpstreamState): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const bodyChunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => bodyChunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const upstreamKey = bearerToken(req.headers.authorization)
      const body = parseJsonObject(Buffer.concat(bodyChunks).toString('utf8'))

      const accountTestProbe = isAccountTestProbeBody(body)
      if (url.pathname !== '/v1/responses' && !(url.pathname === '/v1/chat/completions' && accountTestProbe)) {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { code: 'mock_unexpected_path', message: `unexpected path ${url.pathname}` } }))
        return
      }

      if (accountTestProbe) {
        state.testProbeHitsByUpstreamKey[upstreamKey] = testProbeHitCount(state, upstreamKey) + 1
        state.requests.push({ upstreamKey, scenario: 'account-test-probe' })
        if (upstreamKey.endsWith('-latent-failed')) {
          res.writeHead(402, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: { code: 'insufficient_quota', message: 'mock latent account real test failed' } }))
          return
        }
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache',
          connection: 'keep-alive'
        })
        sendCompletedStream(res)
        return
      }
      state.responseHitsByUpstreamKey[upstreamKey] = hitCount(state, upstreamKey) + 1

      const scenario = resolveMockScenario(body)
      const turnMetadata = typeof req.headers['x-codex-turn-metadata'] === 'string'
        ? req.headers['x-codex-turn-metadata']
        : undefined
      state.requests.push({ upstreamKey, scenario, turnMetadata })

      if (scenario === 'codex-http-non2xx-all-fail') {
        res.writeHead(503, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          error: {
            code: 'server_is_overloaded',
            message: 'mock upstream failed before stream headers'
          }
        }))
        return
      }

      if (scenario === 'client-abort-before-upstream-headers') {
        const timer = setTimeout(() => {
          if (res.destroyed || res.writableEnded) {
            return
          }
          res.writeHead(200, {
            'content-type': 'text/event-stream; charset=utf-8',
            'cache-control': 'no-cache',
            connection: 'keep-alive'
          })
          sendOpenStreamWithoutTerminal(res)
        }, 1000)
        res.once('close', () => clearTimeout(timer))
        return
      }

      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache',
        connection: 'keep-alive'
      })
      if (scenario === 'client-abort-before-terminal') {
        sendOpenStreamWithoutTerminal(res)
        return
      }
      if (scenario === 'after-client-abort') {
        sendCompletedStream(res)
        return
      }
      if (scenario === 'codex-turn-direct-formal-success') {
        sendCompletedStream(res)
        return
      }
      if (upstreamKey.endsWith('-fresh')) {
        sendCompletedStream(res)
        return
      }
      if (scenario === 'codex-missing-terminal-switch') {
        sendMissingTerminalStream(res)
        return
      }
      if (scenario === 'context-window-error') {
        sendFailedStream(res, 'context_length_exceeded', 'Your input exceeds the context window of this model.')
        return
      }
      if (scenario === 'cyber-policy-error') {
        sendFailedStream(res, 'cyber_policy', 'This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber')
        return
      }
      if (scenario === 'cyber-policy-after-output-error') {
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"partial output"}\n\n')
        sendFailedStream(res, 'cyber_policy', 'This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber')
        return
      }
      sendFailedStream(res, 'internal_server_error', 'mock upstream failed before output')
    })
  })
}

async function requestResponsesStream(
  baseUrl: string,
  apiKey: string,
  input: {
    scenario: string
    turnId: string
    codex: boolean
    sessionId?: string
    retryTag?: string
  }
): Promise<string> {
  const response = await requestResponsesRaw(baseUrl, apiKey, input)
  assert.equal(response.status, 200)
  assert(response.contentType.includes('text/event-stream'), '网关应保持 SSE content-type')
  return response.text
}

async function requestResponsesRaw(
  baseUrl: string,
  apiKey: string,
  input: {
    scenario: string
    turnId: string
    codex: boolean
    sessionId?: string
    retryTag?: string
  }
): Promise<{ status: number; contentType: string; headers: Record<string, string>; text: string }> {
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream'
  }
  if (input.codex) {
    headers['x-codex-turn-metadata'] = JSON.stringify({
      turn_id: input.turnId,
      session_id: `session-${input.turnId}`,
      thread_id: `thread-${input.turnId}`
    })
  }
  if (input.sessionId) {
    headers['session-id'] = input.sessionId
  }
  const response = await fetchResponsesWithTransientResetRetry(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model: codexSwitchTestModel,
      input: input.scenario,
      metadata: input.retryTag ? { retry_tag: input.retryTag } : undefined,
      stream: true
    })
  })
  return {
    status: response.status,
    contentType: response.headers.get('content-type') ?? '',
    headers: Object.fromEntries(response.headers.entries()),
    text: await response.text()
  }
}

async function fetchResponsesWithTransientResetRetry(url: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init)
  } catch (error) {
    if (!isTransientFetchResetError(error)) {
      throw error
    }
    await sleep(100)
    return await fetch(url, init)
  }
}

function isTransientFetchResetError(error: unknown): boolean {
  const cause = error instanceof Error
    ? error.cause as { code?: unknown } | undefined
    : undefined
  const code = cause?.code ?? (error as { code?: unknown } | undefined)?.code
  return code === 'ECONNRESET' || code === 'UND_ERR_SOCKET'
}

async function requestResponsesStreamAndAbortAfterFirstChunk(
  baseUrl: string,
  apiKey: string,
  input: {
    scenario: string
    turnId: string
    codex: boolean
    sessionId?: string
  }
): Promise<void> {
  const url = new URL(`${baseUrl}/v1/responses`)
  const body = JSON.stringify({
    model: codexSwitchTestModel,
    input: input.scenario,
    stream: true
  })
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
    'content-length': String(Buffer.byteLength(body))
  }
  if (input.codex) {
    headers['x-codex-turn-metadata'] = JSON.stringify({
      turn_id: input.turnId,
      session_id: `session-${input.turnId}`,
      thread_id: `thread-${input.turnId}`
    })
  }
  if (input.sessionId) {
    headers['session-id'] = input.sessionId
  }
  await new Promise<void>((resolveAbort, rejectAbort) => {
    let settled = false
    const settle = (callback: () => void) => {
      if (settled) return
      settled = true
      callback()
    }
    const req = http.request(url, {
      method: 'POST',
      headers
    }, (res) => {
      try {
        assert.equal(res.statusCode, 200)
        assert(String(res.headers['content-type'] ?? '').includes('text/event-stream'), '网关应保持 SSE content-type')
      } catch (error) {
        settle(() => rejectAbort(error))
        req.destroy()
        res.destroy()
        return
      }
      res.once('data', (chunk) => {
        try {
          assert(Buffer.isBuffer(chunk) && chunk.byteLength > 0, '测试请求应先收到首段流式数据再关闭下游连接')
        } catch (error) {
          settle(() => rejectAbort(error))
          req.destroy()
          res.destroy()
          return
        }
        req.destroy()
        res.destroy()
        settle(() => {
          setTimeout(resolveAbort, 500)
        })
      })
      res.once('end', () => {
        settle(() => rejectAbort(new Error('测试请求在首段数据前已结束')))
      })
      res.once('error', (error) => {
        if (!settled) {
          settle(() => rejectAbort(error))
        }
      })
    })
    req.once('error', (error) => {
      if (!settled) {
        settle(() => rejectAbort(error))
      }
    })
    req.write(body)
    req.end()
  })
}

async function requestResponsesStreamAndAbortAfterUpstreamRequestStarted(
  baseUrl: string,
  apiKey: string,
  input: {
    scenario: string
    turnId: string
    codex: boolean
    sessionId?: string
  },
  upstreamRequestStarted: () => boolean
): Promise<void> {
  const url = new URL('/v1/responses', baseUrl)
  const body = JSON.stringify({
    model: codexSwitchTestModel,
    input: input.scenario,
    stream: true
  })
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
    'content-length': String(Buffer.byteLength(body))
  }
  if (input.codex) {
    headers['x-codex-turn-metadata'] = JSON.stringify({
      turn_id: input.turnId,
      session_id: `session-${input.turnId}`,
      thread_id: `thread-${input.turnId}`
    })
  }
  if (input.sessionId) {
    headers['session-id'] = input.sessionId
  }
  let destroyedByTest = false
  let unexpectedRequestError: unknown
  let clientRequest: http.ClientRequest | undefined
  const requestFinished = new Promise<void>((resolvePromise) => {
    clientRequest = http.request(url, { method: 'POST', headers, agent: false }, (response) => {
      response.resume()
      response.once('end', resolvePromise)
      response.once('aborted', resolvePromise)
      response.once('error', (error) => {
        if (!destroyedByTest) unexpectedRequestError = error
        resolvePromise()
      })
    })
    clientRequest.once('error', (error) => {
      if (!destroyedByTest) unexpectedRequestError = error
      resolvePromise()
    })
    clientRequest.write(body)
    clientRequest.end()
  })
  await waitUntil(
    () => upstreamRequestStarted() || unexpectedRequestError !== undefined,
    3000,
    '响应头前断开测试应先命中上游账号'
  )
  if (unexpectedRequestError) throw unexpectedRequestError
  destroyedByTest = true
  clientRequest?.destroy()
  await requestFinished
  if (unexpectedRequestError) throw unexpectedRequestError
  await sleep(500)
}

function sendCompletedStream(res: http.ServerResponse): void {
  res.write('event: response.created\n')
  res.write('data: {"type":"response.created","response":{"id":"resp_mock","status":"in_progress"}}\n\n')
  res.write('event: response.completed\n')
  res.write('data: {"type":"response.completed","response":{"id":"resp_mock","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}\n\n')
  res.end()
}

function sendOpenStreamWithoutTerminal(res: http.ServerResponse): void {
  res.write('event: response.created\n')
  res.write('data: {"type":"response.created","response":{"id":"resp_open","object":"response","status":"in_progress","output":[]}}\n\n')
  res.write('event: response.output_item.added\n')
  res.write('data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_open","type":"custom_tool_call","status":"in_progress","name":"apply_patch","call_id":"call_open","input":""}}\n\n')
  const interval = setInterval(() => {
    if (res.destroyed || res.writableEnded) {
      clearInterval(interval)
      return
    }
    res.write('event: response.custom_tool_call_input.delta\n')
    res.write('data: {"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_open","call_id":"call_open","delta":"{}"}\n\n')
  }, 25)
  res.once('close', () => clearInterval(interval))
}

function sendMissingTerminalStream(res: http.ServerResponse): void {
  res.write('event: response.created\n')
  res.write('data: {"type":"response.created","response":{"id":"resp_missing_terminal","status":"in_progress"}}\n\n')
  res.end()
}

function sendFailedStream(res: http.ServerResponse, code: string, message: string): void {
  res.write('event: response.failed\n')
  res.write(`data: ${JSON.stringify({
    type: 'response.failed',
    response: {
      id: 'resp_failed',
      status: 'failed',
      error: { code, message }
    }
  })}\n\n`)
  res.end()
}

function assertUsageRecords(seeded: SeededGateway): void {
  usageRecordQueue.flushAllUsageRecordQueue()
  const records = allUsageRecordsForRegression()
  const failedRecords = records.filter((record) => record.accountId === seeded.failedAccountId && record.success === false)
  const successRecords = records.filter((record) => record.accountId === seeded.freshAccountId && record.success === true)
  assert(failedRecords.length >= 1, `应记录首选账号失败，实际 ${failedRecords.length}`)
  assert(successRecords.length >= 1, `应记录备用账号成功，实际 ${successRecords.length}`)
  assert(failedRecords.some((record) => record.errorCode === 'upstream_retryable_error'), '输出前可切换失败记录应保存网关稳定可重试错误码')
  assert.equal(failedRecords.some((record) => record.errorCode === 'internal_server_error'), false, '使用记录不得把供应商自造错误码提升为网关失败分类')
}

function allUsageRecordsForRegression() {
  const records: UsageRecordSummary[] = []
  for (let page = 1; page <= 20; page += 1) {
    const result = repositories.listUsageRecords(undefined, { page, pageSize: 500 })
    records.push(...requireUsageRecordDetails(repositories, result.items))
    if (!result.hasMore) return records
  }
  assert.fail('Codex turn 回归使用记录超过 10000 条，测试夹具可能出现无界重试')
}

function assertAccountsStillActive(gateways: SeededGateway[]): void {
  const accountsBySystemAccountId = new Map<string, ReturnType<typeof repositories.listAccounts>>()
  for (const gateway of gateways) {
    const accounts = accountsBySystemAccountId.get(gateway.systemAccountId) ?? repositories.listAccounts({
      systemAccountId: gateway.systemAccountId,
      role: 'user'
    })
    accountsBySystemAccountId.set(gateway.systemAccountId, accounts)
    for (const accountId of [gateway.failedAccountId, gateway.freshAccountId]) {
      const account = accounts.find((item) => item.id === accountId)
      assert.equal(account?.status, 'active', `账号 ${accountId} 不应被 turn 级策略改成非 active`)
    }
  }
}

function rememberVisibleCodexTurnFailures(
  seeded: Pick<SeededGateway, 'systemAccountId' | 'apiKeyId' | 'groupId'>,
  turnId: string,
  accountIds: string[]
): void {
  const headers = {
    accept: 'text/event-stream',
    'content-type': 'application/json',
    'x-codex-turn-metadata': JSON.stringify({
      turn_id: turnId,
      session_id: `session-${turnId}`,
      thread_id: `thread-${turnId}`
    })
  }
  const strategy = clientStrategies.resolveOpenAIGatewayClientStrategy({
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers,
    header(name: string): string | undefined {
      return headers[name.toLowerCase() as keyof typeof headers]
    },
    get(name: string): string | undefined {
      return headers[name.toLowerCase() as keyof typeof headers]
    }
  } as unknown as Request, {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId,
    endpoint: 'POST /v1/responses',
    clientIp: '127.0.0.1'
  })
  assert(strategy.codexTurn?.stateKey, 'Codex turn 测试必须解析出与正式请求相同的 state key')
  for (const accountId of accountIds) {
    codexTurnRetry.rememberCodexTurnStreamFailure(strategy, accountId, {
      errorCode: 'upstream_retryable_error',
      message: '上游流式响应在输出前失败，请重试'
    })
  }
}

function hitCount(state: MockUpstreamState, upstreamKey: string): number {
  return state.responseHitsByUpstreamKey[upstreamKey] ?? 0
}

function testProbeHitCount(state: MockUpstreamState, upstreamKey: string): number {
  return state.testProbeHitsByUpstreamKey[upstreamKey] ?? 0
}

function bearerToken(value: string | string[] | undefined): string {
  const raw = Array.isArray(value) ? value[0] : value
  const match = raw?.match(/^Bearer\s+(.+)$/i)
  return match?.[1] ?? ''
}

function parseJsonObject(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function isAccountTestProbeBody(body: Record<string, unknown>): boolean {
  return jsonValueContainsString(body.input, '只输出 OK') || jsonValueContainsString(body.messages, '只输出 OK')
}

function jsonValueContainsString(value: unknown, needle: string): boolean {
  if (value === needle) {
    return true
  }
  if (typeof value === 'string') {
    return value.includes(needle)
  }
  if (Array.isArray(value)) {
    return value.some((item) => jsonValueContainsString(item, needle))
  }
  if (typeof value === 'object' && value !== null) {
    return Object.values(value).some((item) => jsonValueContainsString(item, needle))
  }
  return false
}

function resolveMockScenario(body: Record<string, unknown>): string {
  const knownScenarios = [
    'client-abort-before-upstream-headers',
    'client-abort-before-terminal',
    'after-client-abort',
    'codex-turn-direct-formal-success',
    'codex-http-non2xx-all-fail',
    'codex-missing-terminal-switch',
    'context-window-error',
    'cyber-policy-error',
    'cyber-policy-after-output-error'
  ]
  for (const scenario of knownScenarios) {
    if (jsonValueContainsString(body, scenario)) {
      return scenario
    }
  }
  return typeof body.input === 'string' ? body.input : 'unknown'
}

function activateFixtureAccount(account: ReturnType<typeof repositories.createAccount>): void {
  repositories.projectAccountHealthFixtureSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    expectedConfigRevision: account.configRevision
  })
}

function seedGatewayAccess(): { systemAccountId: string; role: 'user' } {
  if (!seedOwnerAccess) {
    const owner = repositories.createSystemAccount({
      username: 'codex_turn_switch_owner',
      displayName: 'CodexTurn切号回归用户',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    })
    seedOwnerAccess = { systemAccountId: owner.id, role: 'user' }
  }
  return seedOwnerAccess
}

function createSessionRequest(sessionId: string): Request {
  const headers: Record<string, string> = { 'session-id': sessionId }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    headers,
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    },
    body: {}
  } as Request
}

function createSessionIdentity(sessionId: string, seeded: Pick<SeededGateway, 'systemAccountId' | 'apiKeyId'>) {
  return resolveGatewaySessionIdentity(createSessionRequest(sessionId), {
    clientProfile: 'codex',
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId
  })
}

function sessionAffinityProviderProfilePool(
  seeded: Pick<SeededGateway, 'systemAccountId' | 'groupId'>
): string {
  const profileIds = [...new Set(
    repositories.listOpenAIAccountsForGroup(seeded.groupId, seeded.systemAccountId)
      .map((account) => account.providerProtocolProfileId?.trim() || account.providerCode.trim())
  )].sort()
  return `pool:${profileIds.join(',') || 'empty'}`
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return Promise.resolve()
  return new Promise((resolveClose) => {
    let settled = false
    let forceTimer: ReturnType<typeof setTimeout> | undefined
    const finish = () => {
      if (settled) return
      settled = true
      if (forceTimer) clearTimeout(forceTimer)
      resolveClose()
    }
    server.close(finish)
    server.closeIdleConnections?.()
    forceTimer = setTimeout(() => {
      server.closeAllConnections?.()
      setTimeout(finish, 500)
    }, 1000)
  })
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) {
        throw error
      }
      await delay(200)
    }
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms))
}

async function waitUntil(predicate: () => boolean, timeoutMs: number, message: string): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) {
      return
    }
    await sleep(10)
  }
  assert(predicate(), message)
}

function waitForClockTick(): void {
  const startedAt = Date.now()
  while (Date.now() === startedAt) {
  }
}

function forceGroupAccountOrder(groupId: string, primaryAccountId: string, stickyAccountId: string): void {
  const statement = databaseModule.getBusinessDatabase().prepare('UPDATE group_accounts SET created_at = ? WHERE group_id = ? AND account_id = ?')
  statement.run('2000-01-01T00:00:00.000Z', groupId, primaryAccountId)
  statement.run('2000-01-01T00:00:01.000Z', groupId, stickyAccountId)
}

function assertCodexAccountScopedGuidanceIsNotClientRetryable(): void {
  const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
  const retryPredicate = sourceFunctionBlock(routesSource, 'function shouldSendDispatchExhaustedProtocolRetry')
  assert(
    retryPredicate.includes('!error.agentGuidanceResponse'),
    '账号范围的协议能力提示不能进入客户端可重试分支，否则客户端会反复重试同一路径'
  )
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextFunction = source.indexOf('\nfunction ', start + marker.length)
  return source.slice(start, nextFunction === -1 ? undefined : nextFunction)
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
