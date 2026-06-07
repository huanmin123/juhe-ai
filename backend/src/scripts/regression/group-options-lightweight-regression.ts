import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-options-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const blockedDatasetDatabasePath = join(tempRoot, 'dataset-as-directory.sqlite3')
const blockedStatsDatabasePath = join(tempRoot, 'stats-as-directory.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = blockedDatasetDatabasePath
runtimeConfig.statsDatabasePath = blockedStatsDatabasePath
runtimeConfig.secret = 'group-options-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(blockedDatasetDatabasePath, { recursive: true })
mkdirSync(blockedStatsDatabasePath, { recursive: true })
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
  providerCode: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  permissions?: {
    canAuthorize?: boolean
    canBindToApiKey?: boolean
  }
  accountIds?: string[]
  accountStats?: unknown
}

interface SeedState {
  adminCookie: string
  adminAuthorizedAccountId: string
  adminAuthorizedGroupId: string
  matchedGroupId: string
  matchedPrefixGroupId: string
  middleGroupId: string
  otherProviderGroupId: string
  userDefaultGroupId: string
  wildcardGroupId: string
  wildcardNeighborGroupId: string
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
  const adminTargetOption = adminOptions.find((group) => group.id === seed.userGroupId)
  assert(adminTargetOption, '分组选项应包含目标分组 ID')
  assert.equal(adminTargetOption.systemAccountId, seed.userId, '管理侧分组选项应保留系统账户归属字段')
  assert.equal(adminTargetOption.permissions?.canAuthorize, true, '自有分组选项应保留可授权权限')
  assertLightweightGroupOption(adminTargetOption)
  const adminAuthorizedOption = adminOptions.find((group) => group.id === seed.adminAuthorizedGroupId)
  assert(adminAuthorizedOption, '普通分组选项仍应包含用户可见的授权分组')
  assert.equal(adminAuthorizedOption.permissions?.canBindToApiKey, true, '有效授权分组选项应标记为可绑定 API Key')

