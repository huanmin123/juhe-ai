import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ANTHROPIC_ANTHROPIC_V1_PROFILE_ID, ANTHROPIC_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import {
  captureGatewayRawBody,
  rejectGatewayRawBodyByContentLength
} from '../../modules/gateway/request/body-middleware.js'
import { preResolveGatewayRuntime } from '../../modules/gateway/request/pre-auth.js'
import { logger } from '../../shared/logger.js'

interface UpstreamHit {
  path: string
  method: string
  xApiKey: string
  authorization: string
  body: Record<string, unknown>
}

interface ImageGenerationHit {
  path: string
  method: string
  authorization: string
  body: Record<string, unknown>
}


const tempRoot = resolve(tmpdir(), `juhe-ai-openai-anthropic-bridge-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'openai-anthropic-bridge-mock.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context-state')
runtimeConfig.openAICompatibleFilesRoot = join(tempRoot, 'openai-files')
runtimeConfig.secret = 'openai-anthropic-bridge-mock-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter, handleGatewayDbServiceUnavailable },
  { requestContextMiddleware },
  { openAICompatibleFilesRouter },
  { openAICompatibleVectorStoresRouter },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  openAICompatibleVectorStoresRepository,
  openAIAnthropicBridge,
  openAICompatibleComputerAdapter
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../modules/openai-compatible-files/files.routes.js'),
  import('../../modules/openai-compatible-vector-stores/vector-stores.routes.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/openai-compatible-vector-stores.repository.js'),
  import('../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'),
  import('../../modules/openai-compatible-computer/computer-adapter.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
const imageGenerationHits: ImageGenerationHit[] = []
const mockImageBase64 = Buffer.from('mock image generation result', 'utf8').toString('base64')
const mockPartialImageBase64 = Buffer.from('mock partial image generation result', 'utf8').toString('base64')

const app = express()
app.use(requestContextMiddleware)
app.use(
  preResolveGatewayRuntime,
  handleGatewayDbServiceUnavailable,
  openAICompatibleFilesRouter,
  openAICompatibleVectorStoresRouter,
  rejectGatewayRawBodyByContentLength,
  express.raw({ type: () => true, limit: '8mb' }),
  captureGatewayRawBody,
  openAIGatewayRouter
)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let imageGenerationServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createAnthropicBridgeMockUpstream()
    imageGenerationServer = createImageGenerationMockProvider()
    await listen(upstreamServer)
    await listen(imageGenerationServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
    const imageGenerationEndpoint = `http://127.0.0.1:${serverAddress(imageGenerationServer).port}/v1/images/generations`
    const imageGenerationResponsesEndpoint = `http://127.0.0.1:${serverAddress(imageGenerationServer).port}/v1/responses`

    const group = repositories.createGroup({
      name: 'OpenAI 到 Anthropic 桥接 mock 分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: 'OpenAI 到 Anthropic 桥接 mock 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-ant-bridge-upstream',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['messages_json', 'messages_sse']
      },
      supportedModels: ['claude-haiku-4-5'],
      clientCompatibility: 'codex_responses',
      modelMappings: [
        {
          sourceModel: 'gpt-5.5',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'claude-haiku-4-5',
          upstreamEndpointFamily: 'messages',
          enabled: true
        },
        {
          sourceModel: 'gpt-5.5',
          sourceEndpointFamily: 'responses',
          upstreamModel: 'claude-haiku-4-5',
          upstreamEndpointFamily: 'messages',
          enabled: true
        }
      ],
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(account.modelMappings?.length, 2, '桥接账号应保存 Chat/Responses 映射')
    const chatCandidates = repositories.listOpenAIAccountsForGroup(group.id, access.systemAccountId, {
      requestedModel: 'gpt-5.5',
      requestedEndpointFamily: 'chat_completions'
    })
    assert.equal(chatCandidates.length, 1, `Chat 桥接模型窗口应返回 Anthropic 映射账号，实际 ${chatCandidates.length}`)
    assert.equal(chatCandidates[0]?.modelMappings?.length, 2, 'Chat 桥接模型窗口返回的账号应带模型映射')
    const apiKey = repositories.createApiKeyRecord({
      name: 'OpenAI 到 Anthropic 桥接 mock Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')
    const otherApiKey = repositories.createApiKeyRecord({
      name: 'OpenAI 到 Anthropic 桥接 mock 隔离 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(otherApiKey.key, '隔离回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertChatJsonBridge(baseUrl, apiKey.key)
    await assertChatSseBridge(baseUrl, apiKey.key)
    await assertChatSseIncludeUsageBridge(baseUrl, apiKey.key)
    await assertChatMultipleChoicesRejected(baseUrl, apiKey.key)
    await assertOpenAIOutputShapeDefaultControlsBridge(baseUrl, apiKey.key)
    await assertChatLogprobsRejected(baseUrl, apiKey.key)
    await assertChatAudioOutputRejected(baseUrl, apiKey.key)
    await assertChatInputAudioRejected(baseUrl, apiKey.key)
    await assertChatUnknownContentPartRejected(baseUrl, apiKey.key)
    await assertChatMultiToolSseBridge(baseUrl, apiKey.key)
    await assertChatLegacyFunctionsSseBridge(baseUrl, apiKey.key)
    await assertResponsesJsonBridge(baseUrl, apiKey.key)
    await assertOpenAISystemInstructionBridge(baseUrl, apiKey.key)
    await assertResponsesSseBridge(baseUrl, apiKey.key)
    await assertResponsesLogprobsIncludeRejected(baseUrl, apiKey.key)
    await assertResponsesTopLogprobsRejected(baseUrl, apiKey.key)
    await assertResponsesInputAudioRejected(baseUrl, apiKey.key)
    await assertResponsesUnknownContentPartRejected(baseUrl, apiKey.key)
    await assertOpenAISemanticDefaultControlsBridge(baseUrl, apiKey.key)
    await assertOpenAIBasicGenerationControlsBridge(baseUrl, apiKey.key)
    await assertOpenAISamplingControlsRejected(baseUrl, apiKey.key)
    await assertOpenAIPredictionRejected(baseUrl, apiKey.key)
    await assertOpenAIVerbosityRejected(baseUrl, apiKey.key)
    await assertOpenAIRequestControlBoundaries(baseUrl, apiKey.key)
    await assertResponsesMultiToolSseBridge(baseUrl, apiKey.key)
    await assertBridgeSseErrorShape(baseUrl, apiKey.key)
    await assertChatImageDataUrlBridge(baseUrl, apiKey.key)
    await assertChatImageUrlBridge(baseUrl, apiKey.key)
    await assertResponsesImageUrlBridge(baseUrl, apiKey.key)
    await assertResponsesImageDataUrlBridge(baseUrl, apiKey.key)
    await assertChatImageDataUrlMimeRejected(baseUrl, apiKey.key)
    await assertChatImageDataUrlBase64Rejected(baseUrl, apiKey.key)
    await assertResponsesImageDataUrlMimeRejected(baseUrl, apiKey.key)
    await assertResponsesImageDataUrlBase64Rejected(baseUrl, apiKey.key)
    await assertChatFileDataPdfBridge(baseUrl, apiKey.key)
    await assertResponsesFileDataTextBridge(baseUrl, apiKey.key)
    await assertResponsesFileUrlPdfBridge(baseUrl, apiKey.key)
    await assertChatFileDataInvalidBase64Rejected(baseUrl, apiKey.key)
    await assertResponsesFileDataInvalidBase64Rejected(baseUrl, apiKey.key)
    await assertChatFileDataUnsupportedMimeRejected(baseUrl, apiKey.key)
    await assertResponsesFileDataUnsupportedMimeRejected(baseUrl, apiKey.key)
    await assertChatFileUrlRejected(baseUrl, apiKey.key)
    await assertResponsesFileUrlUnsupportedRejected(baseUrl, apiKey.key)
    await assertChatToolCallBridge(baseUrl, apiKey.key)
    await assertChatLegacyFunctionsBridge(baseUrl, apiKey.key)
    await assertResponsesFunctionCallBridge(baseUrl, apiKey.key)
    await assertResponsesAllowedFunctionToolsBridge(baseUrl, apiKey.key)
    await assertOpenAIToolControlDefaultBridge(baseUrl, apiKey.key)
    await assertResponsesToolSearchNamespaceBridge(baseUrl, apiKey.key)
    await assertResponsesToolSearchNamespaceSseBridge(baseUrl, apiKey.key)
    await assertChatParallelToolCallsDisabledBridge(baseUrl, apiKey.key)
    await assertResponsesParallelToolCallsDisabledBridge(baseUrl, apiKey.key)
    await assertChatParallelToolCallsDisabledWithNoneBridge(baseUrl, apiKey.key)
    await assertResponsesMaxToolCallsRejected(baseUrl, apiKey.key)
    await assertResponsesStateControlBoundaries(baseUrl, apiKey.key)
    await assertResponsesThinkingForcedToolChoiceRejected(baseUrl, apiKey.key)
    await assertChatToolResultBridge(baseUrl, apiKey.key)
    await assertChatLegacyFunctionHistoryBridge(baseUrl, apiKey.key)
    await assertChatMultiToolHistoryOrderBridge(baseUrl, apiKey.key)
    await assertResponsesFunctionOutputBridge(baseUrl, apiKey.key)
    await assertResponsesMultiFunctionHistoryOrderBridge(baseUrl, apiKey.key)
    await assertChatMissingToolResultCallIdRejected(baseUrl, apiKey.key)
    await assertResponsesMissingFunctionOutputCallIdRejected(baseUrl, apiKey.key)
    await assertChatOrphanToolResultRejected(baseUrl, apiKey.key)
    await assertResponsesOrphanFunctionOutputRejected(baseUrl, apiKey.key)
    await assertChatDuplicateToolResultRejected(baseUrl, apiKey.key)
    await assertResponsesDuplicateFunctionOutputRejected(baseUrl, apiKey.key)
    await assertChatFunctionArgumentsPreservedBridge(baseUrl, apiKey.key)
    await assertResponsesFunctionArgumentsPreservedBridge(baseUrl, apiKey.key)
    await assertChatJsonSchemaStructuredOutputBridge(baseUrl, apiKey.key)
    await assertResponsesJsonSchemaStructuredOutputBridge(baseUrl, apiKey.key)
    await assertChatJsonSchemaValidationFailureBridge(baseUrl, apiKey.key)
    await assertResponsesJsonSchemaValidationFailureBridge(baseUrl, apiKey.key)
    await assertOpenAIReasoningDefaultControlsBridge(baseUrl, apiKey.key)
    await assertResponsesThinkingBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningSummaryAcceptedBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningEffortNoneBridge(baseUrl, apiKey.key)
    await assertChatReasoningEffortNoneBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningUnsupportedEffortRejected(baseUrl, apiKey.key)
    await assertChatReasoningUnsupportedEffortRejected(baseUrl, apiKey.key)
    await assertResponsesReasoningSummaryNoneBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningUnsupportedSummaryRejected(baseUrl, apiKey.key)
    await assertResponsesEncryptedReasoningIncludeRejected(baseUrl, apiKey.key)
    await assertResponsesUnsupportedIncludesRejected(baseUrl, apiKey.key)
    await assertResponsesEncryptedReasoningInputItemRejected(baseUrl, apiKey.key)
    await assertResponsesThinkingSseBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningSummaryAcceptedSseBridge(baseUrl, apiKey.key)
    await assertResponsesReasoningSummaryNoneSseBridge(baseUrl, apiKey.key)
    await assertResponsesHostedToolUnsupported(baseUrl, apiKey.key)
    await assertHostedToolRuntimeRejectMode(baseUrl, apiKey.key)
    await assertHostedToolRuntimeLocalUnavailable(baseUrl, apiKey.key)
    await assertHostedToolRuntimeLocalWorkerLoop(baseUrl, apiKey.key)
    await assertHostedToolRuntimeMockMode(baseUrl, apiKey.key)
    await assertComputerHostedToolRuntimeMockMode(baseUrl, apiKey.key)
    await assertComputerHostedToolRuntimeLocalRuntimeMode(baseUrl, apiKey.key)
    await assertComputerHostedToolRuntimeHttpAdapterMode(baseUrl, apiKey.key)
    configureMockImageGenerationProvider(imageGenerationEndpoint)
    await assertResponsesImageGenerationJsonBridge(baseUrl, apiKey.key)
    await assertResponsesImageGenerationSseBridge(baseUrl, apiKey.key)
    await assertResponsesImageGenerationPartialImageSseBridge(baseUrl, apiKey.key)
    await assertResponsesImageGenerationModerationBlockedBridge(baseUrl, apiKey.key)
    await assertResponsesImageGenerationUnsupportedEditGuidance(baseUrl, apiKey.key)
    await assertResponsesImageGenerationHistoryContextGuidance(baseUrl, apiKey.key)
    await assertResponsesImageGenerationProviderInvalidResponseBridge(baseUrl, apiKey.key)
    configureMockImageGenerationProvider(imageGenerationEndpoint, { maxBodyBytes: 64 })
    await assertResponsesImageGenerationProviderTooLargeBridge(baseUrl, apiKey.key)
    configureMockImageGenerationProvider(imageGenerationEndpoint, { timeoutMs: 20 })
    await assertResponsesImageGenerationProviderTimeoutBridge(baseUrl, apiKey.key)
    configureMockImageGenerationProvider(imageGenerationEndpoint)
    configureMockImageGenerationProvider(imageGenerationResponsesEndpoint, { api: 'responses', model: 'gpt-image-2-chat-mock' })
    await assertResponsesImageGenerationResponsesProviderJsonBridge(baseUrl, apiKey.key)
    await assertResponsesImageGenerationResponsesProviderPartialImageSseBridge(baseUrl, apiKey.key)
    clearMockImageGenerationProvider()
    await assertResponsesImageFileIdNotFound(baseUrl, apiKey.key)
    await assertResponsesFileIdNotFound(baseUrl, apiKey.key)
    await assertResponsesImageFileIdResolverBridge(baseUrl, apiKey.key)
    await assertResponsesImageFileIdUnsupportedMimeRejected(baseUrl, apiKey.key)
    await assertResponsesImageFileIdInvalidBase64Rejected(baseUrl, apiKey.key)
    await assertResponsesFileIdResolverTextBridge(baseUrl, apiKey.key)
    await assertResponsesFileIdUnsupportedMimeRejected(baseUrl, apiKey.key)
    await assertResponsesFileIdInvalidBase64Rejected(baseUrl, apiKey.key)
    await assertChatFileIdNotFound(baseUrl, apiKey.key)
    await assertChatFileIdResolverTextBridge(baseUrl, apiKey.key)
    await assertChatFileIdUnsupportedMimeRejected(baseUrl, apiKey.key)
    await assertChatFileIdInvalidBase64Rejected(baseUrl, apiKey.key)
    await assertOpenAICompatibleFilesUploadResolverBridge(baseUrl, apiKey.key)
    await assertOpenAICompatibleVectorStoreFileSearchBridge(baseUrl, apiKey.key)
    await assertOpenAICompatibleFilesAndVectorStoreBoundaries(baseUrl, apiKey.key, apiKey.id, otherApiKey.key)
    await assertResponsesCompactionSummaryInputBridge(baseUrl, apiKey.key)
    await assertResponsesCompactionInvalidInputRejected(baseUrl, apiKey.key)
    await assertResponsesCompactEndpointBridge(baseUrl, apiKey.key, otherApiKey.key)
    await assertCodexPreviousResponseBridge(baseUrl, apiKey.key)
    await assertGenericResponsesPreviousResponseJsonBridge(baseUrl, apiKey.key, otherApiKey.key)
    await assertGenericResponsesPreviousResponseSseBridge(baseUrl, apiKey.key, otherApiKey.key)
    await assertGenericResponsesPreviousResponseMissingBridge(baseUrl, apiKey.key)

    console.log('openai anthropic bridge mock regression passed')
  } finally {
    clearMockImageGenerationProvider()
    await closeServer(appServer)
    await closeServer(imageGenerationServer)
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

function configureMockImageGenerationProvider(endpoint: string, options: {
  api?: 'images' | 'responses'
  model?: string
  timeoutMs?: number
  maxBodyBytes?: number
} = {}): void {
  runtimeConfig.imageGenerationProvider.endpoint = endpoint
  runtimeConfig.imageGenerationProvider.apiKey = 'sk-image-generation-mock'
  runtimeConfig.imageGenerationProvider.api = options.api ?? 'images'
  runtimeConfig.imageGenerationProvider.model = options.model ?? 'gpt-image-2-mock'
  runtimeConfig.imageGenerationProvider.timeoutMs = options.timeoutMs ?? 30000
  runtimeConfig.imageGenerationProvider.maxBodyBytes = options.maxBodyBytes ?? 1024 * 1024
}

function clearMockImageGenerationProvider(): void {
  runtimeConfig.imageGenerationProvider.endpoint = undefined
  runtimeConfig.imageGenerationProvider.apiKey = undefined
  runtimeConfig.imageGenerationProvider.api = 'images'
  runtimeConfig.imageGenerationProvider.model = 'gpt-image-2'
}

async function assertChatJsonBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'system', content: '只回答短句' },
        { role: 'user', content: 'chat json bridge' }
      ],
      response_format: { type: 'json_object' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat JSON 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { object?: string; choices?: Array<{ message?: { content?: string } }>; usage?: { prompt_tokens?: number } }
  assert.equal(body.object, 'chat.completion')
  assert.equal(body.choices?.[0]?.message?.content, '{"ok":true,"transport":"json"}')
  assert.equal(body.usage?.prompt_tokens, 13)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat json bridge')
  const upstreamMessages = upstreamHits[0]?.body.messages
  assert(Array.isArray(upstreamMessages), 'Anthropic 上游 messages 应为数组')
  assert.equal(upstreamMessages[0]?.content, 'chat json bridge', '纯文本 Chat 消息应使用 Anthropic 字符串 content，避免部分兼容上游卡住')
}

async function assertChatSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat sse bridge' }],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /"object":"chat\.completion\.chunk"/)
  assert.match(text, /"content":"bridge sse ok"/)
  assert.match(text, /data: \[DONE\]/)
  assert.equal(text.includes('event: content_block_delta'), false, '下游 Chat SSE 不应透出 Anthropic 原生事件名')
  assert.doesNotMatch(text, /"choices":\[\],"usage":\{/, '默认 Chat SSE 不应输出真实 usage chunk')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'chat sse bridge')
}

async function assertChatSseIncludeUsageBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat sse include_usage bridge' }],
      stream: true,
      stream_options: { include_usage: true }
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat SSE include_usage 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /"finish_reason":"stop"/)
  assert.match(text, /"choices":\[\],"usage":\{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11/)
  assert.match(text, /data: \[DONE\]/)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'chat sse include_usage bridge')
}

async function assertChatMultipleChoicesRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat n>1 unsupported bridge' }],
      n: 2,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat n>1 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/)
  assert.match(text, /openai_anthropic_bridge_multiple_choices_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat n>1 不应请求 Anthropic 上游')
}

async function assertOpenAIOutputShapeDefaultControlsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat output shape defaults bridge' }],
      n: 1,
      logprobs: false,
      top_logprobs: 0,
      modalities: ['text'],
      audio: null,
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat 默认输出形态控制应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat output shape defaults bridge')
  assert.equal(upstreamHits[0]?.body.n, undefined, 'Chat n=1 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.logprobs, undefined, 'Chat logprobs=false 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.top_logprobs, undefined, 'Chat top_logprobs=0 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.modalities, undefined, 'Chat modalities=[text] 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.audio, undefined, 'Chat audio=null 不应透传到 Anthropic')

  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses output shape defaults bridge',
      include: [],
      top_logprobs: 0,
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses 默认输出形态控制应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses output shape defaults bridge')
  assert.equal(upstreamHits[0]?.body.include, undefined, 'Responses include=[] 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.top_logprobs, undefined, 'Responses top_logprobs=0 不应透传到 Anthropic')
}

async function assertChatLogprobsRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat logprobs unsupported bridge' }],
      logprobs: true,
      top_logprobs: 2,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat logprobs 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/)
  assert.match(text, /openai_anthropic_bridge_logprobs_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat logprobs 不应请求 Anthropic 上游')
}

async function assertChatAudioOutputRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat audio output unsupported bridge' }],
      modalities: ['text', 'audio'],
      audio: { voice: 'alloy', format: 'wav' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat audio output 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/)
  assert.match(text, /openai_anthropic_bridge_output_modality_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat audio output 不应请求 Anthropic 上游')
}

async function assertChatInputAudioRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat input audio unsupported bridge' },
          { type: 'input_audio', input_audio: { data: 'UklGRg==', format: 'wav' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat input_audio 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/)
  assert.match(text, /openai_anthropic_bridge_audio_input_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat input_audio 不应请求 Anthropic 上游')
}

async function assertChatUnknownContentPartRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat unknown content unsupported bridge' },
          { type: 'custom_binary', value: 'opaque' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat 未知 content block 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_content_part/)
  assert.equal(upstreamHits.length, 0, 'Chat 未知 content block 不应请求 Anthropic 上游')
}

