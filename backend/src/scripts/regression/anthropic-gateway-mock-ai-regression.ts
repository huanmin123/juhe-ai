import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { gatewayClientProfileHeader } from '../../modules/gateway/client-profiles/strategy.js'
import { inspectAnthropicStreamText } from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import {
  extractAnthropicSseSemanticFrames,
  parseAnthropicSseEventText
} from '../../modules/gateway/protocols/anthropic-v1/response-semantics.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { closeSqliteReadWorkerPool } from '../../storage/sqlite-read-worker-pool.js'

interface AnthropicUpstreamHit {
  rawUrl: string
  path: string
  method: string
  authorization: string
  xApiKey: string
  userAgent: string
  clientProfileHeader: string
  anthropicVersion: string
  anthropicBeta: string
  claudeCodeSessionId: string
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
// Do not override runtimeConfig.secret here: crypto.ts captures the initial secret during module import,
// and SQLite read workers receive runtimeConfig.secret through env.
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
  rawRepositories,
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
const repositories = {
  ...rawRepositories,
  createAccount(
    input: Parameters<typeof rawRepositories.createAccount>[0],
    actor: Parameters<typeof rawRepositories.createAccount>[1]
  ) {
    const fixtureInput = withFixtureProfile(input)
    const supportedModels = Array.isArray(fixtureInput.supportedModels)
      ? fixtureInput.supportedModels.filter((model): model is string => typeof model === 'string' && Boolean(model.trim()))
      : []
    const fixtureSupportedModels = supportedModels.length > 0
      ? supportedModels
      : fixtureInput.providerCode === ANTHROPIC_PROVIDER_CODE
        ? ['claude-haiku-4-5']
        : ['gpt-5.5']
    const account = rawRepositories.createAccount({
      ...fixtureInput,
      supportedModels: fixtureSupportedModels,
      ...(!fixtureInput.healthCheckModel
        ? { healthCheckModel: fixtureSupportedModels[0] }
        : {})
    }, actor)
    if (input.status === 'active') {
      assert(rawRepositories.recordAccountHealthCheckSuccess(account.id, {
        intervalHours: 12,
        jitterMinutes: 0,
        failureThreshold: 3,
        statusCode: 200
      }), `测试账户 ${account.id} 应通过后台检查激活`)
      return rawRepositories.findAccountSummary(account.id, actor) ?? account
    }
    return account
  }
}
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
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
    await assertAnthropicSsePreCommitFailureSwitchesAccount(baseUrl, upstreamBaseUrl)
    await assertAnthropicHostedWorkUsesUnifiedFailover(baseUrl, upstreamBaseUrl)
    await assertClaudeCodeClientProfileHeader(baseUrl, apiKey.key)
    await assertAnthropicBetaHeaderForwardsClientValue(baseUrl, upstreamBaseUrl)
    await assertAnthropicLocalErrorShape(baseUrl)
    await assertAnthropicCountTokens(baseUrl, apiKey.key)
    await assertAnthropicCountTokensUnsupportedDoesNotPoisonMessages(baseUrl, upstreamBaseUrl)
    await assertAnthropicModelNotFoundDoesNotPoisonMessages(baseUrl, upstreamBaseUrl)
    await assertAnthropicEmptyJsonContentUsesUnifiedFailureHandling(baseUrl, upstreamBaseUrl)
    await assertAnthropicModels(baseUrl, apiKey.key)
    assertAnthropicModelMappingIsOpenAIProtocolOnly(upstreamBaseUrl)
    await assertAnthropicApiKeyPoolOpaqueFailover(baseUrl, upstreamBaseUrl)
    await assertAnthropicConfiguredResponseInspectionSwitchesRequestLocally(baseUrl, upstreamBaseUrl)
    await assertAnthropicJsonErrorSwitchesRequestLocally(baseUrl, upstreamBaseUrl)
    await assertAnthropicSseErrorSwitchesRequestLocally(baseUrl, upstreamBaseUrl)
    await assertAnthropicSseErrorFieldStaysNeutral(baseUrl, upstreamBaseUrl)
    await assertOpenAIGroupDoesNotAcceptAnthropicMessages(baseUrl, upstreamBaseUrl)
    await assertAnthropicGroupRejectsOpenAIResponses(baseUrl, apiKey.key)
    assertAnthropicSignatureDeltaIsNotOutput()
    if (truthyEnv('JUHE_RUN_CLAUDE_CODE_CLI_MOCK')) {
      await assertOfficialClaudeCodeCliMockCapture(baseUrl, upstreamBaseUrl)
    }
    if (truthyEnv('JUHE_RUN_REAL_ANTHROPIC_UPSTREAM')) {
      await assertRealAnthropicGatewayViaClaudeCode(baseUrl)
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
  await closeSqliteReadWorkerPool()
  await removeTempRootForTest(tempRoot)
}

function assertAnthropicSignatureDeltaIsNotOutput(): void {
  const rawEvent = 'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"mock-signature"}}\n\n'
  const inspection = inspectAnthropicStreamText(rawEvent)
  assert.equal(inspection.outputReceived, false, 'Anthropic signature_delta 不应被视为可见输出')
  assert.equal(inspection.outputEventCount, 0, 'Anthropic signature_delta 不应增加输出事件计数')
  assert.equal(inspection.estimatedOutputTokens, undefined, 'Anthropic signature_delta 不应参与输出 token 估算')

  const frames = extractAnthropicSseSemanticFrames(parseAnthropicSseEventText(rawEvent), 'messages')
  assert.equal(frames.some((frame) => frame.frameType === 'output_text_delta'), false, 'Anthropic signature_delta 不应生成 output_text_delta 语义帧')
  assert(frames.some((frame) => frame.frameType === 'raw_json_path'), 'Anthropic signature_delta 仍应保留原始语义帧便于审计')
}

function withFixtureProfile<Input extends { providerCode?: string; providerProtocolProfileId?: string }>(input: Input): Input {
  if (input.providerProtocolProfileId) {
    return input
  }
  if (input.providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return {
      ...input,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    }
  }
  if (input.providerCode !== ANTHROPIC_PROVIDER_CODE) {
    return input
  }
  return {
    ...input,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
  }
}

async function assertOfficialClaudeCodeCliMockCapture(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const expectedUpstreamApiKey = 'sk-ant-claude-cli'
  const group = repositories.createGroup({
    name: 'Claude Code CLI 抓包隔离分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Claude Code CLI 抓包隔离账户',
    type: 'api_key',
    credentials: {
      api_key: expectedUpstreamApiKey,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Claude Code CLI 抓包隔离 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Claude Code CLI 抓包隔离 API Key 未返回明文密钥')

  upstreamHits.length = 0
  gatewayIncomingHits.length = 0
  let output: string
  try {
    output = await runClaudeCodeCli({
      gatewayBaseUrl: baseUrl,
      localApiKey: apiKey.key,
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
  const upstreamMessages = upstreamHits.filter((hit) => hit.path === '/v1/messages' && hit.xApiKey === expectedUpstreamApiKey)
  assert(upstreamMessages.length > 0, 'Claude Code CLI 应通过网关命中 Anthropic mock /v1/messages')
  assert(upstreamMessages.some((hit) => hit.rawUrl === '/v1/messages?beta=true'), '网关转发 Claude Code CLI 请求时应保留 ?beta=true 查询参数')
  assert(upstreamMessages.every((hit) => hit.xApiKey === expectedUpstreamApiKey), '网关转发 Claude Code CLI 请求时应使用隔离账户的上游 Anthropic API Key')
  assert(upstreamMessages.every((hit) => hit.authorization === ''), '网关转发 Anthropic 请求时不应透传客户端 Authorization')
  assert(upstreamMessages.every((hit) => hit.userAgent.startsWith('claude-cli/')), '网关转发真实 Claude Code CLI 请求时应保留客户端 User-Agent')
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
        userAgent: hit.userAgent,
        anthropicVersion: hit.anthropicVersion,
        anthropicBeta: hit.anthropicBeta,
        bodyBytes: Buffer.byteLength(hit.bodyText, 'utf8')
      }))
    }
  }, null, 2))
}

