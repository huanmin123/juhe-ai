import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

type ScenarioName = 'chat_json' | 'chat_sse' | 'responses_json' | 'responses_sse' | 'stream_requested_json'

interface UpstreamHit {
  path: string
  authorization: string
  bodyText: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-response-inspection-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'response-inspection-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'response-inspection-gateway-e2e-secret'
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
  responseInspectionPolicies,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    responseInspectionPolicies.createResponseInspectionPolicy({
      name: '回归广告污染文本',
      enabled: true,
      priority: 1,
      scopeType: 'provider',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      match: {
        outputTextIncludes: ['公益服务器压力很大', 'dc.hhhl.cc', 'UniverseFederation']
      },
      action: 'retry_next_account',
      notes: 'response inspection gateway e2e regression'
    })

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await runScenario(baseUrl, upstreamBaseUrl, 'chat_json')
    await runScenario(baseUrl, upstreamBaseUrl, 'chat_sse')
    await runScenario(baseUrl, upstreamBaseUrl, 'responses_json')
    await runScenario(baseUrl, upstreamBaseUrl, 'responses_sse')
    await runScenario(baseUrl, upstreamBaseUrl, 'stream_requested_json')

    console.log('response inspection gateway e2e regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runScenario(baseUrl: string, upstreamBaseUrl: string, scenario: ScenarioName): Promise<void> {
  upstreamHits.length = 0
  const group = repositories.createGroup({
    name: `响应检查 E2E ${scenario}`,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    name: `响应检查污染账号 ${scenario}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-polluted-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    name: `响应检查干净账号 ${scenario}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-clean-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: `响应检查 E2E Key ${scenario}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')

  const endpoint = scenario.startsWith('chat') || scenario === 'stream_requested_json'
    ? '/v1/chat/completions'
    : '/v1/responses'
  const stream = scenario === 'chat_sse' || scenario === 'responses_sse' || scenario === 'stream_requested_json'
  const response = await fetch(`${baseUrl}${endpoint}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify(requestBodyForScenario(scenario, stream))
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, `${scenario} 应在污染账号命中策略后切到干净账号成功，实际 HTTP ${response.status}: ${responseText}`)
  assert.equal(upstreamHits.length, 2, `${scenario} 应先命中污染账号再服务端切到干净账号`)
  assert.equal(upstreamHits[0]?.authorization, `Bearer sk-upstream-polluted-${scenario}`, `${scenario} 第一次请求应命中污染账号`)
  assert.equal(upstreamHits[1]?.authorization, `Bearer sk-upstream-clean-${scenario}`, `${scenario} 第二次请求应命中干净账号`)
  assert.equal(upstreamHits.some((hit) => hit.bodyText.includes('公益服务器压力很大')), false, `${scenario} 客户端请求体不应携带污染文本`)
  assert(!responseText.includes('公益服务器压力很大'), `${scenario} 最终响应不应透出污染广告`)
  assert(!responseText.includes('dc.hhhl.cc'), `${scenario} 最终响应不应透出污染链接`)
  assert(responseText.includes(`clean ${scenario}`), `${scenario} 最终响应应来自干净账号：${responseText}`)
  if (stream) {
    assert.match(response.headers.get('content-type') ?? '', /text\/event-stream|application\/json/, `${scenario} 流式或上游 JSON 回退应有明确 content-type`)
  } else {
    assert.match(response.headers.get('content-type') ?? '', /application\/json/, `${scenario} 非流式客户端应收到 JSON`)
  }
}

function requestBodyForScenario(scenario: ScenarioName, stream: boolean): Record<string, unknown> {
  if (scenario.startsWith('responses')) {
    return {
      model: 'gpt-5.5',
      input: `run ${scenario}`,
      stream
    }
  }
  return {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: `run ${scenario}` }],
    stream
  }
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const authorization = String(req.headers.authorization ?? '')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({ path, authorization, bodyText })
      const scenario = scenarioFromAuthorization(authorization)
      const polluted = authorization.includes('polluted')
      if (path === '/v1/chat/completions') {
        if (scenario === 'chat_sse' && !polluted) {
          sendChatSse(res, scenario, false)
          return
        }
        if (scenario === 'chat_sse' && polluted) {
          sendChatSse(res, scenario, true)
          return
        }
        sendChatJson(res, scenario, polluted)
        return
      }
      if (path === '/v1/responses') {
        if (scenario === 'responses_sse') {
          sendResponsesSse(res, scenario, polluted)
          return
        }
        sendResponsesJson(res, scenario, polluted)
        return
      }
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { message: 'mock upstream path not found' } }))
    })
  })
}

function scenarioFromAuthorization(authorization: string): ScenarioName {
  const match = authorization.match(/sk-upstream-(?:polluted|clean)-([a-z_]+)/)
  assert(match?.[1], `无法从上游 Authorization 识别回归场景：${authorization}`)
  return match[1] as ScenarioName
}

function pollutedText(): string {
  return '公益服务器压力很大，欢迎加入 https://dc.hhhl.cc/chat/room/amlc1bekzi TG https://t.me/UniverseFederation'
}

function sendChatJson(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: polluted ? pollutedText() : `clean ${scenario}` },
        finish_reason: 'stop'
      }
    ],
    usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 }
  }))
}

function sendChatSse(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: { content: polluted ? pollutedText() : `clean ${scenario}` } }]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
    usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 }
  })}\n\n`)
  res.end('data: [DONE]\n\n')
}

function sendResponsesJson(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `resp-${scenario}`,
    status: 'completed',
    output_text: polluted ? pollutedText() : `clean ${scenario}`,
    usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
  }))
}

function sendResponsesSse(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({
    type: 'response.output_text.delta',
    delta: polluted ? pollutedText() : `clean ${scenario}`
  })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      id: `resp-${scenario}`,
      status: 'completed',
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  })}\n\n`)
  res.end()
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
