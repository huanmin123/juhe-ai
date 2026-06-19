import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ANTHROPIC_PROTOCOL_CODE, ANTHROPIC_PROVIDER_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { gatewayClientProfileHeader } from '../../modules/gateway/client-profiles/strategy.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

interface AnthropicUpstreamHit {
  rawUrl: string
  path: string
  method: string
  authorization: string
  xApiKey: string
  clientProfileHeader: string
  anthropicVersion: string
  anthropicBeta: string
  bodyText: string
}

interface GatewayIncomingHit {
  path: string
  method: string
  authorizationPresent: boolean
  xApiKeyPresent: boolean
  userAgent?: string
  anthropicVersion?: string
  anthropicBeta?: string
  bodySummary: Record<string, unknown>
}

const tempRoot = resolve(tmpdir(), `juhe-ai-anthropic-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'anthropic-gateway-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'anthropic-gateway-mock-ai-secret'
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
const upstreamHits: AnthropicUpstreamHit[] = []
const gatewayIncomingHits: GatewayIncomingHit[] = []
let anthropicOverloadedErrorSwitchPolicyCreated = false

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createAnthropicMockUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({
      name: 'Anthropic Mock AI 回归分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      name: 'Anthropic Mock AI 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-ant-upstream',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: 'Anthropic Mock AI 回归 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertAnthropicMessagesJson(baseUrl, apiKey.key)
    await assertAnthropicMessagesSse(baseUrl, apiKey.key)
    await assertAnthropicSseRetryExhaustedErrorShape(baseUrl, upstreamBaseUrl)
    await assertClaudeCodeClientProfileHeader(baseUrl, apiKey.key)
    await assertAnthropicBetaHeaderForwardsClientValue(baseUrl, upstreamBaseUrl)
    await assertAnthropicLocalErrorShape(baseUrl)
    await assertAnthropicCountTokens(baseUrl, apiKey.key)
    await assertAnthropicCountTokensUnsupportedDoesNotPoisonMessages(baseUrl, upstreamBaseUrl)
    await assertAnthropicModels(baseUrl, apiKey.key)
    await assertAnthropicModelMapping(baseUrl, upstreamBaseUrl)
    await assertAnthropicApiKeyPoolIsolation(baseUrl, upstreamBaseUrl)
    await assertAnthropicResponseInspectionSwitchesAccount(baseUrl, upstreamBaseUrl)
    await assertAnthropicJsonErrorSwitchesAccount(baseUrl, upstreamBaseUrl)
    await assertAnthropicSseErrorSwitchesAccount(baseUrl, upstreamBaseUrl)
    await assertOpenAIGroupDoesNotAcceptAnthropicMessages(baseUrl, upstreamBaseUrl)
    await assertAnthropicGroupRejectsOpenAIResponses(baseUrl, apiKey.key)
    if (truthyEnv('JUHE_RUN_CLAUDE_CODE_CLI_MOCK')) {
      await assertOfficialClaudeCodeCliMockCapture(baseUrl, apiKey.key)
    }

    console.log('anthropic gateway mock ai regression passed')
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

async function assertOfficialClaudeCodeCliMockCapture(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  gatewayIncomingHits.length = 0
  let output: string
  try {
    output = await runClaudeCodeCli({
      gatewayBaseUrl: baseUrl,
      localApiKey,
      model: 'claude-haiku-4-5',
      prompt: 'Reply with exactly: anthropic json ok'
    })
  } catch (error) {
    throw new Error(`${error instanceof Error ? error.message : String(error)}; incoming=${JSON.stringify(gatewayIncomingHits.map(summarizeIncomingHitForError))}; upstream=${JSON.stringify(upstreamHits.map(summarizeUpstreamHitForError))}`)
  }
  assert.match(output, /anthropic (json|sse) ok/, `Claude Code CLI mock 输出应包含 mock 响应：${output.slice(0, 500)}`)
  const incomingMessages = gatewayIncomingHits.filter((hit) => hit.path.split('?', 1)[0].endsWith('/messages'))
  assert(incomingMessages.length > 0, 'Claude Code CLI 应命中本地 /v1/messages')
  assert(incomingMessages.some((hit) => hit.authorizationPresent || hit.xApiKeyPresent), 'Claude Code CLI 请求应携带本地网关 API Key')
  const upstreamMessages = upstreamHits.filter((hit) => hit.path === '/v1/messages')
  assert(upstreamMessages.length > 0, 'Claude Code CLI 应通过网关命中 Anthropic mock /v1/messages')
  assert(upstreamMessages.some((hit) => hit.rawUrl === '/v1/messages?beta=true'), '网关转发 Claude Code CLI 请求时应保留 ?beta=true 查询参数')
  assert(upstreamMessages.every((hit) => hit.xApiKey === 'sk-ant-upstream'), '网关转发 Claude Code CLI 请求时应使用上游 Anthropic API Key')
  assert(upstreamMessages.every((hit) => hit.authorization === ''), '网关转发 Anthropic 请求时不应透传客户端 Authorization')
  console.log(JSON.stringify({
    claudeCodeCliMockCapture: {
      output: output.trim().slice(0, 300),
      incomingRequests: gatewayIncomingHits.map((hit) => ({
        path: hit.path,
        method: hit.method,
        authorizationPresent: hit.authorizationPresent,
        xApiKeyPresent: hit.xApiKeyPresent,
        userAgent: hit.userAgent,
        anthropicVersion: hit.anthropicVersion,
        anthropicBeta: hit.anthropicBeta,
        bodySummary: hit.bodySummary
      })),
      upstreamRequests: upstreamHits.map((hit) => ({
        rawUrl: hit.rawUrl,
        path: hit.path,
        method: hit.method,
        xApiKey: hit.xApiKey,
        authorizationPresent: Boolean(hit.authorization),
        anthropicVersion: hit.anthropicVersion,
        anthropicBeta: hit.anthropicBeta,
        bodyBytes: Buffer.byteLength(hit.bodyText, 'utf8')
      }))
    }
  }, null, 2))
}

function summarizeIncomingHitForError(hit: GatewayIncomingHit): Record<string, unknown> {
  return {
    path: hit.path,
    method: hit.method,
    authorizationPresent: hit.authorizationPresent,
    xApiKeyPresent: hit.xApiKeyPresent,
    userAgent: hit.userAgent,
    anthropicVersion: hit.anthropicVersion,
    anthropicBeta: hit.anthropicBeta,
    bodySummary: hit.bodySummary
  }
}

function summarizeUpstreamHitForError(hit: AnthropicUpstreamHit): Record<string, unknown> {
  return {
    rawUrl: hit.rawUrl,
    path: hit.path,
    method: hit.method,
    xApiKey: hit.xApiKey,
    authorizationPresent: Boolean(hit.authorization),
    anthropicVersion: hit.anthropicVersion,
    anthropicBeta: hit.anthropicBeta,
    bodyBytes: Buffer.byteLength(hit.bodyText, 'utf8')
  }
}

async function assertAnthropicMessagesJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'x-api-key': localApiKey,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'hello anthropic json' }],
      max_tokens: 8,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Messages JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { content?: Array<{ text?: string }>; usage?: { input_tokens?: number; output_tokens?: number } }
  assert.equal(body.content?.[0]?.text, 'anthropic json ok')
  assert.equal(body.usage?.input_tokens, 9)
  assert.equal(body.usage?.output_tokens, 3)
  assert.equal(upstreamHits.length, 1, 'Anthropic JSON 应命中一次 mock 上游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'hello anthropic json'
  })
}

async function assertAnthropicMessagesSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'hello anthropic sse' }],
      max_tokens: 8,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Messages SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'Anthropic SSE 应保持 text/event-stream')
  assert.match(text, /event: content_block_delta/)
  assert.match(text, /anthropic sse ok/)
  assert.equal(upstreamHits.length, 1, 'Anthropic SSE 应命中一次 mock 上游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'hello anthropic sse'
  })
}

async function assertAnthropicSseRetryExhaustedErrorShape(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic SSE 重试耗尽错误形态分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE 提前结束账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-empty-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic SSE 重试耗尽错误形态 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic SSE 重试耗尽错误形态 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'x-api-key': apiKey.key,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'trigger empty anthropic sse' }],
      max_tokens: 8,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic SSE 重试耗尽应以 SSE 错误事件返回，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'Anthropic SSE 重试耗尽应保持 text/event-stream')
  assert.match(text, /^event: error$/m, `Anthropic SSE 失败尾包应使用 event: error：${text}`)
  assert.equal(text.includes('event: response.failed'), false, 'Anthropic SSE 失败尾包不应使用 OpenAI response.failed 事件')
  const errorData = text.split(/\r?\n/).find((line) => line.startsWith('data:'))?.slice(5).trim()
  assert(errorData, `Anthropic SSE 失败尾包应包含 data：${text}`)
  const payload = JSON.parse(errorData) as { type?: string; error?: { type?: string; message?: string; code?: string } }
  assert.equal(payload.type, 'error', 'Anthropic SSE 失败尾包顶层 type 应为 error')
  assert.equal(payload.error?.type, 'api_error', 'Anthropic SSE 失败尾包 error.type 应为 api_error')
  assert(payload.error?.message, 'Anthropic SSE 失败尾包应包含错误消息')
  assert(upstreamHits.some((hit) => hit.xApiKey === 'sk-ant-empty-sse'), 'Anthropic SSE 重试耗尽应命中提前结束 mock 上游')
}

async function assertClaudeCodeClientProfileHeader(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      [gatewayClientProfileHeader]: 'claude_code'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'hello claude code profile' }],
      max_tokens: 8,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Claude Code 客户端画像请求应成功，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 1, 'Claude Code 客户端画像请求应命中一次 mock 上游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'hello claude code profile'
  })
}

async function assertAnthropicBetaHeaderForwardsClientValue(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic Beta 客户端透传回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic Beta 客户端透传回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-beta',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic Beta 客户端透传回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic Beta 客户端透传回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      'anthropic-beta': 'client-beta-2026-01-01, shared-beta'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'hello anthropic beta merge' }],
      max_tokens: 8,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Beta 客户端透传请求应成功，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 1, 'Anthropic Beta 客户端透传请求应命中一次 mock 上游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'hello anthropic beta merge',
    xApiKey: 'sk-ant-beta',
    anthropicBeta: 'client-beta-2026-01-01,shared-beta'
  })
}

async function assertAnthropicLocalErrorShape(baseUrl: string): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'missing local key' }],
      max_tokens: 8
    })
  })
  const text = await response.text()
  assert.equal(response.status, 401, `Anthropic 本地认证错误应返回 401，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { type?: string; error?: { type?: string; message?: string } }
  assert.equal(body.type, 'error', 'Anthropic 本地错误响应顶层 type 应为 error')
  assert.equal(body.error?.type, 'invalid_request_error', 'Anthropic 本地错误应使用 Anthropic error.type')
  assert.match(body.error?.message ?? '', /访问令牌|API Key/, 'Anthropic 本地错误应保留中文错误消息')
}

