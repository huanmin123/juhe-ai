import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-options-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const blockedDatasetDatabasePath = join(tempRoot, 'dataset-as-directory.sqlite3')
const blockedStatsDatabasePath = join(tempRoot, 'stats-as-directory.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = blockedDatasetDatabasePath
runtimeConfig.statsDatabasePath = blockedStatsDatabasePath
runtimeConfig.secret = 'account-options-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(blockedDatasetDatabasePath, { recursive: true })
mkdirSync(blockedStatsDatabasePath, { recursive: true })
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
  groupMatchedAccountId: string
  matchedAccountId: string
  matchedPrefixAccountId: string
  maxLimitAccountId: string
  middleAccountId: string
  notesAccountId: string
  userCookie: string
  userId: string
  wildcardAccountId: string
  wildcardNeighborAccountId: string
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
  assert.equal(expandedAdminOptions.length, 50, '账户选项必须把调用方传入的超大 limit 压到 50')
  assert.equal(expandedAdminOptions.some((account) => account.id === seed.maxLimitAccountId), false, '账户选项不应因为超大 limit 一次性返回远端候选')
  assert.equal(expandedAdminOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '压缩超大 limit 后仍不应混入其他用户账户')

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

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/^\s*SELECT\b/i.test(sql) && /\baccount_option_rows\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare
  try {
    const keyword = encodeURIComponent('账户选项检索目标')
    const keywordOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&keyword=${keyword}&limit=20`, seed.adminCookie)
    const keywordIds = keywordOptions.map((account) => account.id)
    assert(keywordIds.includes(seed.matchedAccountId), '账户选项关键词应命中账户名称精确值')
    assert(keywordIds.includes(seed.matchedPrefixAccountId), '账户选项关键词应命中账户名称前缀值')
    assert(!keywordIds.includes(seed.middleAccountId), '账户选项关键词不应命中账户名称中间包含值')
    assert.equal(keywordOptions.every((account) => account.ownerSystemAccountId === seed.userId), true, '账户选项关键词查询不应混入其他用户账户')

    const groupKeywordOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&keyword=${encodeURIComponent('账户选项绑定分组')}&limit=20`, seed.adminCookie)
    assert(!groupKeywordOptions.some((account) => account.id === seed.groupMatchedAccountId), '账户选项关键词不应通过绑定分组名称命中')

    const groupFilterOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&groupId=grp_account_options_keyword_match&limit=20`, seed.adminCookie)
    assert(groupFilterOptions.some((account) => account.id === seed.groupMatchedAccountId), '账户选项应支持独立 groupId 筛选')

    const notesOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&keyword=${encodeURIComponent('账户选项备注前缀')}&limit=20`, seed.adminCookie)
    assert(!notesOptions.some((account) => account.id === seed.notesAccountId), '账户选项关键词不应通过 notes 长文本命中')

    const wildcardOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&keyword=${encodeURIComponent('percent%')}&limit=20`, seed.adminCookie)
    const wildcardIds = wildcardOptions.map((account) => account.id)
    assert(wildcardIds.includes(seed.wildcardAccountId), '账户选项关键词应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(seed.wildcardNeighborAccountId), '账户选项关键词不应把用户输入的 % 当作 LIKE 通配符')

    const limitedKeywordOptions = await getEnvelope<AccountOptionSummary[]>(baseUrl, `/__aisys__/api/accounts/options?systemAccountId=${seed.userId}&keyword=${keyword}&limit=1`, seed.adminCookie)
    assert.equal(limitedKeywordOptions.length, 1, '账户选项关键词查询应遵守 limit')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 5, '回归应捕获账户 options SQL')
  for (const call of capturedCalls) {
    assert(/\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(call.sql), '账户 options SQL 必须下推 LIMIT/OFFSET')
    assert(!/\bcredentials_encrypted\b/i.test(call.sql), '账户 options SQL 不应读取 credentials_encrypted')
    assert(!/\baccounts\.notes\b/i.test(call.sql), '账户 options SQL 不应读取或搜索 notes 长文本')
    assert(!/\baccount_rows\b/i.test(call.sql), '账户 options SQL 不应复用完整账户列表 account_rows 大查询')
    assert(!/\baccount_quality_scores\b/i.test(call.sql), '账户 options SQL 不应接入统计结果库质量分')
    assert(!/\bCOALESCE\s*\(/i.test(call.sql), '账户 options 关键词不应通过 COALESCE 扫描字段')
    assert(!/\baccounts\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql), '账户 options 关键词不应把账户 ID 放进 WHERE')
    assert(!/\baccounts\.provider_code\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '账户 options 关键词不应把供应商编码放进 WHERE')
    assert(!/\baccounts\.type\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '账户 options 关键词不应把账户类型放进 WHERE')
    assert(!/\boption_groups\.name\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '账户 options 关键词不应把分组名称放进 WHERE')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '账户 options 关键词不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), '账户 options 前缀搜索应显式转义 LIKE 通配符')
    }
  }
  assertBusinessIndexExists('idx_accounts_system_account_name_lookup')
  assertBusinessIndexExists('idx_accounts_system_account_provider_lookup')
  assertBusinessIndexExists('idx_accounts_system_account_type_lookup')
  assertBusinessIndexExists('idx_group_accounts_account_scope_enabled')
  assertBusinessIndexExists('idx_groups_system_account_name_lookup')

  console.log('账户选项轻量回归通过：options 接口不读取统计结果库质量统计，不返回完整账户摘要字段，关键词仅按账户名称匹配，分组使用独立筛选')
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
  const keywordCreatedAt = new Date(Date.parse(now) + 1000).toISOString()
  const matchedAccountId = 'zzz_account_options_keyword_exact'
  const matchedPrefixAccountId = 'zzz_account_options_keyword_prefix'
  const middleAccountId = 'zzz_account_options_keyword_middle'
  const notesAccountId = 'zzz_account_options_notes_only'
  const wildcardAccountId = 'zzz_account_options_wildcard_literal'
  const wildcardNeighborAccountId = 'zzz_account_options_wildcard_neighbor'
  const groupMatchedAccountId = 'zzz_account_options_group_match'
  insertAccount.run(matchedAccountId, user.id, '账户选项检索目标', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(matchedPrefixAccountId, user.id, '账户选项检索目标扩展', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(middleAccountId, user.id, '普通账户选项检索目标', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(notesAccountId, user.id, '备注字段账户选项', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(wildcardAccountId, user.id, 'percent%literal 账户选项', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(wildcardNeighborAccountId, user.id, 'percentXliteral 账户选项', keywordCreatedAt, keywordCreatedAt)
  insertAccount.run(groupMatchedAccountId, user.id, '分组搜索账户选项', keywordCreatedAt, keywordCreatedAt)
  const database = databaseModule.getDatabase()
  database
    .prepare('UPDATE accounts SET notes = ? WHERE id = ?')
    .run('账户选项备注前缀', notesAccountId)
  const groupId = 'grp_account_options_keyword_match'
  database
    .prepare(`
      INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
      VALUES (?, ?, ?, 'openai', NULL, 1, 0, ?, ?)
    `)
    .run(groupId, user.id, '账户选项绑定分组', keywordCreatedAt, keywordCreatedAt)
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, NULL, 1, ?, ?)
    `)
    .run(user.id, groupId, groupMatchedAccountId, keywordCreatedAt, keywordCreatedAt)
  return {
    adminCookie: sessionCookie(admin.id),
    firstUserAccountId: userAccountIds[0],
    groupMatchedAccountId,
    matchedAccountId,
    matchedPrefixAccountId,
    maxLimitAccountId: userAccountIds[499],
    middleAccountId,
    notesAccountId,
    userCookie: sessionCookie(user.id),
    userId: user.id,
    wildcardAccountId,
    wildcardNeighborAccountId
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

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
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
