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
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping, AccountSupportedEndpointMode } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface NewApiChannel {
  key: string
  name: string
  url: string
}

interface RuntimeInfo {
  apiKey: string
  badAccountId: string
  groupId: string
  label: string
  realAccountIds: string[]
}

interface ScenarioResult {
  durationMs: number
  name: string
  outputSample?: string
  status: number
}

interface MockHit {
  authorization: string
  bodyText: string
  path: string
  rawUrl: string
  xApiKey: string
  xGoogApiKey: string
}

interface UpstreamDiagnostic {
  accountId?: string
  accountName?: string
  message?: string
  providerCode?: string
  providerProtocolProfileId?: string
  responseBodyText?: string
  status?: number
  upstreamUrl?: string
}

const channels = readChannels()
const realBaseUrl = sanitizeBaseUrl(channels[0]?.url ?? '')
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_NEWAPI_REQUEST_TIMEOUT_MS') ?? 120_000
const scenarioFilter = envText('JUHE_REAL_NEWAPI_SCENARIO_FILTER')
const skipBadAccount = truthyEnv('JUHE_REAL_NEWAPI_SKIP_BAD_ACCOUNT')
const openAIChatModel = envText('JUHE_REAL_NEWAPI_OPENAI_CHAT_MODEL') || 'gpt-5.5'
const openAIResponsesModel = envText('JUHE_REAL_NEWAPI_OPENAI_RESPONSES_MODEL') || openAIChatModel
const anthropicModel = envText('JUHE_REAL_NEWAPI_ANTHROPIC_MODEL') || 'claude-haiku-4-5-20251001'
const geminiModel = envText('JUHE_REAL_NEWAPI_GEMINI_MODEL') || 'gemini-3.5-flash'
const hybridUpstreamModel = envText('JUHE_REAL_NEWAPI_HYBRID_UPSTREAM_MODEL') || openAIChatModel

