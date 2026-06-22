import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { maxGroupDeleteAffectedApiKeyRoutes } from '../../storage/api-key-group-binding-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-multi-group-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-multi-group-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, dbServiceHandlers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const primaryGroup = repositories.createGroup({
    name: '多分组回归 A 主池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '多分组回归 B 后备池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const disabledProxy = repositories.createProxy({
    name: '多分组回归停用代理',
    type: 'http',
    host: '127.0.0.1',
    port: 19_080,
    enabled: true
  }, access)
  const primaryBlockedAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组回归主池不可派发账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-primary-blocked',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: primaryGroup.id,
    proxyProfileId: disabledProxy.id,
    status: 'active',
    schedulable: true
  }, access)
  repositories.updateProxy(disabledProxy.id, { enabled: false })
  const fallbackAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组回归后备账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-fallback',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const deepSeekGroup = repositories.createGroup({
    name: '多供应商普通 Key DeepSeek 号池',
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)

  assert.throws(() => {
    repositories.createApiKeyRecord({
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }, /API Key 名称不能为空/, '创建 API Key 必须显式提供名称')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组未绑定创建回归 Key'
    }, access)
  }, /至少需要绑定一个分组/, '创建 API Key 时必须显式绑定至少一个分组')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组重复绑定创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: primaryGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /不能重复/, '创建时重复绑定同一分组应被数据层拒绝')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组非法状态创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'paused' }
      ]
    }, access)
  }, /分组绑定状态无效/, '创建时非法分组绑定状态应被数据层拒绝')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组字符串优先级创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: '1', status: 'active' }
      ]
    }, access)
  }, /分组优先级必须是大于 0 的整数/, '创建时数字字符串形式的分组优先级应被数据层拒绝')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组字符串权重创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, weight: '10', status: 'active' }
      ]
    }, access)
  }, /分组权重必须是 1-100 之间的整数/, '创建时数字字符串形式的分组权重应被数据层拒绝')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组未知字段创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' }
      ],
      scopes: ['legacy_scope']
    }, access)
  }, /API Key 创建参数包含未知字段：scopes/, '创建 API Key 不应接收当前契约外的保留/旧字段')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组绑定未知字段创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active', groupName: '旧字段' }
      ]
    }, access)
  }, /API Key 分组绑定参数包含未知字段：groupName/, '创建 API Key 分组绑定不应静默忽略未知字段')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组非法路由策略创建回归 Key',
      groupRouteStrategy: 'legacy_primary',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }, /分组路由策略无效/, '创建时非法分组路由策略应被数据层拒绝')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组非法说明创建回归 Key',
      description: 123,
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }, /API Key 说明必须是字符串/, '创建时非字符串说明不应被数据层静默忽略')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组非法过期时间创建回归 Key',
      expiresAt: 'not-a-date',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }, /API Key 过期时间必须是有效时间字符串/, '创建时非法过期时间不应被数据层当成未填写')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '多分组空绑定创建回归 Key',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: '', priority: 2, status: 'active' }
      ]
    }, access)
  }, /分组无效/, '创建时空分组绑定应被数据层拒绝')

  const apiKey = repositories.createApiKeyRecord({
    name: '多分组路由回归 Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)

  const created = repositories.findApiKeySummary(apiKey.id, access)
  assert.equal(created?.groupRouteStrategy, 'priority_failover', '未显式配置策略时应默认主备优先')
  assert.deepEqual(
    created?.groupBindings.map((binding) => [binding.groupId, binding.priority, binding.weight, binding.status]),
    [
      [primaryGroup.id, 1, 1, 'active'],
      [fallbackGroup.id, 2, 1, 'active']
    ],
    '详情应返回完整分组路由和默认权重'
  )

  const crossProviderApiKey = repositories.createApiKeyRecord({
    name: '普通 Key 跨供应商绑定回归',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: deepSeekGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  assert.equal(crossProviderApiKey.routeMode, 'normal', '跨供应商绑定不应强制切换为混合路由 Key')
  assert.deepEqual(
    crossProviderApiKey.groupBindings.map((binding) => binding.providerProtocolProfileId),
    [GPT_OPENAI_V1_PROFILE_ID, DEEPSEEK_OPENAI_V1_PROFILE_ID],
    '普通 API Key 应允许同时绑定 GPT 和 DeepSeek 供应商协议档案的分组'
  )

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, { name: '' }, access)
  }, /API Key 名称不能为空/, '更新时空名称不应被数据层静默忽略')

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, { description: 123 }, access)
  }, /API Key 说明必须是字符串/, '更新时非字符串说明不应被数据层静默忽略')

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, { expiresAt: 'not-a-date' }, access)
  }, /API Key 过期时间必须是有效时间字符串/, '更新时非法过期时间不应被数据层当成清空')

  const filteredByFallback = repositories.listApiKeysPage(access, {
    groupId: fallbackGroup.id,
    page: 1,
    pageSize: 20
  })
  assert(filteredByFallback.items.some((item) => item.id === apiKey.id), '按后备分组筛选也应命中 API Key')

  const runtime = await dbServiceHandlers.handleDbServiceOperation({
    type: 'read_gateway_runtime',
    key: apiKey.key
  })
  assert.equal(runtime.apiKey?.id, apiKey.id, '运行时应识别多分组 API Key')
  assert.equal(runtime.apiKey?.selected_group_id, fallbackGroup.id, '优先分组无正常可派发账号时运行时应切到后备分组')
  assert.equal(runtime.accounts.length, 1, '运行时应返回后备分组账号')
  assert.equal(runtime.accounts[0]?.id, fallbackAccount.id, '运行时账号应来自后备分组')

  const primaryHealthyAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组回归主池恢复账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-primary-healthy',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const restoredRuntime = await dbServiceHandlers.handleDbServiceOperation({
    type: 'read_gateway_runtime',
    key: apiKey.key
  })
  assert.equal(restoredRuntime.apiKey?.selected_group_id, primaryGroup.id, '优先分组恢复正常账号后运行时应回到主分组')
  assert(restoredRuntime.accounts.some((account) => account.id === primaryHealthyAccount.id), '恢复后的运行时应包含主分组正常账号')
  assert(!restoredRuntime.accounts.some((account) => account.id === primaryBlockedAccount.id && account.proxyProfileUnavailable !== true), '主分组代理不可用账号不应被视为正常可派发账号')

  const roundRobinGroupA = repositories.createGroup({
    name: '多分组轮询回归 A 号池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const roundRobinGroupB = repositories.createGroup({
    name: '多分组轮询回归 B 号池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组轮询回归 A 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-round-robin-a',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: roundRobinGroupA.id,
    status: 'active',
    schedulable: true
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组轮询回归 B 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-round-robin-b',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: roundRobinGroupB.id,
    status: 'active',
    schedulable: true
  }, access)
  const roundRobinKey = repositories.createApiKeyRecord({
    name: '多分组轮询策略回归 Key',
    groupRouteStrategy: 'round_robin',
    groupBindings: [
      { groupId: roundRobinGroupA.id, priority: 1, status: 'active' },
      { groupId: roundRobinGroupB.id, priority: 2, status: 'active' }
    ]
  }, access)
  assert.equal(roundRobinKey.groupRouteStrategy, 'round_robin', '创建时应保存轮询路由策略')
  assert.deepEqual(
    await runtimeGroupSequence(roundRobinKey.key, 4),
    [roundRobinGroupA.id, roundRobinGroupB.id, roundRobinGroupA.id, roundRobinGroupB.id],
    '轮询策略应在启用号池之间按请求轮转'
  )

  const weightedGroupA = repositories.createGroup({
    name: '多分组权重回归 A 号池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const weightedGroupB = repositories.createGroup({
    name: '多分组权重回归 B 号池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组权重回归 A 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-weighted-a',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: weightedGroupA.id,
    status: 'active',
    schedulable: true
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多分组权重回归 B 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-weighted-b',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: weightedGroupB.id,
    status: 'active',
    schedulable: true
  }, access)
  const weightedKey = repositories.createApiKeyRecord({
    name: '多分组权重策略回归 Key',
    groupRouteStrategy: 'weighted_round_robin',
    groupBindings: [
      { groupId: weightedGroupA.id, priority: 1, weight: 3, status: 'active' },
      { groupId: weightedGroupB.id, priority: 2, weight: 1, status: 'active' }
    ]
  }, access)
  const weightedSummary = repositories.findApiKeySummary(weightedKey.id, access)
  assert.equal(weightedSummary?.groupRouteStrategy, 'weighted_round_robin', '详情应返回权重路由策略')
  assert.deepEqual(
    weightedSummary?.groupBindings.map((binding) => [binding.groupId, binding.weight]),
    [
      [weightedGroupA.id, 3],
      [weightedGroupB.id, 1]
    ],
    '详情应返回每个绑定号池的权重'
  )
  const weightedSequence = await runtimeGroupSequence(weightedKey.key, 8)
  assert.equal(weightedSequence.filter((groupId) => groupId === weightedGroupA.id).length, 6, '权重 3 的号池在 8 次选择中应命中 6 次')
  assert.equal(weightedSequence.filter((groupId) => groupId === weightedGroupB.id).length, 2, '权重 1 的号池在 8 次选择中应命中 2 次')
  const strategyUpdated = repositories.updateApiKey(weightedKey.id, {
    groupRouteStrategy: 'round_robin'
  }, access)
  assert.equal(strategyUpdated?.groupRouteStrategy, 'round_robin', '更新 API Key 时应允许单独调整分组路由策略')
  assert.deepEqual(
    await runtimeGroupSequence(weightedKey.key, 4),
    [weightedGroupA.id, weightedGroupB.id, weightedGroupA.id, weightedGroupB.id],
    '策略更新为轮询后运行时应按新策略重新选择号池'
  )

  const updated = repositories.updateApiKey(apiKey.id, {
    groupBindings: [
      { groupId: fallbackGroup.id, priority: 1, status: 'active' },
      { groupId: primaryGroup.id, priority: 2, status: 'disabled' }
    ]
  }, access)
  assert.deepEqual(
    updated?.groupBindings.map((binding) => [binding.groupId, binding.priority, binding.status]),
    [
      [fallbackGroup.id, 1, 'active'],
      [primaryGroup.id, 2, 'disabled']
    ],
    '更新后应保留启停状态和优先级顺序'
  )

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, {
      groupBindings: [
        { groupId: fallbackGroup.id, priority: 1, status: 'active' },
        { groupId: fallbackGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /不能重复/, '重复绑定同一分组应被拒绝')

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, {
      groupBindings: [
        { groupId: fallbackGroup.id, priority: 1, status: 'active' },
        { groupId: '', priority: 2, status: 'active' }
      ]
    }, access)
  }, /分组无效/, '更新时空分组绑定应被拒绝')

  const deletePrimaryGroup = repositories.createGroup({
    name: '多分组删除回归主池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const deleteFallbackGroup = repositories.createGroup({
    name: '多分组删除回归后备池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const deleteRouteKey = repositories.createApiKeyRecord({
    name: '多分组删除回归 Key',
    groupBindings: [
      { groupId: deletePrimaryGroup.id, priority: 1, status: 'active' },
      { groupId: deleteFallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  const deletePrimaryResult = repositories.deleteGroup(deletePrimaryGroup.id, access)
  assert.equal(deletePrimaryResult.deleted, true, '删除非最后启用分组应成功')
  assert.deepEqual(
    deletePrimaryResult.affectedApiKeyRoutes.map((route) => [route.apiKeyId, route.removedGroupId]),
    [[deleteRouteKey.id, deletePrimaryGroup.id]],
    '删除优先分组时应返回受影响 API Key 路由变化，供操作日志记录'
  )
  const afterDeletePrimary = repositories.findApiKeySummary(deleteRouteKey.id, access)
  assert.deepEqual(
    afterDeletePrimary?.groupBindings.map((binding) => [binding.groupId, binding.status]),
    [[deleteFallbackGroup.id, 'active']],
    '删除优先分组后应同步移除对应绑定并保留后备分组'
  )
  assert.throws(() => {
    repositories.deleteGroup(deleteFallbackGroup.id, access)
  }, /唯一启用号池/, '不能删除 API Key 的最后一个启用分组')

  assertGroupDeleteAffectedApiKeyWindowQueryPlan()
  const overLimitDeleteGroup = repositories.createGroup({
    name: '多分组删除超限保护池',
    providerCode: 'gpt',
    enabled: true
  }, access)
  for (let index = 0; index <= maxGroupDeleteAffectedApiKeyRoutes; index += 1) {
    repositories.createApiKeyRecord({
      name: `多分组删除超限保护 Key ${String(index + 1).padStart(3, '0')}`,
      groupBindings: [
        { groupId: overLimitDeleteGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }
  assert.throws(() => {
    repositories.deleteGroup(overLimitDeleteGroup.id, access)
  }, new RegExp(`关联的 API Key 超过 ${maxGroupDeleteAffectedApiKeyRoutes}`), '删除影响 API Key 过多的分组应被固定窗口保护拦截')

  assertBusinessIndexExists('idx_api_key_group_bindings_key_group_unique')
  assertBusinessIndexExists('idx_api_key_group_bindings_owner_group_key')
  assertSqlUniqueIndexRejectsDuplicateBinding(apiKey.id, fallbackGroup.id)

  console.log('API Key 多分组绑定回归通过：创建、筛选、优先级更新、优先级/轮询/权重策略、删除优先分组保留后备、最后启用分组删除保护、删除影响 API Key 固定窗口保护、未绑定拦截、空绑定拦截、重复绑定拦截、唯一索引拒绝重复写入、不可派发优先分组切后备和恢复正常')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function assertGroupDeleteAffectedApiKeyWindowQueryPlan(): void {
  const details = explainBusinessQuery(`
    SELECT
      api_key_group_bindings.api_key_id AS id,
      api_keys.name,
      api_key_group_bindings.status AS targetBindingStatus
    FROM api_key_group_bindings
    INNER JOIN api_keys
      ON api_keys.id = api_key_group_bindings.api_key_id
      AND api_keys.system_account_id = api_key_group_bindings.system_account_id
    WHERE api_key_group_bindings.system_account_id = ?
      AND api_key_group_bindings.group_id = ?
    ORDER BY api_key_group_bindings.api_key_id ASC
    LIMIT ?
  `, ['sys_admin', 'group_delete_query_plan_guard', maxGroupDeleteAffectedApiKeyRoutes + 1])
  assert(details.includes('idx_api_key_group_bindings_owner_group_key'), `分组删除影响 API Key 预检应命中固定窗口索引，实际计划：${details}`)
  assert(!details.includes('SCAN api_key_group_bindings'), `分组删除影响 API Key 预检不能扫描绑定表，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `分组删除影响 API Key 预检不应为排序创建临时 B-TREE，实际计划：${details}`)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}

function assertSqlUniqueIndexRejectsDuplicateBinding(apiKeyId: string, groupId: string): void {
  const database = databaseModule.getBusinessDatabase()
  const now = new Date().toISOString()
  assert.throws(() => {
    database
      .prepare(`
        INSERT INTO api_key_group_bindings (id, api_key_id, system_account_id, group_id, priority, status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(`akgb_duplicate_rejected_${Date.now()}`, apiKeyId, access.systemAccountId, groupId, 100, 'disabled', now, now)
  }, /UNIQUE|constraint/i, '数据库唯一索引应拒绝直接写入重复分组绑定')
}

async function runtimeGroupSequence(apiKey: string, count: number): Promise<Array<string | undefined>> {
  const result: Array<string | undefined> = []
  for (let index = 0; index < count; index += 1) {
    const runtime = await dbServiceHandlers.handleDbServiceOperation({
      type: 'read_gateway_runtime',
      key: apiKey
    })
    result.push(runtime.apiKey?.selected_group_id)
  }
  return result
}