  const userOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/my-groups/options?systemAccountId=sys_admin`, seed.userCookie)
  assert.equal(userOptions.some((group) => group.id === seed.userGroupId), true, '用户侧分组选项应包含当前用户分组')
  assert.equal(userOptions.every((group) => group.ownerSystemAccountId === seed.userId || group.id === seed.adminAuthorizedGroupId), true, '用户侧分组选项必须固定当前用户可见作用域，不能被查询参数改写')
  const userTargetOption = userOptions.find((group) => group.id === seed.userGroupId)
  assert(userTargetOption, '用户侧分组选项应包含目标分组')
  assert.equal(userTargetOption.systemAccountId, undefined, '用户侧分组选项不应暴露管理侧系统账户字段')
  assertLightweightGroupOption(userTargetOption)
  const userAuthorizedOption = userOptions.find((group) => group.id === seed.adminAuthorizedGroupId)
  assert(userAuthorizedOption, '用户侧普通分组选项应保留已授权给当前用户的分组')
  assert.equal(userAuthorizedOption.permissions?.canBindToApiKey, true, '用户侧有效授权分组选项应标记为可绑定 API Key')

  const accountOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, '/__aisys__/api/my-groups/account-options', seed.userCookie)
  const accountTargetOption = accountOptions.find((group) => group.id === seed.userGroupId)
  assert(accountTargetOption, '账户页分组选项应包含目标分组')
  assert.deepEqual(accountTargetOption.accountIds, [seed.userAccountId], '账户页分组选项应返回账号到分组映射所需 accountIds')
  const authorizedAccountOption = accountOptions.find((group) => group.id === seed.adminAuthorizedGroupId)
  assert(authorizedAccountOption, '账户页分组选项应保留已授权分组摘要')
  const authorizedAccountIds: string[] = authorizedAccountOption.accountIds ?? []
  assert.equal(authorizedAccountIds.includes(seed.adminAuthorizedAccountId), false, '账户页分组选项不应包含授权方账号 ID')
  assert.deepEqual(authorizedAccountIds, [], '账户页分组选项不应暴露授权方分组成员账号 ID')
  assert.equal(Object.prototype.hasOwnProperty.call(accountTargetOption, 'accountStats'), false, '账户页分组选项不应返回 accountStats')

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+groups\b/i.test(sql) && /\bORDER\s+BY\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare
  try {
    const defaultCallStart = capturedCalls.length
    const defaultWindowedOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}`, seed.adminCookie)
    assert(defaultWindowedOptions.length > 0, '分组选项未传 limit 时仍应返回默认窗口候选')
    const defaultCalls = capturedCalls.slice(defaultCallStart)
    assert(defaultCalls.some((call) => /\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(call.sql) && call.params.at(-2) === 50 && call.params.at(-1) === 0), '分组选项未传 limit 时也应使用默认窗口上限')

    const keyword = encodeURIComponent('检索目标分组')
    const keywordOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}&keyword=${keyword}&limit=20`, seed.adminCookie)
    const keywordIds = keywordOptions.map((group) => group.id)
    assert(keywordIds.includes(seed.matchedGroupId), '分组选项关键词应命中名称精确值')
    assert(keywordIds.includes(seed.matchedPrefixGroupId), '分组选项关键词应命中名称前缀值')
    assert(!keywordIds.includes(seed.middleGroupId), '分组选项关键词不应命中名称中间包含值')

    const wildcardOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}&keyword=${encodeURIComponent('percent%')}&limit=20`, seed.adminCookie)
    const wildcardIds = wildcardOptions.map((group) => group.id)
    assert(wildcardIds.includes(seed.wildcardGroupId), '分组选项关键词应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(seed.wildcardNeighborGroupId), '分组选项关键词不应把用户输入的 % 当作 LIKE 通配符')

    const limitedOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}&keyword=${keyword}&limit=1`, seed.adminCookie)
    assert.equal(limitedOptions.length, 1, '分组选项关键词查询应遵守 limit')

    const providerOptions = await getEnvelope<GroupOptionSummary[]>(baseUrl, `/__aisys__/api/groups/options?systemAccountId=${seed.userId}&providerCode=gpt&manageableOnly=true&preferDefault=true&limit=20`, seed.adminCookie)
    assert(providerOptions.length > 0, '供应商分组选项应返回当前用户可管理分组')
    assert.equal(providerOptions[0]?.id, seed.userDefaultGroupId, 'preferDefault 应让默认分组排在首位')
    assert.equal(providerOptions.every((group) => group.providerCode === 'gpt'), true, '供应商分组选项必须按 providerCode 精确过滤')
    assert(!providerOptions.some((group) => group.id === seed.otherProviderGroupId), '供应商分组选项不应混入其他供应商分组')
    assert(!providerOptions.some((group) => group.id === seed.adminAuthorizedGroupId), 'manageableOnly 应排除被授权分组，账户绑定下拉只能展示自有可管理分组')
  } finally {
    database.prepare = originalPrepare
  }
  assert(capturedCalls.length >= 3, '回归应捕获分组选项 SQL')
  for (const call of capturedCalls) {
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '分组选项搜索不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), '分组选项前缀搜索应显式转义 LIKE 通配符')
    }
  }
  assertBusinessIndexExists('idx_groups_name_lookup')
  assertBusinessIndexExists('idx_groups_system_account_name_lookup')
  assertBusinessIndexExists('idx_groups_provider_name_lookup')
  assertBusinessIndexExists('idx_groups_system_account_provider_name_lookup')

  console.log('分组选项轻量回归通过：options/account-options 接口不读取统计结果库统计，关键词仅支持精确/前缀匹配，账户表单分组候选按供应商和可管理范围小窗口返回')
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
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const userDefaultGroup = repositories.createGroup({
    name: '默认优先分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE groups SET is_default = CASE WHEN id = ? THEN 1 ELSE 0 END WHERE system_account_id = ? AND provider_code = ?')
    .run(userDefaultGroup.id, user.id, 'gpt')
  const matchedGroup = repositories.createGroup({
    name: '检索目标分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const matchedPrefixGroup = repositories.createGroup({
    name: '检索目标分组扩展',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const middleGroup = repositories.createGroup({
    name: '普通检索目标分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const wildcardGroup = repositories.createGroup({
    name: 'percent%literal 分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const wildcardNeighborGroup = repositories.createGroup({
    name: 'percentXliteral 分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  seedProvider('anthropic')
  const otherProviderGroup = repositories.createGroup({
    name: '检索目标分组 Anthropic',
    providerCode: 'anthropic',
    enabled: true
  }, { systemAccountId: user.id, role: 'user' as const })
  const adminAuthorizedGroup = repositories.createGroup({
    name: '检索目标授权分组',
    providerCode: 'gpt',
    enabled: true
  }, { systemAccountId: admin.id, role: 'admin' as const })
  seedActiveGroupAuthorization(adminAuthorizedGroup.id, admin.id, user.id)
  const adminAuthorizedAccountId = 'acc_group_options_authorized_owner'
  const userAccountId = 'acc_group_options_lightweight'
  const now = new Date().toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, notes, type, status, credential_mask, credentials_encrypted,
        proxy_profile_id, concurrency_limit, error_policy_id, priority, super_priority_enabled,
        fallback_enabled, schedulable, account_expires_at, last_used_at, cooldown_until, last_error_code,
        last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
      ) VALUES (?, ?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, NULL, 'api_key', 'active', 'sk-***', '{}',
        NULL, 20, NULL, 10, 0,
        0, 1, NULL, NULL, NULL, NULL,
        NULL, 0, NULL, ?, ?)
    `)
    .run(userAccountId, user.id, '分组选项账户种子', now, now)
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, notes, type, status, credential_mask, credentials_encrypted,
        proxy_profile_id, concurrency_limit, error_policy_id, priority, super_priority_enabled,
        fallback_enabled, schedulable, account_expires_at, last_used_at, cooldown_until, last_error_code,
        last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
      ) VALUES (?, ?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, NULL, 'api_key', 'active', 'sk-***', '{}',
        NULL, 20, NULL, 10, 0,
        0, 1, NULL, NULL, NULL, NULL,
        NULL, 0, NULL, ?, ?)
    `)
    .run(adminAuthorizedAccountId, admin.id, '授权方分组选项账户种子', now, now)
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, NULL, 1, ?, ?)
    `)
    .run(user.id, userGroup.id, userAccountId, now, now)
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, NULL, 1, ?, ?)
    `)
    .run(admin.id, adminAuthorizedGroup.id, adminAuthorizedAccountId, now, now)
  return {
    adminCookie: sessionCookie(admin.id),
    adminAuthorizedAccountId,
    adminAuthorizedGroupId: adminAuthorizedGroup.id,
    matchedGroupId: matchedGroup.id,
    matchedPrefixGroupId: matchedPrefixGroup.id,
    middleGroupId: middleGroup.id,
    otherProviderGroupId: otherProviderGroup.id,
    userDefaultGroupId: userDefaultGroup.id,
    wildcardGroupId: wildcardGroup.id,
    wildcardNeighborGroupId: wildcardNeighborGroup.id,
    userCookie: sessionCookie(user.id),
    userAccountId,
    userGroupId: userGroup.id,
    userId: user.id
  }
}

function seedActiveGroupAuthorization(resourceId: string, ownerSystemAccountId: string, granteeSystemAccountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO resource_authorizations (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
        scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
        revoked_reason, updated_at
      ) VALUES (?, 'group', ?, ?, ?, 'use', 'active', 'manual', NULL, ?, ?, NULL, NULL, NULL, ?, ?, NULL, NULL, NULL, ?)
    `)
    .run(`ra_group_options_${resourceId}`, resourceId, ownerSystemAccountId, granteeSystemAccountId, now, now, ownerSystemAccountId, now, now)
}

