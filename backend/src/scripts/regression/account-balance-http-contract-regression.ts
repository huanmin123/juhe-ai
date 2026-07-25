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
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
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
let mockBalanceServer: http.Server | undefined

try {
  mockBalanceServer = createMockBalanceServer()
  mockBalanceServer.listen(0, '127.0.0.1')
  await onceListening(mockBalanceServer)
  const mockBaseUrl = `http://127.0.0.1:${serverPort(mockBalanceServer)}`
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
    credentials: { api_key: 'sk-http-single', base_url: mockBaseUrl },
    groupId: group.id,
    status: 'disabled',
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  const errorAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '余额 HTTP 错误账户',
    type: 'api_key',
    credentials: { api_key: 'sk-http-error', base_url: mockBaseUrl },
    groupId: group.id,
    status: 'error',
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  const businessDatabase = databaseModule.getBusinessDatabase()
  businessDatabase.prepare(`UPDATE accounts SET status = 'error', schedulable = 0 WHERE id = ?`).run(errorAccount.id)
  const storedErrorAccountState = businessDatabase.prepare(`SELECT status, schedulable FROM accounts WHERE id = ?`).get(errorAccount.id) as {
    status?: string
    schedulable?: number
  } | undefined
  assert.equal(storedErrorAccountState?.status, 'error', '错误账户 HTTP 回归夹具必须真实处于 error 状态')
  assert.equal(storedErrorAccountState?.schedulable, 0, '错误账户 HTTP 回归夹具必须真实不可调度')
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

  const disabledRefresh = await refreshAccountBalance(baseUrl, cookie, `/__aisys__/api/accounts/${account.id}/balance/refresh`)
  assert.equal(disabledRefresh.status, 200, '停用的自有账户必须可人工刷新余额')
  assert.equal(disabledRefresh.body.data?.status, 'fresh')
  assert.equal(disabledRefresh.body.data?.remainingUsd, '42.500000')

  const errorRefresh = await refreshAccountBalance(baseUrl, cookie, `/__aisys__/api/accounts/${errorAccount.id}/balance/refresh`)
  assert.equal(errorRefresh.status, 200, '错误状态的自有账户必须可人工刷新余额并返回诊断结果')
  assert.equal(errorRefresh.body.data?.status, 'failed', '上游 HTTP 非 2xx 不得被内部解读为余额能力 unsupported')
  assert.match(errorRefresh.body.data?.errorMessage ?? '', /HTTP \d{3}/, '诊断可记录观测到的状态码，但不赋予业务语义')
  const errorAccountAfterRefresh = businessDatabase.prepare(`
    SELECT status, schedulable, balance_query_enabled, balance_query_config_json
    FROM accounts
    WHERE id = ?
  `).get(errorAccount.id) as Record<string, unknown>
  assert.equal(errorAccountAfterRefresh.status, 'error', '余额 HTTP 失败不得改写账户状态')
  assert.equal(errorAccountAfterRefresh.schedulable, 0, '余额 HTTP 失败不得改写账户调度属性')
  assert.equal(errorAccountAfterRefresh.balance_query_enabled, 1, '余额 HTTP 失败不得关闭用户开关')
  assert.deepEqual(JSON.parse(String(errorAccountAfterRefresh.balance_query_config_json)), {
    adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'sub2api'
  }, '余额 HTTP 失败不得清除用户配置或首选适配器')

  const grantee = repositories.createSystemAccount({
    username: `balance_http_grantee_${Date.now()}`,
    displayName: '余额HTTP契约被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({ name: '余额 HTTP 授权实例分组', providerCode: 'gpt', enabled: true }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: errorAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '余额 HTTP 授权权限回归'
  }, access)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === errorAccount.id)
  assert(authorizedInstance, '授权后必须生成被授权账户实例')
  const granteeCookie = `juhe_ai_session=${repositories.createSession(grantee.id, 1).token}`
  const authorizedRefresh = await refreshAccountBalance(
    baseUrl,
    granteeCookie,
    `/__aisys__/api/my-accounts/${authorizedInstance.id}/balance/refresh`
  )
  assert.equal(authorizedRefresh.status, 403, '授权实例仍不得人工刷新来源账户余额')
  assert.match(authorizedRefresh.body.message ?? '', /无权刷新/)

  const multiResponse = await patchAccount(baseUrl, cookie, account.id, {
    credentials: {
      api_key: 'sk-http-single',
      api_keys: ['sk-http-single', 'sk-http-second'],
      api_key_strategy: 'round_robin',
      base_url: mockBaseUrl
    }
  })
  assert.equal(multiResponse.status, 200, '单 Key 改为多 Key 必须保存成功')
  assert.equal(multiResponse.body.data?.balanceQueryEnabled, false, 'PATCH 响应必须即时返回余额已关闭')
  assert.equal(multiResponse.body.data?.balanceQueryNextRefreshAt, undefined, 'PATCH 响应不得保留旧调度时间')

  const multiList = await listAccounts(baseUrl, cookie)
  const listedMulti = multiList.find((item) => item.id === account.id)
  assert(listedMulti, '账户列表必须返回刚更新的账户')
  assert.equal(listedMulti.balanceQueryEnabled, undefined, '轻量列表不应夹带余额配置详情')
  assert.equal(listedMulti.balanceSnapshot, undefined, '即使跨库快照尚未删除，列表也不得回显旧 Key 金额')
  assert.equal(
    businessDatabase.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(account.id)?.balance_query_enabled,
    0,
    '业务库必须真实关闭多 Key 账户的余额查询'
  )

  const singleResponse = await patchAccount(baseUrl, cookie, account.id, {
    credentials: {
      api_key: 'sk-http-single',
      api_keys: ['sk-http-single'],
      base_url: mockBaseUrl
    }
  })
  assert.equal(singleResponse.status, 200, '多 Key 恢复单 Key 必须保存成功')
  assert.equal(singleResponse.body.data?.balanceQueryEnabled, false, '恢复单 Key 后不得自动重新开启余额查询')

  const singleList = await listAccounts(baseUrl, cookie)
  const listedSingle = singleList.find((item) => item.id === account.id)
  assert(listedSingle, '恢复单 Key 后账户仍应存在')
  assert.equal(listedSingle.balanceQueryEnabled, undefined, '轻量列表仍不返回余额配置详情')
  assert.equal(listedSingle.balanceSnapshot, undefined, '恢复单 Key 但未人工开启时仍不得回显旧快照')
  assert.equal(
    businessDatabase.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(account.id)?.balance_query_enabled,
    0,
    '恢复单 Key 后不得自动重新开启余额查询'
  )
} finally {
  await closeServer(apiServer)
  await closeServer(mockBalanceServer)
  await closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('账户余额 HTTP 契约回归通过：停用/错误自有账户可刷新、授权实例保持 403、多 Key 自动关闭且旧快照隐藏')

type AccountResponse = {
  id?: string
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: unknown
}

type BalanceRefreshResponse = {
  status?: string
  remainingUsd?: string
  errorMessage?: string
}

function createMockBalanceServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'GET' || req.url?.split('?', 1)[0] !== '/v1/usage') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    if (req.headers.authorization === 'Bearer sk-http-error') {
      res.writeHead(401, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'invalid api key' } }))
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ unit: 'USD', remaining: '42.50', mode: 'quota_limited' }))
  })
}

async function refreshAccountBalance(
  baseUrl: string,
  cookie: string,
  path: string
): Promise<{ status: number; body: { data?: BalanceRefreshResponse; message?: string } }> {
  const response = await fetch(`${baseUrl}${path}`, { method: 'POST', headers: { cookie } })
  return {
    status: response.status,
    body: await response.json() as { data?: BalanceRefreshResponse; message?: string }
  }
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
