import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import type { UpstreamAttempt } from '../../modules/gateway/upstream/attempt.js'
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
  bodyText: string
  phase: 'failing' | 'recovered'
}

interface GatewayScenario {
  accountId: string
  groupId: string
  systemAccountId: string
  apiKey: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-runtime-recovery-probe-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-runtime-recovery-probe-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-runtime-recovery-probe-mock-ai-secret'
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
  readWorkerPool,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  accountCircuit,
  circuitRecovery,
  accountTest,
  accountProbeOutcome,
  usageRecordQueue,
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/background/account-circuit-recovery.service.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/accounts/automatic-account-probe-outcome.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: MockUpstreamHit[] = []
let upstreamPhase: MockUpstreamHit['phase'] = 'failing'

const app = express()
app.set('trust proxy', true)
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  await assertGatewayAutomaticProbeConcurrencyLimit()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
    const scenario = createSingleAccountScenario(upstreamBaseUrl)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const opened = await assertConfirmedTransportFailureOpensCircuit(baseUrl, scenario)
    await assertDispatchRevisionFencesStaleRecovery(opened, scenario, upstreamBaseUrl)
    const reopened = await assertConfirmedTransportFailureOpensCircuit(baseUrl, scenario)
    await assertControlledRecoverySingleFlightAndCloses(baseUrl, reopened, scenario)

