import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { inspectGeminiStreamText } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import {
  extractGeminiJsonSemanticFrames,
  extractGeminiSseSemanticFrames,
  geminiResponseEndpointFamilyFromPath
} from '../../modules/gateway/protocols/gemini-v1beta/response-semantics.js'
import { parseGeminiUsageFromJsonBuffer } from '../../modules/gateway/protocols/gemini-v1beta/usage.js'
import { parseGeminiErrorPayload } from '../../modules/gateway/protocols/gemini-v1beta/error-payload.js'
import { parseOpenAISseEventText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'
import { listResponseInspectionPolicyDefaultRules } from '../../storage/response-inspection-policy.repository.js'
import { logger } from '../../shared/logger.js'

interface GeminiUpstreamHit {
  rawUrl: string
  path: string
  method: string
  authorization: string
  xGoogApiKey: string
  xApiKey: string
  accept: string
  bodyText: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-gemini-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gemini-gateway-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
// crypto.ts may already be loaded by static imports, so keep the captured runtime secret for this fixture.
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
  modelCatalog,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue,
  providerDriverRegistry,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/providers/drivers/registry.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: GeminiUpstreamHit[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1beta', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createGeminiMockUpstream()
    await listen(upstreamServer)
    const upstreamOrigin = `http://127.0.0.1:${serverAddress(upstreamServer).port}`

    assertGeminiSeeds()

    const group = repositories.createGroup({
      name: 'Gemini Mock AI 回归分组',
      providerCode: GEMINI_PROVIDER_CODE,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      name: 'Gemini Mock AI 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-gemini-upstream',
        base_url: upstreamOrigin
      },
      groupId: group.id,
      priority: 0,
      status: 'active',
      schedulable: true
    }, access)
    activateFixtureAccount(account.id)
    const fallbackAccount = repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      name: 'Gemini Mock AI 回归备用账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-gemini-upstream-fallback',
        base_url: upstreamOrigin
      },
      groupId: group.id,
      priority: 10,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(account.providerCode, GEMINI_PROVIDER_CODE)
    assert.equal(account.providerProtocolProfileId, GEMINI_NATIVE_V1BETA_PROFILE_ID)
    assert.deepEqual(account.credentials.supported_endpoint_modes, ['generate_content_json', 'generate_content_sse', 'count_tokens'])
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini Mock AI 回归 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    const openAIChatGroup = repositories.createGroup({
      name: 'Gemini OpenAI Chat Mock AI 回归分组',
      providerCode: GEMINI_PROVIDER_CODE,
      enabled: true
    }, access)
    const openAIChatAccount = repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      name: 'Gemini OpenAI Chat Mock AI 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-gemini-openai-upstream',
        base_url: `${upstreamOrigin}/v1beta/openai`
      },
      groupId: openAIChatGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(openAIChatAccount.providerProtocolProfileId, GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
    assert.deepEqual(openAIChatAccount.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse'])
    assert.throws(
      () => repositories.createAccount({
        providerCode: GEMINI_PROVIDER_CODE,
        providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
        name: 'Gemini OpenAI Chat 禁止 Messages 映射账户',
        type: 'api_key',
        credentials: {
          api_key: 'sk-gemini-openai-invalid-mapping',
          base_url: `${upstreamOrigin}/v1beta/openai`
        },
        groupId: openAIChatGroup.id,
        modelMappings: [
          {
            sourceModel: 'claude-haiku-4-5',
            sourceEndpointFamily: 'messages',
            upstreamModel: 'gemini-3.5-flash',
            upstreamEndpointFamily: 'chat_completions',
            enabled: true
          }
        ],
        status: 'active',
        schedulable: true
      }, access),
      /Gemini OpenAI Chat 档案的账号模型别名只能使用 Chat Completions|账号模型别名只支持同协议映射|请改用混合供应商账户/,
      'Gemini OpenAI Chat 账号别名不应承接 Anthropic Messages 来源映射'
    )
    const openAIChatApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini OpenAI Chat Mock AI 回归 Key',
      groupBindings: [{ groupId: openAIChatGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(openAIChatApiKey.key, 'Gemini OpenAI Chat 回归 API Key 未返回明文密钥')

    const openAIChatRootGroup = repositories.createGroup({
      name: 'Gemini OpenAI Chat NewAPI 根地址回归分组',
      providerCode: GEMINI_PROVIDER_CODE,
      enabled: true
    }, access)
    const openAIChatRootAccount = repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      name: 'Gemini OpenAI Chat NewAPI 根地址回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-gemini-openai-root-upstream',
        base_url: upstreamOrigin
      },
      groupId: openAIChatRootGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const openAIChatRootApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini OpenAI Chat NewAPI 根地址回归 Key',
      groupBindings: [{ groupId: openAIChatRootGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(openAIChatRootApiKey.key, 'Gemini OpenAI Chat 根地址回归 API Key 未返回明文密钥')

    const glmBridgeGroup = repositories.createGroup({
      name: 'Gemini Native 转 GLM Chat 回归分组',
      providerCode: GLM_PROVIDER_CODE,
      enabled: true
    }, access)
    const glmBridgeAccount = repositories.createAccount({
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      name: 'Gemini Native 转 GLM Chat 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-glm-upstream',
        base_url: upstreamOrigin
      },
      groupId: glmBridgeGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    assert.throws(
      () => repositories.createAccount({
        providerCode: GLM_PROVIDER_CODE,
        providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
        name: 'Gemini Native 转 GLM Chat 非法映射账户',
        type: 'api_key',
        credentials: {
          api_key: 'sk-glm-invalid-upstream',
          base_url: upstreamOrigin
        },
        groupId: glmBridgeGroup.id,
        modelMappings: [
          {
            sourceModel: 'gemini-3.5-flash',
            sourceEndpointFamily: 'generate_content',
            upstreamModel: 'glm-5.2',
            upstreamEndpointFamily: 'responses',
            enabled: true
          }
        ],
        status: 'active',
        schedulable: true
      }, access),
      /Gemini OpenAI Chat 档案的账号模型别名只能使用 Chat Completions|账号模型别名只支持同协议映射|请改用混合供应商账户/,
      '账号模型别名不应允许 Gemini GenerateContent 跨协议桥接'
    )
    const glmBridgeRules = [
      explicitHybridRule({
        id: 'gemini_generate_to_glm_chat',
        sourceEndpointFamily: 'generate_content',
        sourceModel: 'gemini-3.5-flash',
        targetGroupId: glmBridgeGroup.id,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: 'glm-5.2'
      }),
      explicitHybridRule({
        id: 'gemini_stream_generate_to_glm_chat',
        sourceEndpointFamily: 'stream_generate_content',
        sourceModel: 'gemini-3.5-flash',
        targetGroupId: glmBridgeGroup.id,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: 'glm-5.2'
      })
    ]
    const glmBridgeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini Native 转 GLM Chat 回归 Key',
      groupBindings: [{ groupId: glmBridgeGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(glmBridgeApiKey.key, 'Gemini Native 转 GLM Chat 回归 API Key 未返回明文密钥')

    const anthropicBridgeGroup = repositories.createGroup({
      name: 'Gemini Native 转 Anthropic Messages 回归分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      enabled: true
    }, access)
    const anthropicBridgeAccount = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: 'Gemini Native 转 Anthropic Messages 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-anthropic-upstream',
        base_url: upstreamOrigin,
        supported_endpoint_modes: ['messages_json', 'messages_sse']
      },
      groupId: anthropicBridgeGroup.id,
      supportedModels: ['claude-haiku-4-5'],
      healthCheckModel: 'claude-haiku-4-5',
      healthCheckEndpointMode: 'messages_json',
      status: 'active',
      schedulable: true
    }, access)
    activateFixtureAccount(glmBridgeAccount.id)
    activateFixtureAccount(openAIChatRootAccount.id)
    activateFixtureAccount(openAIChatAccount.id)
    activateFixtureAccount(fallbackAccount.id)
    activateFixtureAccount(anthropicBridgeAccount.id)
    const anthropicBridgeRules = [
      explicitHybridRule({
        id: 'gemini_generate_to_anthropic_messages',
        sourceEndpointFamily: 'generate_content',
        sourceModel: 'gemini-3.5-flash',
        targetGroupId: anthropicBridgeGroup.id,
        upstreamEndpointFamily: 'messages',
        upstreamModel: 'claude-haiku-4-5'
      }),
      explicitHybridRule({
        id: 'gemini_stream_generate_to_anthropic_messages',
        sourceEndpointFamily: 'stream_generate_content',
        sourceModel: 'gemini-3.5-flash',
        targetGroupId: anthropicBridgeGroup.id,
        upstreamEndpointFamily: 'messages',
        upstreamModel: 'claude-haiku-4-5'
      })
    ]
    const anthropicBridgeRuntimeAccounts = repositories.listOpenAIAccountsForGroup(anthropicBridgeGroup.id, access.systemAccountId)
      .map((item) => item.id === anthropicBridgeAccount.id
        ? { ...item, modelMappings: runtimeMappingsFromExplicitRules(anthropicBridgeRules) }
        : item)
    assert(
      anthropicBridgeRuntimeAccounts.some((item) => item.modelMappings?.some((mapping) => (
        mapping.sourceModel === 'gemini-3.5-flash'
        && mapping.sourceEndpointFamily === 'generate_content'
        && mapping.upstreamEndpointFamily === 'messages'
      ))),
      '模型感知候选窗口必须能按 Gemini GenerateContent source 映射找到 Anthropic Messages 账号'
    )
    const anthropicBridgeRuntimeAccount = anthropicBridgeRuntimeAccounts[0]
    assert(anthropicBridgeRuntimeAccount, 'Gemini Native 转 Anthropic Messages 回归账号必须进入运行时账号窗口')
    const anthropicBridgeGatewayRequest = fakeGatewayPostRequest('/v1beta/models/gemini-3.5-flash:generateContent?trace=gemini-native-to-anthropic-messages-json')
    assert.deepEqual(
      providerDriverRegistry.buildGatewayUpstreamUrlsForAccount(anthropicBridgeRuntimeAccount, anthropicBridgeGatewayRequest),
      [],
      'Anthropic 普通账号不应再通过旧显式混合映射承接 Gemini GenerateContent 请求'
    )
    assert.equal(
      providerDriverRegistry.accountSupportsGatewayRequest(anthropicBridgeGatewayRequest, anthropicBridgeRuntimeAccount),
      false,
      'Anthropic 普通账号 capability check 不应允许 Gemini GenerateContent -> Messages 旧显式映射请求'
    )
    const anthropicBridgeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini Native 转 Anthropic Messages 回归 Key',
      groupBindings: [{ groupId: anthropicBridgeGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(anthropicBridgeApiKey.key, 'Gemini Native 转 Anthropic Messages 回归 API Key 未返回明文密钥')

    const geminiNativeTargetBridgeGroup = repositories.createGroup({
      name: 'OpenAI Anthropic 转 Gemini Native 回归分组',
      providerCode: GEMINI_PROVIDER_CODE,
      enabled: true
    }, access)
    const geminiNativeTargetBridgeAccount = repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      name: 'OpenAI Anthropic 转 Gemini Native 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-gemini-target-upstream',
        base_url: upstreamOrigin,
        supported_endpoint_modes: ['generate_content_json', 'generate_content_sse']
      },
      groupId: geminiNativeTargetBridgeGroup.id,
      supportedModels: ['gemini-3.5-flash'],
      status: 'active',
      schedulable: true
    }, access)
    activateFixtureAccount(geminiNativeTargetBridgeAccount.id)
    const geminiNativeTargetBridgeRules = [
      explicitHybridRule({
        id: 'openai_chat_to_gemini_generate',
        sourceEndpointFamily: 'chat_completions',
        sourceModel: 'gpt-5.5',
        targetGroupId: geminiNativeTargetBridgeGroup.id,
        upstreamEndpointFamily: 'generate_content',
        upstreamModel: 'gemini-3.5-flash'
      }),
      explicitHybridRule({
        id: 'openai_responses_to_gemini_generate',
        sourceEndpointFamily: 'responses',
        sourceModel: 'gpt-5.5',
        targetGroupId: geminiNativeTargetBridgeGroup.id,
        upstreamEndpointFamily: 'generate_content',
        upstreamModel: 'gemini-3.5-flash'
      }),
      explicitHybridRule({
        id: 'anthropic_messages_to_gemini_generate',
        sourceEndpointFamily: 'messages',
        sourceModel: 'claude-haiku-4-5',
        targetGroupId: geminiNativeTargetBridgeGroup.id,
        upstreamEndpointFamily: 'generate_content',
        upstreamModel: 'gemini-3.5-flash'
      })
    ]
    const geminiNativeTargetRuntimeAccount = repositories.listOpenAIAccountsForGroup(geminiNativeTargetBridgeGroup.id, access.systemAccountId)
      .map((item) => item.id === geminiNativeTargetBridgeAccount.id
        ? { ...item, modelMappings: runtimeMappingsFromExplicitRules(geminiNativeTargetBridgeRules) }
        : item)
      .find((item) => item.id === geminiNativeTargetBridgeAccount.id)
    assert(geminiNativeTargetRuntimeAccount, 'OpenAI Chat -> Gemini native 回归账号必须进入运行时账号窗口')
    const geminiNativeTargetRuntimeRequest = fakeGatewayPostRequest('/v1/chat/completions')
    geminiNativeTargetRuntimeRequest.body = { model: 'gpt-5.5', messages: [{ role: 'user', content: 'ping' }] }
    assert.deepEqual(
      providerDriverRegistry.buildGatewayUpstreamUrlsForAccount(geminiNativeTargetRuntimeAccount, geminiNativeTargetRuntimeRequest),
      [],
      'Gemini 普通账号不应再通过旧显式混合映射承接 OpenAI Chat 请求'
    )
    assert.equal(
      providerDriverRegistry.accountSupportsGatewayRequest(geminiNativeTargetRuntimeRequest, geminiNativeTargetRuntimeAccount),
      false,
      'Gemini 普通账号 capability check 不应允许 OpenAI Chat -> Gemini native 旧显式映射请求'
    )
    const geminiNativeTargetBridgeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'OpenAI Anthropic 转 Gemini Native 回归 Key',
      groupBindings: [{ groupId: geminiNativeTargetBridgeGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(geminiNativeTargetBridgeApiKey.key, 'OpenAI Anthropic 转 Gemini Native 回归 API Key 未返回明文密钥')
    const geminiNativeTargetRuntime = await gatewayCache.readCachedGatewayRuntimeAsync(geminiNativeTargetBridgeApiKey.key)
    assert.equal(geminiNativeTargetRuntime.apiKey?.selected_group_id, geminiNativeTargetBridgeGroup.id, 'OpenAI/Anthropic -> Gemini native runtime 应选中桥接分组')
    assert(
      geminiNativeTargetRuntime.accounts.some((item) => item.id === geminiNativeTargetBridgeAccount.id),
      `OpenAI/Anthropic -> Gemini native runtime 应包含桥接账号，实际 ${geminiNativeTargetRuntime.accounts.length}，诊断 ${JSON.stringify(geminiNativeTargetRuntime.accountDispatchDiagnostics)}`
    )

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    assertGeminiProtocolHelpers()
    await assertGeminiModels(baseUrl, apiKey.key)
    await assertGeminiGenerateContentJson(baseUrl, apiKey.key)
    await assertGeminiGenerateContentXGoogApiKey(baseUrl, apiKey.key)
    await assertGeminiGenerateContentKeyQuery(baseUrl, apiKey.key)
    await assertGeminiStreamGenerateContent(baseUrl, apiKey.key)
    await assertGeminiCountTokens(baseUrl, apiKey.key)
    await assertGenericGeminiRetryableErrorDoesNotSwitchAccount(baseUrl, apiKey.key)
    await assertGeminiCliRetryableErrorSwitchesAccount(baseUrl, apiKey.key)
    await assertGeminiUpstreamError(baseUrl, apiKey.key)
    await assertGeminiLocalAuthError(baseUrl)
    await assertGeminiOpenAIChatPathRejected(baseUrl, apiKey.key)
    await assertGeminiOpenAIChatDirect(baseUrl, openAIChatApiKey.key)
    await assertGeminiOpenAIChatRootBaseUrl(baseUrl, openAIChatRootApiKey.key)
    assertGeminiPolicyDefaults()

    console.log('gemini gateway mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGeminiSeeds(): void {
  const providers = repositories.listProviders()
  const geminiProvider = providers.find((provider) => provider.code === GEMINI_PROVIDER_CODE)
  assert(geminiProvider, 'Gemini 供应商种子必须存在')
  assert.equal(geminiProvider.defaultProtocolProfileId, GEMINI_NATIVE_V1BETA_PROFILE_ID, 'Gemini 默认协议档案必须指向 v1beta 原生协议')
  assert(geminiProvider.protocolProfiles.some((profile) => profile.id === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID), 'Gemini 必须内置 OpenAI Chat 兼容协议档案')
  const catalog = modelCatalog.listProviderModelCatalog({ providerCode: GEMINI_PROVIDER_CODE })
  const geminiFlash = catalog.find((item) => item.model === 'gemini-3.5-flash')
  assert(geminiFlash, 'Gemini 模型目录必须包含 gemini-3.5-flash')
  assert(geminiFlash.supportedApiProtocols.includes('chat_completions'), 'Gemini 文本模型必须可作为 OpenAI Chat 映射上游')
  assert(geminiFlash.supportedApiProtocols.includes('generate_content'), 'Gemini 文本模型必须可作为 Gemini GenerateContent 映射下游')
  assert(catalog.some((item) => item.model === 'gemini-3.1-pro-preview'), 'Gemini 模型目录必须包含官方 Gemini 3.1 Pro Preview')
  assert(modelCatalog.listProviderModelCatalog({ providerCode: GLM_PROVIDER_CODE }).some((item) => item.model === 'glm-5.2'), 'GLM 模型目录必须包含 glm-5.2')
  assert(catalog.some((item) => item.model === 'gemini-embedding-2'), 'Gemini 模型目录必须包含官方 Gemini Embedding 2')
  assert.equal(catalog.some((item) => item.model.includes('antigravity')), false, 'Gemini 官方内置目录不应包含中转自定义 Antigravity 型号')
}

function assertGeminiProtocolHelpers(): void {
  const jsonUsage = parseGeminiUsageFromJsonBuffer(Buffer.from(JSON.stringify({
    usageMetadata: {
      promptTokenCount: 12,
      candidatesTokenCount: 7,
      cachedContentTokenCount: 3
    }
  }), 'utf8'))
  assert.equal(jsonUsage.inputTokens, 12)
  assert.equal(jsonUsage.outputTokens, 7)
  assert.equal(jsonUsage.cacheReadTokens, 3)

  const jsonFrames = extractGeminiJsonSemanticFrames({
    candidates: [
      {
        content: {
          parts: [{ text: 'gemini helper ok' }]
        },
        finishReason: 'STOP'
      }
    ],
    usageMetadata: {
      promptTokenCount: 1,
      candidatesTokenCount: 2
    }
  }, 'generate_content')
  assert(jsonFrames.some((frame) => frame.frameType === 'output_text_done'), 'Gemini JSON 语义帧必须识别输出文本')
  assert(jsonFrames.some((frame) => frame.frameType === 'usage'), 'Gemini JSON 语义帧必须识别 usageMetadata')

  const sseText = 'data: {"candidates":[{"content":{"parts":[{"text":"gemini stream helper"}]}}]}\n\n'
  const sseFrames = extractGeminiSseSemanticFrames(parseOpenAISseEventText(sseText), geminiResponseEndpointFamilyFromPath('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse'))
  assert(sseFrames.some((frame) => frame.frameType === 'output_text_delta'), 'Gemini SSE 语义帧必须识别增量输出')

  const inspection = inspectGeminiStreamText(sseText)
  assert.equal(inspection.outputReceived, true, 'Gemini 流检查必须识别可见输出')
  assert.equal(inspection.terminalReceived, true, 'Gemini 流检查在 EOF 时必须视为终止成功')

  const parsedError = parseGeminiErrorPayload(JSON.stringify({
    error: {
      code: 429,
      message: 'quota exhausted',
      status: 'RESOURCE_EXHAUSTED'
    }
  }), new Headers({ 'content-type': 'application/json' }))
  assert.equal(parsedError.code, '429')
  assert.equal(parsedError.type, 'RESOURCE_EXHAUSTED')
  assert.equal(parsedError.message, 'quota exhausted')
}

function explicitHybridRule(input: {
  id: string
  sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'
  sourceModel: string
  targetGroupId: string
  upstreamEndpointFamily: 'chat_completions' | 'messages' | 'generate_content'
  upstreamModel: string
}) {
  return {
    id: input.id,
    enabled: true,
    priority: 1,
    sourceClientProfile: 'auto' as const,
    sourceEndpointFamily: input.sourceEndpointFamily,
    sourceModel: input.sourceModel,
    targetGroupId: input.targetGroupId,
    upstreamEndpointFamily: input.upstreamEndpointFamily,
    upstreamModel: input.upstreamModel,
    adapterMode: 'bridge' as const
  }
}

function runtimeMappingsFromExplicitRules(rules: Array<ReturnType<typeof explicitHybridRule>>) {
  return rules.map((rule) => ({
    sourceModel: rule.sourceModel,
    sourceEndpointFamily: rule.sourceEndpointFamily,
    upstreamModel: rule.upstreamModel,
    upstreamEndpointFamily: rule.upstreamEndpointFamily,
    enabled: true,
    runtimeSource: 'explicit_hybrid_route',
    runtimeRouteRuleId: rule.id
  })) as unknown as import('../../domain/types.js').AccountModelMapping[]
}

async function assertGeminiModels(baseUrl: string, localApiKey: string): Promise<void> {
  const before = upstreamHits.length
  const response = await fetch(new URL('/v1beta/models', baseUrl), {
    method: 'GET',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      accept: 'application/json'
    }
  })
  assert.equal(response.status, 200)
  const body = await response.json() as { models?: Array<{ name?: string }> }
  assert(body.models?.some((item) => item.name === 'models/gemini-3.5-flash'), 'Gemini models 响应必须包含内置模型')
  assert(body.models?.some((item) => item.name === 'models/gemini-3.1-pro-preview'), 'Gemini models 响应必须包含官方 Pro Preview 模型')
  assert.equal(body.models?.some((item) => item.name?.includes('antigravity')), false, 'Gemini models 响应不应包含中转自定义型号')
  assert.equal(upstreamHits.length, before, 'Gemini models 请求不应命中上游')
}

async function assertGeminiGenerateContentJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with gemini json ok' }] }
      ]
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as {
    candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }>
    usageMetadata?: { promptTokenCount?: number; candidatesTokenCount?: number }
  }
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'gemini json ok')
  assert.equal(body.usageMetadata?.promptTokenCount, 11)
  assert.equal(body.usageMetadata?.candidatesTokenCount, 4)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.authorization, '', 'Gemini 上游请求不应透传本地 Authorization')
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-upstream', 'Gemini 上游请求必须使用账号 API Key')
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:generateContent')
}

