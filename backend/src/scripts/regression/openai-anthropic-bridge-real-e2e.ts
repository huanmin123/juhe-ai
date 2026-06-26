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

const apiKey = requiredEnv('JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_API_KEY')
const upstreamBaseUrl = (process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_BASE_URL ?? 'https://vsllm.com').replace(/\/+$/, '')
const upstreamModel = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_MODEL ?? 'claude-sonnet-4-6'
const sourceModel = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_SOURCE_MODEL ?? 'gpt-5.5'
const realE2ERequestTimeoutMs = positiveIntegerEnv('JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_REQUEST_TIMEOUT_MS') ?? 45_000
const streamRequestTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_STREAM_REQUEST_TIMEOUT_SECONDS')
const streamIdleTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_STREAM_IDLE_TIMEOUT_SECONDS')
const progressEnabled = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_PROGRESS !== '0'
const requiredOnly = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_REQUIRED_ONLY === '1'
const runImageProviderE2E = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_RUN_IMAGE_PROVIDER === '1'
const runImageProviderStreamE2E = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_RUN_IMAGE_PROVIDER_STREAM === '1'
const imageProviderBaseUrl = (process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_IMAGE_PROVIDER_BASE_URL ?? upstreamBaseUrl).replace(/\/+$/, '')
const imageProviderEndpoint = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_IMAGE_PROVIDER_ENDPOINT ?? `${imageProviderBaseUrl}/v1/responses`
const imageProviderModel = process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_IMAGE_PROVIDER_MODEL ?? 'gpt-image-2-chat'
const imageProviderTimeoutMs = Number(process.env.JUHE_REAL_OPENAI_ANTHROPIC_BRIDGE_IMAGE_PROVIDER_TIMEOUT_MS ?? '180000')

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-anthropic-bridge-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'openai-anthropic-bridge-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context-state')
runtimeConfig.openAICompatibleFilesRoot = join(tempRoot, 'openai-files')
runtimeConfig.secret = 'openai-anthropic-bridge-real-secret'
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
  auditLogQueue
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
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
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
  applyGatewayStreamTimeoutOverrides()
  let appServer: http.Server | undefined
  try {
    const group = repositories.createGroup({
      name: 'OpenAI 到 Anthropic 桥接真实 E2E 分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: 'OpenAI 到 Anthropic 桥接真实 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: apiKey,
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['messages_json', 'messages_sse']
      },
      supportedModels: [upstreamModel],
      clientCompatibility: 'codex_responses',
      modelMappings: [
        {
          sourceModel,
          sourceEndpointFamily: 'chat_completions',
          upstreamModel,
          upstreamEndpointFamily: 'messages',
          enabled: true
        },
        {
          sourceModel,
          sourceEndpointFamily: 'responses',
          upstreamModel,
          upstreamEndpointFamily: 'messages',
          enabled: true
        }
      ],
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(account.modelMappings?.length, 2, '真实 E2E 桥接账号应保存 Chat/Responses 映射')
    const defaultCandidateResult = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId)
    assert.equal(defaultCandidateResult.accounts.length, 1, `真实 E2E 默认候选窗口应返回桥接账号，实际 ${defaultCandidateResult.accounts.length}，诊断 ${JSON.stringify(defaultCandidateResult.diagnostics)}`)
    const chatCandidateResult = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId, {
      requestedModel: sourceModel,
      requestedEndpointFamily: 'chat_completions'
    })
    assert.equal(chatCandidateResult.accounts.length, 1, `真实 E2E Chat 模型候选窗口应返回桥接账号，实际 ${chatCandidateResult.accounts.length}，诊断 ${JSON.stringify(chatCandidateResult.diagnostics)}`)
    assert.equal(chatCandidateResult.accounts[0]?.modelMappings?.length, 2, '真实 E2E Chat 模型候选窗口返回的账号应带模型映射')
    const responsesCandidateResult = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId, {
      requestedModel: sourceModel,
      requestedEndpointFamily: 'responses'
    })
    assert.equal(responsesCandidateResult.accounts.length, 1, `真实 E2E Responses 模型候选窗口应返回桥接账号，实际 ${responsesCandidateResult.accounts.length}，诊断 ${JSON.stringify(responsesCandidateResult.diagnostics)}`)
    assert.equal(responsesCandidateResult.accounts[0]?.modelMappings?.length, 2, '真实 E2E Responses 模型候选窗口返回的账号应带模型映射')
    const localKey = repositories.createApiKeyRecord({
      name: 'OpenAI 到 Anthropic 桥接真实 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(localKey.key, '真实 E2E 本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await runRequiredCheck('chat_json', () => assertChatJson(baseUrl, localKey.key))
    await runRequiredCheck('chat_sse', () => assertChatSse(baseUrl, localKey.key))
    await runRequiredCheck('responses_json', () => assertResponsesJson(baseUrl, localKey.key))
    await runRequiredCheck('responses_sse', () => assertResponsesSse(baseUrl, localKey.key))
    await runRequiredCheck('file_search_vector_store_not_found', () => assertFileSearchVectorStoreNotFound(baseUrl, localKey.key))
    await runRequiredCheck('chat_web_search_guidance', () => assertChatWebSearchGuidance(baseUrl, localKey.key))
    const optionalChecks = requiredOnly
      ? ['optional_checks:skipped:required_only']
      : [
        await optionalCheck('chat_structured_output', () => assertChatStructuredOutput(baseUrl, localKey.key)),
        await optionalCheck('chat_image_data_url', () => assertChatImageDataUrl(baseUrl, localKey.key)),
        await optionalCheck('chat_file_data_text', () => assertChatFileDataText(baseUrl, localKey.key)),
        await optionalCheck('responses_file_data_text', () => assertResponsesFileDataText(baseUrl, localKey.key)),
        await optionalCheck('responses_thinking', () => assertResponsesThinking(baseUrl, localKey.key)),
        await optionalCheck('responses_file_search_local', () => assertResponsesFileSearchLocal(baseUrl, localKey.key)),
        await optionalCheck('responses_tool_search_namespace', () => assertResponsesToolSearchNamespace(baseUrl, localKey.key)),
        await optionalCheck('responses_previous_response_id_json', () => assertResponsesPreviousResponseIdJson(baseUrl, localKey.key)),
        await optionalCheck('responses_compact', () => assertResponsesCompact(baseUrl, localKey.key))
      ]
    if (!requiredOnly && runImageProviderE2E) {
      configureRealResponsesImageGenerationProvider()
      optionalChecks.push(await optionalCheck('responses_image_generation_provider', () => assertResponsesImageGenerationProvider(baseUrl, localKey.key)))
      if (runImageProviderStreamE2E) {
        optionalChecks.push(await optionalCheck('responses_image_generation_provider_sse', () => assertResponsesImageGenerationProviderSse(baseUrl, localKey.key)))
      }
    }

    console.log(JSON.stringify({
      ok: true,
      upstreamBaseUrl,
      upstreamModel,
      sourceModel,
      imageProvider: runImageProviderE2E
        ? { api: 'responses', endpoint: imageProviderEndpoint, model: imageProviderModel }
        : undefined,
      checks: [
        'chat_json',
        'chat_sse',
        'responses_json',
        'responses_sse',
        'file_search_vector_store_not_found',
        'chat_web_search_guidance',
        ...optionalChecks
      ]
    }))
  } finally {
    await closeServer(appServer)
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

function configureRealResponsesImageGenerationProvider(): void {
  runtimeConfig.imageGenerationProvider.endpoint = imageProviderEndpoint
  runtimeConfig.imageGenerationProvider.apiKey = apiKey
  runtimeConfig.imageGenerationProvider.api = 'responses'
  runtimeConfig.imageGenerationProvider.model = imageProviderModel
  runtimeConfig.imageGenerationProvider.timeoutMs = Math.min(Math.max(Math.trunc(imageProviderTimeoutMs), 1000), 300000)
  runtimeConfig.imageGenerationProvider.maxBodyBytes = 64 * 1024 * 1024
}

function applyGatewayStreamTimeoutOverrides(): void {
  const update: Record<string, unknown> = {}
  if (streamRequestTimeoutSecondsOverride) {
    update.streamRequestTimeoutSeconds = streamRequestTimeoutSecondsOverride
  }
  if (streamIdleTimeoutSecondsOverride) {
    update.streamIdleTimeoutSeconds = streamIdleTimeoutSecondsOverride
  }
  if (Object.keys(update).length > 0) {
    repositories.updateSettings(update)
  }
}

async function assertChatJson(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{ role: 'user', content: 'Reply with a short hello.' }],
      max_tokens: 16,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat JSON 桥接应成功，实际 HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { object?: string; choices?: Array<{ message?: { content?: string | null } }> }
  assert.equal(body.object, 'chat.completion')
  assert((body.choices?.[0]?.message?.content ?? '').length > 0, '真实 Chat JSON 应返回非空文本')
}

async function assertChatSse(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{ role: 'user', content: 'Reply with a short streaming hello.' }],
      max_tokens: 16,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat SSE 桥接应成功，实际 HTTP ${response.status}: ${text.slice(0, 500)}`)
  assert.match(text, /"object":"chat\.completion\.chunk"/)
  assert.match(text, /data: \[DONE\]/)
}

async function assertResponsesJson(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Reply with a short responses hello.',
      max_output_tokens: 16,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses JSON 桥接应成功，实际 HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { object?: string; status?: string; output_text?: string }
  assert.equal(body.object, 'response')
  assert.equal(body.status, 'completed')
  assert((body.output_text ?? '').length > 0, '真实 Responses JSON 应返回非空 output_text')
}

async function assertResponsesSse(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Reply with a short streaming responses hello.',
      max_output_tokens: 16,
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses SSE 桥接应成功，实际 HTTP ${response.status}: ${text.slice(0, 500)}`)
  assert.match(text, /event: response\.created/)
  assert.match(text, /event: response\.completed/)
}