async function assertAnthropicCountTokens(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages/count_tokens`, {
    method: 'POST',
    headers: {
      'x-api-key': localApiKey,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'count this' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Count Tokens 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { input_tokens?: number }
  assert.equal(body.input_tokens, 11)
  assert.equal(upstreamHits.length, 1, 'Anthropic Count Tokens 应命中一次 mock 上游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages/count_tokens',
    method: 'POST',
    bodyIncludes: 'count this'
  })
}

async function assertAnthropicCountTokensUnsupportedDoesNotPoisonMessages(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic Count Tokens 不支持降级回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic Count Tokens 不支持降级账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-count-unsupported',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'message_token_counting']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic Count Tokens 不支持降级 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic Count Tokens 不支持降级 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const countResponse = await fetch(`${baseUrl}/v1/messages/count_tokens`, {
    method: 'POST',
    headers: {
      'x-api-key': apiKey.key,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'unsupported count tokens' }]
    })
  })
  const countText = await countResponse.text()
  assert.notEqual(countResponse.status, 200, `mock count_tokens 不支持时不应返回成功：${countText}`)
  assert(upstreamHits.some((hit) => hit.path === '/v1/messages/count_tokens' && hit.xApiKey === 'sk-ant-count-unsupported'), 'Count Tokens 不支持场景应先命中 mock 上游 404')

  upstreamHits.length = 0
  const messageResult = await postAnthropicMessage(baseUrl, apiKey.key, 'messages should still work after unsupported count_tokens')
  assert.equal(messageResult.status, 200, `count_tokens 不支持不应污染账号健康，普通 messages 应继续成功，实际 HTTP ${messageResult.status}: ${messageResult.text}`)
  assert.equal(upstreamHits.length, 1, 'count_tokens 不支持后普通 messages 应继续调度同一账户')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'messages should still work after unsupported count_tokens',
    xApiKey: 'sk-ant-count-unsupported'
  })
}

async function assertAnthropicModels(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/models`, {
    headers: {
      'x-api-key': localApiKey
    }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Models 应由本地模型目录返回，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { data?: unknown[]; has_more?: boolean; first_id?: string | null; last_id?: string | null }
  assert(Array.isArray(body.data), 'Anthropic Models 响应应包含 data 数组')
  assert.equal(body.has_more, false)
  assert(Object.prototype.hasOwnProperty.call(body, 'first_id'), 'Anthropic Models 响应应包含 first_id')
  assert(Object.prototype.hasOwnProperty.call(body, 'last_id'), 'Anthropic Models 响应应包含 last_id')
  assert.equal(upstreamHits.length, 0, 'Anthropic /v1/models 应由本地模型目录响应，不应打到上游 mock')
}

async function assertAnthropicModelMapping(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const sourceModel = 'claude-haiku-4-5'
  const upstreamModel = 'claude-haiku-4-5-20251001'
  const group = repositories.createGroup({
    name: 'Anthropic 模型映射回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 模型映射回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-model-mapping',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    supportedModels: [upstreamModel],
    modelMappings: [
      { sourceModel, upstreamModel, enabled: true }
    ],
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic 模型映射回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic 模型映射回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const result = await postAnthropicMessage(baseUrl, apiKey.key, 'trigger anthropic model mapping', sourceModel)
  assert.equal(result.status, 200, `Anthropic 模型映射请求应成功，实际 HTTP ${result.status}: ${result.text}`)
  assert.equal(upstreamHits.length, 1, 'Anthropic 模型映射请求应命中一次 mock 上游')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { model?: string; messages?: unknown[] }
  assert.equal(upstreamBody.model, upstreamModel, 'Anthropic 上游请求体顶层 model 应被改写为映射后的上游模型')
  assert(Array.isArray(upstreamBody.messages), 'Anthropic 模型映射不应丢失 messages 字段')
}

async function assertAnthropicApiKeyPoolIsolation(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic 多 Key 隔离回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 多 Key 隔离来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-keypool-bad',
      api_keys: ['sk-ant-keypool-bad', 'sk-ant-keypool-good'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 多 Key 隔离救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-keypool-rescue',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic 多 Key 隔离回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic 多 Key 隔离回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const first = await postAnthropicMessage(baseUrl, apiKey.key, 'trigger anthropic key pool failover')
  assert.equal(first.status, 200, `Anthropic 坏 Key 失败后应切到救援账户成功，实际 HTTP ${first.status}: ${first.text}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.xApiKey),
    ['sk-ant-keypool-bad', 'sk-ant-keypool-rescue'],
    'Anthropic 多 Key 账户当前 Key 失败后，本次请求应切到同分组救援账户'
  )

  upstreamHits.length = 0
  const second = await postAnthropicMessage(baseUrl, apiKey.key, 'trigger anthropic key pool local isolation')
  assert.equal(second.status, 200, `Anthropic 坏 Key 本地短避让后应命中同账户好 Key，实际 HTTP ${second.status}: ${second.text}`)
  assert.deepEqual(
    upstreamHits.map((hit) => hit.xApiKey),
    ['sk-ant-keypool-good'],
    'Anthropic 多 Key 账户坏 Key 被短避让后，后续请求应继续使用同账户剩余好 Key'
  )
}

async function assertAnthropicResponseInspectionSwitchesAccount(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic 响应检查换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 污染响应账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-polluted',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 干净响应账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-clean',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  responseInspectionPolicies.createResponseInspectionPolicy({
    name: 'Anthropic mock 污染文本换号',
    enabled: true,
    priority: 10,
    scopeType: 'provider',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    providerCode: ANTHROPIC_PROVIDER_CODE,
    match: {
      outputTextIncludes: ['ANTHROPIC-POLLUTED']
    },
    action: 'retry_next_account'
  })
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic 响应检查换号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic 响应检查换号 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'trigger anthropic response inspection' }],
      max_tokens: 16,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic 响应检查换号后应返回成功，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 2, 'Anthropic 响应检查应先命中污染账号，再服务端换号命中干净账号')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic response inspection',
    xApiKey: 'sk-ant-polluted'
  })
  assertAnthropicUpstreamHit(upstreamHits[1], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic response inspection',
    xApiKey: 'sk-ant-clean'
  })
  const body = JSON.parse(text) as { content?: Array<{ text?: string }> }
  assert.equal(body.content?.[0]?.text, 'anthropic clean ok', `Anthropic 响应检查换号后应返回干净账号内容：${text}`)
  assert(!text.includes('ANTHROPIC-POLLUTED'), 'Anthropic 污染账号响应不应透传给下游')
}

async function assertAnthropicJsonErrorSwitchesAccount(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  createAnthropicOverloadedErrorSwitchPolicy()
  const group = repositories.createGroup({
    name: 'Anthropic JSON error 换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic JSON error 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-error-json',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic JSON error 干净账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-clean',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic JSON error 换号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic JSON error 换号 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'trigger anthropic json error inspection' }],
      max_tokens: 16,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic JSON error 换号后应返回成功，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 2, 'Anthropic JSON error 应先命中错误账号，再服务端换号命中干净账号')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic json error inspection',
    xApiKey: 'sk-ant-error-json'
  })
  assertAnthropicUpstreamHit(upstreamHits[1], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic json error inspection',
    xApiKey: 'sk-ant-clean'
  })
  const body = JSON.parse(text) as { content?: Array<{ text?: string }> }
  assert.equal(body.content?.[0]?.text, 'anthropic clean ok', `Anthropic JSON error 换号后应返回干净账号内容：${text}`)
  assert(!text.includes('mock overloaded'), 'Anthropic JSON error 不应透传给下游')
}

async function assertAnthropicSseErrorSwitchesAccount(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  createAnthropicOverloadedErrorSwitchPolicy()
  const group = repositories.createGroup({
    name: 'Anthropic SSE error 换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE error 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-error-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE error 干净账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-clean-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'Anthropic SSE error 换号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic SSE error 换号 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'trigger anthropic sse error inspection' }],
      max_tokens: 16,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic SSE error 换号后应返回成功，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 2, 'Anthropic SSE error 应先命中错误账号，再服务端换号命中干净账号')
  assert.match(text, /anthropic sse ok/, `Anthropic SSE error 换号后应返回干净 SSE 内容：${text}`)
  assert(!text.includes('mock overloaded'), 'Anthropic SSE error 不应透传给下游')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic sse error inspection',
    xApiKey: 'sk-ant-error-sse'
  })
  assertAnthropicUpstreamHit(upstreamHits[1], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic sse error inspection',
    xApiKey: 'sk-ant-clean-sse'
  })
}

async function assertOpenAIGroupDoesNotAcceptAnthropicMessages(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'OpenAI 不承接 Anthropic Messages 分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    name: 'OpenAI 不承接 Anthropic Messages 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-openai-upstream',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'OpenAI 不承接 Anthropic Messages Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'OpenAI 隔离回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'must not dispatch to openai group' }],
      max_tokens: 8
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `OpenAI 分组不应承接 Anthropic Messages，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /请求路径|客户端协议|request_capability_mismatch/)
  assert.equal(upstreamHits.length, 0, 'OpenAI 分组被拒绝后不应命中 Anthropic mock 上游')
}

async function assertAnthropicGroupRejectsOpenAIResponses(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: 'anthropic-incompatible-turn' })
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      input: 'must not dispatch openai responses to anthropic group',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Anthropic 分组不应承接 OpenAI Responses，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /Anthropic 原生分组/, '错误消息应明确说明当前 API Key 绑定的是 Anthropic 原生分组')
  assert.match(text, /不兼容 Codex \/ OpenAI 请求路径/, '错误消息应明确说明 Codex/OpenAI 路径不兼容')
  assert.match(text, /anthropic_native_group_openai_compatible_request/, '错误 code 应标识 Anthropic 原生分组收到 OpenAI-compatible 请求')
  assert.equal(upstreamHits.length, 0, 'Anthropic 分组拒绝 OpenAI Responses 后不应命中 mock 上游')
}

async function postAnthropicMessage(
  baseUrl: string,
  localApiKey: string,
  prompt: string,
  model = 'claude-haiku-4-5'
): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: prompt }],
      max_tokens: 8,
      stream: false
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
  const rawBody = Buffer.isBuffer((req as { rawBody?: unknown }).rawBody)
    ? (req as unknown as { rawBody: Buffer }).rawBody
    : Buffer.alloc(0)
  gatewayIncomingHits.push({
    path: req.originalUrl || req.path,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    xApiKeyPresent: Boolean(req.headers['x-api-key']),
    userAgent: headerText(req.headers['user-agent']),
    anthropicVersion: headerText(req.headers['anthropic-version']),
    anthropicBeta: headerText(req.headers['anthropic-beta']),
    bodySummary: summarizeGatewayRequestBody(rawBody)
  })
  next()
}