async function assertGeminiGenerateContentXGoogApiKey(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent', baseUrl), {
    method: 'POST',
    headers: {
      'x-goog-api-key': localApiKey,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with gemini json ok' }] }
      ]
    })
  })
  assert.equal(response.status, 200)
  const body = await response.json() as { candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }> }
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'gemini json ok')
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-upstream')
}

async function assertGeminiGenerateContentKeyQuery(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL(`/v1beta/models/gemini-3.5-flash:generateContent?key=${encodeURIComponent(localApiKey)}&alt=json`, baseUrl), {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with gemini json ok' }] }
      ]
    })
  })
  assert.equal(response.status, 200)
  const body = await response.json() as { candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }> }
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'gemini json ok')
  assert.equal(upstreamHits[0]?.rawUrl.includes('key='), false, 'Gemini 上游请求不应保留本地 key 查询参数')
  assert.equal(upstreamHits[0]?.rawUrl.includes('alt=json'), true, 'Gemini 上游请求应保留非认证查询参数')
}

async function assertGeminiStreamGenerateContent(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with gemini sse ok' }] }
      ]
    })
  })
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('content-type')?.includes('text/event-stream'), true)
  const text = await response.text()
  assert.match(text, /gemini /)
  assert.match(text, /sse ok/)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:streamGenerateContent')
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-upstream')
}