async function assertResponsesPreviousResponseIdJson(baseUrl: string, localApiKey: string): Promise<void> {
  const first = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Remember the phrase real previous bridge marker.',
      max_output_tokens: 24,
      stream: false,
      store: false
    })
  })
  const firstText = await first.text()
  assert.equal(first.status, 200, `真实 Responses previous_response_id JSON 首轮应成功，HTTP ${first.status}: ${firstText.slice(0, 500)}`)
  const firstBody = JSON.parse(firstText) as { id?: string; object?: string; status?: string }
  assert.equal(firstBody.object, 'response')
  assert.equal(firstBody.status, 'completed')
  assert(firstBody.id, `真实 Responses previous_response_id JSON 首轮应返回 response id: ${firstText.slice(0, 500)}`)

  const second = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      previous_response_id: firstBody.id,
      input: 'Reply with exactly two words: previous-ok',
      max_output_tokens: 24,
      stream: false,
      store: false
    })
  })
  const secondText = await second.text()
  assert.equal(second.status, 200, `真实 Responses previous_response_id JSON 续链应成功，HTTP ${second.status}: ${secondText.slice(0, 500)}`)
  const secondBody = JSON.parse(secondText) as { object?: string; status?: string; previous_response_id?: string; output_text?: string }
  assert.equal(secondBody.object, 'response')
  assert.equal(secondBody.status, 'completed')
  assert.equal(secondBody.previous_response_id, firstBody.id)
  assert((secondBody.output_text ?? '').length > 0, '真实 Responses previous_response_id JSON 续链应返回非空 output_text')
}

