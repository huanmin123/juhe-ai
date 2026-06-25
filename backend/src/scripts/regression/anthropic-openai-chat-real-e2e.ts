import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

const realApiKey = requiredEnv('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_API_KEY', [
  'JUHE_REAL_OPENAI_COMPATIBLE_API_KEY',
  'JUHE_REAL_HYBRID_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_BASE_URL', [
  'JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL',
  'JUHE_REAL_HYBRID_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const sourceModel = envText('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_SOURCE_MODEL') || 'claude-sonnet-4-6'
const upstreamModel = envText('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_UPSTREAM_MODEL') || 'gpt-5.4-mini'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_REQUEST_TIMEOUT_MS') ?? 120_000
const runImageCase = truthyEnv('JUHE_REAL_ANTHROPIC_OPENAI_CHAT_RUN_IMAGE')

const tempRoot = resolve(tmpdir(), `juhe-ai-anthropic-openai-chat-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'anthropic-openai-chat-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'anthropic-openai-chat-real-secret'
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
      name: 'Anthropic Messages 到 OpenAI Chat 真实 E2E 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'Anthropic Messages 到 OpenAI Chat 真实 E2E 账户',
      type: 'api_key',
      clientCompatibility: 'openai_standard',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [upstreamModel],
      modelMappings: messagesToChatMappings()
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: 'Anthropic Messages 到 OpenAI Chat 真实 E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '真实 Messages -> Chat E2E 本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const messagesJson = await assertMessagesJson(baseUrl, apiKey.key)
    const messagesSse = await assertMessagesSse(baseUrl, apiKey.key)
    const toolUse = await assertMessagesToolUse(baseUrl, apiKey.key)
    const guidance = await assertUnsupportedGuidance(baseUrl, apiKey.key)
    const image = runImageCase ? await assertMessagesImage(baseUrl, apiKey.key) : { skipped: true }

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    assertUsageAndAuditRecords(account.id, group.id)

    console.log(JSON.stringify({
      ok: true,
      provider: OPENAI_COMPATIBLE_PROVIDER_CODE,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      sourceModel,
      upstreamModel,
      messagesJson,
      messagesSse,
      toolUse,
      guidance,
      image
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

async function assertMessagesJson(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(localApiKey),
    body: JSON.stringify({
      model: sourceModel,
      max_tokens: 48,
      messages: [{ role: 'user', content: '只输出 messages-chat-ok' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Chat JSON 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  assert.match(response.headers.get('content-type') ?? '', /application\/json/)
  const body = parseJsonObject(text)
  assert.equal(body.type, 'message', `Messages -> Chat JSON 响应应是 Anthropic message：${responseSnippet(text)}`)
  const output = anthropicMessageText(body)
  assert(output.trim(), `Messages -> Chat JSON 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    model: typeof body.model === 'string' ? body.model : undefined,
    contentSample: output.trim().slice(0, 80)
  }
}

async function assertMessagesSse(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      ...anthropicHeaders(localApiKey),
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: sourceModel,
      max_tokens: 48,
      stream: true,
      messages: [{ role: 'user', content: '只输出 messages-stream-ok' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Chat SSE 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/)
  assert.match(text, /event:\s*message_start/, `Messages SSE 应包含 message_start：${responseSnippet(text)}`)
  assert.match(text, /event:\s*message_stop/, `Messages SSE 应包含 message_stop：${responseSnippet(text)}`)
  assert.doesNotMatch(text, /\[DONE\]/, 'Messages SSE 不应泄漏 OpenAI [DONE]')
  const output = anthropicSseText(text)
  assert(output.trim(), `Messages -> Chat SSE 输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80),
    events: sseEventNames(text).slice(0, 12)
  }
}

async function assertMessagesToolUse(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(localApiKey),
    body: JSON.stringify({
      model: sourceModel,
      max_tokens: 96,
      messages: [{ role: 'user', content: '调用 lookup 工具，query 必须是 real-messages-chat。' }],
      tools: [{
        name: 'lookup',
        description: 'lookup real bridge data',
        input_schema: {
          type: 'object',
          properties: { query: { type: 'string' } },
          required: ['query']
        }
      }],
      tool_choice: { type: 'tool', name: 'lookup' }
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Chat tool_use 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = parseJsonObject(text)
  const toolUse = firstAnthropicToolUse(body)
  assert(toolUse, `Messages -> Chat tool_use 响应缺少 tool_use block：${responseSnippet(text)}`)
  assert.equal(toolUse.name, 'lookup', `Messages -> Chat tool_use 工具名错误：${responseSnippet(text)}`)
  return {
    status: response.status,
    stopReason: typeof body.stop_reason === 'string' ? body.stop_reason : undefined,
    toolName: toolUse.name,
    toolInput: toolUse.input
  }
}

async function assertUnsupportedGuidance(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(localApiKey),
    body: JSON.stringify({
      model: sourceModel,
      max_tokens: 32,
      thinking: { type: 'enabled', budget_tokens: 1024 },
      messages: [{ role: 'user', content: 'hello' }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `不支持的 Messages 能力应返回 guidance，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = parseJsonObject(text)
  const output = anthropicMessageText(body)
  assert.match(output, /thinking|不支持|上游|客户端|agent/i, `guidance 文案应指出 thinking 能力缺口：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 160)
  }
}

async function assertMessagesImage(baseUrl: string, localApiKey: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/messages`, {
    method: 'POST',
    headers: anthropicHeaders(localApiKey),
    body: JSON.stringify({
      model: sourceModel,
      max_tokens: 80,
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: '这是一张 1x1 PNG。只输出 image-ok。' },
          {
            type: 'image',
            source: {
              type: 'base64',
              media_type: 'image/png',
              data: 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgwJ/l5yV4wAAAABJRU5ErkJggg=='
            }
          }
        ]
      }]
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `Messages -> Chat 图片输入应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = parseJsonObject(text)
  const output = anthropicMessageText(body)
  assert(output.trim(), `Messages -> Chat 图片输入输出为空：${responseSnippet(text)}`)
  return {
    status: response.status,
    contentSample: output.trim().slice(0, 80)
  }
}

function registerCustomModels(): void {
  saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: sourceModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['messages'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    actorSystemAccountId: access.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    actorSystemAccountId: access.systemAccountId
  })
}

function messagesToChatMappings(): AccountModelMapping[] {
  return [{
    sourceModel,
    sourceEndpointFamily: 'messages',
    upstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }]
}

function assertUsageAndAuditRecords(accountId: string, groupId: string): void {
  const usageRecords = repositories.listUsageRecords(access, { pageSize: 200, result: 'all' }).items
  const successfulRecords = usageRecords.filter((record) =>
    record.accountId === accountId
    && record.groupId === groupId
    && record.model === sourceModel
    && record.upstreamModel === upstreamModel
    && record.modelMappingApplied === true
  )
  assert(successfulRecords.length >= 3, `真实 Messages -> Chat 应至少写入 JSON/SSE/tool 三条成功使用记录，实际 ${successfulRecords.length}`)
  assert(successfulRecords.some((record) => record.stream === false), '真实 Messages -> Chat usage 应包含非流式请求')
  assert(successfulRecords.some((record) => record.stream === true), '真实 Messages -> Chat usage 应包含流式请求')

  const auditLogs = databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT group_id, account_id, model, upstream_model, model_mapping_applied, success
      FROM audit_logs
    `)
    .all() as Array<{
      account_id?: string | null
      group_id?: string | null
      model?: string | null
      model_mapping_applied?: number | null
      success?: number | null
      upstream_model?: string | null
    }>
  assert(auditLogs.some((log) =>
    log.group_id === groupId
    && log.account_id === accountId
    && log.model === sourceModel
    && log.upstream_model === upstreamModel
    && log.model_mapping_applied === 1
    && log.success === 1
  ), '真实 Messages -> Chat 应写入带模型映射上下文的成功审计日志')
}

function anthropicHeaders(localApiKey: string): Record<string, string> {
  return {
    'x-api-key': localApiKey,
    'anthropic-version': '2023-06-01',
    'content-type': 'application/json'
  }
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), `响应不是 JSON 对象：${responseSnippet(text)}`)
  return parsed as Record<string, unknown>
}

function anthropicMessageText(body: Record<string, unknown>): string {
  const content = Array.isArray(body.content) ? body.content : []
  return content
    .map((item) => item && typeof item === 'object' && typeof (item as { text?: unknown }).text === 'string'
      ? (item as { text: string }).text
      : '')
    .join('')
}

function firstAnthropicToolUse(body: Record<string, unknown>): { input?: unknown, name?: string } | undefined {
  const content = Array.isArray(body.content) ? body.content : []
  for (const item of content) {
    if (!item || typeof item !== 'object') continue
    const block = item as { input?: unknown, name?: unknown, type?: unknown }
    if (block.type === 'tool_use' && typeof block.name === 'string') {
      return { input: block.input, name: block.name }
    }
  }
  return undefined
}

function anthropicSseText(text: string): string {
  let output = ''
  for (const data of sseDataObjects(text)) {
    if (data.type === 'content_block_delta') {
      const delta = data.delta
      if (delta && typeof delta === 'object' && typeof (delta as { text?: unknown }).text === 'string') {
        output += (delta as { text: string }).text
      }
    }
  }
  return output
}

function sseEventNames(text: string): string[] {
  const names: string[] = []
  for (const match of text.matchAll(/^event:\s*(.+)$/gm)) {
    const name = match[1]?.trim()
    if (name) names.push(name)
  }
  return names
}

function sseDataObjects(text: string): Array<Record<string, unknown>> {
  const output: Array<Record<string, unknown>> = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText) continue
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
    throw new Error(`${name} 未设置；运行真实 Messages -> Chat 测试前请通过环境变量传入 API Key`)
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

function truthyEnv(name: string): boolean {
  const value = envText(name)
  return value === '1' || value?.toLowerCase() === 'true' || value?.toLowerCase() === 'yes'
}

function responseSnippet(text: string): string {
  return sanitizeSecretText(text).replace(/\s+/g, ' ').slice(0, 800)
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
  return value.replaceAll(realApiKey, '[redacted-real-api-key]')
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
