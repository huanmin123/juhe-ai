import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

const realApiKey = requiredEnv('JUHE_REAL_OPENAI_COMPATIBLE_API_KEY', ['JUHE_REAL_HYBRID_API_KEY', 'HYBRID_REAL_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL', ['JUHE_REAL_HYBRID_BASE_URL', 'HYBRID_REAL_BASE_URL']) || 'https://vsllm.com'
const chatModel = envText('JUHE_REAL_OPENAI_COMPATIBLE_CHAT_MODEL') || 'gpt-5.4-mini'
const responsesSourceModel = envText('JUHE_REAL_OPENAI_COMPATIBLE_RESPONSES_SOURCE_MODEL') || `${chatModel}-responses-alias`
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_OPENAI_COMPATIBLE_REQUEST_TIMEOUT_MS') ?? 120_000

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-compatible-real-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'openai-compatible-real-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'openai-compatible-real-gateway-e2e-secret'
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
    const group = repositories.createGroup({
      name: '通用 OpenAI 兼容真实网关 E2E 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '通用 OpenAI 兼容真实网关 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      modelMappings: [{
        sourceModel: responsesSourceModel,
        sourceEndpointFamily: 'responses',
        upstreamModel: chatModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }],
      status: 'active',
      schedulable: true,
      supportedModels: [chatModel]
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '通用 OpenAI 兼容真实网关 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '真实联调本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const chatJson = await assertChatJson(baseUrl, apiKey.key)
    const chatSse = await assertChatSse(baseUrl, apiKey.key)
    const responsesJson = await assertResponsesJson(baseUrl, apiKey.key)
    const responsesSse = await assertResponsesSse(baseUrl, apiKey.key)

    console.log(JSON.stringify({
      ok: true,
      provider: OPENAI_COMPATIBLE_PROVIDER_CODE,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      chatModel,
      responsesSourceModel,
      chatJson,
      chatSse,
      responsesJson,
      responsesSse
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
      max_tokens: 32
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `通用 OpenAI 兼容 Chat JSON 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = parseJsonObject(text)
  const output = firstChoiceText(body)
  assert(output.trim(), `通用 OpenAI 兼容 Chat JSON 输出为空：${responseSnippet(text)}`)
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
      max_tokens: 32
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `通用 OpenAI 兼容 Chat SSE 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  assert.match(text, /data:\s*\[DONE\]/, `通用 OpenAI 兼容 Chat SSE 应包含 [DONE]：${responseSnippet(text)}`)
  const output = extractChatSseText(text)
  assert(output.trim(), `通用 OpenAI 兼容 Chat SSE 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80)
  }
}

async function assertResponsesJson(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: responsesSourceModel,
      input: '只输出 responses-ok',
      stream: false,
      max_output_tokens: 32
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `通用 OpenAI 兼容 Responses -> Chat JSON 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = parseJsonObject(text)
  const output = firstResponsesText(body)
  assert(output.trim(), `通用 OpenAI 兼容 Responses -> Chat JSON 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    responseModel: typeof body.model === 'string' ? body.model : undefined,
    contentSample: output.trim().slice(0, 80)
  }
}

async function assertResponsesSse(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: responsesSourceModel,
      input: '只输出 responses-stream-ok',
      stream: true,
      max_output_tokens: 32
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `通用 OpenAI 兼容 Responses -> Chat SSE 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  assert.match(text, /response\.(completed|output_text\.delta)/, `通用 OpenAI 兼容 Responses -> Chat SSE 应包含 Responses 事件：${responseSnippet(text)}`)
  const output = extractResponsesSseText(text)
  assert(output.trim(), `通用 OpenAI 兼容 Responses -> Chat SSE 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80)
  }
}

function registerCustomModels(): void {
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: chatModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    cachedInputUsdPer1M: 0.0002,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: responsesSourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    cachedInputUsdPer1M: 0.0002,
    actorSystemAccountId: access.systemAccountId
  })
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

function firstResponsesText(body: Record<string, unknown>): string {
  const outputText = body.output_text
  if (typeof outputText === 'string' && outputText.trim()) return outputText
  const output = Array.isArray(body.output) ? body.output : []
  for (const item of output) {
    if (!item || typeof item !== 'object') continue
    const content = (item as { content?: unknown }).content
    if (!Array.isArray(content)) continue
    for (const part of content) {
      if (!part || typeof part !== 'object') continue
      const text = (part as { text?: unknown }).text
      if (typeof text === 'string' && text.trim()) return text
    }
  }
  return ''
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
  const parts: string[] = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText || dataText === '[DONE]') continue
    try {
      const data = JSON.parse(dataText) as { delta?: unknown; text?: unknown; type?: unknown }
      if (typeof data.delta === 'string') parts.push(data.delta)
      if (typeof data.text === 'string') parts.push(data.text)
    } catch {
    }
  }
  return parts.join('').trim()
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
    throw new Error(`${name} 未设置；运行真实 OpenAI 兼容测试前请通过环境变量传入 API Key`)
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
