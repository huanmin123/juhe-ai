import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-visible-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-list-visible.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-visible-secret'
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
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
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
  ownerSystemAccountId?: string
  systemAccountId?: string
  name: string
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface SeedState {
  adminCookie: string
  userAId: string
  userAAccountId: string
  userACookie: string
  userBId: string
  userBAccountId: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const seed = seedData()
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('账户列表可见性回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const adminAllAccounts = await getEnvelope<AccountListResult>(baseUrl, '/__aisys__/api/accounts?systemAccountId=all&page=1&pageSize=20', seed.adminCookie)
  assert.equal(adminAllAccounts.items.some((account) => account.id === seed.userAAccountId), true, '管理员全量账户列表应包含用户 A 的种子账户')
  assert.equal(adminAllAccounts.items.some((account) => account.id === seed.userBAccountId), true, '管理员全量账户列表应包含用户 B 的种子账户')
  assert.equal(adminAllAccounts.total >= 2, true, '管理员全量账户列表总数不应为空')

  const adminUserAAccounts = await getEnvelope<AccountListResult>(baseUrl, `/__aisys__/api/accounts?systemAccountId=${seed.userAId}&page=1&pageSize=20`, seed.adminCookie)
  assert.deepEqual(adminUserAAccounts.items.map((account) => account.id), [seed.userAAccountId], '管理员按系统账户筛选后应返回对应用户的种子账户')
  assert.equal(adminUserAAccounts.items.every((account) => account.ownerSystemAccountId === seed.userAId), true, '管理员筛选账户不应混入其他所有者')

  const userAMyAccounts = await getEnvelope<AccountListResult>(baseUrl, `/__aisys__/api/my-accounts?systemAccountId=${seed.userBId}&page=1&pageSize=20`, seed.userACookie)
  assert.deepEqual(userAMyAccounts.items.map((account) => account.id), [seed.userAAccountId], '用户侧账户列表应固定为当前用户，不应被 systemAccountId 查询参数筛空或越权')

  const outOfRangePage = await getEnvelope<AccountListResult>(baseUrl, `/__aisys__/api/accounts?systemAccountId=${seed.userAId}&page=99&pageSize=20`, seed.adminCookie)
  assert.equal(outOfRangePage.total, 980, '页码越界时应返回当前窗口分页上界 total，避免额外 COUNT(*)')
  assert.equal(outOfRangePage.hasMore, false, '页码越界时应明确 hasMore=false，供前端回退到第一页')
  assert.equal(outOfRangePage.items.length, 0, '页码越界契约应保持为空页，由前端根据 hasMore 回退')

  console.log('AI 账户列表可见性回归通过：种子账户在管理/用户侧列表均可见，越界空页返回轻量分页信号')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const userA = repositories.createSystemAccount({
    username: 'account_list_visible_user_a',
    displayName: '账户列表可见用户 A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'account_list_visible_user_b',
    displayName: '账户列表可见用户 B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
  const userBAccess = { systemAccountId: userB.id, role: 'user' as const }
  const userAGroup = repositories.createGroup({
    name: '账户列表可见分组 A',
    providerCode: 'openai'
  }, userAAccess)
  const userBGroup = repositories.createGroup({
    name: '账户列表可见分组 B',
    providerCode: 'openai'
  }, userBAccess)
  const userAAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '账户列表可见种子 A',
    type: 'api_key',
    credentials: { api_key: 'sk-account-list-visible-a', base_url: 'https://api.openai.com/v1' },
    status: 'active',
    groupId: userAGroup.id
  }, userAAccess)
  const userBAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '账户列表可见种子 B',
    type: 'api_key',
    credentials: { api_key: 'sk-account-list-visible-b', base_url: 'https://api.openai.com/v1' },
    status: 'active',
    groupId: userBGroup.id
  }, userBAccess)
  return {
    adminCookie: sessionCookie(admin.id),
    userAId: userA.id,
    userAAccountId: userAAccount.id,
    userACookie: sessionCookie(userA.id),
    userBId: userB.id,
    userBAccountId: userBAccount.id
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
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
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}
