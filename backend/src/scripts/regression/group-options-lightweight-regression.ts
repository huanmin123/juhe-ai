import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-options-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const blockedRecordDatabasePath = join(tempRoot, 'records-as-directory.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = blockedRecordDatabasePath
runtimeConfig.secret = 'group-options-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(blockedRecordDatabasePath, { recursive: true })
logger.level = 'silent'

const [
  { groupsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/__aisys__/api/groups', requireAdmin, groupsRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface GroupOptionSummary {
  id: string
  name: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  permissions?: {
    canAuthorize?: boolean
  }
  accountIds?: string[]
  accountStats?: unknown
}

interface SeedState {
  adminCookie: string
  userCookie: string
  userAccountId: string
  userGroupId: string
  userId: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const seed = seedData()
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('分组选项轻量回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const adminOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}`, seed.adminCookie)
  assert.equal(adminOptions.every((group) => group.ownerSystemAccountId === seed.userId), true, '管理员按系统账户筛选分组选项时不应混入其他用户分组')
  const adminTargetOption = adminOptions.find((group) => group.id === seed.userGroupId)
  assert(adminTargetOption, '分组选项应包含目标分组 ID')
  assert.equal(adminTargetOption.systemAccountId, seed.userId, '管理侧分组选项应保留系统账户归属字段')
  assert.equal(adminTargetOption.permissions?.canAuthorize, true, '自有分组选项应保留可授权权限')
  assertLightweightGroupOption(adminTargetOption)

  const userOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/my-groups/options?systemAccountId=sys_admin`, seed.userCookie)
  assert.equal(userOptions.some((group) => group.id === seed.userGroupId), true, '用户侧分组选项应包含当前用户分组')
  assert.equal(userOptions.every((group) => group.ownerSystemAccountId === seed.userId), true, '用户侧分组选项必须固定当前用户作用域，不能被查询参数改写')
  const userTargetOption = userOptions.find((group) => group.id === seed.userGroupId)
  assert(userTargetOption, '用户侧分组选项应包含目标分组')
  assert.equal(userTargetOption.systemAccountId, undefined, '用户侧分组选项不应暴露管理侧系统账户字段')
  assertLightweightGroupOption(userTargetOption)

  const accountOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, '/__aisys__/api/my-groups/account-options', seed.userCookie)
  const accountTargetOption = accountOptions.find((group) => group.id === seed.userGroupId)
  assert(accountTargetOption, '账户页分组选项应包含目标分组')
  assert.deepEqual(accountTargetOption.accountIds, [seed.userAccountId], '账户页分组选项应返回账号到分组映射所需 accountIds')
  assert.equal(Object.prototype.hasOwnProperty.call(accountTargetOption, 'accountStats'), false, '账户页分组选项不应返回 accountStats')

  console.log('分组选项轻量回归通过：options/account-options 接口不读取记录库统计，也不返回完整分组摘要字段')
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
    username: 'group_options_user',
    displayName: '分组选项用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userGroup = repositories.createGroup({
    name: '分组选项种子',
    providerCode: 'openai',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const userAccountId = 'acc_group_options_lightweight'
  const now = new Date().toISOString()
  databaseModule.getDatabase()
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
    .run(userAccountId, user.id, '分组选项账户种子', now, now)
  databaseModule.getDatabase()
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, weight, enabled, created_at, updated_at)
      VALUES (?, ?, ?, NULL, 1, 1, ?, ?)
    `)
    .run(user.id, userGroup.id, userAccountId, now, now)
  return {
    adminCookie: sessionCookie(admin.id),
    userCookie: sessionCookie(user.id),
    userAccountId,
    userGroupId: userGroup.id,
    userId: user.id
  }
}

function assertLightweightGroupOption(group: GroupOptionSummary | undefined): void {
  assert(group, '分组选项不能为空')
  assert.equal(Object.prototype.hasOwnProperty.call(group, 'accountIds'), false, '分组选项不应返回 accountIds')
  assert.equal(Object.prototype.hasOwnProperty.call(group, 'accountStats'), false, '分组选项不应返回 accountStats')
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
