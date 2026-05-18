import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-options-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const blockedRecordDatabasePath = join(tempRoot, 'records-as-directory.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = blockedRecordDatabasePath
runtimeConfig.secret = 'account-options-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(blockedRecordDatabasePath, { recursive: true })
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

interface AccountOptionSummary {
  id: string
  name: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  permissions?: {
    canAuthorize?: boolean
  }
  credentials?: unknown
  currentConcurrency?: unknown
  todayUsage?: unknown
  usage?: unknown
  qualityScore?: unknown
}

interface SeedState {
  adminCookie: string
  firstUserAccountId: string
  maxLimitAccountId: string
  userCookie: string
  userId: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const seed = seedData()
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('账户选项轻量回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const adminOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&limit=20`, seed.adminCookie)
  assert.equal(adminOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '管理员按系统账户筛选账户选项时不应混入其他用户账户')
  assert.equal(adminOptions.length, 20, '账户选项应遵守 limit 查询参数')
  const adminTargetOption = adminOptions.find((account) => account.id === seed.firstUserAccountId)
  assert(adminTargetOption, '账户选项应包含目标账户')
  assert.equal(adminTargetOption.systemAccountId, seed.userId, '管理侧账户选项应保留系统账户归属字段')
  assert.equal(adminTargetOption.permissions?.canAuthorize, true, '自有账户选项应保留可授权权限')
  assertLightweightAccountOption(adminTargetOption)

  const expandedAdminOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&limit=500`, seed.adminCookie)
  assert.equal(expandedAdminOptions.length, 500, '账户选项应允许调用方把 limit 提升到前端资源筛选需要的 500')
  assert(expandedAdminOptions.some((account) => account.id === seed.maxLimitAccountId), '账户选项 limit 提升后应能返回普通列表上限 200 之后的账号')
  assert.equal(expandedAdminOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '扩展 limit 后仍不应混入其他用户账户')

  const sortedAdminOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&sorts=qualityScore:desc&limit=1`, seed.adminCookie)
  assert.equal(sortedAdminOptions.length, 1, '账户选项应忽略重型排序请求并继续遵守 limit')
  assert.equal(sortedAdminOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '账户选项不应因重型排序请求混入其他账户')
  assertLightweightAccountOption(sortedAdminOptions[0])

  const userOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/my-accounts/options?systemAccountId=sys_admin&limit=20`, seed.userCookie)
  assert.equal(userOptions.length, 20, '用户侧账户选项也应遵守 limit 查询参数')
  const userTargetOption = userOptions.find((account) => account.id === seed.firstUserAccountId)
  assert(userTargetOption, '用户侧账户选项应包含当前用户账户')
  assert.equal(userOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '用户侧账户选项必须固定当前用户作用域，不能被查询参数改写')
  assert.equal(userTargetOption.systemAccountId, undefined, '用户侧账户选项不应暴露管理侧系统账户字段')
  assertLightweightAccountOption(userTargetOption)
  const repositorySortedOptions = repositories.listAccountOptions(
    { systemAccountId: seed.userId, role: 'admin', systemAccountFilterId: seed.userId },
    { sorts: [{ field: 'qualityScore', order: 'desc' }], limit: 1 }
  )
  assert.equal(repositorySortedOptions.length, 1, '账户选项 repository 层应忽略重型质量分排序并继续遵守 limit')
  assert.equal(repositorySortedOptions[0]?.ownerSystemAccountId, seed.userId, 'repository 层账户选项不应因重型排序请求混入其他账户')
  assertLightweightAccountOption(repositorySortedOptions[0])

  console.log('账户选项轻量回归通过：options 接口不读取记录库统计，也不返回完整账户摘要字段')
} finally {
  await closeServer(server)
  try {
    databaseModule.getDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const user = repositories.createSystemAccount({
    username: 'account_options_user',
    displayName: '账户选项用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const now = new Date().toISOString()
  const insertAccount = databaseModule.getDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, name, notes, type, status, credential_mask, credentials_encrypted,
        proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id, priority, super_priority_enabled,
        fallback_enabled, schedulable, account_expires_at, last_used_at, cooldown_until, last_error_code,
        last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
      ) VALUES (?, ?, 'openai', ?, NULL, 'api_key', 'active', 'sk-***', '{}',
        NULL, 20, 1, NULL, 10, 0,
        0, 1, NULL, NULL, NULL, NULL,
        NULL, 0, NULL, ?, ?)
    `)
  const userAccountIds: string[] = []
  for (let index = 0; index < 525; index += 1) {
    const accountId = `acc_account_options_lightweight_${String(index).padStart(3, '0')}`
    userAccountIds.push(accountId)
    insertAccount.run(accountId, user.id, `账户选项种子 ${String(index).padStart(3, '0')}`, now, now)
  }
  return {
    adminCookie: sessionCookie(admin.id),
    firstUserAccountId: userAccountIds[0],
    maxLimitAccountId: userAccountIds[499],
    userCookie: sessionCookie(user.id),
    userId: user.id
  }
}

function assertLightweightAccountOption(account: AccountOptionSummary | undefined): void {
  assert(account, '账户选项不能为空')
  for (const field of ['credentials', 'currentConcurrency', 'todayUsage', 'usage', 'qualityScore'] as const) {
    assert.equal(Object.prototype.hasOwnProperty.call(account, field), false, `账户选项不应返回 ${field}`)
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
