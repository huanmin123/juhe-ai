import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ANTHROPIC_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-image-permission-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'api-key-image-permission.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-image-permission-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue,
  upstreamModule,
  requestBodyModule,
  jsonParserModule,
  dbServiceHandlers,
  dbServiceIpc
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/upstream/request.js'),
  import('../../modules/gateway/request/body.js'),
  import('../../modules/gateway/request/json-parser.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js')
])

interface SeededGateway {
  apiKey: string
  ownerSystemAccountId: string
  upstreamKey: string
  normalRouteApiKey: string
  normalRouteGptUpstreamKey: string
}

interface MockUpstreamRequest {
  path: string
  accountKey: string
  model?: string
  body: Record<string, unknown>
}

interface MockUpstreamState {
  requests: MockUpstreamRequest[]
}

let gatewayServer: http.Server | undefined
let upstreamServer: http.Server | undefined
const upstreamState: MockUpstreamState = { requests: [] }

class FakeDbServiceChild extends EventTarget {
  readonly pid = 929292
  readonly connected = true
  private listeners = new Map<string, Set<(message?: unknown) => void>>()

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    void this.handleMessage(message, callback)
    return true
  }

  on(event: string, listener: (message?: unknown) => void): this {
    const listeners = this.listeners.get(event) ?? new Set()
    listeners.add(listener)
    this.listeners.set(event, listeners)
    return this
  }

  once(event: string, listener: (message?: unknown) => void): this {
    const onceListener = (message?: unknown) => {
      this.off(event, onceListener)
      listener(message)
    }
    return this.on(event, onceListener)
  }

  off(event: string, listener: (message?: unknown) => void): this {
    this.listeners.get(event)?.delete(listener)
    return this
  }

  removeAllListeners(event?: string): this {
    if (event) {
      this.listeners.delete(event)
    } else {
      this.listeners.clear()
    }
    return this
  }

  emit(event: string, message?: unknown): boolean {
    const listeners = this.listeners.get(event)
    if (!listeners?.size) return false
    for (const listener of [...listeners]) {
      listener(message)
    }
    return true
  }

  private async handleMessage(message: unknown, callback?: (error?: Error | null) => void): Promise<void> {
    if (!isDbServiceRequest(message)) {
      callback?.()
      return
    }
    const previousProcessRole = runtimeConfig.processRole
    try {
      runtimeConfig.processRole = 'db-service'
      const result = await dbServiceHandlers.handleDbServiceOperation(message.operation)
      queueMicrotask(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: message.requestId,
          ok: true,
          result
        })
      })
      callback?.()
    } catch (error) {
      queueMicrotask(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: message.requestId,
          ok: false,
          errorMessage: error instanceof Error ? error.message : String(error)
        })
      })
      callback?.()
    } finally {
      runtimeConfig.processRole = previousProcessRole
    }
  }
}

