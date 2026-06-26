import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

const realApiKey = requiredEnv('JUHE_REAL_GEMINI_OPENAI_CHAT_API_KEY', ['JUHE_REAL_GEMINI_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_GEMINI_OPENAI_CHAT_BASE_URL') || 'https://vsllm.com'
const chatModel = envText('JUHE_REAL_GEMINI_OPENAI_CHAT_MODEL') || 'gemini-3.5-flash'
const responsesSourceModel = envText('JUHE_REAL_GEMINI_OPENAI_CHAT_RESPONSES_SOURCE_MODEL') || 'gpt-5.5'
const responsesUpstreamModel = envText('JUHE_REAL_GEMINI_OPENAI_CHAT_RESPONSES_UPSTREAM_MODEL') || chatModel
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_GEMINI_OPENAI_CHAT_REQUEST_TIMEOUT_MS') ?? 120_000
const responsesMaxOutputTokens = positiveIntegerEnv('JUHE_REAL_GEMINI_OPENAI_CHAT_RESPONSES_MAX_OUTPUT_TOKENS') ?? 160

const tempRoot = resolve(tmpdir(), `juhe-ai-gemini-openai-chat-real-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gemini-openai-chat-real-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gemini-openai-chat-real-e2e-secret'
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
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  let appServer: http.Server | undefined
  try {
    registerCustomModels()
    const upstreamModels = await listRealModels()
    assert(
      upstreamModels.availableModels.length === 0 || upstreamModels.availableModels.includes(chatModel),
      `真实上游模型列表未包含 ${chatModel}，Gemini 模型样本：${upstreamModels.geminiModels.slice(0, 30).join('、')}`
    )

    const group = repositories.createGroup({
      name: 'Gemini OpenAI Chat 真实网关 E2E 分组',
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      name: 'Gemini OpenAI Chat 真实网关 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [...new Set([chatModel, responsesUpstreamModel])],
      modelMappings: codexBridgeModelMappings()
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: 'Gemini OpenAI Chat 真实网关 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '真实联调本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const chatJson = await assertChatJson(baseUrl, apiKey.key)
    const chatSse = await assertChatSse(baseUrl, apiKey.key)
    const responsesBridge = await assertResponsesBridge(baseUrl, apiKey.key)

    console.log(JSON.stringify({
      ok: true,
      provider: GEMINI_PROVIDER_CODE,
      profile: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      upstreamModels,
      chatModel,
      responsesSourceModel,
      responsesUpstreamModel,
      chatJson,
      chatSse,
      responsesBridge
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

async function listRealModels(): Promise<Record<string, unknown> & { availableModels: string[]; geminiModels: string[] }> {
  const response = await fetchWithTimeout(openAICompatibleModelsUrl(realBaseUrl), {
    headers: {
      authorization: `Bearer ${realApiKey}`
    }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini OpenAI Chat 真实上游 /models 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  const availableModels = Array.isArray(body.data)
    ? body.data.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
    : Array.isArray(body.models)
      ? body.models.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
      : []
  const geminiModels = availableModels.filter((model) => model.includes('gemini'))
  assert(geminiModels.length > 0, `Gemini OpenAI Chat 真实上游 /models 未返回 Gemini 模型：${responseSnippet(text)}`)
  return {
    status: response.status,
    modelCount: availableModels.length,
    geminiModels: geminiModels.slice(0, 40),
    availableModels
  }
}

async function assertChatJson(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: chatModel,
      messages: [{ role: 'user', content: '只输出 ok' }],
      stream: false,
      max_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini OpenAI Chat JSON 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  const output = firstChoiceText(body)
  assert(output.trim(), `Gemini OpenAI Chat JSON 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    responseModel: typeof body.model === 'string' ? body.model : undefined,
    contentSample: output.trim().slice(0, 80)
  }
}

