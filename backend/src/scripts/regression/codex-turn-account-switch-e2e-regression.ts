import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-turn-switch-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'codex-turn-switch.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'codex-turn-switch-records.sqlite3')
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
  codexTurnRetry
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
  import('../../modules/gateway/openai-gateway-codex-turn-retry.service.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  failedAccountId: string
  freshAccountId: string
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
    const terminal = seedTwoAccountGateway(upstreamBaseUrl, 'terminal-error')

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertCodexFourthRequestSwitchesAccount(baseUrl, codexSwitch, upstreamState)
    await assertNonCodexDoesNotSwitchAccount(baseUrl, nonCodex, upstreamState)
    await assertCodexTerminalErrorDoesNotSwitchAccount(baseUrl, terminal, upstreamState)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecords(codexSwitch)
    assertAccountsStillActive([codexSwitch, nonCodex, terminal])

    console.log('Codex turn 切号 e2e 回归通过：临时库假账号、mock 上游、3 次失败后第 4 次避让、非 Codex 不切号、Codex 终止类错误不切号均符合预期')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.getRecordDatabase().close()
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

async function assertNonCodexDoesNotSwitchAccount(
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
    assert(streamText.includes('internal_server_error'), `非 Codex 第 ${index} 次应保留原始错误码：${streamText}`)
    assert(!streamText.includes('upstream_retryable_error'), `非 Codex 第 ${index} 次不应伪造可重试错误：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 4, '非 Codex 不应触发 turn 级避让')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, '非 Codex 不应命中备用账号')
}

async function assertCodexTerminalErrorDoesNotSwitchAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const beforeFailedHits = hitCount(upstreamState, seeded.failedUpstreamKey)
  const beforeFreshHits = hitCount(upstreamState, seeded.freshUpstreamKey)

  for (let index = 1; index <= 4; index += 1) {
    const streamText = await requestResponsesStream(baseUrl, seeded.apiKey, {
      scenario: 'codex-terminal-error',
      turnId: 'turn-terminal-error',
      codex: true
    })
    assert(streamText.includes('context_length_exceeded'), `Codex 终止类错误第 ${index} 次应保留原始错误码：${streamText}`)
    assert(!streamText.includes('upstream_retryable_error'), `Codex 终止类错误第 ${index} 次不应伪造成可重试：${streamText}`)
  }

  assert.equal(hitCount(upstreamState, seeded.failedUpstreamKey) - beforeFailedHits, 4, 'Codex 终止类错误不应进入 turn 级切号计数')
  assert.equal(hitCount(upstreamState, seeded.freshUpstreamKey) - beforeFreshHits, 0, 'Codex 终止类错误不应命中备用账号')
}

function seedTwoAccountGateway(upstreamBaseUrl: string, label: string): SeededGateway {
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
    name: `Codex 切号 e2e 失败账号-${label}`,
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
  const freshAccount = repositories.createAccount({
    providerCode: 'openai',
    name: `Codex 切号 e2e 备用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: freshUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  })
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

      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache',
        connection: 'keep-alive'
      })
      if (upstreamKey.endsWith('-fresh')) {
        sendCompletedStream(res)
        return
      }
      if (scenario === 'codex-terminal-error') {
        sendFailedStream(res, 'context_length_exceeded', 'Your input exceeds the context window of this model.')
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

function sendCompletedStream(res: http.ServerResponse): void {
  res.write('event: response.created\n')
  res.write('data: {"type":"response.created","response":{"id":"resp_mock","status":"in_progress"}}\n\n')
  res.write('event: response.completed\n')
  res.write('data: {"type":"response.completed","response":{"id":"resp_mock","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}\n\n')
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

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
