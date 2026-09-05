import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

type MockBehavior = 'success' | 'precommit_server_error' | 'precommit_server_error_once_then_success' | 'precommit_server_error_twice_then_success' | 'precommit_policy_error'

interface UpstreamHit {
  authorization: string
  path: string
  atMs: number
}

interface Fixture {
  apiKey: string
  primaryAuthorization: string
  secondaryAuthorization: string
  accountAuthorizations: string[]
}

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-precommit-retry-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-precommit-retry-mock.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-precommit-retry-mock-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  responseInspectionPolicies,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  sqliteReadWorkerPool,
  codexTurnRetry
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../modules/gateway/client-profiles/codex-turn-retry.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
const mockBehaviorByAuthorization = new Map<string, MockBehavior>()
const upstreamHitCountByAuthorization = new Map<string, number>()

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 2,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    gatewayServer = http.createServer(app)
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`

    await runGenericClientRetryLoopScenario(gatewayBaseUrl, upstreamBaseUrl)
    await runSameAccountRecoveryScenario(gatewayBaseUrl, upstreamBaseUrl)
    await runSameAccountRetryDelayScenario(gatewayBaseUrl, upstreamBaseUrl)
    await runCodexTurnAvoidanceScenario(gatewayBaseUrl, upstreamBaseUrl)
    await runTransientFailoverPoolExhaustionScenario(gatewayBaseUrl, upstreamBaseUrl)
    await runExplicitServerRetryScenario(gatewayBaseUrl, upstreamBaseUrl)

    console.log('gateway precommit SSE retry mock AI regression passed')
  } finally {
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
  }
} finally {
  codexTurnRetry.clearCodexTurnRetryStateForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  usageRecordQueue.clearUsageRecordQueueForTest()
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runGenericClientRetryLoopScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const fixture = createFixture({
    name: 'generic-default-retry-no-avoidance',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl
  })
  mockBehaviorByAuthorization.set(fixture.primaryAuthorization, 'precommit_server_error')
  mockBehaviorByAuthorization.set(fixture.secondaryAuthorization, 'success')

  for (let attempt = 1; attempt <= 4; attempt += 1) {
    upstreamHits.length = 0
    const response = await postResponses(gatewayBaseUrl, fixture.apiKey, {
      stream: true,
      headers: { accept: 'text/event-stream' }
    })
    const body = await response.text()
    assert.equal(response.status, 200, `generic attempt ${attempt} should return the fallback success: ${body}`)
    assert(body.includes('mock secondary success'), `generic attempt ${attempt} should return the fallback response: ${JSON.stringify({ body, upstreamHits })}`)
    assert.deepEqual(
      upstreamHits.map((hit) => hit.authorization),
      [fixture.primaryAuthorization, fixture.primaryAuthorization, fixture.primaryAuthorization, fixture.secondaryAuthorization],
      `generic attempt ${attempt} must switch accounts inside the gateway request`
    )
  }
}

async function runSameAccountRecoveryScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const fixture = createFixture({
    name: 'same-account-recovers-on-third-attempt',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl
  })
  mockBehaviorByAuthorization.set(fixture.primaryAuthorization, 'precommit_server_error_twice_then_success')
  mockBehaviorByAuthorization.set(fixture.secondaryAuthorization, 'success')
  upstreamHits.length = 0
  const response = await postResponses(gatewayBaseUrl, fixture.apiKey, {
    stream: true,
    headers: { accept: 'text/event-stream' }
  })
  const body = await response.text()
  assert.equal(response.status, 200, `same account should recover on third attempt: ${body}`)
  assert(body.includes('mock secondary success'), `same account recovery should return success: ${body}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.authorization),
    [fixture.primaryAuthorization, fixture.primaryAuthorization, fixture.primaryAuthorization],
    '瞬态错误在第三次同账号派发成功时不得提前切号'
  )
}

async function runSameAccountRetryDelayScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const fixture = createFixture({
    name: 'same-account-retry-honors-interval',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl
  })
  mockBehaviorByAuthorization.set(fixture.primaryAuthorization, 'precommit_server_error_once_then_success')
  mockBehaviorByAuthorization.set(fixture.secondaryAuthorization, 'success')
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 2,
    temporaryUnschedulableRetryIntervalSeconds: 1,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  gatewayCache.clearGatewayRuntimeCache()
  try {
    upstreamHits.length = 0
    const response = await postResponses(gatewayBaseUrl, fixture.apiKey, {
      stream: true,
      headers: { accept: 'text/event-stream' }
    })
    const body = await response.text()
    assert.equal(response.status, 200, `precommit 同账户重试应在等待后恢复：${body}`)
    assert.deepEqual(
      upstreamHits.map((hit) => hit.authorization),
      [fixture.primaryAuthorization, fixture.primaryAuthorization],
      '首语义输出前瞬态错误应等待后只重试同一账户'
    )
    assert(
      upstreamHits[1]!.atMs - upstreamHits[0]!.atMs >= 900,
      `首语义输出前同账户重试必须遵循 1 秒配置间隔：${upstreamHits[1]!.atMs - upstreamHits[0]!.atMs}ms`
    )
  } finally {
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 2,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      noAvailableAccountWaitTimeoutSeconds: 10
    })
    gatewayCache.clearGatewayRuntimeCache()
  }
}

async function runCodexTurnAvoidanceScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const fixture = createFixture({
    name: 'codex-turn-avoidance',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl
  })
  mockBehaviorByAuthorization.set(fixture.primaryAuthorization, 'precommit_server_error')
  mockBehaviorByAuthorization.set(fixture.secondaryAuthorization, 'success')
  const codexHeaders = {
    accept: 'text/event-stream',
    'x-codex-turn-metadata': JSON.stringify({
      turn_id: 'mock-turn-retry-loop',
      session_id: 'mock-session-retry-loop',
      thread_id: 'mock-thread-retry-loop'
    })
  }

  upstreamHits.length = 0
  const response = await postResponses(gatewayBaseUrl, fixture.apiKey, { stream: true, headers: codexHeaders })
  const body = await response.text()
  assert.equal(response.status, 200, `Codex should return the fallback success: ${body}`)
  assert(body.includes('mock secondary success'), `Codex should return the fallback response: ${body}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.authorization),
    [fixture.primaryAuthorization, fixture.primaryAuthorization, fixture.primaryAuthorization, fixture.secondaryAuthorization],
    'Codex must use the same per-account transient retry budget before failover'
  )
}

async function runExplicitServerRetryScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  responseInspectionPolicies.createResponseInspectionPolicy({
    name: 'mock precommit server error switches account',
    enabled: true,
    priority: 1,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    match: { errorCodes: ['mock_policy_error'] },
    action: 'retry_next_account',
    notes: 'local mock AI retry regression'
  })
  const fixture = createFixture({
    name: 'generic-explicit-retry-next-account',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl
  })
  mockBehaviorByAuthorization.set(fixture.primaryAuthorization, 'precommit_policy_error')
  mockBehaviorByAuthorization.set(fixture.secondaryAuthorization, 'success')

  upstreamHits.length = 0
  const response = await postResponses(gatewayBaseUrl, fixture.apiKey, {
    stream: true,
    headers: { accept: 'text/event-stream' }
  })
  const body = await response.text()
  assert.equal(response.status, 200, `explicit retry_next_account should return the secondary result: ${body}`)
  assert(body.includes('mock secondary success'), `explicit retry_next_account should not expose the primary error: ${body}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.authorization),
    [fixture.primaryAuthorization, fixture.secondaryAuthorization],
    'explicit retry_next_account must retry the request on the secondary account in the same gateway request'
  )
}

