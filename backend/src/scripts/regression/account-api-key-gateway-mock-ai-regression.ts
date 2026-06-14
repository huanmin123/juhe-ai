import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
}

type ApiKeyStrategy = 'round_robin' | 'weighted_round_robin'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-gateway-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
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
const mockHits: MockUpstreamHit[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const roundRobinApiKey = createGatewayApiKeyScenario({
      name: '单账户多 Key 网关轮询',
      upstreamBaseUrl,
      apiKeys: ['sk-gateway-rr-a', 'sk-gateway-rr-b', 'sk-gateway-rr-c'],
      strategy: 'round_robin'
    })
    await postChatCompletions(gatewayBaseUrl, roundRobinApiKey, 5)
    assert.deepEqual(
      lastAuthorizations(5),
      [
        'Bearer sk-gateway-rr-a',
        'Bearer sk-gateway-rr-b',
        'Bearer sk-gateway-rr-c',
        'Bearer sk-gateway-rr-a',
        'Bearer sk-gateway-rr-b'
      ],
      '网关真实请求应在单个账户内按 API Key 轮询转发'
    )

    const weightedApiKey = createGatewayApiKeyScenario({
      name: '单账户多 Key 网关权重',
      upstreamBaseUrl,
      apiKeys: ['sk-gateway-weight-a', 'sk-gateway-weight-b'],
      strategy: 'weighted_round_robin',
      weights: [3, 1]
    })
    await postChatCompletions(gatewayBaseUrl, weightedApiKey, 8)
    const weightedAuthorizations = lastAuthorizations(8)
    assert.equal(
      weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-a').length,
      6,
      '权重 3 的 API Key 在 8 次真实网关请求中应命中 6 次'
    )
    assert.equal(
      weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-b').length,
      2,
      '权重 1 的 API Key 在 8 次真实网关请求中应命中 2 次'
    )
    assert.equal(
      repositories.listAccounts(access, { page: 1, pageSize: 20 }).filter((account) => account.name.includes('单账户多 Key 网关')).length,
      2,
      '两个策略场景各自只应创建一个账户，不应按 API Key 展开账户'
    )

    console.log(JSON.stringify({
      message: '单账户多 API Key 网关 mock AI 回归通过',
      roundRobin: lastAuthorizations(13).slice(0, 5),
      weighted: weightedAuthorizations
    }, null, 2))
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createGatewayApiKeyScenario(input: {
  apiKeys: string[]
  name: string
  strategy: ApiKeyStrategy
  upstreamBaseUrl: string
  weights?: number[]
}): string {
  const group = repositories.createGroup({
    name: `${input.name} 分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: `${input.name} 账户`,
    type: 'api_key',
    credentials: {
      api_key: input.apiKeys[0],
      api_keys: input.apiKeys,
      api_key_strategy: input.strategy,
      api_key_weights: input.weights,
      base_url: input.upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert.deepEqual(account.credentials.api_keys, input.apiKeys, `${input.name} 应把多个 API Key 保存在同一个账户`)
  const apiKey = repositories.createApiKeyRecord({
    name: `${input.name} 网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${input.name} 未返回网关 API Key 明文`)
  gatewayCache.clearGatewayRuntimeCache()
  return apiKey.key
}

async function postChatCompletions(gatewayBaseUrl: string, apiKey: string, count: number): Promise<void> {
  for (let index = 0; index < count; index += 1) {
    const response = await fetch(`${gatewayBaseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{ role: 'user', content: `mock gateway api key rotation ${index + 1}` }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `网关请求应成功，实际 HTTP ${response.status}: ${text}`)
  }
}

function lastAuthorizations(count: number): string[] {
  return mockHits.slice(-count).map((hit) => hit.authorization)
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url ?? ''
    if (req.method !== 'POST' || requestPath !== '/v1/chat/completions') {
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { message: 'mock upstream path not found' } }))
      return
    }
    mockHits.push({
      authorization: String(req.headers.authorization ?? ''),
      path: requestPath
    })
    req.resume()
    req.on('end', () => {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-account-api-key-gateway-mock-ai',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'mock api key gateway ok' },
            finish_reason: 'stop'
          }
        ],
        usage: {
          input_tokens: 3,
          output_tokens: 4,
          total_tokens: 7
        }
      }))
    })
  })
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