async function assertResponsesImageGenerationProvider(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Generate a simple flat icon of a red square on a white background.',
      tools: [{ type: 'image_generation', action: 'generate', size: '1024x1024', quality: 'low', output_format: 'png' }],
      tool_choice: { type: 'image_generation' },
      max_output_tokens: 256,
      stream: false
    })
  }, Math.min(Math.max(Math.trunc(imageProviderTimeoutMs), 1000), 300000))
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses image_generation provider E2E 应成功，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as {
    object?: string
    status?: string
    output?: Array<{ type?: string; result?: string; revised_prompt?: string }>
    output_text?: string
  }
  assert.equal(body.object, 'response')
  assert.equal(body.status, 'completed')
  const imageItem = body.output?.find((item) => item.type === 'image_generation_call')
  assert(imageItem, `真实 Responses image_generation provider E2E 应返回 image_generation_call: ${text.slice(0, 500)}`)
  assert((imageItem.result ?? '').length > 1000, '真实 image_generation_call.result 应包含非空 base64 图片')
  assert.match(imageItem.result ?? '', /^[A-Za-z0-9+/]+={0,2}$/)
  assert((imageItem.revised_prompt ?? '').length > 0, '真实 image_generation_call 应保留 revised_prompt')
  assert.equal(body.output_text, '', '真实 image_generation 成功时不应把 revised prompt 当 output_text 暴露')
}

