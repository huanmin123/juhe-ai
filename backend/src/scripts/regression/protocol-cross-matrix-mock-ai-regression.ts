import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

interface UpstreamHit {
  method: string
  rawUrl: string
  path: string
  authorization: string
  xApiKey: string
  xGoogApiKey: string
  bodyText: string
  body: Record<string, unknown>
}

interface CrossRuntime {
  apiKey: string
  groupId: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-protocol-cross-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
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

const openAIChatSourceModel = 'cross-gpt-chat-source'
const openAIResponsesSourceModel = 'cross-gpt-responses-source'
const anthropicMessagesSourceModel = 'cross-claude-messages-source'
const geminiGenerateContentSourceModel = 'cross-gemini-native-source'
const openAIChatUpstreamModel = 'cross-openai-chat-upstream'

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

    assertAccountModelMappingsRejectCrossProtocol(upstreamOrigin)
    const chatRuntime = createOpenAIChatRuntime(upstreamOrigin)
    const messagesRuntime = createAnthropicMessagesRuntime(upstreamOrigin)
    const geminiRuntime = createGeminiNativeRuntime(upstreamOrigin)
    const hybridOpenAIChatRuntime = createHybridOpenAIChatRuntime(upstreamOrigin)
    const hybridOpenAIChatFailoverRuntime = createHybridOpenAIChatFailoverRuntime(upstreamOrigin)
    assertExplicitHybridRulesRejected(chatRuntime.groupId)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertOpenAIChatNativePasses(baseUrl, chatRuntime.apiKey)
    await assertResponsesToOpenAIChatRejected(baseUrl, chatRuntime.apiKey)
    await assertAnthropicMessagesToOpenAIChatRejected(baseUrl, chatRuntime.apiKey)
    await assertGeminiNativeToOpenAIChatRejected(baseUrl, chatRuntime.apiKey)
    await assertOpenAIChatToAnthropicMessagesRejected(baseUrl, messagesRuntime.apiKey)
    await assertOpenAIChatToGeminiNativeRejected(baseUrl, geminiRuntime.apiKey)
    await assertAnthropicMessagesToHybridOpenAIChatPasses(baseUrl, hybridOpenAIChatRuntime.apiKey)
    await assertHybridOpenAIChatFailoverPreservesBridge(baseUrl, hybridOpenAIChatFailoverRuntime.apiKey)

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()

    console.log('protocol cross matrix mock ai boundary regression passed')
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
  for (const providerCode of [GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE]) {
    saveCustomProviderModel({
      providerCode,
      model: openAIChatSourceModel,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 2,
      actorSystemAccountId: access.systemAccountId
    })
  }
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: openAIResponsesSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicMessagesSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiGenerateContentSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: openAIChatUpstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: access.systemAccountId
  })
}

function assertAccountModelMappingsRejectCrossProtocol(upstreamOrigin: string): void {
  const group = repositories.createGroup({
    name: '协议交叉矩阵模型映射拒绝分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  assert.throws(() => repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '协议交叉矩阵错误跨协议映射账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-invalid-mapping',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    supportedModels: [openAIChatUpstreamModel],
    modelMappings: [{
      sourceModel: openAIResponsesSourceModel,
      sourceEndpointFamily: 'responses',
      upstreamModel: openAIChatUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    status: 'active',
    schedulable: true
  }, access), /账号模型别名只支持同协议映射|请改用混合供应商账户/)

  const hybridGroup = repositories.createGroup({
    name: '协议交叉矩阵混合供应商 Responses 上游拒绝分组',
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    enabled: true
  }, access)
  assert.throws(() => repositories.createAccount({
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    name: '协议交叉矩阵混合供应商错误 Responses 上游账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-hybrid-invalid-responses',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json']
    },
    groupId: hybridGroup.id,
    supportedModels: [openAIChatUpstreamModel],
    modelMappings: [{
      sourceModel: openAIResponsesSourceModel,
      sourceEndpointFamily: 'responses',
      upstreamModel: openAIChatUpstreamModel,
      upstreamEndpointFamily: 'responses',
      enabled: true
    }],
    status: 'active',
    schedulable: true
  }, access), /混合供应商账户暂不支持 Responses 到 Responses/)
}

function createOpenAIChatRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 OpenAI Chat 分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '协议交叉矩阵 OpenAI Chat 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-openai-chat-upstream',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    supportedModels: [openAIChatUpstreamModel],
    modelMappings: [{
      sourceModel: openAIChatSourceModel,
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: openAIChatUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    status: 'active',
    schedulable: true
  }, access)
  return createRuntime(group.id, '协议交叉矩阵 OpenAI Chat Key')
}

function createAnthropicMessagesRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 Anthropic Messages 分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: '协议交叉矩阵 Anthropic Messages 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-anthropic-messages-upstream',
      base_url: upstreamOrigin,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    groupId: group.id,
    supportedModels: [anthropicMessagesSourceModel],
    status: 'active',
    schedulable: true
  }, access)
  return createRuntime(group.id, '协议交叉矩阵 Anthropic Messages Key')
}

function createGeminiNativeRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵 Gemini native 分组',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: '协议交叉矩阵 Gemini native 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-gemini-native-upstream',
      base_url: upstreamOrigin,
      supported_endpoint_modes: ['generate_content_json', 'generate_content_sse']
    },
    groupId: group.id,
    supportedModels: [geminiGenerateContentSourceModel],
    status: 'active',
    schedulable: true
  }, access)
  return createRuntime(group.id, '协议交叉矩阵 Gemini native Key')
}

function createHybridOpenAIChatRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵混合供应商 OpenAI Chat 分组',
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    name: '协议交叉矩阵混合供应商 OpenAI Chat 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cross-hybrid-openai-chat-upstream',
      base_url: `${upstreamOrigin}/openai-compatible`,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    supportedModels: [openAIChatUpstreamModel],
    modelMappings: [{
      sourceModel: anthropicMessagesSourceModel,
      sourceEndpointFamily: 'messages',
      upstreamModel: openAIChatUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    status: 'active',
    schedulable: true
  }, access)
  return createRuntime(group.id, '协议交叉矩阵混合供应商 OpenAI Chat Key')
}

