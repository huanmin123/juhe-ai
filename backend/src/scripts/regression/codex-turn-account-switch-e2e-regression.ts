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
runtimeConfig.processRole = 'worker'
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
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/gateway/openai-gateway-request-body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/openai-gateway-codex-turn-retry.service.js'),
  import('../../modules/gateway/openai-gateway-session-affinity.service.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  failedAccountId: string
  freshAccountId: string
  apiKeyId: string
  failedUpstreamKey: string
  freshUpstreamKey: string
}

interface MockUpstreamState {
  hitsByUpstreamKey: Record<string, number>
  requests: Array<{
    upstreamKey: string
    scenario: string
    turnMetadata?: string
  }>
}

let sequence = 0

async function main(): Promise<void> {
  let gatewayServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamState: MockUpstreamState = {
    hitsByUpstreamKey: {},
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
    const nonCodex = seedTwoAccountGateway(upstreamBaseUrl, 'non-codex')
    const contextWindow = seedTwoAccountGateway(upstreamBaseUrl, 'context-window')
    const cyberPolicy = seedTwoAccountGateway(upstreamBaseUrl, 'cyber-policy')
    const clientAbortAffinity = seedTwoAccountGateway(upstreamBaseUrl, 'client-abort-affinity', { freshPriority: 0 })

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertCodexFourthRequestSwitchesAccount(baseUrl, codexSwitch, upstreamState)
    await assertGenericClientStreamInterceptDoesNotSwitchAccount(baseUrl, nonCodex, upstreamState)
    await assertCodexContextWindowSwitchesAccount(baseUrl, contextWindow, upstreamState)
    await assertCodexCyberPolicySwitchesAccount(baseUrl, cyberPolicy, upstreamState)
    await assertClientAbortClearsSessionAffinity(baseUrl, clientAbortAffinity, upstreamState)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecords(codexSwitch)
    assertAccountsStillActive([codexSwitch, nonCodex, contextWindow, cyberPolicy, clientAbortAffinity])

    console.log('Codex turn 切号 e2e 回归通过：临时库假账号、mock 上游、3 次失败后第 4 次避让、普通客户端统一流式拦截但不触发 Codex turn 切号、context_length_exceeded 和 cyber_policy 均可重试并切号，client_aborted 会释放会话亲和，符合预期')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function assertCodexFourthRequestSwitchesAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  for (let index = 1; index <= 3; index += 1) {
    const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
      scenario: 'codex-retry-switch',
      turnId: 'turn-codex-switch',
      codex: true
    })
    assert(streamText.includes('upstream_retryable_error'), `第 ${index} 次 Codex 失败应触发可重试错误：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, '前 3 次应都命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, '第 4 次前不应提前命中备用账号')

  const fourthText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'codex-retry-switch',
    turnId: 'turn-codex-switch',
    codex: true
  })
  assert(fourthText.includes('response.completed'), `第 4 次应切到备用账号并完成：${fourthText}`)
  assert(!fourthText.includes('response.failed'), `第 4 次不应继续失败：${fourthText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, '第 4 次不应继续请求已失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, '第 4 次应命中备用账号')
}

async function assertGenericClientStreamInterceptDoesNotSwitchAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  for (let index = 1; index <= 4; index += 1) {
    const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
      scenario: 'non-codex-retry-switch',
      turnId: `non-codex-${index}`,
      codex: false
    })
    assert(streamText.includes('stream_intercepted'), `普通客户端第 ${index} 次应按统一流式拦截返回普通失败码：${streamText}`)
    assert(!streamText.includes('upstream_retryable_error'), `非 Codex 第 ${index} 次不应伪造可重试错误：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 4, '普通客户端不应触发 Codex turn 级避让')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, '普通客户端不应命中 Codex 备用账号')
}

async function assertCodexContextWindowSwitchesAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  for (let index = 1; index <= 3; index += 1) {
    const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
      scenario: 'context-window-error',
      turnId: 'turn-context-window',
      codex: true
    })
    assert(streamText.includes('upstream_retryable_error'), `第 ${index} 次 context_length_exceeded 应改写为可重试错误：${streamText}`)
    assert(!streamText.includes('context_length_exceeded'), `第 ${index} 次 context_length_exceeded 不应透出原始错误码：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, 'context_length_exceeded 前 3 次应都命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, 'context_length_exceeded 第 4 次前不应提前命中备用账号')

  const fourthText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'context-window-error',
    turnId: 'turn-context-window',
    codex: true
  })
  assert(fourthText.includes('response.completed'), `context_length_exceeded 第 4 次应切到备用账号并完成：${fourthText}`)
  assert(!fourthText.includes('response.failed'), `context_length_exceeded 第 4 次不应继续失败：${fourthText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, 'context_length_exceeded 第 4 次不应继续请求已失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'context_length_exceeded 第 4 次应命中备用账号')
}

async function assertCodexCyberPolicySwitchesAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  for (let index = 1; index <= 3; index += 1) {
    const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
      scenario: 'cyber-policy-after-output-error',
      turnId: 'turn-cyber-policy',
      codex: true
    })
    assert(streamText.includes('upstream_retryable_error'), `第 ${index} 次 cyber_policy 失败应改写为可重试错误：${streamText}`)
    assert(!streamText.includes('partial output'), `第 ${index} 次同批次 cyber_policy 失败不应泄露尚未写入下游的输出：${streamText}`)
    assert(!streamText.includes('cyber_policy'), `第 ${index} 次 cyber_policy 失败不应透出原始错误码：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, 'cyber_policy 前 3 次应都命中首选失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, 'cyber_policy 第 4 次前不应提前命中备用账号')

  const fourthText = await requestResponsesStream(baseUrl, seeded.apiKey, {
    scenario: 'cyber-policy-after-output-error',
    turnId: 'turn-cyber-policy',
    codex: true
  })
  assert(fourthText.includes('response.completed'), `cyber_policy 第 4 次应切到备用账号并完成：${fourthText}`)
  assert(!fourthText.includes('response.failed'), `cyber_policy 第 4 次不应继续失败：${fourthText}`)
  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 3, 'cyber_policy 第 4 次不应继续请求已失败账号')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 1, 'cyber_policy 第 4 次应命中备用账号')
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
    systemAccountId: 'sys_admin',
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
  })
  assert(sessionKey, '测试应能生成会话亲和 key')
  sessionAffinity.rememberOpenAIAccountForSession(sessionKey, seeded.freshAccountId, {
    systemAccountId: 'sys_admin',
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
    systemAccountId: 'sys_admin',
    apiKeyId: seeded.apiKeyId,
    groupId: seeded.groupId
  })
  assert(headerDelaySessionKey, '测试应能生成响应头前断开场景的会话亲和 key')
  sessionAffinity.rememberOpenAIAccountForSession(headerDelaySessionKey, seeded.freshAccountId, {
    systemAccountId: 'sys_admin',
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
  const group = repositories.createGroup({
    name: `Codex 切号 e2e 分组-${label}`,
    providerCode: 'openai',
    enabled: true
  })
  const failedUpstreamKey = `sk-codex-switch-${sequence}-failed`
  const freshUpstreamKey = `sk-codex-switch-${sequence}-fresh`
  const failedAccount = repositories.createAccount({
    providerCode: 'openai',
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
  })
  if ((options.freshPriority ?? 10) === 0) {
    waitForClockTick()
  }
  const freshAccount = repositories.createAccount({
    providerCode: 'openai',
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
  })
  if ((options.freshPriority ?? 10) === 0) {
    forceGroupAccountOrder(group.id, failedAccount.id, freshAccount.id)
  }
  const apiKey = repositories.createApiKeyRecord({
    name: `Codex 切号 e2e Key-${label}`,
    groupId: group.id,
    status: 'active'
  })
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    failedAccountId: failedAccount.id,
    freshAccountId: freshAccount.id,
    apiKeyId: apiKey.id,
    failedUpstreamKey,
    freshUpstreamKey
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
      state.hitsByUpstreamKey[upstreamKey] = hitCount(state, upstreamKey) + 1

      if (url.pathname !== '/v1/responses') {
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ object: 'list', data: [] }))
        return
      }

      const body = parseJsonObject(Buffer.concat(bodyChunks).toString('utf8'))
      const scenario = typeof body.input === 'string' ? body.input : 'unknown'
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
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      model: 'gpt-5.3-codex',
      input: input.scenario,
      stream: true
    }),
    signal: controller.signal
  })
  assert.equal(response.status, 200)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const reader = response.body?.getReader()
  assert(reader, '流式响应应有可读取 body')
  const firstChunk = await reader.read()
  assert.equal(firstChunk.done, false, '测试请求应先收到首段流式数据再关闭下游连接')
  controller.abort()
  try {
    await reader.cancel()
  } catch {
  }
  await sleep(500)
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
  assert(failedRecords.length >= 3, `应记录首选账号 3 次失败，实际 ${failedRecords.length}`)
  assert(successRecords.length >= 1, `应记录备用账号成功，实际 ${successRecords.length}`)
  assert(failedRecords.some((record) => record.errorCode === 'internal_server_error'), '失败记录应保留上游原始错误码')
}

function assertAccountsStillActive(gateways: SeededGateway[]): void {
  const accounts = repositories.listAccounts()
  for (const gateway of gateways) {
    for (const accountId of [gateway.failedAccountId, gateway.freshAccountId]) {
      const account = accounts.find((item) => item.id === accountId)
      assert.equal(account?.status, 'active', `账号 ${accountId} 不应被 turn 级策略改成非 active`)
    }
  }
}

function hitCount(state: MockUpstreamState, upstreamKey: string): number {
  return state.hitsByUpstreamKey[upstreamKey] ?? 0
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
  const statement = databaseModule.getDatabase().prepare('UPDATE group_accounts SET created_at = ? WHERE group_id = ? AND account_id = ?')
  statement.run('2000-01-01T00:00:00.000Z', groupId, primaryAccountId)
  statement.run('2000-01-01T00:00:01.000Z', groupId, stickyAccountId)
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