function summarizeGatewayRequestBody(rawBody: Buffer): Record<string, unknown> {
  if (!rawBody.byteLength) return { bytes: 0 }
  const parsed = safeParseJson(rawBody.toString('utf8'))
  const body = parsed && typeof parsed === 'object' && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : undefined
  if (!body) return { bytes: rawBody.byteLength, json: false }
  return {
    bytes: rawBody.byteLength,
    model: typeof body.model === 'string' ? body.model : undefined,
    stream: body.stream === true,
    maxTokens: typeof body.max_tokens === 'number' ? body.max_tokens : undefined,
    messageCount: Array.isArray(body.messages) ? body.messages.length : undefined,
    toolCount: Array.isArray(body.tools) ? body.tools.length : undefined,
    hasSystem: typeof body.system === 'string' || Array.isArray(body.system),
    hasThinking: typeof body.thinking === 'object' && body.thinking !== null
  }
}

function safeParseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

function headerText(value: unknown): string | undefined {
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function runClaudeCodeCli(input: {
  gatewayBaseUrl: string
  localApiKey: string
  model: string
  prompt: string
}): Promise<string> {
  const npxArgs = [
    '-y',
    '@anthropic-ai/claude-code@latest',
    '--bare',
    '--print',
    '--setting-sources',
    'local',
    '--no-session-persistence',
    '--output-format',
    'text',
    '--model',
    input.model,
    '--max-budget-usd',
    '1',
    input.prompt
  ]
  const command = process.platform === 'win32' ? process.env.ComSpec || 'cmd.exe' : 'npx'
  const args = process.platform === 'win32'
    ? ['/d', '/s', '/c', windowsCommandLine(['npx', ...npxArgs])]
    : npxArgs
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const rejectOnce = (error: Error) => {
      if (settled) return
      settled = true
      rejectPromise(error)
    }
    const resolveOnce = (value: string) => {
      if (settled) return
      settled = true
      resolvePromise(value)
    }
    const child = spawn(command, args, {
      cwd: resolve(process.cwd(), '..'),
      env: {
        ...sanitizedProcessEnv(),
        ANTHROPIC_BASE_URL: input.gatewayBaseUrl,
        ANTHROPIC_API_KEY: input.localApiKey,
        CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: '1',
        CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS: '1',
        DISABLE_AUTOUPDATER: '1',
        DISABLE_BUG_COMMAND: '1',
        DISABLE_ERROR_REPORTING: '1',
        DISABLE_TELEMETRY: '1',
        DISABLE_FEEDBACK_COMMAND: '1'
      },
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    const timeout = setTimeout(() => {
      child.kill()
      rejectOnce(new Error(`Claude Code CLI mock 抓包超时；stdout=${sanitizeSecretText(Buffer.concat(stdout).toString('utf8')).slice(0, 1000)}；stderr=${sanitizeSecretText(Buffer.concat(stderr).toString('utf8')).slice(0, 1000)}`))
    }, 120_000)
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.stdin.end()
    child.on('error', (error) => {
      clearTimeout(timeout)
      rejectOnce(error)
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      const output = Buffer.concat(stdout).toString('utf8')
      const errorOutput = Buffer.concat(stderr).toString('utf8')
      if (code === 0) {
        resolveOnce(output)
      } else {
        rejectOnce(new Error(`Claude Code CLI 退出码 ${code}: ${sanitizeSecretText(errorOutput || output).slice(0, 1000)}`))
      }
    })
  })
}

function windowsCommandLine(args: string[]): string {
  return args.map(quoteWindowsCommandArg).join(' ')
}

function quoteWindowsCommandArg(value: string): string {
  if (/^[A-Za-z0-9_./:@=+-]+$/.test(value)) {
    return value
  }
  return `"${value.replace(/(["^&|<>%])/g, '^$1')}"`
}

function sanitizedProcessEnv(): Record<string, string> {
  const output: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (!key || key.includes('=') || value === undefined) continue
    output[key] = value
  }
  return output
}