async function assertChatMultiToolSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat multi tool sse bridge' }],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat 多工具 SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"tool_calls":\[\{"index":0,"id":"toolu_multi_weather"/, 'Chat SSE 第一个工具 index 应从 0 开始')
  assert.match(text, /"tool_calls":\[\{"index":1,"id":"toolu_multi_news"/, 'Chat SSE 第二个工具 index 应连续为 1')
  assert.match(text, /"tool_calls":\[\{"index":0,"function":\{"arguments":"\{\\"city\\""/, 'Chat SSE 应透出第一个工具参数分片')
  assert.match(text, /"tool_calls":\[\{"index":1,"function":\{"arguments":"\{\\"topic\\""/, 'Chat SSE 应透出第二个工具参数分片')
  assert.match(text, /data: \[DONE\]/)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'chat multi tool sse bridge')
}

async function assertChatLegacyFunctionsSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat legacy functions sse bridge' }],
      functions: [{
        name: 'legacy_lookup',
        description: '旧版函数定义',
        parameters: {
          type: 'object',
          properties: { query: { type: 'string' } },
          required: ['query']
        }
      }],
      function_call: { name: 'legacy_lookup' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat legacy functions SSE 应正常桥接，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"function_call":\{"name":"legacy_lookup","arguments":""\}/, 'Chat legacy SSE 应输出 function_call.name 首片')
  assert.match(text, /"function_call":\{"arguments":"\{\\"query\\":/, 'Chat legacy SSE 应输出 function_call.arguments 参数分片')
  assert.match(text, /"function_call":\{"arguments":"\\"weather\\"\}"/, 'Chat legacy SSE 应输出完整后续参数分片')
  assert.match(text, /"finish_reason":"function_call"/, 'Chat legacy SSE 应使用 finish_reason=function_call')
  assert.match(text, /data: \[DONE\]/)
  assert.equal(text.includes('"tool_calls"'), false, 'Chat legacy SSE 不应暴露新版 tool_calls delta')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'chat legacy functions sse bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['legacy_lookup'], 'Chat legacy SSE functions 应转为 Anthropic tools')
  assert.deepEqual(
    upstreamHits[0]?.body.tool_choice,
    { type: 'tool', name: 'legacy_lookup', disable_parallel_tool_use: true },
    'Chat legacy SSE function_call 应转为 Anthropic tool_choice 并禁用并行工具调用'
  )
}

async function assertResponsesJsonBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      instructions: '只回答短句',
      input: 'responses json bridge',
      text: { format: { type: 'json_object' } },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses JSON 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { object?: string; status?: string; output_text?: string; usage?: { input_tokens?: number } }
  assert.equal(body.object, 'response')
  assert.equal(body.status, 'completed')
  assert.equal(body.output_text, '{"ok":true,"transport":"json"}')
  assert.equal(body.usage?.input_tokens, 13)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses json bridge')
}

async function assertOpenAISystemInstructionBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'system', content: 'chat system bridge guard' },
        { role: 'developer', content: 'chat developer bridge guard' },
        { role: 'user', name: 'user_alpha', content: 'chat named user bridge guard' },
        { role: 'assistant', name: 'assistant_beta', content: 'chat named assistant bridge guard' },
        { role: 'user', content: 'chat system instruction bridge' }
      ],
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat system/developer 应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat system instruction bridge')
  const chatSystem = String(upstreamHits[0]?.body.system ?? '')
  assert(chatSystem.includes('chat system bridge guard'), 'Chat system 应进入 Anthropic system')
  assert(chatSystem.includes('chat developer bridge guard'), 'Chat developer 应进入 Anthropic system')
  assert(chatSystem.indexOf('chat system bridge guard') < chatSystem.indexOf('chat developer bridge guard'), 'Chat system/developer 应保持输入顺序')
  const chatMessagesText = JSON.stringify(upstreamHits[0]?.body.messages ?? [])
  assert(!chatMessagesText.includes('chat system bridge guard'), 'Chat system 不应进入 Anthropic messages')
  assert(!chatMessagesText.includes('chat developer bridge guard'), 'Chat developer 不应进入 Anthropic messages')
  assert(chatMessagesText.includes('参与者: user_alpha'), 'Chat messages[].name 应以前缀保留 user 参与者')
  assert(chatMessagesText.includes('chat named user bridge guard'), 'Chat named user 原始内容应保留')
  assert(chatMessagesText.includes('参与者: assistant_beta'), 'Chat messages[].name 应以前缀保留 assistant 参与者')
  assert(chatMessagesText.includes('chat named assistant bridge guard'), 'Chat named assistant 原始内容应保留')
  assert(!/"name"\s*:/.test(chatMessagesText), 'Chat messages[].name 不应作为 Anthropic name 字段透传')

  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      instructions: 'responses instructions bridge guard',
      input: [
        {
          type: 'message',
          role: 'system',
          content: [{ type: 'input_text', text: 'responses input system bridge guard' }]
        },
        {
          type: 'message',
          role: 'developer',
          content: [{ type: 'input_text', text: 'responses input developer bridge guard' }]
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses system instruction bridge' }]
        }
      ],
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses instructions/system/developer 应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses system instruction bridge')
  const responsesSystem = String(upstreamHits[0]?.body.system ?? '')
  assert(responsesSystem.includes('responses instructions bridge guard'), 'Responses instructions 应进入 Anthropic system')
  assert(responsesSystem.includes('responses input system bridge guard'), 'Responses input system 应进入 Anthropic system')
  assert(responsesSystem.includes('responses input developer bridge guard'), 'Responses input developer 应进入 Anthropic system')
  assert(
    responsesSystem.indexOf('responses instructions bridge guard') < responsesSystem.indexOf('responses input system bridge guard')
      && responsesSystem.indexOf('responses input system bridge guard') < responsesSystem.indexOf('responses input developer bridge guard'),
    'Responses instructions/system/developer 应保持合并顺序'
  )
  const responsesMessagesText = JSON.stringify(upstreamHits[0]?.body.messages ?? [])
  assert(!responsesMessagesText.includes('responses instructions bridge guard'), 'Responses instructions 不应进入 Anthropic messages')
  assert(!responsesMessagesText.includes('responses input system bridge guard'), 'Responses input system 不应进入 Anthropic messages')
  assert(!responsesMessagesText.includes('responses input developer bridge guard'), 'Responses input developer 不应进入 Anthropic messages')
}

async function assertResponsesSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses sse bridge',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /event: response\.created/)
  assert.match(text, /event: response\.output_text\.delta/)
  assert.match(text, /bridge sse ok/)
  assert.match(text, /event: response\.completed/)
  assert.equal(text.includes('data: [DONE]'), false, 'Responses SSE 不应使用 Chat 的 [DONE] 结束语义')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses sse bridge')
}

async function assertResponsesLogprobsIncludeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses include output_text logprobs unsupported bridge',
      include: ['message.output_text.logprobs'],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses logprobs include 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_logprobs_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses logprobs include 不应请求 Anthropic 上游')
}

async function assertResponsesTopLogprobsRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses top_logprobs unsupported bridge',
      top_logprobs: 2,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses top_logprobs>0 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_logprobs_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses top_logprobs>0 不应请求 Anthropic 上游')
}

async function assertResponsesInputAudioRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses input audio unsupported bridge' },
          { type: 'input_audio', input_audio: { data: 'UklGRg==', format: 'wav' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses input_audio 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_audio_input_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses input_audio 不应请求 Anthropic 上游')
}

async function assertResponsesUnknownContentPartRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses unknown content unsupported bridge' },
          { type: 'custom_binary', value: 'opaque' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 未知 content block 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_content_part/)
  assert.equal(upstreamHits.length, 0, 'Responses 未知 content block 不应请求 Anthropic 上游')
}

async function assertOpenAISemanticDefaultControlsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat semantic defaults bridge' }],
      temperature: 1,
      top_p: 0.75,
      presence_penalty: 0,
      frequency_penalty: 0,
      logit_bias: {},
      seed: null,
      prediction: null,
      verbosity: null,
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat 默认语义控制应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat semantic defaults bridge')
  assert.equal(upstreamHits[0]?.body.temperature, 1, 'Chat temperature<=1 应映射到 Anthropic')
  assert.equal(upstreamHits[0]?.body.top_p, 0.75, 'Chat top_p 应映射到 Anthropic')
  assert.equal(upstreamHits[0]?.body.presence_penalty, undefined, 'Chat presence_penalty=0 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.frequency_penalty, undefined, 'Chat frequency_penalty=0 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.logit_bias, undefined, 'Chat 空 logit_bias 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.seed, undefined, 'Chat seed=null 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.prediction, undefined, 'Chat prediction=null 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.verbosity, undefined, 'Chat verbosity=null 不应透传到 Anthropic')

  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses semantic defaults bridge',
      temperature: 0.5,
      top_p: 0.9,
      presence_penalty: 0,
      frequency_penalty: 0,
      logit_bias: {},
      seed: null,
      prediction: null,
      text: { verbosity: null },
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses 默认语义控制应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses semantic defaults bridge')
  assert.equal(upstreamHits[0]?.body.temperature, 0.5, 'Responses temperature<=1 应映射到 Anthropic')
  assert.equal(upstreamHits[0]?.body.top_p, 0.9, 'Responses top_p 应映射到 Anthropic')
  assert.equal(upstreamHits[0]?.body.presence_penalty, undefined, 'Responses presence_penalty=0 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.frequency_penalty, undefined, 'Responses frequency_penalty=0 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.logit_bias, undefined, 'Responses 空 logit_bias 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.seed, undefined, 'Responses seed=null 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.prediction, undefined, 'Responses prediction=null 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.text, undefined, 'Responses text.verbosity=null 不应透传到 Anthropic')
}

async function assertOpenAIBasicGenerationControlsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat basic generation controls bridge' }],
      max_completion_tokens: 123,
      stop: '<END>',
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat 基础生成控制应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat basic generation controls bridge')
  assert.equal(upstreamHits[0]?.body.max_tokens, 123, 'Chat max_completion_tokens 应映射为 Anthropic max_tokens')
  assert.deepEqual(upstreamHits[0]?.body.stop_sequences, ['<END>'], 'Chat stop 字符串应映射为 Anthropic stop_sequences')
  assert.equal(upstreamHits[0]?.body.max_completion_tokens, undefined, 'Chat max_completion_tokens 不应原样透传')
  assert.equal(upstreamHits[0]?.body.stop, undefined, 'Chat stop 不应原样透传')

  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses basic generation controls bridge',
      max_output_tokens: 234,
      stop: ['DONE', 'HALT', '', 'CUT', 'DROP', 'IGNORED'],
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses 基础生成控制应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses basic generation controls bridge')
  assert.equal(upstreamHits[0]?.body.max_tokens, 234, 'Responses max_output_tokens 应映射为 Anthropic max_tokens')
  assert.deepEqual(upstreamHits[0]?.body.stop_sequences, ['DONE', 'HALT', 'CUT', 'DROP'], 'Responses stop 数组应映射为最多 4 个非空 stop_sequences')
  assert.equal(upstreamHits[0]?.body.max_output_tokens, undefined, 'Responses max_output_tokens 不应原样透传')
  assert.equal(upstreamHits[0]?.body.stop, undefined, 'Responses stop 不应原样透传')
}

async function assertOpenAISamplingControlsRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat temperature unsupported bridge' }],
      temperature: 1.5,
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_sampling_control_unsupported',
    label: 'Chat temperature>1'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat penalties unsupported bridge' }],
      presence_penalty: 0.25,
      frequency_penalty: 0.5,
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_sampling_control_unsupported',
    label: 'Chat penalty'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses logit bias unsupported bridge',
      logit_bias: { '42': -100 },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_sampling_control_unsupported',
    label: 'Responses logit_bias'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses seed unsupported bridge',
      seed: 1234,
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_sampling_control_unsupported',
    label: 'Responses seed'
  })
}

async function assertOpenAIPredictionRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat prediction unsupported bridge' }],
      prediction: { type: 'content', content: 'expected output' },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_prediction_unsupported',
    label: 'Chat prediction'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses prediction unsupported bridge',
      prediction: { type: 'content', content: 'expected output' },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_prediction_unsupported',
    label: 'Responses prediction'
  })
}

async function assertOpenAIVerbosityRejected(baseUrl: string, localApiKey: string): Promise<void> {
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat verbosity unsupported bridge' }],
      verbosity: 'high',
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_verbosity_unsupported',
    label: 'Chat verbosity'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses verbosity unsupported bridge',
      text: { verbosity: 'low' },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_verbosity_unsupported',
    label: 'Responses text.verbosity'
  })
}

async function assertOpenAIRequestControlBoundaries(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const allowedResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses request control allowed bridge',
      service_tier: 'default',
      safety_identifier: 'safe-user-123',
      user: 'fallback-user-should-not-win',
      metadata: { tenant: 'tenant-should-not-forward', trace: 'trace-should-not-forward' },
      prompt_cache_key: 'local-cache-affinity-only',
      stream: false
    })
  })
  const allowedText = await allowedResponse.text()
  assert.equal(allowedResponse.status, 200, `Responses 请求级默认控制字段应正常桥接，实际 HTTP ${allowedResponse.status}: ${allowedText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses request control allowed bridge')
  const upstreamMetadata = upstreamHits[0]?.body.metadata as Record<string, unknown> | undefined
  assert.equal(upstreamMetadata?.user_id, 'safe-user-123', 'safety_identifier 应映射为 Anthropic metadata.user_id')
  assert.equal(upstreamMetadata?.tenant, undefined, '业务 metadata.tenant 不应透传到 Anthropic metadata')
  assert.equal(upstreamMetadata?.trace, undefined, '业务 metadata.trace 不应透传到 Anthropic metadata')
  assert.equal(upstreamHits[0]?.body.user, undefined, 'OpenAI user 不应以顶层字段透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.prompt_cache_key, undefined, 'prompt_cache_key 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.service_tier, undefined, 'service_tier=default 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.safety_identifier, undefined, 'safety_identifier 不应以 OpenAI 字段透传到 Anthropic')
  assert.equal(upstreamMetadata?.user_id, 'safe-user-123', 'safety_identifier 应优先于 user')

  upstreamHits.length = 0
  const userFallbackResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat request control user fallback bridge' }],
      service_tier: 'auto',
      store: false,
      user: 'chat-user-fallback-456',
      metadata: { tenant: 'chat-tenant-should-not-forward' },
      prompt_cache_key: 'chat-local-cache-affinity-only',
      stream: false
    })
  })
  const userFallbackText = await userFallbackResponse.text()
  assert.equal(userFallbackResponse.status, 200, `Chat user fallback 应正常桥接，实际 HTTP ${userFallbackResponse.status}: ${userFallbackText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat request control user fallback bridge')
  const fallbackMetadata = upstreamHits[0]?.body.metadata as Record<string, unknown> | undefined
  assert.equal(fallbackMetadata?.user_id, 'chat-user-fallback-456', '缺少 safety_identifier 时应使用 OpenAI user 作为 Anthropic metadata.user_id')
  assert.equal(fallbackMetadata?.tenant, undefined, 'Chat 业务 metadata 不应透传到 Anthropic metadata')
  assert.equal(upstreamHits[0]?.body.user, undefined, 'Chat OpenAI user 不应以顶层字段透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.service_tier, undefined, 'Chat service_tier=auto 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.store, undefined, 'Chat store=false 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.prompt_cache_key, undefined, 'Chat prompt_cache_key 不应透传到 Anthropic')

  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat priority service tier unsupported bridge' }],
      service_tier: 'priority',
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_service_tier_unsupported',
    label: 'Chat service_tier=priority'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses flex service tier unsupported bridge',
      service_tier: 'flex',
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_service_tier_unsupported',
    label: 'Responses service_tier=flex'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses prompt cache retention unsupported bridge',
      prompt_cache_retention: '24h',
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_prompt_cache_retention_unsupported',
    label: 'Responses prompt_cache_retention'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat prompt cache retention unsupported bridge' }],
      prompt_cache_retention: '24h',
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_prompt_cache_retention_unsupported',
    label: 'Chat prompt_cache_retention'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat store unsupported bridge' }],
      store: true,
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_store_unsupported',
    label: 'Chat store=true'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses store unsupported bridge',
      store: true,
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_store_unsupported',
    label: 'Responses store=true'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses prompt template unsupported bridge',
      prompt: { id: 'pmpt_openai_anthropic_bridge', variables: { topic: 'bridge' } },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_prompt_template_unsupported',
    label: 'Responses prompt template'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat top level moderation unsupported bridge' }],
      moderation: { input: true, output: true },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_moderation_unsupported',
    label: 'Chat top-level moderation'
  })
  await assertLocalBridgeGuidance(baseUrl, localApiKey, {
    path: '/v1/responses',
    body: {
      model: 'gpt-5.5',
      input: 'responses top level moderation unsupported bridge',
      moderation: { input: true, output: true },
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_moderation_unsupported',
    label: 'Responses top-level moderation'
  })
}

async function assertLocalBridgeRejects(
  baseUrl: string,
  localApiKey: string,
  input: { path: '/v1/chat/completions' | '/v1/responses'; body: Record<string, unknown>; expectedCode: string; label: string }
): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}${input.path}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify(input.body)
  })
  const text = await response.text()
  assert.equal(response.status, 400, `${input.label} 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, new RegExp(input.expectedCode))
  assert.equal(upstreamHits.length, 0, `${input.label} 不应请求 Anthropic 上游`)
}

async function assertLocalBridgeGuidance(
  baseUrl: string,
  localApiKey: string,
  input: { path: '/v1/chat/completions' | '/v1/responses'; body: Record<string, unknown>; expectedCode: string; label: string }
): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}${input.path}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify(input.body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${input.label} 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, new RegExp(input.expectedCode))
  if (input.path === '/v1/responses') {
    assert.match(text, /"object":"response"/, `${input.label} guidance 应保持 Responses JSON 形态`)
    assert.match(text, /"status":"completed"/, `${input.label} guidance 应以 completed 结束`)
  } else {
    assert.match(text, /"object":"chat\.completion"/, `${input.label} guidance 应保持 Chat Completions JSON 形态`)
  }
  assert.equal(upstreamHits.length, 0, `${input.label} guidance 不应请求 Anthropic 上游`)
}

async function assertResponsesMultiToolSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses multi tool sse bridge',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses 多工具 SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.output_item\.added/)
  assert.match(text, /"type":"function_call","status":"in_progress","call_id":"toolu_multi_weather"/)
  assert.match(text, /"type":"function_call","status":"in_progress","call_id":"toolu_multi_news"/)
  const argumentDeltas = sseEventPayloads(text, 'response.function_call_arguments.delta')
  assert.deepEqual(
    argumentDeltas.map((payload) => payload.delta),
    ['{"city"', ':"Shanghai"}', '{"topic"', ':"weather"}'],
    `Responses 多工具 SSE 应逐段输出 function_call_arguments.delta: ${text}`
  )
  assert.deepEqual(
    aggregateArgumentDeltasByItem(argumentDeltas),
    {
      fc_toolu_multi_weather: '{"city":"Shanghai"}',
      fc_toolu_multi_news: '{"topic":"weather"}'
    },
    `Responses 多工具 SSE delta 聚合结果应等于最终 arguments: ${text}`
  )
  const argumentDonePayloads = sseEventPayloads(text, 'response.function_call_arguments.done')
  assert.deepEqual(
    argumentDonePayloads.map((payload) => (payload.item as Record<string, unknown> | undefined)?.arguments),
    ['{"city":"Shanghai"}', '{"topic":"weather"}'],
    `Responses 多工具 SSE 应输出 function_call_arguments.done: ${text}`
  )
  assert.match(text, /"type":"function_call","status":"completed","call_id":"toolu_multi_weather","name":"lookup_weather","arguments":"\{\\"city\\":\\"Shanghai\\"\}"/)
  assert.match(text, /"type":"function_call","status":"completed","call_id":"toolu_multi_news","name":"lookup_news","arguments":"\{\\"topic\\":\\"weather\\"\}"/)
  assert.match(text, /event: response\.completed/)
  assert.equal(text.includes('data: [DONE]'), false, 'Responses 多工具 SSE 不应使用 Chat 的 [DONE]')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses multi tool sse bridge')
}

async function assertBridgeSseErrorShape(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'trigger bridge sse error',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic SSE error 外层 HTTP 应保持流式响应，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.failed/)
  assert.match(text, /mock bridge stream overloaded/)
  assert.equal(text.includes('event: error'), false, 'Responses SSE 不应透出 Anthropic 原生 error 事件')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'trigger bridge sse error')
}

async function assertChatImageDataUrlBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat image data url bridge' },
          { type: 'image_url', image_url: { url: 'data:image/png;base64,aGVsbG8=' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat image data URL 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat image data url bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"image"/)
  assert.match(upstreamBodyText, /"source":\{"type":"base64","media_type":"image\/png","data":"aGVsbG8="/)
}

async function assertChatImageUrlBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat image url bridge' },
          { type: 'image_url', image_url: { url: 'https://example.com/chat-bridge.png' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat image URL 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat image url bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"image"/)
  assert.match(upstreamBodyText, /"source":\{"type":"url","url":"https:\/\/example\.com\/chat-bridge\.png"/)
}

async function assertResponsesImageUrlBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses image url bridge' },
          { type: 'input_image', image_url: 'https://example.com/bridge.png' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image URL 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image url bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"image"/)
  assert.match(upstreamBodyText, /"source":\{"type":"url","url":"https:\/\/example\.com\/bridge\.png"/)
}

async function assertResponsesImageDataUrlBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses image data url bridge' },
          { type: 'input_image', image_url: 'data:image/webp;base64,d2VicA==' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image data URL 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image data url bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"image"/)
  assert.match(upstreamBodyText, /"source":\{"type":"base64","media_type":"image\/webp","data":"d2VicA=="/)
}

async function assertChatImageDataUrlMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat image data url invalid mime' },
          { type: 'image_url', image_url: { url: 'data:text/html;base64,PGgxPk5vdCBhbiBpbWFnZTwvaDE+' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat 非图片 data URL 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_image_media_type/)
  assert.equal(upstreamHits.length, 0, 'Chat 非图片 data URL 不应请求 Anthropic 上游')
}

async function assertChatImageDataUrlBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat image data url invalid base64' },
          { type: 'image_url', image_url: { url: 'data:image/png;base64,not-valid-@@' } }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat 非法图片 base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_invalid_image_base64/)
  assert.equal(upstreamHits.length, 0, 'Chat 非法图片 base64 不应请求 Anthropic 上游')
}

async function assertResponsesImageDataUrlMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses image data url invalid mime' },
          { type: 'input_image', image_url: 'data:text/html;base64,PGgxPk5vdCBhbiBpbWFnZTwvaDE+' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 非图片 data URL 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_image_media_type/)
  assert.equal(upstreamHits.length, 0, 'Responses 非图片 data URL 不应请求 Anthropic 上游')
}

async function assertResponsesImageDataUrlBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses image data url invalid base64' },
          { type: 'input_image', image_url: 'data:image/png;base64,not-valid-@@' }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 非法图片 base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_invalid_image_base64/)
  assert.equal(upstreamHits.length, 0, 'Responses 非法图片 base64 不应请求 Anthropic 上游')
}

async function assertChatFileDataPdfBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const pdfPayload = Buffer.from('%PDF-1.7 mock chat bridge pdf').toString('base64')
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat file pdf bridge' },
          {
            type: 'file',
            file: {
              filename: 'bridge-chat.pdf',
              file_data: `data:application/pdf;base64,${pdfPayload}`
            }
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat file_data PDF 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat file pdf bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"document"/)
  assert.match(upstreamBodyText, /"source":\{"type":"base64","media_type":"application\/pdf"/)
  assert.match(upstreamBodyText, new RegExp(`"data":"${pdfPayload.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"`))
  assert.match(upstreamBodyText, /"title":"bridge-chat\.pdf"/)
}

async function assertResponsesFileDataTextBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const textPayload = Buffer.from('plain text fixture for responses file bridge', 'utf8').toString('base64')
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses file text bridge' },
          {
            type: 'input_file',
            filename: 'bridge-note.txt',
            file_data: `data:text/plain;base64,${textPayload}`
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses file_data text 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses file text bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"document"/)
  assert.match(upstreamBodyText, /"source":\{"type":"text","media_type":"text\/plain","data":"plain text fixture for responses file bridge"/)
  assert.match(upstreamBodyText, /"title":"bridge-note\.txt"/)
}

async function assertResponsesFileUrlPdfBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses file url pdf bridge' },
          {
            type: 'input_file',
            filename: 'bridge-url.pdf',
            file_url: 'https://example.com/bridge-url.pdf?download=1'
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses file_url PDF 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses file url pdf bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"document"/)
  assert.match(upstreamBodyText, /"source":\{"type":"url","url":"https:\/\/example\.com\/bridge-url\.pdf\?download=1"/)
  assert.match(upstreamBodyText, /"title":"bridge-url\.pdf"/)
}

async function assertChatFileDataInvalidBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat invalid file base64' },
          {
            type: 'file',
            file: {
              filename: 'broken.pdf',
              file_data: 'data:application/pdf;base64,not-valid-@@'
            }
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat 非法 file_data base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
  assert.equal(upstreamHits.length, 0, 'Chat 非法 file_data base64 不应请求 Anthropic 上游')
}

async function assertResponsesFileDataInvalidBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses invalid file base64' },
          {
            type: 'input_file',
            filename: 'broken.txt',
            file_data: 'data:text/plain;base64,not-valid-@@'
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 非法 file_data base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
  assert.equal(upstreamHits.length, 0, 'Responses 非法 file_data base64 不应请求 Anthropic 上游')
}

async function assertChatFileDataUnsupportedMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat unsupported file mime' },
          {
            type: 'file',
            file: {
              filename: 'payload.json',
              file_data: `data:application/json;base64,${Buffer.from('{"ok":true}', 'utf8').toString('base64')}`
            }
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat 不支持的 file_data MIME 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
  assert.equal(upstreamHits.length, 0, 'Chat 不支持的 file_data MIME 不应请求 Anthropic 上游')
}

async function assertResponsesFileDataUnsupportedMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses unsupported file mime' },
          {
            type: 'input_file',
            filename: 'payload.json',
            file_data: `data:application/json;base64,${Buffer.from('{"ok":true}', 'utf8').toString('base64')}`
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 不支持的 file_data MIME 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
  assert.equal(upstreamHits.length, 0, 'Responses 不支持的 file_data MIME 不应请求 Anthropic 上游')
}

async function assertChatFileUrlRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat file url rejected' },
          {
            type: 'file',
            file: {
              filename: 'bridge-url.pdf',
              file_url: 'https://example.com/bridge-url.pdf'
            }
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat file_url 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
  assert.equal(upstreamHits.length, 0, 'Chat file_url 不应请求 Anthropic 上游')
}

async function assertResponsesFileUrlUnsupportedRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses file url unsupported' },
          {
            type: 'input_file',
            filename: 'bridge-url.txt',
            file_url: 'https://example.com/bridge-url.txt'
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses 非 PDF file_url 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
  assert.equal(upstreamHits.length, 0, 'Responses 非 PDF file_url 不应请求 Anthropic 上游')
}

async function assertChatToolCallBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat tool bridge' }],
      tools: [{
        type: 'function',
        function: {
          name: 'lookup_weather',
          description: '查询城市天气',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        }
      }],
      tool_choice: { type: 'function', function: { name: 'lookup_weather' } },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat 工具调用桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    choices?: Array<{
      finish_reason?: string
      message?: {
        content?: string | null
        tool_calls?: Array<{ id?: string; type?: string; function?: { name?: string; arguments?: string } }>
      }
    }>
  }
  const toolCall = body.choices?.[0]?.message?.tool_calls?.[0]
  assert.equal(body.choices?.[0]?.finish_reason, 'tool_calls')
  assert.equal(body.choices?.[0]?.message?.content, null)
  assert.equal(toolCall?.id, 'toolu_bridge_lookup')
  assert.equal(toolCall?.type, 'function')
  assert.equal(toolCall?.function?.name, 'lookup_weather')
  assert.deepEqual(JSON.parse(toolCall?.function?.arguments ?? '{}'), { city: 'Shanghai' })
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat tool bridge')
  assert.equal(upstreamToolNames(upstreamHits[0])[0], 'lookup_weather')
}

async function assertChatLegacyFunctionsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const legacyResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat legacy functions bridge' }],
      functions: [{
        name: 'legacy_lookup',
        description: '旧版函数定义',
        parameters: {
          type: 'object',
          properties: { query: { type: 'string' } },
          required: ['query']
        }
      }],
      function_call: { name: 'legacy_lookup' },
      stream: false
    })
  })
  const legacyText = await legacyResponse.text()
  assert.equal(legacyResponse.status, 200, `Chat legacy functions/function_call 应正常桥接，实际 HTTP ${legacyResponse.status}: ${legacyText}`)
  const legacyBody = JSON.parse(legacyText) as {
    choices?: Array<{
      finish_reason?: string
      message?: {
        content?: string | null
        function_call?: { name?: string; arguments?: string }
        tool_calls?: unknown
      }
    }>
  }
  const legacyMessage = legacyBody.choices?.[0]?.message
  assert.equal(legacyBody.choices?.[0]?.finish_reason, 'function_call', 'Chat legacy JSON 工具回包应恢复 finish_reason=function_call')
  assert.equal(legacyMessage?.content, null, 'Chat legacy JSON 工具回包 content 应为 null')
  assert.equal(legacyMessage?.function_call?.name, 'legacy_lookup', 'Chat legacy JSON 工具回包应恢复 message.function_call.name')
  assert.deepEqual(
    JSON.parse(legacyMessage?.function_call?.arguments ?? '{}'),
    { query: 'weather' },
    'Chat legacy JSON 工具回包应恢复 message.function_call.arguments'
  )
  assert.equal(legacyMessage?.tool_calls, undefined, 'Chat legacy JSON 工具回包不应暴露新版 tool_calls')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat legacy functions bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['legacy_lookup'], 'Chat legacy functions 应转为 Anthropic tools')
  assert.deepEqual(
    upstreamHits[0]?.body.tool_choice,
    { type: 'tool', name: 'legacy_lookup', disable_parallel_tool_use: true },
    'Chat legacy function_call 应转为 Anthropic tool_choice 并禁用并行工具调用'
  )
  assert.equal(upstreamHits[0]?.body.functions, undefined, 'Chat legacy functions 不应原样透传')
  assert.equal(upstreamHits[0]?.body.function_call, undefined, 'Chat legacy function_call 不应原样透传')

  upstreamHits.length = 0
  const priorityResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat legacy functions new fields priority bridge' }],
      functions: [{
        name: 'legacy_ignored',
        description: '旧版函数应被新版 tools 覆盖',
        parameters: { type: 'object', properties: {} }
      }],
      function_call: { name: 'legacy_ignored' },
      tools: [{
        type: 'function',
        function: {
          name: 'modern_lookup',
          description: '新版函数定义',
          parameters: {
            type: 'object',
            properties: { query: { type: 'string' } },
            required: ['query']
          }
        }
      }],
      tool_choice: { type: 'function', function: { name: 'modern_lookup' } },
      stream: false
    })
  })
  const priorityText = await priorityResponse.text()
  assert.equal(priorityResponse.status, 200, `Chat 新旧工具字段同时存在时应新版优先，实际 HTTP ${priorityResponse.status}: ${priorityText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat legacy functions new fields priority bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['modern_lookup'], 'Chat tools 应优先于 legacy functions')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'tool', name: 'modern_lookup' }, 'Chat tool_choice 应优先于 legacy function_call')
  assert.equal(JSON.stringify(upstreamHits[0]?.body ?? {}).includes('legacy_ignored'), false, '新版字段存在时不应把 legacy functions/function_call 发给 Anthropic')
}

async function assertResponsesFunctionCallBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses tool bridge',
      tools: [{
        type: 'function',
        name: 'lookup_weather',
        description: '查询城市天气',
        parameters: {
          type: 'object',
          properties: { city: { type: 'string' } },
          required: ['city']
        }
      }],
      tool_choice: { type: 'function', name: 'lookup_weather' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses 工具调用桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    output?: Array<{ type?: string; call_id?: string; name?: string; arguments?: string; status?: string }>
  }
  const functionCall = body.output?.find((item) => item.type === 'function_call')
  assert.equal(functionCall?.status, 'completed')
  assert.equal(functionCall?.call_id, 'toolu_bridge_lookup')
  assert.equal(functionCall?.name, 'lookup_weather')
  assert.deepEqual(JSON.parse(functionCall?.arguments ?? '{}'), { city: 'Shanghai' })
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses tool bridge')
  assert.equal(upstreamToolNames(upstreamHits[0])[0], 'lookup_weather')
}

async function assertResponsesAllowedFunctionToolsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses allowed tools bridge',
      tools: [
        {
          type: 'function',
          name: 'lookup_weather',
          description: '查询城市天气',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        },
        {
          type: 'function',
          name: 'lookup_news',
          description: '查询城市新闻',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        }
      ],
      tool_choice: {
        type: 'allowed_tools',
        mode: 'required',
        tools: [{ type: 'function', name: 'lookup_weather' }]
      },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses allowed_tools 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses allowed tools bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['lookup_weather'], 'allowed_tools 只应发送允许的 function tool')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'any' }, 'allowed_tools required 应映射为 Anthropic any')
}

async function assertOpenAIToolControlDefaultBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat tool control defaults bridge' }],
      tools: [{
        type: 'function',
        function: {
          name: 'lookup_weather',
          description: '查询城市天气',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        }
      }],
      tool_choice: 'auto',
      parallel_tool_calls: true,
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat 工具控制默认值应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat tool control defaults bridge')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'auto' }, 'Chat parallel_tool_calls=true 不应禁用 Anthropic 并行工具')
  assert.equal(upstreamHits[0]?.body.parallel_tool_calls, undefined, 'Chat parallel_tool_calls 不应以 OpenAI 字段透传')

  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses tool control defaults bridge',
      tools: [{
        type: 'function',
        name: 'lookup_weather',
        description: '查询城市天气',
        parameters: {
          type: 'object',
          properties: { city: { type: 'string' } },
          required: ['city']
        }
      }],
      tool_choice: 'auto',
      parallel_tool_calls: null,
      max_tool_calls: null,
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses 工具控制默认值应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses tool control defaults bridge')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'auto' }, 'Responses parallel_tool_calls=null 不应禁用 Anthropic 并行工具')
  assert.equal(upstreamHits[0]?.body.parallel_tool_calls, undefined, 'Responses parallel_tool_calls 不应以 OpenAI 字段透传')
  assert.equal(upstreamHits[0]?.body.max_tool_calls, undefined, 'Responses max_tool_calls=null 不应透传到 Anthropic')
}

async function assertResponsesToolSearchNamespaceBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses tool search namespace bridge',
      tools: [
        {
          type: 'namespace',
          name: 'crm',
          description: 'CRM tools for order management.',
          tools: [{
            type: 'function',
            name: 'list_open_orders',
            description: 'List open orders for a customer.',
            defer_loading: true,
            parameters: {
              type: 'object',
              properties: { customer_id: { type: 'string' } },
              required: ['customer_id'],
              additionalProperties: false
            }
          }]
        },
        { type: 'tool_search' }
      ],
      tool_choice: { type: 'function', name: 'list_open_orders', namespace: 'crm' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses tool_search namespace 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    output?: Array<{ type?: string; call_id?: string; name?: string; namespace?: string; arguments?: string; status?: string }>
  }
  const functionCall = body.output?.find((item) => item.type === 'function_call')
  assert.equal(functionCall?.status, 'completed')
  assert.equal(functionCall?.call_id, 'toolu_bridge_lookup')
  assert.equal(functionCall?.name, 'list_open_orders')
  assert.equal(functionCall?.namespace, 'crm')
  assert.deepEqual(JSON.parse(functionCall?.arguments ?? '{}'), { customer_id: 'CUST-12345' })
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses tool search namespace bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['crm__list_open_orders'])
  assert.deepEqual(
    upstreamHits[0]?.body.tool_choice,
    { type: 'tool', name: 'crm__list_open_orders' },
    'namespace function tool_choice 应映射为展开后的 Anthropic tool name'
  )
}

async function assertResponsesToolSearchNamespaceSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses tool search namespace sse bridge',
      tools: [
        {
          type: 'namespace',
          name: 'crm',
          description: 'CRM tools for order management.',
          tools: [{
            type: 'function',
            name: 'list_open_orders',
            description: 'List open orders for a customer.',
            defer_loading: true,
            parameters: {
              type: 'object',
              properties: { customer_id: { type: 'string' } },
              required: ['customer_id'],
              additionalProperties: false
            }
          }]
        },
        { type: 'tool_search' }
      ],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses tool_search namespace SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.output_item\.added/)
  assert.match(text, /"type":"function_call","status":"in_progress","call_id":"toolu_namespace_open_orders","name":"list_open_orders","arguments":"","namespace":"crm"/)
  assert.match(text, /"type":"function_call","status":"completed","call_id":"toolu_namespace_open_orders","name":"list_open_orders","arguments":"\{\\"customer_id\\":\\"CUST-12345\\"\}","namespace":"crm"/)
  assert.match(text, /event: response\.completed/)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses tool search namespace sse bridge')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['crm__list_open_orders'])
}

async function assertChatParallelToolCallsDisabledBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat parallel tool calls disabled bridge' }],
      tools: [{
        type: 'function',
        function: {
          name: 'lookup_weather',
          description: '查询城市天气',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        }
      }],
      parallel_tool_calls: false,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat parallel_tool_calls=false 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat parallel tool calls disabled bridge')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, {
    type: 'auto',
    disable_parallel_tool_use: true
  })
}

async function assertResponsesParallelToolCallsDisabledBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses parallel tool calls disabled bridge',
      tools: [{
        type: 'function',
        name: 'lookup_weather',
        description: '查询城市天气',
        parameters: {
          type: 'object',
          properties: { city: { type: 'string' } },
          required: ['city']
        }
      }],
      tool_choice: 'required',
      parallel_tool_calls: false,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses parallel_tool_calls=false 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses parallel tool calls disabled bridge')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, {
    type: 'any',
    disable_parallel_tool_use: true
  })
}

async function assertChatParallelToolCallsDisabledWithNoneBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat parallel tool calls none bridge' }],
      tools: [{
        type: 'function',
        function: {
          name: 'lookup_weather',
          description: '查询城市天气',
          parameters: {
            type: 'object',
            properties: { city: { type: 'string' } },
            required: ['city']
          }
        }
      }],
      tool_choice: 'none',
      parallel_tool_calls: false,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat tool_choice=none + parallel_tool_calls=false 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat parallel tool calls none bridge')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'none' })
}

async function assertResponsesMaxToolCallsRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses max tool calls unsupported',
      tools: [{
        type: 'function',
        name: 'lookup_weather',
        description: '查询城市天气',
        parameters: {
          type: 'object',
          properties: { city: { type: 'string' } },
          required: ['city']
        }
      }],
      max_tool_calls: 1,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses max_tool_calls 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_max_tool_calls_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses max_tool_calls 不应请求 Anthropic 上游')
}

async function assertResponsesStateControlBoundaries(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const allowedResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses state control allowed bridge',
      conversation: null,
      background: false,
      store: false,
      truncation: 'disabled',
      context_management: null,
      stream: false
    })
  })
  const allowedText = await allowedResponse.text()
  assert.equal(allowedResponse.status, 200, `Responses background=false + store=false + truncation=disabled 应正常桥接，实际 HTTP ${allowedResponse.status}: ${allowedText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses state control allowed bridge')
  assert.equal(upstreamHits[0]?.body.conversation, undefined, 'conversation=null 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.background, undefined, 'background=false 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.store, undefined, 'store=false 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.truncation, undefined, 'truncation=disabled 不应透传到 Anthropic')
  assert.equal(upstreamHits[0]?.body.context_management, undefined, 'context_management=null 不应透传到 Anthropic')

  await assertResponsesStateControlRejected(baseUrl, localApiKey, {
    conversation: 'conv_openai_anthropic_bridge'
  }, 'openai_anthropic_bridge_conversation_unsupported')
  await assertResponsesStateControlRejected(baseUrl, localApiKey, {
    background: true
  }, 'openai_anthropic_bridge_background_unsupported')
  await assertResponsesStateControlRejected(baseUrl, localApiKey, {
    store: true
  }, 'openai_anthropic_bridge_store_unsupported')
  await assertResponsesStateControlRejected(baseUrl, localApiKey, {
    truncation: 'auto'
  }, 'openai_anthropic_bridge_truncation_unsupported')
  await assertResponsesStateControlRejected(baseUrl, localApiKey, {
    context_management: {}
  }, 'openai_anthropic_bridge_context_management_unsupported')
}

async function assertResponsesStateControlRejected(
  baseUrl: string,
  localApiKey: string,
  extraBody: Record<string, unknown>,
  expectedCode: string
): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: `responses state control rejected ${expectedCode}`,
      stream: false,
      ...extraBody
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses 状态 / 上下文控制 ${expectedCode} 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, new RegExp(expectedCode))
  assert.equal(upstreamHits.length, 0, `Responses 状态 / 上下文控制 ${expectedCode} 不应请求 Anthropic 上游`)
}

async function assertResponsesThinkingForcedToolChoiceRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking forced tool choice',
      reasoning: { effort: 'low' },
      tools: [{
        type: 'function',
        name: 'lookup_weather',
        description: '查询城市天气',
        parameters: {
          type: 'object',
          properties: { city: { type: 'string' } },
          required: ['city']
        }
      }],
      tool_choice: 'required',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning + forced tool_choice 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_thinking_forced_tool_choice_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses reasoning + forced tool_choice 不应请求 Anthropic 上游')
}

async function assertChatToolResultBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat tool result bridge' },
        {
          role: 'assistant',
          content: null,
          tool_calls: [{
            id: 'call_bridge_result',
            type: 'function',
            function: { name: 'lookup_weather', arguments: '{"city":"Shanghai"}' }
          }]
        },
        { role: 'tool', tool_call_id: 'call_bridge_result', content: 'sunny' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat tool_result 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat tool result bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"tool_use"/, 'Chat assistant tool_calls 应转成 Anthropic tool_use')
  assert.match(upstreamBodyText, /"type":"tool_result"/, 'Chat role=tool 应转成 Anthropic tool_result')
  assert.match(upstreamBodyText, /"tool_use_id":"call_bridge_result"/, 'Chat tool_result 应保留 tool_call_id')
  assert.match(upstreamBodyText, /sunny/, 'Chat tool_result 应保留工具输出内容')
}

async function assertChatLegacyFunctionHistoryBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat legacy function history bridge' },
        {
          role: 'assistant',
          content: null,
          function_call: { name: 'legacy_lookup', arguments: '{"query":"weather"}' }
        },
        { role: 'function', name: 'legacy_lookup', content: 'legacy weather result' },
        { role: 'user', content: 'continue after legacy function result' }
      ],
      functions: [{
        name: 'legacy_lookup',
        description: '旧版函数定义',
        parameters: {
          type: 'object',
          properties: { query: { type: 'string' } },
          required: ['query']
        }
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat legacy function 历史桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat legacy function history bridge')
  const requestMessages = Array.isArray(upstreamHits[0]?.body.messages) ? upstreamHits[0]?.body.messages : []
  const assistantMessage = requestMessages.find((item) => isRecord(item) && item.role === 'assistant') as Record<string, unknown> | undefined
  const assistantContent = Array.isArray(assistantMessage?.content) ? assistantMessage.content : []
  const toolUse = assistantContent.find((item) => isRecord(item) && item.type === 'tool_use') as Record<string, unknown> | undefined
  assert.equal(toolUse?.name, 'legacy_lookup', 'Chat legacy assistant.function_call 应转成 Anthropic tool_use')
  assert.equal(String(toolUse?.id ?? '').startsWith('legacy_function_call_1_legacy_lookup'), true, 'Chat legacy function_call 应生成稳定合成 call id')
  assert.deepEqual(toolUse?.input, { query: 'weather' }, 'Chat legacy function_call arguments 应保真进入 Anthropic input')
  const userMessages = requestMessages.filter((item) => isRecord(item) && item.role === 'user') as Array<Record<string, unknown>>
  const toolResultBlock = userMessages
    .flatMap((item) => Array.isArray(item.content) ? item.content : [])
    .find((item) => isRecord(item) && item.type === 'tool_result') as Record<string, unknown> | undefined
  assert.equal(toolResultBlock?.tool_use_id, toolUse?.id, 'Chat legacy role=function 应匹配 assistant.function_call 合成 id')
  assert.equal(toolResultBlock?.content, 'legacy weather result', 'Chat legacy role=function content 应保留为 tool_result')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.equal(upstreamBodyText.includes('"role":"function"'), false, 'Chat legacy role=function 不应原样透传')
  assert.equal(upstreamBodyText.includes('"function_call"'), false, 'Chat legacy assistant.function_call 不应原样透传')

  await assertLocalBridgeRejects(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat legacy function missing name bridge' },
        { role: 'function', content: 'missing name result' }
      ],
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_tool_result_missing_call_id',
    label: 'Chat legacy role=function missing name'
  })
  await assertLocalBridgeRejects(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat legacy function orphan bridge' },
        { role: 'function', name: 'legacy_lookup', content: 'orphan result' }
      ],
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_orphan_tool_result',
    label: 'Chat legacy orphan role=function'
  })
  await assertLocalBridgeRejects(baseUrl, localApiKey, {
    path: '/v1/chat/completions',
    body: {
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat legacy function duplicate bridge' },
        {
          role: 'assistant',
          content: null,
          function_call: { name: 'legacy_lookup', arguments: '{"query":"weather"}' }
        },
        { role: 'function', name: 'legacy_lookup', content: 'first result' },
        { role: 'function', name: 'legacy_lookup', content: 'duplicate result' }
      ],
      stream: false
    },
    expectedCode: 'openai_anthropic_bridge_duplicate_tool_result',
    label: 'Chat legacy duplicate role=function'
  })
}

async function assertChatMultiToolHistoryOrderBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat multi tool history order bridge' },
        {
          role: 'assistant',
          content: null,
          tool_calls: [
            {
              id: 'call_weather_order',
              type: 'function',
              function: { name: 'lookup_weather', arguments: '{"city":"Shanghai"}' }
            },
            {
              id: 'call_news_order',
              type: 'function',
              function: { name: 'lookup_news', arguments: '{"topic":"weather"}' }
            }
          ]
        },
        { role: 'tool', tool_call_id: 'call_weather_order', content: 'weather result' },
        { role: 'tool', tool_call_id: 'call_news_order', content: 'news result' },
        { role: 'user', content: 'continue after ordered tool results' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat 多工具历史顺序桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat multi tool history order bridge')
  const requestMessages = Array.isArray(upstreamHits[0]?.body.messages) ? upstreamHits[0]?.body.messages : []
  const assistantMessage = requestMessages.find((item) => isRecord(item) && item.role === 'assistant') as Record<string, unknown> | undefined
  const assistantContent = Array.isArray(assistantMessage?.content) ? assistantMessage.content : []
  const toolUseBlocks = assistantContent.filter((item) => isRecord(item) && item.type === 'tool_use') as Array<Record<string, unknown>>
  assert.deepEqual(
    toolUseBlocks.map((item) => item.id),
    ['call_weather_order', 'call_news_order'],
    'Chat 多个 tool_calls 应按客户端数组顺序转 Anthropic tool_use'
  )
  assert.deepEqual(
    toolUseBlocks.map((item) => item.name),
    ['lookup_weather', 'lookup_news'],
    'Chat 多个 tool_calls 应保留各自 function name'
  )
  assert.deepEqual(
    toolUseBlocks.map((item) => item.input),
    [{ city: 'Shanghai' }, { topic: 'weather' }],
    'Chat 多个 tool_calls 应保留各自 arguments'
  )

  const userMessages = requestMessages.filter((item) => isRecord(item) && item.role === 'user') as Array<Record<string, unknown>>
  const toolResultMessage = userMessages.find((item) => Array.isArray(item.content)
    && item.content.some((block) => isRecord(block) && block.type === 'tool_result')) as Record<string, unknown> | undefined
  const toolResultContent = Array.isArray(toolResultMessage?.content) ? toolResultMessage.content : []
  const orderedBlocks = toolResultContent.filter((item) => isRecord(item)) as Array<Record<string, unknown>>
  assert.deepEqual(
    orderedBlocks.map((item) => item.type),
    ['tool_result', 'tool_result', 'text'],
    'Chat 多个 tool_result 和后续用户文本应按消息顺序进入同一个 Anthropic user content'
  )
  assert.deepEqual(
    orderedBlocks.map((item) => item.type === 'tool_result' ? item.tool_use_id : item.text),
    ['call_weather_order', 'call_news_order', 'continue after ordered tool results'],
    'Chat 多个 tool_result 应保持 tool_call_id 顺序并让后续用户文本留在最后'
  )
}

async function assertResponsesFunctionOutputBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'function_call',
          call_id: 'call_response_result',
          name: 'lookup_weather',
          arguments: '{"city":"Shanghai"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_response_result',
          output: 'sunny'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses function output bridge' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses function_call_output 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses function output bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"type":"tool_use"/, 'Responses function_call 应转成 Anthropic tool_use')
  assert.match(upstreamBodyText, /"type":"tool_result"/, 'Responses function_call_output 应转成 Anthropic tool_result')
  assert.match(upstreamBodyText, /"tool_use_id":"call_response_result"/, 'Responses function_call_output 应保留 call_id')
  assert.match(upstreamBodyText, /sunny/, 'Responses function_call_output 应保留工具输出内容')
}

async function assertResponsesMultiFunctionHistoryOrderBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses multi function history order bridge' }]
        },
        {
          type: 'function_call',
          call_id: 'call_weather_response_order',
          name: 'lookup_weather',
          arguments: '{"city":"Shanghai"}'
        },
        {
          type: 'function_call',
          call_id: 'call_news_response_order',
          name: 'lookup_news',
          arguments: '{"topic":"weather"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_weather_response_order',
          output: 'weather result'
        },
        {
          type: 'function_call_output',
          call_id: 'call_news_response_order',
          output: 'news result'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'continue after ordered response tool results' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses 多工具历史顺序桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses multi function history order bridge')
  const requestMessages = Array.isArray(upstreamHits[0]?.body.messages) ? upstreamHits[0]?.body.messages : []
  const assistantMessage = requestMessages.find((item) => isRecord(item) && item.role === 'assistant') as Record<string, unknown> | undefined
  const assistantContent = Array.isArray(assistantMessage?.content) ? assistantMessage.content : []
  const toolUseBlocks = assistantContent.filter((item) => isRecord(item) && item.type === 'tool_use') as Array<Record<string, unknown>>
  assert.deepEqual(
    toolUseBlocks.map((item) => item.id),
    ['call_weather_response_order', 'call_news_response_order'],
    'Responses 多个 function_call 应按 input 数组顺序转 Anthropic tool_use'
  )
  assert.deepEqual(
    toolUseBlocks.map((item) => item.name),
    ['lookup_weather', 'lookup_news'],
    'Responses 多个 function_call 应保留各自 name'
  )
  assert.deepEqual(
    toolUseBlocks.map((item) => item.input),
    [{ city: 'Shanghai' }, { topic: 'weather' }],
    'Responses 多个 function_call 应保留各自 arguments'
  )

  const userMessages = requestMessages.filter((item) => isRecord(item) && item.role === 'user') as Array<Record<string, unknown>>
  const toolResultMessage = userMessages.find((item) => Array.isArray(item.content)
    && item.content.some((block) => isRecord(block) && block.type === 'tool_result')) as Record<string, unknown> | undefined
  const toolResultContent = Array.isArray(toolResultMessage?.content) ? toolResultMessage.content : []
  const orderedBlocks = toolResultContent.filter((item) => isRecord(item)) as Array<Record<string, unknown>>
  assert.deepEqual(
    orderedBlocks.map((item) => item.type),
    ['tool_result', 'tool_result', 'text'],
    'Responses 多个 function_call_output 和后续用户文本应按 input 顺序进入同一个 Anthropic user content'
  )
  assert.deepEqual(
    orderedBlocks.map((item) => item.type === 'tool_result' ? item.tool_use_id : item.text),
    ['call_weather_response_order', 'call_news_response_order', 'continue after ordered response tool results'],
    'Responses 多个 function_call_output 应保持 call_id 顺序并让后续用户文本留在最后'
  )
}

async function assertChatMissingToolResultCallIdRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat missing tool result call id' },
        { role: 'tool', content: 'sunny' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat tool_result 缺少 tool_call_id 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_tool_result_missing_call_id/)
  assert.equal(upstreamHits.length, 0, 'Chat tool_result 缺少 tool_call_id 不应请求 Anthropic 上游')
}

async function assertResponsesMissingFunctionOutputCallIdRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'function_call_output',
          output: 'sunny'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses missing function output call id' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses function_call_output 缺少 call_id 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_tool_result_missing_call_id/)
  assert.equal(upstreamHits.length, 0, 'Responses function_call_output 缺少 call_id 不应请求 Anthropic 上游')
}

async function assertChatOrphanToolResultRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat orphan tool result' },
        { role: 'tool', tool_call_id: 'missing_call', content: 'sunny' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat orphan tool_result 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_orphan_tool_result/)
  assert.equal(upstreamHits.length, 0, 'Chat orphan tool_result 不应请求 Anthropic 上游')
}

async function assertResponsesOrphanFunctionOutputRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'function_call_output',
          call_id: 'missing_call',
          output: 'sunny'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses orphan function output' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses orphan function_call_output 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_orphan_tool_result/)
  assert.equal(upstreamHits.length, 0, 'Responses orphan function_call_output 不应请求 Anthropic 上游')
}

async function assertChatDuplicateToolResultRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat duplicate tool result' },
        {
          role: 'assistant',
          content: null,
          tool_calls: [{
            id: 'call_duplicate_result',
            type: 'function',
            function: { name: 'lookup_weather', arguments: '{"city":"Shanghai"}' }
          }]
        },
        { role: 'tool', tool_call_id: 'call_duplicate_result', content: 'sunny' },
        { role: 'tool', tool_call_id: 'call_duplicate_result', content: 'sunny again' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Chat duplicate tool_result 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_duplicate_tool_result/)
  assert.equal(upstreamHits.length, 0, 'Chat duplicate tool_result 不应请求 Anthropic 上游')
}

async function assertResponsesDuplicateFunctionOutputRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'function_call',
          call_id: 'call_duplicate_response',
          name: 'lookup_weather',
          arguments: '{"city":"Shanghai"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_duplicate_response',
          output: 'sunny'
        },
        {
          type: 'function_call_output',
          call_id: 'call_duplicate_response',
          output: 'sunny again'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses duplicate function output' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses duplicate function_call_output 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_duplicate_tool_result/)
  assert.equal(upstreamHits.length, 0, 'Responses duplicate function_call_output 不应请求 Anthropic 上游')
}

async function assertChatFunctionArgumentsPreservedBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [
        { role: 'user', content: 'chat function arguments history' },
        {
          role: 'assistant',
          content: null,
          tool_calls: [
            {
              id: 'call_object_arguments',
              type: 'function',
              function: { name: 'lookup_weather', arguments: '{"city":"Shanghai"}' }
            },
            {
              id: 'call_array_arguments',
              type: 'function',
              function: { name: 'lookup_weather', arguments: '["Shanghai"]' }
            },
            {
              id: 'call_scalar_arguments',
              type: 'function',
              function: { name: 'lookup_weather', arguments: '42' }
            },
            {
              id: 'call_invalid_arguments',
              type: 'function',
              function: { name: 'lookup_weather', arguments: 'not-json' }
            }
          ]
        },
        { role: 'tool', tool_call_id: 'call_object_arguments', content: 'sunny' },
        { role: 'tool', tool_call_id: 'call_array_arguments', content: 'sunny' },
        { role: 'tool', tool_call_id: 'call_scalar_arguments', content: 'sunny' },
        { role: 'tool', tool_call_id: 'call_invalid_arguments', content: 'sunny' },
        { role: 'user', content: 'chat function arguments preservation bridge' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat arguments 保真桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat function arguments preservation bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"input":\{"city":"Shanghai"\}/, 'Chat JSON object arguments 应原样进入 Anthropic input')
  assert.match(upstreamBodyText, /"openai_arguments":\["Shanghai"\]/, 'Chat JSON 非对象 arguments 不应被吞成空对象')
  assert.match(upstreamBodyText, /"openai_arguments":42/, 'Chat JSON 标量 arguments 不应被吞成空对象')
  assert.match(upstreamBodyText, /"openai_arguments_text":"not-json"/, 'Chat 非法 JSON arguments 不应被吞成空对象')
}

async function assertResponsesFunctionArgumentsPreservedBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'function_call',
          call_id: 'call_object_arguments',
          name: 'lookup_weather',
          arguments: '{"city":"Shanghai"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_object_arguments',
          output: 'sunny'
        },
        {
          type: 'function_call',
          call_id: 'call_array_arguments',
          name: 'lookup_weather',
          arguments: '["Shanghai"]'
        },
        {
          type: 'function_call_output',
          call_id: 'call_array_arguments',
          output: 'sunny'
        },
        {
          type: 'function_call',
          call_id: 'call_scalar_arguments',
          name: 'lookup_weather',
          arguments: '42'
        },
        {
          type: 'function_call_output',
          call_id: 'call_scalar_arguments',
          output: 'sunny'
        },
        {
          type: 'function_call',
          call_id: 'call_invalid_arguments',
          name: 'lookup_weather',
          arguments: 'not-json'
        },
        {
          type: 'function_call_output',
          call_id: 'call_invalid_arguments',
          output: 'sunny'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses function arguments preservation bridge' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses arguments 保真桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses function arguments preservation bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"input":\{"city":"Shanghai"\}/, 'Responses JSON object arguments 应原样进入 Anthropic input')
  assert.match(upstreamBodyText, /"openai_arguments":\["Shanghai"\]/, 'Responses JSON 数组 arguments 不应被吞成空对象')
  assert.match(upstreamBodyText, /"openai_arguments":42/, 'Responses JSON 标量 arguments 不应被吞成空对象')
  assert.match(upstreamBodyText, /"openai_arguments_text":"not-json"/, 'Responses 非法 JSON arguments 不应被吞成空对象')
}

async function assertChatJsonSchemaStructuredOutputBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat structured schema bridge' }],
      response_format: {
        type: 'json_schema',
        json_schema: {
          name: 'bridge_result',
          strict: true,
          schema: {
            type: 'object',
            properties: {
              ok: { type: 'boolean' },
              mode: { type: 'string' }
            },
            required: ['ok', 'mode'],
            additionalProperties: false
          }
        }
      },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat strict JSON schema 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string }; finish_reason?: string }> }
  assert.equal(body.choices?.[0]?.message?.content, '{"ok":true,"mode":"structured"}')
  assert.equal(body.choices?.[0]?.finish_reason, 'stop')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat structured schema bridge')
  assert.equal(upstreamToolNames(upstreamHits[0])[0], 'emit_structured_output')
  assert.deepEqual(upstreamHits[0]?.body.tool_choice, { type: 'tool', name: 'emit_structured_output' })
}

async function assertResponsesJsonSchemaStructuredOutputBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses structured schema bridge',
      text: {
        format: {
          type: 'json_schema',
          name: 'bridge_result',
          strict: true,
          schema: {
            type: 'object',
            properties: {
              ok: { type: 'boolean' },
              mode: { type: 'string' }
            },
            required: ['ok', 'mode'],
            additionalProperties: false
          }
        }
      },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses strict JSON schema 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { output_text?: string; output?: Array<{ type?: string; content?: Array<{ text?: string }> }> }
  assert.equal(body.output_text, '{"ok":true,"mode":"structured"}')
  assert.equal(body.output?.some((item) => item.type === 'function_call'), false, '合成结构化工具不应暴露为 Responses function_call')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses structured schema bridge')
  assert.equal(upstreamToolNames(upstreamHits[0])[0], 'emit_structured_output')
}

async function assertChatJsonSchemaValidationFailureBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat structured schema invalid bridge' }],
      response_format: {
        type: 'json_schema',
        json_schema: {
          name: 'bridge_result',
          strict: true,
          schema: {
            type: 'object',
            properties: {
              ok: { type: 'boolean' },
              mode: { type: 'string' }
            },
            required: ['ok', 'mode'],
            additionalProperties: false
          }
        }
      },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat strict JSON schema 校验失败应返回 Chat refusal，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string | null; refusal?: string }; finish_reason?: string }> }
  assert.equal(body.choices?.[0]?.message?.content, null)
  assert.match(body.choices?.[0]?.message?.refusal ?? '', /openai_anthropic_bridge_structured_output_schema_mismatch/)
  assert.match(body.choices?.[0]?.message?.refusal ?? '', /JSON Schema/)
  assert.equal(body.choices?.[0]?.finish_reason, 'stop')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat structured schema invalid bridge')
}

async function assertResponsesJsonSchemaValidationFailureBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses structured schema invalid bridge',
      text: {
        format: {
          type: 'json_schema',
          name: 'bridge_result',
          strict: true,
          schema: {
            type: 'object',
            properties: {
              ok: { type: 'boolean' },
              mode: { type: 'string' }
            },
            required: ['ok', 'mode'],
            additionalProperties: false
          }
        }
      },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 503, `Responses strict JSON schema 校验失败应被 response-inspection 改写为 503，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { error?: { code?: string; type?: string; message?: string } }
  assert.equal(body.error?.code, 'openai_anthropic_bridge_structured_output_schema_mismatch')
  assert.equal(body.error?.type, 'response_inspection_failed')
  assert.match(body.error?.message ?? '', /JSON Schema/)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses structured schema invalid bridge')
}

async function assertOpenAIReasoningDefaultControlsBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const responsesResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses reasoning defaults bridge',
      reasoning: { effort: null, summary: null },
      stream: false
    })
  })
  const responsesText = await responsesResponse.text()
  assert.equal(responsesResponse.status, 200, `Responses reasoning 默认空值应正常桥接，实际 HTTP ${responsesResponse.status}: ${responsesText}`)
  const responsesBody = JSON.parse(responsesText) as { reasoning?: { effort?: string | null; summary?: string | null } }
  assert.equal(responsesBody.reasoning?.effort, null, 'Responses reasoning.effort=null 应在响应快照中保持未请求语义')
  assert.equal(responsesBody.reasoning?.summary, null, 'Responses reasoning.summary=null 应在响应快照中保持未请求语义')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses reasoning defaults bridge')
  assert.equal(upstreamHits[0]?.body.thinking, undefined, 'Responses reasoning 空值不应启用 Anthropic thinking')
  assert.equal(upstreamHits[0]?.body.reasoning, undefined, 'Responses reasoning 不应以 OpenAI 字段透传')
  assert.equal(upstreamHits[0]?.body.reasoning_effort, undefined, 'Responses reasoning_effort 不应透传')

  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat reasoning defaults bridge' }],
      reasoning_effort: null,
      reasoning: { effort: null },
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat reasoning 默认空值应正常桥接，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat reasoning defaults bridge')
  assert.equal(upstreamHits[0]?.body.thinking, undefined, 'Chat reasoning 空值不应启用 Anthropic thinking')
  assert.equal(upstreamHits[0]?.body.reasoning, undefined, 'Chat reasoning 不应以 OpenAI 字段透传')
  assert.equal(upstreamHits[0]?.body.reasoning_effort, undefined, 'Chat reasoning_effort 不应透传')
}

async function assertResponsesThinkingBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking bridge',
      reasoning: { effort: 'low' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses thinking 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    output_text?: string
    reasoning?: { effort?: string | null }
    usage?: { output_tokens_details?: { reasoning_tokens?: number } }
    output?: Array<{ type?: string; summary?: Array<{ text?: string }> }>
  }
  assert.equal(body.output_text, 'thinking visible answer')
  assert.equal(body.reasoning?.effort, 'low')
  assert.equal(body.usage?.output_tokens_details?.reasoning_tokens, 3)
  assert.equal(body.output?.some((item) => item.type === 'reasoning'), true, 'Anthropic thinking 应渲染为 Responses reasoning item')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses thinking bridge')
  assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 2048 })
}

async function assertResponsesReasoningSummaryAcceptedBridge(baseUrl: string, localApiKey: string): Promise<void> {
  for (const summary of ['auto', 'concise', 'detailed']) {
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: `responses thinking bridge summary ${summary}`,
        reasoning: { effort: 'low', summary },
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Responses reasoning summary ${summary} 应成功，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as {
      output_text?: string
      reasoning?: { effort?: string | null; summary?: string | null }
      output?: Array<{ type?: string; summary?: Array<{ text?: string }> }>
      usage?: { output_tokens_details?: { reasoning_tokens?: number } }
    }
    const reasoningItem = body.output?.find((item) => item.type === 'reasoning')
    assert.equal(body.output_text, 'thinking visible answer')
    assert.equal(body.reasoning?.effort, 'low')
    assert.equal(body.reasoning?.summary, summary)
    assert.equal(reasoningItem?.summary?.[0]?.text, 'hidden thinking summary', `reasoning.summary=${summary} 应输出 Responses reasoning summary item`)
    assert.equal(body.usage?.output_tokens_details?.reasoning_tokens, 3, `reasoning.summary=${summary} 应保留 reasoning token usage`)
    assert.doesNotMatch(body.output_text ?? '', /hidden thinking summary/, `reasoning.summary=${summary} 不应把 thinking 混入 output_text`)
    assertBridgeUpstreamHit(upstreamHits[0], false, `responses thinking bridge summary ${summary}`)
    assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 2048 })
  }
}

async function assertResponsesReasoningEffortNoneBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses reasoning effort none bridge',
      reasoning: { effort: 'none' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning effort none 应成功且不启用 Anthropic thinking，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { reasoning?: { effort?: string | null } }
  assert.equal(body.reasoning?.effort, 'none')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses reasoning effort none bridge')
  assert.equal(upstreamHits[0]?.body.thinking, undefined, 'reasoning.effort=none 不应向 Anthropic 发送 thinking')
}

async function assertChatReasoningEffortNoneBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat reasoning effort none bridge' }],
      reasoning_effort: 'none',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat reasoning_effort none 应成功且不启用 Anthropic thinking，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat reasoning effort none bridge')
  assert.equal(upstreamHits[0]?.body.thinking, undefined, 'Chat reasoning_effort=none 不应向 Anthropic 发送 thinking')
}

async function assertResponsesReasoningUnsupportedEffortRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses reasoning xhigh unsupported',
      reasoning: { effort: 'xhigh' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning effort xhigh 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_reasoning_effort_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses reasoning effort xhigh 不应请求 Anthropic 上游')
}

async function assertChatReasoningUnsupportedEffortRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat reasoning xhigh unsupported' }],
      reasoning_effort: 'xhigh',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat reasoning_effort xhigh 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"chat\.completion"/)
  assert.match(text, /openai_anthropic_bridge_reasoning_effort_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat reasoning_effort xhigh 不应请求 Anthropic 上游')
}

async function assertResponsesReasoningSummaryNoneBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking bridge summary none',
      reasoning: { effort: 'low', summary: 'none' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning summary none 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    output_text?: string
    reasoning?: { effort?: string | null; summary?: string | null }
    output?: Array<{ type?: string }>
    usage?: { output_tokens_details?: { reasoning_tokens?: number } }
  }
  assert.equal(body.output_text, 'thinking visible answer')
  assert.equal(body.reasoning?.effort, 'low')
  assert.equal(body.reasoning?.summary, 'none')
  assert.equal(body.output?.some((item) => item.type === 'reasoning'), false, 'reasoning.summary=none 不应输出 Responses reasoning item')
  assert.equal(body.usage?.output_tokens_details?.reasoning_tokens, 3, 'summary=none 仍应保留 reasoning token usage')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses thinking bridge summary none')
  assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 2048 })
}

async function assertResponsesReasoningUnsupportedSummaryRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses reasoning summary unsupported',
      reasoning: { effort: 'low', summary: 'verbose' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning summary verbose 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_reasoning_summary_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses reasoning summary verbose 不应请求 Anthropic 上游')
}

async function assertResponsesEncryptedReasoningIncludeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses encrypted reasoning include unsupported',
      reasoning: { effort: 'low' },
      include: ['reasoning.encrypted_content'],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses encrypted reasoning include 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"object":"response"/)
  assert.match(text, /"status":"completed"/)
  assert.match(text, /openai_anthropic_bridge_encrypted_reasoning_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses encrypted reasoning include 不应请求 Anthropic 上游')
}

async function assertResponsesUnsupportedIncludesRejected(baseUrl: string, localApiKey: string): Promise<void> {
  const unsupportedIncludes = [
    'web_search_call.action.sources',
    'code_interpreter_call.outputs',
    'computer_call_output.output.image_url',
    'message.input_image.image_url'
  ]
  for (const include of unsupportedIncludes) {
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: `responses unsupported include ${include}`,
        include: [include],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Responses unsupported include ${include} 应返回协议内 guidance，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /"object":"response"/)
    assert.match(text, /"status":"completed"/)
    assert.match(text, /openai_anthropic_bridge_include_unsupported/)
    assert.equal(upstreamHits.length, 0, `Responses unsupported include ${include} 不应请求 Anthropic 上游`)
  }
}

async function assertResponsesEncryptedReasoningInputItemRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        {
          type: 'reasoning',
          encrypted_content: 'opaque-openai-reasoning-state'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'responses encrypted reasoning input item unsupported' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 400, `Responses encrypted reasoning input item 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_encrypted_reasoning_input_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses encrypted reasoning input item 不应请求 Anthropic 上游')
}

async function assertResponsesThinkingSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking sse bridge',
      reasoning: { effort: 'medium' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses thinking SSE 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"type":"reasoning"/)
  assert.match(text, /thinking visible sse answer/)
  assert.match(text, /"reasoning_tokens":5/)
  assert.doesNotMatch(text, /"delta":"hidden thinking sse"/, 'thinking_delta 不应作为 output_text.delta 暴露')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses thinking sse bridge')
  assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 4096 })
}

async function assertResponsesReasoningSummaryAcceptedSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking sse bridge summary detailed',
      reasoning: { effort: 'medium', summary: 'detailed' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning summary detailed SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /"summary":"detailed"/, 'Responses SSE completed snapshot 应回显 reasoning.summary=detailed')
  assert.match(text, /"type":"reasoning"/, 'reasoning.summary=detailed SSE 应输出 reasoning item')
  assert.match(text, /hidden thinking sse/, 'reasoning.summary=detailed SSE 应输出 reasoning summary delta')
  assert.match(text, /thinking visible sse answer/)
  assert.match(text, /"reasoning_tokens":5/)
  assert.doesNotMatch(text, /"type":"output_text\.delta","delta":"hidden thinking sse"/, 'reasoning.summary=detailed SSE 不应把 thinking_delta 混入 output_text.delta')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses thinking sse bridge summary detailed')
  assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 4096 })
}

async function assertResponsesReasoningSummaryNoneSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses thinking sse bridge summary none',
      reasoning: { effort: 'medium', summary: 'none' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses reasoning summary none SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.doesNotMatch(text, /"type":"reasoning"/, 'reasoning.summary=none SSE 不应输出 reasoning item')
  assert.match(text, /thinking visible sse answer/)
  assert.match(text, /"reasoning_tokens":5/)
  assert.doesNotMatch(text, /"delta":"hidden thinking sse"/, 'summary=none SSE 也不能把 thinking_delta 混入 output_text.delta')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses thinking sse bridge summary none')
  assert.deepEqual(upstreamHits[0]?.body.thinking, { type: 'enabled', budget_tokens: 4096 })
}

async function assertResponsesHostedToolUnsupported(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses hosted tool unsupported',
      tools: [{ type: 'computer' }],
      tool_choice: 'required',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `unsupported hosted tool 应返回正常 guidance，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { output_text?: string; status?: string }
  assert.equal(body.status, 'completed')
  assert.match(body.output_text ?? '', /能力未执行：computer/)
  assert.match(body.output_text ?? '', /建议下一步/)
  assert.doesNotMatch(text, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'unsupported hosted tool 不应请求 Anthropic 上游')

  const responsesCodeInterpreterResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses code_interpreter hosted tool unsupported',
      tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
      tool_choice: { type: 'code_interpreter' },
      stream: false
    })
  })
  const responsesCodeInterpreterText = await responsesCodeInterpreterResponse.text()
  assert.equal(responsesCodeInterpreterResponse.status, 200, `Responses code_interpreter 应返回正常 guidance，实际 HTTP ${responsesCodeInterpreterResponse.status}: ${responsesCodeInterpreterText}`)
  const responsesCodeInterpreterBody = JSON.parse(responsesCodeInterpreterText) as { output_text?: string; status?: string }
  assert.equal(responsesCodeInterpreterBody.status, 'completed')
  assert.match(responsesCodeInterpreterBody.output_text ?? '', /能力未执行：code_interpreter/)
  assert.match(responsesCodeInterpreterBody.output_text ?? '', /沙箱/)
  assert.doesNotMatch(responsesCodeInterpreterText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Responses code_interpreter guidance 不应请求 Anthropic 上游')

  const responsesMcpResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses mcp hosted tool unsupported',
      tools: [{ type: 'mcp', server_label: 'mock-mcp', server_url: 'https://example.invalid/mcp', require_approval: 'never' }],
      tool_choice: { type: 'mcp' },
      stream: false
    })
  })
  const responsesMcpText = await responsesMcpResponse.text()
  assert.equal(responsesMcpResponse.status, 200, `Responses MCP 应返回正常 guidance，实际 HTTP ${responsesMcpResponse.status}: ${responsesMcpText}`)
  const responsesMcpBody = JSON.parse(responsesMcpText) as { output_text?: string; status?: string }
  assert.equal(responsesMcpBody.status, 'completed')
  assert.match(responsesMcpBody.output_text ?? '', /能力未执行：mcp/)
  assert.match(responsesMcpBody.output_text ?? '', /MCP/)
  assert.doesNotMatch(responsesMcpText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Responses MCP guidance 不应请求 Anthropic 上游')

  const responsesWebSearchResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses web_search hosted tool unsupported',
      tools: [{ type: 'web_search', search_context_size: 'low' }],
      tool_choice: { type: 'web_search' },
      stream: false
    })
  })
  const responsesWebSearchText = await responsesWebSearchResponse.text()
  assert.equal(responsesWebSearchResponse.status, 200, `Responses web_search 应返回正常 guidance，实际 HTTP ${responsesWebSearchResponse.status}: ${responsesWebSearchText}`)
  const responsesWebSearchBody = JSON.parse(responsesWebSearchText) as { output_text?: string; status?: string }
  assert.equal(responsesWebSearchBody.status, 'completed')
  assert.match(responsesWebSearchBody.output_text ?? '', /能力未执行：web_search/)
  assert.match(responsesWebSearchBody.output_text ?? '', /本地客户端/)
  assert.doesNotMatch(responsesWebSearchText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Responses web_search guidance 不应请求 Anthropic 上游')

  const responsesImageGenerationResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation hosted tool unsupported',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'low' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const responsesImageGenerationText = await responsesImageGenerationResponse.text()
  assert.equal(responsesImageGenerationResponse.status, 200, `Responses image_generation 无 provider 应返回正常 guidance，实际 HTTP ${responsesImageGenerationResponse.status}: ${responsesImageGenerationText}`)
  const responsesImageGenerationBody = JSON.parse(responsesImageGenerationText) as { output_text?: string; status?: string }
  assert.equal(responsesImageGenerationBody.status, 'completed')
  assert.match(responsesImageGenerationBody.output_text ?? '', /能力未执行：image_generation/)
  assert.match(responsesImageGenerationBody.output_text ?? '', /图像生成 provider/)
  assert.doesNotMatch(responsesImageGenerationText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Responses image_generation guidance 不应请求 Anthropic 上游')

  const chatWebSearchResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat web_search hosted tool unsupported' }],
      tools: [{ type: 'web_search', search_context_size: 'low' }],
      tool_choice: 'required',
      stream: false
    })
  })
  const chatWebSearchText = await chatWebSearchResponse.text()
  assert.equal(chatWebSearchResponse.status, 200, `Chat web_search 应返回正常 guidance，实际 HTTP ${chatWebSearchResponse.status}: ${chatWebSearchText}`)
  const chatWebSearchBody = JSON.parse(chatWebSearchText) as { choices?: Array<{ message?: { content?: string } }> }
  const chatGuidance = chatWebSearchBody.choices?.[0]?.message?.content ?? ''
  assert.match(chatGuidance, /能力未执行：web_search/)
  assert.match(chatGuidance, /建议下一步/)
  assert.doesNotMatch(chatWebSearchText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Chat web_search guidance 不应请求 Anthropic 上游')

  const chatCodeInterpreterResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'chat code_interpreter hosted tool unsupported' }],
      tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
      tool_choice: 'required',
      stream: false
    })
  })
  const chatCodeInterpreterText = await chatCodeInterpreterResponse.text()
  assert.equal(chatCodeInterpreterResponse.status, 200, `Chat code_interpreter 应返回正常 guidance，实际 HTTP ${chatCodeInterpreterResponse.status}: ${chatCodeInterpreterText}`)
  const chatCodeInterpreterBody = JSON.parse(chatCodeInterpreterText) as { choices?: Array<{ message?: { content?: string } }> }
  const chatCodeInterpreterGuidance = chatCodeInterpreterBody.choices?.[0]?.message?.content ?? ''
  assert.match(chatCodeInterpreterGuidance, /能力未执行：code_interpreter/)
  assert.match(chatCodeInterpreterGuidance, /沙箱/)
  assert.doesNotMatch(chatCodeInterpreterText, /openai_anthropic_bridge_unsupported_hosted_tool/)
  assert.equal(upstreamHits.length, 0, 'Chat code_interpreter guidance 不应请求 Anthropic 上游')
}

async function assertHostedToolRuntimeRejectMode(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.codeInterpreter
  runtimeConfig.hostedToolRuntimes.codeInterpreter = 'reject'
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses code_interpreter hosted runtime reject',
        tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
        tool_choice: { type: 'code_interpreter' },
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `code_interpreter reject 模式应返回本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_hosted_tool_runtime_rejected/)
    assert.match(text, /code_interpreter/)
    assert.equal(upstreamHits.length, 0, 'code_interpreter reject 模式不应请求 Anthropic 上游')
  } finally {
    runtimeConfig.hostedToolRuntimes.codeInterpreter = previousMode
  }
}

async function assertHostedToolRuntimeLocalUnavailable(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.codeInterpreter
  const previousPythonCommand = runtimeConfig.codeInterpreter.pythonCommand
  runtimeConfig.hostedToolRuntimes.codeInterpreter = 'local_runtime'
  runtimeConfig.codeInterpreter.pythonCommand = ''
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses code_interpreter hosted runtime local unavailable',
        tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
        tool_choice: { type: 'code_interpreter' },
        include: ['code_interpreter_call.outputs'],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 503, `code_interpreter local_runtime 未配置 executor 应返回本地 503，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_code_interpreter_runtime_unavailable/)
    assert.match(text, /service_unavailable/)
    assert.doesNotMatch(text, /openai_anthropic_bridge_include_unsupported/, 'local_runtime 应先进入运行时分支，不应被 include 校验误拒绝')
    assert.equal(upstreamHits.length, 0, 'code_interpreter local_runtime 未配置 executor 不应请求 Anthropic 上游')
  } finally {
    runtimeConfig.hostedToolRuntimes.codeInterpreter = previousMode
    runtimeConfig.codeInterpreter.pythonCommand = previousPythonCommand
  }
}

async function assertHostedToolRuntimeLocalWorkerLoop(baseUrl: string, localApiKey: string): Promise<void> {
  const previousMode = runtimeConfig.hostedToolRuntimes.codeInterpreter
  const previousPythonCommand = runtimeConfig.codeInterpreter.pythonCommand
  const previousTimeoutMs = runtimeConfig.codeInterpreter.timeoutMs
  const previousMaxOutputBytes = runtimeConfig.codeInterpreter.maxOutputBytes
  const previousMaxArtifactCount = runtimeConfig.codeInterpreter.maxArtifactCount
  const previousMaxArtifactBytes = runtimeConfig.codeInterpreter.maxArtifactBytes
  runtimeConfig.hostedToolRuntimes.codeInterpreter = 'local_runtime'
  runtimeConfig.codeInterpreter.pythonCommand = previousPythonCommand || 'python'
  runtimeConfig.codeInterpreter.timeoutMs = 3000
  runtimeConfig.codeInterpreter.maxOutputBytes = 4096
  runtimeConfig.codeInterpreter.maxArtifactCount = 4
  runtimeConfig.codeInterpreter.maxArtifactBytes = 1024
  try {
    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker success',
      expectedLog: /ci-success: 5/,
      expectedFinal: /Code interpreter final answer: .*ci-success: 5/s,
      validate(codeItem, text) {
        assert.match(codeItem.outputs?.[0]?.logs ?? '', /secret: missing/)
        assert.doesNotMatch(text, /juhe-ai-dev-secret-change-me/, 'Code interpreter 子进程不应继承网关密钥环境')
      }
    })

    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker stderr',
      expectedLog: /\[stderr\]\nci-stderr-line/,
      expectedFinal: /Code interpreter final answer: .*ci-stdout-line/s,
      validate(codeItem) {
        assert.equal(codeItem.metadata?.exit_code, 0)
      }
    })

    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker artifact',
      expectedLog: /artifact ready/,
      expectedFinal: /Code interpreter final answer: .*artifact ready/s,
      async validate(codeItem, text) {
        const artifactLogs = codeItem.outputs?.map((output) => output.logs ?? '').join('\n') ?? ''
        assert.match(artifactLogs, /\[artifacts\]/)
        assert.match(artifactLogs, /result\.txt/)
        assert.doesNotMatch(text, /artifact_body_marker/, 'Code interpreter 文件正文不应进入 Responses payload 或 Anthropic tool_result')
        const containerId = typeof codeItem.container_id === 'string' ? codeItem.container_id : ''
        assert.match(containerId, /^cntr_local_/, 'Code interpreter 产物应绑定本地 container_id')
        const artifact = codeItem.metadata?.artifacts?.[0]
        assert.equal(artifact?.filename, 'result.txt')
        assert.equal(artifact?.media_type, 'text/plain')
        assert.equal(typeof artifact?.bytes, 'number')
        assert((artifact?.bytes ?? 0) > 0, 'Code interpreter 文件产物应记录字节数')
        assert.match(artifact?.file_id ?? '', /^file-/)
        assert.equal(artifact?.download_path, `/v1/files/${artifact?.file_id}/content`)
        assert.equal(artifact?.container_id, containerId)
        assert.equal(artifact?.container_download_path, `/v1/containers/${containerId}/files/${artifact?.file_id}/content`)
        assert.match(artifactLogs, new RegExp(`file_id: ${artifact?.file_id}`))
        assert.equal(artifact?.content_omitted, undefined)
        assert.equal(artifact?.omit_reason, undefined)
        assert.equal(codeItem.metadata?.artifacts_omitted_count, undefined)
        const fileId = artifact?.file_id ?? ''
        const artifactBody = 'artifact_body_marker'
        const expectedBytes = Buffer.byteLength(artifactBody, 'utf8')
        assert.equal(artifact?.bytes, expectedBytes)
        const listResponse = await fetch(`${baseUrl}/v1/containers/${encodeURIComponent(containerId)}/files`, {
          headers: { authorization: `Bearer ${localApiKey}` }
        })
        const listText = await listResponse.text()
        assert.equal(listResponse.status, 200, `Code interpreter container 文件列表应可读取，实际 HTTP ${listResponse.status}: ${listText}`)
        const listBody = JSON.parse(listText) as {
          object?: string
          data?: Array<{
            id?: string
            object?: string
            container_id?: string
            filename?: string
            bytes?: number
            status?: string
          }>
        }
        assert.equal(listBody.object, 'list')
        const listedFile = listBody.data?.find((item) => item.id === fileId)
        assert(listedFile, 'Code interpreter container 文件列表应包含当前产物')
        assert.equal(listedFile?.object, 'container.file')
        assert.equal(listedFile?.container_id, containerId)
        assert.equal(listedFile?.filename, 'result.txt')
        assert.equal(listedFile?.bytes, expectedBytes)
        const detailResponse = await fetch(`${baseUrl}/v1/containers/${encodeURIComponent(containerId)}/files/${encodeURIComponent(fileId)}`, {
          headers: { authorization: `Bearer ${localApiKey}` }
        })
        const detailText = await detailResponse.text()
        assert.equal(detailResponse.status, 200, `Code interpreter container 文件详情应可读取，实际 HTTP ${detailResponse.status}: ${detailText}`)
        const detailBody = JSON.parse(detailText) as {
          id?: string
          object?: string
          container_id?: string
          filename?: string
          bytes?: number
          status?: string
        }
        assert.equal(detailBody.object, 'container.file')
        assert.equal(detailBody.id, fileId)
        assert.equal(detailBody.container_id, containerId)
        assert.equal(detailBody.filename, 'result.txt')
        assert.equal(detailBody.bytes, expectedBytes)
        const containerDownloadResponse = await fetch(`${baseUrl}/v1/containers/${encodeURIComponent(containerId)}/files/${encodeURIComponent(fileId)}/content`, {
          headers: { authorization: `Bearer ${localApiKey}` }
        })
        const containerDownloaded = await containerDownloadResponse.text()
        assert.equal(containerDownloadResponse.status, 200, `Code interpreter container 文件内容应可下载，实际 HTTP ${containerDownloadResponse.status}: ${containerDownloaded}`)
        assert.equal(containerDownloaded, artifactBody)
        const wrongContainerResponse = await fetch(`${baseUrl}/v1/containers/cntr_local_wrong/files/${encodeURIComponent(fileId)}`, {
          headers: { authorization: `Bearer ${localApiKey}` }
        })
        assert.equal(wrongContainerResponse.status, 404, '错误 container 不应读取到当前文件')
        const downloadResponse = await fetch(`${baseUrl}/v1/files/${encodeURIComponent(fileId)}/content`, {
          headers: { authorization: `Bearer ${localApiKey}` }
        })
        const downloaded = await downloadResponse.text()
        assert.equal(downloadResponse.status, 200, `Code interpreter 文件产物应可下载，实际 HTTP ${downloadResponse.status}: ${downloaded}`)
        assert.equal(downloaded, artifactBody)
      }
    })

    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker artifact large',
      expectedLog: /large artifact ready/,
      expectedFinal: /Code interpreter final answer: .*large artifact ready/s,
      validate(codeItem) {
        const artifact = codeItem.metadata?.artifacts?.[0]
        assert.equal(artifact?.filename, 'big.txt')
        assert.equal(artifact?.content_omitted, true)
        assert.equal(artifact?.omit_reason, 'file_too_large')
        assert.equal(artifact?.file_id, undefined)
        assert.equal(artifact?.download_path, undefined)
      }
    })

    runtimeConfig.codeInterpreter.maxOutputBytes = 32
    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker truncation',
      expectedLog: /\[truncated\]/,
      expectedFinal: /Code interpreter final answer:/,
      validate(codeItem) {
        assert.equal(codeItem.metadata?.output_truncated, true, 'Code interpreter 大输出应标记 output_truncated')
      }
    })

    runtimeConfig.codeInterpreter.maxOutputBytes = 4096
    runtimeConfig.codeInterpreter.timeoutMs = 200
    await assertCodeInterpreterWorkerCase(baseUrl, localApiKey, {
      input: 'responses code_interpreter local runtime worker timeout',
      expectedLog: /\[timeout\]/,
      expectedFinal: /Code interpreter final answer:/,
      validate(codeItem) {
        assert.equal(codeItem.metadata?.timed_out, true, 'Code interpreter 超时应标记 timed_out')
      }
    })
  } finally {
    runtimeConfig.hostedToolRuntimes.codeInterpreter = previousMode
    runtimeConfig.codeInterpreter.pythonCommand = previousPythonCommand
    runtimeConfig.codeInterpreter.timeoutMs = previousTimeoutMs
    runtimeConfig.codeInterpreter.maxOutputBytes = previousMaxOutputBytes
    runtimeConfig.codeInterpreter.maxArtifactCount = previousMaxArtifactCount
    runtimeConfig.codeInterpreter.maxArtifactBytes = previousMaxArtifactBytes
  }
}