async function assertGeminiCountTokens(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:countTokens', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'count these tokens' }] }
      ]
    })
  })
  assert.equal(response.status, 200)
  const body = await response.json() as { totalTokens?: number }
  assert.equal(body.totalTokens, 17)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:countTokens')
}

async function assertGeminiUpstreamError(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?case=quota', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'force quota error' }] }
      ]
    })
  })
  assert.equal(response.status, 503)
  const body = await response.json() as { error?: { status?: string; message?: string; code?: string } }
  assert.equal(body.error?.status, 'UNAVAILABLE')
  assert.match(body.error?.message ?? '', /上游暂时不可用，请重试/)
  assert.equal(body.error?.code, 'upstream_retryable_error')
  assert(upstreamHits.length >= 1, 'Gemini 上游错误用例必须命中 mock upstream')
  assert(upstreamHits.every((hit) => hit.rawUrl.includes('case=quota')), 'Gemini 上游错误重试必须保持原始查询参数')
}

async function assertGenericGeminiRetryableErrorDoesNotSwitchAccount(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?case=generic-retryable-json', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'generic retryable error should not switch' }] }
      ]
    })
  })
  assert.equal(response.status, 503)
  await response.text()
  assert.equal(upstreamHits.length, 1, '通用 Gemini 客户端不应触发 Gemini CLI 专属服务端换账号')
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-upstream')
}

