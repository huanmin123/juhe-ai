import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-group-route-cache-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-group-route-cache-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

interface MockUpstreamRequest {
  path: string
  accountKey: string
}

interface SeededRoute {
  apiKeyId: string
  apiKey: string
  systemAccountId: string
  fallbackAccountAuthorizationId: string
  fallbackUpstreamKey: string
}

interface SeededRoundRobinRoute {
  apiKey: string
  firstUpstreamKey: string
  secondUpstreamKey: string
}

const upstreamRequests: MockUpstreamRequest[] = []
let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined
let closeGatewayUpstreamAgentsForTest: (() => void) | undefined

try {
  upstreamServer = createMockOpenAIUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
  const seededRoute = seedRoute(upstreamBaseUrl)
  const roundRobinRoute = seedRoundRobinRoute(upstreamBaseUrl)

  const [
    { openAIGatewayRouter },
    { captureGatewayRawBody },
    { requestContextMiddleware },
    dbServiceHandlers,
    dbServiceIpc,
    gatewayCache,
    apiKeyQuotaService,
    authorizationQuotaService,
    quotaSnapshot,
    upstreamModule
  ] = await Promise.all([
    import('../../modules/gateway/openai-gateway.routes.js'),
    import('../../modules/gateway/openai-gateway-request-body-middleware.js'),
    import('../../shared/request-context.js'),
    import('../../modules/db-service/db-service-handlers.js'),
    import('../../modules/db-service/db-service-ipc.js'),
    import('../../modules/gateway/gateway-runtime-cache.service.js'),
    import('../../modules/gateway/api-key-quota.service.js'),
    import('../../modules/gateway/authorization-quota.service.js'),
    import('../../modules/gateway/gateway-quota-snapshot-cache.service.js'),
    import('../../modules/gateway/openai-gateway-upstream.js')
  ])
  closeGatewayUpstreamAgentsForTest = upstreamModule.closeGatewayUpstreamAgentsForTest

  class FakeDbServiceChild extends EventEmitter {
    readonly pid = 434343
    readonly connected = true
    readonly operationCounts = new Map<string, number>()

    send(message: unknown, callback?: (error?: Error | null) => void): boolean {
      void this.handleMessage(message, callback)
      return true
    }

    operationCount(type: string): number {
      return this.operationCounts.get(type) ?? 0
    }

    totalOperationCount(): number {
      let total = 0
      for (const count of this.operationCounts.values()) {
        total += count
      }
      return total
    }

    private async handleMessage(message: unknown, callback?: (error?: Error | null) => void): Promise<void> {
      if (!isDbServiceRequest(message)) {
        callback?.()
        return
      }
      this.operationCounts.set(message.operation.type, this.operationCount(message.operation.type) + 1)
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

  const fakeChild = new FakeDbServiceChild()
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  gatewayCache.clearGatewayRuntimeCacheLocal()
  apiKeyQuotaService.clearApiKeyQuotaCache()
  authorizationQuotaService.clearAuthorizationQuotaCache()
  quotaSnapshot.replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [{
      systemAccountId: seededRoute.systemAccountId,
      scopeType: 'api_key',
      scopeId: seededRoute.apiKeyId,
      hourlyWindowHours: 2,
      costs: {
        hourly: 0,
        daily: 0,
        weekly: 0,
        monthly: 0,
        total: 0
      }
    }],
    authorizationEntries: [{
      scopeType: 'account_authorization',
      authorizationId: seededRoute.fallbackAccountAuthorizationId,
      decision: { allowed: true }
    }]
  })

  gatewayServer = createGatewayServer(openAIGatewayRouter, captureGatewayRawBody, requestContextMiddleware)
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  const firstResponse = await requestChatCompletion(gatewayBaseUrl, seededRoute.apiKey, 'trace-route-cache-first')
  assert.equal(firstResponse.status, 200, `首次请求应完成主分组到后备分组的预派发切换，实际 ${firstResponse.status}: ${firstResponse.text}`)
  assert.equal(upstreamRequests.at(-1)?.accountKey, seededRoute.fallbackUpstreamKey, '首次请求应命中后备授权账号')
  assert.equal(fakeChild.operationCount('read_gateway_runtime'), 1, '首次请求只应读取一次 API Key 运行时')
  assert.equal(fakeChild.operationCount('resolve_group_usage_access'), 1, '首次 fallback 只应读取一次后备分组授权元数据')
  assert.equal(fakeChild.operationCount('list_openai_accounts_for_group_result'), 1, '首次 fallback 只应读取一次后备分组账号列表及计划元信息')
  assert.equal(fakeChild.operationCount('list_openai_accounts_for_group'), 0, '首次 fallback 必须读取带计划元信息的分组账号列表')
  assert.equal(fakeChild.operationCount('check_api_key_quota'), 0, 'server 请求链路不应通过 DB service 主动查询 API Key 统计额度窗口')
  assert.equal(fakeChild.operationCount('check_authorization_quota'), 0, 'server 请求链路不应通过 DB service 主动查询单条授权额度')
  assert.equal(fakeChild.operationCount('check_authorization_quota_batch'), 0, 'server 请求链路不应通过 DB service 主动查询批量授权额度')

  const operationsAfterFirstRequest = fakeChild.totalOperationCount()
  const secondUpstreamStart = upstreamRequests.length
  const secondResponse = await requestChatCompletion(gatewayBaseUrl, seededRoute.apiKey, 'trace-route-cache-second')
  assert.equal(secondResponse.status, 200, `第二次同组合请求应继续成功，实际 ${secondResponse.status}: ${secondResponse.text}`)
  const secondUpstreamRequests = upstreamRequests.slice(secondUpstreamStart)
  assert.equal(secondUpstreamRequests.length, 1, '第二次同组合请求只应派发一次上游')
  assert.equal(secondUpstreamRequests[0]?.accountKey, seededRoute.fallbackUpstreamKey, '第二次同组合请求应继续命中后备授权账号')
  assert.equal(fakeChild.totalOperationCount(), operationsAfterFirstRequest, '同一 API Key/分组/授权组合第二次请求必须完全命中 server 本地缓存和被动快照，不应再请求 DB service')

  const operationsBeforeDynamicRoute = fakeChild.operationCount('read_gateway_runtime')
  const dynamicUpstreamStart = upstreamRequests.length
  const roundRobinFirstResponse = await requestChatCompletion(gatewayBaseUrl, roundRobinRoute.apiKey, 'trace-route-cache-round-robin-first')
  assert.equal(roundRobinFirstResponse.status, 200, `轮询策略首次请求应成功，实际 ${roundRobinFirstResponse.status}: ${roundRobinFirstResponse.text}`)
  const roundRobinSecondResponse = await requestChatCompletion(gatewayBaseUrl, roundRobinRoute.apiKey, 'trace-route-cache-round-robin-second')
  assert.equal(roundRobinSecondResponse.status, 200, `轮询策略第二次请求应成功，实际 ${roundRobinSecondResponse.status}: ${roundRobinSecondResponse.text}`)
  const dynamicUpstreamRequests = upstreamRequests.slice(dynamicUpstreamStart)
  assert.equal(dynamicUpstreamRequests.length, 2, '轮询策略两次请求应各派发一次上游')
  assert.deepEqual(
    dynamicUpstreamRequests.map((request) => request.accountKey),
    [roundRobinRoute.firstUpstreamKey, roundRobinRoute.secondUpstreamKey],
    '轮询策略不能缓存首次命中的分组，第二次请求应重新选择下一个号池'
  )
  assert.equal(
    fakeChild.operationCount('read_gateway_runtime'),
    operationsBeforeDynamicRoute + 2,
    '轮询策略每次请求都应重新读取运行时以计算候选分组起点'
  )

  console.log('API Key 多分组路由缓存回归通过：主备同组合第二次请求不再读取运行时；轮询策略不会缓存最终命中分组，连续请求可重新选组')
} finally {
  closeGatewayUpstreamAgentsForTest?.()
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedRoundRobinRoute(upstreamBaseUrl: string): SeededRoundRobinRoute {
  const owner = repositories.createSystemAccount({
    username: 'route_cache_round_robin_owner',
    displayName: '路由缓存轮询用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const firstGroup = repositories.createGroup({
    name: '路由缓存轮询 A 号池',
    providerCode: 'openai',
    groupType: 'personal'
  }, access)
  const secondGroup = repositories.createGroup({
    name: '路由缓存轮询 B 号池',
    providerCode: 'openai',
    groupType: 'personal'
  }, access)
  const firstUpstreamKey = 'sk-route-cache-round-robin-a'
  const secondUpstreamKey = 'sk-route-cache-round-robin-b'
  repositories.createAccount({
    providerCode: 'openai',
    name: '路由缓存轮询 A 账号',
    type: 'api_key',
    credentials: {
      api_key: firstUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: firstGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  repositories.createAccount({
    providerCode: 'openai',
    name: '路由缓存轮询 B 账号',
    type: 'api_key',
    credentials: {
      api_key: secondUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: secondGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '路由缓存轮询 API Key',
    groupRouteStrategy: 'round_robin',
    groupBindings: [
      { groupId: firstGroup.id, priority: 1, status: 'active' },
      { groupId: secondGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  return {
    apiKey: apiKey.key,
    firstUpstreamKey,
    secondUpstreamKey
  }
}

function seedRoute(upstreamBaseUrl: string): SeededRoute {
  const owner = repositories.createSystemAccount({
    username: 'route_cache_owner',
    displayName: '路由缓存资源方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'route_cache_grantee',
    displayName: '路由缓存使用方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '路由缓存主 OAuth 号池',
    providerCode: 'openai',
    groupType: 'personal'
  }, granteeAccess)
  const fallbackGroup = repositories.createGroup({
    name: '路由缓存后备授权号池',
    providerCode: 'openai',
    groupType: 'personal'
  }, granteeAccess)
  repositories.createAccount({
    providerCode: 'openai',
    name: '路由缓存主 OAuth 账号',
    type: 'oauth',
    credentials: {
      access_token: 'access-route-cache-primary',
      refresh_token: 'refresh-route-cache-primary',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true
  }, granteeAccess)
  const fallbackUpstreamKey = 'sk-route-cache-fallback'
  const ownerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '路由缓存后备授权账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    status: 'active',
    schedulable: true,
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: fallbackGroup.id,
    remark: '路由缓存授权账号',
    limits: {
      total: { enabled: true, limit: 1000 }
    }
  }, ownerAccess)
  const authorizationRow = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(ownerAccount.id, grantee.id) as unknown as { id?: string } | undefined
  assert(authorizationRow?.id, '路由缓存回归需要授权记录 ID')
  const apiKey = repositories.createApiKeyRecord({
    name: '路由缓存 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ],
    quotaLimits: {
      hourly: { enabled: true, hours: 2, limit: 1000 },
      daily: { enabled: true, limit: 1000 },
      weekly: { enabled: true, limit: 1000 },
      monthly: { enabled: true, limit: 1000 },
      total: { enabled: true, limit: 1000 }
    }
  }, granteeAccess)
  return {
    apiKeyId: apiKey.id,
    apiKey: apiKey.key,
    systemAccountId: grantee.id,
    fallbackAccountAuthorizationId: authorizationRow.id,
    fallbackUpstreamKey
  }
}

function createGatewayServer(
  openAIGatewayRouter: express.Router,
  captureGatewayRawBody: express.RequestHandler,
  requestContextMiddleware: express.RequestHandler
): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    req.on('end', () => {
      upstreamRequests.push({
        path: String(req.url ?? '').split('?')[0] || '/',
        accountKey: bearerToken(req.headers.authorization)
      })
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-route-cache',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'route cache ok' },
            finish_reason: 'stop'
          }
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
    req.resume()
  })
}

async function requestChatCompletion(baseUrl: string, apiKey: string, traceId: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-trace-id': traceId
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'route cache' }]
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function isDbServiceRequest(value: unknown): value is { type: 'db_service_request'; requestId: string; operation: Parameters<typeof import('../../modules/db-service/db-service-handlers.js')['handleDbServiceOperation']>[0] } {
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