function createHybridOpenAIChatFailoverRuntime(upstreamOrigin: string): CrossRuntime {
  const group = repositories.createGroup({
    name: '协议交叉矩阵混合供应商 OpenAI Chat 切号分组',
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    enabled: true
  }, access)
  for (const account of [
    { name: '协议交叉矩阵混合供应商故障账号', apiKey: 'sk-cross-hybrid-openai-chat-fail', priority: 0 },
    { name: '协议交叉矩阵混合供应商备用账号', apiKey: 'sk-cross-hybrid-openai-chat-good', priority: 1 }
  ]) {
    repositories.createAccount({
      providerCode: HYBRID_PROVIDER_CODE,
      providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
      name: account.name,
      type: 'api_key',
      credentials: {
        api_key: account.apiKey,
        base_url: `${upstreamOrigin}/openai-compatible`,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      supportedModels: [openAIChatUpstreamModel],
      modelMappings: [{
        sourceModel: anthropicMessagesSourceModel,
        sourceEndpointFamily: 'messages',
        upstreamModel: openAIChatUpstreamModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }],
      priority: account.priority,
      status: 'active',
      schedulable: true
    }, access)
  }
  return createRuntime(group.id, '协议交叉矩阵混合供应商 OpenAI Chat 切号 Key')
}

function createRuntime(groupId: string, name: string): CrossRuntime {
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${name} 未返回明文密钥`)
  return { apiKey: apiKey.key, groupId }
}

function assertExplicitHybridRulesRejected(groupId: string): void {
  assert.throws(() => {
    createApiKeyRecordWithRouteStrategy(repositories, {
      name: '协议交叉矩阵旧显式混合规则 Key',
      groupBindings: [{ groupId, priority: 1, status: 'active' }],
      explicitHybridRouteRules: [{
        id: 'legacy_explicit_hybrid_boundary',
        enabled: true,
        priority: 1,
        sourceClientProfile: 'auto',
        sourceEndpointFamily: 'responses',
        sourceModel: openAIResponsesSourceModel,
        targetGroupId: groupId,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: openAIChatUpstreamModel,
        adapterMode: 'bridge'
      }],
      status: 'active'
    }, access)
  }, /explicitHybridRouteRules/, 'API Key 不应继续接收显式混合跨协议规则')
}

async function assertOpenAIChatNativePasses(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/chat/completions', localApiKey, {
    model: openAIChatSourceModel,
    messages: [{ role: 'user', content: 'ping' }],
    stream: false
  })
  const text = await response.text()
  assert.equal(response.status, 200, `OpenAI-compatible 同协议 Chat 请求应成功，实际 HTTP ${response.status}: ${text}`)
  const hit = onlyNewHit(start)
  assert.equal(hit.path, '/openai-compatible/v1/chat/completions')
  assert.equal(hit.authorization, 'Bearer sk-cross-openai-chat-upstream')
  assert.equal(hit.body.model, openAIChatUpstreamModel, '同协议模型映射应改写为当前账号上游模型')
}

async function assertResponsesToOpenAIChatRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertCrossProtocolRejected('Responses -> OpenAI Chat', baseUrl, '/v1/responses', localApiKey, {
    model: openAIResponsesSourceModel,
    input: 'ping',
    stream: false
  })
}

async function assertAnthropicMessagesToOpenAIChatRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertCrossProtocolRejected('Anthropic Messages -> OpenAI Chat', baseUrl, '/v1/messages', localApiKey, {
    model: anthropicMessagesSourceModel,
    max_tokens: 16,
    messages: [{ role: 'user', content: 'ping' }]
  }, { 'anthropic-version': '2023-06-01' })
}

async function assertGeminiNativeToOpenAIChatRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertCrossProtocolRejected(
    'Gemini native -> OpenAI Chat',
    baseUrl,
    `/v1beta/models/${geminiGenerateContentSourceModel}:generateContent`,
    localApiKey,
    { contents: [{ role: 'user', parts: [{ text: 'ping' }] }] }
  )
}

async function assertOpenAIChatToAnthropicMessagesRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertCrossProtocolRejected('OpenAI Chat -> Anthropic Messages', baseUrl, '/v1/chat/completions', localApiKey, {
    model: openAIChatSourceModel,
    messages: [{ role: 'user', content: 'ping' }],
    stream: false
  })
}

async function assertOpenAIChatToGeminiNativeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertCrossProtocolRejected('OpenAI Chat -> Gemini native', baseUrl, '/v1/chat/completions', localApiKey, {
    model: openAIChatSourceModel,
    messages: [{ role: 'user', content: 'ping' }],
    stream: false
  })
}

async function assertAnthropicMessagesToHybridOpenAIChatPasses(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/messages', localApiKey, {
    model: anthropicMessagesSourceModel,
    max_tokens: 16,
    messages: [{ role: 'user', content: 'ping' }]
  }, { 'anthropic-version': '2023-06-01' })
  const text = await response.text()
  assert.equal(response.status, 200, `混合供应商 Messages -> OpenAI Chat 请求应成功，实际 HTTP ${response.status}: ${text}`)
  const parsed = safeJson(text)
  assert.equal(parsed.type, 'message', '混合供应商桥接响应应渲染为 Anthropic message')
  assert.equal(Array.isArray(parsed.content), true, '混合供应商桥接响应应包含 Anthropic content 数组')
  const firstContent = (parsed.content as Array<Record<string, unknown>>)[0]
  assert.equal(firstContent?.type, 'text', '混合供应商桥接响应正文应为 Anthropic text block')
  const hit = onlyNewHit(start)
  assert.equal(hit.path, '/openai-compatible/v1/chat/completions')
  assert.equal(hit.authorization, 'Bearer sk-cross-hybrid-openai-chat-upstream')
  assert.equal(hit.xApiKey, '', 'OpenAI Chat 上游不应透传 Anthropic x-api-key')
  assert.equal(hit.body.model, openAIChatUpstreamModel, '混合供应商模型映射应改写为真实上游模型')
  assert(Array.isArray(hit.body.messages), '混合供应商应把 Anthropic Messages 请求转换为 OpenAI Chat messages')
}

async function assertHybridOpenAIChatFailoverPreservesBridge(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, '/v1/messages', localApiKey, {
    model: anthropicMessagesSourceModel,
    max_tokens: 16,
    messages: [{ role: 'user', content: 'failover ping' }]
  }, { 'anthropic-version': '2023-06-01' })
  const text = await response.text()
  assert.equal(response.status, 200, `混合供应商首个账号失败后应切号成功，实际 HTTP ${response.status}: ${text}`)
  const hits = upstreamHits.slice(start)
  assert(hits.length >= 2, `混合供应商切号请求应至少命中故障账号和备用账号，实际 ${hits.length} 次`)
  assert(hits.some((hit) => hit.authorization === 'Bearer sk-cross-hybrid-openai-chat-fail'), '混合供应商切号请求应先经历故障账号失败')
  const successfulHit = lastHitWithAuthorization(hits, 'Bearer sk-cross-hybrid-openai-chat-good')
  assert(successfulHit, '混合供应商切号请求应最终命中备用账号')
  assert.equal(successfulHit.path, '/openai-compatible/v1/chat/completions')
  assert.equal(successfulHit.body.model, openAIChatUpstreamModel, '切号后仍应保留混合供应商上游模型映射')
  assert(Array.isArray(successfulHit.body.messages), '切号后仍应保留 Anthropic Messages -> OpenAI Chat 请求体转换')
}

function lastHitWithAuthorization(hits: UpstreamHit[], authorization: string): UpstreamHit | undefined {
  for (let index = hits.length - 1; index >= 0; index -= 1) {
    const hit = hits[index]
    if (hit?.authorization === authorization) return hit
  }
  return undefined
}

async function assertCrossProtocolRejected(
  label: string,
  baseUrl: string,
  path: string,
  localApiKey: string,
  body: Record<string, unknown>,
  extraHeaders: Record<string, string> = {}
): Promise<void> {
  const start = upstreamHits.length
  const response = await gatewayFetch(baseUrl, path, localApiKey, body, extraHeaders)
  const text = await response.text()
  assert.notEqual(response.status, 200, `${label} 普通供应商跨协议请求不应成功: ${text}`)
  assert.equal(upstreamHits.length, start, `${label} 普通供应商跨协议请求不应命中上游`)
}

async function gatewayFetch(
  baseUrl: string,
  path: string,
  localApiKey: string,
  body: Record<string, unknown>,
  extraHeaders: Record<string, string> = {}
): Promise<Response> {
  return fetch(new URL(path, baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      ...extraHeaders
    },
    body: JSON.stringify(body)
  })
}

function onlyNewHit(start: number): UpstreamHit {
  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, `预期只命中一次上游，实际 ${hits.length}`)
  const hit = hits[0]
  assert(hit, '缺少上游命中记录')
  return hit
}

function createCrossProtocolMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      upstreamHits.push({
        method: req.method ?? '',
        rawUrl: req.url ?? '',
        path: req.url?.split('?', 1)[0] ?? '',
        authorization: String(req.headers.authorization ?? ''),
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        xGoogApiKey: String(req.headers['x-goog-api-key'] ?? ''),
        bodyText,
        body: safeJson(bodyText)
      })
      if (String(req.headers.authorization ?? '') === 'Bearer sk-cross-hybrid-openai-chat-fail') {
        res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'forced hybrid account failure' } }))
        return
      }
      if (req.url?.split('?', 1)[0] === '/openai-compatible/v1/chat/completions') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'chatcmpl-cross-boundary',
          object: 'chat.completion',
          choices: [{
            index: 0,
            message: { role: 'assistant', content: 'cross chat ok' },
            finish_reason: 'stop'
          }],
          usage: { prompt_tokens: 3, completion_tokens: 3, total_tokens: 6 }
        }))
        return
      }
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { message: 'unexpected upstream path' } }))
    })
  })
}

function safeJson(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address === 'object')
  return { port: address.port }
}