    console.log('gateway runtime recovery probe mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  circuitRecovery.installScheduledAccountCircuitRecoveryResolver(undefined)
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  await removeTempRoot()
}

async function assertGatewayAutomaticProbeConcurrencyLimit(): Promise<void> {
  let runningCount = 0
  let maxRunningCount = 0
  await Promise.all(Array.from({ length: 8 }, () => accountSideEffects.runWithGatewayAutomaticProbeSlotForTest(async () => {
    runningCount += 1
    maxRunningCount = Math.max(maxRunningCount, runningCount)
    await delay(20)
    runningCount -= 1
  })))
  assert(maxRunningCount > 0 && maxRunningCount <= runtimeConfig.concurrency.globalMax, 'server 恢复探针和 precheck 只能受进程级共享并发池治理')
}

async function assertConfirmedTransportFailureOpensCircuit(
  baseUrl: string,
  scenario: GatewayScenario
): Promise<{ scope: ReturnType<typeof accountCircuit.gatewayAccountProtocolModelScope>; dispatchRevision: string }> {
  upstreamPhase = 'failing'
  const firstResponse = await postChat(baseUrl, scenario.apiKey, 'first request should establish suspect', '198.51.100.10')
  assert(firstResponse.status >= 500, `Mock AI 连接中断阶段应返回网关失败，实际 HTTP ${firstResponse.status}: ${firstResponse.text}`)
  assert(upstreamHits.some((hit) => hit.phase === 'failing'), '失败阶段应真实命中 Mock AI 上游')

  const candidate = repositories.findOpenAIAccountForGroup(
    scenario.groupId,
    scenario.accountId,
    scenario.systemAccountId,
    { includeUnavailable: true, ignoreAvailability: true }
  )
  assert(candidate, '应能加载真实账户候选以核对电路作用域')
  const scope = accountCircuit.gatewayAccountProtocolModelScope(candidate, 'text', 'gpt-5.5')
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const suspected = await store.get(scope)
  assert.equal(suspected.phase, 'SUSPECT', '首次独立传输失败只能建立 SUSPECT，不能由同一请求自我确认')
  assert.equal(suspected.lease, undefined, '首次请求结束后必须释放 confirmation lease，留给后续请求确认')

  const beforeObserverHits = upstreamHits.length
  const observerResponse = await postChat(baseUrl, scenario.apiKey, 'independent observer before confirmation due', '198.51.100.11')
  assert(observerResponse.status >= 500, `未到期 observer 的 Mock AI 连接中断应返回网关失败，实际 HTTP ${observerResponse.status}: ${observerResponse.text}`)
  assert.equal(upstreamHits.length - beforeObserverHits, 1, `未到期的独立 observer 必须仍可真实命中 Mock AI，避免坏会话饿死其他会话：${observerResponse.text}`)
  const observerNeutral = await store.get(scope)
  assert.equal(observerNeutral.phase, 'SUSPECT', '未到期 observer 失败不得提前打开账户电路')
  assert.equal(observerNeutral.confirmationFailureCount, 0, '未到期 observer 失败不得绕过 confirmation 时间门禁累计阈值')
  assert.equal(observerNeutral.failureEvidenceKeys?.length, 1, '未到期 observer 失败不得追加共享熔断 evidence')
  assert.equal(observerNeutral.lease, undefined, '未到期 observer 失败不得占用 confirmation lease')
  await forceSuspectRetryAt(scope, 'first-confirmation')

  const beforeSecondHits = upstreamHits.length
  const secondResponse = await postChat(baseUrl, scenario.apiKey, 'second request should confirm transport failure', '198.51.100.12')
  assert(secondResponse.status >= 500, `第二次 Mock AI 连接中断应返回网关失败，实际 HTTP ${secondResponse.status}: ${secondResponse.text}`)
  assert.equal(upstreamHits.length - beforeSecondHits, 1, '到期后的第一次独立 confirmation 必须真实命中一次 Mock AI 上游')
  const onceConfirmed = await store.get(scope)
  assert.equal(onceConfirmed.phase, 'SUSPECT', '首次独立 confirmation 失败后必须继续切号并保持 SUSPECT')
  assert.equal(onceConfirmed.confirmationFailureCount, 1)
  assert.equal(onceConfirmed.failureEvidenceKeys?.length, 2, '首次独立 confirmation 必须追加且仅追加一个 evidence')
  assert.equal(onceConfirmed.lease, undefined)
  await forceSuspectRetryAt(scope, 'second-confirmation')

  const beforeThirdHits = upstreamHits.length
  const thirdResponse = await postChat(baseUrl, scenario.apiKey, 'third request should reach confirmation threshold', '198.51.100.13')
  assert(thirdResponse.status >= 500, `第三次 Mock AI 连接中断应返回网关失败，实际 HTTP ${thirdResponse.status}: ${thirdResponse.text}`)
  assert.equal(upstreamHits.length - beforeThirdHits, 1, '到期后的第二次独立 confirmation 必须真实命中一次 Mock AI 上游')
  const opened = await store.get(scope)
  assert.equal(opened.phase, 'OPEN', '首次失败加两次独立 confirmation 失败后必须打开账户电路')
  assert.equal(opened.confirmationFailureCount, 2)
  assert.equal(opened.failureEvidenceKeys?.length, 3)
  assert.equal(opened.lease, undefined, 'OPEN 状态不得残留 confirmation lease')
  assert.equal(opened.backoffAttempt, 1, '达到确认阈值后应进入第一档恢复退避')
  assert.equal(opened.dispatchRevision, accountCircuit.accountCircuitDispatchRevision(candidate))
  return { scope, dispatchRevision: opened.dispatchRevision }
}

async function forceSuspectRetryAt(
  scope: ReturnType<typeof accountCircuit.gatewayAccountProtocolModelScope>,
  transitionLabel: string
): Promise<void> {
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const state = await store.get(scope)
  assert.equal(state.phase, 'SUSPECT', `${transitionLabel} 只能推进 SUSPECT 测试夹具`)
  assert.equal(state.lease, undefined, `${transitionLabel} 推进前不得残留 confirmation lease`)
  const nowMs = Date.now()
  const updatedAtMs = Math.max(nowMs, state.updatedAtMs + 1)
  const restored = await store.restore({
    ...state,
    transitionId: `test:${transitionLabel}:${updatedAtMs}`,
    retryAtMs: nowMs - 1,
    updatedAtMs
  }, updatedAtMs)
  assert.equal(restored.status, 'applied', `${transitionLabel} 必须确定性推进到 confirmation 到期窗口`)
}

async function assertDispatchRevisionFencesStaleRecovery(
  opened: { scope: ReturnType<typeof accountCircuit.gatewayAccountProtocolModelScope>; dispatchRevision: string },
  scenario: GatewayScenario,
  upstreamBaseUrl: string
): Promise<void> {
  upstreamPhase = 'recovered'
  const updated = repositories.updateAccount(scenario.accountId, {
    credentials: {
      api_key: 'sk-runtime-recovery-probe',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json', 'responses_json']
    }
  }, access)
  assert(updated, '测试应能通过真实传输身份更新入口推进 dispatch revision')
  repositories.projectAccountHealthFixtureSuccess(scenario.accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const candidate = repositories.findOpenAIAccountForGroup(
    scenario.groupId,
    scenario.accountId,
    scenario.systemAccountId,
    { includeUnavailable: true, ignoreAvailability: true }
  )
  assert(candidate, 'revision 更新后应能重新加载账户候选')
  const nextRevision = accountCircuit.accountCircuitDispatchRevision(candidate)
  assert.notEqual(nextRevision, opened.dispatchRevision, '凭据连接身份变化必须推进 dispatch revision')

  const store = accountCircuit.getGatewayAccountCircuitStore()
  const beforeHits = upstreamHits.length
  let nowMs = (await store.get(opened.scope)).retryAtMs ?? Date.now()
  const fenced = await createRecoveryService(store, scenario, () => nowMs).sweep()
  assert.equal(fenced.fencedCount, 1, '旧 dispatch revision 的恢复任务必须被 fencing')
  assert.equal(upstreamHits.length, beforeHits, 'revision fencing 必须发生在真实上游探针之前')
  const state = await store.get(opened.scope, nowMs)
  assert.equal(state.phase, 'CLOSED', 'revision 替换后旧事故应关闭，等待新 revision 独立取证')
  assert.equal(state.dispatchRevision, nextRevision)
}

async function assertControlledRecoverySingleFlightAndCloses(
  baseUrl: string,
  opened: { scope: ReturnType<typeof accountCircuit.gatewayAccountProtocolModelScope>; dispatchRevision: string },
  scenario: GatewayScenario
): Promise<void> {
  upstreamPhase = 'recovered'
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const openedState = await store.get(opened.scope)
  let nowMs = openedState.retryAtMs ?? Date.now()
  let releaseProbe!: () => void
  let markProbeStarted!: () => void
  const probeStarted = new Promise<void>((resolve) => { markProbeStarted = resolve })
  const probeGate = new Promise<void>((resolve) => { releaseProbe = resolve })
  let probeCount = 0
  const recoveryA = createRecoveryService(store, scenario, () => nowMs, async () => {
    probeCount += 1
    markProbeStarted()
    await probeGate
  })
  const recoveryB = createRecoveryService(store, scenario, () => nowMs, async () => {
    probeCount += 1
    markProbeStarted()
    await probeGate
  })
  const sweepA = recoveryA.sweep()
  const sweepB = recoveryB.sweep()
  await probeStarted
  const leased = await store.get(opened.scope, nowMs)
  assert.equal(leased.phase, 'HALF_OPEN', '恢复 worker 发真实探针前必须原子进入 HALF_OPEN')
  assert.equal(leased.lease?.kind, 'half_open', 'OPEN 来源的首个 canary 必须持有 half_open lease')
  releaseProbe()
  const [resultA, resultB] = await Promise.all([sweepA, sweepB])
  assert.equal(resultA.leasedCount + resultB.leasedCount, 1, '两个恢复 worker 并发扫描同一 generation 时只能一个取得 lease')
  assert.equal(probeCount, 1, '并发恢复 worker 必须保持真实上游探针单飞')

  let state = await store.get(opened.scope, nowMs)
  assert.equal(state.phase, 'RECOVERING', '首次 framing 完整 canary 应进入 RECOVERING')
  assert.equal(state.recoverySuccessCount, 0, 'OPEN 到 RECOVERING 的首次 canary 不计入连续恢复计数')
  assert.equal(state.lease, undefined, '完成 canary 后必须释放 half_open lease')

  for (const expectedSuccessCount of [1, 2, 0]) {
    nowMs = state.retryAtMs ?? nowMs + 3_000
    const result = await createRecoveryService(store, scenario, () => nowMs).sweep()
    assert.equal(result.framingCompleteCount, 1, '每轮到期恢复应完成一个真实 framing 探针')
    state = await store.get(opened.scope, nowMs)
    assert.equal(state.recoverySuccessCount, expectedSuccessCount)
  }
  assert.equal(state.phase, 'CLOSED', '三次 RECOVERING 成功后必须关闭账户电路')
  assert.equal(state.lease, undefined, 'CLOSED 状态不得残留恢复 lease')

  const probeHits = upstreamHits.filter((hit) => hit.phase === 'recovered' && hit.path === '/v1/responses')
  assert(
    probeHits.length >= 4 && probeHits.every((hit) => hit.authorization === 'Bearer sk-runtime-recovery-probe'),
    `受控恢复应在无用户流量时通过真实账户测试命中 Mock AI responses，实际命中：${JSON.stringify(probeHits)}`
  )

  const response = await postChat(baseUrl, scenario.apiKey, 'user request after background probe should recover')
  assert.equal(response.status, 200, `后台探针恢复后真实用户请求应成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai recovered chat/, '恢复后的用户请求应返回 Mock AI 正常响应')
}

function createRecoveryService(
  store: ReturnType<typeof accountCircuit.getGatewayAccountCircuitStore>,
  scenario: GatewayScenario,
  now: () => number,
  beforeProbe?: () => Promise<void>
): InstanceType<typeof circuitRecovery.AccountCircuitRecoveryService> {
  const resolver = circuitRecovery.createScheduledAccountCircuitRecoveryResolver({
    findAccountForTest: async (accountId, scopeAccess) => repositories.findAccountForTest(accountId, scopeAccess),
    findOpenAIAccountForGroup: async (groupId, accountId, systemAccountId) => repositories.findOpenAIAccountForGroup(
      groupId,
      accountId,
      systemAccountId,
      { includeUnavailable: true, ignoreAvailability: true }
    ),
    probe: async (input) => {
      await beforeProbe?.()
      let upstreamAttempt: UpstreamAttempt | undefined
      const result = await accountTest.testOpenAIAccount(input.account, {
        diagnostics: 'limited',
        groupId: input.groupId,
        systemAccountId: input.systemAccountId,
        model: input.model,
        signal: input.signal,
        trafficSource: 'runtime_recovery_probe',
        testEndpointMode: input.account.healthCheckEndpointMode,
        candidateAccount: input.candidateAccount,
        disableAccountStateMutation: true,
        onUpstreamAttempt: (attempt) => { upstreamAttempt = attempt }
      })
      return accountProbeOutcome.transportProbeOutcomeFromAccountTestResult(result, {
        upstreamAttempt,
        canceled: input.signal.aborted
      })
    }
  })
  return new circuitRecovery.AccountCircuitRecoveryService(store, resolver, {
    batchSize: 10,
    leaseDurationMs: 30_000,
    now
  })
}

function createSingleAccountScenario(upstreamBaseUrl: string): GatewayScenario {
  const group = repositories.createGroup({
    name: '后台恢复探针 Mock AI 分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '后台恢复探针 Mock AI 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-recovery-probe',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse', 'chat_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5'
  }, access)
  assert(repositories.projectAccountHealthFixtureSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), '后台恢复探针 Mock AI 账户应能通过健康检查激活')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '后台恢复探针 Mock AI 网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '后台恢复探针 Mock AI 网关 Key 未返回明文密钥')
  return {
    accountId: account.id,
    groupId: group.id,
    systemAccountId: access.systemAccountId,
    apiKey: apiKey.key
  }
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  content: string,
  clientIp = '198.51.100.10'
): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      session_id: content,
      messages: [{ role: 'user', content }],
      stream: false
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        path,
        bodyText,
        phase: upstreamPhase
      })
      if (req.method !== 'POST' || (path !== '/v1/chat/completions' && path !== '/v1/responses')) {
        sendJsonError(res, 404, 'mock ai path not found')
        return
      }
      if (upstreamPhase === 'failing') {
        res.destroy(new Error('mock ai transport interrupted'))
        return
      }
      if (path === '/v1/responses') {
        sendResponsesCompleted(res, 'juhe')
        return
      }
      sendChatCompletion(res)
    })
  })
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function sendChatCompletion(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'chatcmpl-runtime-recovery-probe-mock-ai',
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: 'mock ai recovered chat' },
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 5,
      completion_tokens: 3,
      total_tokens: 8
    }
  }))
}

function sendResponsesCompleted(res: http.ServerResponse, outputText: string): void {
  const completedEvent = {
    type: 'response.completed',
    response: {
      id: 'resp_runtime_recovery_probe_mock_ai',
      object: 'response',
      status: 'completed',
      output: [
        {
          id: 'msg_runtime_recovery_probe_mock_ai',
          type: 'message',
          status: 'completed',
          role: 'assistant',
          content: [{ type: 'output_text', text: outputText, annotations: [] }]
        }
      ],
      usage: {
        input_tokens: 1,
        output_tokens: 1,
        total_tokens: 2
      }
    }
  }
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
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

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) {
        throw error
      }
      if (attempt === 4) return
      await delay(250)
    }
  }
}
