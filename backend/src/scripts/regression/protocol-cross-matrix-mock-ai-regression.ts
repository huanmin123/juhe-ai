import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type {
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  ApiKeyExplicitHybridRouteRule
} from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface UpstreamHit {
  method: string
  rawUrl: string
  path: string
  authorization: string
  xApiKey: string
  xGoogApiKey: string
  anthropicVersion: string
  contentType: string
  accept: string
  bodyText: string
  body: Record<string, unknown>
}

interface CrossRuntime {
  apiKey: string
  groupId: string
  accountId: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-protocol-cross-matrix-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'protocol-cross-matrix-mock-ai-secret'
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
const upstreamHits: UpstreamHit[] = []
const png1x1Base64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII='
const pngDataUrl = `data:image/png;base64,${png1x1Base64}`

const openAIChatSourceModel = 'cross-gpt-chat-source'
const openAIResponsesSourceModel = 'cross-gpt-responses-source'
const anthropicMessagesSourceModel = 'cross-claude-messages-source'
const geminiGenerateContentSourceModel = 'cross-gemini-native-source'
const openAIChatUpstreamModel = 'cross-openai-chat-upstream'
const anthropicMessagesUpstreamModel = 'cross-claude-messages-upstream'
const geminiGenerateContentUpstreamModel = 'cross-gemini-native-upstream'
const nativeResponsesUpstreamModel = 'cross-openai-responses-native-upstream'

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
app.use('/v1beta', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    registerCustomModels()
    upstreamServer = createCrossProtocolMockUpstream()
    await listen(upstreamServer)
    const upstreamOrigin = `http://127.0.0.1:${serverAddress(upstreamServer).port}`

    const chatRuntime = createOpenAIChatTargetRuntime(upstreamOrigin)
    const messagesRuntime = createAnthropicMessagesTargetRuntime(upstreamOrigin)
    const geminiRuntime = createGeminiNativeTargetRuntime(upstreamOrigin)
    const guidanceFallbackRuntime = createAgentGuidanceFallbackRuntime(upstreamOrigin)
    assertForbiddenResponsesTargets(upstreamOrigin)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertResponsesToChatComplex(baseUrl, chatRuntime.apiKey)
    await assertMessagesToChatComplex(baseUrl, chatRuntime.apiKey)
    await assertGeminiToChatSseComplex(baseUrl, chatRuntime.apiKey)
    await assertChatToAnthropicMessagesComplex(baseUrl, messagesRuntime.apiKey)
    await assertChatToAnthropicStructuredToolGuidance(baseUrl, messagesRuntime.apiKey)
    await assertChatToAnthropicOutputCapabilityGuidance(baseUrl, messagesRuntime.apiKey)
    await assertAccountScopedGuidanceFallsBackToNextGroup(baseUrl, guidanceFallbackRuntime.apiKey)
    await assertResponsesToAnthropicMessagesComplex(baseUrl, messagesRuntime.apiKey)
    await assertResponsesToAnthropicStructuredToolGuidance(baseUrl, messagesRuntime.apiKey)
    await assertResponsesToAnthropicCapabilityGuidance(baseUrl, messagesRuntime.apiKey)
    await assertResponsesToAnthropicInvalidRequestStillRejected(baseUrl, messagesRuntime.apiKey)
    await assertGeminiToAnthropicMessagesComplex(baseUrl, messagesRuntime.apiKey)
    await assertGeminiToAnthropicStructuredOutputGuidance(baseUrl, messagesRuntime.apiKey)
    await assertChatToGeminiNativeSseComplex(baseUrl, geminiRuntime.apiKey)
    await assertResponsesToGeminiNativeComplex(baseUrl, geminiRuntime.apiKey)
    await assertAnthropicMessagesToGeminiNativeComplex(baseUrl, geminiRuntime.apiKey)
    await assertResponsesStateToGeminiNativeGuidance(baseUrl, geminiRuntime.apiKey)

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    assertUsageRecords()

    console.log('protocol cross matrix mock ai regression passed')
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
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: openAIChatSourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: openAIResponsesSourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicMessagesSourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiGenerateContentSourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: openAIChatUpstreamModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: openAIChatSourceModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: nativeResponsesUpstreamModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicMessagesUpstreamModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiGenerateContentUpstreamModel,
    scope: 'global',
    status: 'active',
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: access.systemAccountId
  })
}

function createOpenAIChatTargetRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 OpenAI Chat 上游分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '协议交叉矩阵 OpenAI Chat 上游账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-openai-chat-upstream',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    supportedModels: [openAIChatUpstreamModel],
    status: 'active',
    schedulable: true
  }, access)
  return runtime(group.id, account.id, '协议交叉矩阵 OpenAI Chat Key', [
    explicitRule('responses_to_chat', group.id, openAIResponsesSourceModel, 'responses', openAIChatUpstreamModel, 'chat_completions'),
    explicitRule('messages_to_chat', group.id, anthropicMessagesSourceModel, 'messages', openAIChatUpstreamModel, 'chat_completions'),
    explicitRule('gemini_generate_to_chat', group.id, geminiGenerateContentSourceModel, 'generate_content', openAIChatUpstreamModel, 'chat_completions'),
    explicitRule('gemini_stream_to_chat', group.id, geminiGenerateContentSourceModel, 'stream_generate_content', openAIChatUpstreamModel, 'chat_completions')
  ])
}

function createAnthropicMessagesTargetRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 Anthropic Messages 上游分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: '协议交叉矩阵 Anthropic Messages 上游账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-anthropic-messages-upstream',
      base_url: upstreamOrigin,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: group.id,
    supportedModels: [anthropicMessagesUpstreamModel],
    status: 'active',
    schedulable: true
  }, access)
  return runtime(group.id, account.id, '协议交叉矩阵 Anthropic Messages Key', [
    explicitRule('chat_to_messages', group.id, openAIChatSourceModel, 'chat_completions', anthropicMessagesUpstreamModel, 'messages'),
    explicitRule('responses_to_messages', group.id, openAIResponsesSourceModel, 'responses', anthropicMessagesUpstreamModel, 'messages'),
    explicitRule('gemini_generate_to_messages', group.id, geminiGenerateContentSourceModel, 'generate_content', anthropicMessagesUpstreamModel, 'messages'),
    explicitRule('gemini_stream_to_messages', group.id, geminiGenerateContentSourceModel, 'stream_generate_content', anthropicMessagesUpstreamModel, 'messages')
  ])
}

function createGeminiNativeTargetRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 Gemini native 上游分组',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: '协议交叉矩阵 Gemini native 上游账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-gemini-native-upstream',
      base_url: upstreamOrigin,
      supported_endpoint_modes: ['generate_content_json', 'generate_content_sse']
    },
    groupId: group.id,
    supportedModels: [geminiGenerateContentUpstreamModel],
    status: 'active',
    schedulable: true
  }, access)
  return runtime(group.id, account.id, '协议交叉矩阵 Gemini native Key', [
    explicitRule('chat_to_gemini_generate', group.id, openAIChatSourceModel, 'chat_completions', geminiGenerateContentUpstreamModel, 'generate_content'),
    explicitRule('responses_to_gemini_generate', group.id, openAIResponsesSourceModel, 'responses', geminiGenerateContentUpstreamModel, 'generate_content'),
    explicitRule('messages_to_gemini_generate', group.id, anthropicMessagesSourceModel, 'messages', geminiGenerateContentUpstreamModel, 'generate_content')
  ])
}

function createAgentGuidanceFallbackRuntime(upstreamOrigin: string): CrossRuntime {
  const primaryGroup = repositories.createGroup({
    name: '协议交叉矩阵 guidance 主 Anthropic 号池',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  const primaryAccount = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: '协议交叉矩阵 guidance 主 Anthropic 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-guidance-primary-anthropic',
      base_url: upstreamOrigin,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: primaryGroup.id,
    supportedModels: [anthropicMessagesUpstreamModel],
    status: 'active',
    schedulable: true
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '协议交叉矩阵 guidance 后备 OpenAI Chat 号池',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '协议交叉矩阵 guidance 后备 OpenAI Chat 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-guidance-fallback-openai-chat',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: fallbackGroup.id,
    supportedModels: [openAIChatUpstreamModel],
    modelMappings: [
      mapping(openAIChatSourceModel, 'chat_completions', openAIChatUpstreamModel, 'chat_completions')
    ],
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '协议交叉矩阵 guidance 后备 Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ],
    explicitHybridRouteRules: [
      explicitRule('guidance_primary_chat_to_messages', primaryGroup.id, openAIChatSourceModel, 'chat_completions', anthropicMessagesUpstreamModel, 'messages')
    ],
    status: 'active'
  }, access)
  assert(apiKey.key, '协议交叉矩阵 guidance 后备 Key 未返回明文密钥')
  return { apiKey: apiKey.key, groupId: primaryGroup.id, accountId: primaryAccount.id }
}

