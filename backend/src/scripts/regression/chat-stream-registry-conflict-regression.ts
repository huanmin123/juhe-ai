import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { NextFunction, Request, Response } from 'express'

const tempRoot = resolve(tmpdir(), `juhe-ai-chat-registry-conflict-${Date.now()}-${Math.random().toString(16).slice(2)}`)

process.env.JUHE_AI_DISABLE_BASE_ENV = 'true'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
process.env.JUHE_AI_SECRET = 'chat-registry-conflict-secret'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_CHAT_DATABASE_PATH = join(tempRoot, 'chat.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_ROOT = join(tempRoot, 'codex-context')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context', 'state-shards')
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'

mkdirSync(tempRoot, { recursive: true })

const { default: express } = await import('express')
const { runtimeConfig } = await import('../../config/runtime.js')
const { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } = await import('../../domain/provider-protocol.js')
const { withRequestAuthContext } = await import('../../modules/auth/request-context.js')
const { chatRouter, shutdownChatGenerationRegistry } = await import('../../modules/chat/chat.routes.js')
const { rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync } = await import('../../modules/model-pricing/published-model-catalog.service.js')
const { getChatDatabaseClient } = await import('../../storage/chat-client.js')
const { closeStorageDatabases } = await import('../../storage/database.js')
const {
  createChatConversation,
  findChatTurnByClientMessageId,
  getChatConversation,
  listChatMessages
} = await import('../../storage/chat.repository.js')
const repositories = await import('../../storage/repositories.js')
const { createApiKeyRecordWithRouteStrategy } = await import('../shared/route-strategy-fixture.js')

const ownerId = 'sys_admin'
const model = 'gpt-5.5'
const clientMessageId = 'chat-registry-conflict-client'
const appPort = await freePort()
const baseUrl = `http://127.0.0.1:${appPort}`
runtimeConfig.port = appPort
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

let upstreamRequestCount = 0
let server: Server | undefined

try {
  const access = { systemAccountId: ownerId, role: 'user' as const }
  const group = repositories.createGroup({
    name: 'Chat registry conflict group',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'Chat registry conflict account',
    type: 'api_key',
    credentials: { api_key: 'sk-chat-registry-conflict', base_url: `${baseUrl}/v1` },
    groupId: group.id,
    supportedModels: [model],
    healthCheckModel: model,
    status: 'active',
    schedulable: true
  }, access)
  assert(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }))
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Chat registry conflict key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)
  await rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(ownerId)

  const client = await getChatDatabaseClient()
  const conversation = await createChatConversation(client, {
    id: 'chat_registry_conflict_conversation',
    systemAccountId: ownerId,
    apiKeyId: apiKey.id,
    apiKeyNameSnapshot: apiKey.name,
    defaultModel: model,
    now: new Date().toISOString(),
    maxConversationsPerUser: 50
  })

  const app = express()
  app.use(express.json())
  app.use((req: Request, _res: Response, next: NextFunction) => {
    withRequestAuthContext({
      systemAccountId: ownerId,
      role: 'user',
      username: ownerId,
      displayName: ownerId,
      mustChangePassword: false,
      sessionId: 'session_chat_registry_conflict'
    }, next)
  })
  app.post(['/v1/responses', '/v1/chat/completions'], (_req: Request, res: Response) => {
    upstreamRequestCount += 1
    res.status(502).json({ error: { message: 'registry conflict test must not reach the upstream route' } })
  })
  app.use('/my-chat', chatRouter)
  app.use((error: unknown, _req: Request, res: Response, _next: NextFunction) => {
    res.status(500).json({ message: error instanceof Error ? error.message : String(error) })
  })
  server = await listen(app, appPort)

  await shutdownChatGenerationRegistry({ timeoutMs: 0 })
  const upstreamRequestsBefore = upstreamRequestCount
  const response = await fetch(`${baseUrl}/my-chat/conversations/${conversation.id}/stream`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId, content: 'This turn must be finalized when the registry rejects it.', model })
  })
  const responseBody = await response.text()
  assert.equal(response.status, 409, responseBody)
  assert.equal((JSON.parse(responseBody) as { code?: unknown }).code, 'chat_stream_conflict')

  const submission = await findChatTurnByClientMessageId(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    clientMessageId
  })
  assert(submission, 'accepted turn must retain an authoritative submission record')
  assert.deepEqual({ status: submission.assistantStatus, errorCode: submission.errorCode }, {
    status: 'failed',
    errorCode: 'internal_generation_failed'
  })

  const storedConversation = await getChatConversation(client, conversation.id, ownerId)
  assert(storedConversation, 'conversation must remain readable after conflict finalization')
  assert.equal(storedConversation.activeTurnId, undefined, 'conflict finalization must clear activeTurnId')

  const messages = await listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    limit: 10,
    now: new Date().toISOString()
  })
  const assistant = messages.find((message) => message.role === 'assistant')
  assert(assistant, 'accepted turn must retain its assistant placeholder')
  assert.deepEqual({ status: assistant.status, errorCode: assistant.errorCode }, {
    status: 'failed',
    errorCode: 'internal_generation_failed'
  })
  assert.equal(upstreamRequestCount, upstreamRequestsBefore, 'registry rejection must not start an upstream request')

  console.log('Chat stream registry conflict behavior regression passed')
} finally {
  await closeServer(server)
  await import('../../storage/sqlite-read-worker-pool.js')
    .then((module) => module.closeSqliteReadWorkerPool())
    .catch(() => undefined)
  closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const socket = net.createServer()
    socket.once('error', reject)
    socket.listen(0, '127.0.0.1', () => {
      const address = socket.address()
      if (!address || typeof address === 'string') {
        reject(new Error('Unable to allocate test port'))
        return
      }
      const port = address.port
      socket.close((error) => error ? reject(error) : resolvePort(port))
    })
  })
}

async function listen(app: ReturnType<typeof express>, port: number): Promise<Server> {
  return new Promise((resolveServer, reject) => {
    const listeningServer = app.listen(port, '127.0.0.1')
    listeningServer.once('listening', () => resolveServer(listeningServer))
    listeningServer.once('error', reject)
  })
}

async function closeServer(listeningServer?: Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolveClose, reject) => {
    listeningServer.close((error) => error ? reject(error) : resolveClose())
  })
}
