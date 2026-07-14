import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-balance-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-balance-http-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  balanceRepository,
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-balance.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

let apiServer: http.Server | undefined

try {
  const admin = repositories.createSystemAccount({
    username: `balance_http_admin_${Date.now()}`,
    displayName: '余额HTTP契约管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({ name: '余额 HTTP 契约分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '余额 HTTP 契约账户',
    type: 'api_key',
    credentials: { api_key: 'sk-http-single', base_url: 'https://relay.example/v1' },
    groupId: group.id,
    status: 'disabled',
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  const generation = account.balanceQueryNextRefreshAt
  assert(generation, '启用余额查询后必须生成刷新代次')
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: account.id,
    systemAccountId: admin.id,
    snapshot: {
      status: 'fresh',
      remainingUsd: '88.000000',
      lastAttemptAt: generation,
      lastSuccessAt: generation
    },
    nextRefreshAfter: generation
  })

  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`
  apiServer = app.listen(0, '127.0.0.1')
  await onceListening(apiServer)
  const baseUrl = `http://127.0.0.1:${serverPort(apiServer)}`

  const multiResponse = await patchAccount(baseUrl, cookie, account.id, {
    credentials: {
      api_key: 'sk-http-single',
      api_keys: ['sk-http-single', 'sk-http-second'],
      api_key_strategy: 'round_robin',
      base_url: 'https://relay.example/v1'
    }
  })
  assert.equal(multiResponse.status, 200, '单 Key 改为多 Key 必须保存成功')
  assert.equal(multiResponse.body.data?.balanceQueryEnabled, false, 'PATCH 响应必须即时返回余额已关闭')
  assert.equal(multiResponse.body.data?.balanceQueryNextRefreshAt, undefined, 'PATCH 响应不得保留旧调度时间')

  const multiList = await listAccounts(baseUrl, cookie)
  const listedMulti = multiList.find((item) => item.id === account.id)
  assert(listedMulti, '账户列表必须返回刚更新的账户')
  assert.equal(listedMulti.balanceQueryEnabled, false, '列表必须以业务库关闭状态为准')
  assert.equal(listedMulti.balanceSnapshot, undefined, '即使跨库快照尚未删除，列表也不得回显旧 Key 金额')

  const singleResponse = await patchAccount(baseUrl, cookie, account.id, {
    credentials: {
      api_key: 'sk-http-single',
      api_keys: ['sk-http-single'],
      base_url: 'https://relay.example/v1'
    }
  })
  assert.equal(singleResponse.status, 200, '多 Key 恢复单 Key 必须保存成功')
  assert.equal(singleResponse.body.data?.balanceQueryEnabled, false, '恢复单 Key 后不得自动重新开启余额查询')

  const singleList = await listAccounts(baseUrl, cookie)
  const listedSingle = singleList.find((item) => item.id === account.id)
  assert(listedSingle, '恢复单 Key 后账户仍应存在')
  assert.equal(listedSingle.balanceQueryEnabled, false)
  assert.equal(listedSingle.balanceSnapshot, undefined, '恢复单 Key 但未人工开启时仍不得回显旧快照')
} finally {
  await closeServer(apiServer)
  await closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('账户余额 HTTP 契约回归通过：多 Key 自动关闭、响应即时收口、旧快照隐藏且恢复单 Key 不自启')

type AccountResponse = {
  id?: string
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: unknown
}

async function patchAccount(
  baseUrl: string,
  cookie: string,
  accountId: string,
  body: Record<string, unknown>
): Promise<{ status: number; body: { data?: AccountResponse; message?: string } }> {
  const response = await fetch(`${baseUrl}/__aisys__/api/accounts/${accountId}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return {
    status: response.status,
    body: await response.json() as { data?: AccountResponse; message?: string }
  }
}

async function listAccounts(baseUrl: string, cookie: string): Promise<AccountResponse[]> {
  const response = await fetch(`${baseUrl}/__aisys__/api/accounts?page=1&pageSize=20`, {
    headers: { cookie }
  })
  assert.equal(response.status, 200, '账户列表请求必须成功')
  const body = await response.json() as { data?: { items?: AccountResponse[] } }
  return body.data?.items ?? []
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
