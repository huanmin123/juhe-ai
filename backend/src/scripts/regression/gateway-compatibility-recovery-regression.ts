import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-compatibility-recovery-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-compatibility-recovery.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-compatibility-recovery-secret'
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
  gatewayJsonParser
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
  import('../../modules/gateway/request/json-parser.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  primaryAccountId: string
  fallbackAccountId: string
  primaryUpstreamKey: string
  fallbackUpstreamKey: string
}

interface MockUpstreamRequest {
  upstreamKey: string
  scenario: string
  body: Record<string, unknown>
  rawBodyBytes: number
}

interface MockUpstreamState {
  requests: MockUpstreamRequest[]
}

let sequence = 0
let seedOwnerAccess: { systemAccountId: string; role: 'user' } | undefined

async function main(): Promise<void> {
  let gatewayServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamState: MockUpstreamState = { requests: [] }

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      defaultTemporaryUnschedulableMinutes: 5
    })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const cleanupSuccess = seedTwoAccountGateway(upstreamBaseUrl, 'cleanup-success')
    const cleanupObjectInput = seedTwoAccountGateway(upstreamBaseUrl, 'cleanup-object-input')
    const functionOutput = seedTwoAccountGateway(upstreamBaseUrl, 'function-output')
    const fallback = seedTwoAccountGateway(upstreamBaseUrl, 'fallback-after-cleanup-failed')
    const noEncrypted = seedTwoAccountGateway(upstreamBaseUrl, 'no-encrypted-default-retry')

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertEncryptedReasoningCleanupSucceedsOnSameAccount(baseUrl, cleanupSuccess, upstreamState)
    await assertEncryptedReasoningObjectInputDropsEmptyReasoning(baseUrl, cleanupObjectInput, upstreamState)
    await assertFunctionCallOutputKeepsPreviousResponseId(baseUrl, functionOutput, upstreamState)
    await assertCleanupFailureFallsBackWithOriginalBody(baseUrl, fallback, upstreamState)
    await assertNoEncryptedReasoningUsesDefaultRetry(baseUrl, noEncrypted, upstreamState)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertAccountsStillActive([cleanupSuccess, cleanupObjectInput, functionOutput, fallback, noEncrypted])

    console.log('网关兼容策略请求恢复回归通过：mock AI 覆盖 encrypted reasoning 大请求清洗重试、function_call_output 边界、清洗失败后 fallback 和无可恢复体默认重试')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await gatewayJsonParser.stopGatewayJsonParseWorker()
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

async function assertEncryptedReasoningCleanupSucceedsOnSameAccount(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const before = upstreamState.requests.length
  const responseText = await requestResponsesStream(baseUrl, seeded.apiKey, buildEncryptedReasoningRequest('cleanup-success', {
    largeBody: true
  }))
  assert(responseText.includes('response.completed'), `清洗重试后应返回成功 SSE：${responseText}`)

  const requests = upstreamState.requests.slice(before)
  assert.equal(requests.length, 2, '首轮失败后应只进行一次清洗重试')
  assert((requests[0]?.rawBodyBytes ?? 0) > 256 * 1024, '首轮大请求应超过 JSON worker 解析阈值')
  assert.equal(requests[0]?.upstreamKey, seeded.primaryUpstreamKey)
  assert.equal(requests[1]?.upstreamKey, seeded.primaryUpstreamKey)
  assert.equal(hasEncryptedReasoning(requests[0]?.body), true, '首轮应保留客户端 encrypted reasoning')
  assert.equal(hasPreviousResponseId(requests[0]?.body), true, '首轮应保留 previous_response_id')
  assert.equal(hasEncryptedReasoning(requests[1]?.body), false, '清洗重试应删除 encrypted reasoning')
  assert.equal(hasPreviousResponseId(requests[1]?.body), false, '非 function_call_output 场景应删除 previous_response_id')
  assert.equal(requests.some((request) => request.upstreamKey === seeded.fallbackUpstreamKey), false, '清洗成功时不应切到备用账号')
}

async function assertEncryptedReasoningObjectInputDropsEmptyReasoning(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const before = upstreamState.requests.length
  const responseText = await requestResponsesStream(baseUrl, seeded.apiKey, buildEncryptedReasoningRequest('cleanup-object-input', {
    objectInput: true
  }))
  assert(responseText.includes('response.completed'), `对象形态 input 清洗重试后应返回成功 SSE：${responseText}`)

  const requests = upstreamState.requests.slice(before)
  assert.equal(requests.length, 2, '对象形态 input 首轮失败后应只进行一次清洗重试')
  assert.equal(hasEncryptedReasoning(requests[0]?.body), true, '对象形态 input 首轮应保留 encrypted reasoning')
  assert.equal(hasEncryptedReasoning(requests[1]?.body), false, '对象形态 input 清洗重试应删除 encrypted reasoning')
  assert.deepEqual(inputItems(requests[1]?.body), [], '对象形态空 reasoning 清洗后应丢弃整个 reasoning item')
}

