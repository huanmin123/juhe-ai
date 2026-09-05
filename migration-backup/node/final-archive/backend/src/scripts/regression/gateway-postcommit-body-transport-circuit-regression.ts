import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  gatewayAccountProtocolModelScope
} from '../../modules/gateway/runtime/account-circuit.service.js'
import { type AccountCircuitState } from '../../modules/gateway/runtime/account-circuit-store.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import {
  clearAccountConcurrency,
  getAccountCurrentConcurrency
} from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const model = 'gpt-5.5'
const clientIp = '198.51.100.210'
const tempRoot = resolve(
  tmpdir(),
  `juhe-ai-gateway-postcommit-body-transport-${Date.now()}-${Math.random().toString(16).slice(2)}`
)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-postcommit-body-transport-secret'
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
  apiKeyFailureGuard,
  clientIpAvoidance,
  clientIpErrorCircuit,
  proxyHealth,
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
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/runtime/client-ip-error-circuit.service.js'),
  import('../../modules/gateway/runtime/proxy-health.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

type ResponseKind = 'non_stream' | 'sse'
type AttemptKind = 'ordinary' | 'observer' | 'confirmation_1' | 'confirmation_2'

interface UpstreamHit {
  responseKind: ResponseKind
  attemptKind: AttemptKind
  key: string
  partialWriteFlushed: boolean
}

interface Scenario {
  responseKind: ResponseKind
  apiKey: string
  primaryGroupId: string
  primaryAccountId: string
  primaryKey: string
  backupGroupId: string
  backupAccountId: string
  backupKey: string
}

interface PartialResponseResult {
  statusCode: number
  headers: http.IncomingHttpHeaders
  text: string
  terminated: boolean
  durationMs: number
  responseLifetimeMs: number
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    accountCircuitConfirmationFailuresRequired: 2,
    temporaryUnschedulableRetryAttempts: 0,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    textFirstResponseTimeoutSeconds: 10,
    textStreamIdleTimeoutSeconds: 10,
    textUncommittedAttemptMaxLifetimeSeconds: 60,
    noAvailableAccountWaitTimeoutSeconds: 10
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  clearRuntimeState()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
  const sseScenario = createScenario('sse', upstreamBaseUrl)
  const nonStreamScenario = createScenario('non_stream', upstreamBaseUrl)

  gatewayServer = createGatewayServer()
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  const sseOrdinary = await runTruncatedAttempt(gatewayBaseUrl, sseScenario, 'ordinary')
  const nonStreamOrdinary = await runTruncatedAttempt(gatewayBaseUrl, nonStreamScenario, 'ordinary')
  assertCommittedClientBoundary(sseScenario, 'ordinary', sseOrdinary)
  assertCommittedClientBoundary(nonStreamScenario, 'ordinary', nonStreamOrdinary)

  const sseScope = protocolScope(sseScenario)
  const nonStreamScope = protocolScope(nonStreamScenario)
  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  const sseInitialState = await circuitStore.get(sseScope)
  const nonStreamInitialState = await circuitStore.get(nonStreamScope)

  assertInitialSuspect(sseInitialState, 'SSE 已提交后 transport truncation')
  assertInitialSuspect(nonStreamInitialState, '非流式已提交后 transport truncation')
  await assertNeutralHotQuality(nonStreamScenario, 1, '非流式 ordinary')
  await assertNeutralHotQuality(sseScenario, 1, 'SSE ordinary')

  await forceSuspectRetryAt(sseScope, Date.now() + 60_000, 'sse-observer-not-due')
  await forceSuspectRetryAt(nonStreamScope, Date.now() + 60_000, 'non-stream-observer-not-due')
  const sseObserver = await runTruncatedAttempt(gatewayBaseUrl, sseScenario, 'observer')
  const nonStreamObserver = await runTruncatedAttempt(gatewayBaseUrl, nonStreamScenario, 'observer')
  assertCommittedClientBoundary(sseScenario, 'observer', sseObserver)
  assertCommittedClientBoundary(nonStreamScenario, 'observer', nonStreamObserver)
  assertObserverStayedNeutral(await circuitStore.get(sseScope), 'SSE observer')
  assertObserverStayedNeutral(await circuitStore.get(nonStreamScope), '非流式 observer')
  await assertNeutralHotQuality(sseScenario, 2, 'SSE ordinary + observer')
  await assertNeutralHotQuality(nonStreamScenario, 2, '非流式 ordinary + observer')

  await runIndependentConfirmation(gatewayBaseUrl, sseScenario, 'confirmation_1', 'SUSPECT', 1)
  await runIndependentConfirmation(gatewayBaseUrl, nonStreamScenario, 'confirmation_1', 'SUSPECT', 1)
  await assertConfirmedHotQuality(sseScenario, 3, 2, 1, 'SSE 第一次独立 confirmation')
  await assertConfirmedHotQuality(nonStreamScenario, 3, 2, 1, '非流式第一次独立 confirmation')

  await runIndependentConfirmation(gatewayBaseUrl, sseScenario, 'confirmation_2', 'OPEN', 2)
  await runIndependentConfirmation(gatewayBaseUrl, nonStreamScenario, 'confirmation_2', 'OPEN', 2)
  await assertConfirmedHotQuality(sseScenario, 4, 2, 2, 'SSE 第二次独立 confirmation')
  await assertConfirmedHotQuality(nonStreamScenario, 4, 2, 2, '非流式第二次独立 confirmation')

  await assertBusinessStateRemainsUntouched([sseScenario, nonStreamScenario])
  await assertRuntimeResourcesReleased([sseScenario, nonStreamScenario])

  console.log(JSON.stringify({
    message: 'gateway postcommit body transport circuit regression passed',
    paths: ['non_stream_postcommit', 'sse_postcommit'],
    attemptsPerPath: 4,
    upstreamHits: upstreamHits.length
  }))
} finally {
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  clearRuntimeState()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
}

function clearRuntimeState(): void {
  gatewayCache.clearGatewayRuntimeCache()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()
}

function createScenario(responseKind: ResponseKind, upstreamBaseUrl: string): Scenario {
  const label = responseKind === 'sse' ? 'SSE已提交断流' : '非流式已提交断流'
  const primaryGroup = repositories.createGroup({
    name: `${label}-主分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const backupGroup = repositories.createGroup({
    name: `${label}-备用分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primaryKey = `sk-postcommit-${responseKind}-primary`
  const backupKey = `sk-postcommit-${responseKind}-backup`
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}-主账户`,
    type: 'api_key',
    credentials: { api_key: primaryKey, base_url: upstreamBaseUrl },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 4,
    priority: 0,
    supportedModels: [model]
  }, access)
  const backup = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}-备用账户`,
    type: 'api_key',
    credentials: { api_key: backupKey, base_url: upstreamBaseUrl },
    groupId: backupGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 4,
    priority: 0,
    fallbackEnabled: true,
    supportedModels: [model]
  }, access)
  activate(primary.id)
  activate(backup.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${label}-网关Key`,
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: backupGroup.id, priority: 2, status: 'active' }
    ],
    status: 'active'
  }, access)
  assert(apiKey.key)
  gatewayCache.clearGatewayRuntimeCache()
  return {
    responseKind,
    apiKey: apiKey.key,
    primaryGroupId: primaryGroup.id,
    primaryAccountId: primary.id,
    primaryKey,
    backupGroupId: backupGroup.id,
    backupAccountId: backup.id,
    backupKey
  }
}

function activate(accountId: string): void {
  assert.equal(repositories.projectAccountHealthFixtureSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `激活 Mock 账户失败：${accountId}`)
}

function createGatewayServer(): http.Server {
  const app = express()
  app.set('trust proxy', 1)
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const responseKind = requiredResponseKind(url.searchParams.get('postcommit_kind'))
    const attemptKind = requiredAttemptKind(url.searchParams.get('postcommit_attempt'))
    const key = bearerToken(req.headers.authorization)
    const hit: UpstreamHit = { responseKind, attemptKind, key, partialWriteFlushed: false }
    upstreamHits.push(hit)
    req.resume()

    if (key.includes('-backup')) {
      sendBackupResponse(res, responseKind, attemptKind)
      return
    }
    sendCommittedThenTruncate(res, responseKind, attemptKind, hit)
  })
}

function sendCommittedThenTruncate(
  res: http.ServerResponse,
  responseKind: ResponseKind,
  attemptKind: AttemptKind,
  hit: UpstreamHit
): void {
  const marker = `postcommit-partial-${responseKind}-${attemptKind}`
  const event = responseKind === 'sse'
    ? `data: ${JSON.stringify({
        id: `chatcmpl-${attemptKind}`,
        object: 'chat.completion.chunk',
        created: Math.floor(Date.now() / 1000),
        model,
        choices: [{ index: 0, delta: { role: 'assistant', content: marker }, finish_reason: null }]
      })}\n\n`
    : `ID3${marker}`
  res.writeHead(200, {
    'content-type': responseKind === 'sse'
      ? 'text/event-stream; charset=utf-8'
      : 'audio/mpeg',
    'content-length': String(Buffer.byteLength(event) + 4096),
    connection: 'close',
    'x-provider-private-error': 'must-not-drive-state'
  })
  res.flushHeaders()
  const socket = res.socket
  res.write(event, () => {
    hit.partialWriteFlushed = true
    const truncate = () => {
      if (socket && !socket.destroyed) {
        socket.destroy()
        return
      }
      res.destroy()
    }
    const timer = setTimeout(truncate, 75)
    timer.unref()
  })
}

function sendBackupResponse(
  res: http.ServerResponse,
  responseKind: ResponseKind,
  attemptKind: AttemptKind
): void {
  const marker = `postcommit-backup-must-not-run-${responseKind}-${attemptKind}`
  if (responseKind === 'sse') {
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    res.end(`event: response.completed\ndata: ${JSON.stringify({
      type: 'response.completed',
      response: { id: marker, status: 'completed' }
    })}\n\n`)
    return
  }
  res.writeHead(200, { 'content-type': 'audio/mpeg' })
  res.end(`ID3${marker}`)
}

async function runTruncatedAttempt(
  gatewayBaseUrl: string,
  scenario: Scenario,
  attemptKind: AttemptKind
): Promise<PartialResponseResult> {
  const query = new URLSearchParams({
    postcommit_kind: scenario.responseKind,
    postcommit_attempt: attemptKind
  })
  const isSse = scenario.responseKind === 'sse'
  const path = isSse
    ? `/v1/chat/completions?${query}`
    : `/v1/audio/speech?${query}`
  const body = JSON.stringify(isSse
    ? {
        model,
        messages: [{ role: 'user', content: `postcommit ${scenario.responseKind} ${attemptKind}` }],
        stream: true
      }
    : {
        model,
        input: `postcommit ${scenario.responseKind} ${attemptKind}`,
        voice: 'alloy',
        response_format: 'mp3'
      })
  return await rawHttpPost(`${gatewayBaseUrl}${path}`, body, {
    authorization: `Bearer ${scenario.apiKey}`,
    'content-type': 'application/json',
    accept: isSse ? 'text/event-stream' : 'audio/mpeg',
    'x-forwarded-for': clientIpForAttempt(attemptKind),
    'x-session-id': `postcommit-${scenario.responseKind}-${attemptKind}`
  })
}

function clientIpForAttempt(attemptKind: AttemptKind): string {
  switch (attemptKind) {
    case 'ordinary':
      return clientIp
    case 'observer':
      return '198.51.100.211'
    case 'confirmation_1':
      return '198.51.100.212'
    case 'confirmation_2':
      return '198.51.100.213'
  }
}

function rawHttpPost(
  url: string,
  body: string,
  headers: Record<string, string>
): Promise<PartialResponseResult> {
  return new Promise((resolvePromise, rejectPromise) => {
    const startedAtMs = Date.now()
    let responseStarted = false
    let settled = false
    const timer = setTimeout(() => finishError(new Error(postcommitTimeoutMessage(url, responseStarted))), 5_000)
    timer.unref()
    const request = http.request(url, {
      method: 'POST',
      headers: {
        ...headers,
        'content-length': String(Buffer.byteLength(body))
      }
    })

    const finishError = (error: Error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      request.destroy()
      rejectPromise(error)
    }
    request.once('error', (error) => {
      if (!responseStarted) finishError(error)
    })
    request.once('response', (response) => {
      responseStarted = true
      const responseStartedAtMs = Date.now()
      const chunks: Buffer[] = []
      let ended = false
      const finish = (terminated: boolean) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        resolvePromise({
          statusCode: response.statusCode ?? 0,
          headers: response.headers,
          text: Buffer.concat(chunks).toString('utf8'),
          terminated,
          durationMs: Date.now() - startedAtMs,
          responseLifetimeMs: Date.now() - responseStartedAtMs
        })
      }
      response.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
      response.once('end', () => {
        ended = true
        finish(false)
      })
      response.once('aborted', () => finish(true))
      response.once('error', () => finish(true))
      response.once('close', () => {
        if (!ended) finish(true)
      })
    })
    request.end(body)
  })
}

function assertCommittedClientBoundary(
  scenario: Scenario,
  attemptKind: AttemptKind,
  result: PartialResponseResult
): void {
  const label = `${scenario.responseKind}/${attemptKind}`
  assert.equal(result.statusCode, 200, `${label} 必须先向客户端提交上游 200 header`)
  assert(
    result.responseLifetimeMs < 2_000,
    `${label} 响应头提交后必须由真实正文 socket truncation 快速终止，不能等待墙钟/idle timeout：response=${result.responseLifetimeMs}ms total=${result.durationMs}ms`
  )
  assert.match(
    String(result.headers['content-type'] ?? ''),
    scenario.responseKind === 'sse' ? /text\/event-stream/i : /audio\/mpeg/i,
    `${label} 必须保留已提交响应形态`
  )
  assert.equal(result.terminated, true, `${label} 正文截断后必须中断下游，不能伪造完整成功`)
  assert.match(result.text, new RegExp(`postcommit-partial-${scenario.responseKind}-${attemptKind}`), `${label} 必须真实提交首段正文`)
  assert.doesNotMatch(
    result.text,
    /postcommit-backup-must-not-run|response\.failed|upstream_retryable_error|service_unavailable|gateway_request_wall_budget_exhausted|downstream_connection_closed|上游暂时不可用/u,
    `${label} 已提交后不得拼接备用账户或网关错误正文`
  )
  const hits = hitsFor(scenario.responseKind, attemptKind)
  assert.deepEqual(hits.map((hit) => hit.key), [scenario.primaryKey], `${label} 已提交后不得重放或切到备用账户`)
  assert.equal(hits[0]?.partialWriteFlushed, true, `${label} Mock 必须先确认部分正文已写入 socket，再制造 transport truncation`)
}

function postcommitTimeoutMessage(url: string, responseStarted: boolean): string {
  const parsed = new URL(url)
  const responseKind = requiredResponseKind(parsed.searchParams.get('postcommit_kind'))
  const attemptKind = requiredAttemptKind(parsed.searchParams.get('postcommit_attempt'))
  return `等待已提交断流响应超时：${url}；responseStarted=${responseStarted}；upstreamHits=${JSON.stringify(hitsFor(responseKind, attemptKind))}`
}

function assertInitialSuspect(state: AccountCircuitState, label: string): void {
  assert.equal(state.phase, 'SUSPECT', `${label} 必须向 protocol/model circuit 记录客观 transport evidence`)
  assert.equal(state.confirmationFailureCount ?? 0, 0, `${label} 的首次同源证据不得自证失败`)
  assert.equal(state.lease, undefined, `${label} 收口后 confirmation lease 必须释放`)
  assert.equal(state.failureEvidenceKeys?.length, 1, `${label} 只能留下一个请求 evidence`)
}

function assertObserverStayedNeutral(state: AccountCircuitState, label: string): void {
  assert.equal(state.phase, 'SUSPECT', `${label} 失败必须保持 SUSPECT`)
  assert.equal(state.confirmationFailureCount ?? 0, 0, `${label} 未到期时不得累计 confirmation 失败`)
  assert.equal(state.lease, undefined, `${label} 不得占用 confirmation lease`)
  assert.equal(state.failureEvidenceKeys?.length, 1, `${label} 不得把观察者失败追加为独立确认 evidence`)
}

async function runIndependentConfirmation(
  gatewayBaseUrl: string,
  scenario: Scenario,
  attemptKind: Extract<AttemptKind, 'confirmation_1' | 'confirmation_2'>,
  expectedPhase: 'SUSPECT' | 'OPEN',
  expectedFailureCount: number
): Promise<void> {
  const scope = protocolScope(scenario)
  await forceSuspectRetryAt(scope, Date.now() - 1, `${scenario.responseKind}-${attemptKind}-due`)
  const response = await runTruncatedAttempt(gatewayBaseUrl, scenario, attemptKind)
  assertCommittedClientBoundary(scenario, attemptKind, response)
  const state = await accountCircuit.getGatewayAccountCircuitStore().get(scope)
  assert.equal(state.phase, expectedPhase, `${scenario.responseKind}/${attemptKind} 必须按独立 confirmation 阈值推进`)
  assert.equal(state.confirmationFailureCount ?? 0, expectedFailureCount)
  assert.equal(state.lease, undefined, `${scenario.responseKind}/${attemptKind} 收口后 lease 必须释放`)
}

async function forceSuspectRetryAt(
  scope: ReturnType<typeof protocolScope>,
  retryAtMs: number,
  transitionLabel: string
): Promise<void> {
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const state = await store.get(scope)
  assert.equal(state.phase, 'SUSPECT', `${transitionLabel} 只能调整 SUSPECT 测试夹具`)
  const updatedAtMs = Math.max(Date.now(), state.updatedAtMs + 1)
  const result = await store.restore({
    ...state,
    transitionId: `test:${transitionLabel}:${updatedAtMs}`,
    retryAtMs,
    lease: undefined,
    updatedAtMs
  }, updatedAtMs)
  assert.equal(result.status, 'applied', `${transitionLabel} 必须原子调整测试 retryAt`)
}

async function assertNeutralHotQuality(
  scenario: Scenario,
  expectedAttempts: number,
  label: string
): Promise<void> {
  const snapshot = await hotQualitySnapshot(scenario)
  assert.equal(snapshot.window5m.attempts, expectedAttempts, `${label} attempt 计数必须精确`)
  const diagnostic = JSON.stringify(snapshot.window5m)
  assert.equal(snapshot.window5m.unknownOutcomes, expectedAttempts, `${label} ordinary/observer transport 必须只记 unknown：${diagnostic}`)
  assert.equal(snapshot.window5m.localTransportFailures, 0, `${label} 不得写共享 transport 质量失败：${diagnostic}`)
  assert.equal(snapshot.window5m.readInterruptions, 0, `${label} 不得污染共享 read interruption：${diagnostic}`)
  assert.equal(snapshot.window5m.timeouts, 0, `${label} 真实 body transport 不得误记为墙钟/idle timeout：${diagnostic}`)
  assert.equal(snapshot.window5m.clientCancellations, 0, `${label} 上游截断不得误记为客户端取消：${diagnostic}`)
  assert.equal(snapshot.window5m.qualityAttempts, 0, `${label} 不得进入共享可靠性分母：${diagnostic}`)
  assert.equal(snapshot.window5m.lastFailureAtMs, undefined, `${label} 不得推进共享失败时间`)
}

async function assertConfirmedHotQuality(
  scenario: Scenario,
  expectedAttempts: number,
  expectedUnknown: number,
  expectedConfirmedFailures: number,
  label: string
): Promise<void> {
  const snapshot = await hotQualitySnapshot(scenario)
  assert.equal(snapshot.window5m.attempts, expectedAttempts, `${label} attempt 计数必须精确`)
  assert.equal(snapshot.window5m.unknownOutcomes, expectedUnknown, `${label} 不能改写 ordinary/observer 的中性事实`)
  assert.equal(snapshot.window5m.localTransportFailures, expectedConfirmedFailures, `${label} 只有 confirmation 失败可写共享 transport 质量`)
  assert.equal(snapshot.window5m.readInterruptions, expectedConfirmedFailures, `${label} confirmation 断流必须保留读取中断分类`)
  assert.equal(snapshot.window5m.timeouts, 0, `${label} confirmation 断流不得误记为墙钟/idle timeout`)
  assert.equal(snapshot.window5m.clientCancellations, 0, `${label} confirmation 断流不得误记为客户端取消`)
  assert.equal(snapshot.window5m.qualityAttempts, expectedConfirmedFailures, `${label} 共享可靠性分母只能包含独立 confirmation 失败`)
  assert.notEqual(snapshot.window5m.lastFailureAtMs, undefined, `${label} 必须记录确认失败时间`)
}

async function hotQualitySnapshot(scenario: Scenario) {
  const account = primaryDispatchAccount(scenario)
  const snapshot = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    requestLane: 'text',
    modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
  })
  assert(snapshot, `${scenario.responseKind} 必须留下 hot-quality attempt 观测`)
  return snapshot
}

async function assertBusinessStateRemainsUntouched(scenarios: Scenario[]): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '正文 transport 失败不得写 IP×账户避让')
  assert.deepEqual(clientIpErrorCircuit.getGatewayClientIpSecuritySnapshotForTest(), {
    preAuth: [],
    clientIpErrors: []
  }, '正文 transport 失败不得写客户端 IP 错误电路')

  for (const scenario of scenarios) {
    const summary = repositories.findAccountForTest(scenario.primaryAccountId, access)
    assert.equal(summary?.status, 'active', `${scenario.responseKind} 不得按上游状态或正文写死账户`)
    assert.equal(summary?.schedulable, true, `${scenario.responseKind} 不得取消账户调度`)
    assert.equal(summary?.cooldownUntil, undefined, `${scenario.responseKind} 不得写账户冷却`)
    assert.equal(summary?.lastErrorMessage, undefined, `${scenario.responseKind} 不得持久化不可信错误正文`)
    assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${scenario.responseKind} 不得写 Key 临时不可用`)
    assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, `${scenario.responseKind} 不得写 Key 池全不可用`)
    assert.equal(
      apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()
        .filter((entry) => entry.accountId === scenario.primaryAccountId).length,
      0,
      `${scenario.responseKind} 不得残留进程级 Key failure guard`
    )
    assert.equal(
      accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.primaryAccountId],
      undefined,
      `${scenario.responseKind} 不得写账户共享运行态`
    )
    const proxyOrder = proxyHealth.orderOpenAIAccountsByGatewayProxyHealth([
      primaryDispatchAccount(scenario),
      backupDispatchAccount(scenario)
    ])
    assert.equal(proxyOrder.applied, false, `${scenario.responseKind} 单账户断流不得污染 proxy/upstream bucket 顺序`)
    assert.deepEqual(proxyOrder.avoidedAccountIds, [], `${scenario.responseKind} 不得产生 proxy/upstream bucket 账户避让`)

    const parentState = await accountCircuit.getGatewayAccountCircuitStore().get({
      kind: 'account',
      accountRuntimeKey: gatewayAccountRuntimeKey(primaryDispatchAccount(scenario))
    })
    assert.equal(parentState.phase, 'CLOSED', `${scenario.responseKind} 单一 protocol scope 不得升级为全账户 OPEN`)
    assert.equal(parentState.lease, undefined, `${scenario.responseKind} 父账户 circuit 不得残留 lease`)
  }
}