async function assertCodeInterpreterWorkerCase(
  baseUrl: string,
  localApiKey: string,
  input: {
    input: string
    expectedLog: RegExp
    expectedFinal: RegExp
    validate?: (codeItem: {
      type?: string
      code?: string
      container_id?: string
      outputs?: Array<{ type?: string; logs?: string }>
      metadata?: {
        exit_code?: number
        timed_out?: boolean
        output_truncated?: boolean
        artifacts?: Array<{
          filename?: string
          bytes?: number
          file_id?: string
          download_path?: string
          container_id?: string
          container_download_path?: string
          media_type?: string
          content_omitted?: boolean
          omit_reason?: string
        }>
        artifacts_omitted_count?: number
      }
    }, text: string) => void | Promise<void>
  }
): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: input.input,
      tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
      tool_choice: { type: 'code_interpreter' },
      include: ['code_interpreter_call.outputs'],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `code_interpreter local_runtime worker 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    status?: string
    output?: Array<{
      type?: string
      code?: string
      container_id?: string
      outputs?: Array<{ type?: string; logs?: string }>
      metadata?: {
        exit_code?: number
        timed_out?: boolean
        output_truncated?: boolean
        artifacts?: Array<{
          filename?: string
          bytes?: number
          file_id?: string
          download_path?: string
          container_id?: string
          container_download_path?: string
          media_type?: string
          content_omitted?: boolean
          omit_reason?: string
        }>
        artifacts_omitted_count?: number
      }
    }>
    output_text?: string
    metadata?: { gateway_runtime?: string; gateway_tool?: string; gateway_local_runtime_tool_loop_rounds?: number }
  }
  assert.equal(body.status, 'completed')
  assert.equal(body.metadata?.gateway_runtime, 'local_runtime')
  assert.equal(body.metadata?.gateway_tool, 'code_interpreter')
  assert.equal(body.metadata?.gateway_local_runtime_tool_loop_rounds, 1)
  const codeItem = body.output?.find((item) => item.type === 'code_interpreter_call')
  assert(codeItem, `code_interpreter local_runtime worker 应返回 code_interpreter_call: ${text}`)
  assert.match(codeItem.code ?? '', /print|while True|sys\.stderr/)
  assert.equal(codeItem.outputs?.[0]?.type, 'logs')
  assert.match(codeItem.outputs?.[0]?.logs ?? '', input.expectedLog)
  assert.match(body.output_text ?? '', input.expectedFinal)
  assert.equal(upstreamHits.length, 2, 'code_interpreter local_runtime worker 应请求 Anthropic 选择工具并回灌 tool_result')
  assert.deepEqual(upstreamToolNames(upstreamHits[0]), ['python'])
  assert.match(JSON.stringify(upstreamHits[1]?.body ?? {}), /"type":"tool_result"/, 'Code interpreter 第二轮应回灌 Anthropic tool_result')
  await input.validate?.(codeItem, text)
}

async function assertHostedToolRuntimeMockMode(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.codeInterpreter
  runtimeConfig.hostedToolRuntimes.codeInterpreter = 'mock'
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses code_interpreter hosted runtime mock json',
        tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
        tool_choice: { type: 'code_interpreter' },
        include: ['code_interpreter_call.outputs'],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `code_interpreter mock JSON 应返回本地 Responses，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as {
      status?: string
      output?: Array<{ type?: string; outputs?: Array<{ type?: string; logs?: string }>; code?: string }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string }
      usage?: { total_tokens?: number }
    }
    assert.equal(body.status, 'completed')
    assert.equal(body.metadata?.gateway_runtime, 'mock')
    assert.equal(body.metadata?.gateway_tool, 'code_interpreter')
    assert.equal(body.usage?.total_tokens, 0)
    const codeItem = body.output?.find((item) => item.type === 'code_interpreter_call')
    assert(codeItem, `code_interpreter mock JSON 应返回 code_interpreter_call: ${text}`)
    assert.match(codeItem.code ?? '', /juhe-ai code_interpreter mock runtime/)
    assert.equal(codeItem.outputs?.[0]?.type, 'logs')
    assert.match(codeItem.outputs?.[0]?.logs ?? '', /execution skipped/)
    assert.match(body.output_text ?? '', /mock runtime completed without executing code/i)
    assert.doesNotMatch(text, /responses code_interpreter hosted runtime mock json/, 'mock 输出不能泄漏用户 prompt')
    assert.equal(upstreamHits.length, 0, 'code_interpreter mock JSON 不应请求 Anthropic 上游')

    upstreamHits.length = 0
    const sseResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses code_interpreter hosted runtime mock sse',
        tools: [{ type: 'code_interpreter', container: { type: 'auto' } }],
        tool_choice: { type: 'code_interpreter' },
        include: ['code_interpreter_call.outputs'],
        stream: true
      })
    })
    const sseText = await sseResponse.text()
    assert.equal(sseResponse.status, 200, `code_interpreter mock SSE 应返回本地 Responses SSE，实际 HTTP ${sseResponse.status}: ${sseText}`)
    assert.match(sseText, /event: response\.created/)
    assert.match(sseText, /event: response\.output_item\.added/)
    assert.match(sseText, /event: response\.output_item\.done/)
    assert.match(sseText, /event: response\.output_text\.delta/)
    assert.match(sseText, /event: response\.completed/)
    assert.match(sseText, /"type":"code_interpreter_call"/)
    assert.match(sseText, /juhe-ai code_interpreter mock runtime/)
    assert.match(sseText, /execution skipped/)
    assert.doesNotMatch(sseText, /data: \[DONE\]/, 'Responses mock SSE 不应使用 Chat [DONE]')
    assert.doesNotMatch(sseText, /responses code_interpreter hosted runtime mock sse/, 'mock SSE 输出不能泄漏用户 prompt')
    assert.equal(upstreamHits.length, 0, 'code_interpreter mock SSE 不应请求 Anthropic 上游')
  } finally {
    runtimeConfig.hostedToolRuntimes.codeInterpreter = previousMode
  }
}

async function assertComputerHostedToolRuntimeMockMode(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.computer
  runtimeConfig.hostedToolRuntimes.computer = 'mock'
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer hosted runtime mock json',
        tools: [{ type: 'computer', display_width: 1280, display_height: 720, environment: 'browser' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `computer mock JSON 应返回本地 Responses，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as {
      status?: string
      output?: Array<{ type?: string; actions?: Array<{ type?: string }>; call_id?: string }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string; gateway_computer_action_policy?: string }
      tools?: Array<{ type?: string; display_width?: number; display_height?: number; environment?: string }>
      usage?: { total_tokens?: number }
    }
    assert.equal(body.status, 'completed')
    assert.equal(body.metadata?.gateway_runtime, 'mock')
    assert.equal(body.metadata?.gateway_tool, 'computer')
    assert.equal(body.metadata?.gateway_computer_action_policy, 'screenshot_only')
    assert.equal(body.usage?.total_tokens, 0)
    assert.deepEqual(body.tools?.[0], { type: 'computer', display_width: 1280, display_height: 720, environment: 'browser' })
    const computerItem = body.output?.find((item) => item.type === 'computer_call')
    assert(computerItem, `computer mock JSON 应返回 computer_call: ${text}`)
    assert(computerItem.call_id, `computer mock JSON 应返回 call_id: ${text}`)
    assert.deepEqual(computerItem.actions, [{ type: 'screenshot' }])
    assert.match(body.output_text ?? '', /screenshot request/i)
    assert.doesNotMatch(text, /responses computer hosted runtime mock json/, 'computer mock JSON 输出不能泄漏用户 prompt')
    assert.equal(upstreamHits.length, 0, 'computer mock JSON 不应请求 Anthropic 上游')

    upstreamHits.length = 0
    const sseResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer hosted runtime mock sse',
        tools: [{ type: 'computer' }],
        tool_choice: { type: 'computer' },
        stream: true
      })
    })
    const sseText = await sseResponse.text()
    assert.equal(sseResponse.status, 200, `computer mock SSE 应返回本地 Responses SSE，实际 HTTP ${sseResponse.status}: ${sseText}`)
    assert.match(sseText, /event: response\.created/)
    assert.match(sseText, /event: response\.output_item\.added/)
    assert.match(sseText, /event: response\.output_item\.done/)
    assert.match(sseText, /event: response\.output_text\.delta/)
    assert.match(sseText, /event: response\.completed/)
    assert.match(sseText, /"type":"computer_call"/)
    assert.match(sseText, /"type":"screenshot"/)
    assert.match(sseText, /Computer mock runtime returned a screenshot request/)
    assert.doesNotMatch(sseText, /data: \[DONE\]/, 'Responses computer mock SSE 不应使用 Chat [DONE]')
    assert.doesNotMatch(sseText, /responses computer hosted runtime mock sse/, 'computer mock SSE 输出不能泄漏用户 prompt')
    assert.equal(upstreamHits.length, 0, 'computer mock SSE 不应请求 Anthropic 上游')

    upstreamHits.length = 0
    const outputResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'computer_call_output',
          call_id: 'call_mock_computer_output',
          output: {
            type: 'input_image',
            image_url: 'data:image/png;base64,aW1hZ2U='
          }
        }],
        tools: [{ type: 'computer' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const outputText = await outputResponse.text()
    assert.equal(outputResponse.status, 200, `computer_call_output mock JSON 应正常收口，实际 HTTP ${outputResponse.status}: ${outputText}`)
    const outputBody = JSON.parse(outputText) as {
      status?: string
      output?: Array<{ type?: string }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string }
    }
    assert.equal(outputBody.status, 'completed')
    assert.equal(outputBody.metadata?.gateway_runtime, 'mock')
    assert.equal(outputBody.metadata?.gateway_tool, 'computer')
    assert.equal(outputBody.output?.some((item) => item.type === 'computer_call'), false)
    assert.match(outputBody.output_text ?? '', /received computer_call_output/i)
    assert.doesNotMatch(outputText, /data:image\/png;base64/, 'computer_call_output mock 不应回显截图正文')
    assert.equal(upstreamHits.length, 0, 'computer_call_output mock 不应请求 Anthropic 上游')
  } finally {
    runtimeConfig.hostedToolRuntimes.computer = previousMode
  }
}

async function assertComputerHostedToolRuntimeLocalRuntimeMode(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.computer
  runtimeConfig.hostedToolRuntimes.computer = 'local_runtime'
  openAICompatibleComputerAdapter.setOpenAICompatibleComputerExecutorForTest(undefined)
  try {
    const unavailableResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer hosted runtime local unavailable',
        tools: [{ type: 'computer', display_width: 1280, display_height: 720, environment: 'browser' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const unavailableText = await unavailableResponse.text()
    assert.equal(unavailableResponse.status, 503, `computer local_runtime 未配置 adapter 应返回 503，实际 HTTP ${unavailableResponse.status}: ${unavailableText}`)
    assert.match(unavailableText, /openai_anthropic_bridge_computer_runtime_unavailable/)
    assert.equal(upstreamHits.length, 0, 'computer local_runtime 未配置 adapter 不应请求 Anthropic 上游')

    openAICompatibleComputerAdapter.setOpenAICompatibleComputerExecutorForTest({
      async run(input) {
        const hasComputerOutput = Array.isArray(input.body.input)
          && input.body.input.some((item) => typeof item === 'object' && item !== null && !Array.isArray(item) && (item as { type?: unknown }).type === 'computer_call_output')
        if (hasComputerOutput) {
          return {
            message: 'Computer local_runtime adapter received computer_call_output and completed.',
            metadata: {
              session_id: 'comp_sess_local',
              screenshot_omitted: true,
              image_url: 'data:image/png;base64,should_not_leak_local_output',
              prompt: 'responses computer local runtime output prompt should not leak'
            }
          }
        }
        return {
          message: 'Computer local_runtime adapter returned controlled screenshot request.',
          call: {
            callId: 'local_computer_test',
            status: 'completed',
            actions: [{
              type: 'screenshot',
              text: 'typed secret should be omitted',
              metadata: {
                screenshot_ref: 'omitted://computer/local',
                image_url: 'data:image/png;base64,should_not_leak_action',
                token: 'should_not_leak_token'
              }
            }],
            metadata: {
              session_id: 'comp_sess_local',
              action_count: 1,
              screenshot_ref: 'omitted://computer/local',
              image_url: 'data:image/png;base64,should_not_leak_call'
            }
          },
          metadata: {
            session_id: 'comp_sess_local',
            screenshot_omitted: true,
            screenshot_ref: 'omitted://computer/local',
            prompt: 'responses computer local runtime json prompt should not leak',
            base64_blob: 'QkFTRTY0X1NIT1VMRF9OT1RfTEVBSw=='
          }
        }
      }
    })

    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer local runtime json prompt should not leak',
        tools: [{ type: 'computer', display_width: 1024, display_height: 768, environment: 'browser' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `computer local_runtime JSON 应返回本地 Responses，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as {
      status?: string
      output?: Array<{ type?: string; call_id?: string; actions?: Array<{ type?: string; text_omitted?: boolean; text_length?: number; metadata?: Record<string, unknown> }>; metadata?: Record<string, unknown> }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string; gateway_computer_adapter?: { session_id?: string; screenshot_omitted?: boolean; screenshot_ref?: string } }
      tools?: Array<{ type?: string; display_width?: number; display_height?: number; environment?: string }>
    }
    assert.equal(body.status, 'completed')
    assert.equal(body.metadata?.gateway_runtime, 'local_runtime')
    assert.equal(body.metadata?.gateway_tool, 'computer')
    assert.equal(body.metadata?.gateway_computer_adapter?.session_id, 'comp_sess_local')
    assert.equal(body.metadata?.gateway_computer_adapter?.screenshot_omitted, true)
    assert.deepEqual(body.tools?.[0], { type: 'computer', display_width: 1024, display_height: 768, environment: 'browser' })
    const computerItem = body.output?.find((item) => item.type === 'computer_call')
    assert(computerItem, `computer local_runtime JSON 应返回 computer_call: ${text}`)
    assert.equal(computerItem.call_id, 'call_local_computer_test')
    assert.equal(computerItem.actions?.[0]?.type, 'screenshot')
    assert.equal(computerItem.actions?.[0]?.text_omitted, true)
    assert.equal(computerItem.actions?.[0]?.metadata?.screenshot_ref, 'omitted://computer/local')
    assert.match(body.output_text ?? '', /controlled screenshot request/i)
    assert.doesNotMatch(text, /responses computer local runtime json prompt should not leak/, 'computer local_runtime JSON 输出不能泄漏用户 prompt')
    assert.doesNotMatch(text, /should_not_leak|typed secret|data:image\/png;base64|QkFTRTY0/, 'computer local_runtime JSON 不应回显截图、token、prompt 或 base64 正文')
    assert.equal(upstreamHits.length, 0, 'computer local_runtime JSON 不应请求 Anthropic 上游')

    upstreamHits.length = 0
    const sseResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer local runtime sse prompt should not leak',
        tools: [{ type: 'computer' }],
        tool_choice: { type: 'computer' },
        stream: true
      })
    })
    const sseText = await sseResponse.text()
    assert.equal(sseResponse.status, 200, `computer local_runtime SSE 应返回本地 Responses SSE，实际 HTTP ${sseResponse.status}: ${sseText}`)
    assert.match(sseText, /event: response\.created/)
    assert.match(sseText, /event: response\.output_item\.added/)
    assert.match(sseText, /event: response\.output_item\.done/)
    assert.match(sseText, /event: response\.output_text\.delta/)
    assert.match(sseText, /event: response\.completed/)
    assert.match(sseText, /"gateway_runtime":"local_runtime"/)
    assert.match(sseText, /"type":"computer_call"/)
    assert.match(sseText, /"type":"screenshot"/)
    assert.doesNotMatch(sseText, /data: \[DONE\]/, 'Responses computer local_runtime SSE 不应使用 Chat [DONE]')
    assert.doesNotMatch(sseText, /responses computer local runtime sse prompt should not leak|should_not_leak|typed secret|data:image\/png;base64|QkFTRTY0/, 'computer local_runtime SSE 不应泄漏 prompt 或截图正文')
    assert.equal(upstreamHits.length, 0, 'computer local_runtime SSE 不应请求 Anthropic 上游')

    upstreamHits.length = 0
    const outputResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'computer_call_output',
          call_id: 'call_local_computer_test',
          output: {
            type: 'input_image',
            image_url: 'data:image/png;base64,aW1hZ2U='
          }
        }],
        tools: [{ type: 'computer' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const outputText = await outputResponse.text()
    assert.equal(outputResponse.status, 200, `computer_call_output local_runtime JSON 应正常收口，实际 HTTP ${outputResponse.status}: ${outputText}`)
    const outputBody = JSON.parse(outputText) as {
      status?: string
      output?: Array<{ type?: string }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string }
    }
    assert.equal(outputBody.status, 'completed')
    assert.equal(outputBody.metadata?.gateway_runtime, 'local_runtime')
    assert.equal(outputBody.metadata?.gateway_tool, 'computer')
    assert.equal(outputBody.output?.some((item) => item.type === 'computer_call'), false)
    assert.match(outputBody.output_text ?? '', /received computer_call_output/i)
    assert.doesNotMatch(outputText, /data:image\/png;base64|aW1hZ2U=|should_not_leak|prompt should not leak/, 'computer_call_output local_runtime 不应回显截图正文')
    assert.equal(upstreamHits.length, 0, 'computer_call_output local_runtime 不应请求 Anthropic 上游')
  } finally {
    openAICompatibleComputerAdapter.setOpenAICompatibleComputerExecutorForTest(undefined)
    runtimeConfig.hostedToolRuntimes.computer = previousMode
  }
}

async function assertComputerHostedToolRuntimeHttpAdapterMode(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const previousMode = runtimeConfig.hostedToolRuntimes.computer
  const previousAdapter = { ...runtimeConfig.computerAdapter }
  const adapterHits: Array<Record<string, unknown>> = []
  let adapterServer: http.Server | undefined
  try {
    adapterServer = http.createServer(async (req, res) => {
      try {
        if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/computer') {
          res.writeHead(404, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: 'not_found' }))
          return
        }
        const requestText = await readComputerAdapterMockRequestText(req, 1024 * 1024)
        const requestBody = JSON.parse(requestText) as Record<string, unknown>
        adapterHits.push(requestBody)
        const requestBodyText = JSON.stringify(requestBody)
        res.writeHead(200, { 'content-type': 'application/json' })
        if (requestBodyText.includes('responses computer http adapter oversized')) {
          res.end(JSON.stringify({ message: 'X'.repeat(2048) }))
          return
        }
        res.end(JSON.stringify({
          message: 'Computer HTTP adapter returned controlled screenshot request.',
          call: {
            call_id: 'http_adapter_call',
            status: 'completed',
            actions: [{
              type: 'screenshot',
              text: 'http adapter typed secret should be omitted',
              metadata: {
                screenshot_ref: 'omitted://computer/http',
                image_url: 'data:image/png;base64,should_not_leak_http_action',
                token: 'should_not_leak_http_token'
              }
            }],
            metadata: {
              session_id: 'comp_sess_http',
              screenshot_ref: 'omitted://computer/http',
              image_url: 'data:image/png;base64,should_not_leak_http_call'
            }
          },
          metadata: {
            session_id: 'comp_sess_http',
            screenshot_omitted: true,
            screenshot_ref: 'omitted://computer/http',
            prompt: 'responses computer http adapter prompt should not leak',
            base64_blob: 'SFRUUF9BREFQVEVSX0JBU0U2NF9TSE9VTERfTk9UX0xFQUs='
          }
        }))
      } catch (error) {
        res.writeHead(500, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: error instanceof Error ? error.message : 'adapter_error' }))
      }
    })
    await listen(adapterServer)
    runtimeConfig.hostedToolRuntimes.computer = 'local_runtime'
    Object.assign(runtimeConfig.computerAdapter, {
      enabled: true,
      endpoint: `http://127.0.0.1:${serverAddress(adapterServer).port}/computer`,
      timeoutMs: 3000,
      maxBodyBytes: 4096
    })
    openAICompatibleComputerAdapter.setOpenAICompatibleComputerExecutorForTest(undefined)

    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer http adapter prompt should not leak',
        tools: [{ type: 'computer', display_width: 1280, display_height: 720, environment: 'browser' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `computer HTTP adapter JSON 应返回本地 Responses，实际 HTTP ${response.status}: ${text}`)
    assert.equal(adapterHits.length, 1, 'computer HTTP adapter 应被调用一次')
    assert.equal((adapterHits[0]?.tool as { type?: string } | undefined)?.type, 'computer')
    assert.equal(adapterHits[0]?.stream, false)
    const body = JSON.parse(text) as {
      status?: string
      output?: Array<{ type?: string; call_id?: string; actions?: Array<{ type?: string; text_omitted?: boolean; metadata?: Record<string, unknown> }>; metadata?: Record<string, unknown> }>
      output_text?: string
      metadata?: { gateway_runtime?: string; gateway_tool?: string; gateway_computer_adapter?: { adapter?: string; session_id?: string; screenshot_omitted?: boolean; screenshot_ref?: string } }
    }
    assert.equal(body.status, 'completed')
    assert.equal(body.metadata?.gateway_runtime, 'local_runtime')
    assert.equal(body.metadata?.gateway_tool, 'computer')
    assert.equal(body.metadata?.gateway_computer_adapter?.adapter, 'http_browser')
    assert.equal(body.metadata?.gateway_computer_adapter?.session_id, 'comp_sess_http')
    assert.equal(body.metadata?.gateway_computer_adapter?.screenshot_omitted, true)
    const computerItem = body.output?.find((item) => item.type === 'computer_call')
    assert(computerItem, `computer HTTP adapter JSON 应返回 computer_call: ${text}`)
    assert.equal(computerItem.call_id, 'call_http_adapter_call')
    assert.equal(computerItem.actions?.[0]?.type, 'screenshot')
    assert.equal(computerItem.actions?.[0]?.text_omitted, true)
    assert.equal(computerItem.actions?.[0]?.metadata?.screenshot_ref, 'omitted://computer/http')
    assert.match(body.output_text ?? '', /HTTP adapter returned controlled screenshot request/)
    assert.doesNotMatch(text, /responses computer http adapter prompt should not leak/, 'computer HTTP adapter JSON 输出不能泄漏用户 prompt')
    assert.doesNotMatch(text, /should_not_leak|http adapter typed secret|data:image\/png;base64|SFRUUF9BREFQVEVS/, 'computer HTTP adapter JSON 不应回显截图、token、prompt 或 base64 正文')
    assert.equal(upstreamHits.length, 0, 'computer HTTP adapter JSON 不应请求 Anthropic 上游')

    runtimeConfig.computerAdapter.maxBodyBytes = 64
    const largeResponse = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: 'responses computer http adapter oversized',
        tools: [{ type: 'computer' }],
        tool_choice: { type: 'computer' },
        stream: false
      })
    })
    const largeText = await largeResponse.text()
    assert.equal(largeResponse.status, 502, `computer HTTP adapter 响应超限应返回 502，实际 HTTP ${largeResponse.status}: ${largeText}`)
    assert.match(largeText, /openai_anthropic_bridge_computer_runtime_failed/)
    assert.match(largeText, /exceeded limit/)
    assert.equal(upstreamHits.length, 0, 'computer HTTP adapter 响应超限不应请求 Anthropic 上游')
  } finally {
    openAICompatibleComputerAdapter.setOpenAICompatibleComputerExecutorForTest(undefined)
    runtimeConfig.hostedToolRuntimes.computer = previousMode
    Object.assign(runtimeConfig.computerAdapter, previousAdapter)
    await closeServer(adapterServer)
  }
}