async function assertGeminiCliRetryableErrorSwitchesAccount(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?case=cli-retryable-json', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'user-agent': 'GeminiCLI/0.12.0/gemini-3.5-flash (win32; x64)'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'gemini cli should switch account' }] }
      ]
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }> }
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'gemini cli retry ok')
  assert.deepEqual(
    upstreamHits.map((hit) => hit.xGoogApiKey),
    ['sk-gemini-upstream', 'sk-gemini-upstream-fallback'],
    'Gemini CLI 专属可重试错误应先命中主账号，再由服务端切到下一个账号'
  )
}

async function assertGeminiLocalAuthError(baseUrl: string): Promise<void> {
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent', baseUrl), {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'missing auth' }] }
      ]
    })
  })
  assert.equal(response.status, 401)
  const body = await response.json() as { error?: { status?: string; message?: string } }
  assert.equal(body.error?.status, 'UNAUTHENTICATED')
  assert.match(body.error?.message ?? '', /缺少访问令牌/)
}

async function assertGeminiOpenAIChatPathRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/chat/completions', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'should not route to Gemini' }]
    })
  })
  assert.notEqual(response.status, 200, 'Gemini 分组不应直接承接 OpenAI Chat 路径')
  assert.equal(upstreamHits.length, 0, 'OpenAI Chat 路径不应命中 Gemini 上游')
}

async function assertGeminiOpenAIChatDirect(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/chat/completions?trace=gemini-openai-chat', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'gemini-3.5-flash',
      messages: [{ role: 'user', content: 'reply with gemini openai direct ok' }],
      stream: false
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, 'gemini openai direct ok')
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/v1beta/openai/chat/completions?trace=gemini-openai-chat')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-gemini-openai-upstream', 'Gemini OpenAI Chat 上游必须使用账号 Bearer API Key')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '', 'Gemini OpenAI Chat 上游不应使用 Gemini 原生 x-goog-api-key')
  assert.equal(JSON.parse(upstreamHits[0]?.bodyText ?? '{}').model, 'gemini-3.5-flash')
}

async function assertGeminiOpenAIChatRootBaseUrl(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/chat/completions?trace=gemini-openai-chat-root', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gemini-3.5-flash',
      messages: [{ role: 'user', content: 'reply with gemini openai direct ok' }],
      stream: false
    })
  })
  assert.equal(response.status, 200)
  const body = await response.json() as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, 'gemini openai direct ok')
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/v1/chat/completions?trace=gemini-openai-chat-root')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-gemini-openai-root-upstream', 'Gemini OpenAI Chat 根地址上游必须使用账号 Bearer API Key')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '', 'Gemini OpenAI Chat 根地址上游不应使用 Gemini 原生 x-goog-api-key')
}

