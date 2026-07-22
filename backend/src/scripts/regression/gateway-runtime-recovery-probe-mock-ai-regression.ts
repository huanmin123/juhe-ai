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
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
  bodyText: string
  phase: 'failing' | 'recovered'
}

interface GatewayScenario {
  accountId: string
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
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: MockUpstreamHit[] = []
let upstreamPhase: MockUpstreamHit['phase'] = 'failing'

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
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

    await assertUserFailureOnlySchedulesBackgroundRecovery(baseUrl, scenario)
    await assertBackgroundProbeRecoversWithoutUserTraffic(baseUrl, scenario)

    console.log('gateway runtime recovery probe mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
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
  assert.equal(maxRunningCount, 3, 'server 恢复探针和 precheck 必须共享最多 3 路自动诊断门禁')
}

async function assertUserFailureOnlySchedulesBackgroundRecovery(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  upstreamPhase = 'failing'
  const response = await postChat(baseUrl, scenario.apiKey, 'first user request should only discover failure')
  assert(response.status >= 500, `Mock AI 失败阶段应返回网关失败，实际 HTTP ${response.status}: ${response.text}`)
  assert(upstreamHits.some((hit) => hit.phase === 'failing'), '失败阶段应真实命中 Mock AI 上游')

  const runtime = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountId]
  assert.notEqual(runtime?.status, 'degraded', '用户请求失败不应直接激活账号调度降级')
  assert.notEqual(runtime?.status, 'precheck_pending', '用户请求失败不应绕过观察窗口直接进入事前确认')
  assert(
    accountSideEffects.getGatewayAccountSideEffectState().recoveryProbePendingAccountCount >= 1,
    '用户请求失败后应调度后台恢复探针'
  )
}

async function assertBackgroundProbeRecoversWithoutUserTraffic(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const recoveredHitStart = upstreamHits.length
  upstreamPhase = 'recovered'
  await accountSideEffects.flushGatewayAccountRecoveryProbesForTest()

  const probeHits = upstreamHits.slice(recoveredHitStart)
  assert(
    probeHits.some((hit) => hit.authorization === 'Bearer sk-runtime-recovery-probe' && hit.path === '/v1/responses'),
    `后台恢复探针应在无用户请求时命中 Mock AI responses 探针链路，实际命中：${JSON.stringify(probeHits)}`
  )
  assert.equal(
    accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountId],
    undefined,
    '后台恢复探针成功后应清理账号本地运行态'
  )

  const response = await postChat(baseUrl, scenario.apiKey, 'user request after background probe should recover')
  assert.equal(response.status, 200, `后台探针恢复后真实用户请求应成功，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai recovered chat/, '恢复后的用户请求应返回 Mock AI 正常响应')
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
  assert(repositories.recordAccountHealthCheckSuccess(account.id, {
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
    apiKey: apiKey.key
  }
}

async function postChat(baseUrl: string, apiKey: string, content: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
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
        sendJsonError(res, 503, 'mock ai temporary failure')
        return
      }
      if (path === '/v1/responses') {
        sendResponsesCompleted(res, 'OK')
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