try {
  upstreamServer = createMockOpenAIUpstream(upstreamState)
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
  const seeded = seedGateway(upstreamBaseUrl)
  const fakeChild = new FakeDbServiceChild()
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  gatewayServer = createGatewayServer()
  await listen(gatewayServer)
  const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  const denied = await requestImageGeneration(baseUrl, seeded.apiKey, 'disabled-image')
  assert.equal(denied.status, 403, `禁用图像生成时应返回 403，实际 ${denied.status}: ${denied.text}`)
  assert.match(denied.text, /当前用户图像生成被禁用了，请联系管理员开启/, '禁用图像生成错误文案应提示联系管理员开启')
  assert.match(denied.text, /image_generation_disabled/, '禁用图像生成应返回稳定错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/images/generations'), 0, '禁用图像生成时不应请求上游图片接口')

  const downgradedLargeTool = await requestLargeResponsesImageTool(baseUrl, seeded.apiKey)
  assert.equal(downgradedLargeTool.status, 200, `大 JSON 后段 auto image_generation 工具应被移除后继续文本请求，实际 ${downgradedLargeTool.status}: ${downgradedLargeTool.text}`)
  const responsesHitsAfterDowngradedLargeTool = upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/responses')
  assert.equal(responsesHitsAfterDowngradedLargeTool, 1, '禁用图像生成时 auto 图像工具请求应按文本请求继续进入上游')
  assert.equal(hasImageGenerationTool(lastUpstreamRequest(upstreamState, seeded.upstreamKey, '/v1/responses')?.body), false, '禁用图像生成时转发给上游的 Responses body 不应保留 image_generation 工具')

  const oversizedTool = await requestOversizedResponsesImageTool(baseUrl, seeded.apiKey)
  assert.equal(oversizedTool.status, 413, `超文本上限的 auto image_generation 大 JSON 降级后应直接拒绝，实际 ${oversizedTool.status}: ${oversizedTool.text}`)
  assert.match(oversizedTool.text, /request_too_large/, '超文本上限的 auto image_generation 大 JSON 应返回请求体过大错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/responses'), responsesHitsAfterDowngradedLargeTool, '超文本上限的 auto image_generation 大 JSON 不应进入上游')

  const forcedTool = await requestForcedResponsesImageTool(baseUrl, seeded.apiKey)
  assert.equal(forcedTool.status, 403, `强制 image_generation 工具仍应被图像权限拦截，实际 ${forcedTool.status}: ${forcedTool.text}`)
  assert.match(forcedTool.text, /image_generation_disabled/, '强制 image_generation 工具应返回稳定错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/responses'), responsesHitsAfterDowngradedLargeTool, '禁用图像生成时强制工具请求不应进入上游')

  const normalRouteForcedTool = await requestForcedResponsesImageTool(baseUrl, seeded.normalRouteApiKey)
  assert.equal(normalRouteForcedTool.status, 403, `normal 路由切到 GPT 号池后强制 image_generation 工具仍应被图像权限拦截，实际 ${normalRouteForcedTool.status}: ${normalRouteForcedTool.text}`)
  assert.match(normalRouteForcedTool.text, /image_generation_disabled/, 'normal 路由后的强制 image_generation 工具应返回稳定错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.normalRouteGptUpstreamKey, '/v1/responses'), 0, 'normal 路由从 Anthropic 初始分组切到 GPT 后不应绕过权限请求 GPT 上游')

  const text = await requestChatCompletion(baseUrl, seeded.apiKey)
  assert.equal(text.status, 200, `图像权限禁用不应影响文本请求，实际 ${text.status}: ${text.text}`)
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/chat/completions'), 1, '文本请求应正常命中上游')

  const updated = repositories.updateSystemAccount(seeded.ownerSystemAccountId, { imageGenerationEnabled: true })
  assert.equal(updated?.imageGenerationEnabled, true, '开启系统账户图像生成权限后应返回 true')

  const allowed = await requestImageGeneration(baseUrl, seeded.apiKey, 'enabled-image')
  assert.equal(allowed.status, 200, `开启图像生成后同一个 API Key 应通过，实际 ${allowed.status}: ${allowed.text}`)
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/images/generations'), 1, '开启图像生成后应命中上游图片接口')

  const allowedResponsesTool = await requestLargeResponsesImageTool(baseUrl, seeded.apiKey)
  assert.equal(allowedResponsesTool.status, 200, `开启图像生成后 Responses image_generation 工具应原样通过，实际 ${allowedResponsesTool.status}: ${allowedResponsesTool.text}`)
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/responses'), responsesHitsAfterDowngradedLargeTool + 1, '开启图像生成后 Responses 工具请求应继续进入上游')
  assert.equal(hasImageGenerationTool(lastUpstreamRequest(upstreamState, seeded.upstreamKey, '/v1/responses')?.body), true, '开启图像生成后转发给上游的 Responses body 应保留 image_generation 工具')

  console.log('API Key 图像生成权限回归通过：默认禁用图片接口不上游，auto image_generation 工具降级为文本，normal 路由后强制工具仍拦截，开启后同一 Key 立即放行')
} finally {
  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  upstreamModule.closeGatewayUpstreamAgentsForTest()
  await jsonParserModule.stopGatewayJsonParseWorker()
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGateway(upstreamBaseUrl: string): SeededGateway {
  const owner = repositories.createSystemAccount({
    username: 'image_permission_owner',
    displayName: '图像权限回归用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  assert.equal(owner.imageGenerationEnabled, false, '新建系统账户应默认不支持图像生成')

  const access = { systemAccountId: owner.id, role: 'user' as const }
  const upstreamKey = 'sk-image-permission-upstream'
  const group = repositories.createGroup({
    name: '图像权限回归分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: '图像权限回归上游账号',
    type: 'api_key',
    credentials: {
      api_key: upstreamKey,
      base_url: upstreamBaseUrl
    },
    status: 'active',
    schedulable: true,
    groupId: group.id
  }, access)
  assert.equal(account.boundGroupId, group.id, '新建账户应绑定指定分组')
  const boundGroupId = account.boundGroupId

  const apiKey = repositories.createApiKeyRecord({
    name: '图像权限回归 API Key',
    groupBindings: [{ groupId: boundGroupId, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')

  const normalRouteAnthropicGroup = repositories.createGroup({
    name: '图像权限 normal 路由 Anthropic 初始分组',
    providerCode: ANTHROPIC_PROVIDER_CODE
  }, access)
  const normalRouteGptGroup = repositories.createGroup({
    name: '图像权限 normal 路由 GPT 目标分组',
    providerCode: 'gpt'
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    name: '图像权限 normal 路由 Anthropic 初始账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-image-permission-anthropic-upstream',
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    status: 'active',
    schedulable: true,
    groupId: normalRouteAnthropicGroup.id
  }, access)
  const normalRouteGptUpstreamKey = 'sk-image-permission-normal-route-gpt-upstream'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '图像权限 normal 路由 GPT 目标账号',
    type: 'api_key',
    credentials: {
      api_key: normalRouteGptUpstreamKey,
      base_url: upstreamBaseUrl
    },
    status: 'active',
    schedulable: true,
    groupId: normalRouteGptGroup.id,
    supportedModels: ['gpt-5.4']
  }, access)
  const normalRouteApiKey = repositories.createApiKeyRecord({
    name: '图像权限 normal 路由 API Key',
    routeMode: 'normal',
    groupBindings: [
      { groupId: normalRouteAnthropicGroup.id, priority: 1, status: 'active' },
      { groupId: normalRouteGptGroup.id, priority: 2, status: 'active' }
    ],
    status: 'active'
  }, access)
  assert(normalRouteApiKey.key, 'normal 路由 API Key 未返回明文密钥')

  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    ownerSystemAccountId: owner.id,
    upstreamKey,
    normalRouteApiKey: normalRouteApiKey.key,
    normalRouteGptUpstreamKey
  }
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: requestBodyModule.gatewayRawBodyHardLimit }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(state: MockUpstreamState): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const body = parseJsonObject(Buffer.concat(chunks).toString('utf8'))
      const requestRecord = {
        path: String(req.url ?? '').split('?')[0] || '/',
        accountKey: bearerToken(req.headers.authorization),
        model: typeof body.model === 'string' ? body.model : undefined,
        body
      }
      state.requests.push(requestRecord)

      if (requestRecord.path.endsWith('/images/generations')) {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          created: Math.floor(Date.now() / 1000),
          data: [{ b64_json: Buffer.from('tiny-image').toString('base64') }]
        }))
        return
      }

      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-image-permission',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'text ok' },
            finish_reason: 'stop'
          }
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
  })
}

