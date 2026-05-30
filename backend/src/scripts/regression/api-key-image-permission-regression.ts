import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-image-permission-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'api-key-image-permission.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-image-permission-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
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
  jsonParserModule
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/gateway/openai-gateway-request-body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/openai-gateway-upstream.js'),
  import('../../modules/gateway/openai-gateway-request-body.js'),
  import('../../modules/gateway/openai-gateway-json-parser.js')
])

interface SeededGateway {
  apiKey: string
  ownerSystemAccountId: string
  upstreamKey: string
}

interface MockUpstreamRequest {
  path: string
  accountKey: string
  model?: string
}

interface MockUpstreamState {
  requests: MockUpstreamRequest[]
}

let gatewayServer: http.Server | undefined
let upstreamServer: http.Server | undefined
const upstreamState: MockUpstreamState = { requests: [] }

try {
  upstreamServer = createMockOpenAIUpstream(upstreamState)
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
  const seeded = seedGateway(upstreamBaseUrl)

  gatewayServer = createGatewayServer()
  await listen(gatewayServer)
  const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  console.error('debug: before denied')
  const denied = await requestImageGeneration(baseUrl, seeded.apiKey, 'disabled-image')
  console.error('debug: after denied', denied.status)
  assert.equal(denied.status, 403, `禁用图像生成时应返回 403，实际 ${denied.status}: ${denied.text}`)
  assert.match(denied.text, /当前用户图像生成被禁用了，请联系管理员开启/, '禁用图像生成错误文案应提示联系管理员开启')
  assert.match(denied.text, /image_generation_disabled/, '禁用图像生成应返回稳定错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/images/generations'), 0, '禁用图像生成时不应请求上游图片接口')

  console.error('debug: before large')
  const deniedLargeTool = await requestLargeResponsesImageTool(baseUrl, seeded.apiKey)
  console.error('debug: after large', deniedLargeTool.status)
  assert.equal(deniedLargeTool.status, 403, `大 JSON 后段 image_generation 工具也应被图像权限拦截，实际 ${deniedLargeTool.status}: ${deniedLargeTool.text}`)
  assert.match(deniedLargeTool.text, /image_generation_disabled/, '大 JSON 后段 image_generation 工具应返回稳定错误码')
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/responses'), 0, '禁用图像生成时大 JSON 后段工具请求不应进入上游')

  console.error('debug: before text')
  const text = await requestChatCompletion(baseUrl, seeded.apiKey)
  console.error('debug: after text', text.status)
  assert.equal(text.status, 200, `图像权限禁用不应影响文本请求，实际 ${text.status}: ${text.text}`)
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/chat/completions'), 1, '文本请求应正常命中上游')

  const updated = repositories.updateSystemAccount(seeded.ownerSystemAccountId, { imageGenerationEnabled: true })
  assert.equal(updated?.imageGenerationEnabled, true, '开启系统账户图像生成权限后应返回 true')

  console.error('debug: before allowed')
  const allowed = await requestImageGeneration(baseUrl, seeded.apiKey, 'enabled-image')
  console.error('debug: after allowed', allowed.status)
  assert.equal(allowed.status, 200, `开启图像生成后同一个 API Key 应通过，实际 ${allowed.status}: ${allowed.text}`)
  assert.equal(upstreamHitCount(upstreamState, seeded.upstreamKey, '/v1/images/generations'), 1, '开启图像生成后应命中上游图片接口')

  console.log('API Key 图像生成权限回归通过：默认禁用不上游，开启后同一 Key 立即放行')
} finally {
  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  upstreamModule.closeGatewayUpstreamAgentsForTest()
  await jsonParserModule.stopGatewayJsonParseWorker()
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  try {
    databaseModule.getDatabase().close()
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
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '图像权限回归上游账号',
    type: 'api_key',
    credentials: {
      api_key: upstreamKey,
      base_url: upstreamBaseUrl
    },
    status: 'active',
    schedulable: true,
    passthroughEnabled: true
  }, access)
  assert(account.boundGroupId, '新建账户应绑定默认分组')

  const apiKey = repositories.createApiKeyRecord({
    name: '图像权限回归 API Key',
    groupId: account.boundGroupId,
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    ownerSystemAccountId: owner.id,
    upstreamKey
  }
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
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
        model: typeof body.model === 'string' ? body.model : undefined
      }
      state.requests.push(requestRecord)
      console.error('debug upstream hit', requestRecord.path, requestRecord.model)

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
      input: 'x'.repeat(requestBodyModule.gatewayJsonBodyLargeWarningBytes),
      tools: [{ type: 'image_generation' }]
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