async function assertFunctionCallOutputKeepsPreviousResponseId(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const before = upstreamState.requests.length
  const responseText = await requestResponsesStream(baseUrl, seeded.apiKey, buildEncryptedReasoningRequest('function-output', {
    functionCallOutput: true
  }))
  assert(responseText.includes('response.completed'), `function_call_output 清洗重试后应成功：${responseText}`)

  const requests = upstreamState.requests.slice(before)
  assert.equal(requests.length, 2, 'function_call_output 场景也应只清洗重试一次')
  assert.equal(hasEncryptedReasoning(requests[1]?.body), false, 'function_call_output 场景应删除 encrypted reasoning')
  assert.equal(hasPreviousResponseId(requests[1]?.body), true, 'function_call_output 场景应保留 previous_response_id')
  assert.equal(hasFunctionCallOutput(requests[1]?.body), true, 'function_call_output 项应保留')
}

async function assertCleanupFailureFallsBackWithOriginalBody(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const before = upstreamState.requests.length
  const responseText = await requestResponsesStream(baseUrl, seeded.apiKey, buildEncryptedReasoningRequest('fallback-after-cleanup-failed'))
  assert(responseText.includes('response.completed'), `清洗失败后应 fallback 到后续账号成功：${responseText}`)

  const requests = upstreamState.requests.slice(before)
  assert.equal(requests.length, 3, '应为首选账号原始请求、首选账号清洗重试、备用账号原始请求')
  assert.equal(requests[0]?.upstreamKey, seeded.primaryUpstreamKey)
  assert.equal(requests[1]?.upstreamKey, seeded.primaryUpstreamKey)
  assert.equal(requests[2]?.upstreamKey, seeded.fallbackUpstreamKey)
  assert.equal(hasEncryptedReasoning(requests[0]?.body), true)
  assert.equal(hasEncryptedReasoning(requests[1]?.body), false)
  assert.equal(hasEncryptedReasoning(requests[2]?.body), true, 'fallback 账号应重新使用原始 body，证明 req.rawBody 未被改写')
}

async function assertNoEncryptedReasoningUsesDefaultRetry(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 1,
    temporaryUnschedulableRetryIntervalSeconds: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  try {
    const before = upstreamState.requests.length
    const responseText = await requestResponsesStream(baseUrl, seeded.apiKey, buildPlainResponsesRequest('no-encrypted-default-retry'))
    assert(responseText.includes('response.completed'), `无 encrypted reasoning 的默认重试后应成功：${responseText}`)

    const requests = upstreamState.requests.slice(before)
    assert.equal(requests.length, 2, '无可恢复 encrypted reasoning 时应回到默认同账号重试')
    assert.equal(requests[0]?.upstreamKey, seeded.primaryUpstreamKey)
    assert.equal(requests[1]?.upstreamKey, seeded.primaryUpstreamKey)
    assert.equal(hasEncryptedReasoning(requests[0]?.body), false)
    assert.equal(hasEncryptedReasoning(requests[1]?.body), false)
    assert.equal(requests.some((request) => request.upstreamKey === seeded.fallbackUpstreamKey), false, '默认同账号重试成功时不应切到备用账号')
  } finally {
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
  }
}

