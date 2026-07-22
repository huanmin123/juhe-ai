import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import type { AccountSummary } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-diagnostic-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const testSecret = 'account-diagnostic-mock-ai-secret'

process.env.JUHE_AI_DISABLE_BASE_ENV = 'true'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
process.env.JUHE_AI_SECRET = testSecret
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'

const { runtimeConfig } = await import('../../config/runtime.js')

assert.equal(
  process.env.JUHE_AI_SECRET,
  runtimeConfig.secret,
  'SQLite read worker 的 JUHE_AI_SECRET 必须在 config 和 crypto 初始化前与父进程一致'
)
assert.deepEqual(
  {
    disableBaseEnv: process.env.JUHE_AI_DISABLE_BASE_ENV,
    runtimeMode: runtimeConfig.runtimeMode,
    databaseDriver: runtimeConfig.databaseDriver,
    cacheDriver: runtimeConfig.cacheDriver,
    runtimeStateDriver: runtimeConfig.runtimeStateDriver,
    queueDriver: runtimeConfig.queueDriver
  },
  {
    disableBaseEnv: 'true',
    runtimeMode: 'standalone',
    databaseDriver: 'sqlite',
    cacheDriver: 'memory',
    runtimeStateDriver: 'memory',
    queueDriver: 'memory'
  },
  '账号诊断回归必须禁用基础 .env，并固定使用 standalone SQLite 与进程内驱动'
)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const { logger } = await import('../../shared/logger.js')
logger.level = 'silent'

const upstreamState = {
  hitsByKey: new Map<string, number>()
}

const [
  { GPT_OPENAI_V1_PROFILE_ID },
  { resolveOpenAIGatewayClientStrategy },
  { testOpenAIAccountWithDiagnosticRetries },
  { readGatewaySettings },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  auditLogQueue,
  databaseModule,
  repositories
] = await Promise.all([
  import('../../domain/provider-protocol.js'),
  import('../../modules/gateway/client-profiles/strategy.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

assert.equal(
  existsSync(new URL('../../modules/gateway/client-profiles/codex-switch-probe.ts', import.meta.url)),
  false,
  '旧 Codex 直接切号 probe 必须删除，客户端识别与重试协调由正式 strategy owner 管理'
)

const codexStrategy = resolveOpenAIGatewayClientStrategy(mockResponsesRequest('gpt-5.5'), {
  systemAccountId: 'diagnostic-strategy-system-account',
  apiKeyId: 'diagnostic-strategy-api-key',
  groupId: 'diagnostic-strategy-group',
  endpoint: '/v1/responses'
})
assert.equal(codexStrategy.clientProfile, 'codex', '带 turn metadata 的 Responses SSE 请求必须由正式 Codex client strategy 识别')
assert.equal(codexStrategy.requestClientCompatibility, 'codex_responses')
assert.equal(codexStrategy.retryCoordination.preCommitFailureSignal, 'protocol_error_event')
assert.equal(codexStrategy.allowCodexTurnAccountAvoidance, true)

let upstream: http.Server | undefined

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

  console.log('账号诊断 mock AI 回归通过：真实 mock 上游覆盖手动测试三档重试、持续失败不分类，并由正式 Codex client strategy 管理客户端重试协调')
} finally {
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

function mockResponsesRequest(model: string): Request {
  return {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    body: { model, stream: true },
    header: (name: string) => {
      if (name.toLowerCase() === 'accept') return 'text/event-stream'
      if (name.toLowerCase() === 'x-codex-turn-metadata') {
        return JSON.stringify({ turn_id: 'diagnostic-strategy-turn' })
      }
      return undefined
    }
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