async function assertResponsesImageGenerationProviderSse(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Generate a simple flat icon of a blue circle on a white background.',
      tools: [{ type: 'image_generation', action: 'generate', size: '1024x1024', quality: 'low', output_format: 'png', partial_images: 1 }],
      tool_choice: { type: 'image_generation' },
      max_output_tokens: 256,
      stream: true
    })
  }, Math.min(Math.max(Math.trunc(imageProviderTimeoutMs), 1000), 300000))
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses image_generation provider SSE E2E 应成功，HTTP ${response.status}: ${text.slice(0, 500)}`)
  assert.match(text, /event: response\.created/)
  assert.match(text, /event: response\.output_item\.added/)
  assert.match(text, /event: response\.image_generation_call\.completed/)
  assert.match(text, /event: response\.output_item\.done/)
  assert.match(text, /event: response\.completed/)
  assert.match(text, /"type":"image_generation_call"/)
  assert.match(text, /"result":"[A-Za-z0-9+/]+={0,2}"/)
  assert.doesNotMatch(text, /response\.output_text\.delta/, '真实 image_generation provider SSE 不应把 revised prompt 当文本增量输出')
}

async function assertResponsesCompact(baseUrl: string, localApiKey: string): Promise<void> {
  const compact = await fetchWithTimeout(`${baseUrl}/v1/responses/compact`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'real-openai-anthropic-compact-turn',
        session_id: 'real-openai-anthropic-compact-session',
        thread_id: 'real-openai-anthropic-compact-thread'
      })
    },
    body: JSON.stringify({
      model: sourceModel,
      input: [{
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'Remember this compact probe context for the next turn.' }]
      }],
      store: false
    })
  })
  const compactText = await compact.text()
  assert.equal(compact.status, 200, `真实 /responses/compact 应成功，实际 HTTP ${compact.status}: ${compactText.slice(0, 500)}`)
  const compactBody = JSON.parse(compactText) as { object?: string; output?: Array<Record<string, unknown>> }
  assert.equal(compactBody.object, 'response.compaction')
  const compactItem = compactBody.output?.find((item) => item.type === 'compaction')
  assert(compactItem, `真实 /responses/compact 应返回 compaction item: ${compactText.slice(0, 500)}`)
  assert.match(String(compactItem.encrypted_content ?? ''), /^juhecmp\.v2\.cmp_[^.]+\.[a-f0-9]{64}$/i)

  const followup = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'real-openai-anthropic-compact-followup-turn',
        session_id: 'real-openai-anthropic-compact-followup-session',
        thread_id: 'real-openai-anthropic-compact-followup-thread'
      })
    },
    body: JSON.stringify({
      model: sourceModel,
      input: [
        compactItem,
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'Reply with one short acknowledgement.' }]
        }
      ],
      max_output_tokens: 24,
      stream: true,
      store: false
    })
  })
  const followupText = await followup.text()
  assert.equal(followup.status, 200, `真实 compaction item 回灌应成功，实际 HTTP ${followup.status}: ${followupText.slice(0, 500)}`)
  assert.match(followupText, /event: response\.completed/)
}

async function assertFileSearchVectorStoreNotFound(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'This request should fail locally before upstream dispatch.',
      tools: [{ type: 'file_search', vector_store_ids: ['vs_real_bridge_probe'] }],
      tool_choice: 'required',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 404, `真实 E2E file_search 未知 vector store 应本地 404，实际 HTTP ${response.status}: ${text.slice(0, 500)}`)
  assert.match(text, /openai_anthropic_bridge_file_search_vector_store_not_found/)
}

async function assertChatStructuredOutput(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{ role: 'user', content: 'Return {"ok":true,"mode":"real"} using the requested schema.' }],
      response_format: {
        type: 'json_schema',
        json_schema: {
          name: 'bridge_real',
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
      max_tokens: 128,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat structured output 探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string | null } }> }
  const content = body.choices?.[0]?.message?.content ?? ''
  const parsed = JSON.parse(content) as { ok?: unknown; mode?: unknown }
  assert.equal(parsed.ok, true)
  assert.equal(typeof parsed.mode, 'string')
}

async function assertChatImageDataUrl(baseUrl: string, localApiKey: string): Promise<void> {
  const onePixelPng = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII='
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'Describe this tiny image in a few words.' },
          { type: 'image_url', image_url: { url: `data:image/png;base64,${onePixelPng}` } }
        ]
      }],
      max_tokens: 32,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat 图片 data URL 探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string | null } }> }
  assert((body.choices?.[0]?.message?.content ?? '').length > 0, '真实 Chat 图片探针应返回非空文本')
}

async function assertChatFileDataText(baseUrl: string, localApiKey: string): Promise<void> {
  const textFile = Buffer.from('bridge document text from chat file data', 'utf8').toString('base64')
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'What short phrase is in this attached text file?' },
          {
            type: 'file',
            file: {
              filename: 'bridge-real-chat.txt',
              file_data: `data:text/plain;base64,${textFile}`
            }
          }
        ]
      }],
      max_tokens: 64,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat file_data text 探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string | null } }> }
  assert((body.choices?.[0]?.message?.content ?? '').length > 0, '真实 Chat file_data text 探针应返回非空文本')
}

async function assertChatWebSearchGuidance(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      messages: [{ role: 'user', content: 'chat web_search should return local guidance' }],
      tools: [{ type: 'web_search', search_context_size: 'low' }],
      tool_choice: 'required',
      max_tokens: 48,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Chat web_search 应返回本地 guidance，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string | null } }> }
  const guidance = body.choices?.[0]?.message?.content ?? ''
  assert.match(guidance, /能力未执行：web_search/)
  assert.match(guidance, /建议下一步/)
  assert.doesNotMatch(text, /openai_anthropic_bridge_unsupported_hosted_tool/)
}

async function assertResponsesFileDataText(baseUrl: string, localApiKey: string): Promise<void> {
  const textFile = Buffer.from('bridge document text from responses file data', 'utf8').toString('base64')
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: [{
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'What short phrase is in this attached text file?' },
          {
            type: 'input_file',
            filename: 'bridge-real-responses.txt',
            file_data: `data:text/plain;base64,${textFile}`
          }
        ]
      }],
      max_output_tokens: 64,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses file_data text 探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { output_text?: string }
  assert((body.output_text ?? '').length > 0, '真实 Responses file_data text 探针应返回非空 output_text')
}

async function assertResponsesThinking(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Answer with one short sentence.',
      reasoning: { effort: 'minimal' },
      max_output_tokens: 256,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses thinking 探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { output_text?: string; reasoning?: { effort?: string | null } }
  assert((body.output_text ?? '').length > 0, '真实 Responses thinking 探针应返回非空 output_text')
  assert.equal(body.reasoning?.effort, 'minimal')
}

async function assertResponsesFileSearchLocal(baseUrl: string, localApiKey: string): Promise<void> {
  const file = await uploadOpenAICompatibleRealFile({
    baseUrl,
    localApiKey,
    filename: 'bridge-real-file-search.md',
    mediaType: 'text/markdown',
    content: [
      'Real bridge local file search fixture.',
      'The alpha compatibility phrase should be retrievable from the local vector store.',
      'The gateway must return OpenAI shaped file_search_call output.'
    ].join('\n')
  })
  const vectorStoreResponse = await fetchWithTimeout(`${baseUrl}/v1/vector_stores`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ name: 'real bridge file search' })
  })
  const vectorStoreText = await vectorStoreResponse.text()
  assert.equal(vectorStoreResponse.status, 200, `真实 Vector Store 创建失败，HTTP ${vectorStoreResponse.status}: ${vectorStoreText.slice(0, 500)}`)
  const vectorStore = JSON.parse(vectorStoreText) as { id?: string }
  assert(vectorStore.id, '真实 Vector Store 创建应返回 id')

  const bindResponse = await fetchWithTimeout(`${baseUrl}/v1/vector_stores/${vectorStore.id}/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      file_id: file.id,
      attributes: { suite: 'real-file-search' }
    })
  })
  const bindText = await bindResponse.text()
  assert.equal(bindResponse.status, 200, `真实 Vector Store 文件绑定失败，HTTP ${bindResponse.status}: ${bindText.slice(0, 500)}`)
  const bindBody = JSON.parse(bindText) as { status?: string }
  assert.equal(bindBody.status, 'in_progress')
  await waitForOpenAICompatibleRealVectorStoreFileStatus(baseUrl, localApiKey, vectorStore.id, file.id, 'completed')

  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Use the file search result and answer in one short sentence. What phrase is retrievable?',
      tools: [{
        type: 'file_search',
        vector_store_ids: [vectorStore.id],
        max_num_results: 2,
        filters: { type: 'eq', key: 'suite', value: 'real-file-search' }
      }],
      include: ['file_search_call.results'],
      max_output_tokens: 96,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses file_search 本地探针失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as {
    output?: Array<{ type?: string; results?: Array<{ file_id?: string }> }>
    output_text?: string
  }
  assert.equal(body.output?.[0]?.type, 'file_search_call')
  assert.equal(body.output?.[0]?.results?.[0]?.file_id, file.id)
  assert((body.output_text ?? '').length > 0, '真实 Responses file_search 应返回非空 output_text')
}

