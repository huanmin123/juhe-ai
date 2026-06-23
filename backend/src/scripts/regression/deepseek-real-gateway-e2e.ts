import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

interface RealCaseResult {
  case: string
  ok: boolean
  error?: string
}

const realApiKey = requiredEnv('JUHE_REAL_DEEPSEEK_API_KEY')
const realBaseUrl = envText('JUHE_REAL_DEEPSEEK_BASE_URL') || 'https://api.deepseek.com'
const realModels = modelListFromEnv()
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_DEEPSEEK_REQUEST_TIMEOUT_MS') ?? 180_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_DEEPSEEK_REQUEST_INTERVAL_MS') ?? 1_000

const tempRoot = resolve(tmpdir(), `juhe-ai-deepseek-real-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'deepseek-real-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'deepseek-real-gateway-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { handleOpenAIGatewayRequest },
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
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res, { exposeUpstreamDiagnostics: true })
  } catch (error) {
    next(error)
  }
})

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let appServer: http.Server | undefined
  try {
    const group = repositories.createGroup({
      name: 'DeepSeek Real E2E 分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const codexBridgeGroup = repositories.createGroup({
      name: 'DeepSeek Codex Bridge Real E2E 分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Real E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 4,
      supportedModels: realModels
    }, access)
    assert.equal(account.providerCode, DEEPSEEK_PROVIDER_CODE)
    assert.equal(account.providerProtocolProfileId, DEEPSEEK_OPENAI_V1_PROFILE_ID)
    const codexBridgeAccount = repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Codex Bridge Real E2E 账户',
      type: 'api_key',
      clientCompatibility: 'codex_responses',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: codexBridgeGroup.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 4,
      supportedModels: realModels,
      modelMappings: codexBridgeModelMappings(realModels)
    }, access)
    assert.equal(codexBridgeAccount.clientCompatibility, 'codex_responses')

    const apiKey = repositories.createApiKeyRecord({
      name: 'DeepSeek Real E2E Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '真实联调本地 API Key 未返回明文密钥')
    const codexBridgeApiKey = repositories.createApiKeyRecord({
      name: 'DeepSeek Codex Bridge Real E2E Key',
      groupBindings: [{ groupId: codexBridgeGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(codexBridgeApiKey.key, '真实联调本地 Codex Bridge API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const recordCase = async (name: string, run: () => Promise<void>): Promise<RealCaseResult> => {
      const result = await runCase(name, run)
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      gatewayCache.clearGatewayRuntimeCache()
      return result
    }

    const localModelsResult = await recordCase('local-models', () => assertLocalModels(baseUrl, apiKey.key))
    assert(localModelsResult.ok, `DeepSeek 本地模型目录必须通过：${localModelsResult.error}`)
    const results: Array<{
      model: string
      chatJson: RealCaseResult
      chatSse: RealCaseResult
      codexBridge: RealCaseResult
    }> = []
    for (const model of realModels) {
      await wait(requestIntervalMs)
      const chatJson = await recordCase(`${model}:chat-json`, () => assertChatJson(baseUrl, apiKey.key, model))
      await wait(requestIntervalMs)
      const chatSse = await recordCase(`${model}:chat-sse`, () => assertChatSse(baseUrl, apiKey.key, model))
      await wait(requestIntervalMs)
      const codexBridge = await recordCase(`${model}:codex-bridge`, () => assertCodexBridge(baseUrl, codexBridgeApiKey.key, model))
      results.push({ model, chatJson, chatSse, codexBridge })
    }
    assert(results.some((item) => item.chatJson.ok), `DeepSeek 真实 Chat JSON 至少需要一个模型通过或被协议守卫受控处理，实际结果：${JSON.stringify(results, null, 2)}`)
    assert(results.some((item) => item.chatSse.ok), `DeepSeek 真实 Chat SSE 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)
    assert(results.some((item) => item.codexBridge.ok), `DeepSeek 真实 Codex bridge 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)

    console.log(JSON.stringify({
      ok: true,
      provider: DEEPSEEK_PROVIDER_CODE,
      baseUrl: realBaseUrl,
      models: results,
      requestTimeoutMs
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

async function assertLocalModels(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/models`, {
    headers: { authorization: `Bearer ${localApiKey}` }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `本地 DeepSeek 模型目录应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = JSON.parse(text) as { data?: Array<{ id?: string }> }
  const models = new Set((body.data ?? []).map((item) => item.id))
  for (const model of realModels) {
    assert(models.has(model), `本地 DeepSeek 模型目录应包含 ${model}`)
  }
}

async function assertChatJson(baseUrl: string, localApiKey: string, model: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [
        { role: 'user', content: 'Say OK.' }
      ],
      stream: false
    })
  })
  const text = await response.text()
  if (response.status === 502 && /upstream_protocol_error/.test(text)) {
    assert.doesNotMatch(text, /"choices"\s*:\s*null/, `${model} Chat JSON 上游坏结构不应原样暴露给客户端`)
    return
  }
  assert.equal(response.status, 200, `${model} Chat JSON 应成功或被网关转成受控协议错误，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const body = JSON.parse(text) as {
    choices?: Array<{ message?: { content?: string; reasoning_content?: string } }>
  }
  const output = `${body.choices?.[0]?.message?.reasoning_content ?? ''}${body.choices?.[0]?.message?.content ?? ''}`.trim()
  assert(output.length > 0, `${model} Chat JSON 应返回非空输出，实际响应：${responseSnippet(text)}`)
}

async function assertChatSse(baseUrl: string, localApiKey: string, model: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [
        { role: 'user', content: 'Say STREAM.' }
      ],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${model} Chat SSE 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  const output = extractChatSseText(text)
  assert(output.length > 0, `${model} Chat SSE 应返回非空输出，实际响应：${responseSnippet(text)}`)
  assert.match(text, /data:\s*\[DONE\]/, `${model} Chat SSE 应收到 [DONE]`)
}

async function assertCodexBridge(baseUrl: string, localApiKey: string, model: string): Promise<void> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-real-e2e-session',
        thread_id: 'deepseek-real-e2e-thread',
        turn_id: `deepseek-real-e2e-${model}`
      })
    },
    body: JSON.stringify({
      model,
      instructions: '只输出一个简短确认。',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'Say CODEX.' }]
        }
      ],
      stream: true,
      store: false,
      max_output_tokens: 64
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${model} DeepSeek Codex bridge 应成功，实际 HTTP ${response.status}: ${sanitizeSecretText(text)}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, `${model} DeepSeek Codex bridge 应返回 SSE`)
  assert.match(text, /event: response\.created/, `${model} DeepSeek Codex bridge 应输出 response.created，实际响应：${responseSnippet(text)}`)
  assert.match(text, /event: response\.completed/, `${model} DeepSeek Codex bridge 应输出 response.completed，实际响应：${responseSnippet(text)}`)
  assert.equal(text.includes('chat.completion.chunk'), false, `${model} DeepSeek Codex bridge 不应泄漏 Chat Completions 原始 SSE`)
  const output = extractResponsesSseText(text)
  assert(output.length > 0, `${model} DeepSeek Codex bridge 应返回非空 Responses 输出，实际响应：${responseSnippet(text)}`)
}

