import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { maxRouteStrategyGroupBindings } from '../../storage/route-strategy-group-binding-limits.js'
import { DEFAULT_BUILT_IN_GROUPS, DEFAULT_GPT_GROUP } from '../../storage/schema-defaults.js'

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
const defaultRouteResourceCount = DEFAULT_BUILT_IN_GROUPS.filter((group) => group.providerCode !== HYBRID_PROVIDER_CODE).length

try {
  const initialDefaultStrategies = repositories.listRouteStrategyOptions({ ...access, systemAccountFilterId: access.systemAccountId }, { limit: 20 }).filter((strategy) => strategy.isDefault)
  assert.equal(initialDefaultStrategies.length, defaultRouteResourceCount, '策略路由列表应包含非混合默认分组对应的默认普通路由')
  assert.equal(initialDefaultStrategies.every((strategy) => strategy.mode === 'normal'), true, '默认策略路由必须都是普通路由')

  const database = databaseModule.getBusinessDatabase()
  const defaultGroup = repositories.listGroups(access).find((group) => group.enabled)
  assert(defaultGroup, '默认保护回归需要一个可绑定分组')
  database.prepare(`
    INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
    VALUES ('route_strategy_default_delete_guard_regression', 'sys_admin', '默认删除保护回归策略', NULL, 'normal', 'active', 1, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
  `).run()
  database.prepare(`
    INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
    VALUES ('rsg_default_delete_guard_regression', 'route_strategy_default_delete_guard_regression', 'sys_admin', ?, 1, 1, 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
  `).run(defaultGroup.id)
  const defaultStrategy = repositories.listRouteStrategyOptions(access, { limit: 20 }).find((strategy) => strategy.isDefault)
  assert(defaultStrategy, '手工标记的默认策略路由应能正常读取')
  assert.equal(repositories.updateRouteStrategy(defaultStrategy.id, { name: defaultStrategy.name }, access)?.name, defaultStrategy.name, '默认策略路由携带原名称更新时不应被误拦截')
  assert.throws(() => {
    repositories.updateRouteStrategy(defaultStrategy.id, { name: `${defaultStrategy.name}改` }, access)
  }, /默认策略路由不允许修改名称/, '默认策略路由不能修改名称')
  assert.throws(() => {
    repositories.deleteRouteStrategy(defaultStrategy.id, access)
  }, /默认策略路由不允许删除/, '默认策略路由不能删除')

  const primaryGroup = repositories.createGroup({
    name: '策略路由回归主分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '策略路由回归后备分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, access)
  const disabledGroup = repositories.createGroup({
    name: '策略路由回归停用分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
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
  assert.equal(normalStrategy.normalRoutingConfig?.schedulingPreference, 'cost_first', '普通路由默认应使用成本优先调度')
  assert.equal(Object.hasOwn(normalStrategy.normalRoutingConfig ?? {}, 'firstByteDeadlineMs'), false, '普通路由成本优先不得创建首字截止')
  assert.equal(normalStrategy.groupBindings.length, 1, '普通路由只能保存一个分组绑定')
  assert.equal(normalStrategy.groupBindings[0]?.groupId, primaryGroup.id, '普通路由应绑定目标分组')

  const speedFirstStrategy = repositories.createRouteStrategy({
    name: '普通路由速度优先回归策略',
    mode: 'normal',
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: 30000,
      speedFirstConfig: {
        slowTriggerCount: 3,
        slowWindowSeconds: 120,
        recoverySuccessCount: 3,
        probeIntervalSeconds: 30,
        degradedTtlSeconds: 300
      }
    },
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
  }, access)
  assert.equal(speedFirstStrategy.normalRoutingConfig?.schedulingPreference, 'speed_first', '速度优先配置应保存到普通路由')
  assert.equal(speedFirstStrategy.normalRoutingConfig?.firstByteDeadlineMs, 30000, '速度优先应读取公共首字截止字段')
  assert.equal(Object.hasOwn(speedFirstStrategy.normalRoutingConfig?.speedFirstConfig ?? {}, 'firstByteThresholdMs'), false, '规范化速度配置不得继续输出旧首字阈值字段')
  assert.equal(speedFirstStrategy.normalRoutingConfig?.speedFirstConfig?.slowTriggerCount, 3, '速度优先慢速触发次数默认基线应为 3')
  assert.equal(speedFirstStrategy.normalRoutingConfig?.speedFirstConfig?.maxFirstByteRetriesPerRequest, 2, '速度优先单请求切号次数默认基线应为 2')
  const speedFirstListItem = repositories
    .listRouteStrategyListItemsPage(access, { mode: 'normal', page: 1, pageSize: 50 })
    .items
    .find((item) => item.id === speedFirstStrategy.id)
  assert.equal(speedFirstListItem?.normalRoutingConfig?.schedulingPreference, 'speed_first', '策略路由列表项应返回速度优先配置供前端模式列展示')
  const storedSpeedFirstConfig = JSON.parse(String((database.prepare('SELECT config_json FROM route_strategies WHERE id = ?').get(speedFirstStrategy.id) as { config_json: string }).config_json)) as Record<string, unknown>
  assert.equal(JSON.stringify(storedSpeedFirstConfig).includes('firstByteDeadlineMs'), true, 'repository 应把首字截止写入公共字段')
  assert.equal(JSON.stringify(storedSpeedFirstConfig).includes('firstByteThresholdMs'), false, 'repository 不得写入旧速度模式首字阈值字段')

  const legacySpeedFirstStrategy = repositories.createRouteStrategy({
    name: '普通路由旧首字阈值兼容回归策略',
    mode: 'normal',
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      speedFirstConfig: {
        firstByteThresholdMs: 25000,
        slowTriggerCount: 4
      }
    },
    groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
  }, access)
  assert.equal(legacySpeedFirstStrategy.normalRoutingConfig?.firstByteDeadlineMs, 25000, '旧 firstByteThresholdMs 应只作为公共首字截止的读取兼容别名')
  assert.equal(legacySpeedFirstStrategy.normalRoutingConfig?.speedFirstConfig?.slowTriggerCount, 4, '旧配置迁移时应保留速度优先专属参数')
  assert.equal(Object.hasOwn(legacySpeedFirstStrategy.normalRoutingConfig?.speedFirstConfig ?? {}, 'firstByteThresholdMs'), false, '旧首字阈值兼容读取后不得继续出现在规范化结果')
  const storedLegacyConfig = String((database.prepare('SELECT config_json FROM route_strategies WHERE id = ?').get(legacySpeedFirstStrategy.id) as { config_json: string }).config_json)
  assert.equal(storedLegacyConfig.includes('firstByteDeadlineMs'), true, '旧首字阈值输入写库时必须迁移为公共字段')
  assert.equal(storedLegacyConfig.includes('firstByteThresholdMs'), false, '旧首字阈值输入写库后不得保留兼容别名')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '普通路由首字阈值过低回归策略',
      mode: 'normal',
      normalRoutingConfig: {
        schedulingPreference: 'speed_first',
        firstByteDeadlineMs: 9999
      },
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /首字截止时间/, '普通路由公共首字截止不能低于 10 秒')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '普通路由双首字字段回归策略',
      mode: 'normal',
      normalRoutingConfig: {
        schedulingPreference: 'speed_first',
        firstByteDeadlineMs: 10000,
        speedFirstConfig: { firstByteThresholdMs: 30000 }
      },
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /不能同时配置/, '新旧首字字段同时出现时必须拒绝，避免双事实源')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '普通路由慢速触发次数过低回归策略',
      mode: 'normal',
      normalRoutingConfig: {
        schedulingPreference: 'speed_first',
        speedFirstConfig: { slowTriggerCount: 1 }
      },
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /触发次数/, '速度优先慢速触发次数不能低于 2')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '普通路由误配混合规则回归策略',
      mode: 'normal',
      hybridRoutingConfig: {},
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /普通路由不能配置混合评分规则/, '普通路由不能接收混合智能配置')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '权重路由误配普通调度回归策略',
      mode: 'weighted',
      normalRoutingConfig: { schedulingPreference: 'speed_first' },
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, weight: 50, status: 'active' },
        { groupId: fallbackGroup.id, priority: 1, weight: 50, status: 'active' }
      ]
    }, access)
  }, /只有普通路由可以配置调度偏好/, '非普通路由不能接收普通路由调度配置')

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

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '故障回退缺少备用分组回归策略',
      mode: 'failover',
      groupBindings: [{ groupId: primaryGroup.id, priority: 1, status: 'active' }]
    }, access)
  }, /故障回退路由需要一个主用分组和至少一个备用分组/, '故障回退路由必须配置一个主用和至少一个备用分组')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '故障回退主用停用回归策略',
      mode: 'failover',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'disabled' },
        { groupId: fallbackGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /故障回退路由的主用分组必须启用/, '故障回退路由的主用分组必须保持启用')

  assert.throws(() => {
    repositories.createRouteStrategy({
      name: '故障回退无启用备用回归策略',
      mode: 'failover',
      groupBindings: [
        { groupId: primaryGroup.id, priority: 1, status: 'active' },
        { groupId: fallbackGroup.id, priority: 2, status: 'disabled' }
      ]
    }, access)
  }, /故障回退路由至少需要一个启用备用分组/, '故障回退路由必须至少保留一个启用备用分组')

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
    providerCode: DEFAULT_GPT_GROUP.providerCode,
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
    providerCode: DEFAULT_GPT_GROUP.providerCode,
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

  console.log('策略路由分组绑定回归通过：API Key 只绑定策略路由，分组绑定、故障回退主备、删除保护和固定上限均在策略路由层生效')
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