function sanitizeSecretText(value: string): string {
  return value.replace(/sk-[A-Za-z0-9_-]+/g, 'sk-***')
}

function truthyEnv(name: string): boolean {
  const value = process.env[name]
  return value !== undefined && ['1', 'true', 'yes', 'on'].includes(value.trim().toLowerCase())
}

function assertAnthropicUpstreamHit(hit: AnthropicUpstreamHit | undefined, input: {
  path: string
  method: string
  bodyIncludes: string
  xApiKey?: string
  anthropicBeta?: string
}): void {
  assert(hit, '缺少 Anthropic mock 上游命中记录')
  assert.equal(hit.path, input.path)
  assert.equal(hit.method, input.method)
  assert.equal(hit.xApiKey, input.xApiKey ?? 'sk-ant-upstream', '上游 x-api-key 应替换为账户 API Key')
  assert.equal(hit.authorization, '', 'Anthropic 上游不应收到本地 Authorization/Bearer')
  assert.equal(hit.clientProfileHeader, '', 'Anthropic 上游不应收到本地客户端画像 header')
  assert.equal(hit.anthropicVersion, '2023-06-01', '缺省 Anthropic-Version 应按官方默认版本补齐')
  assert.equal(hit.anthropicBeta, input.anthropicBeta ?? '', 'Anthropic beta 头只应透传客户端显式 header')
  assert.match(hit.bodyText, new RegExp(input.bodyIncludes.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
}

function createAnthropicOverloadedErrorSwitchPolicy(): void {
  if (anthropicOverloadedErrorSwitchPolicyCreated) return
  responseInspectionPolicies.createResponseInspectionPolicy({
    name: 'Anthropic mock overloaded error 换号',
    enabled: true,
    priority: 9,
    scopeType: 'provider',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    providerCode: ANTHROPIC_PROVIDER_CODE,
    match: {
      errorTypes: ['overloaded_error']
    },
    action: 'retry_next_account'
  })
  anthropicOverloadedErrorSwitchPolicyCreated = true
}

function createAnthropicMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({
        rawUrl: req.url ?? '',
        path,
        method: req.method ?? '',
        authorization: String(req.headers.authorization ?? ''),
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        clientProfileHeader: String(req.headers[gatewayClientProfileHeader] ?? ''),
        anthropicVersion: String(req.headers['anthropic-version'] ?? ''),
        anthropicBeta: String(req.headers['anthropic-beta'] ?? ''),
        bodyText
      })
      if (path === '/v1/messages/count_tokens') {
        if (req.headers['x-api-key'] === 'sk-ant-count-unsupported') {
          res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
          res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'count tokens unsupported' } }))
          return
        }
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ input_tokens: 11 }))
        return
      }
      if (path === '/v1/messages') {
        const stream = bodyText.includes('"stream":true')
        if (stream) {
          if (req.headers['x-api-key'] === 'sk-ant-empty-sse') {
            sendAnthropicSseEmpty(res)
          } else if (req.headers['x-api-key'] === 'sk-ant-error-sse') {
            sendAnthropicSseError(res)
          } else {
            sendAnthropicSse(res)
          }
        } else {
          sendAnthropicJson(res, String(req.headers['x-api-key'] ?? ''))
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'mock path not found' } }))
    })
  })
}