async function readComputerAdapterMockRequestText(req: http.IncomingMessage, maxBytes: number): Promise<string> {
  const chunks: Buffer[] = []
  let total = 0
  for await (const chunk of req) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    total += buffer.byteLength
    if (total > maxBytes) {
      throw new Error('computer adapter mock request body too large')
    }
    chunks.push(buffer)
  }
  return Buffer.concat(chunks).toString('utf8')
}

async function assertResponsesImageGenerationJsonBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation json bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'low', output_format: 'png' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    output?: Array<{ type?: string; result?: string; revised_prompt?: string }>
    output_text?: string
    tools?: Array<{ type?: string }>
  }
  const imageItem = body.output?.find((item) => item.type === 'image_generation_call')
  assert(imageItem, `Responses JSON 应返回 image_generation_call: ${text}`)
  assert.equal(imageItem.result, mockImageBase64)
  assert.match(imageItem.revised_prompt ?? '', /provider revised/)
  assert.equal(body.output_text, '', 'image_generation 成功时不应把 revised prompt 当 output_text 暴露')
  assert.equal(body.tools?.[0]?.type, 'image_generation')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image_generation json bridge')
  assert.doesNotMatch(JSON.stringify(upstreamHits[0]?.body ?? {}), /"type":"image_generation"/, 'Anthropic 上游不应收到 OpenAI image_generation tool')
  assert.equal(imageGenerationHits.length, 1, 'image_generation JSON 应调用一次本地图像 provider')
  assert.equal(imageGenerationHits[0]?.path, '/v1/images/generations')
  assert.equal(imageGenerationHits[0]?.authorization, 'Bearer sk-image-generation-mock')
  assert.equal(imageGenerationHits[0]?.body.model, 'gpt-image-2-mock')
  assert.equal(imageGenerationHits[0]?.body.size, '1024x1024')
  assert.equal(imageGenerationHits[0]?.body.quality, 'low')
  assert.equal(imageGenerationHits[0]?.body.output_format, 'png')
  assert.match(String(imageGenerationHits[0]?.body.prompt ?? ''), /watercolor city skyline/)
}

async function assertResponsesImageGenerationSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation sse bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'medium' }],
      tool_choice: { type: 'image_generation' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.created/)
  assert.match(text, /event: response\.output_item\.added/)
  assert.match(text, /event: response\.image_generation_call\.completed/)
  assert.match(text, /event: response\.completed/)
  assert.match(text, /"type":"image_generation_call"/)
  assert.match(text, new RegExp(mockImageBase64.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  assert.doesNotMatch(text, /response\.output_text\.delta/, 'image_generation SSE 不应把 revised prompt 当文本增量输出')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses image_generation sse bridge')
  assert.equal(imageGenerationHits.length, 1, 'image_generation SSE 应调用一次本地图像 provider')
  assert.equal(imageGenerationHits[0]?.body.quality, 'medium')
  assert.match(String(imageGenerationHits[0]?.body.prompt ?? ''), /watercolor river at dawn/)
}

async function assertResponsesImageGenerationPartialImageSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation partial sse bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'medium', output_format: 'png', partial_images: 2 }],
      tool_choice: { type: 'image_generation' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation partial SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.output_item\.added/)
  assert.match(text, /event: response\.image_generation_call\.partial_image/)
  assert.match(text, /"partial_image_index":0/)
  assert(text.includes(mockPartialImageBase64), 'partial SSE 应透出 provider partial image base64')
  assert.match(text, /event: response\.image_generation_call\.completed/)
  assert(text.includes(mockImageBase64), 'partial SSE 最终 completed 应透出 provider final image base64')
  assert.match(text, /event: response\.output_item\.done/)
  assert.match(text, /event: response\.completed/)
  assert.doesNotMatch(text, /response\.output_text\.delta/, 'partial image_generation SSE 不应把 revised prompt 当文本增量输出')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses image_generation partial sse bridge')
  assert.equal(imageGenerationHits.length, 1, 'image_generation partial SSE 应调用一次本地图像 provider')
  assert.equal(imageGenerationHits[0]?.body.stream, true)
  assert.equal(imageGenerationHits[0]?.body.partial_images, 2)
  assert.equal(imageGenerationHits[0]?.body.output_format, 'png')
  assert.match(String(imageGenerationHits[0]?.body.prompt ?? ''), /watercolor river at dawn/)
}

async function assertResponsesImageGenerationResponsesProviderJsonBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation json bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'low', output_format: 'png' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses provider image_generation JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { output?: Array<{ type?: string; result?: string; revised_prompt?: string }> }
  const imageItem = body.output?.find((item) => item.type === 'image_generation_call')
  assert(imageItem, `Responses provider JSON 应返回 image_generation_call: ${text}`)
  assert.equal(imageItem.result, mockImageBase64)
  assert.match(imageItem.revised_prompt ?? '', /responses provider revised/)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image_generation json bridge')
  assert.equal(imageGenerationHits.length, 1, 'Responses provider JSON 应调用一次本地图像 provider')
  assert.equal(imageGenerationHits[0]?.path, '/v1/responses')
  assert.equal(imageGenerationHits[0]?.authorization, 'Bearer sk-image-generation-mock')
  assert.equal(imageGenerationHits[0]?.body.model, 'gpt-image-2-chat-mock')
  assert.match(String(imageGenerationHits[0]?.body.input ?? ''), /watercolor city skyline/)
  const tools = imageGenerationHits[0]?.body.tools as Array<Record<string, unknown>> | undefined
  assert.equal(tools?.[0]?.type, 'image_generation')
  assert.equal(tools?.[0]?.action, 'generate')
  assert.equal(tools?.[0]?.size, '1024x1024')
  assert.equal(tools?.[0]?.quality, 'low')
  assert.equal(tools?.[0]?.output_format, 'png')
  assert.deepEqual(imageGenerationHits[0]?.body.tool_choice, { type: 'image_generation' })
}

async function assertResponsesImageGenerationResponsesProviderPartialImageSseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation partial sse bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'medium', output_format: 'png', partial_images: 2 }],
      tool_choice: { type: 'image_generation' },
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses provider image_generation partial SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.image_generation_call\.partial_image/)
  assert(text.includes(mockPartialImageBase64), 'Responses provider partial SSE 应透出 provider partial image base64')
  assert.match(text, /event: response\.image_generation_call\.completed/)
  assert(text.includes(mockImageBase64), 'Responses provider partial SSE 最终 completed 应透出 provider final image base64')
  assert.match(text, /event: response\.completed/)
  assert.doesNotMatch(text, /response\.output_text\.delta/, 'Responses provider partial image_generation SSE 不应把 revised prompt 当文本增量输出')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'responses image_generation partial sse bridge')
  assert.equal(imageGenerationHits.length, 1, 'Responses provider partial SSE 应调用一次本地图像 provider')
  assert.equal(imageGenerationHits[0]?.path, '/v1/responses')
  assert.equal(imageGenerationHits[0]?.body.stream, true)
  const tools = imageGenerationHits[0]?.body.tools as Array<Record<string, unknown>> | undefined
  assert.equal(tools?.[0]?.type, 'image_generation')
  assert.equal(tools?.[0]?.partial_images, 2)
  assert.equal(tools?.[0]?.output_format, 'png')
  assert.match(String(imageGenerationHits[0]?.body.input ?? ''), /watercolor river at dawn/)
}

async function assertResponsesImageGenerationModerationBlockedBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation moderation bridge',
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'low', moderation: 'auto' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation moderation blocked 应返回 Responses 失败对象，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    status?: string
    error?: {
      type?: string
      code?: string
      message?: string
      moderation_details?: { moderation_stage?: string; categories?: string[] }
    }
    output?: Array<{ type?: string; result?: string }>
  }
  assert.equal(body.status, 'failed')
  assert.equal(body.error?.type, 'image_generation_user_error')
  assert.equal(body.error?.code, 'moderation_blocked')
  assert.match(body.error?.message ?? '', /blocked by moderation/)
  assert.equal(body.error?.moderation_details?.moderation_stage, 'input')
  assert.deepEqual(body.error?.moderation_details?.categories, ['harassment'])
  assert.equal(body.output?.length ?? 0, 0, 'moderation blocked 不应返回图片 output')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image_generation moderation bridge')
  assert.equal(imageGenerationHits.length, 1, 'moderation blocked 应调用一次本地图像 provider 并透传其错误')
  assert.match(String(imageGenerationHits[0]?.body.prompt ?? ''), /moderation blocked poster/)
  assert.equal(imageGenerationHits[0]?.body.moderation, 'auto')
}

async function assertResponsesImageGenerationUnsupportedEditGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const editResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation edit unsupported',
      tools: [{ type: 'image_generation', action: 'edit', size: '1024x1024' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const editText = await editResponse.text()
  assert.equal(editResponse.status, 200, `Responses image_generation edit 应返回 guidance，实际 HTTP ${editResponse.status}: ${editText}`)
  const editBody = JSON.parse(editText) as { output_text?: string; status?: string; metadata?: { gateway_guidance?: boolean } }
  assert.equal(editBody.status, 'completed')
  assert.equal(editBody.metadata?.gateway_guidance, true)
  assert.match(editBody.output_text ?? '', /能力未执行：image_generation/)
  assert.match(editBody.output_text ?? '', /图像生成 provider/)

  const maskResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'responses image_generation mask unsupported',
      tools: [{ type: 'image_generation', input_image_mask: { file_id: 'file_mask_1' }, size: '1024x1024' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const maskText = await maskResponse.text()
  assert.equal(maskResponse.status, 200, `Responses image_generation mask 应返回 guidance，实际 HTTP ${maskResponse.status}: ${maskText}`)
  const maskBody = JSON.parse(maskText) as { output_text?: string; status?: string; metadata?: { gateway_guidance?: boolean } }
  assert.equal(maskBody.status, 'completed')
  assert.equal(maskBody.metadata?.gateway_guidance, true)
  assert.match(maskBody.output_text ?? '', /能力未执行：image_generation/)
  assert.equal(upstreamHits.length, 0, 'unsupported image_generation edit/mask 不应请求 Anthropic 上游')
  assert.equal(imageGenerationHits.length, 0, 'unsupported image_generation edit/mask 不应调用图像 provider')
}

async function assertResponsesImageGenerationHistoryContextGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses image_generation history reuse unsupported' },
          { type: 'image_generation_call', id: 'ig_previous_fixture', status: 'completed', result: mockImageBase64 }
        ]
      }],
      tools: [{ type: 'image_generation', size: '1024x1024' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation 历史图片复用应返回 guidance，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { output_text?: string; status?: string; metadata?: { gateway_guidance?: boolean } }
  assert.equal(body.status, 'completed')
  assert.equal(body.metadata?.gateway_guidance, true)
  assert.match(body.output_text ?? '', /能力未执行：image_generation/)
  assert.match(body.output_text ?? '', /图像生成 provider/)
  assert.equal(upstreamHits.length, 0, 'history image_generation_call 复用不应请求 Anthropic 上游')
  assert.equal(imageGenerationHits.length, 0, 'history image_generation_call 复用不应调用图像 provider')
}

async function assertResponsesImageGenerationProviderInvalidResponseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  await assertResponsesImageGenerationProviderFailure(baseUrl, localApiKey, {
    input: 'responses image_generation provider invalid json bridge',
    expectedPrompt: /provider invalid json fixture/,
    expectedCode: 'openai_anthropic_bridge_image_generation_provider_invalid_response',
    expectedMessage: /缺少 data\[0\]\.b64_json/
  })
}

async function assertResponsesImageGenerationProviderTooLargeBridge(baseUrl: string, localApiKey: string): Promise<void> {
  await assertResponsesImageGenerationProviderFailure(baseUrl, localApiKey, {
    input: 'responses image_generation provider too large bridge',
    expectedPrompt: /provider too large fixture/,
    expectedCode: 'openai_anthropic_bridge_image_generation_provider_response_too_large',
    expectedMessage: /响应体超过读取上限/
  })
}

async function assertResponsesImageGenerationProviderTimeoutBridge(baseUrl: string, localApiKey: string): Promise<void> {
  await assertResponsesImageGenerationProviderFailure(baseUrl, localApiKey, {
    input: 'responses image_generation provider timeout bridge',
    expectedPrompt: /provider timeout fixture/,
    expectedCode: 'openai_anthropic_bridge_image_generation_provider_timeout',
    expectedMessage: /请求超时/
  })
}

async function assertResponsesImageGenerationProviderFailure(
  baseUrl: string,
  localApiKey: string,
  input: {
    input: string
    expectedPrompt: RegExp
    expectedCode: string
    expectedMessage: RegExp
  }
): Promise<void> {
  upstreamHits.length = 0
  imageGenerationHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: input.input,
      tools: [{ type: 'image_generation', size: '1024x1024', quality: 'low' }],
      tool_choice: { type: 'image_generation' },
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses image_generation provider failure 应返回 Responses failed 对象，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as {
    status?: string
    error?: { type?: string; code?: string; message?: string }
    output?: Array<{ type?: string; result?: string }>
    metadata?: { gateway_generated_failure?: boolean; gateway_failure_source?: string }
  }
  assert.equal(body.status, 'failed')
  assert.equal(body.error?.type, 'upstream_error')
  assert.equal(body.error?.code, input.expectedCode)
  assert.match(body.error?.message ?? '', input.expectedMessage)
  assert.equal(body.metadata?.gateway_generated_failure, true)
  assert.equal(body.metadata?.gateway_failure_source, 'image_generation_provider')
  assert.equal(body.output?.length ?? 0, 0, 'provider failure 不应返回图片 output')
  assertBridgeUpstreamHit(upstreamHits[0], false, input.input)
  assert.equal(imageGenerationHits.length, 1, 'provider failure 应调用一次本地图像 provider 并返回其失败语义')
  assert.match(String(imageGenerationHits[0]?.body.prompt ?? ''), input.expectedPrompt)
}

async function assertResponsesImageFileIdNotFound(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_image', file_id: 'file_bridge_image_1' }]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 404, `input_image.file_id 不存在应返回本地 404，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_file_not_found/)
  assert.equal(upstreamHits.length, 0, 'input_image.file_id 不存在不应请求 Anthropic 上游')
}

async function assertResponsesFileIdNotFound(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_file', file_id: 'file_bridge_missing_text_1' }]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 404, `input_file.file_id 不存在应返回本地 404，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_file_not_found/)
  assert.equal(upstreamHits.length, 0, 'input_file.file_id 不存在不应请求 Anthropic 上游')
}

async function assertResponsesImageFileIdResolverBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const imagePayload = Buffer.from('resolved image fixture', 'utf8').toString('base64')
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_image_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_image')
      return {
        fileId: input.fileId,
        filename: 'bridge-file-id.png',
        mediaType: 'image/png',
        contentBase64: imagePayload
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses image file id bridge' },
            { type: 'input_image', file_id: 'file_bridge_image_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Responses input_image.file_id resolver 桥接应成功，实际 HTTP ${response.status}: ${text}`)
    assertBridgeUpstreamHit(upstreamHits[0], false, 'responses image file id bridge')
    const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
    assert.match(upstreamBodyText, /"type":"image"/)
    assert.match(upstreamBodyText, /"source":\{"type":"base64","media_type":"image\/png"/)
    assert.match(upstreamBodyText, new RegExp(`"data":"${imagePayload}"`))
    assert.doesNotMatch(upstreamBodyText, /file_bridge_image_1/, 'Anthropic 上游不应收到 OpenAI file_id')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertResponsesImageFileIdUnsupportedMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_image_text_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_image')
      return {
        fileId: input.fileId,
        filename: 'not-an-image.txt',
        mediaType: 'text/plain',
        contentBase64: Buffer.from('not an image', 'utf8').toString('base64')
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses image file id unsupported mime' },
            { type: 'input_image', file_id: 'file_bridge_image_text_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Responses input_image.file_id 非图片 MIME 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
    assert.equal(upstreamHits.length, 0, 'Responses input_image.file_id 非图片 MIME 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertResponsesImageFileIdInvalidBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_image_bad_base64_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_image')
      return {
        fileId: input.fileId,
        filename: 'bad-image.png',
        mediaType: 'image/png',
        contentBase64: 'not-valid-@@'
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses image file id invalid base64' },
            { type: 'input_image', file_id: 'file_bridge_image_bad_base64_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Responses input_image.file_id 非法 base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
    assert.equal(upstreamHits.length, 0, 'Responses input_image.file_id 非法 base64 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertResponsesFileIdResolverTextBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_text_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-response-note.txt',
        mediaType: 'text/plain',
        contentText: 'resolved responses file id content'
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses file id text bridge' },
            { type: 'input_file', file_id: 'file_bridge_text_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Responses input_file.file_id resolver 桥接应成功，实际 HTTP ${response.status}: ${text}`)
    assertBridgeUpstreamHit(upstreamHits[0], false, 'responses file id text bridge')
    const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
    assert.match(upstreamBodyText, /"type":"document"/)
    assert.match(upstreamBodyText, /"source":\{"type":"text","media_type":"text\/plain","data":"resolved responses file id content"/)
    assert.match(upstreamBodyText, /"title":"resolved-response-note\.txt"/)
    assert.doesNotMatch(upstreamBodyText, /file_bridge_text_1/, 'Anthropic 上游不应收到 OpenAI file_id')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertResponsesFileIdUnsupportedMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_json_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-response-payload.json',
        mediaType: 'application/json',
        contentBase64: Buffer.from('{"ok":true}', 'utf8').toString('base64')
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses file id unsupported mime' },
            { type: 'input_file', file_id: 'file_bridge_json_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Responses input_file.file_id 不支持 MIME 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
    assert.equal(upstreamHits.length, 0, 'Responses input_file.file_id 不支持 MIME 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertResponsesFileIdInvalidBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_pdf_bad_base64_1')
      assert.equal(input.sourceEndpointFamily, 'responses')
      assert.equal(input.usage, 'input_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-response-bad.pdf',
        mediaType: 'application/pdf',
        contentBase64: 'not-valid-@@'
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [{
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'responses file id invalid base64' },
            { type: 'input_file', file_id: 'file_bridge_pdf_bad_base64_1' }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Responses input_file.file_id 非法 base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
    assert.equal(upstreamHits.length, 0, 'Responses input_file.file_id 非法 base64 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertChatFileIdNotFound(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat file id missing' },
          {
            type: 'file',
            file: {
              file_id: 'file_bridge_chat_missing_1'
            }
          }
        ]
      }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 404, `Chat file.file_id 不存在应返回本地 404，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_file_not_found/)
  assert.equal(upstreamHits.length, 0, 'Chat file.file_id 不存在不应请求 Anthropic 上游')
}

async function assertChatFileIdResolverTextBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_chat_text_1')
      assert.equal(input.sourceEndpointFamily, 'chat_completions')
      assert.equal(input.usage, 'chat_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-chat-note.txt',
        mediaType: 'text/plain',
        contentText: 'resolved chat file id content'
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{
          role: 'user',
          content: [
            { type: 'text', text: 'chat file id text bridge' },
            {
              type: 'file',
              file: {
                file_id: 'file_bridge_chat_text_1'
              }
            }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Chat file.file_id resolver 桥接应成功，实际 HTTP ${response.status}: ${text}`)
    assertBridgeUpstreamHit(upstreamHits[0], false, 'chat file id text bridge')
    const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
    assert.match(upstreamBodyText, /"type":"document"/)
    assert.match(upstreamBodyText, /"source":\{"type":"text","media_type":"text\/plain","data":"resolved chat file id content"/)
    assert.match(upstreamBodyText, /"title":"resolved-chat-note\.txt"/)
    assert.doesNotMatch(upstreamBodyText, /file_bridge_chat_text_1/, 'Anthropic 上游不应收到 OpenAI file_id')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertChatFileIdUnsupportedMimeRejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_chat_json_1')
      assert.equal(input.sourceEndpointFamily, 'chat_completions')
      assert.equal(input.usage, 'chat_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-chat-payload.json',
        mediaType: 'application/json',
        contentBase64: Buffer.from('{"ok":true}', 'utf8').toString('base64')
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{
          role: 'user',
          content: [
            { type: 'text', text: 'chat file id unsupported mime' },
            {
              type: 'file',
              file: {
                file_id: 'file_bridge_chat_json_1'
              }
            }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Chat file.file_id 不支持 MIME 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_unsupported_file_media_type/)
    assert.equal(upstreamHits.length, 0, 'Chat file.file_id 不支持 MIME 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertChatFileIdInvalidBase64Rejected(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest({
    async resolveFile(input) {
      assert.equal(input.fileId, 'file_bridge_chat_pdf_bad_base64_1')
      assert.equal(input.sourceEndpointFamily, 'chat_completions')
      assert.equal(input.usage, 'chat_file')
      return {
        fileId: input.fileId,
        filename: 'resolved-chat-bad.pdf',
        mediaType: 'application/pdf',
        contentBase64: 'not-valid-@@'
      }
    }
  })
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{
          role: 'user',
          content: [
            { type: 'text', text: 'chat file id invalid base64' },
            {
              type: 'file',
              file: {
                file_id: 'file_bridge_chat_pdf_bad_base64_1'
              }
            }
          ]
        }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `Chat file.file_id 非法 base64 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /openai_anthropic_bridge_invalid_file_input/)
    assert.equal(upstreamHits.length, 0, 'Chat file.file_id 非法 base64 不应请求 Anthropic 上游')
  } finally {
    openAIAnthropicBridge.setOpenAIToAnthropicBridgeFileResolverForTest(undefined)
  }
}

async function assertOpenAICompatibleFilesUploadResolverBridge(baseUrl: string, localApiKey: string): Promise<void> {
  const textFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey,
    filename: 'uploaded-note.txt',
    mediaType: 'text/plain',
    content: 'uploaded local file id text content'
  })
  assert.equal(textFile.object, 'file')
  assert.equal(textFile.filename, 'uploaded-note.txt')
  assert.equal(textFile.purpose, 'assistants')
  assert.equal(textFile.bytes, Buffer.byteLength('uploaded local file id text content'))

  const listResponse = await fetch(`${baseUrl}/v1/files?purpose=assistants&limit=10`, {
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const listText = await listResponse.text()
  assert.equal(listResponse.status, 200, `Files list 应成功，实际 HTTP ${listResponse.status}: ${listText}`)
  const listBody = JSON.parse(listText) as { object?: string; data?: Array<{ id?: string }>; has_more?: boolean }
  assert.equal(listBody.object, 'list')
  assert.equal(listBody.data?.some((item) => item.id === textFile.id), true, 'Files list 应包含刚上传文件')
  assert.equal(listBody.has_more, false)

  const contentResponse = await fetch(`${baseUrl}/v1/files/${textFile.id}/content`, {
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const contentText = await contentResponse.text()
  assert.equal(contentResponse.status, 200, `Files content 应成功，实际 HTTP ${contentResponse.status}: ${contentText}`)
  assert.equal(contentText, 'uploaded local file id text content')

  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'chat uploaded local file id bridge' },
          {
            type: 'file',
            file: {
              file_id: textFile.id
            }
          }
        ]
      }],
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `上传后 Chat file_id bridge 应成功，实际 HTTP ${chatResponse.status}: ${chatText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat uploaded local file id bridge')
  const chatUpstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(chatUpstreamBodyText, /"type":"document"/)
  assert.match(chatUpstreamBodyText, /uploaded local file id text content/)
  assert.doesNotMatch(chatUpstreamBodyText, new RegExp(textFile.id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), 'Anthropic 上游不应收到上传文件 file_id')

  const imagePayload = 'uploaded image bytes'
  const imageFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey,
    filename: 'uploaded-image.png',
    mediaType: 'image/png',
    content: imagePayload
  })
  upstreamHits.length = 0
  const responsesImageResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'responses uploaded image file id bridge' },
          { type: 'input_image', file_id: imageFile.id }
        ]
      }],
      stream: false
    })
  })
  const responsesImageText = await responsesImageResponse.text()
  assert.equal(responsesImageResponse.status, 200, `上传后 Responses input_image.file_id bridge 应成功，实际 HTTP ${responsesImageResponse.status}: ${responsesImageText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses uploaded image file id bridge')
  const imageUpstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(imageUpstreamBodyText, /"type":"image"/)
  assert.match(imageUpstreamBodyText, /"source":\{"type":"base64","media_type":"image\/png"/)
  assert.match(imageUpstreamBodyText, new RegExp(Buffer.from(imagePayload).toString('base64')))
  assert.doesNotMatch(imageUpstreamBodyText, new RegExp(imageFile.id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), 'Anthropic 上游不应收到上传图片 file_id')

  await deleteOpenAICompatibleTestFile(baseUrl, localApiKey, textFile.id)
  await deleteOpenAICompatibleTestFile(baseUrl, localApiKey, imageFile.id)
}

async function assertOpenAICompatibleVectorStoreFileSearchBridge(baseUrl: string, localApiKey: string): Promise<void> {
  const knowledgeText = [
    'Anthropic file search bridge fixture.',
    'The local vector store contains alpha bridge compatibility knowledge.',
    'Clients should receive OpenAI shaped file_search_call output while Anthropic only sees injected context.'
  ].join('\n')
  const textFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey,
    filename: 'bridge-knowledge.md',
    mediaType: 'text/markdown',
    content: knowledgeText
  })

  const vectorStoreResponse = await fetch(`${baseUrl}/v1/vector_stores`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      name: 'Bridge Knowledge',
      metadata: { suite: 'openai-anthropic-bridge' }
    })
  })
  const vectorStoreText = await vectorStoreResponse.text()
  assert.equal(vectorStoreResponse.status, 200, `Vector store create 应成功，实际 HTTP ${vectorStoreResponse.status}: ${vectorStoreText}`)
  const vectorStore = JSON.parse(vectorStoreText) as { id?: string; object?: string; file_counts?: { total?: number } }
  assert(vectorStore.id, 'Vector store create 应返回 id')
  assert.equal(vectorStore.object, 'vector_store')
  assert.equal(vectorStore.file_counts?.total, 0)

  const vectorStoreFileResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      file_id: textFile.id,
      attributes: { topic: 'bridge' }
    })
  })
  const vectorStoreFileText = await vectorStoreFileResponse.text()
  assert.equal(vectorStoreFileResponse.status, 200, `Vector store file create 应成功，实际 HTTP ${vectorStoreFileResponse.status}: ${vectorStoreFileText}`)
  const vectorStoreFile = JSON.parse(vectorStoreFileText) as { id?: string; object?: string; status?: string; attributes?: { topic?: string } }
  assert.equal(vectorStoreFile.id, textFile.id)
  assert.equal(vectorStoreFile.object, 'vector_store.file')
  assert.equal(vectorStoreFile.status, 'in_progress')
  assert.equal(vectorStoreFile.attributes?.topic, 'bridge')
  const completedVectorStoreFile = await waitForOpenAICompatibleVectorStoreFileStatus(
    baseUrl,
    localApiKey,
    vectorStore.id,
    textFile.id,
    'completed'
  )
  assert.equal(completedVectorStoreFile.attributes?.topic, 'bridge')

  const vectorStoreFilesResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files`, {
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const vectorStoreFilesText = await vectorStoreFilesResponse.text()
  assert.equal(vectorStoreFilesResponse.status, 200, `Vector store files list 应成功，实际 HTTP ${vectorStoreFilesResponse.status}: ${vectorStoreFilesText}`)
  const vectorStoreFiles = JSON.parse(vectorStoreFilesText) as { data?: Array<{ id?: string }> }
  assert.equal(vectorStoreFiles.data?.some((item) => item.id === textFile.id), true)

  const vectorStoreContentResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files/${textFile.id}/content`, {
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const vectorStoreContentText = await vectorStoreContentResponse.text()
  assert.equal(vectorStoreContentResponse.status, 200, `Vector store file content 应成功，实际 HTTP ${vectorStoreContentResponse.status}: ${vectorStoreContentText}`)
  const vectorStoreContent = JSON.parse(vectorStoreContentText) as { data?: Array<{ text?: string }> }
  assert.match(vectorStoreContent.data?.[0]?.text ?? '', /alpha bridge compatibility knowledge/)

  const searchResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/search`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      query: 'alpha bridge compatibility',
      max_num_results: 2,
      filters: { type: 'eq', key: 'topic', value: 'bridge' }
    })
  })
  const searchText = await searchResponse.text()
  assert.equal(searchResponse.status, 200, `Vector store search 应成功，实际 HTTP ${searchResponse.status}: ${searchText}`)
  const searchBody = JSON.parse(searchText) as { object?: string; search_query?: string; data?: Array<{ file_id?: string; content?: Array<{ text?: string }> }> }
  assert.equal(searchBody.object, 'vector_store.search_results.page')
  assert.equal(searchBody.search_query, 'alpha bridge compatibility')
  assert.equal(searchBody.data?.[0]?.file_id, textFile.id)
  assert.match(searchBody.data?.[0]?.content?.[0]?.text ?? '', /alpha bridge compatibility knowledge/)

  upstreamHits.length = 0
  const responsesJson = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'anthropic file search bridge',
      tools: [{
        type: 'file_search',
        vector_store_ids: [vectorStore.id],
        max_num_results: 2,
        filters: { type: 'eq', key: 'topic', value: 'bridge' }
      }],
      include: ['file_search_call.results'],
      stream: false
    })
  })
  const responsesJsonText = await responsesJson.text()
  assert.equal(responsesJson.status, 200, `Responses file_search JSON 应成功，实际 HTTP ${responsesJson.status}: ${responsesJsonText}`)
  const responsesJsonBody = JSON.parse(responsesJsonText) as {
    output?: Array<{ type?: string; results?: Array<{ file_id?: string }>; content?: Array<{ text?: string; annotations?: Array<{ type?: string; file_id?: string }> }> }>
    output_text?: string
  }
  assert.equal(responsesJsonBody.output?.[0]?.type, 'file_search_call')
  assert.equal(responsesJsonBody.output?.[0]?.results?.[0]?.file_id, textFile.id)
  assert.equal(responsesJsonBody.output_text, 'anthropic file search answer [F1]')
  const responsesMessage = responsesJsonBody.output?.find((item) => item.type === 'message')
  assert.equal(responsesMessage?.content?.[0]?.annotations?.[0]?.type, 'file_citation')
  assert.equal(responsesMessage?.content?.[0]?.annotations?.[0]?.file_id, textFile.id)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'anthropic file search bridge')
  const responsesUpstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(responsesUpstreamBodyText, /File search results are provided by the gateway/)
  assert.match(responsesUpstreamBodyText, /alpha bridge compatibility knowledge/)
  assert.doesNotMatch(responsesUpstreamBodyText, /"type":"file_search"/)
  assert.doesNotMatch(responsesUpstreamBodyText, /vector_store_ids/)

  upstreamHits.length = 0
  const responsesSse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'anthropic file search sse bridge',
      tools: [{ type: 'file_search', vector_store_ids: [vectorStore.id] }],
      stream: true
    })
  })
  const responsesSseText = await responsesSse.text()
  assert.equal(responsesSse.status, 200, `Responses file_search SSE 应成功，实际 HTTP ${responsesSse.status}: ${responsesSseText}`)
  assert.match(responsesSseText, /"type":"file_search_call"/)
  assert.match(responsesSseText, /anthropic file search sse answer \[F1\]/)
  assert.match(responsesSseText, /"type":"file_citation"/)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'anthropic file search sse bridge')

  upstreamHits.length = 0
  const chatResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'anthropic chat file search bridge' }],
      tools: [{ type: 'file_search', vector_store_ids: [vectorStore.id], max_num_results: 1 }],
      stream: false
    })
  })
  const chatText = await chatResponse.text()
  assert.equal(chatResponse.status, 200, `Chat file_search JSON 应成功，实际 HTTP ${chatResponse.status}: ${chatText}`)
  const chatBody = JSON.parse(chatText) as { choices?: Array<{ message?: { content?: string; annotations?: Array<{ type?: string; file_id?: string }> } }> }
  assert.equal(chatBody.choices?.[0]?.message?.content, 'anthropic file search answer [F1]')
  assert.equal(chatBody.choices?.[0]?.message?.annotations?.[0]?.type, 'file_citation')
  assert.equal(chatBody.choices?.[0]?.message?.annotations?.[0]?.file_id, textFile.id)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'anthropic chat file search bridge')
  const chatUpstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(chatUpstreamBodyText, /alpha bridge compatibility knowledge/)
  assert.doesNotMatch(chatUpstreamBodyText, /"type":"file_search"/)

  const deleteVectorStoreFileResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files/${textFile.id}`, {
    method: 'DELETE',
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const deleteVectorStoreFileText = await deleteVectorStoreFileResponse.text()
  assert.equal(deleteVectorStoreFileResponse.status, 200, `Vector store file delete 应成功，实际 HTTP ${deleteVectorStoreFileResponse.status}: ${deleteVectorStoreFileText}`)

  const deleteVectorStoreResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}`, {
    method: 'DELETE',
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const deleteVectorStoreText = await deleteVectorStoreResponse.text()
  assert.equal(deleteVectorStoreResponse.status, 200, `Vector store delete 应成功，实际 HTTP ${deleteVectorStoreResponse.status}: ${deleteVectorStoreText}`)
  await deleteOpenAICompatibleTestFile(baseUrl, localApiKey, textFile.id)
}

