import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountSummary } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

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
  { readGatewaySettings },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  auditLogQueue,
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/client-profiles/codex-switch-probe.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

let upstream: http.Server | undefined
const originalAbortSignalTimeout = AbortSignal.timeout

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
    onDiagnosticAttemptProgress: (progress) => {
      transientProgress.push(progress.timeoutMs)
    }
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(transientResult.success, true, `瞬态 mock AI 两次失败后应在第 3 次恢复：${transientResult.message}`)
  assert.equal(hitCount('manual-transient'), 3, '手动账号测试应在同一账号真实请求失败后按 10/20/30 三档重试到成功')
  assert.deepEqual(transientProgress, [10_000, 20_000, 30_000], '手动账号测试进度应按 10s、20s、30s 上报')

  const persistentAccount = createMockAccount(group.id, upstreamBaseUrl, 'manual-persistent-401', access)
  const persistentResult = await testOpenAIAccountWithDiagnosticRetries(persistentAccount, { model: 'gpt-5.5' })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(persistentResult.success, false, '持续 401 的 mock AI 不应被误判成功')
  assert.equal(persistentResult.statusCode, 401, '持续失败应保留最后一次真实上游 HTTP 状态')
  assert.equal(hitCount('manual-persistent-401'), 3, '手动账号测试不应按状态码分类提前停止，应完整走三次真实请求')

  const imageAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '诊断 mock AI image catalog',
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
  assert.equal(imageResult.success, true, `图像模型目录探针应成功：${imageResult.message}`)
  assert.equal(imageResult.requestUrl, '/v1/models', '图像生成模型不得再调用文本 Responses 探针')
  assert.equal(imageResult.message, 'OpenAI 模型目录 测试通过', '图像模型探针结果不得误报为 Responses 测试')
  assert.equal(hitCount('image-catalog'), 1, '模型目录探针成功后不得重复请求或触发生图')

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
  assert.equal(hitCount('codex-timeout'), 3, 'Codex 切号探针只有本地超时时才应在同一候选账号递进三档')

  console.log('账号诊断 mock AI 回归通过：真实 mock 上游覆盖手动测试三档重试、持续失败不分类、Codex 明确失败立即换号和本地超时三档递进')
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
    credentials: {
      api_key: `sk-${label}`,
      base_url: upstreamBaseUrl
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
      if (req.method === 'GET' && url.pathname === '/v1/models') {
        const key = upstreamKey(req.headers.authorization)
        incrementHit(key)
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ object: 'list', data: [{ id: 'gpt-image-2', object: 'model' }] }))
        return
      }
      const responseMode = mockResponseMode(url.pathname)
      if (req.method !== 'POST' || !responseMode) {
        sendJsonError(res, 404, 'mock path not found')
        return
      }
      const key = upstreamKey(req.headers.authorization)
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
      sendMockCompleted(res, responseMode, 'OK')
    })
  })
}

function mockResponseMode(pathname: string): 'responses' | 'chat' | undefined {
  if (pathname === '/v1/responses') return 'responses'
  if (pathname === '/v1/chat/completions') return 'chat'
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

function sendMockCompleted(res: http.ServerResponse, mode: 'responses' | 'chat', outputText: string): void {
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
