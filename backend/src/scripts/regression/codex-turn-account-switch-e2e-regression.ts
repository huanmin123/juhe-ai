import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-turn-switch-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'codex-turn-switch.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'codex-turn-switch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

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
  codexTurnRetry,
  sessionAffinity
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
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/client-profiles/codex-turn-retry.service.js'),
  import('../../modules/gateway/runtime/session-affinity.service.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  systemAccountId: string
  failedAccountId: string
  freshAccountId: string
  apiKeyId: string
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
    codexTurnRetry.clearCodexTurnRetryStateForTest()
    settingsRepository.updateSettings({
      streamCircuitBreakerEnabled: true,
      streamRequestTimeoutSeconds: 10,
      streamIdleTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const codexSwitch = seedTwoAccountGateway(upstreamBaseUrl, 'codex-switch')
    const latentCodexSwitch = seedThreeAccountGateway(upstreamBaseUrl, 'codex-latent-switch')
    const probeFailCodexSwitch = seedProbeFailureGateway(upstreamBaseUrl, 'codex-probe-fail')
    const nonCodex = seedTwoAccountGateway(upstreamBaseUrl, 'non-codex')
    const nonCodexAllFail = seedProbeFailureGateway(upstreamBaseUrl, 'non-codex-all-fail')
    const contextWindow = seedTwoAccountGateway(upstreamBaseUrl, 'context-window')
    const cyberPolicy = seedTwoAccountGateway(upstreamBaseUrl, 'cyber-policy')
    const clientAbortAffinity = seedTwoAccountGateway(upstreamBaseUrl, 'client-abort-affinity', { freshPriority: 0 })

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertCodexPreCommitFailureSwitchesAccountOnServer(baseUrl, codexSwitch, upstreamState)
    await assertCodexPreCommitFailureWalksCandidatesOnServer(baseUrl, latentCodexSwitch, upstreamState)
    await assertCodexPreCommitFailureReturnsRetryableWhenAllCandidatesFail(baseUrl, probeFailCodexSwitch, upstreamState)
    await assertGenericPreCommitFailureSwitchesAccountOnServer(baseUrl, nonCodex, upstreamState)
    await assertGenericPreCommitFailureReturnsOrdinaryFailureWhenAllCandidatesFail(baseUrl, nonCodexAllFail, upstreamState)
    await assertCodexContextWindowSingleRequestSwitchesAccountOnServer(baseUrl, contextWindow, upstreamState)
    await assertCodexCyberPolicySingleRequestSwitchesAccountOnServer(baseUrl, cyberPolicy, upstreamState)
    await assertClientAbortClearsSessionAffinity(baseUrl, clientAbortAffinity, upstreamState)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecords(codexSwitch)
    assertAccountsStillActive([codexSwitch, latentCodexSwitch, nonCodex, contextWindow, cyberPolicy, clientAbortAffinity, {
      ...probeFailCodexSwitch,
      freshAccountId: probeFailCodexSwitch.probeFailedAccountId,
      freshUpstreamKey: probeFailCodexSwitch.probeFailedUpstreamKey
    }, {
      ...nonCodexAllFail,
      freshAccountId: nonCodexAllFail.probeFailedAccountId,
      freshUpstreamKey: nonCodexAllFail.probeFailedUpstreamKey
    }])

    console.log('Codex turn 切号 e2e 回归通过：临时库假账号、mock 上游、Codex/普通客户端输出前流失败均由服务端优先隐藏切号，账号耗尽时仅 Codex 返回客户端可重试 SSE，context_length_exceeded 和 cyber_policy 均可服务端切号，client_aborted 会释放会话亲和，符合预期')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
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

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-retry-switch',
    turnId: 'turn-codex-switch',
    codex: true,
    retryTag: 'server-retry'
  })
  assert(streamText.includes('response.completed'), `Codex 输出前失败应由服务端切到备用账号并完成：${streamText}`)
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

async function assertGenericPreCommitFailureSwitchesAccountOnServer(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'non-codex-retry-switch',
    turnId: 'non-codex-server-retry',
    codex: false
  })
  assert(streamText.includes('response.completed'), `普通客户端输出前失败也应由服务端隐藏切号完成：${streamText}`)
  assert(!streamText.includes('response.failed'), `普通客户端服务端切号成功时不应收到中间失败：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `普通客户端不应伪造 Codex 客户端专用可重试错误：${streamText}`)
  assert(!streamText.includes('internal_server_error'), `普通客户端服务端切号成功时不应透出首个账号错误码：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, '普通客户端本次请求应先命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, '普通客户端本次请求应隐藏切到备用账号')
}

