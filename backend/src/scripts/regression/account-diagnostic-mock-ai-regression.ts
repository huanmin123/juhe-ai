import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { isDiagnosticTimeoutSignal } from '../../modules/accounts/account-diagnostic-retry-policy.js'
import {
  gatewayDiagnosticAbortSourceFromSignal,
  markGatewayRequestAbortSource,
  gatewayRequestAbortSource
} from '../../modules/gateway/request/abort-attribution.js'
import { createMemoryGatewayRequest } from '../../modules/gateway/testing/memory-gateway-http.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-diagnostic-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-diagnostic-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const upstreamState = {
  hitsByKey: new Map<string, number>()
}

const [
  { testOpenAIAccountWithDiagnosticRetries },
  { probeCodexSwitchCandidateAccount },
  healthCheckService,
  cooldownRetestService,
  { readGatewaySettings },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  auditLogQueue,
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/client-profiles/codex-switch-probe.js'),
  import('../../modules/background/account-health-check.service.js'),
  import('../../modules/background/cooldown-account-retest.service.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

let upstream: http.Server | undefined
const originalAbortSignalTimeout = AbortSignal.timeout

const diagnosticAbortController = new AbortController()
const diagnosticAbortRequest = createMemoryGatewayRequest({
  method: 'POST',
  path: '/v1/responses',
  signal: diagnosticAbortController.signal,
  serverDiagnostic: true
})
const diagnosticAbortSourcePromise = new Promise<void>((resolve) => {
  diagnosticAbortRequest.once('aborted', () => resolve())
})
diagnosticAbortController.abort('account_diagnostic_deadline')
await diagnosticAbortSourcePromise
assert.equal(
  gatewayRequestAbortSource(diagnosticAbortRequest),
  'server_diagnostic_timeout',
  '服务端诊断 deadline 必须携带 server_diagnostic_timeout 来源，不能落为客户端断开'
)
assert.equal(
  gatewayDiagnosticAbortSourceFromSignal(AbortSignal.abort('manual_server_cancel')),
  'server_diagnostic_cancel'
)
assert.equal(
  isDiagnosticTimeoutSignal(AbortSignal.abort('account_circuit_probe_lease_deadline')),
  true,
  '服务端租约 deadline 必须保留为诊断超时，而不能降级为取消'
)
const ordinaryRequest = createMemoryGatewayRequest({ method: 'POST', path: '/v1/responses' })
markGatewayRequestAbortSource(ordinaryRequest, 'server_diagnostic_cancel')
assert.equal(gatewayRequestAbortSource(ordinaryRequest), 'server_diagnostic_cancel')

const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const nonStreamInspectionSource = readFileSync(new URL('../../modules/gateway/response/non-stream-json-inspection.ts', import.meta.url), 'utf8')
const usageRecordMapperSource = readFileSync(new URL('../../storage/usage-record-mappers.ts', import.meta.url), 'utf8')
assert.match(finalizationSource, /server_diagnostic_timeout|responsesFailedTerminal/)
assert.match(nonStreamInspectionSource, /upstream_protocol_failure/)
assert.match(usageRecordMapperSource, /upstream_protocol_failure:\s*'上游响应返回失败终态'/)
assert.doesNotMatch(usageRecordMapperSource, /upstream_protocol_failure:\s*'上游流式响应返回失败终态'/)
assert.doesNotMatch(
  readFileSync(new URL('../../modules/gateway/request/error-response.ts', import.meta.url), 'utf8'),
  /const statusCode = 499/,
  '服务端诊断取消不得使用客户端断开语义的 HTTP 499'
)

try {
  setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  upstream = createMockAIUpstream()
  await listen(upstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstream)}`

  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({
    name: '诊断 mock AI 回归分组',
    providerCode: 'gpt',
  }, access)

  const transientAccount = createMockAccount(group.id, upstreamBaseUrl, 'manual-transient', access)
  const transientProgress: number[] = []
  const transientResult = await testOpenAIAccountWithDiagnosticRetries(transientAccount, {
    model: 'gpt-5.5',
    testEndpointMode: 'responses_sse',
    onDiagnosticAttemptProgress: (progress) => {
      transientProgress.push(progress.timeoutMs)
    }
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(transientResult.success, true, `瞬态 mock AI 两次失败后应在第 3 次恢复：${transientResult.message}`)
  assert.equal(hitCount('manual-transient'), 3, '手动账号测试应在同一账号真实请求失败后按 10/20/30 三档重试到成功')
  assert.deepEqual(transientProgress, [10_000, 10_000, 20_000, 30_000], '模型目录预检和文本测试都应按各自的 10/20/30 秒诊断阶梯上报')

  const catalogTimeoutAccount = createMockAccount(group.id, upstreamBaseUrl, 'catalog-timeout', access)
  AbortSignal.timeout = ((timeoutMs: number) => originalAbortSignalTimeout(Math.min(timeoutMs, 20))) as typeof AbortSignal.timeout
  let catalogTimeoutResult: AccountTestResult
  try {
    catalogTimeoutResult = await testOpenAIAccountWithDiagnosticRetries(catalogTimeoutAccount, {
      model: 'gpt-5.5',
      testEndpointMode: 'responses_sse'
    })
  } finally {
    AbortSignal.timeout = originalAbortSignalTimeout
  }
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(catalogTimeoutResult.success, false, '模型目录持续超时不应继续执行真实模型测试')
  assert.equal(catalogTimeoutResult.errorCode, 'server_diagnostic_timeout', '模型目录诊断超时必须保留服务端 deadline 错误码')
  assert.equal(hitCount('catalog-timeout:models'), 3, '模型目录预检必须完整执行 10/20/30 三档探测')
  assert.equal(hitCount('catalog-timeout'), 0, '模型目录预检失败后不得继续发起真实模型测试')

  requiredRuntimeAccount(group.id, catalogTimeoutAccount.id, admin.id)
  const activeCatalogTimeoutAccount = repositories.findAccountSummary(catalogTimeoutAccount.id, access)
  assert(activeCatalogTimeoutAccount, '模型目录超时账户激活后必须可读取')
  AbortSignal.timeout = ((timeoutMs: number) => originalAbortSignalTimeout(Math.min(timeoutMs, 20))) as typeof AbortSignal.timeout
  try {
    assert.equal(healthCheckService.enqueueAccountHealthCheck(activeCatalogTimeoutAccount, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      maxPauseMinutes: 60
    }, 'request_failure'), true, '模型目录超时的独立健康检查必须成功投递')
    await waitForAccountHealthCheckQueueIdle(healthCheckService)
  } finally {
    AbortSignal.timeout = originalAbortSignalTimeout
  }
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  const catalogTimeoutAfterHealthCheck = repositories.findAccountSummary(catalogTimeoutAccount.id, access)
  assert.equal(hitCount('catalog-timeout:models'), 6, '自动健康检查的模型目录预检也必须完整执行三档探测')
  assert.equal(catalogTimeoutAfterHealthCheck?.status, 'temporary_unavailable', '模型目录完整诊断阶梯超时也必须立即临时不可调度')

  const persistentAccount = createMockAccount(group.id, upstreamBaseUrl, 'manual-persistent-401', access)
  const persistentResult = await testOpenAIAccountWithDiagnosticRetries(persistentAccount, { model: 'gpt-5.5', testEndpointMode: 'responses_sse' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(persistentResult.success, false, '持续 401 的 mock AI 不应被误判成功')
  assert.equal(persistentResult.statusCode, 401, '持续失败应保留最后一次真实上游 HTTP 状态')
  assert.equal(hitCount('manual-persistent-401'), 3, '手动账号测试不应按状态码分类提前停止，应完整走三次真实请求')

  const imageAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '诊断 mock AI image generation',
    type: 'api_key',
    groupId: group.id,
    status: 'active',
    supportedModels: ['gpt-image-2'],
    healthCheckModel: 'gpt-image-2',
    healthCheckEndpointMode: 'responses_sse',
    credentials: {
      api_key: 'sk-image-catalog',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse']
    }
  }, access)
  const imageResult = await testOpenAIAccountWithDiagnosticRetries(imageAccount)
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(imageResult.success, true, `图像模型真实生成探针应成功：${imageResult.message}`)
  assert.equal(imageResult.requestUrl, '/v1/images/generations', '图像生成模型必须调用 Images generations 探针')
  assert.equal(imageResult.testEndpointMode, 'images_json', '图像模型测试结果必须记录 Images API 请求形态')
  assert.equal(hitCount('image-catalog'), 1, '图像生成探针成功后应只请求一次上游')

  const codexFailedAccount = createMockAccount(group.id, upstreamBaseUrl, 'codex-explicit-failure', access)
  const codexFailedCandidate = requiredRuntimeAccount(group.id, codexFailedAccount.id, admin.id)
  const codexFailedResult = await probeCodexSwitchCandidateAccount(codexFailedCandidate, {
    req: mockResponsesRequest('gpt-5.5'),
    systemAccountId: admin.id,
    groupId: group.id
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(codexFailedResult.success, false, 'Codex 切号探针明确失败不应通过')
  assert.equal(
    hitCount('codex-explicit-failure'),
    1,
    `Codex 切号探针拿到明确失败后应立即淘汰候选，不在同账号烧完三档：${codexFailedResult.message}`
  )

  const codexTimeoutAccount = createMockAccount(group.id, upstreamBaseUrl, 'codex-timeout', access)
  const codexTimeoutCandidate = requiredRuntimeAccount(group.id, codexTimeoutAccount.id, admin.id)
  AbortSignal.timeout = ((timeoutMs: number) => originalAbortSignalTimeout(Math.min(timeoutMs, 20))) as typeof AbortSignal.timeout
  const codexTimeoutResult = await probeCodexSwitchCandidateAccount(codexTimeoutCandidate, {
    req: mockResponsesRequest('gpt-5.5'),
    systemAccountId: admin.id,
    groupId: group.id
  })
  AbortSignal.timeout = originalAbortSignalTimeout
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(codexTimeoutResult.success, false, '持续本地超时的 Codex 切号探针不应通过')
  assert.equal(codexTimeoutResult.errorCode, 'server_diagnostic_timeout', '账户诊断结果必须明确记录服务端诊断超时来源')
  assert.equal(hitCount('codex-timeout'), 3, 'Codex 切号探针只有本地超时时才应在同一候选账号递进三档')

  const healthTimeoutAccount = createMockAccount(group.id, upstreamBaseUrl, 'health-timeout', access)
  requiredRuntimeAccount(group.id, healthTimeoutAccount.id, admin.id)
  const activeHealthTimeoutAccount = repositories.findAccountSummary(healthTimeoutAccount.id, access)
  assert(activeHealthTimeoutAccount, '激活后的超时回归账户必须可读取')
  AbortSignal.timeout = ((timeoutMs: number) => originalAbortSignalTimeout(Math.min(timeoutMs, 20))) as typeof AbortSignal.timeout
  try {
    assert.equal(healthCheckService.enqueueAccountHealthCheck(activeHealthTimeoutAccount, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      maxPauseMinutes: 60
    }, 'request_failure'), true, '客户请求失败必须投递独立健康检查')
    await waitForAccountHealthCheckQueueIdle(healthCheckService)
  } finally {
    AbortSignal.timeout = originalAbortSignalTimeout
  }
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  const healthTimeoutAfterProbe = repositories.findAccountSummary(healthTimeoutAccount.id, access)
  assert.equal(hitCount('health-timeout'), 3, '后台健康检查在响应头前超时时必须完整执行三档探测')
  assert.equal(healthTimeoutAfterProbe?.status, 'temporary_unavailable', '真实上游尝试完整耗尽诊断阶梯后必须立即临时不可调度')
  assert.equal(healthTimeoutAfterProbe?.healthCheckFailureCount, 1, '完整诊断阶梯超时必须累计健康检查失败次数')
  assert.equal(
    repositories.findOpenAIAccountForGroup(group.id, healthTimeoutAccount.id, admin.id),
    undefined,
    '健康检查完整阶梯超时后账户不得继续作为可调度候选返回'
  )

  const cooldownTimeoutAccount = createMockAccount(group.id, upstreamBaseUrl, 'cooldown-timeout', access)
  requiredRuntimeAccount(group.id, cooldownTimeoutAccount.id, admin.id)
  assert.equal(
    repositories.markAccountTemporaryUnavailable(cooldownTimeoutAccount.id, '模拟冷却复测')?.status,
    'temporary_unavailable',
    '账户冷却写入必须成功'
  )
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1_000).toISOString(), cooldownTimeoutAccount.id)
  const cooldownCandidate = repositories.findAccountForCooldownRetest(cooldownTimeoutAccount.id)
  assert(cooldownCandidate, '进入冷却的超时账户必须成为复测候选')
  AbortSignal.timeout = ((timeoutMs: number) => originalAbortSignalTimeout(Math.min(timeoutMs, 20))) as typeof AbortSignal.timeout
  try {
    assert.equal(cooldownRetestService.enqueueCooldownAccountRetest(cooldownCandidate, {
      maxPauseMinutes: 60,
      maxRecoveryHours: 1
    }), true, '冷却超时账户必须投递复测')
    await waitForCooldownRetestQueueIdle(cooldownRetestService)
  } finally {
    AbortSignal.timeout = originalAbortSignalTimeout
  }
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  const cooldownTimeoutAfterProbe = repositories.findAccountSummary(cooldownTimeoutAccount.id, access)
  assert.equal(hitCount('cooldown-timeout'), 3, '冷却复测在响应头前超时时必须完整执行三档探测')
  assert.equal(cooldownTimeoutAfterProbe?.status, 'temporary_unavailable', '冷却复测超时不得提前恢复账户')
  assert.equal(cooldownTimeoutAfterProbe?.cooldownRetestFailureCount, 1, '冷却复测完整诊断阶梯超时必须累计上游失败次数')

  console.log('账号诊断 mock AI 回归通过：真实 mock 上游覆盖模型目录与手动测试三档重试、持续失败不分类、完整诊断阶梯超时的健康检查立即临时不可调度，以及冷却复测失败退避')
} finally {
  AbortSignal.timeout = originalAbortSignalTimeout
  auditLogQueue.flushAllAuditLogQueue()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await closeServer(upstream)
  await import('../../storage/sqlite-read-worker-pool.js')
    .then((module) => module.closeSqliteReadWorkerPool())
    .catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockAccount(
  groupId: string,
  upstreamBaseUrl: string,
  label: string,
  access: { systemAccountId: string; role: 'admin' }
): AccountSummary {
  return repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `诊断 mock AI ${label}`,
    type: 'api_key',
    groupId,
    status: 'active',
    supportedModels: ['gpt-5.2'],
    healthCheckModel: 'gpt-5.2',
    healthCheckEndpointMode: 'responses_sse',
    credentials: {
      api_key: `sk-${label}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_sse']
    }
  }, access)
}

