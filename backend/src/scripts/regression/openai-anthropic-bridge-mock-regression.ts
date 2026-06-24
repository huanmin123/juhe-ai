import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ANTHROPIC_PROVIDER_CODE } from '../../domain/provider-protocol.js'
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
  openAIAnthropicBridge
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
  import('../../modules/providers/drivers/_shared/openai-anthropic-bridge.js')
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

    const group = repositories.createGroup({
      name: 'OpenAI 到 Anthropic 桥接 mock 分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
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
    await assertChatMultipleChoicesRejected(baseUrl, apiKey.key)
    await assertChatLogprobsRejected(baseUrl, apiKey.key)
    await assertChatAudioOutputRejected(baseUrl, apiKey.key)
    await assertChatInputAudioRejected(baseUrl, apiKey.key)
    await assertChatUnknownContentPartRejected(baseUrl, apiKey.key)
    await assertChatMultiToolSseBridge(baseUrl, apiKey.key)
    await assertResponsesJsonBridge(baseUrl, apiKey.key)
    await assertResponsesSseBridge(baseUrl, apiKey.key)
    await assertResponsesLogprobsIncludeRejected(baseUrl, apiKey.key)
    await assertResponsesTopLogprobsRejected(baseUrl, apiKey.key)
    await assertResponsesInputAudioRejected(baseUrl, apiKey.key)
    await assertResponsesUnknownContentPartRejected(baseUrl, apiKey.key)
    await assertResponsesMultiToolSseBridge(baseUrl, apiKey.key)
    await assertBridgeSseErrorShape(baseUrl, apiKey.key)
    await assertChatImageDataUrlBridge(baseUrl, apiKey.key)
    await assertResponsesImageUrlBridge(baseUrl, apiKey.key)
    await assertChatImageDataUrlMimeRejected(baseUrl, apiKey.key)
    await assertResponsesImageDataUrlBase64Rejected(baseUrl, apiKey.key)
    await assertChatFileDataPdfBridge(baseUrl, apiKey.key)
    await assertResponsesFileDataTextBridge(baseUrl, apiKey.key)
    await assertResponsesFileUrlPdfBridge(baseUrl, apiKey.key)
    await assertChatToolCallBridge(baseUrl, apiKey.key)
    await assertResponsesFunctionCallBridge(baseUrl, apiKey.key)
    await assertResponsesAllowedFunctionToolsBridge(baseUrl, apiKey.key)
    await assertResponsesThinkingForcedToolChoiceRejected(baseUrl, apiKey.key)
    await assertChatToolResultBridge(baseUrl, apiKey.key)
    await assertResponsesFunctionOutputBridge(baseUrl, apiKey.key)
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
    await assertResponsesThinkingBridge(baseUrl, apiKey.key)
    await assertResponsesEncryptedReasoningIncludeRejected(baseUrl, apiKey.key)
    await assertResponsesThinkingSseBridge(baseUrl, apiKey.key)
    await assertResponsesHostedToolUnsupported(baseUrl, apiKey.key)
    await assertHostedToolRuntimeRejectMode(baseUrl, apiKey.key)
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
    clearMockImageGenerationProvider()
    await assertResponsesImageFileIdNotFound(baseUrl, apiKey.key)
    await assertResponsesImageFileIdResolverBridge(baseUrl, apiKey.key)
    await assertResponsesFileIdResolverTextBridge(baseUrl, apiKey.key)
    await assertChatFileIdResolverTextBridge(baseUrl, apiKey.key)
    await assertOpenAICompatibleFilesUploadResolverBridge(baseUrl, apiKey.key)
    await assertOpenAICompatibleVectorStoreFileSearchBridge(baseUrl, apiKey.key)
    await assertOpenAICompatibleFilesAndVectorStoreBoundaries(baseUrl, apiKey.key, apiKey.id, otherApiKey.key)
    await assertResponsesCompactionSummaryInputBridge(baseUrl, apiKey.key)
    await assertResponsesCompactEndpointBridge(baseUrl, apiKey.key, otherApiKey.key)
    await assertCodexPreviousResponseBridge(baseUrl, apiKey.key)

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
  timeoutMs?: number
  maxBodyBytes?: number
} = {}): void {
  runtimeConfig.imageGenerationProvider.endpoint = endpoint
  runtimeConfig.imageGenerationProvider.apiKey = 'sk-image-generation-mock'
  runtimeConfig.imageGenerationProvider.model = 'gpt-image-2-mock'
  runtimeConfig.imageGenerationProvider.timeoutMs = options.timeoutMs ?? 30000
  runtimeConfig.imageGenerationProvider.maxBodyBytes = options.maxBodyBytes ?? 1024 * 1024
}