async function assertGenericPreCommitFailureReturnsOrdinaryFailureWhenAllCandidatesFail(
  baseUrl: string,
  seeded: SeededProbeFailureGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeProbeFailedHits = hitCount(upstreamState, seeded.probeFailedUpstreamKey)
  const beforeProbeHits = testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-missing-terminal-switch',
    turnId: 'non-codex-all-fail',
    codex: false,
    retryTag: 'server-retry-exhausted'
  })
  assert(streamText.includes('response.failed'), `普通客户端全部账号耗尽时应返回普通 SSE 失败事件：${streamText}`)
  assert(streamText.includes('upstream_stream_interrupted'), `普通客户端全部账号耗尽时应保留普通流失败码：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `普通客户端全部账号耗尽时不应返回 Codex 专用可重试错误：${streamText}`)
  assert(!streamText.includes('response.completed'), `普通客户端全部账号耗尽不应伪成功：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, '普通客户端全部失败场景应先命中首选死号')
  assert.equal(hitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeFailedHits, 1, '普通客户端全部失败场景应隐藏重试唯一备用号')
  assert.equal(testProbeHitCount(upstreamState, seeded.probeFailedUpstreamKey) - beforeProbeHits, 0, '普通客户端不应消耗 Codex turn 探针')
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

async function assertCodexCyberPolicySingleRequestSwitchesAccountOnServer(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const beforeFreshProbeHits = testProbeHitCount(upstreamState, seeded.freshUpstreamKey)

  const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'cyber-policy-after-output-error',
    turnId: 'turn-cyber-policy',
    codex: true,
    retryTag: 'server-retry'
  })
  assert(streamText.includes('response.completed'), `cyber_policy 应由服务端切到备用账号并完成：${streamText}`)
  assert(!streamText.includes('response.failed'), `cyber_policy 服务端切号成功时不应把中间失败交给客户端：${streamText}`)
  assert(!streamText.includes('upstream_retryable_error'), `cyber_policy 服务端切号成功时不应消耗客户端重试次数：${streamText}`)
  assert(!streamText.includes('partial output'), `cyber_policy 服务端切号成功时不应泄露尚未确认的同批次输出：${streamText}`)
  assert(!streamText.includes('cyber_policy'), `cyber_policy 服务端切号成功时不应透出原始错误码：${streamText}`)

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 1, 'cyber_policy 本次请求应命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'cyber_policy 本次请求应命中备用账号')
  assert.equal(testProbeHitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshProbeHits, 0, 'cyber_policy 服务端隐藏重试不应消耗 Codex turn 探针')
}

async function assertClientAbortClearsSessionAffinity(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const sessionId = `session-client-abort-affinity-${Date.now()}`
  const beforePrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  const sessionKey = sessionAffinity.resolveOpenAIGatewaySessionAffinityKey(createSessionRequest(sessionId), {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
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
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforePrimaryHits, 0, 'client_aborted 请求不应先走正常顺序主账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeStickyHits, 1, 'client_aborted 请求应命中已绑定的备用账号')

  const beforeRetryPrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeRetryStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const retryText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'after-client-abort',
    turnId: 'turn-client-abort-affinity',
    codex: true,
    sessionId
  })
  assert(retryText.includes('response.completed'), `client_aborted 后下一次请求应完成：${retryText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeRetryPrimaryHits, 1, 'client_aborted 后应释放会话亲和并回到正常账号顺序')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeRetryStickyHits, 0, 'client_aborted 后不应继续粘住已断开的备用账号')

  const headerDelaySessionId = `session-client-abort-before-headers-${Date.now()}`
  const headerDelaySessionKey = sessionAffinity.resolveOpenAIGatewaySessionAffinityKey(createSessionRequest(headerDelaySessionId), {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
  })
  assert(headerDelaySessionKey, '测试应能生成响应头前断开场景的会话亲和 key')
  sessionAffinity.rememberOpenAIAccountForSession(headerDelaySessionKey, seeded.freshAccountId, {
    systemAccountId: seeded.systemAccountId,
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
  })
  const beforeHeaderAbortPrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeHeaderAbortStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  await requestResponsesStreamAndAbortAfterUpstreamRequestStarted(baseUrl, seeded.apiKey, {
    scenario: 'client-abort-before-upstream-headers',
    turnId: 'turn-client-abort-before-headers',
    codex: true,
    sessionId: headerDelaySessionId
  }, () => hitCount(upstreamState, seeded.freshUpstreamKey) - beforeHeaderAbortStickyHits >= 1)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeHeaderAbortPrimaryHits, 0, '响应头前 client_aborted 请求不应先走正常顺序主账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeHeaderAbortStickyHits, 1, '响应头前 client_aborted 请求应命中已绑定的备用账号')

  const beforeHeaderAbortRetryPrimaryHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeHeaderAbortRetryStickyHits = hitCount(upstreamState, seeded.freshUpstreamKey)
  const headerAbortRetryText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'after-client-abort',
    turnId: 'turn-client-abort-before-headers',
    codex: true,
    sessionId: headerDelaySessionId
  })
  assert(headerAbortRetryText.includes('response.completed'), `响应头前 client_aborted 后下一次请求应完成：${headerAbortRetryText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeHeaderAbortRetryPrimaryHits, 1, '响应头前 client_aborted 后应释放会话亲和并回到正常账号顺序')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeHeaderAbortRetryStickyHits, 0, '响应头前 client_aborted 后不应继续粘住已断开的备用账号')

  usageRecordQueue.flushAllUsageRecordQueue()
  const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 200 }).items
  assert(
    records.some((record) => record.accountId === seeded.freshAccountId && record.errorCode === 'client_aborted' && record.errorMessage === '下游连接提前关闭'),
    'client_aborted 使用记录应使用“下游连接提前关闭”文案，避免误导为用户手动取消'
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
    name: `A-Codex 切号 e2e 失败账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  if ((options.freshPriority ?? 10) === 0) {
    waitForClockTick()
  }
  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `B-Codex 切号 e2e 备用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: freshUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: options.freshPriority ?? 10
  }, access)
  if ((options.freshPriority ?? 10) === 0) {
    forceGroupAccountOrder(group.id, failedAccount.id, freshAccount.id)
  }
  const apiKey = repositories.createApiKeyRecord({
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
    failedUpstreamKey,
    freshUpstreamKey
  }
}

