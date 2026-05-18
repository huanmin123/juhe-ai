import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-admin-dispatch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-admin-dispatch.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'authorized-account-admin-dispatch-records.sqlite3')
runtimeConfig.secret = 'authorized-account-admin-dispatch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  { testOpenAIAccount },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountSummary {
  id: string
  name: string
  status: string
  accessType?: string
  boundGroupId?: string
  bindingSystemAccountId?: string
  ownerSystemAccountId?: string
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface AccountTestResult {
  success: boolean
  message: string
  statusCode?: number
}

interface AccountTrafficMigrationResult {
  sourceAccount: AccountSummary
  targetAccount: AccountSummary
  migratedSessionCount: number
  sourceStatus: string
}

interface SeedState {
  adminCookie: string
  ownerAccountId: string
  ownerId: string
  granteeGroupId: string
  granteeTargetAccountId: string
  granteeId: string
}

let server: ReturnType<typeof app.listen> | undefined
let mockOpenAIServer: http.Server | undefined

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('管理员授权账户调度 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('管理员授权账户调度回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`
  const seed = seedData(mockBaseUrl)

  const adminGranteeAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    `/__aisys__/api/accounts?systemAccountId=${seed.granteeId}&page=1&pageSize=20`,
    seed.adminCookie
  )
  const authorizedAccount = adminGranteeAccounts.items.find((account) => account.id === seed.ownerAccountId)
  assert(authorizedAccount, '管理员按用户筛选时应能看到该用户可用的授权账户')
  assert.equal(authorizedAccount.accessType, 'authorized', '授权账户在被授权用户作用域下应保持 authorized 视角')
  assert.equal(authorizedAccount.boundGroupId, seed.granteeGroupId, '授权账户应返回被授权用户自己的绑定分组')
  assert.equal(authorizedAccount.bindingSystemAccountId, seed.granteeId, '授权账户应返回本地绑定所属系统账户，供管理侧代操作写入同一作用域')

  const updated = await patchEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/accounts/${seed.ownerAccountId}/authorized-dispatch?systemAccountId=${authorizedAccount.bindingSystemAccountId}`,
    seed.adminCookie,
    { superPriorityEnabled: true }
  )
  assert.equal(updated.accessType, 'authorized', '管理员代操作后仍应返回授权账户视角')
  assert.equal(updated.bindingSystemAccountId, seed.granteeId, '管理员代操作响应应保留被代操作用户作用域')
  assert.equal(updated.superPriorityEnabled, true, '管理员应能代被授权用户开启本地超级优先')

  const granteeView = repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)
  const ownerView = repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)
  assert.equal(granteeView?.superPriorityEnabled, true, '管理员代操作应写入被授权用户自己的本地调度标记')
  assert.equal(ownerView?.superPriorityEnabled, false, '管理员代操作授权账户不应修改账户所有者原始超级优先')

  const testAccount = repositories.findAccountForTest(seed.ownerAccountId, { systemAccountId: seed.granteeId, role: 'user' as const })
  assert.equal(testAccount?.accessType, 'authorized', '被授权用户视角应能拿到授权账户测试对象')
  assert.equal(testAccount?.bindingSystemAccountId, seed.granteeId, '测试对象应保留本地绑定所属系统账户')
  const tested: AccountTestResult = await testOpenAIAccount(testAccount, { model: 'gpt-5.5', prompt: 'hi' })
  assert.equal(tested.success, true, `管理员应能代被授权用户测试授权账户：${tested.message}`)
  assert.equal(tested.statusCode, 200, '授权账户测试应通过被授权用户自己的分组绑定进入网关链路')

  const migration = await postEnvelope<AccountTrafficMigrationResult>(
    baseUrl,
    `/__aisys__/api/accounts/${seed.ownerAccountId}/traffic-migration?systemAccountId=${authorizedAccount.bindingSystemAccountId}`,
    seed.adminCookie,
    { targetAccountId: seed.granteeTargetAccountId, sourceStatus: 'temporary_unavailable' }
  )
  assert.equal(migration.sourceAccount.status, 'temporary_unavailable', '管理员应能代被授权用户迁移授权账户流量')
  assert.equal(migration.sourceAccount.bindingSystemAccountId, seed.granteeId, '迁移响应应保留被授权用户本地绑定作用域')
  assert.equal(migration.targetAccount.id, seed.granteeTargetAccountId, '迁移目标应使用被授权用户分组内的可用账户')
  assert.equal(repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const }).find((account) => account.id === seed.ownerAccountId)?.status, 'active', '管理员迁移授权账户不应修改账户所有者原账户状态')
  assert.equal(repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const }).find((account) => account.id === seed.ownerAccountId)?.status, 'temporary_unavailable', '管理员迁移授权账户应只写入被授权用户本地绑定状态')

  console.log('管理员代操作授权账户调度回归通过')
} finally {
  await closeServer(server)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    const database = databaseModule.getDatabase()
    const recordDatabase = databaseModule.getRecordDatabase()
    database.close()
    recordDatabase.close()
  } catch {
  }
  try {
    rmSync(tempRoot, { recursive: true, force: true })
  } catch {
  }
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    req.resume()
    const completedEvent = {
      type: 'response.completed',
      response: {
        id: 'resp_authorized_admin_dispatch',
        object: 'response',
        status: 'completed',
        output: [
          {
            type: 'message',
            content: [{ type: 'output_text', text: 'OK' }]
          }
        ],
        usage: {
          input_tokens: 1,
          output_tokens: 1,
          total_tokens: 2
        }
      }
    }
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
  })
}

function seedData(mockBaseUrl: string): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const owner = repositories.createSystemAccount({
    username: 'admin_dispatch_owner',
    displayName: '管理员调度所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'admin_dispatch_grantee',
    displayName: '管理员调度被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '管理员代操作授权账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch', base_url: mockBaseUrl }
  }, ownerAccess)
  const granteeTargetAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '管理员代操作迁移目标账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch-target', base_url: mockBaseUrl }
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '管理员代操作调度回归'
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '管理员代操作被授权分组',
    providerCode: 'openai'
  }, granteeAccess)
  assert(repositories.setAccountGroup(ownerAccount.id, granteeGroup.id, granteeAccess), '授权账户绑定到被授权用户分组失败')
  assert(repositories.setAccountGroup(granteeTargetAccount.id, granteeGroup.id, granteeAccess), '迁移目标账户绑定到被授权用户分组失败')
  return {
    adminCookie: sessionCookie(admin.id),
    ownerAccountId: ownerAccount.id,
    ownerId: owner.id,
    granteeGroupId: granteeGroup.id,
    granteeTargetAccountId: granteeTargetAccount.id,
    granteeId: grantee.id
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  return parseEnvelope<T>(path, response)
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(path, response)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(path, response)
}

async function parseEnvelope<T>(path: string, response: Response): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      listeningServer.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    listeningServer.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    listeningServer.closeIdleConnections?.()
  })
}