function assertForbiddenResponsesTargets(upstreamOrigin: string): void {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 Responses 禁止方向分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  const base = {
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    type: 'api_key' as const,
    credentials: {
      api_key: 'sk-cross-forbidden-responses',
      base_url: `${upstreamOrigin}/forbidden`,
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    groupId: group.id,
    supportedModels: [nativeResponsesUpstreamModel],
    status: 'active' as const,
    schedulable: true
  }
  repositories.createAccount({
    ...base,
    name: 'Responses 禁止方向目标账号'
  }, access)
  assert.throws(() => runtime(group.id, '', '禁止 Chat 到 Responses Key', [
    explicitRule('forbidden_chat_to_responses', group.id, openAIChatSourceModel, 'chat_completions', nativeResponsesUpstreamModel, 'responses')
  ]), /显式混合路由暂不支持 Chat Completions 到 Responses 的协议转换/)
  assert.throws(() => runtime(group.id, '', '禁止 Messages 到 Responses Key', [
    explicitRule('forbidden_messages_to_responses', group.id, anthropicMessagesSourceModel, 'messages', nativeResponsesUpstreamModel, 'responses')
  ]), /显式混合路由暂不支持 Messages 到 Responses 的协议转换/)
  assert.throws(() => runtime(group.id, '', '禁止 Gemini 到 Responses Key', [
    explicitRule('forbidden_gemini_to_responses', group.id, geminiGenerateContentSourceModel, 'generate_content', nativeResponsesUpstreamModel, 'responses')
  ]), /显式混合路由暂不支持 Gemini GenerateContent 到 Responses 的协议转换/)
}

function runtime(groupId: string, accountId: string, name: string, explicitHybridRouteRules?: ApiKeyExplicitHybridRouteRule[]): CrossRuntime {
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    explicitHybridRouteRules,
    status: 'active'
  }, access)
  assert(apiKey.key, `${name} 未返回明文密钥`)
  return { apiKey: apiKey.key, groupId, accountId }
}

function explicitRule(
  id: string,
  targetGroupId: string,
  sourceModel: string,
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamModel: string,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): ApiKeyExplicitHybridRouteRule {
  return {
    id,
    enabled: true,
    priority: 1,
    sourceClientProfile: 'auto',
    sourceEndpointFamily,
    sourceModel,
    targetGroupId,
    upstreamEndpointFamily,
    upstreamModel,
    adapterMode: sourceEndpointFamily === upstreamEndpointFamily ? 'direct' : 'bridge'
  }
}

function mapping(
  sourceModel: string,
  sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content',
  upstreamModel: string,
  upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content'
) {
  return {
    sourceModel,
    sourceEndpointFamily,
    upstreamModel,
    upstreamEndpointFamily,
    enabled: true
  }
}

async function assertResponsesToChatComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, responsesRequest(false))
  const text = await response.text()
  assert.equal(response.status, 200, `Responses -> Chat JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /chat-tool|function_call|lookup/, 'Responses -> Chat 应把 Chat tool call 还原为 Responses function_call')
  const hit = onlyNewHit(start)
  assert(hit.path.endsWith('/v1/chat/completions'), 'Responses -> Chat 应命中 Chat Completions 上游')
  assert.equal(hit.authorization, 'Bearer sk-cross-openai-chat-upstream')
  assert.equal(hit.body.model, openAIChatUpstreamModel)
  assert(Array.isArray(hit.body.messages), 'Responses -> Chat 上游体必须包含 Chat messages')
  assert.match(hit.bodyText, /input_image|image_url|function_call_output|tool/, 'Responses -> Chat 复杂输入应进入 Chat 上游体')
  assert.equal((hit.body.tools as unknown[] | undefined)?.length, 1, 'Responses function tool 应转换为 Chat tools')
}

async function assertMessagesToChatComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/messages', localApiKey, anthropicMessagesRequest(false), {
    'anthropic-version': '2023-06-01'
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Chat JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /tool_use|lookup|chat-tool/, 'Messages -> Chat 应把 Chat tool call 还原为 Anthropic tool_use')
  const hit = onlyNewHit(start)
  assert(hit.path.endsWith('/v1/chat/completions'), 'Messages -> Chat 应命中 Chat Completions 上游')
  assert.equal(hit.body.model, openAIChatUpstreamModel)
  assert.match(hit.bodyText, /image_url|tool_result|lookup/, 'Messages -> Chat 图片和工具结果应进入 Chat 上游体')
}

async function assertGeminiToChatSseComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, `/v1beta/models/${geminiGenerateContentSourceModel}:streamGenerateContent?alt=sse&trace=cross-gemini-to-chat`, localApiKey, geminiGenerateContentRequest())
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini -> Chat SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /data:/, 'Gemini -> Chat SSE 下游必须返回 SSE')
  assert.match(text, /chat stream text/, 'Gemini -> Chat SSE 应透出 Chat 文本')
  const hit = onlyNewHit(start)
  assert(hit.path.endsWith('/v1/chat/completions'), 'Gemini -> Chat 应命中 Chat Completions 上游')
  assert.equal(hit.body.model, openAIChatUpstreamModel)
  assert.equal(hit.body.stream, true)
  assert.match(hit.bodyText, /inlineData|functionDeclarations|functionResponse|lookup/, 'Gemini 复杂输入应转换进 Chat 上游体')
}

async function assertChatToAnthropicMessagesComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, chatCompletionsRequest(false, {
    structuredOutput: false
  }))
  const text = await response.text()
  assert.equal(response.status, 200, `Chat -> Messages JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /tool_calls|lookup|anthropic-tool/, 'Chat -> Messages 应把 Anthropic tool_use 还原为 Chat tool_calls')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, '/v1/messages')
  assert.equal(hit.xApiKey, 'sk-cross-anthropic-messages-upstream')
  assert.equal(hit.body.model, anthropicMessagesUpstreamModel)
  assert.match(hit.bodyText, /image|tool_result|lookup/, 'Chat 图片和工具定义应转换进 Messages 上游体')
}

async function assertChatToAnthropicStructuredToolGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, chatCompletionsRequest(false))
  const text = await response.text()
  assert.equal(response.status, 200, `Chat strict schema + tools -> Messages 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/, 'Chat guidance 必须保持 Chat Completions JSON 形态')
  assert.match(text, /strict JSON schema|用户工具|structured_output_with_tools/, 'Chat -> Messages 不支持 strict schema + tools 时必须返回可读 guidance')
  assert.equal(upstreamHits.length, start, 'Chat strict schema + tools guidance 不应命中 Anthropic 上游')
}

async function assertChatToAnthropicOutputCapabilityGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const cases: Array<{
    label: string
    body: Record<string, unknown>
    expected: RegExp
  }> = [
    {
      label: 'Chat logprobs',
      body: {
        ...chatCompletionsRequest(false, { structuredOutput: false }),
        logprobs: true,
        top_logprobs: 2
      },
      expected: /logprobs|openai_anthropic_bridge_logprobs_unsupported/
    },
    {
      label: 'Chat audio output modality',
      body: {
        ...chatCompletionsRequest(false, { structuredOutput: false }),
        modalities: ['text', 'audio'],
        audio: { voice: 'alloy', format: 'mp3' }
      },
      expected: /audio output|openai_anthropic_bridge_output_modality_unsupported/
    },
    {
      label: 'Chat multiple choices',
      body: {
        ...chatCompletionsRequest(false, { structuredOutput: false }),
        n: 2
      },
      expected: /一个 Chat choice|openai_anthropic_bridge_multiple_choices_unsupported/
    }
  ]

  for (const item of cases) {
    const start = upstreamHits.length
    const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, item.body)
    const text = await response.text()
    assert.equal(response.status, 200, `${item.label} -> Messages 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /"object":"chat\.completion"/, `${item.label} guidance 必须保持 Chat Completions JSON 形态`)
    assert.match(text, item.expected, `${item.label} guidance 应说明目标能力缺口`)
    assert.equal(upstreamHits.length, start, `${item.label} guidance 不应命中 Anthropic 上游`)
  }
}

async function assertAccountScopedGuidanceFallsBackToNextGroup(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, {
    ...chatCompletionsRequest(false, { structuredOutput: false }),
    logprobs: true,
    top_logprobs: 2
  })
  const text = await response.text()
  assert.equal(response.status, 200, `账号作用域 guidance 应先切后备 OpenAI Chat 成功，实际 HTTP ${response.status}: ${text}`)
  assert.doesNotMatch(text, /能力未执行|openai_anthropic_bridge_logprobs_unsupported|agent_guidance/, '有后备账号可用时不应把 Anthropic guidance 提前返回给客户端')
  const hit = onlyNewHit(start)
  assert(hit.path.endsWith('/v1/chat/completions'), '账号作用域 guidance 后应命中后备 Chat Completions 上游')
  assert.equal(hit.authorization, 'Bearer sk-cross-guidance-fallback-openai-chat')
  assert.equal(hit.body.model, openAIChatUpstreamModel)
  assert.equal(hit.body.logprobs, true, 'Chat logprobs 请求应原样交给可承载的 OpenAI Chat 后备上游')
}

async function assertResponsesToAnthropicMessagesComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, responsesRequest(false, {
    structuredOutput: false
  }))
  const text = await response.text()
  assert.equal(response.status, 200, `Responses -> Messages JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /function_call|anthropic-tool|lookup/, 'Responses -> Messages 应把 Anthropic tool_use 还原为 Responses function_call')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, '/v1/messages')
  assert.equal(hit.body.model, anthropicMessagesUpstreamModel)
  assert.match(hit.bodyText, /input_image|tool_result|lookup/, 'Responses 复杂输入应转换进 Messages 上游体')
}

async function assertResponsesToAnthropicStructuredToolGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, responsesRequest(false))
  const text = await response.text()
  assert.equal(response.status, 200, `Responses strict schema + tools -> Messages 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/, 'Responses guidance 必须保持 Responses JSON 形态')
  assert.match(text, /"status":"completed"/, 'Responses guidance 必须以 completed 响应结束，避免客户端按失败处理')
  assert.match(text, /strict JSON schema|用户工具|structured_output_with_tools/, 'Responses -> Messages 不支持 strict schema + tools 时必须返回可读 guidance')
  assert.equal(upstreamHits.length, start, 'Responses strict schema + tools guidance 不应命中 Anthropic 上游')
}

async function assertResponsesToAnthropicCapabilityGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const cases: Array<{
    label: string
    body: Record<string, unknown>
    expected: RegExp
  }> = [
    {
      label: 'Responses output_text logprobs include',
      body: {
        ...responsesRequest(false, { structuredOutput: false }),
        include: ['message.output_text.logprobs']
      },
      expected: /output_text logprobs|openai_anthropic_bridge_logprobs_unsupported/
    },
    {
      label: 'Responses max_tool_calls',
      body: {
        ...responsesRequest(false, { structuredOutput: false }),
        max_tool_calls: 1
      },
      expected: /max_tool_calls|openai_anthropic_bridge_max_tool_calls_unsupported/
    },
    {
      label: 'Responses context_management',
      body: {
        ...responsesRequest(false, { structuredOutput: false }),
        context_management: { truncation: 'auto' }
      },
      expected: /context_management|openai_anthropic_bridge_context_management_unsupported/
    },
    {
      label: 'Responses service_tier',
      body: {
        ...responsesRequest(false, { structuredOutput: false }),
        service_tier: 'flex'
      },
      expected: /service_tier|openai_anthropic_bridge_service_tier_unsupported/
    }
  ]

  for (const item of cases) {
    const start = upstreamHits.length
    const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, item.body)
    const text = await response.text()
    assert.equal(response.status, 200, `${item.label} -> Messages 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /"object":"response"/, `${item.label} guidance 必须保持 Responses JSON 形态`)
    assert.match(text, /"status":"completed"/, `${item.label} guidance 必须以 completed 响应结束`)
    assert.match(text, item.expected, `${item.label} guidance 应说明目标能力缺口`)
    assert.equal(upstreamHits.length, start, `${item.label} guidance 不应命中 Anthropic 上游`)
  }
}

async function assertResponsesToAnthropicInvalidRequestStillRejected(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, {
    ...responsesRequest(false, { structuredOutput: false }),
    include: 'message.output_text.logprobs'
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses include 非数组属于非法请求，必须继续硬拒绝，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /include 必须是字符串数组|openai_anthropic_bridge_include_unsupported/)
  assert.equal(upstreamHits.length, start, '非法 include 请求不应命中 Anthropic 上游')
}

async function assertGeminiToAnthropicMessagesComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(
    baseUrl,
    `/v1beta/models/${geminiGenerateContentSourceModel}:generateContent?trace=cross-gemini-to-messages`,
    localApiKey,
    geminiGenerateContentRequest({ structuredOutput: false })
  )
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini -> Messages JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /functionCall|anthropic-tool|lookup/, 'Gemini -> Messages 应把 Anthropic tool_use 还原为 Gemini functionCall')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, '/v1/messages')
  assert.equal(hit.body.model, anthropicMessagesUpstreamModel)
  assert.match(hit.bodyText, /inlineData|functionResponse|lookup/, 'Gemini 复杂输入应转换进 Messages 上游体')
}

async function assertGeminiToAnthropicStructuredOutputGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(
    baseUrl,
    `/v1beta/models/${geminiGenerateContentSourceModel}:generateContent?trace=cross-gemini-to-messages-structured`,
    localApiKey,
    geminiGenerateContentRequest()
  )
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini JSON schema -> Messages guidance 应返回可读响应，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /responseMimeType=application\/json|结构化输出|Anthropic/, 'Gemini -> Messages 不支持 JSON schema 时必须返回 guidance')
  assert.equal(upstreamHits.length, start, 'Gemini structured output guidance 不应命中 Anthropic 上游')
}

async function assertChatToGeminiNativeSseComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, chatCompletionsRequest(true))
  const text = await response.text()
  assert.equal(response.status, 200, `Chat -> Gemini native SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /chat\.completion\.chunk/, 'Chat -> Gemini native SSE 应返回 Chat chunk')
  assert.match(text, /gemini /, 'Chat -> Gemini native SSE 应透出 Gemini 文本首段')
  assert.match(text, /stream text/, 'Chat -> Gemini native SSE 应透出 Gemini 文本后续段')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, `/v1beta/models/${geminiGenerateContentUpstreamModel}:streamGenerateContent`)
  assert.equal(hit.xGoogApiKey, 'sk-cross-gemini-native-upstream')
  assert.match(hit.bodyText, /inlineData|functionDeclarations|responseMimeType|lookup/, 'Chat schema / 图片 / tool 应转换进 Gemini 上游体')
}

async function assertResponsesToGeminiNativeComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, responsesRequest(false))
  const text = await response.text()
  assert.equal(response.status, 200, `Responses -> Gemini native JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /function_call|gemini-tool|lookup/, 'Responses -> Gemini native 应把 Gemini functionCall 还原为 Responses function_call')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, `/v1beta/models/${geminiGenerateContentUpstreamModel}:generateContent`)
  assert.equal(hit.xGoogApiKey, 'sk-cross-gemini-native-upstream')
  assert.match(hit.bodyText, /inlineData|functionDeclarations|lookup/, 'Responses 复杂输入应转换进 Gemini 上游体')
}

async function assertAnthropicMessagesToGeminiNativeComplex(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/messages', localApiKey, anthropicMessagesRequest(false), {
    'anthropic-version': '2023-06-01'
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Gemini native JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /tool_use|gemini-tool|lookup/, 'Messages -> Gemini native 应把 Gemini functionCall 还原为 Anthropic tool_use')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, `/v1beta/models/${geminiGenerateContentUpstreamModel}:generateContent`)
  assert.equal(hit.xGoogApiKey, 'sk-cross-gemini-native-upstream')
  assert.match(hit.bodyText, /inlineData|functionDeclarations|functionResponse|lookup/, 'Messages 复杂输入应转换进 Gemini 上游体')
}

async function assertResponsesStateToGeminiNativeGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/responses', localApiKey, {
    ...responsesRequest(false),
    previous_response_id: 'resp_cross_stateful',
    context_management: { truncation: 'auto' }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses state -> Gemini guidance 应返回可读响应，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /previous_response_id|context_management|Gemini native/, 'Responses 状态字段不能保真时应返回 guidance')
  assert.equal(upstreamHits.length, start, 'Responses 状态 guidance 不应命中 Gemini 上游')
}

function responsesRequest(stream: boolean, options: { structuredOutput?: boolean } = {}): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: openAIResponsesSourceModel,
    instructions: 'system instructions from responses',
    input: [
      {
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'lookup cross protocol image' },
          { type: 'input_image', image_url: pngDataUrl, detail: 'low' }
        ]
      },
      {
        type: 'function_call',
        call_id: 'call_lookup',
        name: 'lookup',
        arguments: '{"query":"prior"}'
      },
      {
        type: 'function_call_output',
        call_id: 'call_lookup',
        output: '{"result":"from responses tool"}'
      }
    ],
    tools: [lookupResponsesTool()],
    tool_choice: { type: 'function', name: 'lookup' },
    max_output_tokens: 128,
    stream
  }
  if (options.structuredOutput !== false) {
    body.text = {
      format: {
        type: 'json_schema',
        name: 'cross_schema',
        schema: {
          type: 'object',
          properties: { answer: { type: 'string' } },
          required: ['answer'],
          additionalProperties: false
        }
      }
    }
  }
  return body
}

function chatCompletionsRequest(stream: boolean, options: { structuredOutput?: boolean } = {}): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: openAIChatSourceModel,
    messages: [
      { role: 'system', content: 'system instructions from chat' },
      {
        role: 'user',
        content: [
          { type: 'text', text: 'lookup cross protocol image' },
          { type: 'image_url', image_url: { url: pngDataUrl, detail: 'low' } }
        ]
      },
      {
        role: 'assistant',
        content: null,
        tool_calls: [{
          id: 'call_lookup',
          type: 'function',
          function: { name: 'lookup', arguments: '{"query":"prior"}' }
        }]
      },
      { role: 'tool', tool_call_id: 'call_lookup', content: '{"result":"from chat tool"}' }
    ],
    tools: [lookupChatTool()],
    tool_choice: { type: 'function', function: { name: 'lookup' } },
    max_completion_tokens: 128,
    stream
  }
  if (options.structuredOutput !== false) {
    body.response_format = {
      type: 'json_schema',
      json_schema: {
        name: 'cross_schema',
        strict: true,
        schema: {
          type: 'object',
          properties: { answer: { type: 'string' } },
          required: ['answer'],
          additionalProperties: false
        }
      }
    }
  }
  return body
}

function anthropicMessagesRequest(stream: boolean): Record<string, unknown> {
  return {
    model: anthropicMessagesSourceModel,
    system: 'system instructions from messages',
    messages: [
      {
        role: 'user',
        content: [
          { type: 'text', text: 'lookup cross protocol image' },
          { type: 'image', source: { type: 'base64', media_type: 'image/png', data: png1x1Base64 } }
        ]
      },
      {
        role: 'assistant',
        content: [
          { type: 'text', text: 'prior tool call' },
          { type: 'tool_use', id: 'call_lookup', name: 'lookup', input: { query: 'prior' } }
        ]
      },
      {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'call_lookup', content: '{"result":"from messages tool"}' }
        ]
      }
    ],
    tools: [lookupAnthropicTool()],
    tool_choice: { type: 'tool', name: 'lookup' },
    max_tokens: 128,
    stream
  }
}

function geminiGenerateContentRequest(options: { structuredOutput?: boolean } = {}): Record<string, unknown> {
  const body: Record<string, unknown> = {
    contents: [
      {
        role: 'user',
        parts: [
          { text: 'lookup cross protocol image' },
          { inlineData: { mimeType: 'image/png', data: png1x1Base64 } }
        ]
      },
      {
        role: 'model',
        parts: [
          { functionCall: { name: 'lookup', args: { query: 'prior' } } }
        ]
      },
      {
        role: 'user',
        parts: [
          { functionResponse: { name: 'lookup', response: { result: 'from gemini tool' } } }
        ]
      }
    ],
    systemInstruction: {
      parts: [{ text: 'system instructions from gemini' }]
    },
    tools: [{
      functionDeclarations: [{
        name: 'lookup',
        description: 'lookup cross data',
        parameters: {
          type: 'object',
          properties: { query: { type: 'string' } },
          required: ['query']
        }
      }]
    }],
    toolConfig: {
      functionCallingConfig: {
        mode: 'ANY',
        allowedFunctionNames: ['lookup']
      }
    },
    generationConfig: {
      maxOutputTokens: 128
    }
  }
  if (options.structuredOutput !== false) {
    body.generationConfig = {
      ...(body.generationConfig as Record<string, unknown>),
      responseMimeType: 'application/json',
      responseSchema: {
        type: 'object',
        properties: { answer: { type: 'string' } },
        required: ['answer']
      }
    }
  }
  return body
}

function lookupChatTool(): Record<string, unknown> {
  return {
    type: 'function',
    function: {
      name: 'lookup',
      description: 'lookup cross data',
      parameters: {
        type: 'object',
        properties: { query: { type: 'string' } },
        required: ['query']
      }
    }
  }
}

function lookupResponsesTool(): Record<string, unknown> {
  return {
    type: 'function',
    name: 'lookup',
    description: 'lookup cross data',
    parameters: {
      type: 'object',
      properties: { query: { type: 'string' } },
      required: ['query']
    }
  }
}

function lookupAnthropicTool(): Record<string, unknown> {
  return {
    name: 'lookup',
    description: 'lookup cross data',
    input_schema: {
      type: 'object',
      properties: { query: { type: 'string' } },
      required: ['query']
    }
  }
}

function createCrossProtocolMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const url = new URL(req.url ?? '/', 'http://127.0.0.1')
      const body = parseJsonObject(bodyText)
      upstreamHits.push({
        method: req.method ?? '',
        rawUrl: req.url ?? '',
        path: url.pathname,
        authorization: headerText(req, 'authorization'),
        xApiKey: headerText(req, 'x-api-key'),
        xGoogApiKey: headerText(req, 'x-goog-api-key'),
        anthropicVersion: headerText(req, 'anthropic-version'),
        contentType: headerText(req, 'content-type'),
        accept: headerText(req, 'accept'),
        bodyText,
        body
      })

      if (req.method === 'POST' && url.pathname.endsWith('/v1/chat/completions')) {
        respondChatCompletions(res, body)
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/messages') {
        respondAnthropicMessages(res, body)
        return
      }
      if (req.method === 'POST' && url.pathname === `/v1beta/models/${geminiGenerateContentUpstreamModel}:generateContent`) {
        respondGeminiGenerateContent(res, body)
        return
      }
      if (req.method === 'POST' && url.pathname === `/v1beta/models/${geminiGenerateContentUpstreamModel}:streamGenerateContent`) {
        respondGeminiStreamGenerateContent(res)
        return
      }
      sendJson(res, 404, { error: { message: `unexpected mock path ${url.pathname}` } })
    })
  })
}

function respondChatCompletions(res: http.ServerResponse, body: Record<string, unknown>): void {
  if (body.stream === true) {
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    res.write(`data: ${JSON.stringify({
      id: 'chatcmpl-cross',
      object: 'chat.completion.chunk',
      created: 0,
      model: body.model ?? openAIChatUpstreamModel,
      choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }]
    })}\n\n`)
    res.write(`data: ${JSON.stringify({
      id: 'chatcmpl-cross',
      object: 'chat.completion.chunk',
      created: 0,
      model: body.model ?? openAIChatUpstreamModel,
      choices: [{ index: 0, delta: { content: 'chat stream text' }, finish_reason: null }]
    })}\n\n`)
    res.write(`data: ${JSON.stringify({
      id: 'chatcmpl-cross',
      object: 'chat.completion.chunk',
      created: 0,
      model: body.model ?? openAIChatUpstreamModel,
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            id: 'call_chat_stream_tool',
            type: 'function',
            function: { name: 'lookup', arguments: '' }
          }]
        },
        finish_reason: null
      }]
    })}\n\n`)
    res.write(`data: ${JSON.stringify({
      id: 'chatcmpl-cross',
      object: 'chat.completion.chunk',
      created: 0,
      model: body.model ?? openAIChatUpstreamModel,
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            function: { arguments: '{"query":"chat-tool"}' }
          }]
        },
        finish_reason: 'tool_calls'
      }]
    })}\n\n`)
    res.end('data: [DONE]\n\n')
    return
  }
  sendJson(res, 200, {
    id: 'chatcmpl-cross',
    object: 'chat.completion',
    created: 0,
    model: body.model ?? openAIChatUpstreamModel,
    choices: [{
      index: 0,
      message: {
        role: 'assistant',
        content: null,
        tool_calls: [{
          id: 'call_chat_tool',
          type: 'function',
          function: { name: 'lookup', arguments: '{"query":"chat-tool"}' }
        }]
      },
      finish_reason: 'tool_calls'
    }],
    usage: { prompt_tokens: 17, completion_tokens: 5, total_tokens: 22 }
  })
}

function respondAnthropicMessages(res: http.ServerResponse, body: Record<string, unknown>): void {
  sendJson(res, 200, {
    id: 'msg-cross',
    type: 'message',
    role: 'assistant',
    model: body.model ?? anthropicMessagesUpstreamModel,
    content: [
      { type: 'text', text: 'anthropic text' },
      { type: 'tool_use', id: 'toolu_cross', name: 'lookup', input: { query: 'anthropic-tool' } }
    ],
    stop_reason: 'tool_use',
    stop_sequence: null,
    usage: { input_tokens: 19, output_tokens: 7 }
  })
}

function respondGeminiGenerateContent(res: http.ServerResponse, body: Record<string, unknown>): void {
  sendJson(res, 200, {
    candidates: [{
      content: {
        role: 'model',
        parts: [
          { text: 'gemini text' },
          { functionCall: { name: 'lookup', args: { query: 'gemini-tool' } } }
        ]
      },
      finishReason: Array.isArray(body.tools) ? 'STOP' : 'STOP'
    }],
    usageMetadata: {
      promptTokenCount: 23,
      candidatesTokenCount: 8,
      totalTokenCount: 31
    }
  })
}

function respondGeminiStreamGenerateContent(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write('data: {"candidates":[{"content":{"role":"model","parts":[{"text":"gemini "}]}}]}\n\n')
  res.write('data: {"candidates":[{"content":{"role":"model","parts":[{"text":"stream text"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}}\n\n')
  res.end()
}

async function gatewayFetch(
  baseUrl: string,
  path: string,
  apiKey: string,
  body: Record<string, unknown>,
  extraHeaders: Record<string, string> = {}
): Promise<Response> {
  return await fetch(new URL(path, baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      ...extraHeaders
    },
    body: JSON.stringify(body)
  })
}

function onlyNewHit(start: number): UpstreamHit {
  assert.equal(upstreamHits.length, start + 1, `预期只新增 1 次上游命中，实际新增 ${upstreamHits.length - start}`)
  return upstreamHits[start]!
}

function assertUsageRecords(): void {
  const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 200 }).items
  assert(records.length >= 8, '协议交叉矩阵至少应写入多条 usage record')
  assert(records.some((item) => item.model === openAIResponsesSourceModel && item.upstreamModel === openAIChatUpstreamModel), 'usage 应记录 Responses -> Chat 上游模型')
  assert(records.some((item) => item.model === anthropicMessagesSourceModel && item.upstreamModel === geminiGenerateContentUpstreamModel), 'usage 应记录 Messages -> Gemini 上游模型')
  assert(records.some((item) => item.model === geminiGenerateContentSourceModel && item.upstreamModel === anthropicMessagesUpstreamModel), 'usage 应记录 Gemini -> Messages 上游模型')
}

function sendJson(res: http.ServerResponse, statusCode: number, body: unknown): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(body))
}

function parseJsonObject(text: string): Record<string, unknown> {
  if (!text) return {}
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function headerText(req: http.IncomingMessage, name: string): string {
  const value = req.headers[name.toLowerCase()]
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' ? value : ''
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
