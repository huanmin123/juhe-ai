import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { delimiter as pathDelimiter, dirname, join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ANTHROPIC_ANTHROPIC_V1_PROFILE_ID, ANTHROPIC_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import {
  getUsageRecordShardDatabase,
  usageRecordShardLocationFromKey
} from '../../storage/usage-record-shards.js'

interface GatewayCapture {
  path: string
  method: string
  authorizationPresent: boolean
  xApiKeyPresent: boolean
  clientProfileHeader?: string
  anthropicVersion?: string
  anthropicBeta?: string
  bodySummary: Record<string, unknown>
}

interface ModelListItem {
  id?: string
  display_name?: string
  type?: string
}

interface TestCaseResult {
  name: string
  status: 'passed' | 'failed' | 'skipped'
  detail?: string
}

const realApiKey = requiredEnv('JUHE_REAL_ANTHROPIC_API_KEY')
const realBaseUrl = process.env.JUHE_REAL_ANTHROPIC_BASE_URL?.trim() || 'https://api.anthropic.com'
const requestedModel = process.env.JUHE_REAL_ANTHROPIC_MODEL?.trim()
const runClaudeCodeCapture = truthyEnv('JUHE_REAL_RUN_CLAUDE_CODE')
const countTokensMode = countTokensRunMode()
let runCountTokensCase = false
const runAllErrorCases = truthyEnv('JUHE_REAL_RUN_ERROR_CASES', true)
const runInvalidRequestCase = truthyEnv('JUHE_REAL_RUN_INVALID_REQUEST_CASE', runAllErrorCases)
const runInvalidModelCase = truthyEnv('JUHE_REAL_RUN_INVALID_MODEL_CASE', runAllErrorCases)
const runToolCase = truthyEnv('JUHE_REAL_RUN_TOOL_CASE', true)
const runParallelCase = truthyEnv('JUHE_REAL_RUN_PARALLEL_CASE', true)
const timeoutMs = positiveIntEnv('JUHE_REAL_REQUEST_TIMEOUT_MS') ?? 120_000
const requestIntervalMs = positiveIntEnv('JUHE_REAL_REQUEST_INTERVAL_MS') ?? 0

const tempRoot = resolve(tmpdir(), `juhe-ai-anthropic-real-gateway-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'anthropic-real-gateway.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'anthropic-real-gateway-e2e-secret'
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
const gatewayCaptures: GatewayCapture[] = []
const results: TestCaseResult[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '16mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res, { exposeUpstreamDiagnostics: true })
  } catch (error) {
    next(error)
  }
})

let appServer: http.Server | undefined
let nextRealRequestAt = 0

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  const discoveredModels = await discoverModels()
  const model = requestedModel || preferredModel(discoveredModels)
  assert(model, '没有可用于真实 Anthropic Messages 测试的模型；请设置 JUHE_REAL_ANTHROPIC_MODEL')
  runCountTokensCase = countTokensMode === 'required' || (countTokensMode === 'auto' && await probeCountTokensSupport(model))
  if (countTokensMode === 'auto' && !runCountTokensCase) {
    results.push({
      name: 'count-tokens-capability',
      status: 'skipped',
      detail: '真实上游未声明 /v1/messages/count_tokens 可用，账号能力不包含 message_token_counting'
    })
  }

  const group = repositories.createGroup({
    name: 'Anthropic 真实模型联调分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Anthropic 真实模型联调账户',
    type: 'api_key',
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: realSupportedEndpointModes()
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 8
  }, access)
  const localApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic 真实模型联调本地 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(localApiKey.key, '真实联调本地 API Key 未返回明文密钥')

  appServer = http.createServer(app)
  await listen(appServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  await runCase('stored-account-snapshot', async () => {
    const database = databaseModule.getBusinessDatabase()
    const row = database.prepare(`
      SELECT accounts.id, accounts.status, accounts.schedulable, accounts.concurrency_limit,
             group_accounts.group_id AS group_id, group_accounts.enabled AS group_enabled
      FROM accounts
      LEFT JOIN group_accounts ON group_accounts.account_id = accounts.id
      WHERE accounts.provider_code = ?
      ORDER BY accounts.created_at DESC
      LIMIT 1
    `).get(ANTHROPIC_PROVIDER_CODE) as {
      id: string
      status: string
      schedulable: number
      concurrency_limit: number
      group_id: string | null
      group_enabled: number | null
    } | undefined
    assert(row, '真实联调账号未写入 accounts')
    assert.equal(row.status, 'active', '真实联调账号状态应为 active')
    assert.equal(row.schedulable, 1, '真实联调账号应可调度')
    assert.equal(row.group_id, group.id, '真实联调账号应绑定当前分组')
    assert.equal(row.group_enabled, 1, '真实联调账号分组绑定应启用')
    return `account=${row.id}, concurrency=${row.concurrency_limit}`
  })
  await runCase('runtime-account-selection-snapshot', async () => {
    const apiKey = repositories.findApiKeySecret(localApiKey.id, access)
    const routeStrategy = apiKey ? repositories.findRouteStrategySummary(apiKey.routeStrategyId, access) : undefined
    assert(routeStrategy?.groupBindings.some((binding) => binding.groupId === group.id && binding.status === 'active'), '真实联调策略路由未绑定当前分组')
    const selection = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId)
    assert(selection.accounts.length > 0, `真实联调分组没有可调度账号：${JSON.stringify(selection.diagnostics)}`)
    const account = selection.accounts[0]
    const endpointModes = account.supportedEndpointModes ?? []
    assert.equal(account.providerCode, ANTHROPIC_PROVIDER_CODE, '真实联调分组选中的账号供应商应为 Anthropic')
    assert(endpointModes.includes('messages_json'), '真实联调账号应支持 Messages JSON')
    assert(endpointModes.includes('messages_sse'), '真实联调账号应支持 Messages SSE')
    if (runCountTokensCase) {
      assert(endpointModes.includes('message_token_counting'), '真实联调账号应支持 Count Tokens')
    } else {
      assert(!endpointModes.includes('message_token_counting'), '跳过 Count Tokens 时账号能力不应声明 message_token_counting')
    }
    return `accounts=${selection.accounts.length}, diagnostics=${JSON.stringify(selection.diagnostics)}`
  })
  await runCase('direct-models', async () => {
    assert(discoveredModels.length > 0, '真实上游 /v1/models 没有返回模型列表')
    return `${discoveredModels.length} models, selected ${model}`
  })
  await runCase('gateway-models-local', async () => {
    const body = await fetchJson(`${gatewayBaseUrl}/v1/models`, {
      headers: {
        'x-api-key': localApiKey.key!
      }
    })
    assert(Array.isArray(body.data), '本地 /v1/models 响应缺少 data 数组')
    return `local models ${body.data.length}`
  })
  await runCase('gateway-runtime-cache-snapshot', async () => {
    const runtime = await gatewayCache.readCachedGatewayRuntimeAsync(localApiKey.key!)
    assert(runtime.apiKey, '真实联调本地 API Key 无法通过网关 runtime 校验')
    assert.equal(runtime.apiKey.selected_group_id, group.id, '网关 runtime 选中分组应为真实联调分组')
    assert(runtime.groupAccess, '网关 runtime 缺少分组访问上下文')
    assert(runtime.accounts.length > 0, `网关 runtime 没有账号：${JSON.stringify(runtime.accountDispatchDiagnostics)}`)
    const account = runtime.accounts[0]
    return `selected=${runtime.apiKey.selected_group_id}, accounts=${runtime.accounts.length}, first=${account.id}:${account.providerCode}:${account.baseUrl}:${(account.supportedEndpointModes ?? []).join('|')}`
  })
  await runCase('messages-json-marker', async () => {
    const marker = 'OK'
    const body = await sendGatewayMessage(gatewayBaseUrl, localApiKey.key!, {
      model,
      max_tokens: 512,
      temperature: 0,
      stream: false,
      messages: [{ role: 'user', content: `Reply with this short confirmation word: ${marker}` }]
    })
    assertTextIncludes(anthropicContentText(body), marker, 'Messages JSON 没有返回预期 marker')
    usageRecordQueue.flushAllUsageRecordQueue()
    const usageRecord = latestGatewayUsageRecord(localApiKey.id)
    assertUsageRecordTokens(usageRecord, 'Messages JSON 使用记录')
    return `response usage ${usageSummary(body.usage)}; recorded input=${usageRecord.input_tokens}, output=${usageRecord.output_tokens}`
  })
  await runCase('messages-sse-marker', async () => {
    const marker = 'OK'
    const text = await sendGatewaySse(gatewayBaseUrl, localApiKey.key!, {
      model,
      max_tokens: 512,
      temperature: 0,
      stream: true,
      messages: [{ role: 'user', content: `Reply with this short confirmation word: ${marker}` }]
    })
    assert.match(text, /event:\s*message_stop/, 'Messages SSE 缺少 message_stop')
    assertTextIncludes(extractSseTextDeltas(text), marker, 'Messages SSE 没有返回预期 marker')
    usageRecordQueue.flushAllUsageRecordQueue()
    const usageRecord = latestGatewayUsageRecord(localApiKey.id)
    assertUsageRecordTokens(usageRecord, 'Messages SSE 使用记录')
    return `${Buffer.byteLength(text, 'utf8')} bytes; recorded input=${usageRecord.input_tokens}, output=${usageRecord.output_tokens}`
  })
  if (runCountTokensCase) {
    await runCase('count-tokens', async () => {
      const body = await fetchJson(`${gatewayBaseUrl}/v1/messages/count_tokens`, {
        method: 'POST',
        headers: {
          authorization: `Bearer ${localApiKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify({
          model,
          messages: [{ role: 'user', content: 'Count this stable Anthropic gateway request.' }]
        })
      })
      assert(Number(body.input_tokens) > 0, 'Count Tokens 没有返回正数 input_tokens')
      return `input_tokens ${body.input_tokens}`
    })
  }
  await runCase('claude-code-profile-header', async () => {
    const marker = 'OK'
    const body = await sendGatewayMessage(gatewayBaseUrl, localApiKey.key!, {
      model,
      max_tokens: 512,
      temperature: 0,
      stream: false,
      messages: [{ role: 'user', content: `Reply with this short confirmation word: ${marker}` }]
    }, {
      'x-juhe-client-profile': 'claude_code'
    })
    assertTextIncludes(anthropicContentText(body), marker, 'Claude Code profile header 请求没有返回预期 marker')
    return 'profile accepted'
  })
  if (runToolCase) {
    await runCase('tool-use-forced', async () => {
      const body = await sendGatewayMessage(gatewayBaseUrl, localApiKey.key!, {
        model,
        max_tokens: 256,
        temperature: 0,
        stream: false,
        tools: [{
          name: 'get_weather',
          description: 'Get weather for a city.',
          input_schema: {
            type: 'object',
            properties: {
              city: { type: 'string' }
            },
            required: ['city']
          }
        }],
        tool_choice: { type: 'tool', name: 'get_weather' },
        messages: [{ role: 'user', content: 'Use the get_weather tool for Shanghai.' }]
      })
      const toolUse = Array.isArray(body.content) && body.content.some((item: unknown) =>
        typeof item === 'object' && item !== null && (item as { type?: unknown }).type === 'tool_use'
      )
      assert.equal(toolUse, true, '强制 tool_choice 没有返回 tool_use content block')
      return 'tool_use returned'
    })
  }
  if (runInvalidRequestCase) {
    await runCase('invalid-request-stable-error', async () => {
      const response = await fetchWithTimeout(`${gatewayBaseUrl}/v1/messages`, {
        method: 'POST',
        headers: {
          authorization: `Bearer ${localApiKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify({
          model,
          messages: [{ role: 'user', content: 'This request intentionally omits max_tokens.' }]
        })
      })
      const text = await response.text()
      assert.doesNotMatch(text, /sk-[A-Za-z0-9]/, '错误响应不应泄漏 API Key')
      return response.status >= 400
        ? `HTTP ${response.status}`
        : `compatible upstream accepted missing max_tokens with HTTP ${response.status}`
    })
  }
  if (runInvalidModelCase) {
    await runCase('invalid-model-stable-error', async () => {
      const response = await fetchWithTimeout(`${gatewayBaseUrl}/v1/messages`, {
        method: 'POST',
        headers: {
          'x-api-key': localApiKey.key!,
          'content-type': 'application/json'
        },
        body: JSON.stringify({
          model: `juhe-invalid-model-${Date.now()}`,
          max_tokens: 16,
          messages: [{ role: 'user', content: 'This request intentionally uses an invalid model.' }]
        })
      })
      const text = await response.text()
      assert(response.status >= 400, `无效模型应返回错误状态，实际 ${response.status}: ${text.slice(0, 300)}`)
      assert.doesNotMatch(text, /sk-[A-Za-z0-9]/, '错误响应不应泄漏 API Key')
      return `HTTP ${response.status}`
    })
  }
  if (runParallelCase) {
    await runCase('parallel-json-3', async () => {
      const markers = ['OK1', 'OK2', 'OK3']
      const bodies = await Promise.all(markers.map((marker) => sendGatewayMessage(gatewayBaseUrl, localApiKey.key!, {
        model,
        max_tokens: 512,
        temperature: 0,
        stream: false,
        messages: [{ role: 'user', content: `Reply with this short confirmation word: ${marker}` }]
      })))
      bodies.forEach((body, index) => assertTextIncludes(anthropicContentText(body), markers[index], `并发请求 ${index + 1} 没有返回预期 marker`))
      return '3 concurrent requests passed'
    })
  }
  if (runClaudeCodeCapture) {
    await runCase('official-claude-code-capture', async () => {
      const before = gatewayCaptures.length
      const marker = `JUHE_REAL_CLAUDE_CODE_CLI_OK_${Date.now()}`
      const output = await runClaudeCodeCli({
        gatewayBaseUrl,
        localApiKey: localApiKey.key!,
        model,
        prompt: `Reply with this short confirmation word: ${marker}`
      })
      assertTextIncludes(output, marker, 'Claude Code CLI 输出没有返回预期 marker')
      const captured = gatewayCaptures.slice(before)
      assert(captured.some((item) => item.path.split('?', 1)[0]?.endsWith('/messages')), 'Claude Code CLI 没有命中本地 /v1/messages')
      return `captured ${captured.length} gateway request(s)`
    })
  }

  printSummary({
    baseUrl: realBaseUrl,
    model,
    discoveredModelCount: discoveredModels.length,
    gatewayCaptures
  })
} finally {
  await closeServer(appServer)
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function discoverModels(): Promise<string[]> {
  const response = await fetchWithTimeout(appendBasePath(realBaseUrl, '/v1/models'), {
    headers: {
      'x-api-key': realApiKey,
      'anthropic-version': '2023-06-01'
    }
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`真实上游 /v1/models 失败：HTTP ${response.status} ${text.slice(0, 300)}`)
  }
  const parsed = safeJson(text) as { data?: ModelListItem[] }
  return Array.isArray(parsed.data)
    ? parsed.data.map((item) => item.id).filter((id): id is string => typeof id === 'string' && id.trim().length > 0)
    : []
}

async function probeCountTokensSupport(model: string): Promise<boolean> {
  const response = await fetchWithTimeout(appendBasePath(realBaseUrl, '/v1/messages/count_tokens'), {
    method: 'POST',
    headers: {
      'x-api-key': realApiKey,
      'anthropic-version': '2023-06-01',
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: 'Count this stable Anthropic capability probe.' }]
    })
  })
  const text = await response.text()
  if (!response.ok) {
    return false
  }
  const parsed = safeJson(text) as { input_tokens?: unknown }
  return Number(parsed.input_tokens) > 0
}

function preferredModel(models: string[]): string | undefined {
  const normalized = models.map((model) => model.trim()).filter(Boolean)
  const preferred = [
    'claude-haiku-4-5-20251001',
    'claude-haiku-4-5',
    'claude-sonnet-4-6',
    'claude-sonnet-4-5',
    'claude-fable-5'
  ]
  for (const model of preferred) {
    if (normalized.includes(model)) return model
  }
  return normalized.find((model) => /^claude/i.test(model)) ?? normalized[0]
}

function realSupportedEndpointModes(): string[] {
  return runCountTokensCase
    ? ['messages_json', 'messages_sse', 'message_token_counting']
    : ['messages_json', 'messages_sse']
}

async function runCase(name: string, fn: () => Promise<string | undefined>): Promise<void> {
  try {
    const detail = await fn()
    results.push({ name, status: 'passed', detail })
  } catch (error) {
    results.push({ name, status: 'failed', detail: error instanceof Error ? error.message : String(error) })
    console.error(JSON.stringify({
      failedCase: name,
      results,
      gatewayCaptures
    }, null, 2))
    throw error
  }
}

async function sendGatewayMessage(
  gatewayBaseUrl: string,
  localApiKey: string,
  payload: Record<string, unknown>,
  extraHeaders: Record<string, string> = {}
): Promise<Record<string, unknown>> {
  return await fetchJson(`${gatewayBaseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      ...extraHeaders
    },
    body: JSON.stringify(payload)
  })
}

async function sendGatewaySse(
  gatewayBaseUrl: string,
  localApiKey: string,
  payload: Record<string, unknown>
): Promise<string> {
  const response = await fetchWithTimeout(`${gatewayBaseUrl}/v1/messages`, {
    method: 'POST',
    headers: {
      'x-api-key': localApiKey,
      accept: 'text/event-stream',
      'content-type': 'application/json'
    },
    body: JSON.stringify(payload)
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`SSE 请求失败：HTTP ${response.status} ${text.slice(0, 500)}`)
  }
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'SSE 响应 content-type 应是 text/event-stream')
  return text
}

async function fetchJson(url: string, init: RequestInit): Promise<Record<string, unknown>> {
  const response = await fetchWithTimeout(url, init)
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${text.slice(0, 500)}`)
  }
  return safeJson(text) as Record<string, unknown>
}

async function fetchWithTimeout(url: string, init: RequestInit): Promise<Response> {
  let lastError: unknown
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    await waitForRealRequestSlot()
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), timeoutMs)
    try {
      return await fetch(url, {
        ...init,
        headers: requestHeadersWithConnectionClose(init.headers),
        signal: controller.signal
      })
    } catch (error) {
      lastError = error
      if (!isTransientFetchTransportError(error) || attempt >= 3) {
        throw error
      }
      await sleep(500 * attempt)
    } finally {
      clearTimeout(timeout)
    }
  }
  throw lastError
}

function requestHeadersWithConnectionClose(headers: HeadersInit | undefined): Headers {
  const output = new Headers(headers)
  output.set('connection', 'close')
  return output
}

function isTransientFetchTransportError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  const text = `${error.name} ${error.message} ${String((error as { cause?: unknown }).cause ?? '')}`
  return /fetch failed|ECONNRESET|UND_ERR_SOCKET|socket|terminated/i.test(text)
}

async function waitForRealRequestSlot(): Promise<void> {
  if (requestIntervalMs <= 0) return
  const waitMs = nextRealRequestAt - Date.now()
  if (waitMs > 0) {
    await sleep(waitMs)
  }
  nextRealRequestAt = Date.now() + requestIntervalMs
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, Math.max(0, ms)))
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    throw new Error(`响应不是合法 JSON：${text.slice(0, 300)}`)
  }
}

function anthropicContentText(body: Record<string, unknown>): string {
  const content = Array.isArray(body.content) ? body.content : []
  return content.map((item) => {
    if (typeof item === 'object' && item !== null) {
      const record = item as Record<string, unknown>
      if (typeof record.text === 'string') return record.text
      if (typeof record.thinking === 'string') return record.thinking
      if (record.type === 'tool_use') return JSON.stringify(record.input ?? {})
    }
    return ''
  }).join('\n')
}

function extractSseTextDeltas(text: string): string {
  const output: string[] = []
  for (const rawEvent of text.split(/\r?\n\r?\n/)) {
    const dataLines = rawEvent
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice('data:'.length).trim())
    if (!dataLines.length) continue
    const data = safeParseJson(dataLines.join('\n'))
    const delta = objectValue(data)?.delta
    const deltaObject = objectValue(delta)
    if (typeof deltaObject?.text === 'string') output.push(deltaObject.text)
    if (typeof deltaObject?.partial_json === 'string') output.push(deltaObject.partial_json)
    if (typeof deltaObject?.thinking === 'string') output.push(deltaObject.thinking)
  }
  return output.join('')
}

function assertTextIncludes(actual: string, expected: string, message: string): void {
  const normalizedActual = actual.replace(/\s+/g, '')
  const normalizedExpected = expected.replace(/\s+/g, '')
  assert(normalizedActual.includes(normalizedExpected), `${message}；实际输出：${actual.slice(0, 300)}`)
}

function usageSummary(value: unknown): string {
  const usage = objectValue(value)
  return `input=${usage?.input_tokens ?? 0}, output=${usage?.output_tokens ?? 0}, cache_read=${usage?.cache_read_input_tokens ?? 0}`
}

function latestGatewayUsageRecord(apiKeyId: string): { input_tokens?: number | null; output_tokens?: number | null } {
  const indexRow = databaseModule.getUsageCatalogDatabase()
    .prepare(`
      SELECT usage_id, shard_key
      FROM usage_record_shard_entries
      WHERE api_key_id = ? AND traffic_source = 'gateway' AND success = 1
      ORDER BY created_at DESC, usage_id DESC
      LIMIT 1
    `)
    .get(apiKeyId) as { usage_id?: string; shard_key?: string } | undefined
  assert(indexRow?.usage_id && indexRow.shard_key, '没有找到成功的 gateway 使用记录索引')
  const location = usageRecordShardLocationFromKey(indexRow.shard_key)
  assert(location, `使用记录 shard key 无效：${indexRow.shard_key}`)
  const row = getUsageRecordShardDatabase(location)
    .prepare(`
      SELECT input_tokens, output_tokens
      FROM usage_records
      WHERE id = ?
      LIMIT 1
    `)
    .get(indexRow.usage_id) as { input_tokens?: number | null; output_tokens?: number | null } | undefined
  assert(row, '没有找到成功的 gateway 使用记录明细')
  return row
}

function assertUsageRecordTokens(record: { input_tokens?: number | null; output_tokens?: number | null }, label: string): void {
  assert(Number(record.input_tokens) > 0, `${label} input_tokens 应为正数`)
  assert(Number(record.output_tokens) > 0, `${label} output_tokens 应为正数`)
}

function captureIncomingGatewayRequest(req: Request, _res: ExpressResponse, next: NextFunction): void {
  const rawBody = Buffer.isBuffer((req as { rawBody?: unknown }).rawBody)
    ? (req as unknown as { rawBody: Buffer }).rawBody
    : Buffer.alloc(0)
  gatewayCaptures.push({
    path: req.originalUrl || req.path,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    xApiKeyPresent: Boolean(req.headers['x-api-key']),
    clientProfileHeader: headerText(req.headers['x-juhe-client-profile']),
    anthropicVersion: headerText(req.headers['anthropic-version']),
    anthropicBeta: headerText(req.headers['anthropic-beta']),
    bodySummary: summarizeRequestBody(rawBody)
  })
  next()
}

function summarizeRequestBody(rawBody: Buffer): Record<string, unknown> {
  if (!rawBody.byteLength) return { bytes: 0 }
  const parsed = safeParseJson(rawBody.toString('utf8'))
  const body = objectValue(parsed)
  if (!body) return { bytes: rawBody.byteLength, json: false }
  return {
    bytes: rawBody.byteLength,
    model: typeof body.model === 'string' ? body.model : undefined,
    stream: body.stream === true,
    maxTokens: typeof body.max_tokens === 'number' ? body.max_tokens : undefined,
    messageCount: Array.isArray(body.messages) ? body.messages.length : undefined,
    toolCount: Array.isArray(body.tools) ? body.tools.length : undefined,
    hasSystem: typeof body.system === 'string' || Array.isArray(body.system),
    hasThinking: typeof body.thinking === 'object' && body.thinking !== null
  }
}

function safeParseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function headerText(value: unknown): string | undefined {
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function appendBasePath(baseUrl: string, path: string): string {
  const url = new URL(baseUrl)
  const normalizedBasePath = url.pathname.replace(/\/+$/, '')
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  if (normalizedBasePath.endsWith('/v1') && normalizedPath.startsWith('/v1/')) {
    url.pathname = `${normalizedBasePath}${normalizedPath.slice(3)}`
  } else {
    url.pathname = `${normalizedBasePath}${normalizedPath}`
  }
  return url.toString()
}

function runClaudeCodeCli(input: {
  gatewayBaseUrl: string
  localApiKey: string
  model: string
  prompt: string
}): Promise<string> {
  const claudeArgs = [
    '--print',
    '--setting-sources',
    'local',
    '--no-session-persistence',
    '--output-format',
    'text',
    '--model',
    input.model,
    '--max-budget-usd',
    '5',
    input.prompt
  ]
  const npxArgs = ['-y', '@anthropic-ai/claude-code@latest', ...claudeArgs]
  const directLauncher = process.platform === 'win32' ? resolveWindowsNodeCliLauncher('claude') : undefined
  const command = directLauncher?.command ?? (process.platform === 'win32' ? process.env.ComSpec || 'cmd.exe' : 'npx')
  const args = directLauncher
    ? [...directLauncher.args, ...claudeArgs]
    : process.platform === 'win32'
      ? ['/d', '/s', '/c', windowsCommandLine(['npx', ...npxArgs])]
      : npxArgs
  const cliRoot = createCliRoot('real-claude-code')
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const child = spawn(command, args, {
      cwd: cliRoot,
      env: isolatedCliEnv(cliRoot, {
        ANTHROPIC_BASE_URL: input.gatewayBaseUrl,
        ANTHROPIC_API_KEY: input.localApiKey,
        CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: '1',
        CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS: '1',
        DISABLE_AUTOUPDATER: '1',
        DISABLE_BUG_COMMAND: '1',
        DISABLE_ERROR_REPORTING: '1',
        DISABLE_TELEMETRY: '1',
        DISABLE_FEEDBACK_COMMAND: '1'
      }),
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    const settle = (fn: () => void) => {
      if (settled) return
      settled = true
      fn()
    }
    const timeout = setTimeout(() => {
      child.kill()
      settle(() => rejectPromise(new Error(`Claude Code CLI 超时：stdout=${sanitizeSecretText(Buffer.concat(stdout).toString('utf8')).slice(0, 1000)}；stderr=${sanitizeSecretText(Buffer.concat(stderr).toString('utf8')).slice(0, 1000)}`)))
    }, Math.max(timeoutMs, 180_000) + 60_000)
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.stdin.end()
    child.on('error', (error) => {
      clearTimeout(timeout)
      settle(() => rejectPromise(error))
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      const output = Buffer.concat(stdout).toString('utf8')
      const errorOutput = Buffer.concat(stderr).toString('utf8')
      if (code === 0) {
        settle(() => resolvePromise(output))
      } else {
        settle(() => rejectPromise(new Error(`Claude Code CLI 退出码 ${code}: ${sanitizeSecretText(errorOutput || output).slice(0, 1000)}`)))
      }
    })
  })
}

function createCliRoot(name: string): string {
  const root = join(tempRoot, 'cli', name)
  mkdirSync(root, { recursive: true })
  return root
}

function isolatedCliEnv(cliRoot: string, extra: Record<string, string>): Record<string, string> {
  const env = sanitizedProcessEnv()
  for (const key of Object.keys(env)) {
    if (isAiCredentialEnvName(key)) {
      delete env[key]
    }
  }
  const appData = join(cliRoot, 'AppData', 'Roaming')
  const localAppData = join(cliRoot, 'AppData', 'Local')
  const xdgConfig = join(cliRoot, '.config')
  const xdgData = join(cliRoot, '.local', 'share')
  const xdgCache = join(cliRoot, '.cache')
  mkdirSync(appData, { recursive: true })
  mkdirSync(localAppData, { recursive: true })
  mkdirSync(xdgConfig, { recursive: true })
  mkdirSync(xdgData, { recursive: true })
  mkdirSync(xdgCache, { recursive: true })
  return {
    ...env,
    HOME: cliRoot,
    USERPROFILE: cliRoot,
    APPDATA: appData,
    LOCALAPPDATA: localAppData,
    XDG_CONFIG_HOME: xdgConfig,
    XDG_DATA_HOME: xdgData,
    XDG_CACHE_HOME: xdgCache,
    ...extra
  }
}

function isAiCredentialEnvName(key: string): boolean {
  const normalized = key.toUpperCase()
  return normalized.includes('OPENAI')
    || normalized.includes('ANTHROPIC')
    || normalized.includes('CLAUDE')
    || normalized.includes('CODEX')
    || normalized.includes('OPENCODE')
    || normalized.includes('DEEPSEEK')
    || normalized.includes('GLM')
    || normalized.endsWith('API_KEY')
    || normalized.endsWith('AUTH_TOKEN')
    || normalized.endsWith('ACCESS_TOKEN')
}

function resolveWindowsNodeCliLauncher(command: string): { command: string; args: string[] } | undefined {
  const entryByCommand: Record<string, string> = {
    claude: 'node_modules\\@anthropic-ai\\claude-code\\cli.js'
  }
  const entry = entryByCommand[command]
  if (!entry) return undefined
  const commandPath = findOnPath(`${command}.cmd`) ?? findOnPath(`${command}.ps1`) ?? findOnPath(command)
  if (!commandPath) return undefined
  const baseDir = dirname(commandPath)
  const entryPath = join(baseDir, entry)
  if (!existsSync(entryPath)) return undefined
  const bundledNode = join(baseDir, 'node.exe')
  return {
    command: existsSync(bundledNode) ? bundledNode : 'node.exe',
    args: [entryPath]
  }
}

function findOnPath(filename: string): string | undefined {
  const pathValue = process.env.PATH ?? process.env.Path ?? ''
  for (const rawDir of pathValue.split(pathDelimiter)) {
    const dir = rawDir.trim().replace(/^"|"$/g, '')
    if (!dir) continue
    const candidate = join(dir, filename)
    if (existsSync(candidate)) return candidate
  }
  return undefined
}

function windowsCommandLine(args: string[]): string {
  return args.map(quoteWindowsCommandArg).join(' ')
}

function quoteWindowsCommandArg(value: string): string {
  if (/^[A-Za-z0-9_./:@=+-]+$/.test(value)) {
    return value
  }
  return `"${value.replace(/(["^&|<>%])/g, '^$1')}"`
}

