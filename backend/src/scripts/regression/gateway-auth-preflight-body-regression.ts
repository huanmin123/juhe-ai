import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { GatewayRuntimeRequest } from '../../modules/gateway/openai-gateway-request.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-auth-preflight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-auth-preflight.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'gateway-auth-preflight-records.sqlite3')
runtimeConfig.secret = 'gateway-auth-preflight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  dbServiceHandlers,
  dbServiceIpc,
  requestContext,
  gatewayRequest,
  gatewayRoutes,
  gatewayBodyMiddleware
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/openai-gateway-request.js'),
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/gateway/openai-gateway-request-body-middleware.js')
])

class FakeDbServiceChild extends EventEmitter {
  readonly pid = 414141
  readonly connected = true
  sentOperationCount = 0

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    void this.handleMessage(message, callback)
    return true
  }

  private async handleMessage(message: unknown, callback?: (error?: Error | null) => void): Promise<void> {
    if (!isDbServiceRequest(message)) {
      callback?.()
      return
    }
    this.sentOperationCount += 1
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

let rawBodyMiddlewareHitCount = 0

try {
  const apiKey = seedGatewayRuntime()
  const fakeChild = new FakeDbServiceChild()
  runtimeConfig.processRole = 'server'
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  const server = await listen()
  try {
    const baseUrl = `http://127.0.0.1:${addressPort(server)}`
    const body = JSON.stringify({ model: 'gpt-5.4', input: 'x'.repeat(1024 * 1024) })

    const missingBearer = await postJson(`${baseUrl}/v1/responses`, body)
    assert.equal(missingBearer.status, 401, '缺少 Bearer 应在读取 body 前返回 401')
    assert.equal(rawBodyMiddlewareHitCount, 0, '缺少 Bearer 不应进入 raw body 读取链路')
    assert.equal(fakeChild.sentOperationCount, 0, '缺少 Bearer 不应请求 DB service')

    const invalidKey = await postJson(`${baseUrl}/v1/responses`, body, 'sk-invalid')
    assert.equal(invalidKey.status, 401, '无效 API Key 应在读取 body 前返回 401')
    assert.equal(rawBodyMiddlewareHitCount, 0, '无效 API Key 不应进入 raw body 读取链路')
    assert.equal(fakeChild.sentOperationCount, 1, '无效 API Key 只需要一次运行配置读取')

    const valid = await postJson(`${baseUrl}/v1/responses`, body, apiKey.key)
    assert.equal(valid.status, 200, '合法 API Key 应继续进入网关 body 读取链路')
    assert.equal(rawBodyMiddlewareHitCount, 1, '合法请求应读取一次 raw body')
    assert.deepEqual(JSON.parse(valid.text), {
      apiKeyId: apiKey.id,
      rawBodyBytes: Buffer.byteLength(body)
    })
  } finally {
    await close(server)
  }

  console.log('网关认证预解析回归通过：无效请求不读取大 body，合法请求复用 runtime 后继续处理')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGatewayRuntime(): { id: string; key: string } {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '认证预解析账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-auth-preflight-account',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKey = repositories.createApiKeyRecord({
    name: '认证预解析 API Key',
    groupId: account.boundGroupId
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  return { id: apiKey.id, key: apiKey.key }
}

async function listen(): Promise<Server> {
  const app = express()
  app.use(requestContext.requestContextMiddleware)
  app.use(gatewayRequest.preResolveOpenAIGatewayRuntime)
  app.use(gatewayRoutes.handleGatewayDbServiceUnavailable)
  app.use(express.raw({ type: () => true, limit: '64mb' }))
  app.use((_req, _res, next) => {
    rawBodyMiddlewareHitCount += 1
    next()
  })
  app.use(gatewayBodyMiddleware.captureGatewayRawBody)
  app.use((req, res) => {
    const gatewayReq = req as GatewayRuntimeRequest & { rawBody?: Buffer }
    res.json({
      apiKeyId: gatewayReq.gatewayRuntime?.apiKey?.id,
      rawBodyBytes: gatewayReq.rawBody?.byteLength ?? 0
    })
  })

  const server = createServer(app)
  await new Promise<void>((resolveListen) => {
    server.listen(0, '127.0.0.1', resolveListen)
  })
  return server
}

async function postJson(url: string, body: string, token?: string): Promise<{ status: number; text: string }> {
  const headers: Record<string, string> = {
    'content-type': 'application/json'
  }
  if (token) {
    headers.authorization = `Bearer ${token}`
  }
  const response = await fetch(url, {
    method: 'POST',
    headers,
    body
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function addressPort(server: Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', '测试服务应监听在本地端口')
  return address.port
}

async function close(server: Server): Promise<void> {
  await new Promise<void>((resolveClose, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolveClose()
    })
  })
}

function isDbServiceRequest(value: unknown): value is { type: 'db_service_request'; requestId: string; operation: Parameters<typeof dbServiceHandlers.handleDbServiceOperation>[0] } {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'db_service_request'
    && typeof (value as Record<string, unknown>).requestId === 'string'
    && typeof (value as Record<string, unknown>).operation === 'object'
    && (value as Record<string, unknown>).operation !== null
}
