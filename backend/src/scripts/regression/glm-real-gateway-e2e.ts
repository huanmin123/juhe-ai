import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

type RealScenarioResult =
  | ({ ok: true } & Record<string, unknown>)
  | { ok: false; error: string }

const tempRoot = resolve(tmpdir(), `juhe-ai-glm-real-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'glm-real-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'glm-real-gateway-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const realApiKey = requiredEnv('GLM_REAL_API_KEY')
const realBaseUrl = normalizeBaseUrl(process.env.GLM_REAL_BASE_URL || 'https://vsllm.com/v1')
const realModels = modelsFromEnv(process.env.GLM_REAL_MODELS)

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
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
  let appServer: http.Server | undefined
  try {
    const generalGroup = repositories.createGroup({
      name: 'GLM 真实上游 E2E 分组',
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const codingGroup = repositories.createGroup({
      name: 'GLM Coding 真实上游 E2E 分组',
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      name: 'GLM 真实上游 E2E 账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: generalGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    repositories.createAccount({
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      name: 'GLM Coding 真实上游 E2E 账户',
      type: 'api_key',
      clientCompatibility: 'codex_responses',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: codingGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const generalApiKey = repositories.createApiKeyRecord({
      name: 'GLM 真实上游 E2E Key',
      groupBindings: [{ groupId: generalGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(generalApiKey.key, '回归 API Key 未返回明文密钥')
    const codingApiKey = repositories.createApiKeyRecord({
      name: 'GLM Coding 真实上游 E2E Key',
      groupBindings: [{ groupId: codingGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(codingApiKey.key, '回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const results = []
    for (const model of realModels) {
      results.push({
        model,
        chat: await runRealScenario(() => assertRealGlmChatModel(gatewayBaseUrl, generalApiKey.key, model)),
        codexBridge: await runRealScenario(() => assertRealGlmCodexBridgeModel(gatewayBaseUrl, codingApiKey.key, model))
      })
    }
    assert(results.some((item) => item.chat.ok), `GLM 真实 Chat 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)
    assert(results.some((item) => item.codexBridge.ok), `GLM 真实 Codex bridge 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)

    console.log(JSON.stringify({
      message: 'glm real gateway e2e passed',
      baseUrl: realBaseUrl,
      models: results
    }, null, 2))
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

async function runRealScenario(fn: () => Promise<Record<string, unknown>>): Promise<RealScenarioResult> {
  try {
    const value = await fn()
    return {
      ok: true,
      ...value
    }
  } catch (error) {
    return {
      ok: false,
      error: errorMessage(error)
    }
  }
}

async function assertRealGlmChatModel(baseUrl: string, localApiKey: string, model: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [
        { role: 'system', content: '你是连通性测试助手。' },
        { role: 'user', content: '只回复两个字：通过' }
      ],
      stream: false,
      temperature: 0,
      max_tokens: 128
    })
  }, 60_000)
  const text = await response.text()
  assert.equal(response.status, 200, `GLM 真实模型 ${model} 应返回 200，实际 HTTP ${response.status}: ${text.slice(0, 1000)}`)
  const parsed = parseJsonObject(text)
  const content = firstChoiceText(parsed)
  assert(content.trim(), `GLM 真实模型 ${model} 返回内容为空：${text.slice(0, 1000)}`)
  return {
    status: response.status,
    contentSample: content.trim().slice(0, 80),
    usage: parsed.usage && typeof parsed.usage === 'object' ? parsed.usage : undefined
  }
}

async function assertRealGlmCodexBridgeModel(baseUrl: string, localApiKey: string, model: string): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: `glm-real-${model}`,
        thread_id: `glm-real-${model}`,
        turn_id: `glm-real-turn-${model}`
      })
    },
    body: JSON.stringify({
      model,
      instructions: '你是连通性测试助手。',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: '只回复两个字：通过' }]
        }
      ],
      stream: true,
      store: false,
      max_output_tokens: 128,
      tools: [
        {
          type: 'function',
          name: 'noop_tool',
          description: 'No-op test tool. Do not call it.',
          parameters: {
            type: 'object',
            properties: {},
            additionalProperties: false
          }
        }
      ],
      tool_choice: 'auto',
      parallel_tool_calls: false
    })
  }, 60_000)
  const text = await response.text()
  assert.equal(response.status, 200, `GLM 真实模型 ${model} Codex bridge 应返回 200，实际 HTTP ${response.status}: ${text.slice(0, 1000)}`)
  assert.match(text, /event: response\.completed/, `GLM 真实模型 ${model} Codex bridge 应返回 Responses completed 事件：${text.slice(0, 1000)}`)
  assert(!text.includes('chat.completion.chunk'), 'Codex bridge 不应向下游泄漏 Chat Completions chunk')
  const content = outputTextFromResponsesSse(text)
  assert(content.trim(), `GLM 真实模型 ${model} Codex bridge 返回内容为空：${text.slice(0, 1000)}`)
  return {
    status: response.status,
    contentSample: content.trim().slice(0, 80),
    responseEvents: responseEventNames(text).slice(0, 12)
  }
}

async function fetchWithTimeout(url: string, init: RequestInit, timeoutMs: number): Promise<Response> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error(`请求 ${timeoutMs}ms 超时`)), timeoutMs)
  try {
    return await fetch(url, {
      ...init,
      signal: controller.signal
    })
  } finally {
    clearTimeout(timer)
  }
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) {
    throw new Error(`${name} 未设置；运行真实 GLM 测试前请通过环境变量传入 API Key`)
  }
  return value
}

function normalizeBaseUrl(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) throw new Error('GLM_REAL_BASE_URL 不能为空')
  return trimmed.replace(/\/+$/, '')
}

function modelsFromEnv(value: string | undefined): string[] {
  const models = (value || 'glm-5.2-free,glm-5.2,glm-5-turbo')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
  if (!models.length) throw new Error('GLM_REAL_MODELS 至少需要一个模型')
  return models
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), `响应不是 JSON 对象：${text.slice(0, 1000)}`)
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

function outputTextFromResponsesSse(text: string): string {
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

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  return String(error)
}

function responseEventNames(text: string): string[] {
  return text
    .split(/\r?\n/)
    .filter((line) => line.startsWith('event:'))
    .map((line) => line.slice('event:'.length).trim())
}

function responseSseDataObjects(text: string): Record<string, unknown>[] {
  const events: Record<string, unknown>[] = []
  for (const block of text.split(/\r?\n\r?\n/)) {
    const data = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice('data:'.length).trimStart())
      .join('\n')
    if (!data || data === '[DONE]') continue
    try {
      const parsed = JSON.parse(data) as unknown
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        events.push(parsed as Record<string, unknown>)
      }
    } catch {
      // Ignore non-JSON diagnostic fragments in real upstream tests.
    }
  }
  return events
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