async function assertGeminiCodexResponsesMapping(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/responses?trace=gemini-codex-bridge', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'reply with gemini openai bridge ok',
      stream: true
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /response\.completed/, 'Codex Responses 桥接必须返回 Responses SSE 完成事件')
  assert.match(responseText, /gemini openai bridge ok/, 'Codex Responses 桥接必须透出 Gemini OpenAI Chat 文本')
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/v1beta/openai/chat/completions?trace=gemini-codex-bridge')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-gemini-openai-upstream')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { model?: string; stream?: boolean; messages?: unknown[] }
  assert.equal(upstreamBody.model, 'gemini-3.5-flash', 'Responses -> Chat 显式模型映射必须改写上游 Gemini 模型')
  assert.equal(upstreamBody.stream, true, 'Codex Responses -> Chat 桥接必须使用上游 Chat SSE')
  assert(Array.isArray(upstreamBody.messages) && upstreamBody.messages.length > 0, 'Codex Responses -> Chat 桥接必须生成 Chat messages')
}

async function assertGeminiGenerateContentToGlmChatJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?trace=gemini-native-to-glm-chat-json', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      systemInstruction: {
        parts: [{ text: '只输出简短中文' }]
      },
      contents: [
        { role: 'user', parts: [{ text: 'reply with glm gemini bridge json ok' }] }
      ],
      generationConfig: {
        temperature: 0.2,
        topP: 0.9,
        maxOutputTokens: 64,
        responseMimeType: 'application/json'
      },
      tools: [
        {
          functionDeclarations: [
            {
              name: 'lookup_order',
              description: '查询订单',
              parameters: {
                type: 'object',
                properties: {
                  id: { type: 'string' }
                }
              }
            }
          ]
        }
      ],
      toolConfig: {
        functionCallingConfig: {
          mode: 'AUTO'
        }
      }
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as {
    candidates?: Array<{ content?: { role?: string; parts?: Array<{ text?: string }> }; finishReason?: string }>
    usageMetadata?: { promptTokenCount?: number; candidatesTokenCount?: number; totalTokenCount?: number }
  }
  assert.equal(body.candidates?.[0]?.content?.role, 'model')
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'glm gemini bridge json ok')
  assert.equal(body.candidates?.[0]?.finishReason, 'STOP')
  assert.equal(body.usageMetadata?.promptTokenCount, 13)
  assert.equal(body.usageMetadata?.candidatesTokenCount, 5)
  assert.equal(body.usageMetadata?.totalTokenCount, 18)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/chat/completions?trace=gemini-native-to-glm-chat-json')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-glm-upstream')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '', 'Gemini -> Chat 桥接上游不应使用 Gemini 原生 x-goog-api-key')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as {
    model?: string
    stream?: boolean
    messages?: Array<{ role?: string; content?: unknown }>
    temperature?: number
    top_p?: number
    max_tokens?: number
    response_format?: { type?: string }
    tools?: unknown[]
    tool_choice?: unknown
  }
  assert.equal(upstreamBody.model, 'glm-5.2', 'Gemini -> Chat 显式模型映射必须改写 GLM 上游模型')
  assert.equal(upstreamBody.stream, false)
  assert.equal(upstreamBody.messages?.[0]?.role, 'system')
  assert.equal(upstreamBody.messages?.[1]?.role, 'user')
  assert.equal(upstreamBody.temperature, 0.2)
  assert.equal(upstreamBody.top_p, 0.9)
  assert.equal(upstreamBody.max_tokens, 64)
  assert.equal(upstreamBody.response_format?.type, 'json_object')
  assert(Array.isArray(upstreamBody.tools) && upstreamBody.tools.length === 1, 'Gemini functionDeclarations 必须转换为 Chat tools')
  assert.equal(upstreamBody.tool_choice, 'auto')
}

async function assertGeminiStreamGenerateContentToGlmChatSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse&trace=gemini-native-to-glm-chat-sse', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with glm gemini bridge stream ok' }] }
      ],
      generationConfig: {
        maxOutputTokens: 64
      }
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /glm gemini /)
  assert.match(responseText, /bridge stream ok/)
  assert.match(responseText, /usageMetadata/)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/chat/completions?trace=gemini-native-to-glm-chat-sse')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-glm-upstream')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { model?: string; stream?: boolean; messages?: unknown[]; max_tokens?: number }
  assert.equal(upstreamBody.model, 'glm-5.2')
  assert.equal(upstreamBody.stream, true)
  assert.equal(upstreamBody.max_tokens, 64)
  assert(Array.isArray(upstreamBody.messages) && upstreamBody.messages.length === 1, 'Gemini stream -> Chat 桥接必须生成 Chat messages')
}