function requiredRuntimeAccount(groupId: string, accountId: string, systemAccountId: string) {
  repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const account = repositories.findOpenAIAccountForGroup(groupId, accountId, systemAccountId, { ignoreAvailability: true })
  assert(account, `应能读取运行态账号 ${accountId}`)
  return account
}

function mockResponsesRequest(model: string): Request {
  return {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    body: { model, stream: true },
    header: (name: string) => name.toLowerCase() === 'accept' ? 'text/event-stream' : undefined
  } as unknown as Request
}

function createMockAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      chunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
    const key = upstreamKey(req.headers.authorization)
    if (req.method === 'GET' && url.pathname === '/v1/models') {
      incrementHit(`${key}:models`)
      if (key === 'catalog-timeout') {
        setTimeout(() => {
          if (!res.destroyed) {
            res.writeHead(200, { 'content-type': 'application/json' })
            res.end(JSON.stringify({ object: 'list', data: [{ id: 'gpt-image-2', object: 'model' }] }))
          }
        }, 200)
        return
      }
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ object: 'list', data: [{ id: 'gpt-image-2', object: 'model' }] }))
      return
      }
      const responseMode = mockResponseMode(url.pathname)
      if (req.method !== 'POST' || !responseMode) {
        sendJsonError(res, 404, 'mock path not found')
        return
      }
      const hit = incrementHit(key)
      if (key === 'manual-transient' && hit <= 2) {
        sendJsonError(res, 503, `manual transient failure ${hit}`)
        return
      }
      if (key === 'manual-persistent-401') {
        sendJsonError(res, 401, 'manual persistent unauthorized')
        return
      }
      if (key === 'codex-explicit-failure') {
        sendJsonError(res, 503, 'codex explicit probe failure')
        return
      }
      if (key === 'codex-timeout') {
        setTimeout(() => {
          if (!res.destroyed) {
            sendMockCompleted(res, responseMode, 'OK')
          }
        }, 200)
        return
      }
      if (key === 'health-timeout') {
        setTimeout(() => {
          if (!res.destroyed) {
            sendMockCompleted(res, responseMode, 'OK')
          }
        }, 200)
        return
      }
      if (key === 'cooldown-timeout') {
        setTimeout(() => {
          if (!res.destroyed) {
            sendMockCompleted(res, responseMode, 'OK')
          }
        }, 200)
        return
      }
      sendMockCompleted(res, responseMode, 'OK')
    })
  })
}