function clearMockImageGenerationProvider(): void {
  runtimeConfig.imageGenerationProvider.endpoint = undefined
  runtimeConfig.imageGenerationProvider.apiKey = undefined
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
  assertBridgeUpstreamHit(upstreamHits[0], true, 'chat sse bridge')
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
  assert.equal(response.status, 400, `Chat n>1 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_multiple_choices_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Chat n>1 不应请求 Anthropic 上游')
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
  assert.equal(response.status, 400, `Chat logprobs 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Chat audio output 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Chat input_audio 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Responses logprobs include 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Responses top_logprobs>0 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Responses input_audio 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
  assert.equal(response.status, 400, `Responses reasoning + forced tool_choice 应本地 400，实际 HTTP ${response.status}: ${text}`)
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
          tool_calls: [{
            id: 'call_array_arguments',
            type: 'function',
            function: { name: 'lookup_weather', arguments: '["Shanghai"]' }
          }]
        },
        { role: 'tool', tool_call_id: 'call_array_arguments', content: 'sunny' },
        { role: 'user', content: 'chat non object arguments bridge' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Chat 非对象 arguments 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'chat non object arguments bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
  assert.match(upstreamBodyText, /"openai_arguments":\["Shanghai"\]/, 'Chat JSON 非对象 arguments 不应被吞成空对象')
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
          content: [{ type: 'input_text', text: 'responses invalid arguments bridge' }]
        }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Responses 非法 arguments 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assertBridgeUpstreamHit(upstreamHits[0], false, 'responses invalid arguments bridge')
  const upstreamBodyText = JSON.stringify(upstreamHits[0]?.body ?? {})
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
  assert.equal(response.status, 400, `Responses encrypted reasoning include 应本地 400，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /openai_anthropic_bridge_encrypted_reasoning_unsupported/)
  assert.equal(upstreamHits.length, 0, 'Responses encrypted reasoning include 不应请求 Anthropic 上游')
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

function assertBridgeUpstreamHit(hit: UpstreamHit | undefined, stream: boolean, promptText: string): void {
  assert(hit, '缺少 Anthropic mock 上游命中记录')
  assert.equal(hit.path, '/v1/messages')
  assert.equal(hit.method, 'POST')
  assert.equal(hit.xApiKey, 'sk-ant-bridge-upstream')
  assert.equal(hit.authorization, '', 'Anthropic 上游不应收到本地 Authorization')
  assert.equal(hit.body.model, 'claude-haiku-4-5', '桥接应把下游 gpt-5.5 映射为 Anthropic 上游模型')
  assert.equal(hit.body.stream, stream)
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
      } else if (anthropicRequestHasTool(body)) {
        sendAnthropicToolJson(res)
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
      if ((req.url?.split('?', 1)[0] ?? '') !== '/v1/images/generations') {
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

function sendAnthropicToolJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: 'msg_openai_anthropic_bridge_tool',
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{
      type: 'tool_use',
      id: 'toolu_bridge_lookup',
      name: 'lookup_weather',
      input: { city: 'Shanghai' }
    }],
    stop_reason: 'tool_use',
    usage: {
      input_tokens: 15,
      output_tokens: 7
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