function seedTwoAccountGateway(upstreamBaseUrl: string, label: string): SeededGateway {
  sequence += 1
  const access = seedGatewayAccess()
  const group = repositories.createGroup({
    name: `兼容恢复 e2e 分组-${label}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const primaryUpstreamKey = `sk-compat-recovery-${sequence}-primary`
  const fallbackUpstreamKey = `sk-compat-recovery-${sequence}-fallback`
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `A-兼容恢复首选账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const fallbackAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `B-兼容恢复备用账号-${label}`,
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `兼容恢复 e2e Key-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    primaryAccountId: primaryAccount.id,
    fallbackAccountId: fallbackAccount.id,
    primaryUpstreamKey,
    fallbackUpstreamKey
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
      const rawBody = Buffer.concat(bodyChunks)
      const body = parseJsonObject(rawBody.toString('utf8'))
      const scenario = stringAtPath(body, ['metadata', 'scenario']) ?? 'unknown'
      state.requests.push({ upstreamKey, scenario, body, rawBodyBytes: rawBody.byteLength })

      if (url.pathname !== '/v1/responses') {
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ object: 'list', data: [] }))
        return
      }

      if (upstreamKey.endsWith('-fallback')) {
        sendCompletedStream(res)
        return
      }

      if (scenario === 'cleanup-success' || scenario === 'cleanup-object-input' || scenario === 'function-output') {
        if (hasEncryptedReasoning(body)) {
          sendEncryptedReasoningError(res, scenario === 'cleanup-success' ? 'thinking_signature_invalid' : 'invalid_encrypted_content')
          return
        }
        sendCompletedStream(res)
        return
      }

      if (scenario === 'fallback-after-cleanup-failed') {
        sendEncryptedReasoningError(res, hasEncryptedReasoning(body) ? 'thinking_signature_invalid' : 'invalid_encrypted_content')
        return
      }

      if (scenario === 'no-encrypted-default-retry') {
        const scenarioRequestCount = state.requests.filter((request) => request.scenario === scenario && request.upstreamKey === upstreamKey).length
        if (scenarioRequestCount === 1) {
          sendEncryptedReasoningError(res, 'thinking_signature_invalid')
          return
        }
        sendCompletedStream(res)
        return
      }

      res.writeHead(500, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { type: 'server_error', code: 'mock_unhandled', message: 'mock scenario not handled' } }))
    })
  })
}

async function requestResponsesStream(baseUrl: string, apiKey: string, body: Record<string, unknown>): Promise<string> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `网关请求应最终成功，实际 HTTP ${response.status}: ${text}`)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  return text
}

function buildEncryptedReasoningRequest(
  scenario: string,
  options: {
    functionCallOutput?: boolean
    largeBody?: boolean
    objectInput?: boolean
  } = {}
): Record<string, unknown> {
  const input: Array<Record<string, unknown>> = [
    {
      type: 'reasoning',
      encrypted_content: `encrypted-content-${scenario}`,
      summary: []
    }
  ]
  if (options.functionCallOutput) {
    input.push({
      type: 'function_call_output',
      call_id: 'call_compat_recovery',
      output: 'ok'
    })
  } else {
    input.push({
      type: 'message',
      role: 'user',
      content: [{ type: 'input_text', text: 'hello compatibility recovery' }]
    })
  }
  const requestInput: Record<string, unknown> | Array<Record<string, unknown>> = options.objectInput
    ? input[0]
    : input
  return {
    model: 'gpt-5.4',
    stream: true,
    store: false,
    previous_response_id: `resp_${scenario}`,
    input: requestInput,
    include: ['reasoning.encrypted_content'],
    metadata: {
      scenario,
      padding: options.largeBody ? 'x'.repeat(300 * 1024) : undefined
    }
  }
}

function buildPlainResponsesRequest(scenario: string): Record<string, unknown> {
  return {
    model: 'gpt-5.4',
    stream: true,
    store: false,
    input: [
      {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'hello default retry' }]
      }
    ],
    metadata: { scenario }
  }
}

function sendEncryptedReasoningError(res: http.ServerResponse, code: 'thinking_signature_invalid' | 'invalid_encrypted_content'): void {
  res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    error: {
      type: 'invalid_request_error',
      code,
      message: 'The encrypted content could not be verified. Reason: Encrypted content could not be decrypted or parsed.'
    }
  }))
}

function sendCompletedStream(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  res.write('event: response.created\n')
  res.write('data: {"type":"response.created","response":{"id":"resp_compat_recovery","status":"in_progress"}}\n\n')
  res.write('event: response.completed\n')
  res.write('data: {"type":"response.completed","response":{"id":"resp_compat_recovery","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}\n\n')
  res.end()
}

function hasEncryptedReasoning(body: Record<string, unknown> | undefined): boolean {
  return inputItems(body).some((item) => item.type === 'reasoning' && Object.prototype.hasOwnProperty.call(item, 'encrypted_content'))
}

function hasPreviousResponseId(body: Record<string, unknown> | undefined): boolean {
  return typeof body?.previous_response_id === 'string' && body.previous_response_id.trim().length > 0
}

function hasFunctionCallOutput(body: Record<string, unknown> | undefined): boolean {
  return inputItems(body).some((item) => item.type === 'function_call_output')
}

function inputItems(body: Record<string, unknown> | undefined): Array<Record<string, unknown>> {
  const input = body?.input
  if (Array.isArray(input)) {
    return input.filter(isPlainObject)
  }
  return isPlainObject(input) ? [input] : []
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(isPlainObject(parsed), 'mock 上游请求体应为 JSON object')
  return parsed
}

function stringAtPath(body: Record<string, unknown>, path: string[]): string | undefined {
  let current: unknown = body
  for (const key of path) {
    if (!isPlainObject(current)) {
      return undefined
    }
    current = current[key]
  }
  return typeof current === 'string' ? current : undefined
}

function bearerToken(value: unknown): string {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' ? raw.replace(/^Bearer\s+/i, '').trim() : ''
}

function seedGatewayAccess(): { systemAccountId: string; role: 'user' } {
  if (!seedOwnerAccess) {
    const owner = repositories.createSystemAccount({
      username: 'compat_recovery_owner',
      displayName: '兼容恢复回归用户',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    })
    seedOwnerAccess = { systemAccountId: owner.id, role: 'user' }
  }
  return seedOwnerAccess
}

function assertAccountsStillActive(gateways: SeededGateway[]): void {
  for (const gateway of gateways) {
    const primary = repositories.findAccountSummary(gateway.primaryAccountId, seedGatewayAccess())
    const fallback = repositories.findAccountSummary(gateway.fallbackAccountId, seedGatewayAccess())
    assert.equal(primary?.status, 'active', `首选账号 ${gateway.primaryAccountId} 不应被兼容恢复写成异常`)
    assert.equal(fallback?.status, 'active', `备用账号 ${gateway.fallbackAccountId} 不应被兼容恢复写成异常`)
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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
