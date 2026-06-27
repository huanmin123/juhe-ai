import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
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
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: codingGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const generalApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'GLM 真实上游 E2E Key',
      groupBindings: [{ groupId: generalGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(generalApiKey.key, '回归 API Key 未返回明文密钥')
    const codingApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
        codingChat: await runRealScenario(() => assertRealGlmChatModel(gatewayBaseUrl, codingApiKey.key, model))
      })
    }
    assert(results.some((item) => item.chat.ok), `GLM 真实 Chat 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)
    assert(results.some((item) => item.codingChat.ok), `GLM Coding 真实 Chat 至少需要一个模型通过，实际结果：${JSON.stringify(results, null, 2)}`)

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
  const models = (value || 'glm-5.2,glm-5-turbo,glm-4.7-flash')
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

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  return String(error)
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