function sanitizedProcessEnv(): Record<string, string> {
  const output: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (!key || key.includes('=') || value === undefined) continue
    output[key] = value
  }
  return output
}

function printSummary(input: {
  baseUrl: string
  model: string
  discoveredModelCount: number
  gatewayCaptures: GatewayCapture[]
}): void {
  const failed = results.filter((item) => item.status === 'failed')
  const summary = {
    upstreamBaseUrl: input.baseUrl,
    selectedModel: input.model,
    discoveredModelCount: input.discoveredModelCount,
    results,
    gatewayCaptureCount: input.gatewayCaptures.length,
    gatewayCaptures: input.gatewayCaptures.map((item) => ({
      ...item,
      authorizationPresent: item.authorizationPresent,
      xApiKeyPresent: item.xApiKeyPresent
    }))
  }
  console.log(JSON.stringify(summary, null, 2))
  if (failed.length > 0) {
    process.exitCode = 1
  }
}

function sanitizeSecretText(value: string): string {
  return value.replace(/sk-[A-Za-z0-9_-]+/g, 'sk-***')
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`缺少环境变量 ${name}`)
  return value
}

function countTokensRunMode(): 'auto' | 'required' | 'disabled' {
  const value = process.env.JUHE_REAL_RUN_COUNT_TOKENS?.trim().toLowerCase()
  if (!value || value === 'auto') return 'auto'
  if (['1', 'true', 'yes', 'on', 'required'].includes(value)) return 'required'
  return 'disabled'
}

function truthyEnv(name: string, defaultValue = false): boolean {
  const value = process.env[name]
  if (value === undefined) return defaultValue
  return ['1', 'true', 'yes', 'on'].includes(value.trim().toLowerCase())
}

function positiveIntEnv(name: string): number | undefined {
  const value = Number(process.env[name])
  return Number.isInteger(value) && value > 0 ? value : undefined
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
