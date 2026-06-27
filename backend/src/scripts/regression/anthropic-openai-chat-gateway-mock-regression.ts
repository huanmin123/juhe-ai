import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ProviderCode } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface MessagesBridgeCase {
  label: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  upstreamModel: string
  upstreamApiKey: string
  baseUrl: (origin: string) => string
  expectedPath: string
}

interface UpstreamHit {
  authorization: string
  body: Record<string, unknown>
  bodyText: string
  contentType: string
  accept: string
  anthropicVersion: string
  method: string
  path: string
  rawUrl: string
  xApiKey: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-anthropic-openai-chat-gateway-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'anthropic-openai-chat-gateway-mock.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'anthropic-openai-chat-gateway-mock-secret'
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
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const sourceModel = 'claude-messages-to-chat-source'
const upstreamHits: UpstreamHit[] = []
const cases: MessagesBridgeCase[] = [
  {
    label: 'openai-compatible',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    upstreamModel: 'openai-compatible-messages-chat-upstream',
    upstreamApiKey: 'sk-openai-compatible-messages-chat-upstream',
    baseUrl: (origin) => `${origin}/openai-compatible`,
    expectedPath: '/openai-compatible/v1/chat/completions'
  },
  {
    label: 'gpt',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    upstreamModel: 'gpt-messages-chat-upstream',
    upstreamApiKey: 'sk-gpt-messages-chat-upstream',
    baseUrl: (origin) => `${origin}/gpt`,
    expectedPath: '/gpt/v1/chat/completions'
  },
  {
    label: 'deepseek',
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    upstreamModel: 'deepseek-messages-chat-upstream',
    upstreamApiKey: 'sk-deepseek-messages-chat-upstream',
    baseUrl: (origin) => `${origin}/deepseek`,
    expectedPath: '/deepseek/v1/chat/completions'
  },
  {
    label: 'glm',
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    upstreamModel: 'glm-messages-chat-upstream',
    upstreamApiKey: 'sk-glm-messages-chat-upstream',
    baseUrl: (origin) => `${origin}/glm/v1`,
    expectedPath: '/glm/v1/chat/completions'
  }
]

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
    upstreamServer = createMockChatUpstream()
    await listen(upstreamServer)
    const upstreamOrigin = `http://127.0.0.1:${serverAddress(upstreamServer).port}`
    const runtimes = cases.map((item) => createCaseRuntime(item, upstreamOrigin))

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    for (const runtime of runtimes) {
      await assertMessagesToChatJson(baseUrl, runtime)
      await assertMessagesToChatSse(baseUrl, runtime)
    }
    await assertUnsupportedMessagesCapabilityGuidance(baseUrl, runtimes[0]!)

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    assertUsageAndAuditRecords(runtimes)