function mockResponseMode(pathname: string): 'responses' | 'chat' | 'image' | undefined {
  if (pathname === '/v1/responses') return 'responses'
  if (pathname === '/v1/chat/completions') return 'chat'
  if (pathname === '/v1/images/generations') return 'image'
  return undefined
}

function upstreamKey(authorization: string | string[] | undefined): string {
  const value = Array.isArray(authorization) ? authorization[0] : authorization
  return String(value ?? '').replace(/^Bearer\s+/i, '').replace(/^sk-/, '')
}

function incrementHit(key: string): number {
  const next = (upstreamState.hitsByKey.get(key) ?? 0) + 1
  upstreamState.hitsByKey.set(key, next)
  return next
}

function hitCount(key: string): number {
  return upstreamState.hitsByKey.get(key) ?? 0
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function sendResponsesCompleted(res: http.ServerResponse, outputText: string): void {
  const completedEvent = {
    type: 'response.completed',
    response: {
      id: 'resp_account_diagnostic_mock_ai',
      object: 'response',
      status: 'completed',
      output: [
        {
          type: 'message',
          role: 'assistant',
          content: [{ type: 'output_text', text: outputText }]
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

function sendChatCompleted(res: http.ServerResponse, outputText: string): void {
  const chunk = {
    id: 'chatcmpl_account_diagnostic_mock_ai',
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: { content: outputText }, finish_reason: null }]
  }
  const done = {
    id: 'chatcmpl_account_diagnostic_mock_ai',
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
    usage: {
      prompt_tokens: 1,
      completion_tokens: 1,
      total_tokens: 2
    }
  }
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.end(`data: ${JSON.stringify(chunk)}\n\ndata: ${JSON.stringify(done)}\n\ndata: [DONE]\n\n`)
}

function sendMockCompleted(res: http.ServerResponse, mode: 'responses' | 'chat' | 'image', outputText: string): void {
  if (mode === 'image') {
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ created: 1, data: [{ b64_json: 'aGVsbG8=' }] }))
    return
  }
  if (mode === 'chat') {
    sendChatCompleted(res, outputText)
    return
  }
  sendResponsesCompleted(res, outputText)
}

async function listen(server: http.Server): Promise<void> {
  server.listen(0, '127.0.0.1')
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string', 'mock AI upstream should be listening')
  return address.port
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

async function waitForAccountHealthCheckQueueIdle(
  healthCheckService: typeof import('../../modules/background/account-health-check.service.js')
): Promise<void> {
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    const snapshot = healthCheckService.getAccountHealthCheckQueueSnapshot()
    if (snapshot.pendingCount === 0 && snapshot.runningCount === 0) return
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  throw new Error('账户健康检查队列未在 5 秒内完成')
}

async function waitForCooldownRetestQueueIdle(
  cooldownRetestService: typeof import('../../modules/background/cooldown-account-retest.service.js')
): Promise<void> {
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    const snapshot = cooldownRetestService.getCooldownAccountRetestQueueSnapshot()
    if (snapshot.pendingCount === 0 && snapshot.runningCount === 0) return
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  throw new Error('冷却账户复测队列未在 5 秒内完成')
}
