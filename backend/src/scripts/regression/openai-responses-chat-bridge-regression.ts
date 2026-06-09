import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { captureGatewayRawBody } from '../../modules/gateway/openai-gateway-request-body-middleware.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-responses-chat-bridge-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'responses-chat-bridge-regression-secret'
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
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

const upstreamRequests: Array<{ path: string; body: Record<string, unknown>; authorization: string }> = []
let upstreamScenario: 'json' | 'stream' = 'json'

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockChatUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
    const group = repositories.createGroup({
      name: 'Responses 转 Chat 回归分组',
      providerCode: 'gpt',
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: 'gpt',
      name: 'Responses 转 Chat 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-responses-chat-bridge-upstream',
        base_url: upstreamBaseUrl
      },
      openAIResponsesUpstreamMode: 'chat_completions_bridge',
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(account.openAIResponsesUpstreamMode, 'chat_completions_bridge', '账户应保存 Responses 上游桥接模式')
    assert.equal(repositories.findAccountSummary(account.id, access)?.openAIResponsesUpstreamMode, 'chat_completions_bridge', '账户详情应回显 Responses 上游桥接模式')
    const runtimeAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
    assert.equal(runtimeAccount?.openAIResponsesUpstreamMode, 'chat_completions_bridge', '网关运行时账号应携带 Responses 上游桥接模式')

    const apiKey = repositories.createApiKeyRecord({
      name: 'Responses 转 Chat 回归 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    upstreamScenario = 'json'
    const jsonResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        instructions: '只输出短句',
        input: 'hello bridge',
        max_output_tokens: 8,
        stream: false,
        tools: [{
          type: 'function',
          name: 'noop',
          parameters: { type: 'object', properties: {} }
        }]
      })
    })
    const jsonText = await jsonResponse.text()
    assert.equal(jsonResponse.status, 200, `非流式桥接请求应成功，实际 HTTP ${jsonResponse.status}: ${jsonText}`)
    const responsesBody = parseJsonObject(jsonText)
    assert.equal(responsesBody.object, 'response', '非流式桥接响应应转回 Responses object')
    assert.equal(responsesBody.output_text, 'bridge json ok', '非流式桥接响应应提供 output_text')
    assert.equal(upstreamRequests.at(-1)?.path, '/v1/chat/completions', '桥接请求应投递到上游 Chat Completions')
    assert.equal(upstreamRequests.at(-1)?.authorization, 'Bearer sk-responses-chat-bridge-upstream')
    assert.equal(upstreamRequests.at(-1)?.body.max_tokens, 8, 'max_output_tokens 应映射为 Chat max_tokens')
    assert.deepEqual(upstreamRequests.at(-1)?.body.messages, [
      { role: 'system', content: '只输出短句' },
      { role: 'user', content: 'hello bridge' }
    ])

    upstreamScenario = 'stream'
    const streamResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'stream bridge' }] }],
        stream: true
      })
    })
    const streamText = await streamResponse.text()
    assert.equal(streamResponse.status, 200, `流式桥接请求应成功，实际 HTTP ${streamResponse.status}: ${streamText}`)
    assert.match(streamText, /event: response\.output_text\.delta/, '流式 Chat delta 应转换成 Responses output_text delta')
    assert.match(streamText, /event: response\.completed/, '流式 Chat DONE 应转换成 Responses completed')
    assert.match(streamText, /bridge stream ok/, '流式桥接应保留可见输出')
    assert.equal(upstreamRequests.at(-1)?.path, '/v1/chat/completions')
    assert.equal(upstreamRequests.at(-1)?.body.stream, true)

    const unsupportedHitsBefore = upstreamRequests.length
    const unsupportedResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'unsupported tool',
        tools: [{ type: 'web_search_preview' }]
      })
    })
    const unsupportedText = await unsupportedResponse.text()
    assert.equal(unsupportedResponse.status, 400, `不支持的 Responses 工具应本地拒绝，实际 HTTP ${unsupportedResponse.status}: ${unsupportedText}`)
    assert.match(unsupportedText, /暂不支持转为 Chat Completions/)
    assert.equal(upstreamRequests.length, unsupportedHitsBefore, '本地不支持桥接请求不应命中上游')
    const afterUnsupported = repositories.findAccountSummary(account.id, access)
    assert.equal(afterUnsupported?.status, 'active', '本地不支持桥接请求不应写账号失败状态')
    assert.equal(afterUnsupported?.lastErrorMessage, undefined, '本地不支持桥接请求不应写账号最近错误')

    console.log('Responses 转 Chat Completions 账户桥接回归通过')
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

function createMockChatUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const body = parseJsonObject(Buffer.concat(chunks).toString('utf8'))
      upstreamRequests.push({
        path: req.url?.split('?', 1)[0] ?? '',
        body,
        authorization: String(req.headers.authorization ?? '')
      })
      if (req.url?.split('?', 1)[0] !== '/v1/chat/completions') {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { message: 'not found' } }))
        return
      }
      if (upstreamScenario === 'stream') {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl_bridge_stream',
          object: 'chat.completion.chunk',
          model: body.model,
          choices: [{ index: 0, delta: { role: 'assistant' } }]
        })}\n\n`)
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl_bridge_stream',
          object: 'chat.completion.chunk',
          model: body.model,
          choices: [{ index: 0, delta: { content: 'bridge stream ok' }, finish_reason: 'stop' }]
        })}\n\n`)
        res.end('data: [DONE]\n\n')
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl_bridge_json',
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model: body.model,
        choices: [{
          index: 0,
          message: { role: 'assistant', content: 'bridge json ok' },
          finish_reason: 'stop'
        }],
        usage: {
          prompt_tokens: 3,
          completion_tokens: 2,
          total_tokens: 5
        }
      }))
    })
  })
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
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