async function assertOpenAICompatibleFilesAndVectorStoreBoundaries(
  baseUrl: string,
  ownerApiKey: string,
  ownerApiKeyId: string,
  otherApiKey: string
): Promise<void> {
  upstreamHits.length = 0
  const privateFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey: ownerApiKey,
    filename: 'boundary-private-note.txt',
    mediaType: 'text/plain',
    content: 'boundary private file search knowledge'
  })

  const otherFileResponse = await fetch(`${baseUrl}/v1/files/${privateFile.id}`, {
    headers: { authorization: `Bearer ${otherApiKey}` }
  })
  await assertOpenAICompatibleErrorResponse(
    otherFileResponse,
    404,
    'file_not_found',
    'Files get 使用其他 API Key'
  )
  const otherFilesListResponse = await fetch(`${baseUrl}/v1/files`, {
    headers: { authorization: `Bearer ${otherApiKey}` }
  })
  const otherFilesListText = await otherFilesListResponse.text()
  assert.equal(otherFilesListResponse.status, 200, `Files list 使用其他 API Key 应成功，实际 HTTP ${otherFilesListResponse.status}: ${otherFilesListText}`)
  const otherFilesList = JSON.parse(otherFilesListText) as { data?: Array<{ id?: string }> }
  assert.equal(otherFilesList.data?.some((item) => item.id === privateFile.id), false, '其他 API Key 不应列出 owner 上传的 file')

  const vectorStore = await createOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, 'Boundary Owner Store')
  const otherVectorStoreResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}`, {
    headers: { authorization: `Bearer ${otherApiKey}` }
  })
  await assertOpenAICompatibleErrorResponse(
    otherVectorStoreResponse,
    404,
    'vector_store_not_found',
    'Vector store get 使用其他 API Key'
  )
  const otherVectorStoreBindResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${otherApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ file_id: privateFile.id })
  })
  await assertOpenAICompatibleErrorResponse(
    otherVectorStoreBindResponse,
    404,
    'vector_store_not_found',
    'Vector store file bind 使用其他 API Key'
  )

  const vectorStoreFileResponse = await fetch(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${ownerApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ file_id: privateFile.id })
  })
  const vectorStoreFileText = await vectorStoreFileResponse.text()
  assert.equal(vectorStoreFileResponse.status, 200, `Boundary owner vector store file bind 应成功，实际 HTTP ${vectorStoreFileResponse.status}: ${vectorStoreFileText}`)
  const vectorStoreFile = JSON.parse(vectorStoreFileText) as { status?: string }
  assert.equal(vectorStoreFile.status, 'in_progress')
  await waitForOpenAICompatibleVectorStoreFileStatus(baseUrl, ownerApiKey, vectorStore.id, privateFile.id, 'completed')

  upstreamHits.length = 0
  const otherFileSearchResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${otherApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'boundary private vector store search',
      tools: [{ type: 'file_search', vector_store_ids: [vectorStore.id] }],
      stream: false
    })
  })
  await assertOpenAICompatibleErrorResponse(
    otherFileSearchResponse,
    404,
    'openai_anthropic_bridge_file_search_vector_store_not_found',
    'Responses file_search 使用其他 API Key'
  )
  assert.equal(upstreamHits.length, 0, '其他 API Key file_search 命中隔离边界时不应请求 Anthropic 上游')

  const notReadyFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey: ownerApiKey,
    filename: 'boundary-not-ready-note.txt',
    mediaType: 'text/plain',
    content: 'not ready vector store fixture'
  })
  const notReadyVectorStore = await createOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, 'Boundary Not Ready Store')
  const notReadyRecord = openAICompatibleVectorStoresRepository.createOpenAICompatibleVectorStoreFile({
    vectorStoreId: notReadyVectorStore.id,
    fileId: notReadyFile.id,
    systemAccountId: access.systemAccountId,
    apiKeyId: ownerApiKeyId,
    status: 'in_progress'
  })
  assert.equal(notReadyRecord?.status, 'in_progress')
  upstreamHits.length = 0
  const notReadyFileSearchResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${ownerApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'boundary not ready vector store search',
      tools: [{ type: 'file_search', vector_store_ids: [notReadyVectorStore.id] }],
      stream: false
    })
  })
  await assertOpenAICompatibleErrorResponse(
    notReadyFileSearchResponse,
    409,
    'openai_anthropic_bridge_file_search_vector_store_not_ready',
    'Responses file_search 使用未就绪 vector store'
  )
  assert.equal(upstreamHits.length, 0, '未就绪 vector store file_search 不应请求 Anthropic 上游')

  const unsupportedFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey: ownerApiKey,
    filename: 'boundary-unsupported.bin',
    mediaType: 'application/octet-stream',
    content: 'unsupported binary fixture'
  })
  const unsupportedVectorStore = await createOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, 'Boundary Unsupported Store')
  const unsupportedBindResponse = await fetch(`${baseUrl}/v1/vector_stores/${unsupportedVectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${ownerApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ file_id: unsupportedFile.id })
  })
  const unsupportedBindText = await unsupportedBindResponse.text()
  assert.equal(unsupportedBindResponse.status, 200, `Vector store bind unsupported MIME 应返回可轮询对象，实际 HTTP ${unsupportedBindResponse.status}: ${unsupportedBindText}`)
  const unsupportedBindBody = JSON.parse(unsupportedBindText) as { status?: string }
  assert.equal(unsupportedBindBody.status, 'in_progress')
  const unsupportedFailedFile = await waitForOpenAICompatibleVectorStoreFileStatus(
    baseUrl,
    ownerApiKey,
    unsupportedVectorStore.id,
    unsupportedFile.id,
    'failed'
  )
  assert.equal(unsupportedFailedFile.last_error?.code, 'openai_compatible_file_mime_unsupported')

  const largeTextFile = await uploadOpenAICompatibleTestFile({
    baseUrl,
    localApiKey: ownerApiKey,
    filename: 'boundary-large-note.txt',
    mediaType: 'text/plain',
    content: 'x'.repeat(2 * 1024 * 1024 + 1)
  })
  const largeVectorStore = await createOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, 'Boundary Large Store')
  const largeBindResponse = await fetch(`${baseUrl}/v1/vector_stores/${largeVectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${ownerApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ file_id: largeTextFile.id })
  })
  const largeBindText = await largeBindResponse.text()
  assert.equal(largeBindResponse.status, 200, `Vector store bind 文本索引超限应返回可轮询对象，实际 HTTP ${largeBindResponse.status}: ${largeBindText}`)
  const largeBindBody = JSON.parse(largeBindText) as { status?: string }
  assert.equal(largeBindBody.status, 'in_progress')
  const largeFailedFile = await waitForOpenAICompatibleVectorStoreFileStatus(
    baseUrl,
    ownerApiKey,
    largeVectorStore.id,
    largeTextFile.id,
    'failed'
  )
  assert.equal(largeFailedFile.last_error?.code, 'openai_compatible_vector_store_file_too_large')
  assert.equal(upstreamHits.length, 0, 'Files / Vector Store 边界错误不应请求 Anthropic 上游')

  await deleteOpenAICompatibleTestVectorStoreFile(baseUrl, ownerApiKey, vectorStore.id, privateFile.id)
  await deleteOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, vectorStore.id)
  await deleteOpenAICompatibleTestVectorStoreFile(baseUrl, ownerApiKey, notReadyVectorStore.id, notReadyFile.id)
  await deleteOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, notReadyVectorStore.id)
  await deleteOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, unsupportedVectorStore.id)
  await deleteOpenAICompatibleTestVectorStore(baseUrl, ownerApiKey, largeVectorStore.id)
  await deleteOpenAICompatibleTestFile(baseUrl, ownerApiKey, privateFile.id)
  await deleteOpenAICompatibleTestFile(baseUrl, ownerApiKey, notReadyFile.id)
  await deleteOpenAICompatibleTestFile(baseUrl, ownerApiKey, unsupportedFile.id)
  await deleteOpenAICompatibleTestFile(baseUrl, ownerApiKey, largeTextFile.id)
}

async function uploadOpenAICompatibleTestFile(input: {
  baseUrl: string
  localApiKey: string
  filename: string
  mediaType: string
  content: string
}): Promise<{ id: string; object?: string; filename?: string; purpose?: string; bytes?: number }> {
  const form = new FormData()
  form.append('purpose', 'assistants')
  form.append('file', new Blob([input.content], { type: input.mediaType }), input.filename)
  const response = await fetch(`${input.baseUrl}/v1/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`
    },
    body: form
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Files upload 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { id?: string; object?: string; filename?: string; purpose?: string; bytes?: number }
  assert(body.id, 'Files upload 应返回 file id')
  return body as { id: string; object?: string; filename?: string; purpose?: string; bytes?: number }
}

async function createOpenAICompatibleTestVectorStore(
  baseUrl: string,
  localApiKey: string,
  name: string
): Promise<{ id: string; object?: string }> {
  const response = await fetch(`${baseUrl}/v1/vector_stores`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ name })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Vector store create 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { id?: string; object?: string }
  assert(body.id, 'Vector store create 应返回 id')
  assert.equal(body.object, 'vector_store')
  return body as { id: string; object?: string }
}

async function waitForOpenAICompatibleVectorStoreFileStatus(
  baseUrl: string,
  localApiKey: string,
  vectorStoreId: string,
  fileId: string,
  expectedStatus: 'completed' | 'failed'
): Promise<{
  id?: string
  status?: string
  attributes?: { topic?: string }
  last_error?: { code?: string; type?: string; message?: string } | null
}> {
  let lastText = ''
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const response = await fetch(`${baseUrl}/v1/vector_stores/${vectorStoreId}/files/${fileId}`, {
      headers: { authorization: `Bearer ${localApiKey}` }
    })
    lastText = await response.text()
    assert.equal(response.status, 200, `Vector store file poll 应成功，实际 HTTP ${response.status}: ${lastText}`)
    const body = JSON.parse(lastText) as {
      id?: string
      status?: string
      attributes?: { topic?: string }
      last_error?: { code?: string; type?: string; message?: string } | null
    }
    if (body.status === expectedStatus) return body
    assert.equal(body.status, 'in_progress', `Vector store file poll 只应处于 in_progress 或 ${expectedStatus}，实际: ${lastText}`)
    await delay(25)
  }
  throw new Error(`Vector store file ${fileId} 未在超时内变为 ${expectedStatus}，最后结果: ${lastText}`)
}

async function deleteOpenAICompatibleTestVectorStoreFile(
  baseUrl: string,
  localApiKey: string,
  vectorStoreId: string,
  fileId: string
): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/vector_stores/${vectorStoreId}/files/${fileId}`, {
    method: 'DELETE',
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Vector store file delete 应成功，实际 HTTP ${response.status}: ${text}`)
}

async function deleteOpenAICompatibleTestVectorStore(
  baseUrl: string,
  localApiKey: string,
  vectorStoreId: string
): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/vector_stores/${vectorStoreId}`, {
    method: 'DELETE',
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Vector store delete 应成功，实际 HTTP ${response.status}: ${text}`)
}

async function deleteOpenAICompatibleTestFile(baseUrl: string, localApiKey: string, fileId: string): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/files/${fileId}`, {
    method: 'DELETE',
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Files delete 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { id?: string; deleted?: boolean }
  assert.equal(body.id, fileId)
  assert.equal(body.deleted, true)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function assertOpenAICompatibleErrorResponse(
  response: Response,
  expectedStatus: number,
  expectedCode: string,
  context: string
): Promise<void> {
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${context} 应返回 HTTP ${expectedStatus}，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { error?: { code?: string; type?: string; message?: string } }
  assert.equal(body.error?.code, expectedCode, `${context} 应返回 error.code=${expectedCode}，实际: ${text}`)
}

async function assertResponsesCompactionSummaryInputBridge(baseUrl: string, localApiKey: string): Promise<void> {
  for (const compactType of ['compaction', 'compaction_summary'] as const) {
    upstreamHits.length = 0
    const compactSummary = `anthropic compact restored summary ${compactType}`
    const encryptedContent = `juhecmp.v1.${Buffer.from(JSON.stringify({ summary: compactSummary }), 'utf8').toString('base64url')}`
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [
          { type: compactType, encrypted_content: encryptedContent },
          {
            type: 'message',
            role: 'user',
            content: [{ type: 'input_text', text: `continue after anthropic ${compactType}` }]
          }
        ],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `Responses ${compactType} 桥接应成功，实际 HTTP ${response.status}: ${text}`)
    assertBridgeUpstreamHit(upstreamHits[0], false, `continue after anthropic ${compactType}`)
    const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
    assert.match(upstreamBodyText, new RegExp(compactSummary.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `${compactType} 应恢复到 Anthropic system 上下文`)
    assert.doesNotMatch(upstreamBodyText, /juhecmp\.v1\./, '上游不应看到网关 compact envelope')
  }
}

async function assertResponsesCompactionInvalidInputRejected(baseUrl: string, localApiKey: string): Promise<void> {
  const invalidCases: Array<{
    label: string
    compactItem: Record<string, unknown>
    expectedCode: string
  }> = [
    {
      label: 'compaction missing encrypted_content',
      compactItem: { type: 'compaction' },
      expectedCode: 'openai_anthropic_bridge_compact_summary_missing'
    },
    {
      label: 'compaction invalid juhecmp v1',
      compactItem: { type: 'compaction', encrypted_content: 'juhecmp.v1.not-valid-base64-@@' },
      expectedCode: 'openai_anthropic_bridge_compact_summary_invalid'
    },
    {
      label: 'compaction_summary juhecmp v1 without summary',
      compactItem: {
        type: 'compaction_summary',
        encrypted_content: `juhecmp.v1.${Buffer.from(JSON.stringify({ note: 'missing summary' }), 'utf8').toString('base64url')}`
      },
      expectedCode: 'openai_anthropic_bridge_compact_summary_invalid'
    },
    {
      label: 'compaction unknown envelope',
      compactItem: { type: 'compaction', encrypted_content: 'opaque-openai-compact-state' },
      expectedCode: 'openai_anthropic_bridge_compact_summary_unrecognized'
    }
  ]

  for (const invalidCase of invalidCases) {
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        input: [
          invalidCase.compactItem,
          {
            type: 'message',
            role: 'user',
            content: [{ type: 'input_text', text: `${invalidCase.label} must not reach upstream` }]
          }
        ],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 400, `${invalidCase.label} 应本地 400，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, new RegExp(invalidCase.expectedCode), `${invalidCase.label} 应返回稳定错误码 ${invalidCase.expectedCode}`)
    assert.equal(upstreamHits.length, 0, `${invalidCase.label} 不应请求 Anthropic 上游`)
  }
}

async function assertResponsesCompactEndpointBridge(baseUrl: string, localApiKey: string, otherLocalApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses/compact`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-compact',
        session_id: 'session-openai-anthropic-bridge-compact',
        thread_id: 'thread-openai-anthropic-bridge-compact'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'compact endpoint anthropic history' }]
      }],
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses /compact 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as {
    id?: unknown
    object?: unknown
    created_at?: unknown
    output?: Array<Record<string, unknown>>
    usage?: Record<string, unknown>
  }
  assert.equal(payload.object, 'response.compaction', '/responses/compact 应返回 OpenAI CompactResource object')
  assert.equal(typeof payload.id, 'string', '/responses/compact 应返回顶层 response id')
  assert.equal(typeof payload.created_at, 'number', '/responses/compact 应返回 created_at')
  assert.equal(typeof payload.usage?.total_tokens, 'number', '/responses/compact 应返回 usage')
  assert.equal(payload.output?.length, 1, '/responses/compact 应返回单个 compact output item')
  const compactItem = payload.output?.[0]
  assert.equal(compactItem?.type, 'compaction', '/responses/compact 应返回官方 compaction item')
  assert.match(String(compactItem?.encrypted_content ?? ''), /^juhecmp\.v2\.cmp_[^.]+\.[a-f0-9]{64}$/i, 'compact 应使用网关 snapshot reference envelope')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'compact endpoint anthropic history')
  assert.doesNotMatch(JSON.stringify(upstreamHits[0]?.body ?? {}), /responses\/compact/, '内部摘要请求不应把 /responses/compact 发给 Anthropic')

  upstreamHits.length = 0
  const bridgeResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-compact-followup',
        session_id: 'session-openai-anthropic-bridge-compact-followup',
        thread_id: 'thread-openai-anthropic-bridge-compact-followup'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        compactItem,
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'continue after compact endpoint' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const bridgeText = await bridgeResponse.text()
  assert.equal(bridgeResponse.status, 200, `Responses 应能消费 /compact 返回的 compaction item，实际 HTTP ${bridgeResponse.status}: ${bridgeText}`)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'continue after compact endpoint')
  const followupBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(followupBodyText, /transport/, '后续 Anthropic system 上下文应包含 compact 摘要文本')
  assert.doesNotMatch(followupBodyText, /juhecmp\.v2\./, '上游不应看到 compact snapshot envelope')

  upstreamHits.length = 0
  const tamperedCompactItem = {
    ...compactItem,
    encrypted_content: tamperCompactReferenceDigest(String(compactItem?.encrypted_content ?? ''))
  }
  const tamperedResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-compact-tampered',
        session_id: 'session-openai-anthropic-bridge-compact-tampered',
        thread_id: 'thread-openai-anthropic-bridge-compact-tampered'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        tamperedCompactItem,
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'tampered compact must not reach upstream' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const tamperedText = await tamperedResponse.text()
  assert.equal(tamperedResponse.status, 404, `篡改 digest 的 compact snapshot 应受控拒绝，实际 HTTP ${tamperedResponse.status}: ${tamperedText}`)
  assert.match(tamperedText, /codex_bridge_compact_snapshot_not_found/, '篡改 digest 的 compact snapshot 应返回稳定错误码')
  assert.equal(upstreamHits.length, 0, '篡改 digest 的 compact snapshot 受控失败时不应命中上游')

  upstreamHits.length = 0
  const boundaryResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${otherLocalApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-compact-boundary',
        session_id: 'session-openai-anthropic-bridge-compact-boundary',
        thread_id: 'thread-openai-anthropic-bridge-compact-boundary'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [
        compactItem,
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'cross key compact must not reach upstream' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const boundaryText = await boundaryResponse.text()
  assert.equal(boundaryResponse.status, 403, `跨 API Key compact snapshot 应受控拒绝，实际 HTTP ${boundaryResponse.status}: ${boundaryText}`)
  assert.match(boundaryText, /codex_bridge_compact_boundary_mismatch/, '跨 API Key compact snapshot 应返回稳定错误码')
  assert.equal(upstreamHits.length, 0, '跨 API Key compact snapshot 受控失败时不应命中上游')
}

function tamperCompactReferenceDigest(value: string): string {
  const prefix = 'juhecmp.v2.'
  const separatorIndex = value.lastIndexOf('.')
  assert(value.startsWith(prefix) && separatorIndex > prefix.length, `compact reference 外形不符合预期，无法构造篡改用例：${value}`)
  const digest = value.slice(separatorIndex + 1)
  assert.match(digest, /^[a-f0-9]{64}$/i, `compact reference digest 外形不符合预期：${value}`)
  const replacement = digest.endsWith('0') ? '1' : '0'
  return `${value.slice(0, separatorIndex + 1)}${digest.slice(0, -1)}${replacement}`
}

async function assertCodexPreviousResponseBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const first = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-previous-1',
        session_id: 'session-openai-anthropic-bridge-previous',
        thread_id: 'thread-openai-anthropic-bridge-previous'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'codex previous bridge start' }]
      }],
      stream: true
    })
  })
  const firstText = await first.text()
  assert.equal(first.status, 200, `Codex previous_response_id 首轮桥接应成功，实际 HTTP ${first.status}: ${firstText}`)
  const responseId = responseIdFromResponsesSse(firstText)
  assert(responseId, `Codex previous_response_id 首轮响应应包含 response id: ${firstText}`)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'codex previous bridge start')

  upstreamHits.length = 0
  const second = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn-openai-anthropic-bridge-previous-2',
        session_id: 'session-openai-anthropic-bridge-previous',
        thread_id: 'thread-openai-anthropic-bridge-previous'
      })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: responseId,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'codex previous bridge continuation' }]
      }],
      stream: true
    })
  })
  const secondText = await second.text()
  assert.equal(second.status, 200, `Codex previous_response_id 续链桥接应成功，实际 HTTP ${second.status}: ${secondText}`)
  assert.match(secondText, new RegExp(responseId.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), '续链响应应保留下游 previous_response_id')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'codex previous bridge continuation')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /codex previous bridge start/, '续链上游请求应包含已恢复的历史输入')
  assert.doesNotMatch(upstreamBodyText, /previous_response_id/, '续链上游请求不应把 OpenAI previous_response_id 直传给 Anthropic')
}