async function assertGeminiGenerateContentToAnthropicMessagesJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?trace=gemini-native-to-anthropic-messages-json', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      systemInstruction: {
        parts: [{ text: '只输出简短中文' }]
      },
      contents: [
        {
          role: 'user',
          parts: [
            { text: 'reply with anthropic gemini bridge json ok' },
            { inlineData: { mimeType: 'image/png', data: 'iVBORw0KGgo=' } }
          ]
        },
        {
          role: 'model',
          parts: [
            { functionCall: { name: 'lookup_order', args: { id: 'A-099' } } }
          ]
        },
        {
          role: 'function',
          parts: [
            { functionResponse: { name: 'lookup_order', response: { status: 'paid' } } }
          ]
        },
        {
          role: 'user',
          parts: [{ text: 'now answer with final text' }]
        }
      ],
      generationConfig: {
        temperature: 0.2,
        topP: 0.9,
        topK: 40,
        maxOutputTokens: 64,
        stopSequences: ['END'],
        responseMimeType: 'text/plain'
      },
      tools: [
        {
          functionDeclarations: [
            {
              name: 'lookup_order',
              description: '查询订单',
              parameters: {
                type: 'object',
                properties: {
                  id: { type: 'string' }
                }
              }
            }
          ]
        }
      ],
      toolConfig: {
        functionCallingConfig: {
          mode: 'ANY',
          allowedFunctionNames: ['lookup_order']
        }
      }
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as {
    candidates?: Array<{ content?: { role?: string; parts?: Array<{ text?: string; functionCall?: { name?: string; args?: Record<string, unknown> } }> }; finishReason?: string }>
    usageMetadata?: { promptTokenCount?: number; candidatesTokenCount?: number; totalTokenCount?: number; cachedContentTokenCount?: number; thoughtsTokenCount?: number }
    modelVersion?: string
  }
  assert.equal(body.candidates?.[0]?.content?.role, 'model')
  assert.equal(body.candidates?.[0]?.content?.parts?.[0]?.text, 'anthropic gemini bridge json ok')
  assert.equal(body.candidates?.[0]?.content?.parts?.[1]?.functionCall?.name, 'lookup_order')
  assert.equal(body.candidates?.[0]?.content?.parts?.[1]?.functionCall?.args?.id, 'A-100')
  assert.equal(body.candidates?.[0]?.finishReason, 'STOP')
  assert.equal(body.modelVersion, 'claude-haiku-4-5')
  assert.equal(body.usageMetadata?.promptTokenCount, 15)
  assert.equal(body.usageMetadata?.candidatesTokenCount, 6)
  assert.equal(body.usageMetadata?.totalTokenCount, 21)
  assert.equal(body.usageMetadata?.cachedContentTokenCount, 2)
  assert.equal(body.usageMetadata?.thoughtsTokenCount, 1)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/v1/messages?trace=gemini-native-to-anthropic-messages-json')
  assert.equal(upstreamHits[0]?.xApiKey, 'sk-anthropic-upstream')
  assert.equal(upstreamHits[0]?.authorization, '', 'Gemini -> Anthropic Messages 桥接上游不应使用 Bearer Authorization')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '', 'Gemini -> Anthropic Messages 桥接上游不应使用 Gemini 原生 x-goog-api-key')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as {
    model?: string
    stream?: boolean
    system?: string
    messages?: Array<{ role?: string; content?: Array<{ type?: string; text?: string; id?: string; name?: string; input?: Record<string, unknown>; tool_use_id?: string; source?: { type?: string; media_type?: string; data?: string } }> }>
    temperature?: number
    top_p?: number
    top_k?: number
    max_tokens?: number
    stop_sequences?: string[]
    tools?: Array<{ name?: string; input_schema?: unknown }>
    tool_choice?: { type?: string; name?: string }
  }
  assert.equal(upstreamBody.model, 'claude-haiku-4-5', 'Gemini -> Anthropic Messages 显式模型映射必须改写 Anthropic 上游模型')
  assert.equal(upstreamBody.stream, undefined)
  assert.equal(upstreamBody.system, '只输出简短中文')
  assert.equal(upstreamBody.messages?.[0]?.role, 'user')
  assert.equal(upstreamBody.messages?.[0]?.content?.[0]?.type, 'text')
  assert.equal(upstreamBody.messages?.[0]?.content?.[1]?.type, 'image')
  assert.equal(upstreamBody.messages?.[0]?.content?.[1]?.source?.type, 'base64')
  assert.equal(upstreamBody.messages?.[0]?.content?.[1]?.source?.media_type, 'image/png')
  assert.equal(upstreamBody.messages?.[1]?.role, 'assistant')
  assert.equal(upstreamBody.messages?.[1]?.content?.[0]?.type, 'tool_use')
  assert.equal(upstreamBody.messages?.[1]?.content?.[0]?.name, 'lookup_order')
  assert.equal(upstreamBody.messages?.[1]?.content?.[0]?.input?.id, 'A-099')
  assert.equal(upstreamBody.messages?.[2]?.role, 'user')
  assert.equal(upstreamBody.messages?.[2]?.content?.[0]?.type, 'tool_result')
  assert.equal(upstreamBody.messages?.[2]?.content?.[0]?.tool_use_id, upstreamBody.messages?.[1]?.content?.[0]?.id)
  assert.equal(upstreamBody.messages?.[2]?.content?.[1]?.text, 'now answer with final text')
  assert.equal(upstreamBody.temperature, 0.2)
  assert.equal(upstreamBody.top_p, 0.9)
  assert.equal(upstreamBody.top_k, 40)
  assert.equal(upstreamBody.max_tokens, 64)
  assert.deepEqual(upstreamBody.stop_sequences, ['END'])
  assert.equal(upstreamBody.tools?.[0]?.name, 'lookup_order')
  assert.equal(upstreamBody.tool_choice?.type, 'tool')
  assert.equal(upstreamBody.tool_choice?.name, 'lookup_order')
}

async function assertGeminiStreamGenerateContentToAnthropicMessagesSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse&trace=gemini-native-to-anthropic-messages-sse', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'reply with anthropic gemini bridge stream ok' }] }
      ],
      generationConfig: {
        maxOutputTokens: 64
      }
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /anthropic gemini /)
  assert.match(responseText, /bridge stream ok/)
  assert.match(responseText, /"functionCall":\{"name":"lookup_stream"/)
  assert.match(responseText, /usageMetadata/)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.rawUrl, '/v1/messages?trace=gemini-native-to-anthropic-messages-sse')
  assert.equal(upstreamHits[0]?.xApiKey, 'sk-anthropic-upstream')
  assert.equal(upstreamHits[0]?.xGoogApiKey, '')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { model?: string; stream?: boolean; messages?: unknown[]; max_tokens?: number }
  assert.equal(upstreamBody.model, 'claude-haiku-4-5')
  assert.equal(upstreamBody.stream, true)
  assert.equal(upstreamBody.max_tokens, 64)
  assert(Array.isArray(upstreamBody.messages) && upstreamBody.messages.length === 1, 'Gemini stream -> Anthropic Messages 桥接必须生成 Messages messages')
}

async function assertGeminiGenerateContentToAnthropicMessagesGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1beta/models/gemini-3.5-flash:generateContent?trace=gemini-native-to-anthropic-messages-guidance', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      contents: [
        { role: 'user', parts: [{ text: 'unsupported json schema should be guidance' }] }
      ],
      generationConfig: {
        responseMimeType: 'application/json',
        responseSchema: {
          type: 'object',
          properties: {
            ok: { type: 'boolean' }
          }
        }
      }
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }> }
  const text = body.candidates?.[0]?.content?.parts?.[0]?.text ?? ''
  assert.match(text, /responseMimeType|responseSchema|Anthropic Messages|上游/, '不支持的 Gemini JSON schema 能力应返回 Gemini JSON guidance')
  assert.equal(upstreamHits.length, 0, 'Gemini -> Anthropic Messages guidance 不应命中上游')
}

async function assertOpenAIChatToGeminiNativeJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/chat/completions', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'system', content: '只输出简短中文' },
        { role: 'user', content: [{ type: 'text', text: 'reply with gemini json ok' }] }
      ],
      tools: [{ type: 'function', function: { name: 'lookup_order', parameters: { type: 'object', properties: { id: { type: 'string' } } } } }],
      response_format: { type: 'json_object' },
      reasoning_effort: 'medium',
      service_tier: 'default',
      temperature: 0.2,
      max_tokens: 64
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { choices?: Array<{ message?: { content?: string | null } }>; usage?: { prompt_tokens?: number } }
  assert.equal(body.choices?.[0]?.message?.content, 'gemini json ok')
  assert.equal(body.usage?.prompt_tokens, 11)
  assert.equal(upstreamHits.length, 1)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:generateContent')
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-target-upstream')
  assert.equal(upstreamHits[0]?.authorization, '', 'OpenAI -> Gemini native 上游不应透传本地 Authorization')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { contents?: Array<{ role?: string; parts?: Array<{ text?: string }> }>; systemInstruction?: { parts?: Array<{ text?: string }> }; generationConfig?: { responseMimeType?: string; temperature?: number; maxOutputTokens?: number; thinkingConfig?: { thinkingLevel?: string } }; serviceTier?: string; tools?: Array<{ functionDeclarations?: Array<{ name?: string }> }> }
  assert.equal(upstreamBody.systemInstruction?.parts?.[0]?.text, '只输出简短中文')
  assert.equal(upstreamBody.contents?.[0]?.role, 'user')
  assert.equal(upstreamBody.contents?.[0]?.parts?.[0]?.text, 'reply with gemini json ok')
  assert.equal(upstreamBody.generationConfig?.responseMimeType, 'application/json')
  assert.equal(upstreamBody.generationConfig?.temperature, 0.2)
  assert.equal(upstreamBody.generationConfig?.maxOutputTokens, 64)
  assert.equal(upstreamBody.generationConfig?.thinkingConfig?.thinkingLevel, 'MEDIUM')
  assert.equal(upstreamBody.serviceTier, 'standard')
  assert.equal(upstreamBody.tools?.[0]?.functionDeclarations?.[0]?.name, 'lookup_order')
}

async function assertOpenAIChatToGeminiNativeSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/chat/completions', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      stream: true,
      messages: [{ role: 'user', content: 'reply with gemini sse ok' }]
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /chat\.completion\.chunk/)
  assert.match(responseText, /gemini /)
  assert.match(responseText, /sse ok/)
  assert.match(responseText, /data: \[DONE\]/)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:streamGenerateContent')
  assert.match(upstreamHits[0]?.rawUrl ?? '', /alt=sse/)
  assert.equal(upstreamHits[0]?.xGoogApiKey, 'sk-gemini-target-upstream')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { contents?: unknown[] }
  assert(Array.isArray(upstreamBody.contents) && upstreamBody.contents.length === 1, 'OpenAI Chat SSE -> Gemini native 必须生成 Gemini contents')
}

async function assertOpenAIResponsesToGeminiNativeJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/responses', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      instructions: '只输出简短中文',
      input: 'reply with gemini json ok'
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { output?: Array<{ content?: Array<{ text?: string }> }> }
  assert.equal(body.output?.[0]?.content?.[0]?.text, 'gemini json ok')
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:generateContent')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { contents?: Array<{ parts?: Array<{ text?: string }> }>; systemInstruction?: { parts?: Array<{ text?: string }> } }
  assert.equal(upstreamBody.systemInstruction?.parts?.[0]?.text, '只输出简短中文')
  assert.equal(upstreamBody.contents?.[0]?.parts?.[0]?.text, 'reply with gemini json ok')
}

async function assertOpenAIResponsesToGeminiNativeSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/responses', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      stream: true,
      input: 'reply with gemini sse ok'
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /response.output_text.delta/)
  assert.match(responseText, /gemini /)
  assert.match(responseText, /sse ok/)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:streamGenerateContent')
}

async function assertAnthropicMessagesToGeminiNativeJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/messages', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      max_tokens: 64,
      system: '只输出简短中文',
      messages: [{ role: 'user', content: 'reply with gemini json ok' }]
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  const body = JSON.parse(responseText) as { content?: Array<{ type?: string; text?: string }>; usage?: { input_tokens?: number } }
  assert.equal(body.content?.[0]?.type, 'text')
  assert.equal(body.content?.[0]?.text, 'gemini json ok')
  assert.equal(body.usage?.input_tokens, 11)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:generateContent')
  const upstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { contents?: Array<{ role?: string; parts?: Array<{ text?: string }> }>; systemInstruction?: { parts?: Array<{ text?: string }> } }
  assert.equal(upstreamBody.systemInstruction?.parts?.[0]?.text, '只输出简短中文')
  assert.equal(upstreamBody.contents?.[0]?.role, 'user')
  assert.equal(upstreamBody.contents?.[0]?.parts?.[0]?.text, 'reply with gemini json ok')
}

async function assertAnthropicMessagesToGeminiNativeSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/messages', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'claude-haiku-4-5',
      stream: true,
      max_tokens: 64,
      messages: [{ role: 'user', content: 'reply with gemini sse ok' }]
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(responseText, /message_start/)
  assert.match(responseText, /text_delta/)
  assert.match(responseText, /gemini /)
  assert.match(responseText, /sse ok/)
  assert.equal(upstreamHits[0]?.path, '/v1beta/models/gemini-3.5-flash:streamGenerateContent')
}

async function assertOpenAIResponsesToGeminiNativeGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(new URL('/v1/responses', baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: 'resp_old',
      input: 'this should be guidance'
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, responseText)
  assert.match(responseText, /previous_response_id|context_management|Gemini native/, 'Responses 状态链不能保真时应返回 Responses guidance')
  assert.equal(upstreamHits.length, 0, 'Responses -> Gemini native guidance 不应命中上游')
}

function assertGeminiPolicyDefaults(): void {
  const defaultRules = listResponseInspectionPolicyDefaultRules()
  assert(defaultRules.some((rule) => rule.protocolCode === GEMINI_PROTOCOL_CODE && rule.id === 'default_gemini_error_object'), 'Gemini 默认响应检查策略必须存在')
  assert(defaultRules.some((rule) => rule.protocolCode === GEMINI_PROTOCOL_CODE && rule.id === 'default_gemini_cli_retryable_error' && rule.match.clientProfiles?.includes('gemini_cli')), 'Gemini CLI 专属默认响应检查策略必须存在')
}

