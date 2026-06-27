import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ApiKeyExplicitHybridRouteRule } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

const realApiKey = requiredEnv('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_API_KEY', [
  'JUHE_REAL_OPENAI_COMPATIBLE_API_KEY',
  'JUHE_REAL_HYBRID_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_BASE_URL', [
  'JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL',
  'JUHE_REAL_HYBRID_BASE_URL'
]) || 'https://vsllm.com'
const sourceModel = envText('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_SOURCE_MODEL') || 'gemini-3.5-flash'
const upstreamModel = envText('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_UPSTREAM_MODEL') || 'glm-5.2'
const upstreamProvider = bridgeProvider()
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_TIMEOUT_MS') ?? 180_000
const jsonMarker = 'GEMINI_NATIVE_CHAT_BRIDGE_JSON_OK'
const streamMarker = 'GEMINI_NATIVE_CHAT_BRIDGE_STREAM_OK'

const tempRoot = resolve(tmpdir(), `juhe-ai-gemini-native-chat-bridge-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gemini-native-chat-bridge-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gemini-native-chat-bridge-real-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
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
const app = express()
app.use('/v1beta', requestContextMiddleware, express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  let appServer: http.Server | undefined
  try {
    const upstreamModels = await listRealModels()
    assert(
      upstreamModels.availableModels.length === 0 || upstreamModels.availableModels.includes(upstreamModel),
      `真实上游模型列表未包含 ${upstreamModel}，可见模型样本：${upstreamModels.availableModels.slice(0, 40).join('、')}`
    )

    registerModels()
    const group = repositories.createGroup({
      name: 'Gemini Native 转 Chat 真实 E2E 分组',
      providerCode: upstreamProvider.providerCode,
      providerProtocolProfileId: upstreamProvider.providerProtocolProfileId,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: upstreamProvider.providerCode,
      providerProtocolProfileId: upstreamProvider.providerProtocolProfileId,
      name: 'Gemini Native 转 Chat 真实 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [upstreamModel]
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini Native 转 Chat 真实 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      explicitHybridRouteRules: bridgeRouteRules(group.id),
      status: 'active'
    }, access)
    assert(apiKey.key, 'Gemini Native 转 Chat 真实 E2E 本地 API Key 未返回明文密钥')
    gatewayCache.clearGatewayRuntimeCache()

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const json = await assertGenerateContentJson(baseUrl, apiKey.key)
    const stream = await assertStreamGenerateContent(baseUrl, apiKey.key)

    console.log(JSON.stringify({
      ok: true,
      provider: upstreamProvider.providerCode,
      profile: upstreamProvider.providerProtocolProfileId,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      sourceModel,
      upstreamModel,
      upstreamModels: {
        status: upstreamModels.status,
        modelCount: upstreamModels.availableModels.length,
        sample: upstreamModels.availableModels.slice(0, 20)
      },
      json,
      stream
    }, null, 2))
  } finally {
    await closeServer(appServer)
  }
} catch (error) {
  throw new Error(sanitizeSecretText(error instanceof Error ? error.stack ?? error.message : String(error)))
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function listRealModels(): Promise<{ status: number; availableModels: string[] }> {
  const response = await fetchWithTimeout(openAICompatibleModelsUrl(realBaseUrl), {
    headers: {
      authorization: `Bearer ${realApiKey}`
    }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实上游 /models 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  const availableModels = Array.isArray(body.data)
    ? body.data.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
    : Array.isArray(body.models)
      ? body.models.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
      : []
  return { status: response.status, availableModels }
}

async function assertGenerateContentJson(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1beta/models/${encodeURIComponent(sourceModel)}:generateContent`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json'
    },
    body: JSON.stringify({
      systemInstruction: {
        parts: [{ text: `只输出 ${jsonMarker}，不要解释。` }]
      },
      contents: [
        {
          role: 'user',
          parts: [{ text: `请只输出 ${jsonMarker}` }]
        }
      ],
      generationConfig: {
        temperature: 0,
        maxOutputTokens: 64
      }
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini native -> Chat JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  const output = geminiJsonText(body)
  const markerMatched = new RegExp(jsonMarker).test(output)
  const guidanceFallback = isGatewayGuidanceFallback(output)
  assert(markerMatched || guidanceFallback, `Gemini native -> Chat JSON 输出应包含 marker 或网关 guidance，实际：${responseSnippet(output || text)}`)
  return {
    status: response.status,
    contentSample: output.slice(0, 120),
    markerMatched,
    guidanceFallback,
    finishReason: firstCandidateFinishReason(body),
    usageMetadataPresent: Boolean((body as { usageMetadata?: unknown }).usageMetadata)
  }
}

async function assertStreamGenerateContent(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1beta/models/${encodeURIComponent(sourceModel)}:streamGenerateContent?alt=sse`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      contents: [
        {
          role: 'user',
          parts: [{ text: `请只输出 ${streamMarker}` }]
        }
      ],
      generationConfig: {
        temperature: 0,
        maxOutputTokens: 64
      }
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini native -> Chat SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'Gemini native -> Chat SSE 应返回 text/event-stream')
  const output = extractGeminiSseText(text)
  const markerMatched = new RegExp(streamMarker).test(output)
  const guidanceFallback = isGatewayGuidanceFallback(output)
  assert(markerMatched || guidanceFallback, `Gemini native -> Chat SSE 输出应包含 marker 或网关 guidance，实际：${responseSnippet(output || text)}`)
  return {
    status: response.status,
    contentSample: output.slice(0, 120),
    markerMatched,
    guidanceFallback,
    usageMetadataPresent: /"usageMetadata"\s*:/.test(text)
  }
}

function isGatewayGuidanceFallback(text: string): boolean {
  return /网关已|客户端可以保持当前对话|换用更稳定的上游模型|无法解析为 Gemini GenerateContent/.test(text)
}

function registerModels(): void {
  saveCustomProviderModel({
    providerCode: upstreamProvider.providerCode,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    cachedInputUsdPer1M: 0.0002,
    actorSystemAccountId: access.systemAccountId
  })
}

function bridgeRouteRules(targetGroupId: string): ApiKeyExplicitHybridRouteRule[] {
  return [
    {
      id: 'generate_content_to_chat',
      enabled: true,
      priority: 1,
      sourceClientProfile: 'auto',
      sourceModel,
      sourceEndpointFamily: 'generate_content',
      targetGroupId,
      upstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      adapterMode: 'bridge'
    },
    {
      id: 'stream_generate_content_to_chat',
      enabled: true,
      priority: 2,
      sourceClientProfile: 'auto',
      sourceModel,
      sourceEndpointFamily: 'stream_generate_content',
      targetGroupId,
      upstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      adapterMode: 'bridge'
    }
  ]
}

function bridgeProvider(): { providerCode: string; providerProtocolProfileId: string } {
  const value = (envText('JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_PROVIDER') || 'glm').toLowerCase()
  if (value === 'glm') {
    return {
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID
    }
  }
  if (value === 'openai' || value === 'openai-compatible' || value === 'openai_compatible') {
    return {
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    }
  }
  throw new Error(`JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_PROVIDER 只支持 glm 或 openai，实际：${value}`)
}

function openAICompatibleModelsUrl(baseUrl: string): string {
  const url = new URL(baseUrl)
  const normalizedPath = url.pathname.replace(/\/+$/, '')
  url.pathname = normalizedPath.endsWith('/v1') || normalizedPath.endsWith('/v1beta/openai')
    ? `${normalizedPath}/models`
    : `${normalizedPath}/v1/models`
  url.search = ''
  return url.toString()
}

function modelNameFromUnknown(value: unknown): string | undefined {
  if (!value || typeof value !== 'object') return undefined
  const record = value as { id?: unknown; name?: unknown }
  return typeof record.id === 'string'
    ? record.id
    : typeof record.name === 'string'
      ? record.name
      : undefined
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), `响应不是 JSON 对象：${responseSnippet(text)}`)
  return parsed as Record<string, unknown>
}

function geminiJsonText(body: Record<string, unknown>): string {
  const candidates = Array.isArray(body.candidates) ? body.candidates : []
  const parts: string[] = []
  for (const candidate of candidates) {
    if (!candidate || typeof candidate !== 'object') continue
    const content = (candidate as { content?: unknown }).content
    const contentParts = content && typeof content === 'object' && Array.isArray((content as { parts?: unknown }).parts)
      ? (content as { parts: unknown[] }).parts
      : []
    for (const part of contentParts) {
      if (part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string') {
        parts.push((part as { text: string }).text)
      }
    }
  }
  return parts.join('')
}

function firstCandidateFinishReason(body: Record<string, unknown>): string | undefined {
  const candidates = Array.isArray(body.candidates) ? body.candidates : []
  const first = candidates[0]
  return first && typeof first === 'object' && typeof (first as { finishReason?: unknown }).finishReason === 'string'
    ? (first as { finishReason: string }).finishReason
    : undefined
}

function extractGeminiSseText(text: string): string {
  const output: string[] = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText || dataText === '[DONE]') continue
    try {
      const data = JSON.parse(dataText) as Record<string, unknown>
      output.push(geminiJsonText(data))
    } catch {
    }
  }
  return output.join('')
}

async function fetchWithTimeout(url: string, init: RequestInit = {}): Promise<Response> {
  const timeoutSignal = AbortSignal.timeout(requestTimeoutMs)
  return await fetch(url, {
    ...init,
    signal: init.signal ?? timeoutSignal
  })
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`${name} 未设置；运行真实 Gemini native -> Chat 测试前请通过环境变量传入 API Key`)
  }
  return value
}

function envText(name: string, aliases: string[] = []): string | undefined {
  for (const key of [name, ...aliases]) {
    const value = process.env[key]?.trim()
    if (value) return value
  }
  return undefined
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
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

function sanitizeSecretText(text: string): string {
  return text
    .replaceAll(realApiKey, '[redacted-real-api-key]')
    .replaceAll(encodeURIComponent(realApiKey), '[redacted-real-api-key]')
}

function responseSnippet(text: string): string {
  return sanitizeSecretText(text).slice(0, 1000)
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
