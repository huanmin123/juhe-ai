import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-account-options-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const blockedDatasetDatabasePath = join(tempRoot, 'dataset-as-directory.sqlite3')
const blockedStatsDatabasePath = join(tempRoot, 'stats-as-directory.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = blockedDatasetDatabasePath
runtimeConfig.statsDatabasePath = blockedStatsDatabasePath
runtimeConfig.secret = 'system-account-options-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(blockedDatasetDatabasePath, { recursive: true })
mkdirSync(blockedStatsDatabasePath, { recursive: true })
logger.level = 'silent'

const [
  { systemAccountsRouter },
  { requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/system-accounts/system-accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/system-accounts', systemAccountsRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface SystemAccountOptionSummary {
  id: string
  username: string
  displayName: string
  status: 'active' | 'disabled'
  role?: unknown
  description?: unknown
  mustChangePassword?: unknown
  lastLoginAt?: unknown
  createdAt?: unknown
  updatedAt?: unknown
  passwordHash?: unknown
}

interface SystemAccountSummary {
  id: string
  username: string
  displayName: string
  role: 'super_admin' | 'admin' | 'user'
  status: 'active' | 'disabled'
}

interface SystemAccountListResult {
  items: SystemAccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface SeedState {
  activeUserId: string
  adminCookie: string
  adminId: string
  disabledUserId: string
  middleNameUserId: string
  prefixUserId: string
  promotionTargetId: string
  readonlyAdminCookie: string
  readonlyAdminId: string
  userCookie: string
  wildcardLiteralUserId: string
  wildcardNeighborUserId: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const seed = seedData()
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('系统账户选项轻量回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const systemAccountOptionSqls: string[] = []
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+system_accounts\b/i.test(sql)) {
      systemAccountOptionSqls.push(sql)
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    const adminOptions = await getEnvelope<SystemAccountOptionSummary[]>(baseUrl, '/__aisys__/api/system-accounts/options', seed.adminCookie)
    const activeOption = adminOptions.find((account) => account.id === seed.activeUserId)
    assert(activeOption, '系统账户选项应包含普通启用用户')
    assert.equal(activeOption.displayName, '系统账户选项用户')
    assert.equal(activeOption.status, 'active')
    const disabledOption = adminOptions.find((account) => account.id === seed.disabledUserId)
    assert(disabledOption, '系统账户选项应包含停用用户，供筛选和历史归属展示')
    assert.equal(disabledOption.status, 'disabled')
    assertLightweightSystemAccountOption(activeOption)
    assertLightweightSystemAccountOption(disabledOption)

    assert.equal(systemAccountOptionSqls.length, 1, '系统账户选项请求应只执行一次系统账户选项查询')
    assert.equal(systemAccountOptionSqls.some((sql) => /SELECT\s+\*/i.test(sql)), false, '系统账户选项查询不应 SELECT *')
    assert.equal(systemAccountOptionSqls.some((sql) => /\bpassword_hash\b/i.test(sql)), false, '系统账户选项查询不应读取 password_hash')
    assert.equal(systemAccountOptionSqls.some((sql) => /\brole\b|\bmust_change_password\b|\blast_login_at\b|\bcreated_at\b|\bupdated_at\b/i.test(sql)), false, '系统账户选项查询不应读取管理字段')
    assert.equal(systemAccountOptionSqls.some((sql) => /\bCOALESCE\s*\(/i.test(sql)), false, '系统账户选项查询不应通过 COALESCE 扫描展示字段')

    const prefixOptions = await getEnvelope<SystemAccountOptionSummary[]>(baseUrl, '/__aisys__/api/system-accounts/options?keyword=系统账户选项用户&limit=10', seed.adminCookie)
    const prefixIds = prefixOptions.map((account) => account.id)
    assert(prefixIds.includes(seed.activeUserId), '系统账户选项关键词应命中名称精确匹配')
    assert(prefixIds.includes(seed.prefixUserId), '系统账户选项关键词应命中名称前缀匹配')
    assert(!prefixIds.includes(seed.middleNameUserId), '系统账户选项关键词不应命中名称中间包含')
    assert(prefixOptions.length <= 10, '系统账户选项接口应遵守 limit 参数')

    const usernameOptions = await getEnvelope<SystemAccountOptionSummary[]>(baseUrl, '/__aisys__/api/system-accounts/options?keyword=system_account_options_user&limit=10', seed.adminCookie)
    assert(usernameOptions.some((account) => account.id === seed.activeUserId), '系统账户选项关键词应命中用户名精确匹配')

    const wildcardOptions = await getEnvelope<SystemAccountOptionSummary[]>(baseUrl, `/__aisys__/api/system-accounts/options?keyword=${encodeURIComponent('percent%literal')}&limit=10`, seed.adminCookie)
    const wildcardIds = wildcardOptions.map((account) => account.id)
    assert(wildcardIds.includes(seed.wildcardLiteralUserId), '系统账户选项关键词应支持 % 字面量')
    assert(!wildcardIds.includes(seed.wildcardNeighborUserId), '系统账户选项关键词不应把用户输入的 % 当作 LIKE 通配符')

    const readonlyAdminList = await getEnvelope<SystemAccountListResult>(baseUrl, '/__aisys__/api/system-accounts?page=1&pageSize=10', seed.readonlyAdminCookie)
    assert(readonlyAdminList.items.some((account) => account.id === seed.activeUserId), '普通管理员应能查看系统账户列表')
    const readonlyAdminOptions = await getEnvelope<SystemAccountOptionSummary[]>(baseUrl, '/__aisys__/api/system-accounts/options?limit=10', seed.readonlyAdminCookie)
    assert(readonlyAdminOptions.some((account) => account.id === seed.activeUserId), '普通管理员应能查看系统账户选项')
    await assertJsonStatus(baseUrl, '/__aisys__/api/system-accounts', seed.readonlyAdminCookie, 'POST', {
      username: 'system_account_options_admin_blocked_create',
      displayName: '普通管理员禁止创建',
      password: 'password',
      role: 'user'
    }, 403, '普通管理员不应创建系统账户')
    await assertJsonStatus(baseUrl, '/__aisys__/api/system-accounts', seed.adminCookie, 'POST', {
      username: 'system_account_options_blocked_super_admin',
      displayName: '禁止新增超级管理员',
      password: 'password',
      role: 'super_admin'
    }, 400, '系统账户接口不应新增超级管理员')
    await assertJsonStatus(baseUrl, `/__aisys__/api/system-accounts/${seed.activeUserId}`, seed.readonlyAdminCookie, 'PATCH', {
      displayName: '普通管理员禁止更新'
    }, 403, '普通管理员不应更新系统账户')
    const promoted = await patchEnvelope<SystemAccountSummary>(baseUrl, `/__aisys__/api/system-accounts/${seed.promotionTargetId}`, seed.adminCookie, { role: 'admin' })
    assert.equal(promoted.role, 'admin', '超级管理员应能把普通用户升级为管理员')
    await assertJsonStatus(baseUrl, `/__aisys__/api/system-accounts/${seed.promotionTargetId}`, seed.adminCookie, 'PATCH', {
      role: 'super_admin'
    }, 400, '系统账户接口不应把用户升级为超级管理员')
    await assertJsonStatus(baseUrl, `/__aisys__/api/system-accounts/${seed.adminId}`, seed.adminCookie, 'PATCH', {
      role: 'admin'
    }, 409, '不能移除最后一个启用的超级管理员')

    for (const sql of systemAccountOptionSqls) {
      if (/\bLIKE\s+\?/i.test(sql)) {
        assert(/\bESCAPE\s+'\\'/i.test(sql), '系统账户选项前缀搜索应显式转义 LIKE 通配符')
      }
    }
  } finally {
    database.prepare = originalPrepare
  }

  const forbiddenResponse = await fetch(`${baseUrl}/__aisys__/api/system-accounts/options`, { headers: { cookie: seed.userCookie } })
  assert.equal(forbiddenResponse.status, 403, '普通用户不应调用管理侧系统账户选项接口')

  console.log('系统账户选项轻量回归通过：options 接口只读取最小字段，也不返回系统账户管理字段')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const activeUser = repositories.createSystemAccount({
    username: 'system_account_options_user',
    displayName: '系统账户选项用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const disabledUser = repositories.createSystemAccount({
    username: 'system_account_options_disabled',
    displayName: '系统账户选项停用用户',
    password: 'password',
    role: 'user',
    status: 'disabled',
    mustChangePassword: true
  })
  const prefixUser = repositories.createSystemAccount({
    username: 'system_account_options_user_prefix',
    displayName: '系统账户选项用户扩展',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const middleNameUser = repositories.createSystemAccount({
    username: 'system_account_options_middle',
    displayName: '普通系统账户选项用户中间',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const wildcardLiteralUser = repositories.createSystemAccount({
    username: 'percent%literal_user',
    displayName: 'percent%literal 用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const wildcardNeighborUser = repositories.createSystemAccount({
    username: 'percentXliteral_user',
    displayName: 'percentXliteral 用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const readonlyAdmin = repositories.createSystemAccount({
    username: 'system_account_options_readonly_admin',
    displayName: '系统账户选项只读管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const promotionTarget = repositories.createSystemAccount({
    username: 'system_account_options_promote_target',
    displayName: '系统账户选项待升级用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  return {
    activeUserId: activeUser.id,
    adminCookie: sessionCookie(admin.id),
    adminId: admin.id,
    disabledUserId: disabledUser.id,
    middleNameUserId: middleNameUser.id,
    prefixUserId: prefixUser.id,
    promotionTargetId: promotionTarget.id,
    readonlyAdminCookie: sessionCookie(readonlyAdmin.id),
    readonlyAdminId: readonlyAdmin.id,
    userCookie: sessionCookie(activeUser.id),
    wildcardLiteralUserId: wildcardLiteralUser.id,
    wildcardNeighborUserId: wildcardNeighborUser.id
  }
}

function assertLightweightSystemAccountOption(account: SystemAccountOptionSummary): void {
  for (const field of ['role', 'description', 'mustChangePassword', 'lastLoginAt', 'createdAt', 'updatedAt', 'passwordHash'] as const) {
    assert.equal(Object.prototype.hasOwnProperty.call(account, field), false, `系统账户选项不应返回 ${field}`)
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

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function assertJsonStatus(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'POST' | 'PATCH',
  body: unknown,
  expectedStatus: number,
  message: string
): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  assert.equal(response.status, expectedStatus, `${message}，实际状态 ${response.status}: ${await response.text()}`)
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