async function requestImageGeneration(baseUrl: string, apiKey: string, prompt: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/images/generations`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-image-1',
      prompt
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestChatCompletion(baseUrl: string, apiKey: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.4',
      messages: [{ role: 'user', content: 'text should still work' }]
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestLargeResponsesImageTool(baseUrl: string, apiKey: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.4',
      input: 'x'.repeat(requestBodyModule.gatewayJsonBodyInlineParseMaxBytes + 32 * 1024),
      tools: [{ type: 'image_generation' }]
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestOversizedResponsesImageTool(baseUrl: string, apiKey: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.4',
      input: 'x'.repeat(requestBodyModule.gatewayTextRawBodyLimitBytes() + 32 * 1024),
      tools: [{ type: 'image_generation' }]
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestForcedResponsesImageTool(baseUrl: string, apiKey: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.4',
      input: 'force image generation',
      tools: [{ type: 'image_generation' }],
      tool_choice: { type: 'image_generation' }
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function upstreamHitCount(state: MockUpstreamState, accountKey: string, path: string): number {
  return state.requests.filter((request) => request.accountKey === accountKey && request.path === path).length
}

function lastUpstreamRequest(state: MockUpstreamState, accountKey: string, path: string): MockUpstreamRequest | undefined {
  return [...state.requests].reverse().find((request) => request.accountKey === accountKey && request.path === path)
}

function hasImageGenerationTool(body: Record<string, unknown> | undefined): boolean {
  return Array.isArray(body?.tools) && body.tools.some((tool) => {
    return typeof tool === 'object'
      && tool !== null
      && !Array.isArray(tool)
      && (tool as Record<string, unknown>).type === 'image_generation'
  })
}

function isDbServiceRequest(value: unknown): value is {
  type: 'db_service_request'
  requestId: string
  operation: Parameters<typeof dbServiceHandlers.handleDbServiceOperation>[0]
} {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'db_service_request'
    && typeof (value as Record<string, unknown>).requestId === 'string'
    && typeof (value as Record<string, unknown>).operation === 'object'
    && (value as Record<string, unknown>).operation !== null
}

function bearerToken(value: unknown): string {
  const text = Array.isArray(value) ? value[0] : String(value ?? '')
  return text.replace(/^Bearer\s+/i, '')
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}