function extractChatSseText(text: string): string {
  const parts: string[] = []
  for (const match of text.matchAll(/^data:\s*(.+)$/gm)) {
    const dataText = match[1]?.trim()
    if (!dataText || dataText === '[DONE]') continue
    try {
      const data = JSON.parse(dataText) as { choices?: Array<{ delta?: { content?: string; reasoning_content?: string } }> }
      for (const choice of data.choices ?? []) {
        const content = choice.delta?.content
        const reasoningContent = choice.delta?.reasoning_content
        if (typeof reasoningContent === 'string') parts.push(reasoningContent)
        if (typeof content === 'string') parts.push(content)
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
      const data = JSON.parse(dataText) as {
        type?: string
        delta?: string
        text?: string
      }
      if (
        data.type === 'response.output_text.delta'
        || data.type === 'response.reasoning_summary_text.delta'
        || data.type === 'response.reasoning_text.delta'
      ) {
        const value = data.delta ?? data.text
        if (typeof value === 'string') parts.push(value)
      }
    } catch {
    }
  }
  return parts.join('').trim()
}

async function runCase(name: string, run: () => Promise<void>): Promise<RealCaseResult> {
  try {
    await run()
    const result: RealCaseResult = { case: name, ok: true }
    console.log(JSON.stringify(result))
    return result
  } catch (error) {
    const message = `${name} failed: ${sanitizeSecretText(error instanceof Error ? error.message : String(error))}`
    const result: RealCaseResult = { case: name, ok: false, error: message }
    console.log(JSON.stringify(result))
    return result
  }
}

function requiredEnv(name: string): string {
  const value = envText(name)
  if (!value) {
    throw new Error(`${name} is required`)
  }
  return value
}

function envText(name: string): string | undefined {
  const value = process.env[name]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function modelListFromEnv(): string[] {
  const value = envText('JUHE_REAL_DEEPSEEK_MODELS') || 'deepseek-v4-flash,deepseek-v4-pro'
  const models = [...new Set(value.split(',').map((item) => item.trim()).filter(Boolean))]
  if (!models.length) {
    throw new Error('JUHE_REAL_DEEPSEEK_MODELS must contain at least one model')
  }
  return models
}

function codexBridgeModelMappings(models: string[]): AccountModelMapping[] {
  return models.map((model) => ({
    sourceModel: model,
    sourceEndpointFamily: 'responses',
    upstreamModel: model,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }))
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function fetchWithTimeout(url: string, init: RequestInit = {}): Promise<Response> {
  const timeoutSignal = AbortSignal.timeout(requestTimeoutMs)
  return fetch(url, {
    ...init,
    signal: init.signal ?? timeoutSignal
  })
}

function sanitizeSecretText(text: string): string {
  return text
    .replaceAll(realApiKey, '[REDACTED_DEEPSEEK_KEY]')
    .replace(/sk-[A-Za-z0-9_\-]{12,}/g, '[REDACTED_SK_KEY]')
}

function responseSnippet(text: string): string {
  const sanitized = sanitizeSecretText(text).replace(/\s+/g, ' ').trim()
  return sanitized.length > 800 ? `${sanitized.slice(0, 800)}...` : sanitized
}

function wait(ms: number): Promise<void> {
  return ms > 0 ? new Promise((resolvePromise) => setTimeout(resolvePromise, ms)) : Promise.resolve()
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