const tempRoot = resolve(tmpdir(), `juhe-ai-newapi-real-multi-protocol-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'newapi-real-multi-protocol-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { handleOpenAIGatewayRequest },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const mockHits: MockHit[] = []
const scenarioResults: ScenarioResult[] = []
const upstreamDiagnostics: UpstreamDiagnostic[] = []

const app = express()
app.use(requestContextMiddleware)
const gatewayHandler = async (req: express.Request, res: express.Response, next: express.NextFunction) => {
  try {
    await handleOpenAIGatewayRequest(req, res, {
      exposeUpstreamDiagnostics: true,
      onUpstreamAttemptDiagnostic: (attempt) => {
        upstreamDiagnostics.push({
          accountId: attempt.accountId,
          accountName: attempt.accountName,
          message: sanitizeSecretText(attempt.message ?? ''),
          providerCode: attempt.providerCode,
          providerProtocolProfileId: attempt.providerProtocolProfileId,
          responseBodyText: sanitizeSecretText((attempt.responseBodyText ?? '').slice(0, 1000)),
          status: attempt.status,
          upstreamUrl: sanitizeBaseUrl(attempt.upstreamUrl ?? '')
        })
      }
    })
  } catch (error) {
    next(error)
  }
}
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, gatewayHandler)
app.use('/v1beta', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, gatewayHandler)

let appServer: http.Server | undefined
let mockServer: http.Server | undefined

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  gatewayCache.clearGatewayRuntimeCache()

  registerModels()
  mockServer = createBadMockUpstream()
  await listen(mockServer)
  const mockBaseUrl = `http://127.0.0.1:${serverAddress(mockServer).port}`

  const openAIChatRuntime = createRuntime({
    accountNamePrefix: 'NewAPI OpenAI Chat 真实账户',
    badAccountName: 'NewAPI OpenAI Chat 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['chat_json', 'chat_sse'],
    groupName: 'NewAPI OpenAI Chat 真实 E2E 分组',
    keyName: 'NewAPI OpenAI Chat 真实 E2E Key',
    label: 'openai-chat',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    supportedModels: [openAIChatModel]
  })
  const openAIResponsesRuntime = createRuntime({
    accountNamePrefix: 'NewAPI OpenAI Responses 真实账户',
    badAccountName: 'NewAPI OpenAI Responses 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['responses_json', 'responses_sse'],
    groupName: 'NewAPI OpenAI Responses 真实 E2E 分组',
    keyName: 'NewAPI OpenAI Responses 真实 E2E Key',
    label: 'openai-responses',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    supportedModels: [openAIResponsesModel]
  })
  const anthropicRuntime = createRuntime({
    accountNamePrefix: 'NewAPI Anthropic Messages 真实账户',
    badAccountName: 'NewAPI Anthropic Messages 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['messages_json', 'messages_sse'],
    groupName: 'NewAPI Anthropic Messages 真实 E2E 分组',
    keyName: 'NewAPI Anthropic Messages 真实 E2E Key',
    label: 'anthropic-messages',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    supportedModels: [anthropicModel]
  })
  const geminiRuntime = createRuntime({
    accountNamePrefix: 'NewAPI Gemini Native 真实账户',
    badAccountName: 'NewAPI Gemini Native 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['generate_content_json', 'generate_content_sse'],
    groupName: 'NewAPI Gemini Native 真实 E2E 分组',
    keyName: 'NewAPI Gemini Native 真实 E2E Key',
    label: 'gemini-native',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    supportedModels: [geminiModel]
  })
  const hybridMessagesRuntime = createRuntime({
    accountNamePrefix: 'NewAPI Hybrid Messages 转 Chat 真实账户',
    badAccountName: 'NewAPI Hybrid Messages 转 Chat 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['chat_json', 'chat_sse'],
    groupName: 'NewAPI Hybrid Messages 转 Chat 真实 E2E 分组',
    keyName: 'NewAPI Hybrid Messages 转 Chat 真实 E2E Key',
    label: 'hybrid-messages-to-chat',
    modelMappings: [{
      sourceModel: anthropicModel,
      sourceEndpointFamily: 'messages',
      upstreamModel: hybridUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    supportedModels: [hybridUpstreamModel]
  })
  const hybridGeminiRuntime = createRuntime({
    accountNamePrefix: 'NewAPI Hybrid Gemini 转 Chat 真实账户',
    badAccountName: 'NewAPI Hybrid Gemini 转 Chat 故障 mock 账户',
    badBaseUrl: mockBaseUrl,
    endpointModes: ['chat_json', 'chat_sse'],
    groupName: 'NewAPI Hybrid Gemini 转 Chat 真实 E2E 分组',
    keyName: 'NewAPI Hybrid Gemini 转 Chat 真实 E2E Key',
    label: 'hybrid-gemini-to-chat',
    modelMappings: [{
      sourceModel: geminiModel,
      sourceEndpointFamily: 'generate_content',
      upstreamModel: hybridUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }, {
      sourceModel: geminiModel,
      sourceEndpointFamily: 'stream_generate_content',
      upstreamModel: hybridUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    supportedModels: [hybridUpstreamModel]
  })

  appServer = http.createServer(app)
  await listen(appServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  await runScenario('openai-chat-json-failover', () => assertOpenAIChatJson(gatewayBaseUrl, openAIChatRuntime.apiKey))
  await runScenario('openai-chat-sse', () => assertOpenAIChatSse(gatewayBaseUrl, openAIChatRuntime.apiKey))
  await runScenario('openai-responses-json-failover', () => assertOpenAIResponsesJson(gatewayBaseUrl, openAIResponsesRuntime.apiKey))
  await runScenario('openai-responses-sse', () => assertOpenAIResponsesSse(gatewayBaseUrl, openAIResponsesRuntime.apiKey))
  await runScenario('anthropic-messages-json-failover', () => assertAnthropicMessagesJson(gatewayBaseUrl, anthropicRuntime.apiKey))
  await runScenario('anthropic-messages-sse', () => assertAnthropicMessagesSse(gatewayBaseUrl, anthropicRuntime.apiKey))
  await runScenario('gemini-generate-content-json-failover', () => assertGeminiGenerateContentJson(gatewayBaseUrl, geminiRuntime.apiKey))
  await runScenario('gemini-stream-generate-content', () => assertGeminiStreamGenerateContent(gatewayBaseUrl, geminiRuntime.apiKey))
  await runScenario('hybrid-messages-to-chat-json-failover', () => assertHybridMessagesToChatJson(gatewayBaseUrl, hybridMessagesRuntime.apiKey))
  await runScenario('hybrid-messages-to-chat-sse', () => assertHybridMessagesToChatSse(gatewayBaseUrl, hybridMessagesRuntime.apiKey))
  await runScenario('hybrid-gemini-to-chat-json-failover', () => assertHybridGeminiToChatJson(gatewayBaseUrl, hybridGeminiRuntime.apiKey))

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  if (!scenarioFilter) {
    const runtimes = [
      openAIChatRuntime,
      openAIResponsesRuntime,
      anthropicRuntime,
      geminiRuntime,
      hybridMessagesRuntime,
      hybridGeminiRuntime
    ]
    if (!skipBadAccount) {
      assertBadMockDisruptionRecorded(runtimes)
    }
    for (const runtime of runtimes) {
      assertRuntimeRecoveredThroughRealAccount(runtime)
    }
  }

  console.log(JSON.stringify({
    ok: true,
    upstreamBaseUrl: realBaseUrl,
    channelCount: channels.length,
    models: {
      anthropicModel,
      geminiModel,
      hybridUpstreamModel,
      openAIChatModel,
      openAIResponsesModel
    },
    mockHitCount: mockHits.length,
    scenarios: scenarioResults
  }, null, 2))
} catch (error) {
  const diagnosticSummary = JSON.stringify({
    scenarioResults,
    upstreamDiagnostics,
    mockHitCount: mockHits.length,
    mockHits: mockHits.map((hit) => ({
      path: hit.path,
      rawUrl: hit.rawUrl,
      authorizationPresent: Boolean(hit.authorization),
      xApiKeyPresent: Boolean(hit.xApiKey),
      xGoogApiKeyPresent: Boolean(hit.xGoogApiKey),
      bodySample: sanitizeSecretText(hit.bodyText.slice(0, 300))
    }))
  }, null, 2)
  throw new Error(sanitizeSecretText(`${error instanceof Error ? error.stack ?? error.message : String(error)}\nDiagnostics:\n${diagnosticSummary}`))
} finally {
  await closeServer(appServer)
  await closeServer(mockServer)
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createRuntime(input: {
  accountNamePrefix: string
  badAccountName: string
  badBaseUrl: string
  endpointModes: AccountSupportedEndpointMode[]
  groupName: string
  keyName: string
  label: string
  modelMappings?: AccountModelMapping[]
  providerCode: string
  providerProtocolProfileId: string
  supportedModels: string[]
}): RuntimeInfo {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: input.providerCode,
    enabled: true
  }, access)
  const badAccount = skipBadAccount
    ? undefined
    : repositories.createAccount({
        providerCode: input.providerCode,
        providerProtocolProfileId: input.providerProtocolProfileId,
        name: input.badAccountName,
        type: 'api_key',
        credentials: {
          api_key: `sk-newapi-bad-${input.label}`,
          base_url: input.badBaseUrl,
          supported_endpoint_modes: input.endpointModes
        },
        groupId: group.id,
        modelMappings: input.modelMappings,
        priority: 0,
        status: 'active',
        schedulable: true,
        supportedModels: input.supportedModels
      }, access)
  const realAccountIds = channels.map((channel, index) => repositories.createAccount({
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    name: `${input.accountNamePrefix} ${index + 1}`,
    type: 'api_key',
    credentials: {
      api_key: channel.key,
      base_url: channel.url,
      supported_endpoint_modes: input.endpointModes
    },
    groupId: group.id,
    modelMappings: input.modelMappings,
    priority: (index + 1) * 10,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 4,
    supportedModels: input.supportedModels
  }, access).id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: input.keyName,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${input.keyName} 未返回明文密钥`)
  return {
    apiKey: apiKey.key,
    badAccountId: badAccount?.id ?? '',
    groupId: group.id,
    label: input.label,
    realAccountIds
  }
}

function registerModels(): void {
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: openAIChatModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions', 'responses'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    actorSystemAccountId: access.systemAccountId
  })
  if (openAIResponsesModel !== openAIChatModel) {
    saveCustomProviderModel({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      model: openAIResponsesModel,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['responses'],
      inputUsdPer1M: 0.002,
      outputUsdPer1M: 0.002,
      actorSystemAccountId: access.systemAccountId
    })
  }
  saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    actorSystemAccountId: access.systemAccountId
  })
  if (hybridUpstreamModel !== openAIChatModel) {
    saveCustomProviderModel({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      model: hybridUpstreamModel,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: 0.002,
      outputUsdPer1M: 0.002,
      actorSystemAccountId: access.systemAccountId
    })
  }
}

async function runScenario(name: string, fn: () => Promise<{ outputSample?: string; status: number }>): Promise<void> {
  if (scenarioFilter && !name.includes(scenarioFilter)) {
    return
  }
  const startedAt = Date.now()
  const result = await fn()
  scenarioResults.push({
    durationMs: Date.now() - startedAt,
    name,
    outputSample: result.outputSample,
    status: result.status
  })
  console.log(JSON.stringify({
    scenario: name,
    status: result.status,
    durationMs: Date.now() - startedAt,
    outputSample: result.outputSample
  }))
}

async function assertOpenAIChatJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: openAIHeaders(apiKey),
    body: JSON.stringify({
      model: openAIChatModel,
      messages: [{ role: 'user', content: '只输出 CHATONEOK' }],
      stream: false,
      max_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `OpenAI Chat JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = firstOpenAIChatText(parseJsonObject(text))
  assert(output.trim(), `OpenAI Chat JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'CHATONEOK', 'OpenAI Chat JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertOpenAIChatSse(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: openAIHeaders(apiKey, { accept: 'text/event-stream' }),
    body: JSON.stringify({
      model: openAIChatModel,
      messages: [{ role: 'user', content: '只输出 CHATTWOOK' }],
      stream: true,
      max_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `OpenAI Chat SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(text, /data:\s*\[DONE\]/, 'OpenAI Chat SSE 应包含 [DONE]')
  const output = extractOpenAIChatSseText(text)
  assert(output.trim(), `OpenAI Chat SSE 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'CHATTWOOK', 'OpenAI Chat SSE')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertOpenAIResponsesJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: openAIHeaders(apiKey),
    body: JSON.stringify({
      model: openAIResponsesModel,
      input: '只输出 RESPONEOK',
      stream: false,
      max_output_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `OpenAI Responses JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = firstResponsesText(parseJsonObject(text))
  assert(output.trim(), `OpenAI Responses JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'RESPONEOK', 'OpenAI Responses JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertOpenAIResponsesSse(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: openAIHeaders(apiKey, { accept: 'text/event-stream' }),
    body: JSON.stringify({
      model: openAIResponsesModel,
      input: '只输出 RESPTWOOK',
      stream: true,
      max_output_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `OpenAI Responses SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(text, /response\.(completed|output_text\.delta)/, 'OpenAI Responses SSE 应包含 Responses 事件')
  const output = extractResponsesSseText(text)
  assert(output.trim(), `OpenAI Responses SSE 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'RESPTWOOK', 'OpenAI Responses SSE')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertAnthropicMessagesJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(apiKey),
    body: JSON.stringify({
      model: anthropicModel,
      max_tokens: 32,
      temperature: 0,
      messages: [{ role: 'user', content: '只输出 MSGONEOK' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Messages JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = anthropicContentText(parseJsonObject(text))
  assert(output.trim(), `Anthropic Messages JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'MSGONEOK', 'Anthropic Messages JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertAnthropicMessagesSse(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(apiKey, { accept: 'text/event-stream' }),
    body: JSON.stringify({
      model: anthropicModel,
      max_tokens: 32,
      stream: true,
      temperature: 0,
      messages: [{ role: 'user', content: '只输出 MSGTWOOK' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Anthropic Messages SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(text, /event:\s*message_stop/, 'Anthropic Messages SSE 应包含 message_stop')
  const output = extractAnthropicSseText(text)
  assert(output.trim(), `Anthropic Messages SSE 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'MSGTWOOK', 'Anthropic Messages SSE')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertGeminiGenerateContentJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1beta/models/${encodeURIComponent(geminiModel)}:generateContent`, {
    method: 'POST',
    headers: geminiHeaders(apiKey),
    body: JSON.stringify({
      contents: [{ role: 'user', parts: [{ text: '只输出 GEMONEOK' }] }],
      generationConfig: geminiShortTextGenerationConfig()
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini generateContent JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = geminiContentText(parseJsonObject(text))
  assert(output.trim(), `Gemini generateContent JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'GEMONEOK', 'Gemini generateContent JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertGeminiStreamGenerateContent(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1beta/models/${encodeURIComponent(geminiModel)}:streamGenerateContent?alt=sse`, {
    method: 'POST',
    headers: geminiHeaders(apiKey, { accept: 'text/event-stream' }),
    body: JSON.stringify({
      contents: [{ role: 'user', parts: [{ text: '只输出 GEMTWOOK' }] }],
      generationConfig: geminiShortTextGenerationConfig()
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini streamGenerateContent 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = extractGeminiSseText(text)
  assert(output.trim(), `Gemini streamGenerateContent 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'GEMTWOOK', 'Gemini streamGenerateContent')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertHybridMessagesToChatJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(apiKey),
    body: JSON.stringify({
      model: anthropicModel,
      max_tokens: 32,
      temperature: 0,
      messages: [{ role: 'user', content: '只输出 HMSGONEOK' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Hybrid Messages -> Chat JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  assert.equal(body.type, 'message', 'Hybrid Messages -> Chat JSON 应返回 Anthropic message 形态')
  const output = anthropicContentText(body)
  assert(output.trim(), `Hybrid Messages -> Chat JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'HMSGONEOK', 'Hybrid Messages -> Chat JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertHybridMessagesToChatSse(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(apiKey, { accept: 'text/event-stream' }),
    body: JSON.stringify({
      model: anthropicModel,
      max_tokens: 32,
      stream: true,
      temperature: 0,
      messages: [{ role: 'user', content: '只输出 HMSGTWOOK' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Hybrid Messages -> Chat SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(text, /event:\s*message_stop/, 'Hybrid Messages -> Chat SSE 应返回 Anthropic SSE 事件')
  const output = extractAnthropicSseText(text)
  assert(output.trim(), `Hybrid Messages -> Chat SSE 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'HMSGTWOOK', 'Hybrid Messages -> Chat SSE')
  return { outputSample: output.slice(0, 80), status: response.status }
}

async function assertHybridGeminiToChatJson(baseUrl: string, apiKey: string): Promise<{ outputSample: string; status: number }> {
  const response = await fetchWithTimeout(`${baseUrl}/v1beta/models/${encodeURIComponent(geminiModel)}:generateContent`, {
    method: 'POST',
    headers: geminiHeaders(apiKey),
    body: JSON.stringify({
      contents: [{ role: 'user', parts: [{ text: '只输出 HGEMONEOK' }] }],
      generationConfig: geminiShortTextGenerationConfig()
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Hybrid Gemini -> Chat JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const output = geminiContentText(parseJsonObject(text))
  assert(output.trim(), `Hybrid Gemini -> Chat JSON 输出为空：${responseSnippet(text)}`)
  assertOutput(output, 'HGEMONEOK', 'Hybrid Gemini -> Chat JSON')
  return { outputSample: output.slice(0, 80), status: response.status }
}

function assertRuntimeRecoveredThroughRealAccount(runtime: RuntimeInfo): void {
  const records = repositories.listUsageRecords(access, { pageSize: 1000, result: 'all' }).items
    .filter((record) => record.groupId === runtime.groupId)
  assert(records.some((record) => runtime.realAccountIds.includes(record.accountId ?? '') && record.success === true), `${runtime.label} 应记录真实账户成功`)
}

function assertBadMockDisruptionRecorded(runtimes: RuntimeInfo[]): void {
  assert(mockHits.length > 0, '故障 mock 上游应至少被命中一次')
  const badAccountIds = new Set(runtimes.map((runtime) => runtime.badAccountId).filter(Boolean))
  const records = repositories.listUsageRecords(access, { pageSize: 1000, result: 'all' }).items
  assert(records.some((record) => badAccountIds.has(record.accountId ?? '') && record.success === false), '故障 mock 账户失败应写入使用记录')
}

function createBadMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const rawUrl = req.url ?? '/'
      const path = rawUrl.split('?', 1)[0] ?? '/'
      mockHits.push({
        authorization: String(req.headers.authorization ?? ''),
        bodyText,
        path,
        rawUrl,
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        xGoogApiKey: String(req.headers['x-goog-api-key'] ?? '')
      })
      if (path.includes('/messages')) {
        res.writeHead(529, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ type: 'error', error: { type: 'overloaded_error', message: 'forced mock upstream failure' } }))
        return
      }
      if (path.includes(':generateContent') || path.includes(':streamGenerateContent')) {
        res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { code: 503, status: 'UNAVAILABLE', message: 'forced mock upstream failure' } }))
        return
      }
      res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { code: 'forced_mock_upstream_failure', type: 'mock_failure', message: 'forced mock upstream failure' } }))
    })
  })
}

function openAIHeaders(apiKey: string, extra: Record<string, string> = {}): Record<string, string> {
  return {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    ...extra
  }
}

function anthropicHeaders(apiKey: string, extra: Record<string, string> = {}): Record<string, string> {
  return {
    authorization: `Bearer ${apiKey}`,
    'anthropic-version': '2023-06-01',
    'content-type': 'application/json',
    ...extra
  }
}

function geminiHeaders(apiKey: string, extra: Record<string, string> = {}): Record<string, string> {
  return {
    'x-goog-api-key': apiKey,
    'content-type': 'application/json',
    ...extra
  }
}

function geminiShortTextGenerationConfig(): Record<string, unknown> {
  return {
    maxOutputTokens: 128,
    temperature: 0,
    thinkingConfig: {
      thinkingLevel: 'low'
    }
  }
}

async function fetchWithTimeout(url: string, init: RequestInit): Promise<Response> {
  return await fetch(url, {
    ...init,
    signal: init.signal ?? AbortSignal.timeout(requestTimeoutMs)
  })
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), `响应不是 JSON 对象：${responseSnippet(text)}`)
  return parsed as Record<string, unknown>
}

function firstOpenAIChatText(body: Record<string, unknown>): string {
  const choices = Array.isArray(body.choices) ? body.choices : []
  const first = choices[0]
  if (!first || typeof first !== 'object') return ''
  const message = (first as { message?: unknown }).message
  if (!message || typeof message !== 'object') return ''
  const content = (message as { content?: unknown }).content
  if (typeof content === 'string') return content
  const reasoningContent = (message as { reasoning_content?: unknown }).reasoning_content
  return typeof reasoningContent === 'string' ? reasoningContent : ''
}

function firstResponsesText(body: Record<string, unknown>): string {
  if (typeof body.output_text === 'string') return body.output_text
  const output = Array.isArray(body.output) ? body.output : []
  const parts: string[] = []
  for (const item of output) {
    if (!item || typeof item !== 'object') continue
    const content = (item as { content?: unknown }).content
    if (Array.isArray(content)) {
      for (const part of content) {
        if (part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string') {
          parts.push((part as { text: string }).text)
        }
      }
    }
  }
  return parts.join('')
}

function anthropicContentText(body: Record<string, unknown>): string {
  const content = Array.isArray(body.content) ? body.content : []
  return content.map((item) => {
    if (!item || typeof item !== 'object') return ''
    const record = item as Record<string, unknown>
    if (typeof record.text === 'string') return record.text
    if (typeof record.thinking === 'string') return record.thinking
    if (record.type === 'tool_use') return JSON.stringify(record.input ?? {})
    return ''
  }).join('')
}

function geminiContentText(body: Record<string, unknown>): string {
  const candidates = Array.isArray(body.candidates) ? body.candidates : []
  const first = candidates[0]
  if (!first || typeof first !== 'object') return ''
  const content = (first as { content?: unknown }).content
  if (!content || typeof content !== 'object') return ''
  const parts = Array.isArray((content as { parts?: unknown }).parts) ? (content as { parts: unknown[] }).parts : []
  return parts.map((part) => part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string'
    ? (part as { text: string }).text
    : '').join('')
}

function extractOpenAIChatSseText(text: string): string {
  const parts: string[] = []
  for (const event of sseDataPayloads(text)) {
    if (event === '[DONE]') continue
    const body = safeParseJson(event)
    const choices = Array.isArray(body?.choices) ? body.choices : []
    for (const choice of choices) {
      const delta = choice && typeof choice === 'object' ? (choice as { delta?: { content?: string; reasoning_content?: string } }).delta : undefined
      if (typeof delta?.reasoning_content === 'string') parts.push(delta.reasoning_content)
      if (typeof delta?.content === 'string') parts.push(delta.content)
    }
  }
  return parts.join('')
}

function extractResponsesSseText(text: string): string {
  const parts: string[] = []
  for (const event of sseDataPayloads(text)) {
    const body = safeParseJson(event)
    if (typeof body?.delta === 'string') parts.push(body.delta)
    if (typeof body?.text === 'string') parts.push(body.text)
  }
  return parts.join('')
}

function extractAnthropicSseText(text: string): string {
  const parts: string[] = []
  for (const event of sseDataPayloads(text)) {
    const body = safeParseJson(event)
    const delta = body?.delta
    if (delta && typeof delta === 'object') {
      const record = delta as Record<string, unknown>
      if (typeof record.text === 'string') parts.push(record.text)
      if (typeof record.thinking === 'string') parts.push(record.thinking)
      if (typeof record.partial_json === 'string') parts.push(record.partial_json)
    }
  }
  return parts.join('')
}

function extractGeminiSseText(text: string): string {
  const parts: string[] = []
  for (const event of sseDataPayloads(text)) {
    const body = safeParseJson(event)
    const candidates = Array.isArray(body?.candidates) ? body.candidates : []
    for (const candidate of candidates) {
      if (!candidate || typeof candidate !== 'object') continue
      const content = (candidate as { content?: unknown }).content
      if (!content || typeof content !== 'object') continue
      const contentParts = Array.isArray((content as { parts?: unknown }).parts) ? (content as { parts: unknown[] }).parts : []
      for (const part of contentParts) {
        if (part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string') {
          parts.push((part as { text: string }).text)
        }
      }
    }
  }
  return parts.join('')
}

function sseDataPayloads(text: string): string[] {
  const payloads: string[] = []
  for (const rawEvent of text.split(/\r?\n\r?\n/)) {
    const data = rawEvent
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice('data:'.length).trim())
      .join('\n')
      .trim()
    if (data) payloads.push(data)
  }
  return payloads
}

function safeParseJson(text: string): Record<string, unknown> | undefined {
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : undefined
  } catch {
    return undefined
  }
}

function assertOutput(output: string, expected: string, label: string): void {
  const normalizedOutput = output.replace(/\s+/g, '')
  const normalizedExpected = expected.replace(/\s+/g, '')
  assert(normalizedOutput.includes(normalizedExpected), `${label} 返回内容不包含 ${expected}，实际：${output.slice(0, 300)}`)
}

function responseSnippet(text: string): string {
  return sanitizeSecretText(text).replace(/\s+/g, ' ').trim().slice(0, 1000)
}

function readChannels(): NewApiChannel[] {
  const json = envText('JUHE_REAL_NEWAPI_CHANNELS_JSON')
  if (json) {
    const parsed = JSON.parse(json) as unknown
    assert(Array.isArray(parsed), 'JUHE_REAL_NEWAPI_CHANNELS_JSON 必须是数组')
    return parsed.map((item, index) => {
      assert(item && typeof item === 'object' && !Array.isArray(item), `第 ${index + 1} 个 channel 必须是对象`)
      const record = item as Record<string, unknown>
      const key = typeof record.key === 'string' ? record.key.trim() : ''
      const url = typeof record.url === 'string' ? record.url.trim() : ''
      assert(key, `第 ${index + 1} 个 channel 缺少 key`)
      assert(url, `第 ${index + 1} 个 channel 缺少 url`)
      return {
        key,
        name: typeof record.name === 'string' && record.name.trim() ? record.name.trim() : `channel_${index + 1}`,
        url: url.replace(/\/+$/, '')
      }
    })
  }
  const keys = (envText('JUHE_REAL_NEWAPI_API_KEYS') || '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
  const baseUrl = (envText('JUHE_REAL_NEWAPI_BASE_URL') || 'https://vsllm.com').replace(/\/+$/, '')
  assert(keys.length > 0, '缺少 JUHE_REAL_NEWAPI_CHANNELS_JSON 或 JUHE_REAL_NEWAPI_API_KEYS')
  return keys.map((key, index) => ({ key, name: `channel_${index + 1}`, url: baseUrl }))
}

function envText(name: string): string | undefined {
  const value = process.env[name]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function truthyEnv(name: string): boolean {
  const value = envText(name)
  return value ? ['1', 'true', 'yes', 'on'].includes(value.toLowerCase()) : false
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return sanitizeSecretText(value)
  }
}

function sanitizeSecretText(value: string): string {
  let output = value.replace(/sk-[A-Za-z0-9_-]+/g, 'sk-***')
  for (const channel of channels) {
    output = output.replaceAll(channel.key, 'sk-***')
  }
  return output
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
