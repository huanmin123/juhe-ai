import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { GatewayRuntimeRequest } from '../../modules/gateway/request/pre-auth.js'
import { logger } from '../../shared/logger.js'
import { gatewayErrorPayload } from '../../modules/gateway/response/responses.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-auth-preflight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-auth-preflight.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-auth-preflight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
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
  gatewayBodyMiddleware,
  gatewayJsonParser,
  gatewayRequestBody,
  usageRecordQueue,
  auditLogQueue,
  gatewayCache,
  backgroundIpc
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/request/pre-auth.js'),
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../modules/gateway/request/json-parser.js'),
  import('../../modules/gateway/request/body.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/background/background-ipc.js')
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
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
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
    const authRejectBody = JSON.stringify({ model: 'gpt-5.4', input: 'x'.repeat(1024) })

    const missingBearer = await postJson(`${baseUrl}/v1/responses`, authRejectBody, undefined, 'missingBearer')
    assert.equal(missingBearer.status, 401, '缺少 Bearer 应在读取 body 前返回 401')
    assert.equal(rawBodyMiddlewareHitCount, 0, '缺少 Bearer 不应进入 raw body 读取链路')
    assert.equal(fakeChild.sentOperationCount, 0, '缺少 Bearer 不应请求 DB service')

    const invalidKey = await postJson(`${baseUrl}/v1/responses`, authRejectBody, 'sk-invalid', 'invalidKey')
    assert.equal(invalidKey.status, 401, '无效 API Key 应在读取 body 前返回 401')
    assert.equal(rawBodyMiddlewareHitCount, 0, '无效 API Key 不应进入 raw body 读取链路')
    assert.equal(fakeChild.sentOperationCount, 1, '无效 API Key 只需要一次运行配置读取')

    const disabledImage = await postJson(`${baseUrl}/v1/images/generations`, authRejectBody, apiKey.key, 'disabledImage')
    assert.equal(disabledImage.status, 403, '未开启图像生成权限的 API Key 应在读取 body 前返回 403')
    assert.match(disabledImage.text, /当前用户图像生成被禁用了，请联系管理员开启/, '图像生成权限禁用应返回中文错误')
    assert.equal(rawBodyMiddlewareHitCount, 0, '路径可识别的图像请求被禁用时不应读取 raw body')
    assert.equal(fakeChild.sentOperationCount, 2, '图像权限早拒绝只需要复用一次运行配置读取')

    const valid = await postJson(`${baseUrl}/v1/responses`, body, apiKey.key, 'valid')
    assert.equal(valid.status, 200, `合法 API Key 应继续进入网关 body 读取链路：${valid.text}`)
    assert.equal(rawBodyMiddlewareHitCount, 1, '合法请求应读取一次 raw body')
    assert.deepEqual(JSON.parse(valid.text), {
      apiKeyId: apiKey.id,
      rawBodyBytes: Buffer.byteLength(body)
    })

    repositories.updateSettings({ gatewayTextRawBodyLimitMegabytes: 2 })
    gatewayCache.clearGatewayRuntimeCacheLocal()
    const configuredTextLimitBytes = gatewayRequestBody.gatewayTextRawBodyLimitBytes(2)
    const oversizeBody = JSON.stringify({ model: 'gpt-5.4', input: 'x'.repeat(configuredTextLimitBytes) })
    const usageQueueLengthBeforeOversize = backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.queueLength
    const oversize = await postJson(`${baseUrl}/v1/responses`, oversizeBody, apiKey.key, 'oversize')
    assert.equal(oversize.status, 413, '超过系统设置里的文本请求体上限应在进入业务解析前返回 413')
    assert.equal(rawBodyMiddlewareHitCount, 2, '超过动态文本上限但未超过入口上限的合法请求会进入 raw body 限额判定')
    assert.equal(
      backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.queueLength,
      usageQueueLengthBeforeOversize + 1,
      '合法 API Key 的请求体 413 拒绝必须投递一条失败用量到 ingest-worker 队列'
    )

    const largeImageBody = JSON.stringify({ model: 'gpt-image-1', input: 'x'.repeat(configuredTextLimitBytes) })
    const largeImage = await postJson(`${baseUrl}/v1/responses`, largeImageBody, apiKey.key, 'largeImage')
    assert.equal(largeImage.status, 200, '图像模型请求超过动态文本上限但未超过图像上限时应继续进入业务链路')
    assert.deepEqual(JSON.parse(largeImage.text), {
      apiKeyId: apiKey.id,
      rawBodyBytes: Buffer.byteLength(largeImageBody)
    })
    assert.equal(rawBodyMiddlewareHitCount, 3, '图像大请求应通过 raw body 限额判定并进入后续链路')
  } finally {
    await close(server)
  }

  console.log('网关认证预解析回归通过：无效请求不读取大 body，合法请求复用 runtime 后继续处理')
} finally {
  await gatewayJsonParser.stopGatewayJsonParseWorker()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGatewayRuntime(): { id: string; key: string } {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '认证预解析分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: '认证预解析账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-auth-preflight-account',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true,
    groupId: group.id
  }, access)
  assert(account.boundGroupId, '认证预解析账户应绑定默认分组')
  const boundGroupId = account.boundGroupId
  const apiKey = repositories.createApiKeyRecord({
    name: '认证预解析 API Key',
    groupBindings: [{ groupId: boundGroupId, priority: 1, status: 'active' }],
  }, access)
  return { id: apiKey.id, key: apiKey.key }
}

async function listen(): Promise<Server> {
  const app = express()
  app.use(requestContext.requestContextMiddleware)
  app.use(gatewayRequest.preResolveGatewayRuntime)
  app.use(gatewayRoutes.handleGatewayDbServiceUnavailable)
  app.use(express.raw({ type: () => true, limit: gatewayRequestBody.gatewayRawBodyHardLimit }))
  app.use(handleRawBodyErrorForTest)
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

function handleRawBodyErrorForTest(
  error: Error & { status?: number; statusCode?: number; type?: string; received?: number; length?: number; limit?: number },
  req: Request,
  res: Response,
  next: NextFunction
): void {
  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400
  if (statusCode >= 400 && statusCode < 600) {
    const message = statusCode === 413 ? '请求体过大' : '请求体无效'
    const responsePayload = gatewayErrorPayload(message, statusCode === 413 ? 'request_too_large' : 'invalid_request_error')
    gatewayBodyMiddleware.recordGatewayBodyRejection(req as never, {
      statusCode,
      responsePayload,
      rawBodyBytes: Number(error.received ?? error.length ?? error.limit ?? 0),
      reason: 'gateway_body_parser',
      errorCode: error.type,
      errorMessage: message
    })
    res.status(statusCode).json(responsePayload)
    return
  }
  next(error)
}

async function postJson(url: string, body: string, token?: string, label = 'request'): Promise<{ status: number; text: string }> {
  const headers: Record<string, string> = {
    'content-type': 'application/json'
  }
  if (token) {
    headers.authorization = `Bearer ${token}`
  }
  let response: Awaited<ReturnType<typeof fetch>>
  try {
    response = await fetch(url, {
      method: 'POST',
      headers,
      body
    })
  } catch (error) {
    throw new Error(`${label} fetch failed: ${error instanceof Error ? error.message : String(error)}`)
  }
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