    console.log('anthropic openai chat gateway mock regression passed')
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
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerCustomModels(): void {
  const sourceCatalogModel = saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: sourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  assert.deepEqual(sourceCatalogModel.supportedApiProtocols, ['messages'], 'Messages source 模型目录应保留 messages 协议能力')
  for (const item of cases) {
    saveCustomProviderModel({
      providerCode: item.providerCode,
      model: item.upstreamModel,
      scope: 'global',
      status: 'active',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 2,
      actorSystemAccountId: access.systemAccountId
    })
  }
}

function createCaseRuntime(item: MessagesBridgeCase, upstreamOrigin: string): MessagesBridgeCase & {
  accountId: string
  groupId: string
  localApiKey: string
} {
  const group = repositories.createGroup({
    name: `${item.label} Messages -> Chat 桥接分组`,
    providerCode: item.providerCode,
    providerProtocolProfileId: item.providerProtocolProfileId,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: item.providerCode,
    providerProtocolProfileId: item.providerProtocolProfileId,
    name: `${item.label} Messages -> Chat 桥接账号`,
    type: 'api_key',
    clientCompatibility: 'openai_standard',
    credentials: {
      api_key: item.upstreamApiKey,
      base_url: item.baseUrl(upstreamOrigin),
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [item.upstreamModel]
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: `${item.label} Messages -> Chat 桥接 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    explicitHybridRouteRules: [
      {
        id: 'messages_to_chat',
        enabled: true,
        priority: 1,
        sourceClientProfile: 'auto',
        sourceEndpointFamily: 'messages',
        sourceModel,
        targetGroupId: group.id,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: item.upstreamModel,
        adapterMode: 'bridge'
      }
    ],
    status: 'active'
  }, access)
  assert(apiKey.key, `${item.label} 回归 API Key 未返回明文密钥`)
  return {
    ...item,
    accountId: account.id,
    groupId: group.id,
    localApiKey: apiKey.key
  }
}

async function assertMessagesToChatJson(
  baseUrl: string,
  runtime: MessagesBridgeCase & { localApiKey: string }
): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${runtime.localApiKey}`,
      'content-type': 'application/json',
      'anthropic-version': '2023-06-01'
    },
    body: JSON.stringify(messagesRequest(runtime.label, false))
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${runtime.label} Messages -> Chat JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /application\/json/)
  const body = JSON.parse(text) as {
    type?: string
    model?: string
    role?: string
    content?: Array<{ type?: string; text?: string; name?: string; input?: unknown }>
    stop_reason?: string
    usage?: { input_tokens?: number; output_tokens?: number }
  }
  assert.equal(body.type, 'message', `${runtime.label} JSON 下游响应应是 Anthropic message`)
  assert.equal(body.role, 'assistant')
  assert.equal(body.model, runtime.upstreamModel)
  assert.equal(body.content?.[0]?.type, 'text')
  assert.equal(body.content?.[0]?.text, `messages-json-ok:${runtime.label}`)
  assert.equal(body.content?.[1]?.type, 'tool_use')
  assert.equal(body.content?.[1]?.name, 'lookup')
  assert.deepEqual(body.content?.[1]?.input, { query: runtime.label })
  assert.equal(body.stop_reason, 'tool_use')
  assert.equal(body.usage?.input_tokens, 5)
  assert.equal(body.usage?.output_tokens, 6)

  const hit = singleProviderHit(start, runtime.label)
  assertChatBridgeUpstreamHit(hit, runtime, false)
}

async function assertMessagesToChatSse(
  baseUrl: string,
  runtime: MessagesBridgeCase & { localApiKey: string }
): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${runtime.localApiKey}`,
      accept: 'text/event-stream',
      'content-type': 'application/json',
      'anthropic-version': '2023-06-01'
    },
    body: JSON.stringify(messagesRequest(runtime.label, true))
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${runtime.label} Messages -> Chat SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /event:\s*message_start/, `${runtime.label} SSE 应输出 Anthropic message_start`)
  assert.match(text, /event:\s*content_block_delta/, `${runtime.label} SSE 应输出 Anthropic content_block_delta`)
  assert.match(text, new RegExp(`messages-stream-ok:${escapeRegExp(runtime.label)}`), `${runtime.label} SSE 应输出上游 Chat 文本 delta`)
  assert.match(text, /event:\s*message_delta/, `${runtime.label} SSE 应输出 Anthropic message_delta`)
  assert.match(text, /"output_tokens":8/, `${runtime.label} SSE 应保留 usage output_tokens`)
  assert.match(text, /event:\s*message_stop/, `${runtime.label} SSE 应输出 Anthropic message_stop`)
  assert.doesNotMatch(text, /\[DONE\]/, `${runtime.label} SSE 不应把 OpenAI [DONE] 原样透给 Messages 客户端`)

  const hit = singleProviderHit(start, runtime.label)
  assertChatBridgeUpstreamHit(hit, runtime, true)
}

async function assertUnsupportedMessagesCapabilityGuidance(
  baseUrl: string,
  runtime: MessagesBridgeCase & { localApiKey: string }
): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${runtime.localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      ...messagesRequest(runtime.label, false),
      thinking: { type: 'enabled', budget_tokens: 1024 }
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `不支持的 Messages 能力应返回 agent 引导而不是网关错误，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { type?: string; content?: Array<{ text?: string }>; usage?: { input_tokens?: number; output_tokens?: number } }
  assert.equal(body.type, 'message', 'Messages 引导响应应保持 Anthropic message 形态')
  assert.match(body.content?.[0]?.text ?? '', /不支持 Anthropic Messages 的 thinking 字段|不支持.*thinking/, '引导内容应明确指出 thinking 不支持')
  assert.match(body.content?.[0]?.text ?? '', /客户端|agent|MCP|上游/, '引导内容应指向客户端或真实上游能力修复')
  assert.equal(body.usage?.input_tokens, 0)
  assert.equal(body.usage?.output_tokens, 0)
  assert.equal(upstreamHits.length, start, '能力引导不应命中 Chat-only 上游')
}

function messagesRequest(label: string, stream: boolean): Record<string, unknown> {
  return {
    model: sourceModel,
    max_tokens: 64,
    system: [{ type: 'text', text: `system for ${label}` }],
    messages: [
      {
        role: 'user',
        content: [
          { type: 'text', text: `hello ${label}` },
          {
            type: 'image',
            source: {
              type: 'url',
              url: `https://example.com/${label}.png`
            }
          }
        ]
      },
      {
        role: 'assistant',
        content: [
          { type: 'text', text: 'assistant requested lookup' },
          { type: 'tool_use', id: `toolu_${label}`, name: 'lookup', input: { query: label } }
        ]
      },
      {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            tool_use_id: `toolu_${label}`,
            content: [{ type: 'text', text: `tool result for ${label}` }]
          },
          { type: 'text', text: `final ${label}` }
        ]
      }
    ],
    tools: [{
      name: 'lookup',
      description: 'lookup test data',
      input_schema: {
        type: 'object',
        properties: { query: { type: 'string' } },
        required: ['query']
      }
    }],
    tool_choice: { type: 'tool', name: 'lookup' },
    metadata: { user_id: `user-${label}` },
    stop_sequences: ['STOP'],
    temperature: 0.1,
    stream
  }
}

