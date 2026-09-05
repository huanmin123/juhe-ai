import assert from 'node:assert/strict'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { convertQuestionPlaceholdersToPostgres } from '../../storage/database-client.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'group-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, cacheInvalidation] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/gateway-cache-invalidation.js')
])

const database = databaseModule.getBusinessDatabase()
const patchTargetProviderCode = 'group_patch_target'
const disabledPatchTargetProviderCode = 'group_patch_target_disabled'
const providerCreatedAt = new Date().toISOString()
database.prepare(`
  INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
  VALUES (?, ?, ?, 1, '[]', ?, ?)
`).run('prov_group_patch_target', patchTargetProviderCode, '分组 PATCH 目标供应商', providerCreatedAt, providerCreatedAt)
database.prepare(`
  INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
  VALUES (?, ?, ?, 0, '[]', ?, ?)
`).run('prov_group_patch_target_disabled', disabledPatchTargetProviderCode, '分组 PATCH 停用供应商', providerCreatedAt, providerCreatedAt)
const owner = repositories.createSystemAccount({
  username: 'group_demand_write_owner',
  displayName: '分组按需写所有者',
  password: 'password',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
const grantee = repositories.createSystemAccount({
  username: 'group_demand_write_grantee',
  displayName: '分组按需写被授权人',
  password: 'password',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
const outsider = repositories.createSystemAccount({
  username: 'group_demand_write_outsider',
  displayName: '分组按需写无关用户',
  password: 'password',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
const outsiderAccess = { systemAccountId: outsider.id, role: 'user' as const }
const originalPolicy = writablePolicy({
  defaultSoftConcurrency: 9,
  maxQueueWaitMs: 45_000,
  clientIpConcurrencyLimit: 3,
  clientIpConcurrencyOverflowMode: 'queue',
  imageLaneMaxConcurrency: 1
})
const group = repositories.createGroup({
  name: '分组按需写回归',
  providerCode: 'gpt',
  description: '原说明',
  enabled: true,
  groupType: 'high_concurrency',
  schedulingPolicy: originalPolicy
}, ownerAccess)
repositories.createResourceAuthorization({
  resourceType: 'group',
  resourceId: group.id,
  granteeType: 'system_account',
  granteeId: grantee.id,
  remark: '分组按需写回归授权'
}, ownerAccess)

const invalidations: string[] = []
const unregisterInvalidator = cacheInvalidation.registerGatewayRuntimeCacheInvalidator((reason) => {
  invalidations.push(reason)
})

try {
  const originalStoredPolicyJson = groupPolicyJson(group.id)
  const initialUpdatedAt = storedGroup().updated_at
  invalidations.length = 0
  const descriptionPatch = await captureSql(() => repositories.patchGroupAsync(group.id, {
    description: '新说明',
    expectedUpdatedAt: initialUpdatedAt
  }, ownerAccess))
  assert.deepEqual(descriptionPatch.result?.changedFields, ['description'])
  const descriptionReadSql = selectSql(descriptionPatch.calls)
  assert.match(descriptionReadSql, /groups\.description/i, '说明 PATCH 必须读取当前说明')
  assert.doesNotMatch(descriptionReadSql, /groups\.(?:provider_code|enabled|group_type|scheduling_policy_json)/i, '说明 PATCH 不得读取未提交的供应商、状态或调度策略')
  assert.deepEqual(dmlSql(descriptionPatch.calls), [expectSql(/UPDATE\s+"?groups"?\s+SET\s+"?description"?\s*=\s*\?,\s*updated_at\s*=\s*\?/i, descriptionPatch.calls)], '说明 PATCH 只能更新说明和更新时间')
  assert.match(dmlSql(descriptionPatch.calls)[0] ?? '', /WHERE\s+id\s*=\s*\?\s+AND\s+system_account_id\s*=\s*\?\s+AND\s+updated_at\s*=\s*\?/i, 'owner 与版本条件必须同时进入 UPDATE SQL')
  const descriptionDmlCall = descriptionPatch.calls.find((call) => call.kind === 'run' && /UPDATE\s+"?groups"?/i.test(call.sql))
  assert(descriptionDmlCall)
  const postgresDescriptionSql = convertQuestionPlaceholdersToPostgres(descriptionDmlCall.sql)
  assert.match(postgresDescriptionSql, /SET\s+"?description"?\s*=\s*\$1,\s*updated_at\s*=\s*\$2[\s\S]*WHERE\s+id\s*=\s*\$3\s+AND\s+system_account_id\s*=\s*\$4\s+AND\s+updated_at\s*=\s*\$5/i, 'PostgreSQL owner PATCH 参数顺序必须保持字段、新版本、ID、owner、旧版本')
  assert.equal(descriptionDmlCall.params.length, 5, 'PostgreSQL 绑定参数数量必须与 CAS SQL 占位符一致')
  assert.equal(descriptionPatch.result?.updatedAt, storedGroup().updated_at, 'PATCH 必须返回实际保存的新版本')
  assert.ok((descriptionPatch.result?.updatedAt ?? '') > initialUpdatedAt, '实际写入必须单调推进版本')
  assert.equal(storedGroup().scheduling_policy_json, originalStoredPolicyJson, '说明 PATCH 不得覆盖高并发调度策略')
  assert.deepEqual(invalidations, [], '说明不参与网关运行态或名称 lookup，不得扩大缓存失效范围')

  const updatedAt = storedGroup().updated_at
  invalidations.length = 0
  const noChange = await captureSql(() => repositories.patchGroupAsync(group.id, {
    description: '新说明',
    expectedUpdatedAt: updatedAt
  }, ownerAccess))
  assert.deepEqual(noChange.result?.changedFields, [], '同值 PATCH 必须返回空变化字段')
  assert.deepEqual(dmlSql(noChange.calls), [], '同值 PATCH 不得执行 DML')
  assert.equal(storedGroup().updated_at, updatedAt, '同值 PATCH 不得推进 updated_at')
  assert.deepEqual(invalidations, [], '同值 PATCH 不得失效网关缓存')

  const stalePatch = await captureSqlOutcome(() => repositories.patchGroupAsync(group.id, {
    description: '过期版本不得覆盖',
    expectedUpdatedAt: initialUpdatedAt
  }, ownerAccess))
  assert.equal((stalePatch.error as Error | undefined)?.name, 'GroupPatchConflictError')
  assert.deepEqual(dmlSql(stalePatch.calls), [], '过期版本必须在任何 DML 前拒绝')

  const crossOwner = await captureSql(() => repositories.patchGroupAsync(group.id, { description: '越权修改' }, outsiderAccess))
  assert.equal(crossOwner.result, undefined, '无授权的其他用户不得定位分组')
  assert.match(selectSql(crossOwner.calls), /groups\.system_account_id\s*=\s*\?/i, 'owner 作用域必须下推到分组定位 SQL')
  assert.deepEqual(dmlSql(crossOwner.calls), [], '跨 owner 请求不得执行 DML')

  const sameProvider = await captureSql(() => repositories.patchGroupAsync(group.id, { providerCode: 'gpt' }, ownerAccess))
  assert.deepEqual(sameProvider.result?.changedFields, [])
  assert.doesNotMatch(selectSql(sameProvider.calls), /\bgroup_accounts\b/i, '同值供应商 PATCH 不得查询账户绑定')
  assert.doesNotMatch(selectSql(sameProvider.calls), /\bFROM\s+"?providers"?\b/i, '同值供应商 PATCH 不得查询供应商')
  assert.deepEqual(dmlSql(sameProvider.calls), [])

  const forbiddenAuthorizedFields = await captureSqlOutcome(() => repositories.patchGroupAsync(group.id, {
    name: '禁止修改的名称',
    providerCode: 'anthropic',
    description: '禁止修改的说明'
  }, granteeAccess))
  assert.match(String(forbiddenAuthorizedFields.error), /授权分组使用配置包含未知字段/)
  assert.doesNotMatch(selectSql(forbiddenAuthorizedFields.calls), /\bFROM\s+"?providers"?\b/i, '授权分组禁用字段必须在供应商查询前拒绝')
  assert.deepEqual(dmlSql(forbiddenAuthorizedFields.calls), [], '授权分组禁止字段必须在 DML 前拒绝')

  const providerChangeGroup = repositories.createGroup({
    name: '分组供应商窄查询回归',
    providerCode: 'gpt',
    groupType: 'personal'
  }, ownerAccess)
  invalidations.length = 0
  const missingProviderChange = await captureSqlOutcome(() => repositories.patchGroupAsync(providerChangeGroup.id, { providerCode: 'group_patch_target_missing' }, ownerAccess))
  assert.match(String(missingProviderChange.error), /不支持的供应商/)
  assert.deepEqual(dmlSql(missingProviderChange.calls), [], '不存在的供应商必须在 DML 前拒绝')
  const disabledProviderChange = await captureSqlOutcome(() => repositories.patchGroupAsync(providerChangeGroup.id, { providerCode: disabledPatchTargetProviderCode }, ownerAccess))
  assert.match(String(disabledProviderChange.error), /供应商已停用/)
  assert.deepEqual(dmlSql(disabledProviderChange.calls), [], '已停用供应商必须在 DML 前拒绝')
  const providerChange = await captureSql(() => repositories.patchGroupAsync(providerChangeGroup.id, { providerCode: patchTargetProviderCode }, ownerAccess))
  assert.deepEqual(providerChange.result?.changedFields, ['providerCode'])
  const providerChangeSelectSql = selectSql(providerChange.calls)
  assert.match(providerChangeSelectSql, /SELECT\s+enabled\s+FROM\s+"?providers"?\s+WHERE\s+code\s*=\s*\?/i, '真实供应商变化只能按 code 查询 enabled')
  assert.doesNotMatch(providerChangeSelectSql, /provider_protocol_profiles|default_supported_models_json/i, '供应商变化不得装配完整定义或协议档案')
  assert.deepEqual(invalidations, ['group_updated'])

  invalidations.length = 0
  const policyPatch = await captureSql(() => repositories.patchGroupAsync(group.id, {
    schedulingPolicy: { ...originalPolicy, defaultSoftConcurrency: 12 }
  }, ownerAccess))
  assert.deepEqual(policyPatch.result?.changedFields, ['schedulingPolicy'])
  const policyDml = dmlSql(policyPatch.calls)
  assert.equal(policyDml.length, 1)
  assert.match(policyDml[0], /UPDATE\s+"?groups"?\s+SET\s+"?scheduling_policy_json"?\s*=\s*\?,\s*updated_at\s*=\s*\?/i, '调度策略 PATCH 只能更新调度策略列')
  assert.doesNotMatch(policyDml[0], /group_type\s*=/i, '未修改分组类型时不得覆盖 group_type')
  assert.equal(JSON.parse(storedGroup().scheduling_policy_json ?? '{}').defaultSoftConcurrency, 12)
  assert.deepEqual(invalidations, ['group_updated'])

  const ownerStrategy = repositories.createRouteStrategy({
    name: '分组按需写 owner 可用性保护',
    mode: 'normal',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, ownerAccess)
  invalidations.length = 0
  const ownerAvailabilityBlocked = await captureSqlOutcome(() => repositories.patchGroupAsync(group.id, { enabled: false }, ownerAccess))
  assert.match(String(ownerAvailabilityBlocked.error), /唯一可用启用分组|请先到策略路由中切换或新增启用分组/)
  assert.deepEqual(dmlSql(ownerAvailabilityBlocked.calls), [], 'owner 可用性保护失败不得写入分组')
  assert.deepEqual(invalidations, [])
  repositories.deleteRouteStrategy(ownerStrategy.id, ownerAccess)

  const defaultGroup = repositories.listGroups(ownerAccess).find((item) => item.isDefault)
  assert(defaultGroup, '用户默认分组不存在')
  await assert.rejects(
    () => repositories.patchGroupAsync(defaultGroup.id, { description: '禁止修改' }, ownerAccess),
    /默认分组不允许修改/,
    '默认分组保护必须保留'
  )

  invalidations.length = 0
  const authorizedNoop = await captureSql(() => repositories.patchGroupAsync(group.id, {
    groupType: 'high_concurrency',
    schedulingPolicy: { ...originalPolicy, defaultSoftConcurrency: 12 }
  }, granteeAccess))
  assert.deepEqual(authorizedNoop.result?.changedFields, [])
  assert.deepEqual(dmlSql(authorizedNoop.calls), [], '授权分组同值 PATCH 不得创建本地设置行')
  assert.equal(authorizationSettingsCount(), 0)
  assert.deepEqual(invalidations, [])

  const granteeStrategy = repositories.createRouteStrategy({
    name: '分组按需写授权可用性保护',
    mode: 'normal',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, granteeAccess)
  invalidations.length = 0
  const authorizedAvailabilityBlocked = await captureSqlOutcome(() => repositories.patchGroupAsync(group.id, { enabled: false }, granteeAccess))
  assert.match(String(authorizedAvailabilityBlocked.error), /唯一可用启用分组|请先到策略路由中切换或新增启用分组/)
  assert.deepEqual(dmlSql(authorizedAvailabilityBlocked.calls), [], '授权分组可用性保护失败不得创建设置行')
  assert.equal(authorizationSettingsCount(), 0)
  assert.deepEqual(invalidations, [])
  repositories.deleteRouteStrategy(granteeStrategy.id, granteeAccess)

  invalidations.length = 0
  const authorizedSourceVersion = storedGroup().updated_at
  const authorizedDisable = await captureSql(() => repositories.patchGroupAsync(group.id, {
    enabled: false,
    expectedUpdatedAt: authorizedSourceVersion
  }, granteeAccess))
  assert.deepEqual(authorizedDisable.result?.changedFields, ['enabled'])
  assert.equal(authorizationSettingsCount(), 1, '首次真实本地变化才应创建授权分组设置行')
  const insertedSettings = authorizationSettings()
  assert.equal(authorizedDisable.result?.updatedAt, insertedSettings.updated_at)
  assert.ok(insertedSettings.updated_at > authorizedSourceVersion, '首次授权本地设置必须单调推进有效版本')
  assert.equal(insertedSettings.enabled, 0)
  assert.equal(insertedSettings.group_type, 'high_concurrency', '首次局部覆盖必须保留来源分组类型')
  assert.equal(JSON.parse(insertedSettings.scheduling_policy_json ?? '{}').defaultSoftConcurrency, 12, '首次局部覆盖必须保留当前来源调度策略')
  assert.deepEqual(invalidations, ['group_authorization_settings_updated'])

  invalidations.length = 0
  const authorizedEnable = await captureSql(() => repositories.patchGroupAsync(group.id, {
    enabled: true,
    expectedUpdatedAt: insertedSettings.updated_at
  }, granteeAccess))
  assert.deepEqual(authorizedEnable.result?.changedFields, ['enabled'])
  const authorizedDml = dmlSql(authorizedEnable.calls)
  assert.equal(authorizedDml.length, 1)
  assert.match(authorizedDml[0], /UPDATE\s+"?group_authorization_settings"?\s+SET\s+"?enabled"?\s*=\s*\?,\s*updated_at\s*=\s*\?/i, '已有授权设置的 enabled PATCH 只能更新 enabled')
  assert.match(authorizedDml[0], /AND\s+updated_at\s*=\s*\?/i, '授权本地设置 UPDATE 必须携带版本条件')
  assert.doesNotMatch(authorizedDml[0], /group_type\s*=|scheduling_policy_json\s*=/i, '授权 enabled PATCH 不得覆盖未提交的高并发策略')
  assert.equal(authorizationSettings().scheduling_policy_json, insertedSettings.scheduling_policy_json)
  assert.deepEqual(invalidations, ['group_authorization_settings_updated'])

  invalidations.length = 0
  const authorizedSameEnable = await captureSql(() => repositories.patchGroupAsync(group.id, {
    enabled: true,
    expectedUpdatedAt: authorizedEnable.result?.updatedAt
  }, granteeAccess))
  assert.deepEqual(authorizedSameEnable.result?.changedFields, [])
  assert.deepEqual(dmlSql(authorizedSameEnable.calls), [])
  assert.deepEqual(invalidations, [])

  repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '分组按需写绑定账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-group-demand-write',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_sse']
    },
    supportedModels: ['gpt-5.1'],
    healthCheckModel: 'gpt-5.1',
    healthCheckEndpointMode: 'responses_sse',
    groupId: group.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, ownerAccess)
  invalidations.length = 0
  const providerBlocked = await captureSqlOutcome(() => repositories.patchGroupAsync(group.id, { providerCode: 'anthropic' }, ownerAccess))
  assert.match(String(providerBlocked.error), /已有账户的分组不允许修改供应商/)
  assert.match(selectSql(providerBlocked.calls), /\bgroup_accounts\b/i, '真实供应商变化才应按需检查账户绑定')
  assert.deepEqual(dmlSql(providerBlocked.calls), [], '存在账户时供应商变更必须在 DML 前拒绝')
  assert.deepEqual(invalidations, [])

  const transitionGroup = repositories.createGroup({
    name: '分组类型策略同时提交回归',
    providerCode: 'gpt',
    groupType: 'high_concurrency',
    schedulingPolicy: originalPolicy
  }, ownerAccess)
  const transition = await repositories.patchGroupAsync(transitionGroup.id, {
    groupType: 'personal',
    schedulingPolicy: originalPolicy
  }, ownerAccess)
  assert.deepEqual(transition?.changedFields, ['groupType', 'schedulingPolicy'])
  const transitionStored = database.prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?').get(transitionGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  assert.equal(transitionStored.group_type, 'personal')
  assert.equal(transitionStored.scheduling_policy_json, null, '高并发转普通且同时携带策略时也必须清空旧 JSON')

  console.log('分组按需写回归通过：字段级读写、no-op、默认分组与授权本地设置边界均已验证')
} finally {
  unregisterInvalidator()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

interface SqlCall {
  kind: 'get' | 'all' | 'run'
  sql: string
  params: SQLInputValue[]
}

async function captureSql<T>(operation: () => Promise<T>): Promise<{ result: T; calls: SqlCall[] }> {
  const outcome = await captureSqlOutcome(operation)
  if (outcome.error) throw outcome.error
  return { result: outcome.result as T, calls: outcome.calls }
}

async function captureSqlOutcome<T>(operation: () => Promise<T>): Promise<{ result?: T; error?: unknown; calls: SqlCall[] }> {
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: SqlCall[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'get', sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'all', sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'run', sql, params })
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  try {
    return { result: await operation(), calls }
  } catch (error) {
    return { error, calls }
  } finally {
    database.prepare = originalPrepare
  }
}

function selectSql(calls: SqlCall[]): string {
  return calls.filter((call) => call.kind !== 'run').map((call) => call.sql).join('\n')
}

function dmlSql(calls: SqlCall[]): string[] {
  return calls.filter((call) => call.kind === 'run' && /^\s*(?:INSERT|UPDATE|DELETE)\b/i.test(call.sql)).map((call) => call.sql)
}

function expectSql(pattern: RegExp, calls: SqlCall[]): string {
  const match = dmlSql(calls).find((sql) => pattern.test(sql))
  assert(match, `未捕获预期 SQL：${pattern}`)
  return match
}

function storedGroup(): { scheduling_policy_json: string | null; updated_at: string } {
  return database.prepare('SELECT scheduling_policy_json, updated_at FROM groups WHERE id = ?').get(group.id) as unknown as { scheduling_policy_json: string | null; updated_at: string }
}

function groupPolicyJson(groupId: string): string | null {
  return (database.prepare('SELECT scheduling_policy_json FROM groups WHERE id = ?').get(groupId) as unknown as { scheduling_policy_json?: string | null }).scheduling_policy_json ?? null
}

function authorizationSettingsCount(): number {
  const row = database.prepare('SELECT COUNT(*) AS total FROM group_authorization_settings WHERE group_id = ? AND system_account_id = ?').get(group.id, grantee.id) as unknown as { total?: number }
  return Number(row.total ?? 0)
}

function authorizationSettings(): { enabled: number; group_type: string; scheduling_policy_json: string | null; updated_at: string } {
  const row = database.prepare('SELECT enabled, group_type, scheduling_policy_json, updated_at FROM group_authorization_settings WHERE group_id = ? AND system_account_id = ?').get(group.id, grantee.id) as unknown as { enabled: number; group_type: string; scheduling_policy_json: string | null; updated_at: string } | undefined
  assert(row)
  return row
}

function writablePolicy(policy: GroupSchedulingPolicy): Required<Pick<GroupSchedulingPolicy,
  'defaultSoftConcurrency' | 'maxQueueWaitMs' | 'clientIpConcurrencyLimit' | 'clientIpConcurrencyOverflowMode' | 'imageLaneMaxConcurrency'
>> {
  return {
    defaultSoftConcurrency: policy.defaultSoftConcurrency ?? 5,
    maxQueueWaitMs: policy.maxQueueWaitMs ?? 60_000,
    clientIpConcurrencyLimit: policy.clientIpConcurrencyLimit ?? 0,
    clientIpConcurrencyOverflowMode: policy.clientIpConcurrencyOverflowMode ?? 'reject',
    imageLaneMaxConcurrency: policy.imageLaneMaxConcurrency ?? 0
  }
}