async function assertResponsesToolSearchNamespace(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: sourceModel,
      input: 'Call the CRM tool to list open orders for customer CUST-12345. Do not answer in text.',
      tools: [
        {
          type: 'namespace',
          name: 'crm',
          description: 'CRM tools for customer lookup and order management.',
          tools: [{
            type: 'function',
            name: 'list_open_orders',
            description: 'List open orders for a customer ID.',
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
      max_output_tokens: 96,
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Responses tool_search namespace 桥接失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as {
    output?: Array<{ type?: string; name?: string; namespace?: string; arguments?: string }>
  }
  const functionCall = body.output?.find((item) => item.type === 'function_call')
  assert.equal(functionCall?.name, 'list_open_orders', `真实 Responses tool_search namespace 应恢复 function name: ${text.slice(0, 500)}`)
  assert.equal(functionCall?.namespace, 'crm', `真实 Responses tool_search namespace 应恢复 namespace: ${text.slice(0, 500)}`)
  assert.doesNotThrow(() => JSON.parse(functionCall?.arguments ?? '{}'), '真实 Responses tool_search namespace arguments 应为合法 JSON 字符串')
}

async function waitForOpenAICompatibleRealVectorStoreFileStatus(
  baseUrl: string,
  localApiKey: string,
  vectorStoreId: string,
  fileId: string,
  expectedStatus: 'completed' | 'failed'
): Promise<void> {
  let lastText = ''
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const response = await fetchWithTimeout(`${baseUrl}/v1/vector_stores/${vectorStoreId}/files/${fileId}`, {
      headers: { authorization: `Bearer ${localApiKey}` }
    })
    lastText = await response.text()
    assert.equal(response.status, 200, `真实 Vector Store 文件轮询失败，HTTP ${response.status}: ${lastText.slice(0, 500)}`)
    const body = JSON.parse(lastText) as { status?: string }
    if (body.status === expectedStatus) return
    assert.equal(body.status, 'in_progress', `真实 Vector Store 文件应处于 in_progress 或 ${expectedStatus}，实际: ${lastText.slice(0, 500)}`)
    await delay(25)
  }
  throw new Error(`真实 Vector Store 文件 ${fileId} 未在超时内变为 ${expectedStatus}，最后结果: ${lastText.slice(0, 500)}`)
}

async function uploadOpenAICompatibleRealFile(input: {
  baseUrl: string
  localApiKey: string
  filename: string
  mediaType: string
  content: string
}): Promise<{ id: string }> {
  const form = new FormData()
  form.append('purpose', 'assistants')
  form.append('file', new Blob([input.content], { type: input.mediaType }), input.filename)
  const response = await fetchWithTimeout(`${input.baseUrl}/v1/files`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`
    },
    body: form
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实 Files upload 失败，HTTP ${response.status}: ${text.slice(0, 500)}`)
  const body = JSON.parse(text) as { id?: string }
  assert(body.id, '真实 Files upload 应返回 file id')
  return { id: body.id }
}

async function optionalCheck(name: string, run: () => Promise<void>): Promise<string> {
  const startedAt = Date.now()
  logProgress(`${name}:start`)
  try {
    await run()
    logProgress(`${name}:passed:${Date.now() - startedAt}ms`)
    return `${name}:passed`
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    logProgress(`${name}:failed:${message.slice(0, 180).replace(/\s+/g, ' ')}`)
    return `${name}:failed:${message.slice(0, 180).replace(/\s+/g, ' ')}`
  }
}

async function runRequiredCheck(name: string, run: () => Promise<void>): Promise<void> {
  const startedAt = Date.now()
  logProgress(`${name}:start`)
  try {
    await run()
    logProgress(`${name}:passed:${Date.now() - startedAt}ms`)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    logProgress(`${name}:failed:${message.slice(0, 180).replace(/\s+/g, ' ')}`)
    throw error
  }
}

async function fetchWithTimeout(input: string, init: RequestInit, timeoutMs = realE2ERequestTimeoutMs): Promise<Response> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), timeoutMs)
  let cleared = false
  const clear = () => {
    if (cleared) return
    cleared = true
    clearTimeout(timeout)
  }
  try {
    const response = await fetch(input, { ...init, signal: controller.signal })
    const originalText = response.text.bind(response)
    Object.defineProperty(response, 'text', {
      value: async () => {
        try {
          return await originalText()
        } finally {
          clear()
        }
      }
    })
    return response
  } catch (error) {
    clear()
    throw error
  }
}

function logProgress(message: string): void {
  if (!progressEnabled) return
  console.log(JSON.stringify({
    progress: message,
    elapsed_ms: Date.now()
  }))
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) {
    throw new Error(`缺少环境变量 ${name}`)
  }
  return value
}

function positiveIntegerEnv(name: string): number | undefined {
  const raw = process.env[name]?.trim()
  if (!raw) return undefined
  const value = Number(raw)
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`环境变量 ${name} 必须是正整数毫秒值`)
  }
  return Math.trunc(value)
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