async function runTransientFailoverPoolExhaustionScenario(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const fixture = createFixture({
    name: 'generic-transient-failover-pool-exhaustion',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamBaseUrl,
    accountCount: 4
  })
  for (const authorization of fixture.accountAuthorizations) {
    mockBehaviorByAuthorization.set(authorization, 'precommit_server_error')
  }

  upstreamHits.length = 0
  const response = await postResponses(gatewayBaseUrl, fixture.apiKey, {
    stream: true,
    headers: { accept: 'text/event-stream' }
  })
  const body = await response.text()
  assert.equal(response.status, 503, `pool exhaustion should return a retryable stream failure: ${body}`)
  assert(body.includes('upstream_retryable_error'), `pool exhaustion should preserve the gateway retryable error code: ${body}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.authorization),
    fixture.accountAuthorizations.flatMap((authorization) => [authorization, authorization, authorization]),
    'while the request wall is available, every candidate must use its two extra transient retries before the gateway returns a retryable failure'
  )
}

function createFixture(input: {
  name: string
  providerCode: string
  providerProtocolProfileId: string
  upstreamBaseUrl: string
  accountCount?: number
}): Fixture {
  const group = repositories.createGroup({
    name: `mock retry ${input.name}`,
    providerCode: input.providerCode,
    enabled: true
  }, access)
  const accountCount = Math.max(2, input.accountCount ?? 2)
  const accountAuthorizations = Array.from({ length: accountCount }, (_, index) => (
    `Bearer sk-mock-${index === 0 ? 'primary' : index === 1 ? 'secondary' : `fallback-${index}`}-${input.name}`
  ))
  for (const [index, authorization] of accountAuthorizations.entries()) {
    const account = repositories.createAccount({
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      name: `mock ${index === 0 ? 'primary' : `fallback ${index}`} ${input.name}`,
      type: 'api_key',
      credentials: {
        api_key: authorization.slice('Bearer '.length),
        base_url: input.upstreamBaseUrl,
        supported_endpoint_modes: ['responses_json', 'responses_sse']
      },
      groupId: group.id,
      schedulable: true,
      supportedModels: ['gpt-5.5'],
      priority: index * 10
    }, access)
    activateAccount(account.id)
  }
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `mock retry key ${input.name}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `fixture ${input.name} should return its plaintext API key`)
  return {
    apiKey: apiKey.key,
    primaryAuthorization: accountAuthorizations[0]!,
    secondaryAuthorization: accountAuthorizations[1]!,
    accountAuthorizations
  }
}

function activateAccount(accountId: string): void {
  assert.equal(repositories.projectAccountHealthFixtureSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0
  }), true, `health check should activate account ${accountId}`)
}

function postResponses(
  gatewayBaseUrl: string,
  apiKey: string,
  input: { stream: boolean; headers?: Record<string, string> }
): Promise<Response> {
  return fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      ...input.headers
    },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'mock transient upstream failure', stream: input.stream })
  })
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const authorization = String(req.headers.authorization ?? '')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({ authorization, path, atMs: Date.now() })
      const hitCount = (upstreamHitCountByAuthorization.get(authorization) ?? 0) + 1
      upstreamHitCountByAuthorization.set(authorization, hitCount)
      if (path !== '/v1/responses') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'mock path not found' } }))
        return
      }
      const behavior = mockBehaviorByAuthorization.get(authorization)
      if (behavior === 'precommit_server_error') {
        sendPrecommitServerError(res, 'server_error')
        return
      }
      if (behavior === 'precommit_server_error_once_then_success' && hitCount <= 1) {
        sendPrecommitServerError(res, 'server_error')
        return
      }
      if (behavior === 'precommit_server_error_twice_then_success' && hitCount <= 2) {
        sendPrecommitServerError(res, 'server_error')
        return
      }
      if (behavior === 'precommit_policy_error') {
        sendPrecommitServerError(res, 'mock_policy_error')
        return
      }
      sendSuccess(res)
    })
  })
}

function sendPrecommitServerError(res: http.ServerResponse, code: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.end(`event: response.created\ndata: ${JSON.stringify({
    type: 'response.created',
    response: {
      id: 'resp-mock-transient-error',
      status: 'in_progress',
      error: { code, type: code, message: 'mock transient upstream failure' }
    }
  })}\n\n`)
}

function sendSuccess(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({
    type: 'response.output_text.delta',
    delta: 'mock secondary success'
  })}\n\n`)
  res.end(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: { id: 'resp-mock-success', status: 'completed', usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 } }
  })}\n\n`)
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
  assert(typeof address === 'object' && address !== null, 'server should be listening')
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