function assertChatBridgeUpstreamHit(
  hit: UpstreamHit,
  runtime: MessagesBridgeCase,
  stream: boolean
): void {
  assert.equal(hit.method, 'POST')
  assert.equal(hit.path, runtime.expectedPath, `${runtime.label} 上游路径应桥接到 Chat Completions`)
  assert.equal(hit.authorization, `Bearer ${runtime.upstreamApiKey}`)
  assert.equal(hit.xApiKey, '', `${runtime.label} 上游不应保留 Anthropic x-api-key`)
  assert.equal(hit.anthropicVersion, '', `${runtime.label} 上游不应保留 anthropic-version`)
  assert.match(hit.contentType, /application\/json/)
  assert.match(hit.accept, stream ? /text\/event-stream/ : /application\/json/)
  assert.equal(hit.body.model, runtime.upstreamModel, `${runtime.label} 上游模型应被映射改写`)
  assert.equal(hit.body.stream, stream)
  assert.equal(hit.body.max_tokens, 64)
  assert.equal(hit.body.user, `user-${runtime.label}`)
  assert.equal(hit.body.stop, 'STOP')
  const messages = Array.isArray(hit.body.messages) ? hit.body.messages as Array<Record<string, unknown>> : []
  assert.equal(messages[0]?.role, 'system', `${runtime.label} system block 应转为 Chat system message`)
  assert.equal(messages[1]?.role, 'user', `${runtime.label} user block 应转为 Chat user message`)
  assert(JSON.stringify(messages[1]?.content ?? '').includes('image_url'), `${runtime.label} image block 应转为 Chat image_url`)
  assert.equal(messages[2]?.role, 'assistant', `${runtime.label} assistant block 应转为 Chat assistant message`)
  assert(Array.isArray(messages[2]?.tool_calls), `${runtime.label} assistant tool_use 应转为 Chat tool_calls`)
  assert.equal(messages[3]?.role, 'tool', `${runtime.label} tool_result 应转为 Chat tool message`)
  assert.equal(messages[3]?.tool_call_id, `toolu_${runtime.label}`)
  assert.equal(messages[4]?.role, 'user', `${runtime.label} tool_result 后续 text 应重新转为 user message`)
  const tools = Array.isArray(hit.body.tools) ? hit.body.tools as Array<{ function?: { name?: string } }> : []
  assert.equal(tools[0]?.function?.name, 'lookup', `${runtime.label} Anthropic tool 应转为 Chat function tool`)
  assert.deepEqual(hit.body.tool_choice, { type: 'function', function: { name: 'lookup' } })
}

function singleProviderHit(start: number, label: string): UpstreamHit {
  const hits = upstreamHits.slice(start).filter((hit) => hit.path.includes(`/${label}/`))
  assert.equal(hits.length, 1, `${label} 请求应只命中一次上游，实际 ${hits.length}`)
  return hits[0]!
}