async function assertGenericResponsesPreviousResponseJsonBridge(baseUrl: string, localApiKey: string, otherLocalApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const first = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge json start' }]
      }],
      stream: false,
      store: false
    })
  })
  const firstText = await first.text()
  assert.equal(first.status, 200, `Generic Responses previous_response_id JSON 首轮桥接应成功，实际 HTTP ${first.status}: ${firstText}`)
  const firstBody = JSON.parse(firstText) as { id?: string }
  const responseId = firstBody.id
  assert(responseId, `Generic Responses previous_response_id JSON 首轮响应应包含 response id: ${firstText}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'generic previous bridge json start')

  upstreamHits.length = 0
  const second = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: responseId,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge json continuation' }]
      }],
      stream: false,
      store: false
    })
  })
  const secondText = await second.text()
  assert.equal(second.status, 200, `Generic Responses previous_response_id JSON 续链桥接应成功，实际 HTTP ${second.status}: ${secondText}`)
  const secondBody = JSON.parse(secondText) as { previous_response_id?: string }
  assert.equal(secondBody.previous_response_id, responseId, 'Generic Responses JSON 续链响应应保留下游 previous_response_id')
  assertBridgeUpstreamHit(upstreamHits[0], false, 'generic previous bridge json continuation')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /generic previous bridge json start/, 'Generic Responses JSON 续链上游请求应包含已恢复的历史输入')
  assert.match(upstreamBodyText, /generic previous bridge json continuation/, 'Generic Responses JSON 续链上游请求应包含当前输入')
  assert.doesNotMatch(upstreamBodyText, /previous_response_id/, 'Generic Responses JSON 续链上游请求不应把 OpenAI previous_response_id 直传给 Anthropic')

  upstreamHits.length = 0
  const boundary = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${otherLocalApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: responseId,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge cross key must not reach upstream' }]
      }],
      stream: false,
      store: false
    })
  })
  const boundaryText = await boundary.text()
  assert.equal(boundary.status, 403, `Generic Responses previous_response_id 跨 API Key 应受控拒绝，实际 HTTP ${boundary.status}: ${boundaryText}`)
  assert.match(boundaryText, /codex_bridge_previous_response_boundary_mismatch/, 'Generic Responses previous_response_id 跨 API Key 应返回稳定错误码')
  assert.equal(upstreamHits.length, 0, 'Generic Responses previous_response_id 跨 API Key 受控失败时不应命中上游')
}

async function assertGenericResponsesPreviousResponseSseBridge(baseUrl: string, localApiKey: string, otherLocalApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const first = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge sse start' }]
      }],
      stream: true,
      store: false
    })
  })
  const firstText = await first.text()
  assert.equal(first.status, 200, `Generic Responses previous_response_id SSE 首轮桥接应成功，实际 HTTP ${first.status}: ${firstText}`)
  const responseId = responseIdFromResponsesSse(firstText)
  assert(responseId, `Generic Responses previous_response_id SSE 首轮响应应包含 response id: ${firstText}`)
  assertBridgeUpstreamHit(upstreamHits[0], true, 'generic previous bridge sse start')

  upstreamHits.length = 0
  const second = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: responseId,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge sse continuation' }]
      }],
      stream: true,
      store: false
    })
  })
  const secondText = await second.text()
  assert.equal(second.status, 200, `Generic Responses previous_response_id SSE 续链桥接应成功，实际 HTTP ${second.status}: ${secondText}`)
  const completedPayload = sseEventPayloads(secondText, 'response.completed')[0]
  const completedResponse = typeof completedPayload?.response === 'object' && completedPayload.response !== null && !Array.isArray(completedPayload.response)
    ? completedPayload.response as Record<string, unknown>
    : undefined
  assert.equal(completedResponse?.previous_response_id, responseId, 'Generic Responses SSE 续链 response.completed 应保留下游 previous_response_id')
  assertBridgeUpstreamHit(upstreamHits[0], true, 'generic previous bridge sse continuation')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /generic previous bridge sse start/, 'Generic Responses SSE 续链上游请求应包含已恢复的历史输入')
  assert.match(upstreamBodyText, /generic previous bridge sse continuation/, 'Generic Responses SSE 续链上游请求应包含当前输入')
  assert.doesNotMatch(upstreamBodyText, /previous_response_id/, 'Generic Responses SSE 续链上游请求不应把 OpenAI previous_response_id 直传给 Anthropic')

  upstreamHits.length = 0
  const boundary = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${otherLocalApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      previous_response_id: responseId,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'generic previous bridge sse cross key must not reach upstream' }]
      }],
      stream: true,
      store: false
    })
  })
  const boundaryText = await boundary.text()
  assert.equal(boundary.status, 403, `Generic Responses previous_response_id SSE 跨 API Key 应受控拒绝，实际 HTTP ${boundary.status}: ${boundaryText}`)
  assert.match(boundaryText, /codex_bridge_previous_response_boundary_mismatch/, 'Generic Responses previous_response_id SSE 跨 API Key 应返回稳定错误码')
  assert.equal(upstreamHits.length, 0, 'Generic Responses previous_response_id SSE 跨 API Key 受控失败时不应命中上游')
}

async function assertGenericResponsesPreviousResponseMissingBridge(baseUrl: string, localApiKey: string): Promise<void> {
  for (const input of [
    { label: 'JSON', stream: false, previousResponseId: 'resp_openai_anthropic_bridge_missing_json' },
    { label: 'SSE', stream: true, previousResponseId: 'resp_openai_anthropic_bridge_missing_sse' }
  ]) {
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json',
        ...(input.stream ? { accept: 'text/event-stream' } : {})
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        previous_response_id: input.previousResponseId,
        input: [{
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: `generic previous bridge missing ${input.label}` }]
        }],
        stream: input.stream,
        store: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 404, `Generic Responses previous_response_id ${input.label} 未知 id 应受控 404，实际 HTTP ${response.status}: ${text}`)
    assert.match(text, /codex_bridge_previous_response_not_found/, `Generic Responses previous_response_id ${input.label} 未知 id 应返回稳定错误码`)
    assert.equal(upstreamHits.length, 0, `Generic Responses previous_response_id ${input.label} 未知 id 受控失败时不应命中上游`)
  }
}

function assertBridgeUpstreamHit(hit: UpstreamHit | undefined, stream: boolean, promptText: string): void {
  assert(hit, '缺少 Anthropic mock 上游命中记录')
  assert.equal(hit.path, '/v1/messages')
  assert.equal(hit.method, 'POST')
  assert.equal(hit.xApiKey, 'sk-ant-bridge-upstream')
  assert.equal(hit.authorization, '', 'Anthropic 上游不应收到本地 Authorization')
  assert.equal(hit.body.model, 'claude-haiku-4-5', '桥接应把下游 gpt-5.5 映射为 Anthropic 上游模型')
  assert.equal(hit.body.stream, stream ? true : undefined)
  assert.match(JSON.stringify(hit.body), new RegExp(promptText.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
}

function createAnthropicBridgeMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const body = safeParseJson(bodyText)
      const hitIndex = upstreamHits.length + 1
      upstreamHits.push({
        path: req.url?.split('?', 1)[0] ?? '',
        method: req.method ?? '',
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        authorization: String(req.headers.authorization ?? ''),
        body
      })
      if ((req.url?.split('?', 1)[0] ?? '') !== '/v1/messages') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'mock path not found' } }))
        return
      }
      if (body.stream === true && bodyText.includes('trigger bridge sse error')) {
        sendAnthropicSseError(res, `msg_openai_anthropic_bridge_sse_error_${hitIndex}`)
      } else if (
        body.stream === true
        && (bodyText.includes('responses image_generation sse bridge') || bodyText.includes('responses image_generation partial sse bridge'))
      ) {
        sendAnthropicImageGenerationPromptSse(res, `msg_openai_anthropic_bridge_image_generation_sse_${hitIndex}`)
      } else if (body.stream === true && bodyText.includes('responses thinking sse bridge')) {
        sendAnthropicThinkingSse(res, `msg_openai_anthropic_bridge_thinking_sse_${hitIndex}`)
      } else if (body.stream === true && bodyText.includes('anthropic file search sse bridge')) {
        sendAnthropicFileSearchSse(res, `msg_openai_anthropic_bridge_file_search_sse_${hitIndex}`)
      } else if (body.stream === true && bodyText.includes('responses tool search namespace sse bridge')) {
        sendAnthropicNamespaceToolSse(res, `msg_openai_anthropic_bridge_namespace_tool_sse_${hitIndex}`)
      } else if (body.stream === true && bodyText.includes('chat legacy functions sse bridge')) {
        sendAnthropicSingleToolSse(res, body, `msg_openai_anthropic_bridge_legacy_tool_sse_${hitIndex}`)
      } else if (
        body.stream === true
        && (bodyText.includes('chat multi tool sse bridge') || bodyText.includes('responses multi tool sse bridge'))
      ) {
        sendAnthropicMultiToolSse(res, `msg_openai_anthropic_bridge_multi_tool_sse_${hitIndex}`)
      } else if (body.stream === true) {
        sendAnthropicSse(res, `msg_openai_anthropic_bridge_sse_${hitIndex}`)
      } else if (anthropicRequestHasStructuredOutputTool(body) && bodyText.includes('structured schema invalid bridge')) {
        sendAnthropicInvalidStructuredOutputJson(res)
      } else if (anthropicRequestHasStructuredOutputTool(body)) {
        sendAnthropicStructuredOutputJson(res)
      } else if (bodyText.includes('responses image_generation moderation bridge')) {
        sendAnthropicImageGenerationModerationPromptJson(res)
      } else if (bodyText.includes('responses image_generation provider invalid json bridge')) {
        sendAnthropicImageGenerationCustomPromptJson(res, 'provider invalid json fixture')
      } else if (bodyText.includes('responses image_generation provider too large bridge')) {
        sendAnthropicImageGenerationCustomPromptJson(res, 'provider too large fixture')
      } else if (bodyText.includes('responses image_generation provider timeout bridge')) {
        sendAnthropicImageGenerationCustomPromptJson(res, 'provider timeout fixture')
      } else if (bodyText.includes('responses image_generation json bridge')) {
        sendAnthropicImageGenerationPromptJson(res)
      } else if (bodyText.includes('anthropic file search bridge') || bodyText.includes('anthropic chat file search bridge')) {
        sendAnthropicFileSearchJson(res)
      } else if (bodyText.includes('responses thinking bridge')) {
        sendAnthropicThinkingJson(res)
      } else if (bodyText.includes('responses code_interpreter local runtime worker') && anthropicRequestHasToolResult(body)) {
        sendAnthropicCodeInterpreterFinalJson(res, body)
      } else if (bodyText.includes('responses code_interpreter local runtime worker success') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, [
          'import os',
          'print("ci-success:", 2 + 3)',
          'print("secret:", os.environ.get("JUHE_AI_SECRET", "missing"))'
        ].join('\n'))
      } else if (bodyText.includes('responses code_interpreter local runtime worker stderr') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, [
          'import sys',
          'print("ci-stdout-line")',
          'print("ci-stderr-line", file=sys.stderr)'
        ].join('\n'))
      } else if (bodyText.includes('responses code_interpreter local runtime worker artifact large') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, [
          'from pathlib import Path',
          'Path("big.txt").write_text("X" * 2048, encoding="utf-8")',
          'print("large artifact ready")'
        ].join('\n'))
      } else if (bodyText.includes('responses code_interpreter local runtime worker artifact') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, [
          'from pathlib import Path',
          'Path("result.txt").write_text("artifact" + "_body" + "_marker", encoding="utf-8")',
          'print("artifact ready")'
        ].join('\n'))
      } else if (bodyText.includes('responses code_interpreter local runtime worker truncation') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, 'print("T" * 256)')
      } else if (bodyText.includes('responses code_interpreter local runtime worker timeout') && anthropicRequestHasTool(body)) {
        sendAnthropicCodeInterpreterToolJson(res, body, 'while True:\n    pass')
      } else if (anthropicRequestHasTool(body)) {
        sendAnthropicToolJson(res, body)
      } else {
        sendAnthropicJson(res)
      }
    })
  })
}

function createImageGenerationMockProvider(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const body = safeParseJson(bodyText)
      imageGenerationHits.push({
        path: req.url?.split('?', 1)[0] ?? '',
        method: req.method ?? '',
        authorization: String(req.headers.authorization ?? ''),
        body
      })
      const path = req.url?.split('?', 1)[0] ?? ''
      if (path === '/v1/responses') {
        sendImageGenerationResponsesProviderResponse(res, body)
        return
      }
      if (path !== '/v1/images/generations') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { type: 'not_found_error', message: 'mock image path not found' } }))
        return
      }
      if (String(body.prompt ?? '').includes('moderation blocked poster')) {
        res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          error: {
            type: 'image_generation_user_error',
            code: 'moderation_blocked',
            message: 'Image generation blocked by moderation',
            moderation_details: {
              moderation_stage: 'input',
              categories: ['harassment']
            }
          }
        }))
        return
      }
      if (String(body.prompt ?? '').includes('provider invalid json fixture')) {
        res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' })
        res.end('not json')
        return
      }
      if (String(body.prompt ?? '').includes('provider too large fixture')) {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          data: [{
            b64_json: `${mockImageBase64}${'A'.repeat(4096)}`,
            revised_prompt: `provider revised: ${String(body.prompt ?? '')}`
          }]
        }))
        return
      }
      if (String(body.prompt ?? '').includes('provider timeout fixture')) {
        setTimeout(() => {
          res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
          res.end(JSON.stringify({
            data: [{
              b64_json: mockImageBase64,
              revised_prompt: `provider revised: ${String(body.prompt ?? '')}`
            }]
          }))
        }, 200)
        return
      }
      if (body.stream === true) {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        writeSse(res, 'image_generation.partial_image', {
          type: 'image_generation.partial_image',
          partial_image_index: 0,
          b64_json: mockPartialImageBase64
        })
        writeSse(res, 'image_generation.completed', {
          type: 'image_generation.completed',
          b64_json: mockImageBase64,
          revised_prompt: `provider revised: ${String(body.prompt ?? '')}`
        })
        res.end()
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        data: [{
          b64_json: mockImageBase64,
          revised_prompt: `provider revised: ${String(body.prompt ?? '')}`
        }]
      }))
    })
  })
}

function sendImageGenerationResponsesProviderResponse(res: http.ServerResponse, body: Record<string, unknown>): void {
  const input = String(body.input ?? '')
  const imageItem = {
    id: 'ig_responses_provider_mock',
    type: 'image_generation_call',
    status: 'completed',
    result: mockImageBase64,
    revised_prompt: `responses provider revised: ${input}`
  }
  if (body.stream === true) {
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    writeSse(res, 'response.image_generation_call.partial_image', {
      type: 'response.image_generation_call.partial_image',
      partial_image_index: 0,
      partial_image_b64: mockPartialImageBase64
    })
    writeSse(res, 'response.completed', {
      type: 'response.completed',
      response: {
        id: 'resp_responses_provider_mock',
        object: 'response',
        status: 'completed',
        output: [imageItem]
      }
    })
    res.end()
    return
  }
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'resp_responses_provider_mock',
    object: 'response',
    status: 'completed',
    output: [imageItem],
    output_text: ''
  }))
}

function sendAnthropicJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_json',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: '{"ok":true,"transport":"json"}' }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 11,
      output_tokens: 5,
      cache_read_input_tokens: 2
    }
  }))
}

function sendAnthropicImageGenerationPromptJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_image_generation',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: 'watercolor city skyline at sunrise with precise ink lines' }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 19,
      output_tokens: 10
    }
  }))
}

function sendAnthropicImageGenerationModerationPromptJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_image_generation_moderation',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: 'moderation blocked poster with explicit warning text' }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 19,
      output_tokens: 7
    }
  }))
}

function sendAnthropicImageGenerationCustomPromptJson(res: http.ServerResponse, prompt: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_image_generation_custom',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: prompt }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 19,
      output_tokens: 5
    }
  }))
}

function sendAnthropicToolJson(res: http.ServerResponse, body: Record<string, unknown>): void {
  const toolName = upstreamToolNames({ body } as UpstreamHit)[0] ?? 'lookup_weather'
  const bodyText = JSON.stringify(body)
  const input = toolName.includes('list_open_orders')
      ? { customer_id: 'CUST-12345' }
      : toolName === 'legacy_lookup'
        ? { query: 'weather' }
      : { city: 'Shanghai' }
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_tool',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{
      type: 'tool_use',
      id: 'toolu_bridge_lookup',
      name: toolName,
      input
    }],
    stop_reason: 'tool_use',
    usage: {
      input_tokens: 15,
      output_tokens: 7
    }
  }))
}

function sendAnthropicCodeInterpreterToolJson(
  res: http.ServerResponse,
  body: Record<string, unknown>,
  code: string
): void {
  const toolName = upstreamToolNames({ body } as UpstreamHit)[0] ?? 'python'
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_code_interpreter_tool',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{
      type: 'tool_use',
      id: 'toolu_bridge_code_interpreter',
      name: toolName,
      input: { code }
    }],
    stop_reason: 'tool_use',
    usage: {
      input_tokens: 16,
      output_tokens: 8
    }
  }))
}

function sendAnthropicCodeInterpreterFinalJson(res: http.ServerResponse, body: Record<string, unknown>): void {
  const toolResultText = anthropicToolResultText(body)
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_code_interpreter_final',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: `Code interpreter final answer: ${toolResultText}` }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 18,
      output_tokens: 10
    }
  }))
}

function sendAnthropicStructuredOutputJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_structured',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{
      type: 'tool_use',
      id: 'toolu_bridge_structured',
      name: 'emit_structured_output',
      input: { ok: true, mode: 'structured' }
    }],
    stop_reason: 'tool_use',
    usage: {
      input_tokens: 16,
      output_tokens: 6
    }
  }))
}

function sendAnthropicInvalidStructuredOutputJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_structured_invalid',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{
      type: 'tool_use',
      id: 'toolu_bridge_structured_invalid',
      name: 'emit_structured_output',
      input: { ok: 'yes', extra: true }
    }],
    stop_reason: 'tool_use',
    usage: {
      input_tokens: 16,
      output_tokens: 6
    }
  }))
}

function sendAnthropicThinkingJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_thinking',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [
      { type: 'thinking', thinking: 'hidden thinking summary' },
      { type: 'text', text: 'thinking visible answer' }
    ],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 17,
      output_tokens: 9,
      output_tokens_details: { thinking_tokens: 3 }
    }
  }))
}

function sendAnthropicFileSearchJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_file_search',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: 'anthropic file search answer [F1]' }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 18,
      output_tokens: 8
    }
  }))
}

function sendAnthropicFileSearchSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 14, output_tokens: 0 }
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
    delta: { type: 'text_delta', text: 'anthropic file search sse answer [F1]' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'end_turn' },
    usage: { output_tokens: 10 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicSseError(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 7, output_tokens: 0 }
    }
  })
  writeSse(res, 'error', {
    type: 'error',
    error: {
      type: 'overloaded_error',
      message: 'mock bridge stream overloaded'
    }
  })
  res.end()
}

function sendAnthropicImageGenerationPromptSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 20, output_tokens: 0 }
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
    delta: { type: 'text_delta', text: 'watercolor river at dawn with soft mist' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'end_turn' },
    usage: { output_tokens: 9 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
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
    delta: { type: 'text_delta', text: 'bridge sse ok' }
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

function sendAnthropicMultiToolSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 8, output_tokens: 0 }
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
    delta: { type: 'text_delta', text: 'planning tool calls' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 1,
    content_block: {
      type: 'tool_use',
      id: 'toolu_multi_weather',
      name: 'lookup_weather',
      input: {}
    }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 1,
    delta: { type: 'input_json_delta', partial_json: '{"city"' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 1,
    delta: { type: 'input_json_delta', partial_json: ':"Shanghai"}' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 1
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 2,
    content_block: {
      type: 'tool_use',
      id: 'toolu_multi_news',
      name: 'lookup_news',
      input: {}
    }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 2,
    delta: { type: 'input_json_delta', partial_json: '{"topic"' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 2,
    delta: { type: 'input_json_delta', partial_json: ':"weather"}' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 2
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'tool_use' },
    usage: { output_tokens: 9 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicSingleToolSse(res: http.ServerResponse, body: Record<string, unknown>, messageId: string): void {
  const toolName = upstreamToolNames({ body } as UpstreamHit)[0] ?? 'lookup_weather'
  const input = toolName === 'legacy_lookup'
    ? { query: 'weather' }
    : { city: 'Shanghai' }
  const inputText = JSON.stringify(input)
  const midpoint = Math.max(1, Math.floor(inputText.length / 2))
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 8, output_tokens: 0 }
    }
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: {
      type: 'tool_use',
      id: 'toolu_single_lookup',
      name: toolName,
      input: {}
    }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'input_json_delta', partial_json: inputText.slice(0, midpoint) }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'input_json_delta', partial_json: inputText.slice(midpoint) }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'tool_use' },
    usage: { output_tokens: 7 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicNamespaceToolSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 11, output_tokens: 0 }
    }
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: { type: 'tool_use', id: 'toolu_namespace_open_orders', name: 'crm__list_open_orders', input: {} }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'input_json_delta', partial_json: '{"customer_id"' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'input_json_delta', partial_json: ':"CUST-12345"}' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'tool_use' },
    usage: { output_tokens: 7 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendAnthropicThinkingSse(res: http.ServerResponse, messageId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId,
      type: 'message',
      role: 'assistant',
      model: 'claude-haiku-4-5',
      content: [],
      usage: { input_tokens: 8, output_tokens: 0 }
    }
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: { type: 'thinking', thinking: '' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'thinking_delta', thinking: 'hidden thinking sse' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 1,
    content_block: { type: 'text', text: '' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 1,
    delta: { type: 'text_delta', text: 'thinking visible sse answer' }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 1
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'end_turn' },
    usage: { output_tokens: 11, output_tokens_details: { thinking_tokens: 5 } }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function anthropicRequestHasTool(body: Record<string, unknown>): boolean {
  return upstreamToolNames({ body } as UpstreamHit).length > 0
}

function anthropicRequestHasToolResult(body: Record<string, unknown>): boolean {
  return anthropicToolResultText(body) !== ''
}

function anthropicToolResultText(body: Record<string, unknown>): string {
  return anthropicToolResultTexts(body)[0] ?? ''
}

function anthropicToolResultTexts(body: Record<string, unknown>): string[] {
  const resultTexts: string[] = []
  const messages = Array.isArray(body.messages) ? body.messages : []
  for (const message of messages) {
    if (!message || typeof message !== 'object' || Array.isArray(message)) continue
    const content = Array.isArray((message as Record<string, unknown>).content)
      ? (message as Record<string, unknown>).content as unknown[]
      : []
    for (const block of content) {
      if (!block || typeof block !== 'object' || Array.isArray(block)) continue
      const item = block as Record<string, unknown>
      if (item.type !== 'tool_result') continue
      resultTexts.push(String(item.content ?? ''))
    }
  }
  return resultTexts
}

function anthropicRequestHasStructuredOutputTool(body: Record<string, unknown>): boolean {
  return upstreamToolNames({ body } as UpstreamHit).includes('emit_structured_output')
}

function upstreamToolNames(hit: UpstreamHit | undefined): string[] {
  const tools = Array.isArray(hit?.body.tools) ? hit.body.tools : []
  return tools
    .filter((tool): tool is Record<string, unknown> => typeof tool === 'object' && tool !== null && !Array.isArray(tool))
    .map((tool) => typeof tool.name === 'string' ? tool.name : '')
    .filter(Boolean)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function sseEventPayloads(text: string, eventName: string): Record<string, unknown>[] {
  const payloads: Record<string, unknown>[] = []
  for (const frame of text.split(/\r?\n\r?\n/)) {
    let event = ''
    const dataLines: string[] = []
    for (const line of frame.split(/\r?\n/)) {
      if (line.startsWith('event: ')) event = line.slice('event: '.length)
      if (line.startsWith('data: ')) dataLines.push(line.slice('data: '.length))
    }
    if (event !== eventName || dataLines.length === 0) continue
    payloads.push(safeParseJson(dataLines.join('\n')))
  }
  return payloads
}

function aggregateArgumentDeltasByItem(payloads: Record<string, unknown>[]): Record<string, string> {
  const output: Record<string, string> = {}
  for (const payload of payloads) {
    const itemId = typeof payload.item_id === 'string' ? payload.item_id : ''
    const delta = typeof payload.delta === 'string' ? payload.delta : ''
    if (!itemId || !delta) continue
    output[itemId] = `${output[itemId] ?? ''}${delta}`
  }
  return output
}

function responseIdFromResponsesSse(text: string): string | undefined {
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith('data: ')) continue
    const parsed = safeParseJson(line.slice('data: '.length))
    const response = typeof parsed.response === 'object' && parsed.response !== null && !Array.isArray(parsed.response)
      ? parsed.response as Record<string, unknown>
      : undefined
    if (typeof response?.id === 'string') return response.id
  }
  return undefined
}

function writeSse(res: http.ServerResponse, event: string, payload: Record<string, unknown>): void {
  res.write(`event: ${event}\n`)
  res.write(`data: ${JSON.stringify(payload)}\n\n`)
}

function safeParseJson(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
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
