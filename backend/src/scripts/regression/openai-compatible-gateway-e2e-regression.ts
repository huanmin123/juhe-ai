import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-compatible-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'openai-compatible-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'openai-compatible-gateway-e2e-secret'
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
  auditLogQueue,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
let upstreamHitCount = 0
let upstreamAuthorization = ''
let upstreamPath = ''
let upstreamRequestBody = ''
const chatToResponsesSourceModel = 'openai-compatible-chat-to-responses-source'
const chatToResponsesUpstreamModel = 'openai-compatible-responses-upstream'
const responsesToChatSourceModel = 'openai-compatible-responses-source'
const responsesToChatUpstreamModel = 'openai-compatible-chat-upstream'

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    registerCustomModels()
    upstreamServer = createOpenAICompatibleUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({
      name: '通用 OpenAI 兼容网关 E2E 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    assert.throws(() => repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '通用 OpenAI 兼容网关非法 Chat 到 Responses 映射账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-openai-compatible-upstream-invalid',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['responses_json', 'responses_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      modelMappings: [{
        sourceModel: chatToResponsesSourceModel,
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: chatToResponsesUpstreamModel,
        upstreamEndpointFamily: 'responses',
        enabled: true
      }]
    }, access), /账号模型别名只支持同协议映射|请改用混合供应商账户/, '通用 OpenAI-compatible 账号必须拒绝 Chat -> Responses 映射')

    const account = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '通用 OpenAI 兼容网关 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-openai-compatible-upstream',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      skipInitialHealthCheck: true,
      schedulable: true,
      supportedModels: ['gpt-5.5', responsesToChatUpstreamModel],
      modelMappings: [{
        sourceModel: responsesToChatSourceModel,
        sourceEndpointFamily: 'responses',
        upstreamModel: responsesToChatUpstreamModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }]
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '通用 OpenAI 兼容网关 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{ role: 'user', content: 'hello generic openai provider' }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `通用 openai 供应商网关请求应成功，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
    assert.equal(body.choices?.[0]?.message?.content, 'generic openai provider ok')
    assert.equal(upstreamHitCount, 1, '通用 openai 供应商应命中一次 mock 上游')
    assert.equal(upstreamPath, '/v1/chat/completions')
    assert.equal(upstreamAuthorization, 'Bearer sk-openai-compatible-upstream')
    assert.match(upstreamRequestBody, /generic openai provider/)

    const bridgeResponse = await fetch(`${baseUrl}/v1/responses?trace=openai-compatible-responses-to-chat`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json',
        accept: 'text/event-stream'
      },
      body: JSON.stringify({
        model: responsesToChatSourceModel,
        input: 'hello generic openai responses bridge',
        stream: true
      })
    })
    const bridgeText = await bridgeResponse.text()
    assert.equal(bridgeResponse.status, 200, `通用 openai 供应商 Responses -> Chat 映射请求应成功，实际 HTTP ${bridgeResponse.status}: ${bridgeText}`)
    assert.match(bridgeResponse.headers.get('content-type') ?? '', /text\/event-stream/)
    assert.match(bridgeText, /response\.completed/, 'Responses -> Chat 映射响应应渲染 Responses 完成事件')
    assert.match(bridgeText, /generic openai provider stream ok/, 'Chat SSE 内容应转成 Responses 文本事件')
    assert.equal(upstreamHitCount, 2, 'Responses -> Chat bridge 应追加命中一次 mock 上游')
    assert.equal(upstreamPath, '/v1/chat/completions?trace=openai-compatible-responses-to-chat')
    const bridgeUpstreamBody = JSON.parse(upstreamRequestBody) as { model?: string; stream?: boolean; messages?: unknown[] }
    assert.equal(bridgeUpstreamBody.model, responsesToChatUpstreamModel, 'Responses -> Chat 映射必须改写上游 Chat 模型')
    assert.equal(bridgeUpstreamBody.stream, true, 'Responses -> Chat bridge 必须使用 Chat SSE')
    assert(Array.isArray(bridgeUpstreamBody.messages), 'Responses -> Chat bridge 必须生成 Chat messages')

    const updated = repositories.findAccountSummary(account.id, access)
    assert.equal(updated?.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, '命中账号应保持通用 openai providerCode')
    assert.equal(updated?.status, 'active', '成功请求不应改写账号状态')
    assert.equal(updated?.schedulable, true, '成功请求不应关闭账号调度')

    console.log('openai compatible gateway e2e regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerCustomModels(): void {
  for (const model of [chatToResponsesSourceModel, chatToResponsesUpstreamModel]) {
    saveCustomProviderModel({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      model,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions', 'responses'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 2,
      actorSystemAccountId: access.systemAccountId
    })
  }
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: responsesToChatSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: responsesToChatUpstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
}

function createOpenAICompatibleUpstream(): http.Server {
  return http.createServer((req, res) => {
    upstreamHitCount += 1
    upstreamPath = req.url ?? ''
    upstreamAuthorization = String(req.headers.authorization ?? '')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      upstreamRequestBody = Buffer.concat(chunks).toString('utf8')
      if ((req.url ?? '').split('?', 1)[0] === '/v1/responses') {
        const requestBody = JSON.parse(upstreamRequestBody) as { stream?: boolean; tools?: unknown[] }
        if (requestBody.stream === true) {
          res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
          res.write('event: response.created\ndata: {"type":"response.created","response":{"id":"resp-openai-compatible-e2e","model":"openai-compatible-responses-upstream","status":"in_progress"}}\n\n')
          res.write('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"responses-stream-ok"}\n\n')
          res.end('event: response.completed\ndata: {"type":"response.completed","response":{"id":"resp-openai-compatible-e2e","model":"openai-compatible-responses-upstream","status":"completed","usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}}\n\n')
          return
        }
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'resp-openai-compatible-e2e',
          object: 'response',
          created_at: 1782259200,
          status: 'completed',
          model: 'openai-compatible-responses-upstream',
          output: Array.isArray(requestBody.tools) && requestBody.tools.length > 0
            ? [{
                id: 'fc-openai-compatible-e2e',
                type: 'function_call',
                status: 'completed',
                call_id: 'call-openai-compatible-e2e',
                name: 'lookup',
                arguments: '{"query":"ping"}'
              }]
            : [{
                id: 'msg-openai-compatible-e2e',
                type: 'message',
                role: 'assistant',
                status: 'completed',
                content: [{ type: 'output_text', text: 'responses-json-ok' }]
              }],
          usage: {
            input_tokens: 5,
            output_tokens: 6,
            total_tokens: 11
          }
        }))
        return
      }
      const requestBody = JSON.parse(upstreamRequestBody) as { stream?: boolean }
      if (requestBody.stream === true) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache'
        })
        res.write('data: {"id":"chatcmpl-openai-compatible-e2e-stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"generic openai provider stream ok"},"finish_reason":null}]}\n\n')
        res.write('data: {"id":"chatcmpl-openai-compatible-e2e-stream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}\n\n')
        res.end('data: [DONE]\n\n')
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-openai-compatible-e2e',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'generic openai provider ok' },
            finish_reason: 'stop'
          }
        ],
        usage: {
          input_tokens: 3,
          output_tokens: 4,
          total_tokens: 7
        }
      }))
    })
  })
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