async function assertChatSse(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: chatModel,
      messages: [{ role: 'user', content: '只输出 stream-ok' }],
      stream: true,
      max_tokens: 32,
      temperature: 0
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini OpenAI Chat SSE 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(text, /data:\s*\[DONE\]/, `Gemini OpenAI Chat SSE 应包含 [DONE]：${responseSnippet(text)}`)
  const output = extractChatSseText(text)
  assert(output.trim(), `Gemini OpenAI Chat SSE 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80)
  }
}

async function assertResponsesBridge(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'gemini-openai-chat-real-e2e',
        thread_id: 'gemini-openai-chat-real-e2e',
        turn_id: `gemini-openai-chat-real-e2e-${Date.now()}`
      })
    },
    body: JSON.stringify({
      model: responsesSourceModel,
      instructions: '只输出一个简短确认。',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: '只输出 bridge-ok' }]
        }
      ],
      stream: true,
      store: false,
      max_output_tokens: responsesMaxOutputTokens
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Gemini OpenAI Chat Responses bridge 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'Responses bridge 应返回 SSE')
  assert.match(text, /event: response\.created/, `Responses bridge 应输出 response.created：${responseSnippet(text)}`)
  assert.match(text, /event: response\.completed/, `Responses bridge 应输出 response.completed：${responseSnippet(text)}`)
  assert.equal(text.includes('chat.completion.chunk'), false, 'Responses bridge 不应泄漏 Chat Completions 原始 SSE')
  const output = extractResponsesSseText(text)
  assert(output.trim(), `Responses bridge 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80),
    responseEvents: responseEventNames(text).slice(0, 12)
  }
}

function registerCustomModels(): void {
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: responsesSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions', 'responses'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    cachedInputUsdPer1M: 0.0002,
    actorSystemAccountId: access.systemAccountId
  })
  for (const model of new Set([chatModel, responsesUpstreamModel])) {
    saveCustomProviderModel({
      providerCode: GEMINI_PROVIDER_CODE,
      model,
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
}

function codexBridgeModelMappings(): AccountModelMapping[] {
  return [{
    sourceModel: responsesSourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel: responsesUpstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }]
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

function firstChoiceText(body: Record<string, unknown>): string {
  const choices = Array.isArray(body.choices) ? body.choices : []
  const first = choices[0]
  if (!first || typeof first !== 'object') return ''
  const message = (first as { message?: unknown }).message
  if (!message || typeof message !== 'object') return ''
  const content = (message as { content?: unknown }).content
  if (typeof content === 'string' && content.trim()) return content
  const reasoningContent = (message as { reasoning_content?: unknown }).reasoning_content
  return typeof reasoningContent === 'string' ? reasoningContent : ''
}

function extractChatSseText(text: string): string {
  const parts: string[] = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText || dataText === '[DONE]') continue
    try {
      const data = JSON.parse(dataText) as { choices?: Array<{ delta?: { content?: string; reasoning_content?: string } }> }
      for (const choice of data.choices ?? []) {
        if (typeof choice.delta?.reasoning_content === 'string') parts.push(choice.delta.reasoning_content)
        if (typeof choice.delta?.content === 'string') parts.push(choice.delta.content)
      }
    } catch {
    }
  }
  return parts.join('').trim()
}

function extractResponsesSseText(text: string): string {
  let output = ''
  for (const event of responseSseDataObjects(text)) {
    if (event.type === 'response.output_text.delta' && typeof event.delta === 'string') {
      output += event.delta
    }
    if (!output && event.type === 'response.completed') {
      output = outputTextFromResponsesOutput(event.response)
    }
  }
  return output
}

function outputTextFromResponsesOutput(value: unknown): string {
  if (!value || typeof value !== 'object') return ''
  const output = Array.isArray((value as { output?: unknown }).output)
    ? (value as { output: unknown[] }).output
    : []
  const parts: string[] = []
  for (const item of output) {
    if (!item || typeof item !== 'object') continue
    const content = Array.isArray((item as { content?: unknown }).content)
      ? (item as { content: unknown[] }).content
      : []
    for (const part of content) {
      if (part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string') {
        parts.push((part as { text: string }).text)
      }
    }
  }
  return parts.join('')
}

function responseEventNames(text: string): string[] {
  const names: string[] = []
  for (const match of text.matchAll(/^event:\s*(.+)$/gm)) {
    const name = match[1]?.trim()
    if (name) names.push(name)
  }
  return names
}

function responseSseDataObjects(text: string): Array<Record<string, unknown>> {
  const output: Array<Record<string, unknown>> = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText || dataText === '[DONE]') continue
    try {
      const data = JSON.parse(dataText) as unknown
      if (data && typeof data === 'object' && !Array.isArray(data)) output.push(data as Record<string, unknown>)
    } catch {
    }
  }
  return output
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
    throw new Error(`${name} 未设置；运行真实 Gemini OpenAI Chat 测试前请通过环境变量传入 API Key`)
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
