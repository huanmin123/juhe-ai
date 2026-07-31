import assert from 'node:assert/strict'
import { createServer, type Server } from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-models-dynamic-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-models-dynamic-http-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.raw({ type: () => true, limit: '1mb' }))
app.use(captureGatewayRawBody)
app.use(openAIGatewayRouter)

let server: Server | undefined
try {
  databaseModule.getBusinessDatabase()
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const gptGroup = repositories.createGroup({
    name: '动态模型 HTTP 回归 GPT 分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const geminiGroup = repositories.createGroup({
    name: '动态模型 HTTP 回归 Gemini 分组',
    providerCode: 'gemini',
    enabled: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '动态模型 HTTP 回归 API Key',
    status: 'active',
    groupBindings: [
      { groupId: gptGroup.id, priority: 1, status: 'active' },
      { groupId: geminiGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  assert(apiKey.key, '动态模型 HTTP 回归必须创建可用的网关 API Key')
  server = createServer(app)
  await listen(server)
  const baseUrl = `http://127.0.0.1:${address(server).port}`

  const [missingRootResponse, missingV1Response] = await Promise.all([
    fetch(`${baseUrl}/models`),
    fetch(`${baseUrl}/v1/models`)
  ])
  assert.equal(missingRootResponse.status, 401, '未认证 /models 不得返回模型目录')
  assert.equal(missingV1Response.status, 401, '未认证 /v1/models 不得返回模型目录')

  const xApiKeyHeaders = { 'x-api-key': apiKey.key }
  const bearerHeaders = { authorization: `Bearer ${apiKey.key}` }
  const [xApiKeyRootResponse, xApiKeyV1Response, bearerRootResponse, bearerV1Response] = await Promise.all([
    fetch(`${baseUrl}/models`, { headers: xApiKeyHeaders }),
    fetch(`${baseUrl}/v1/models`, { headers: xApiKeyHeaders }),
    fetch(`${baseUrl}/models`, { headers: bearerHeaders }),
    fetch(`${baseUrl}/v1/models`, { headers: bearerHeaders })
  ])
  assert.equal(xApiKeyRootResponse.status, 200, 'x-api-key 必须可认证 /models')
  assert.equal(xApiKeyV1Response.status, 200, 'x-api-key 必须可认证 /v1/models')
  assert.equal(bearerRootResponse.status, 200, 'Bearer 必须可认证 /models')
  assert.equal(bearerV1Response.status, xApiKeyV1Response.status, 'Bearer 与 x-api-key 必须为 /v1/models 返回同一认证状态')
  assert.equal(bearerV1Response.status, bearerRootResponse.status, 'Bearer 必须为 /models 与 /v1/models 返回同一认证状态')

  const xApiKeyRootBody = await xApiKeyRootResponse.json() as OpenAIModelsBody
  const xApiKeyV1Body = await xApiKeyV1Response.json() as OpenAIModelsBody
  const bearerRootBody = await bearerRootResponse.json() as OpenAIModelsBody
  const bearerV1Body = await bearerV1Response.json() as OpenAIModelsBody
  assertOpenAIModelsBody(xApiKeyRootBody, 'x-api-key /models')
  assertOpenAIModelsBody(xApiKeyV1Body, 'x-api-key /v1/models')
  assertOpenAIModelsBody(bearerRootBody, 'Bearer /models')
  assertOpenAIModelsBody(bearerV1Body, 'Bearer /v1/models')
  assert.deepEqual(modelIds(xApiKeyRootBody), modelIds(xApiKeyV1Body), 'x-api-key 下 /models 与 /v1/models 必须返回同一目录')
  assert.deepEqual(modelIds(xApiKeyRootBody), modelIds(bearerRootBody), 'x-api-key 与 Bearer 必须返回同一目录')
  assert.deepEqual(modelIds(xApiKeyV1Body), modelIds(bearerV1Body), 'Bearer 与 x-api-key 必须为 /v1/models 返回同一目录')
  assert.deepEqual(modelIds(bearerRootBody), modelIds(bearerV1Body), 'Bearer 必须为 /models 与 /v1/models 返回同一目录')
  const authenticatedIds = new Set(modelIds(xApiKeyRootBody))
  assert(authenticatedIds.has('gpt-5.6-sol'), 'API Key 模型目录必须包含其 GPT 分组模型')
  assert(authenticatedIds.has('gemini-3.5-flash'), 'API Key 模型目录必须包含其 Gemini 分组模型')
  assert.equal(authenticatedIds.has('claude-fable-5'), false, 'API Key 模型目录不得泄漏未绑定的 Anthropic 模型')

  const regularRequest = {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      model: 'gpt-5.6-sol',
      messages: [{ role: 'user', content: '验证本地网关认证' }]
    })
  }
  const [xApiKeyRegularResponse, bearerRegularResponse] = await Promise.all([
    fetch(`${baseUrl}/v1/chat/completions`, {
      ...regularRequest,
      headers: { ...regularRequest.headers, ...xApiKeyHeaders }
    }),
    fetch(`${baseUrl}/v1/chat/completions`, {
      ...regularRequest,
      headers: { ...regularRequest.headers, ...bearerHeaders }
    })
  ])
  assert.notEqual(xApiKeyRegularResponse.status, 401, 'x-api-key 普通网关请求不得被误判为未认证')
  assert.equal(xApiKeyRegularResponse.status, bearerRegularResponse.status, 'x-api-key 与 Bearer 普通网关请求必须进入同一认证结果')
  const [xApiKeyRegularBody, bearerRegularBody] = await Promise.all([
    xApiKeyRegularResponse.text(),
    bearerRegularResponse.text()
  ])
  assert.equal(xApiKeyRegularBody, bearerRegularBody, 'x-api-key 与 Bearer 普通网关请求必须保留同一后续错误语义')

  const geminiResponse = await fetch(`${baseUrl}/v1beta/models`, {
    headers: { 'x-goog-api-key': apiKey.key }
  })
  assert.equal(geminiResponse.status, 200)
  const geminiBody = await geminiResponse.json() as { models?: Array<{ name?: string }> }
  assert(Array.isArray(geminiBody.models), '/v1beta/models 必须保留 Gemini 原生 models 数组')
  assert(geminiBody.models?.some((model) => model.name === 'models/gemini-3.5-flash'), 'Gemini 原生目录必须包含 Gemini 模型')

  console.log('gateway models dynamic HTTP regression passed')
} finally {
  await closeServer(server)
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
}

interface OpenAIModelsBody {
  object?: string
  data?: Array<{ id?: string }>
  models?: unknown[]
}

function assertOpenAIModelsBody(body: OpenAIModelsBody, label: string): void {
  assert.equal(body.object, 'list', `${label} 必须返回 OpenAI-compatible object=list`)
  assert(Array.isArray(body.data), `${label} 必须返回 OpenAI-compatible data 数组`)
  assert.equal(Array.isArray(body.models), false, `${label} 不得误判为 Gemini models 数组`)
}

function modelIds(body: OpenAIModelsBody): string[] {
  return [...new Set((body.data ?? []).flatMap((model) => (
    typeof model.id === 'string' && model.id ? [model.id] : []
  )))].sort()
}

function listen(server: Server): Promise<void> {
  return new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolveListen()
    })
  })
}

function address(server: Server): { port: number } {
  const value = server.address()
  assert(value && typeof value !== 'string')
  return value
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server) return
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
}