function seedProvider(code: string): void {
  const now = new Date().toISOString()
  const database = databaseModule.getBusinessDatabase()
  const profileId = `profile_${code}_openai_v1`
  database
    .prepare(`
      INSERT OR IGNORE INTO providers (
        id, code, name, description, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?, ?)
    `)
    .run(
      `provider_${code}`,
      code,
      code,
      `${code} provider`,
      now,
      now
    )
  database
    .prepare(`
      INSERT OR IGNORE INTO provider_protocol_profiles (
        id, provider_code, name, description, enabled, protocol_code, protocol_version,
        base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      profileId,
      code,
      `${code} / OpenAI v1`,
      `${code} OpenAI v1 profile`,
      OPENAI_PROTOCOL_CODE,
      OPENAI_PROTOCOL_VERSION,
      'https://example.invalid/v1',
      `${code}-test-model`,
      JSON.stringify(['api_key']),
      JSON.stringify(['chat']),
      now,
      now
    )
  const familyStatement = database.prepare(`
    INSERT OR IGNORE INTO provider_protocol_profile_families (
      profile_id, family_code, enabled, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, 1, '[]', ?, ?)
  `)
  familyStatement.run(profileId, 'chat_completions', now, now)
  familyStatement.run(profileId, 'responses', now, now)
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

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
