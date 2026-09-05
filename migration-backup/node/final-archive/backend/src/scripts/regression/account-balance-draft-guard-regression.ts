import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-balance-draft-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-balance-draft-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { testAccountBalanceCandidate },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/accounts/account-balance-query.service.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

let upstreamHits = 0
const upstream = http.createServer((req, res) => {
  upstreamHits += 1
  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  if (req.method !== 'GET' || url.pathname !== '/v1/usage') {
    res.writeHead(404, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ error: { message: 'mock path not found' } }))
    return
  }
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ unit: 'USD', remaining: '12.34', mode: 'quota_limited' }))
})

let apiServer: http.Server | undefined

try {
  upstream.listen(0, '127.0.0.1')
  await onceListening(upstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstream)}/v1`

  const admin = repositories.createSystemAccount({
    username: `balance_draft_guard_admin_${Date.now()}`,
    displayName: '余额草稿守卫管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({ name: '余额草稿守卫分组', providerCode: 'gpt', enabled: true }, access)
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  apiServer = app.listen(0, '127.0.0.1')
  await onceListening(apiServer)
  const apiBaseUrl = `http://127.0.0.1:${serverPort(apiServer)}`

  const multiKeyCredentials = {
    api_key: 'sk-balance-draft-first',
    api_keys: ['sk-balance-draft-first', 'sk-balance-draft-second'],
    api_key_strategy: 'round_robin',
    base_url: upstreamBaseUrl
  }
  const multiDraft = await postBalanceDraft(apiBaseUrl, cookie, draftAccount(group.id, multiKeyCredentials))
  assert.equal(multiDraft.status, 200, '多 Key 草稿余额测试应保持可用')
  assert.equal(multiDraft.body.data?.status, 'fresh', '独立 Key 额度应生成可合计的余额快照')
  assert.equal(multiDraft.body.data?.remainingUsd, '24.68', '多 Key 草稿应按 Key 精确合计余额')
  assert.equal(multiDraft.body.data?.keyCount, 2)
  assert.equal(multiDraft.body.data?.queriedKeyCount, 2)
  assert.equal(upstreamHits, 2, '多 Key 草稿应逐 Key 调用余额上游')

  const directResult = await testAccountBalanceCandidate({
    id: 'multi-key-direct-balance-test',
    credentials: multiKeyCredentials,
    config: { adapter: 'builtin', intervalMinutes: 5 }
  })
  assert.equal(directResult.status, 'fresh', '直接调用查询服务时也应支持多 Key')
  assert.equal(directResult.remainingUsd, '24.68')
  assert.equal(directResult.keyCount, 2)
  assert.equal(directResult.queriedKeyCount, 2)
  assert.equal(upstreamHits, 4, '查询服务应为每个 Key 发起独立请求')

  const singleKeyResult = await postBalanceDraft(apiBaseUrl, cookie, draftAccount(group.id, {
    api_key: 'sk-balance-draft-single',
    api_keys: ['sk-balance-draft-single'],
    base_url: upstreamBaseUrl
  }))
  assert.equal(singleKeyResult.status, 200, '单 Key 草稿余额测试应保持可用')
  assert.equal(singleKeyResult.body.data?.status, 'fresh')
  assert.equal(singleKeyResult.body.data?.remainingUsd, '12.340000')
  assert.equal(upstreamHits, 5, '单 Key 草稿应只调用一次首选余额上游')
} finally {
  await closeServer(apiServer)
  await closeServer(upstream)
  await closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account balance draft guard regression passed')

function draftAccount(groupId: string, credentials: Record<string, unknown>): Record<string, unknown> {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '余额草稿守卫账户',
    type: 'api_key',
    credentials,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'chat_json',
    groupId
  }
}

async function postBalanceDraft(
  baseUrl: string,
  cookie: string,
  account: Record<string, unknown>
): Promise<{
  status: number
  body: { data?: { status?: string; remainingUsd?: string; keyCount?: number; queriedKeyCount?: number }; message?: string }
}> {
  const response = await fetch(`${baseUrl}/__aisys__/api/accounts/balance/test-draft`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({
      account,
      balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' }
    })
  })
  return {
    status: response.status,
    body: await response.json() as {
      data?: { status?: string; remainingUsd?: string; keyCount?: number; queriedKeyCount?: number }
      message?: string
    }
  }
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务地址不可用')
  return address.port
}