function seedThreeAccountGateway(upstreamBaseUrl: string, label: string): SeededThreeAccountGateway {
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
    name: `A-Codex 切号 e2e 死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const latentFailedAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `B-Codex 切号 e2e 假正常死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: latentFailedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 5
  }, access)
  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `C-Codex 切号 e2e 真可用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: freshUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
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
    name: `A-Codex 切号 e2e 死号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: failedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const probeFailedAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `B-Codex 切号 e2e 探针失败账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: probeFailedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
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

      if (url.pathname !== '/v1/responses') {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { code: 'mock_unexpected_path', message: `unexpected path ${url.pathname}` } }))
        return
      }

      if (isAccountTestProbeBody(body)) {
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
    headers['x-session-id'] = input.sessionId
  }
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model: 'gpt-5.3-codex',
      input: input.scenario,
      metadata: input.retryTag ? { retry_tag: input.retryTag } : undefined,
      stream: true
    })
  })
  assert.equal(response.status, 200)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  return response.text()
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
    model: 'gpt-5.3-codex',
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
    headers['x-session-id'] = input.sessionId
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
  const controller = new AbortController()
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
    headers['x-session-id'] = input.sessionId
  }
  const request = fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model: 'gpt-5.3-codex',
      input: input.scenario,
      stream: true
    }),
    signal: controller.signal
  }).then(async (response) => {
    await response.arrayBuffer()
  }).catch((error) => {
    if (!controller.signal.aborted) {
      throw error
    }
  })
  await waitUntil(upstreamRequestStarted, 1000, '响应头前断开测试应先命中上游账号')
  controller.abort()
  await request
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
  res.write('data: {"type":"response.created","response":{"id":"resp_open","status":"in_progress"}}\n\n')
  res.write('event: response.output_item.added\n')
  res.write('data: {"type":"response.output_item.added","item":{"id":"item_open","type":"custom_tool_call","status":"in_progress"}}\n\n')
  const interval = setInterval(() => {
    if (res.destroyed || res.writableEnded) {
      clearInterval(interval)
      return
    }
    res.write('event: response.custom_tool_call_input.delta\n')
    res.write('data: {"type":"response.custom_tool_call_input.delta","delta":"{}"}\n\n')
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
  const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 100 }).items
  const failedRecords = records.filter((record) => record.accountId === seeded.failedAccountId && record.success === false)
  const successRecords = records.filter((record) => record.accountId === seeded.freshAccountId && record.success === true)
  assert(failedRecords.length >= 1, `应记录首选账号失败，实际 ${failedRecords.length}`)
  assert(successRecords.length >= 1, `应记录备用账号成功，实际 ${successRecords.length}`)
  assert(failedRecords.some((record) => record.errorCode === 'internal_server_error'), '失败记录应保留上游原始错误码')
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
  return body.instructions === 'You are ChatGPT, a helpful assistant.'
    && (jsonValueContainsString(body.input, '只输出 OK') || jsonValueContainsString(body.messages, '只输出 OK'))
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
  return {
    header(name: string) {
      return name.toLowerCase() === 'x-session-id' ? sessionId : undefined
    },
    body: {}
  } as Request
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

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return Promise.resolve()
  return new Promise((resolveClose) => {
    server.close(() => resolveClose())
  })
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

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