function sendAnthropicJson(res: http.ServerResponse, xApiKey: string): void {
  if (xApiKey === 'sk-ant-keypool-bad') {
    res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      type: 'error',
      error: {
        type: 'overloaded_error',
        message: 'mock key pool bad key'
      }
    }))
    return
  }
  if (xApiKey === 'sk-ant-error-json') {
    res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      type: 'error',
      error: {
        type: 'overloaded_error',
        message: 'mock overloaded'
      }
    }))
    return
  }
  const text = xApiKey === 'sk-ant-polluted'
    ? 'ANTHROPIC-POLLUTED'
    : xApiKey === 'sk-ant-clean'
      ? 'anthropic clean ok'
      : 'anthropic json ok'
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_mock_json',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 9,
      output_tokens: 3,
      cache_read_input_tokens: 2
    }
  }))
}

function sendAnthropicSse(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: 'msg_mock_sse',
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 7, output_tokens: 0 }
    }
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: { type: 'text', text: '' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'text_delta', text: 'anthropic sse ok' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'end_turn' },
    usage: { output_tokens: 4 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicSseError(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'error', {
    type: 'error',
    error: {
      type: 'overloaded_error',
      message: 'mock overloaded'
    }
  })
  res.end()
}

function sendAnthropicSseEmpty(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.end()
}

function writeSse(res: http.ServerResponse, event: string, payload: Record<string, unknown>): void {
  res.write(`event: ${event}\n`)
  res.write(`data: ${JSON.stringify(payload)}\n\n`)
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