async function assertRealAnthropicGatewayViaClaudeCode(baseUrl: string): Promise<void> {
  const upstreamApiKey = process.env.JUHE_REAL_ANTHROPIC_API_KEY?.trim()
  assert(upstreamApiKey, '真实 Anthropic 上游 smoke 需要 JUHE_REAL_ANTHROPIC_API_KEY')
  const upstreamBaseUrl = process.env.JUHE_REAL_ANTHROPIC_BASE_URL?.trim() || 'https://www.micuapi.ai'
  const model = process.env.JUHE_REAL_ANTHROPIC_MODEL?.trim() || 'claude-sonnet-4-6'
  const expectedText = process.env.JUHE_REAL_ANTHROPIC_EXPECTED_TEXT?.trim() || 'juhe gateway micu ok'
  const group = repositories.createGroup({
    name: 'Anthropic 真实上游 smoke 分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic 真实上游 smoke 账户',
    type: 'api_key',
    credentials: {
      api_key: upstreamApiKey,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic 真实上游 smoke Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic 真实上游 smoke API Key 未返回明文密钥')

  gatewayIncomingHits.length = 0
  const output = await runClaudeCodeCli({
    gatewayBaseUrl: baseUrl,
    localApiKey: apiKey.key,
    model,
    prompt: `Reply exactly: ${expectedText}`
  })
  assert.match(output, new RegExp(escapeRegExp(expectedText)), `真实上游 smoke 应返回预期文本，实际输出：${output.slice(0, 500)}`)
  const incomingMessages = gatewayIncomingHits.filter((hit) => hit.path.split('?', 1)[0].endsWith('/messages'))
  assert(incomingMessages.length > 0, '真实上游 smoke 应通过本地网关收到 Claude Code /v1/messages 请求')
  assert(incomingMessages.some((hit) => hit.xApiKeyPresent), '真实上游 smoke 的 Claude Code 请求应携带本地网关 x-api-key')
  assert(incomingMessages.some((hit) => hit.userAgent?.startsWith('claude-cli/')), '真实上游 smoke 应由真实 Claude Code CLI 发起')
  console.log(JSON.stringify({
    realAnthropicGatewaySmoke: {
      upstreamBaseUrl,
      model,
      output: output.trim().slice(0, 300),
      incomingRequests: incomingMessages.map((hit) => ({
        path: hit.path,
        method: hit.method,
        authorizationPresent: hit.authorizationPresent,
        xApiKeyPresent: hit.xApiKeyPresent,
        userAgent: hit.userAgent,
        anthropicVersion: hit.anthropicVersion,
        anthropicBeta: hit.anthropicBeta,
        bodySummary: hit.bodySummary
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
    userAgent: hit.userAgent,
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
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  assert.equal(response.status, 503, `通用 Anthropic 客户端的空 SSE 未形成可交付成功，候选耗尽后应返回稳定错误，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /application\/json/, '候选耗尽应返回 Anthropic JSON 错误形态')
  assert.match(text, /upstream_retryable_error/, '候选耗尽必须返回网关稳定可重试码')
  assert.doesNotMatch(text, /sk-ant-empty-sse|upstream_outcome_unknown/, '候选耗尽不得泄漏供应商或旧重放阻断语义')

  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewayCache.clearGatewayRuntimeCache()
  const claudeCodeHitOffset = upstreamHits.length
  const claudeCodeResponse = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'x-api-key': apiKey.key,
      'content-type': 'application/json',
      [gatewayClientProfileHeader]: 'claude_code'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'trigger empty anthropic sse for claude code' }],
      max_tokens: 8,
      stream: true
    })
  })
  const claudeCodeText = await claudeCodeResponse.text()
  assert.equal(claudeCodeResponse.status, 200, `Claude Code SSE 重试耗尽应以协议错误事件返回，实际 HTTP ${claudeCodeResponse.status}: ${claudeCodeText}`)
  assert.match(claudeCodeResponse.headers.get('content-type') ?? '', /text\/event-stream/, 'Claude Code SSE 重试耗尽应保持 text/event-stream')
  assert.match(claudeCodeText, /^event: error$/m, `Claude Code SSE 失败尾包应使用 event: error：${claudeCodeText}`)
  assert.equal(claudeCodeText.includes('event: response.failed'), false, 'Claude Code SSE 失败尾包不应使用 OpenAI response.failed 事件')
  const errorData = claudeCodeText.split(/\r?\n/).find((line) => line.startsWith('data:'))?.slice(5).trim()
  assert(errorData, `Claude Code SSE 失败尾包应包含 data：${claudeCodeText}`)
  const payload = JSON.parse(errorData) as { type?: string; error?: { type?: string; message?: string; code?: string } }
  assert.equal(payload.type, 'error', 'Claude Code SSE 失败尾包顶层 type 应为 error')
  assert.equal(payload.error?.type, 'overloaded_error', 'Claude Code SSE 失败尾包 error.type 应为 overloaded_error')
  assert.equal(payload.error?.code, 'upstream_retryable_error', 'Claude Code SSE 失败尾包应返回网关稳定可重试码')
  assert(payload.error?.message, 'Claude Code SSE 失败尾包应包含错误消息')
  const claudeCodeHits = upstreamHits.slice(claudeCodeHitOffset)
  assert(claudeCodeHits.length > 0, 'Claude Code SSE 重试耗尽必须在本次请求真实命中提前结束 mock 上游')
  assert(claudeCodeHits.every((hit) => hit.xApiKey === 'sk-ant-empty-sse'), 'Claude Code SSE 重试耗尽不得被前一个通用请求的历史命中掩盖')
}

async function assertAnthropicSsePreCommitFailureSwitchesAccount(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic SSE 提交前切号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const failedAccount = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE 提交前空响应账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-empty-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const healthyAccount = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE 提交前健康账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-empty-sse-failover-good',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic SSE 提交前切号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic SSE 提交前切号 API Key 未返回明文密钥')

  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewayCache.clearGatewayRuntimeCache()
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'x-api-key': apiKey.key,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      [gatewayClientProfileHeader]: 'claude_code'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'switch after empty pre-commit SSE' }],
      max_tokens: 16,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic 提交前空 SSE 应切换健康账户，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /anthropic sse ok/)
  assert.doesNotMatch(text, /^event: error$/m, '健康后备账户成功后不得保留前一账户错误尾包')
  assert.deepEqual(
    upstreamHits.map((hit) => hit.xApiKey),
    ['sk-ant-empty-sse', 'sk-ant-empty-sse-failover-good'],
    'Anthropic 提交前协议失败必须只切换一次并返回健康账户结果'
  )
  await assertRequestLocalSwitchDidNotMutateSharedState([failedAccount.id, healthyAccount.id], apiKey.id)
}

async function assertAnthropicHostedWorkUsesUnifiedFailover(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const cases = [
    {
      label: 'server-tool',
      body: { tools: [{ type: 'web_search_20250305', name: 'web_search' }] }
    },
    {
      label: 'mcp',
      body: { mcp_servers: [{ type: 'url', url: 'https://mcp.invalid' }] }
    },
    {
      label: 'container',
      body: { container: 'container_mock' }
    }
  ] as const

  for (const testCase of cases) {
    const group = repositories.createGroup({
      name: `Anthropic ${testCase.label} 统一切号分组`,
      providerCode: ANTHROPIC_PROVIDER_CODE,
      enabled: true
    }, access)
    const failedAccount = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      name: `Anthropic ${testCase.label} 空响应账户`,
      type: 'api_key',
      credentials: {
        api_key: 'sk-ant-empty-sse',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['messages_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      priority: 0
    }, access)
    const fallbackAccount = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      name: `Anthropic ${testCase.label} 后备账户`,
      type: 'api_key',
      credentials: {
        api_key: `sk-ant-${testCase.label}-fallback`,
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['messages_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      priority: 100
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: `Anthropic ${testCase.label} 统一切号 Key`,
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, `Anthropic ${testCase.label} API Key 未返回明文密钥`)

    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    gatewayCache.clearGatewayRuntimeCache()
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/messages`, {
      method: 'POST',
      headers: {
        'x-api-key': apiKey.key,
        'content-type': 'application/json',
        accept: 'text/event-stream',
        [gatewayClientProfileHeader]: 'claude_code'
      },
      body: JSON.stringify({
        model: 'claude-haiku-4-5',
        messages: [{ role: 'user', content: `unified failover ${testCase.label}` }],
        max_tokens: 16,
        stream: true,
        ...testCase.body
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Anthropic ${testCase.label} 未交付成功结果时必须切到健康账户，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /anthropic sse ok/)
    assert.doesNotMatch(text, /upstream_outcome_unknown|upstream_retryable_error/)
    assert.deepEqual(
      upstreamHits.map((hit) => hit.xApiKey),
      ['sk-ant-empty-sse', `sk-ant-${testCase.label}-fallback`],
      `Anthropic ${testCase.label} 必须只命中一次失败首选和一次健康后备`
    )
    await assertRequestLocalSwitchDidNotMutateSharedState([failedAccount.id, fallbackAccount.id], apiKey.id)
  }
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
    bodyIncludes: 'hello claude code profile',
    rawUrl: '/v1/messages?beta=true',
    userAgent: /^claude-cli\/2\.1\.201 \(external, sdk-cli\)$/,
    anthropicBeta: 'claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24',
    expectsClaudeCodeSession: true
  })
  assert(!upstreamHits[0].bodyText.includes('"tools"'), '合成 Claude Code 画像不应伪造工具 schema')
  assert(!upstreamHits[0].bodyText.includes('"thinking"'), '合成 Claude Code 画像不应伪造 thinking 请求体')
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  const prompt = 'messages should still work after unsupported count_tokens'
  const messageResult = await postAnthropicMessage(baseUrl, apiKey.key, prompt)
  assert.equal(messageResult.status, 200, `count_tokens 不支持不应污染账号健康，普通 messages 应继续成功，实际 HTTP ${messageResult.status}: ${messageResult.text}`)
  const currentMessageHits = upstreamHits.filter((hit) => (
    hit.path === '/v1/messages'
    && hit.xApiKey === 'sk-ant-count-unsupported'
    && hit.bodyText.includes(prompt)
  ))
  assert.equal(
    currentMessageHits.length,
    1,
    `count_tokens 不支持后普通 messages 应继续调度同一账户；upstream=${JSON.stringify(upstreamHits.map(summarizeUpstreamHitForError))}`
  )
  assertAnthropicUpstreamHit(currentMessageHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: prompt,
    xApiKey: 'sk-ant-count-unsupported'
  })
}

async function assertAnthropicModelNotFoundDoesNotPoisonMessages(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic model_not_found 不污染账号回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic model_not_found 不污染账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-model-not-found',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    supportedModels: ['claude-fable-5', 'claude-haiku-4-5'],
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic model_not_found 不污染账号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic model_not_found 回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const invalidResponse = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-fable-5',
      messages: [{ role: 'user', content: 'force-model-not-found should not poison account' }],
      max_tokens: 8
    })
  })
  const invalidText = await invalidResponse.text()
  assert.notEqual(invalidResponse.status, 200, `model_not_found 应作为受控失败返回：${invalidText}`)
  assert(
    upstreamHits.some((hit) => hit.path === '/v1/messages' && hit.xApiKey === 'sk-ant-model-not-found'),
    `model_not_found 场景应命中 mock 上游，实际 HTTP ${invalidResponse.status}: ${invalidText}`
  )

  upstreamHits.length = 0
  const prompt = 'messages should still work after model_not_found'
  const messageResult = await postAnthropicMessage(baseUrl, apiKey.key, prompt)
  assert.equal(messageResult.status, 200, `model_not_found 不应污染账号健康，普通 messages 应继续成功，实际 HTTP ${messageResult.status}: ${messageResult.text}`)
  const currentMessageHits = upstreamHits.filter((hit) => (
    hit.path === '/v1/messages'
    && hit.xApiKey === 'sk-ant-model-not-found'
    && hit.bodyText.includes(prompt)
  ))
  assert.equal(
    currentMessageHits.length,
    1,
    `model_not_found 后普通 messages 应继续调度同一账户；upstream=${JSON.stringify(upstreamHits.map(summarizeUpstreamHitForError))}`
  )
  assertAnthropicUpstreamHit(currentMessageHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: prompt,
    xApiKey: 'sk-ant-model-not-found'
  })
}

async function assertAnthropicEmptyJsonContentUsesUnifiedFailureHandling(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic empty content 协议守卫回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic empty content 协议守卫账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-empty-json',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic empty content 协议守卫 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic empty content 回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      messages: [{ role: 'user', content: 'empty content should be guarded' }],
      max_tokens: 8
    })
  })
  const text = await response.text()
  assert.equal(response.status, 502, `Anthropic empty content 必须被识别为协议错误，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /upstream_protocol_error/)
  assert.doesNotMatch(text, /upstream_outcome_unknown|"content"\s*:\s*\[\]/)
  assert.equal(upstreamHits.length, 1, 'Anthropic empty content 候选耗尽应只命中唯一账户一次')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'empty content should be guarded',
    xApiKey: 'sk-ant-empty-json'
  })
}

async function assertAnthropicModels(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/models`, {
    headers: {
      'x-api-key': localApiKey,
      'anthropic-version': '2023-06-01'
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

  const fallbackResponse = await fetch(`${baseUrl}/v1/models`, {
    headers: {
      authorization: `Bearer ${localApiKey}`
    }
  })
  const fallbackText = await fallbackResponse.text()
  assert.equal(fallbackResponse.status, 200, `未识别客户端 /v1/models 应默认返回 OpenAI 模型目录，实际 HTTP ${fallbackResponse.status}: ${fallbackText}`)
  const fallbackBody = JSON.parse(fallbackText) as { object?: string; data?: unknown[]; has_more?: boolean }
  assert.equal(fallbackBody.object, 'list', '未识别客户端 /v1/models 应返回 OpenAI object=list')
  assert(Array.isArray(fallbackBody.data), '未识别客户端 /v1/models 应返回 OpenAI data 数组')
  assert.equal(Object.prototype.hasOwnProperty.call(fallbackBody, 'has_more'), false, '未识别客户端 /v1/models 不应返回 Anthropic has_more 字段')
  assert.equal(upstreamHits.length, 0, '未识别客户端 /v1/models 应由本地模型目录响应，不应打到上游 mock')
}

function assertAnthropicModelMappingIsOpenAIProtocolOnly(upstreamBaseUrl: string): void {
  const sourceModel = 'claude-haiku-4-5'
  const upstreamModel = 'claude-haiku-4-5-20251001'
  const group = repositories.createGroup({
    name: 'Anthropic 模型映射回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  assert.throws(() => repositories.createAccount({
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
      {
        sourceModel,
        sourceEndpointFamily: 'chat_completions',
        upstreamModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }
    ],
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access), /当前供应商协议不支持 OpenAI 账号模型别名/, 'Anthropic Messages 账号不应允许配置 OpenAI 协议模型映射')
}

async function assertAnthropicApiKeyPoolOpaqueFailover(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic 多 Key 隔离回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const sourceAccount = repositories.createAccount({
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
  const rescueAccount = repositories.createAccount({
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic 多 Key 隔离回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic 多 Key 隔离回归 API Key 未返回明文密钥')

  upstreamHits.length = 0
  const first = await postAnthropicMessage(baseUrl, apiKey.key, 'trigger anthropic key pool failover')
  assert.equal(first.status, 200, `通用 Anthropic 文本请求应 opaque 切换到同账户健康 Key，实际 HTTP ${first.status}: ${first.text}`)
  assert.match(first.text, /anthropic json ok/, '通用 Anthropic 客户端应收到健康 Key 的完整响应')
  assert.doesNotMatch(first.text, /mock key pool bad key/, '网关不得把中间坏 Key 的供应商原始错误泄漏给客户端')
  assert.deepEqual(
    upstreamHits.map((hit) => hit.xApiKey),
    ['sk-ant-keypool-bad', 'sk-ant-keypool-good'],
    '通用 Anthropic 文本请求应只依据完整 HTTP 失败做请求内 opaque Key 轮换，并优先耗尽兄弟 Key'
  )
  await assertConfirmedSameAccountKeyFailureIsScoped(sourceAccount.id, rescueAccount.id, apiKey.id)
}

async function assertAnthropicConfiguredResponseInspectionSwitchesRequestLocally(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const group = repositories.createGroup({
    name: 'Anthropic 响应检查换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const pollutedAccount = repositories.createAccount({
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
  const cleanAccount = repositories.createAccount({
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  assert.equal(response.status, 200, `用户明确配置的响应策略应在下游提交前切到健康账户，实际 HTTP ${response.status}: ${text}`)
  assert.deepEqual(upstreamHits.map((hit) => hit.xApiKey), ['sk-ant-polluted', 'sk-ant-clean'], '响应检查策略只能在当前请求内有界切到下一账户')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic response inspection',
    xApiKey: 'sk-ant-polluted'
  })
  const body = JSON.parse(text) as { content?: Array<{ text?: string }> }
  assert.equal(body.content?.[0]?.text, 'anthropic clean ok', `客户端只能看到切号后的健康响应：${text}`)
  assert.doesNotMatch(text, /ANTHROPIC-POLLUTED/, '中间账户的失败内容不得泄漏给客户端')
  await assertRequestLocalSwitchDidNotMutateSharedState([pollutedAccount.id, cleanAccount.id], apiKey.id)
}

async function assertAnthropicJsonErrorSwitchesRequestLocally(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  createAnthropicOverloadedErrorSwitchPolicy()
  const group = repositories.createGroup({
    name: 'Anthropic JSON error 换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const failedAccount = repositories.createAccount({
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
  const cleanAccount = repositories.createAccount({
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  assert.equal(response.status, 200, `明确 JSON type:error 应在下游提交前切到健康账户，实际 HTTP ${response.status}: ${text}`)
  assert.deepEqual(upstreamHits.map((hit) => hit.xApiKey), ['sk-ant-error-json', 'sk-ant-clean'], '明确 JSON type:error 只能在当前请求内有界切号一次')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic json error inspection',
    xApiKey: 'sk-ant-error-json'
  })
  const body = JSON.parse(text) as { content?: Array<{ text?: string }> }
  assert.equal(body.content?.[0]?.text, 'anthropic clean ok', `客户端只能看到切号后的健康 JSON：${text}`)
  assert.doesNotMatch(text, /mock overloaded/, '中间 JSON type:error 不得泄漏给客户端')
  await assertRequestLocalSwitchDidNotMutateSharedState([failedAccount.id, cleanAccount.id], apiKey.id)
}

async function assertAnthropicSseErrorSwitchesRequestLocally(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  createAnthropicOverloadedErrorSwitchPolicy()
  const group = repositories.createGroup({
    name: 'Anthropic SSE error 换号分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const failedAccount = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE error 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-error-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const cleanAccount = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE error 干净账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-clean-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  assert.equal(response.status, 200, `明确 event:error 应在下游提交前切到健康账户，实际 HTTP ${response.status}: ${text}`)
  assert.deepEqual(upstreamHits.map((hit) => hit.xApiKey), ['sk-ant-error-sse', 'sk-ant-clean-sse'], '明确 event:error 只能在当前请求内有界切号一次')
  assert.match(text, /anthropic sse ok/, `客户端应收到健康账户的完整 SSE：${text}`)
  assert.doesNotMatch(text, /mock overloaded/, '中间 event:error 不得泄漏给客户端')
  assert.doesNotMatch(text, /^event: error$/m, '客户端不得看到中间账户的错误事件')
  assertAnthropicUpstreamHit(upstreamHits[0], {
    path: '/v1/messages',
    method: 'POST',
    bodyIncludes: 'trigger anthropic sse error inspection',
    xApiKey: 'sk-ant-error-sse'
  })
  await assertRequestLocalSwitchDidNotMutateSharedState([failedAccount.id, cleanAccount.id], apiKey.id)
}

async function assertAnthropicSseErrorFieldStaysNeutral(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  createAnthropicOverloadedErrorSwitchPolicy()
  const group = repositories.createGroup({
    name: 'Anthropic SSE 普通 error 字段分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: 'Anthropic SSE 普通 error 字段账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-error-field-sse',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic SSE 普通 error 字段 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, 'Anthropic SSE 普通 error 字段 API Key 未返回明文密钥')

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
      messages: [{ role: 'user', content: 'trigger anthropic ordinary event error field' }],
      max_tokens: 16,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `普通事件的 error 字段必须保持中性，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 1, '普通事件的 data.error 不得触发切号')
  assert.equal(upstreamHits[0]?.xApiKey, 'sk-ant-error-field-sse')
  assert.match(text, /anthropic sse ok/, `普通事件仍应完整透传可见输出：${text}`)
  assert.doesNotMatch(text, /^event: error$/m, '普通事件的 data.error 不得被改写为错误事件')
  assert.equal(inspectAnthropicStreamText(text).failedReceived, false, '普通事件的 data.error 不得标记协议失败')
  await assertRequestLocalSwitchDidNotMutateSharedState([account.id], apiKey.id)
}

async function assertRequestLocalSwitchDidNotMutateSharedState(accountIds: string[], gatewayApiKeyId: string): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  for (const accountId of accountIds) {
    const summary = repositories.findAccountForTest(accountId, access)
    assert(summary, `缺少账户状态摘要：${accountId}`)
    assert.equal(summary.status, 'active', `请求内切号不得修改账户状态：${accountId}`)
    assert.equal(summary.schedulable, true, `请求内切号不得关闭账户调度：${accountId}`)
    assert.equal(summary.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `请求内切号不得写 Key temporary_unavailable：${accountId}`)
    assert.equal(summary.apiKeyRuntime?.rateLimited ?? 0, 0, `请求内切号不得写 Key rate_limited：${accountId}`)
    assert.equal(summary.apiKeyRuntime?.error ?? 0, 0, `请求内切号不得写 Key error：${accountId}`)
    assert.equal(summary.apiKeyRuntime?.disabled ?? 0, 0, `请求内切号不得写 Key disabled：${accountId}`)
  }
  assert.equal(repositories.findApiKeySummary(gatewayApiKeyId, access)?.status, 'active', '请求内切号不得修改客户端网关 API Key 状态')
}

async function assertConfirmedSameAccountKeyFailureIsScoped(
  sourceAccountId: string,
  rescueAccountId: string,
  gatewayApiKeyId: string
): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const source = repositories.findAccountForTest(sourceAccountId, access)
  assert(source, `缺少同账户 Key 确认来源账户：${sourceAccountId}`)
  assert.equal(source.status, 'active', '同账户 Key 确认不得修改账户状态')
  assert.equal(source.schedulable, true, '同账户 Key 确认不得关闭账户调度')
  assert.equal(source.apiKeyRuntime?.temporaryUnavailable ?? 0, 1, '后继 Key 成功后必须只标记失败 Key 为临时避让')
  assert.equal(source.apiKeyRuntime?.rateLimited ?? 0, 0)
  assert.equal(source.apiKeyRuntime?.error ?? 0, 0)
  assert.equal(source.apiKeyRuntime?.disabled ?? 0, 0)
  await assertRequestLocalSwitchDidNotMutateSharedState([rescueAccountId], gatewayApiKeyId)
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
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
  assert.equal(response.status, 503, `OpenAI 分组不应承接 Anthropic Messages，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /请求路径|客户端协议|request_capability_mismatch|没有可路由模型|model_not_routable_for_api_key/)
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
      input: 'must dispatch openai responses to anthropic messages bridge',
      stream: true
    })
  })
  const text = await response.text()
  assert.notEqual(response.status, 200, `Anthropic 原生分组不应承接 OpenAI Responses，实际响应成功：${text}`)
  assert.match(text, /anthropic_native_group_openai_compatible_request|不兼容 Codex|OpenAI 请求路径/, 'Anthropic 原生分组拒绝 OpenAI Responses 时应返回稳定边界错误')
  assert.equal(upstreamHits.length, 0, 'Anthropic 原生分组拒绝 OpenAI Responses 时不应命中上游')
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
    '1'
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
    child.stdin.end(`${input.prompt}\n`)
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

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function truthyEnv(name: string): boolean {
  const value = process.env[name]
  return value !== undefined && ['1', 'true', 'yes', 'on'].includes(value.trim().toLowerCase())
}

async function removeTempRootForTest(path: string): Promise<void> {
  const maxAttempts = 30
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      if (!isWindowsTempCleanupBusyError(error) || attempt >= maxAttempts) {
        console.warn(`Anthropic mock 回归临时目录清理失败: ${error instanceof Error ? error.message : String(error)}`)
        return
      }
      await sleep(200)
    }
  }
}

function isWindowsTempCleanupBusyError(error: unknown): boolean {
  return typeof error === 'object'
    && error !== null
    && 'code' in error
    && (error as { code?: unknown }).code === 'EBUSY'
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function assertAnthropicUpstreamHit(hit: AnthropicUpstreamHit | undefined, input: {
  rawUrl?: string
  path: string
  method: string
  bodyIncludes: string
  xApiKey?: string
  anthropicBeta?: string
  userAgent?: string | RegExp
  expectsClaudeCodeSession?: boolean
}): void {
  assert(hit, '缺少 Anthropic mock 上游命中记录')
  if (input.rawUrl !== undefined) {
    assert.equal(hit.rawUrl, input.rawUrl)
  }
  assert.equal(hit.path, input.path)
  assert.equal(hit.method, input.method)
  assert.equal(hit.xApiKey, input.xApiKey ?? 'sk-ant-upstream', '上游 x-api-key 应替换为账户 API Key')
  assert.equal(hit.authorization, '', 'Anthropic 上游不应收到本地 Authorization/Bearer')
  assert.equal(hit.clientProfileHeader, '', 'Anthropic 上游不应收到本地客户端画像 header')
  assert.equal(hit.anthropicVersion, '2023-06-01', '缺省 Anthropic-Version 应按官方默认版本补齐')
  assert.equal(hit.anthropicBeta, input.anthropicBeta ?? '', 'Anthropic beta 头应按当前画像边界处理')
  if (input.expectsClaudeCodeSession) {
    assert.notEqual(hit.claudeCodeSessionId, '', 'Claude Code 画像请求应补齐上游 session header')
  }
  if (input.userAgent instanceof RegExp) {
    assert.match(hit.userAgent, input.userAgent, '上游 User-Agent 不符合预期')
  } else if (input.userAgent !== undefined) {
    assert.equal(hit.userAgent, input.userAgent, '上游 User-Agent 不符合预期')
  }
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
        userAgent: String(req.headers['user-agent'] ?? ''),
        clientProfileHeader: String(req.headers[gatewayClientProfileHeader] ?? ''),
        anthropicVersion: String(req.headers['anthropic-version'] ?? ''),
        anthropicBeta: String(req.headers['anthropic-beta'] ?? ''),
        claudeCodeSessionId: String(req.headers['x-claude-code-session-id'] ?? ''),
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
          } else if (req.headers['x-api-key'] === 'sk-ant-error-field-sse') {
            sendAnthropicSse(res, true)
          } else {
            sendAnthropicSse(res)
          }
        } else {
            sendAnthropicJson(res, String(req.headers['x-api-key'] ?? ''), bodyText)
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'mock path not found' } }))
    })
  })
}

function sendAnthropicJson(res: http.ServerResponse, xApiKey: string, bodyText: string): void {
  if (xApiKey === 'sk-ant-model-not-found' && bodyText.includes('force-model-not-found')) {
    res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      type: 'error',
      error: {
        code: 'model_not_found',
        type: 'new_api_error',
        message: 'No available channel for model juhe-invalid-model under group default'
      }
    }))
    return
  }
  if (xApiKey === 'sk-ant-empty-json') {
    res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      id: 'msg_mock_empty_json',
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      stop_reason: 'end_turn',
      usage: {
        input_tokens: 0,
        output_tokens: 0
      }
    }))
    return
  }
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

function sendAnthropicSse(res: http.ServerResponse, includeErrorField = false): void {
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
    delta: { type: 'text_delta', text: 'anthropic sse ok' },
    ...(includeErrorField
      ? {
          error: { type: 'overloaded_error', message: 'ordinary field only' },
          metadata: { error: 'diagnostic only' }
        }
      : {})
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
