import assert from 'node:assert/strict'
import { createServer, type Server } from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

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
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
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
  server = createServer(app)
  await listen(server)
  const baseUrl = `http://127.0.0.1:${address(server).port}`

  const rootResponse = await fetch(`${baseUrl}/models`)
  assert.equal(rootResponse.status, 200)
  const rootBody = await rootResponse.json() as { object?: string; data?: Array<{ id?: string }>; models?: unknown[] }
  assert.equal(rootBody.object, 'list', '普通 /models 必须默认返回 OpenAI-compatible object=list')
  assert(Array.isArray(rootBody.data), '普通 /models 必须默认返回 OpenAI-compatible data 数组')
  assert.equal(Array.isArray(rootBody.models), false, '普通 /models 不得误判为 Gemini models 数组')
  assertOpenAIProviders(rootBody.data ?? [])

  const v1Response = await fetch(`${baseUrl}/v1/models`)
  assert.equal(v1Response.status, 200)
  const v1Body = await v1Response.json() as { object?: string; data?: Array<{ id?: string }> }
  assert.equal(v1Body.object, 'list')
  assertOpenAIProviders(v1Body.data ?? [])

  const geminiResponse = await fetch(`${baseUrl}/v1beta/models`)
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

function assertOpenAIProviders(items: Array<{ id?: string }>): void {
  const ids = new Set(items.map((item) => item.id))
  for (const [providerCode, model] of [
    ['gpt', 'gpt-5.6-sol'],
    ['deepseek', 'deepseek-v4-flash'],
    ['glm', 'glm-5.2'],
    ['anthropic', 'claude-fable-5'],
    ['gemini', 'gemini-3.5-flash'],
    ['xai', 'grok-4.5']
  ]) {
    assert(ids.has(model), `公开 OpenAI-compatible 目录必须覆盖 ${providerCode} 供应商模型 ${model}`)
  }
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