async function assertRuntimeResourcesReleased(scenarios: Scenario[]): Promise<void> {
  await waitUntil(() => scenarios.every((scenario) => (
    getAccountCurrentConcurrency(scenario.primaryAccountId) === 0
    && getAccountCurrentConcurrency(scenario.backupAccountId) === 0
  )), 2_000)
  await waitUntil(() => highConcurrencyGroupQueueSnapshot().length === 0, 2_000)
  for (const scenario of scenarios) {
    assert.equal(getAccountCurrentConcurrency(scenario.primaryAccountId), 0, `${scenario.responseKind} 主账户并发槽必须归零`)
    assert.equal(getAccountCurrentConcurrency(scenario.backupAccountId), 0, `${scenario.responseKind} 备用账户并发槽必须归零`)
    const state = await accountCircuit.getGatewayAccountCircuitStore().get(protocolScope(scenario))
    assert.equal(state.lease, undefined, `${scenario.responseKind} protocol/model circuit lease 必须归零`)
  }
  assert.deepEqual(highConcurrencyGroupQueueSnapshot(), [], '高并发短队列、timer 与索引必须归零')
  assert.deepEqual(
    accountSideEffects.recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    '可恢复等待 scope、waiter 与 timer 必须归零'
  )
  const sideEffects = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(sideEffects.queueLength, 0, '账户持久副作用队列必须归零')
  assert.equal(sideEffects.processing, false, '账户持久副作用 drain 必须停止')
  assert.equal(sideEffects.precheckPendingAccountCount, 0, '账户 precheck 队列必须归零')
  assert.equal(sideEffects.recoveryProbePendingAccountCount, 0, '账户 recovery probe 队列必须归零')
  assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, 'precheck half-open lease 必须归零')
}

