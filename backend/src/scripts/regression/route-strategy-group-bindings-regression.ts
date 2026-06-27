import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { maxRouteStrategyGroupBindings } from '../../storage/route-strategy-group-binding-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-route-strategy-group-bindings-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'route-strategy-group-bindings-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const primaryGroup = repositories.createGroup({
    name: '策略路由回归主分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '策略路由回归后备分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const disabledGroup = repositories.createGroup({
    name: '策略路由回归停用分组',
    providerCode: 'gpt',
    enabled: false
  }, access)

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '旧分组绑定入口回归 Key',
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /groupBindings/, 'API Key 创建不应再接受 groupBindings')

  assert.throws(() => {
    repositories.createApiKeyRecord({
      name: '缺少策略路由回归 Key'
    }, access)
  }, /API Key 必须绑定策略路由/, 'API Key 创建必须显式绑定策略路由')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '策略路由显式桥接字段回归策略',
      mode: 'normal',
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }],
      explicitHybridRouteRules: []
    }, access)
  }, /explicitHybridRouteRules/, '策略路由不应接收显式跨协议桥接规则')

  const normalStrategy = repositories.createRouteStrategy({
    name: '普通路由回归策略',
    mode: 'normal',
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, weight: 10, status: 'active' }]
  }, access)
  assert.equal(normalStrategy.mode, 'normal', '普通路由策略模式应为 normal')
  assert.equal(normalStrategy.groupBindings.length, 1, '普通路由只能保存一个分组绑定')
  assert.equal(normalStrategy.groupBindings[0]?.groupId, primaryGroup.id, '普通路由应绑定目标分组')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '普通路由多启用分组回归策略',
      mode: 'normal',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: fallbackGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /普通路由只能绑定一个启用分组/, '普通路由不能配置多个启用分组')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '策略路由重复分组回归策略',
      mode: 'failover',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: primaryGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /策略路由绑定分组不能重复/, '策略路由不能重复绑定同一分组')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '策略路由重复优先级回归策略',
      mode: 'failover',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: fallbackGroup.id, priority: 1, status: 'active' }
      ]
    }, access)
  }, /策略路由启用分组优先级不能重复/, '策略路由启用分组优先级不能重复')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '策略路由停用分组回归策略',
      mode: 'failover',
      groupBindings: [{ groupId: disabledGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /策略路由不能启用已停用分组/, '策略路由不能启用已停用分组')

  const failoverStrategy = repositories.createRouteStrategy({
    name: '故障回退路由回归策略',
    mode: 'failover',
    groupBindings: [
      { groupId: fallbackGroup.id, priority: 2, weight: 30, status: 'active' },
      { groupId: primaryGroup.id, priority: 1, weight: 70, status: 'active' }
    ]
  }, access)
  assert.equal(failoverStrategy.mode, 'failover', '故障回退策略模式应为 failover')
  assert.deepEqual(
    failoverStrategy.groupBindings.map((binding) => binding.groupId),
    [primaryGroup.id, fallbackGroup.id],
    '策略路由分组绑定应按优先级稳定排序'
  )

  const apiKey = repositories.createApiKeyRecord({
    name: '策略路由绑定回归 Key',
    routeStrategyId: failoverStrategy.id
  }, access)
  assert.equal(apiKey.routeStrategyId, failoverStrategy.id, 'API Key 应只保存 routeStrategyId')
  assert.equal(apiKey.routeStrategyMode, 'failover', 'API Key 摘要应返回策略路由模式摘要')
  assert.equal(Object.prototype.hasOwnProperty.call(apiKey, 'groupBindings'), false, 'API Key 摘要不应返回分组绑定详情')

  const usedStrategy = repositories.findRouteStrategySummary(failoverStrategy.id, access)
  assert.equal(usedStrategy?.apiKeyCount, 1, '策略路由应统计已绑定的 API Key 数量')
  assertRouteStrategyGroupLookupUsesGroupLeadingIndex(primaryGroup.id)
  assert.throws(() => {
    repositories.deleteRouteStrategy(failoverStrategy.id, access)
  }, /策略路由已被 1 个 API Key 使用/, '已被 API Key 使用的策略路由不能删除')

  assert.throws(() => {
    repositories.updateGroup(primaryGroup.id, { enabled: false }, access)
  }, /无法停用分组.*唯一可用启用分组|请先到策略路由中切换或新增启用分组/, '停用分组前必须检查策略路由唯一可用启用分组')

  assert.throws(() => {
    repositories.deleteGroup(primaryGroup.id, access)
  }, /仍是以下策略路由的唯一启用分组|请先到策略路由中切换或新增启用分组/, '删除分组前必须检查策略路由绑定')

  const owner = repositories.createSystemAccount({
    username: `route_strategy_authorized_owner_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: '策略路由授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: `route_strategy_authorized_grantee_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: '策略路由被授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const authorizedSourceGroup = repositories.createGroup({
    name: '策略路由授权来源分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: authorizedSourceGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '策略路由授权分组停用保护回归'
  }, ownerAccess)
  const authorizedStrategy = repositories.createRouteStrategy({
    name: '授权分组停用保护回归策略',
    mode: 'normal',
    groupBindings: [{ groupId: authorizedSourceGroup.id, priority: 1, status: 'active' }]
  }, granteeAccess)
  assert.equal(
    repositories.findRouteStrategySummary(authorizedStrategy.id, granteeAccess)?.groupBindings[0]?.groupId,
    authorizedSourceGroup.id,
    '策略路由应允许绑定有效授权给当前用户的分组'
  )
  assert.throws(() => {
    repositories.updateGroup(authorizedSourceGroup.id, { enabled: false }, granteeAccess)
  }, /无法停用授权分组.*唯一可用启用分组|请先到策略路由中切换或新增启用分组/, '停用授权分组设置前必须检查策略路由唯一可用启用分组')

  const extraGroups = Array.from({ length: maxRouteStrategyGroupBindings }, (_, index) => repositories.createGroup({
    name: `策略路由绑定上限回归分组 ${index + 1}`,
    providerCode: 'gpt',
    enabled: true
  }, access))
  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '策略路由绑定上限回归策略',
      mode: 'failover',
      groupBindings: [primaryGroup, ...extraGroups].map((group, index) => ({
        groupId: group.id,
        priority: index + 1,
        status: 'active'
      }))
    }, access)
  }, new RegExp(`策略路由最多绑定 ${maxRouteStrategyGroupBindings} 个分组`), '策略路由分组绑定数量必须有固定上限')

  console.log('策略路由分组绑定回归通过：API Key 只绑定策略路由，分组绑定、优先级、删除保护和固定上限均在策略路由层生效')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertRouteStrategyGroupLookupUsesGroupLeadingIndex(groupId: string): void {
  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT
        route_strategy_groups.route_strategy_id AS id,
        route_strategies.name,
        route_strategies.system_account_id AS systemAccountId,
        route_strategy_groups.status AS targetBindingStatus
      FROM route_strategy_groups
      INNER JOIN route_strategies
        ON route_strategies.id = route_strategy_groups.route_strategy_id
        AND route_strategies.system_account_id = route_strategy_groups.system_account_id
      WHERE route_strategy_groups.group_id = ?
      ORDER BY route_strategy_groups.route_strategy_id ASC
      LIMIT ?
    `)
    .all(groupId, 101) as Array<{ detail?: string }>
  const details = rows.map((row) => String(row.detail ?? '')).join('\n')
  assert.match(
    details,
    /idx_route_strategy_groups_group_strategy/,
    `按分组反查策略路由必须命中 group_id 前导索引，避免分组删除/停用 guard 扫描绑定表：${details}`
  )
}