function createGeminiMockUpstream(): http.Server {
  return http.createServer(async (req, res) => {
    const rawUrl = req.url ?? '/'
    const url = new URL(rawUrl, 'http://127.0.0.1')
    const bodyText = await readRequestBody(req)
    upstreamHits.push({
      rawUrl,
      path: url.pathname,
      method: req.method ?? 'GET',
      authorization: headerText(req, 'authorization'),
      xGoogApiKey: headerText(req, 'x-goog-api-key'),
      xApiKey: headerText(req, 'x-api-key'),
      accept: headerText(req, 'accept'),
      bodyText
    })

    if (req.method === 'GET' && url.pathname === '/v1beta/models') {
      sendJson(res, 200, {
        models: [
          {
            name: 'models/gemini-3.5-flash',
            version: 'gemini-3.5-flash',
            displayName: 'gemini-3.5-flash',
            supportedGenerationMethods: ['generateContent', 'countTokens']
          },
          {
            name: 'models/gemini-embedding-001',
            version: 'gemini-embedding-001',
            displayName: 'gemini-embedding-001',
            supportedGenerationMethods: ['embedContent']
          }
        ]
      })
      return
    }

    if (req.method === 'POST' && url.pathname === '/v1beta/models/gemini-3.5-flash:generateContent') {
      if (url.searchParams.get('case') === 'generic-retryable-json' || url.searchParams.get('case') === 'cli-retryable-json') {
        if (headerText(req, 'x-goog-api-key') === 'sk-gemini-upstream') {
          sendJson(res, 200, {
            error: {
              code: 503,
              message: 'primary account temporarily unavailable',
              status: 'UNAVAILABLE'
            }
          })
          return
        }
        sendJson(res, 200, {
          candidates: [
            {
              content: {
                parts: [{ text: 'gemini cli retry ok' }]
              },
              finishReason: 'STOP'
            }
          ],
          usageMetadata: {
            promptTokenCount: 12,
            candidatesTokenCount: 5
          }
        })
        return
      }
      if (url.searchParams.get('case') === 'quota') {
        sendJson(res, 429, {
          error: {
            code: 429,
            message: 'quota exhausted',
            status: 'RESOURCE_EXHAUSTED'
          }
        })
        return
      }
      sendJson(res, 200, {
        candidates: [
          {
            content: {
              parts: [{ text: 'gemini json ok' }]
            },
            finishReason: 'STOP'
          }
        ],
        usageMetadata: {
          promptTokenCount: 11,
          candidatesTokenCount: 4,
          cachedContentTokenCount: 2
        }
      })
      return
    }

    if (req.method === 'POST' && url.pathname === '/v1beta/models/gemini-3.5-flash:streamGenerateContent') {
      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache, no-transform',
        connection: 'keep-alive'
      })
      res.write('data: {"candidates":[{"content":{"parts":[{"text":"gemini "}]}}]}\n\n')
      res.write('data: {"candidates":[{"content":{"parts":[{"text":"sse ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":5}}\n\n')
      res.end()
      return
    }

    if (req.method === 'POST' && url.pathname === '/v1beta/models/gemini-3.5-flash:countTokens') {
      sendJson(res, 200, { totalTokens: 17 })
      return
    }

    if (req.method === 'POST' && (url.pathname === '/v1beta/openai/chat/completions' || url.pathname === '/v1/chat/completions' || url.pathname === '/chat/completions')) {
      const body = parseJsonObject(bodyText)
      if (body.stream === true) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache, no-transform',
          connection: 'keep-alive'
        })
        const streamText = url.pathname === '/chat/completions'
          ? 'glm gemini bridge stream ok'
          : 'gemini openai bridge ok'
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl-gemini-openai',
          object: 'chat.completion.chunk',
          created: 0,
          model: body.model ?? 'gemini-3.5-flash',
          choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }]
        })}\n\n`)
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl-gemini-openai',
          object: 'chat.completion.chunk',
          created: 0,
          model: body.model ?? 'gemini-3.5-flash',
          choices: [{ index: 0, delta: { content: streamText }, finish_reason: null }]
        })}\n\n`)
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl-gemini-openai',
          object: 'chat.completion.chunk',
          created: 0,
          model: body.model ?? 'gemini-3.5-flash',
          choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
          usage: { prompt_tokens: 9, completion_tokens: 4, total_tokens: 13 }
        })}\n\n`)
        res.write('data: [DONE]\n\n')
        res.end()
        return
      }
      sendJson(res, 200, {
        id: 'chatcmpl-gemini-openai',
        object: 'chat.completion',
        created: 0,
        model: body.model ?? 'gemini-3.5-flash',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: url.pathname === '/chat/completions' ? 'glm gemini bridge json ok' : 'gemini openai direct ok' },
            finish_reason: 'stop'
          }
        ],
        usage: url.pathname === '/chat/completions'
          ? { prompt_tokens: 13, completion_tokens: 5, total_tokens: 18 }
          : { prompt_tokens: 7, completion_tokens: 4, total_tokens: 11 }
      })
      return
    }

    if (req.method === 'POST' && (url.pathname === '/messages' || url.pathname === '/v1/messages')) {
      const body = parseJsonObject(bodyText)
      if (body.stream === true) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache, no-transform',
          connection: 'keep-alive'
        })
        res.write(`event: message_start\ndata: ${JSON.stringify({
          type: 'message_start',
          message: {
            id: 'msg-gemini-anthropic-stream',
            type: 'message',
            role: 'assistant',
            model: body.model ?? 'claude-haiku-4-5',
            content: [],
            stop_reason: null,
            stop_sequence: null,
            usage: { input_tokens: 8, output_tokens: 0 }
          }
        })}\n\n`)
        res.write(`event: content_block_start\ndata: ${JSON.stringify({
          type: 'content_block_start',
          index: 0,
          content_block: { type: 'text', text: '' }
        })}\n\n`)
        res.write(`event: content_block_delta\ndata: ${JSON.stringify({
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'text_delta', text: 'anthropic gemini ' }
        })}\n\n`)
        res.write(`event: content_block_delta\ndata: ${JSON.stringify({
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'text_delta', text: 'bridge stream ok' }
        })}\n\n`)
        res.write(`event: content_block_stop\ndata: ${JSON.stringify({ type: 'content_block_stop', index: 0 })}\n\n`)
        res.write(`event: content_block_start\ndata: ${JSON.stringify({
          type: 'content_block_start',
          index: 1,
          content_block: { type: 'tool_use', id: 'toolu_stream_0', name: 'lookup_stream', input: {} }
        })}\n\n`)
        res.write(`event: content_block_delta\ndata: ${JSON.stringify({
          type: 'content_block_delta',
          index: 1,
          delta: { type: 'input_json_delta', partial_json: '{"id":"S-1"}' }
        })}\n\n`)
        res.write(`event: content_block_stop\ndata: ${JSON.stringify({ type: 'content_block_stop', index: 1 })}\n\n`)
        res.write(`event: message_delta\ndata: ${JSON.stringify({
          type: 'message_delta',
          delta: { stop_reason: 'end_turn', stop_sequence: null },
          usage: { output_tokens: 5, cache_read_input_tokens: 1 }
        })}\n\n`)
        res.write(`event: message_stop\ndata: ${JSON.stringify({ type: 'message_stop' })}\n\n`)
        res.end()
        return
      }
      sendJson(res, 200, {
        id: 'msg-gemini-anthropic-json',
        type: 'message',
        role: 'assistant',
        model: body.model ?? 'claude-haiku-4-5',
        content: [
          { type: 'text', text: 'anthropic gemini bridge json ok' },
          { type: 'tool_use', id: 'toolu_lookup_order_0', name: 'lookup_order', input: { id: 'A-100' } }
        ],
        stop_reason: 'tool_use',
        stop_sequence: null,
        usage: {
          input_tokens: 13,
          cache_read_input_tokens: 2,
          output_tokens: 6,
          thinking_tokens: 1
        }
      })
      return
    }

    sendJson(res, 404, {
      error: {
        code: 404,
        message: `not found: ${url.pathname}`,
        status: 'NOT_FOUND'
      }
    })
  })
}

function parseJsonObject(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function sendJson(res: http.ServerResponse, statusCode: number, body: unknown): void {
  const text = JSON.stringify(body)
  res.writeHead(statusCode, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(text)
  })
  res.end(text)
}

function readRequestBody(req: http.IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk) => {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
    })
    req.on('end', () => {
      resolve(Buffer.concat(chunks).toString('utf8'))
    })
    req.on('error', () => resolve(''))
  })
}

function headerText(req: http.IncomingMessage, name: string): string {
  const value = req.headers[name.toLowerCase()]
  if (Array.isArray(value)) {
    return value[0] ?? ''
  }
  return typeof value === 'string' ? value : ''
}

function fakeGatewayPostRequest(originalUrl: string): Request {
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {},
    body: {},
    header: () => undefined
  } as unknown as Request
}

function activateFixtureAccount(accountId: string): void {
  assert(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), `测试账户 ${accountId} 应通过后台检查激活`)
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolve) => {
    server.listen(0, '127.0.0.1', resolve)
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server) return
  await new Promise<void>((resolve) => {
    server.close(() => resolve())
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务器未启动')
  }
  return address
}