function protocolScope(scenario: Scenario) {
  return gatewayAccountProtocolModelScope(primaryDispatchAccount(scenario), 'text', model)
}

function primaryDispatchAccount(scenario: Scenario) {
  const account = repositories.listOpenAIAccountsForGroup(scenario.primaryGroupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === scenario.primaryAccountId)
  assert(account, `找不到 ${scenario.responseKind} 主账户`)
  return account
}

function backupDispatchAccount(scenario: Scenario) {
  const account = repositories.listOpenAIAccountsForGroup(scenario.backupGroupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === scenario.backupAccountId)
  assert(account, `找不到 ${scenario.responseKind} 备用账户`)
  return account
}

function hitsFor(responseKind: ResponseKind, attemptKind: AttemptKind): UpstreamHit[] {
  return upstreamHits.filter((hit) => hit.responseKind === responseKind && hit.attemptKind === attemptKind)
}

function bearerToken(value: unknown): string {
  const text = Array.isArray(value) ? value[0] : String(value ?? '')
  return text.replace(/^Bearer\s+/i, '')
}

function requiredResponseKind(value: string | null): ResponseKind {
  assert(value === 'non_stream' || value === 'sse', `未知 response kind：${String(value)}`)
  return value
}

function requiredAttemptKind(value: string | null): AttemptKind {
  assert(
    value === 'ordinary'
      || value === 'observer'
      || value === 'confirmation_1'
      || value === 'confirmation_2',
    `未知 attempt kind：${String(value)}`
  )
  return value
}

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadlineAtMs = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadlineAtMs) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1')
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string')
  return address.port
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