function assertUsageAndAuditRecords(runtimes: Array<MessagesBridgeCase & { accountId: string; groupId: string }>): void {
  const usageRecords = repositories.listUsageRecords(access, { pageSize: 500, result: 'all' }).items
  for (const runtime of runtimes) {
    const records = usageRecords.filter((record) => record.accountId === runtime.accountId && record.groupId === runtime.groupId)
    assert(records.length >= 2, `${runtime.label} 应写入 JSON/SSE 两条成功使用记录，实际 ${records.length}`)
    assert(records.every((record) => record.providerCode === runtime.providerCode), `${runtime.label} usage providerCode 不应串供应商`)
    assert(records.every((record) => record.model === sourceModel), `${runtime.label} usage 下游模型应保持 Messages 源模型`)
    assert(records.every((record) => record.upstreamModel === runtime.upstreamModel), `${runtime.label} usage 上游模型应记录映射后的 Chat 模型`)
    assert(records.every((record) => record.modelMappingApplied === true), `${runtime.label} usage 应标记模型映射已应用`)
    assert(records.some((record) => record.stream === false), `${runtime.label} usage 应包含 JSON 请求`)
    assert(records.some((record) => record.stream === true), `${runtime.label} usage 应包含 SSE 请求`)
  }

  const auditLogs = databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT provider_code, group_id, account_id, model, upstream_model, model_mapping_applied, success
      FROM audit_logs
    `)
    .all() as Array<{
      provider_code?: string | null
      group_id?: string | null
      account_id?: string | null
      model?: string | null
      upstream_model?: string | null
      model_mapping_applied?: number | null
      success?: number | null
    }>
  for (const runtime of runtimes) {
    assert(auditLogs.some((log) =>
      log.provider_code === runtime.providerCode
      && log.group_id === runtime.groupId
      && log.account_id === runtime.accountId
      && log.model === sourceModel
      && log.upstream_model === runtime.upstreamModel
      && log.model_mapping_applied === 1
      && log.success === 1
    ), `${runtime.label} 应写入带模型映射上下文的成功审计日志`)
  }
}

function createMockChatUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const body = safeJsonObject(bodyText)
      const rawUrl = req.url ?? ''
      const path = rawUrl.split('?', 1)[0] ?? ''
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        body,
        bodyText,
        contentType: String(req.headers['content-type'] ?? ''),
        accept: String(req.headers.accept ?? ''),
        anthropicVersion: String(req.headers['anthropic-version'] ?? ''),
        method: req.method ?? '',
        path,
        rawUrl,
        xApiKey: String(req.headers['x-api-key'] ?? '')
      })

      const providerLabel = providerLabelFromPath(path)
      if (!path.endsWith('/chat/completions')) {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: `unexpected path: ${path}` } }))
        return
      }
      if (body.stream === true) {
        sendChatSse(res, providerLabel)
        return
      }
      sendChatJson(res, providerLabel)
    })
  })
}

function sendChatJson(res: http.ServerResponse, providerLabel: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl-${providerLabel}-messages-json`,
    object: 'chat.completion',
    model: `${providerLabel}-messages-chat-upstream`,
    choices: [
      {
        index: 0,
        message: {
          role: 'assistant',
          content: `messages-json-ok:${providerLabel}`,
          tool_calls: [
            {
              id: `call_${providerLabel}`,
              type: 'function',
              function: {
                name: 'lookup',
                arguments: JSON.stringify({ query: providerLabel })
              }
            }
          ]
        },
        finish_reason: 'tool_calls'
      }
    ],
    usage: {
      prompt_tokens: 5,
      completion_tokens: 6,
      total_tokens: 11
    }
  }))
}

function sendChatSse(res: http.ServerResponse, providerLabel: string): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache'
  })
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${providerLabel}-messages-sse`,
    object: 'chat.completion.chunk',
    model: `${providerLabel}-messages-chat-upstream`,
    choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${providerLabel}-messages-sse`,
    object: 'chat.completion.chunk',
    model: `${providerLabel}-messages-chat-upstream`,
    choices: [{ index: 0, delta: { content: `messages-stream-ok:${providerLabel}` }, finish_reason: null }]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${providerLabel}-messages-sse`,
    object: 'chat.completion.chunk',
    model: `${providerLabel}-messages-chat-upstream`,
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
    usage: {
      prompt_tokens: 7,
      completion_tokens: 8,
      total_tokens: 15
    }
  })}\n\n`)
  res.end('data: [DONE]\n\n')
}

function providerLabelFromPath(path: string): string {
  return path.split('/').filter(Boolean)[0] ?? 'unknown'
}

function safeJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
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
