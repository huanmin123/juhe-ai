import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { loadActiveGatewayApiKeyGroupBindings } from '../../storage/gateway-api-key.repository.js'
import { maxRouteStrategyGroupBindings } from '../../storage/route-strategy-group-binding-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-api-key-route-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-api-key-route-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const groups = Array.from({ length: maxRouteStrategyGroupBindings + 1 }, (_, index) => repositories.createGroup({
    name: `网关 API Key 路由查询防护分组 ${String(index + 1).padStart(2, '0')}`,
    providerCode: 'gpt',
    enabled: true
  }, access))
  const routeStrategy = repositories.createRouteStrategy({
    name: '网关 API Key 路由查询防护策略',
    mode: 'failover',
    groupBindings: [{ groupId: groups[0].id, priority: 1, weight: 1, status: 'active' }]
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '网关 API Key 路由查询防护 Key',
    routeStrategyId: routeStrategy.id
  }, access)
  const database = databaseModule.getBusinessDatabase()
  const apiKeyRouteRow = database.prepare('SELECT route_strategy_id FROM api_keys WHERE id = ?')
    .get(apiKey.id) as { route_strategy_id?: string } | undefined
  assert(apiKeyRouteRow?.route_strategy_id, 'API Key 创建后必须绑定策略路由')
  const routeStrategyId = apiKeyRouteRow.route_strategy_id
  const now = new Date().toISOString()
  const insertBinding = database.prepare(`
    INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, 1, 'active', ?, ?)
  `)
  for (let index = 1; index < groups.length; index += 1) {
    const params: SQLInputValue[] = [`rsg_route_guard_${index}`, routeStrategyId, 'sys_admin', groups[index].id, index + 1, now, now]
    insertBinding.run(...params)
  }

  assertGatewayRouteBindingQueryPlan(apiKey.id, routeStrategyId)
  const bindings = loadActiveGatewayApiKeyGroupBindings(apiKey.id, routeStrategyId, 'sys_admin')
  assert.equal(bindings.length, maxRouteStrategyGroupBindings, '网关运行态单次只应读取固定上限的策略路由分组绑定')
  assert.deepEqual(bindings.map((binding) => binding.priority), Array.from({ length: maxRouteStrategyGroupBindings }, (_, index) => index + 1), '网关策略路由分组绑定应按优先级稳定返回固定窗口')

  console.log('网关策略路由绑定查询防护回归通过：读取固定 20 条窗口并命中路由索引，无全表扫描或临时排序')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGatewayRouteBindingQueryPlan(apiKeyId: string, routeStrategyId: string): void {
  const details = explainBusinessQuery(`
    SELECT
      route_strategy_groups.id,
      ? AS api_key_id,
      route_strategy_groups.system_account_id,
      route_strategy_groups.group_id,
      route_strategy_groups.priority,
      route_strategy_groups.weight,
      route_strategy_groups.status,
      groups.provider_code,
      groups.enabled AS group_enabled
    FROM route_strategies
    INNER JOIN route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
    INNER JOIN groups
      ON groups.id = route_strategy_groups.group_id
    LEFT JOIN resource_authorizations group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = groups.id
      AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
      AND group_authorization.status = 'active'
      AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
    WHERE route_strategies.id = ?
      AND route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategy_groups.status = 'active'
      AND groups.enabled = 1
      AND (
        groups.system_account_id = route_strategy_groups.system_account_id
        OR group_authorization.id IS NOT NULL
      )
    ORDER BY route_strategy_groups.priority ASC, route_strategy_groups.created_at ASC, route_strategy_groups.id ASC
    LIMIT ?
  `, [apiKeyId, new Date().toISOString(), routeStrategyId, 'sys_admin', maxRouteStrategyGroupBindings])
  assert(details.includes('idx_route_strategy_groups_strategy_priority'), `网关策略路由分组读取应命中路由窗口索引，实际计划：${details}`)
  assert(!details.includes('SCAN route_strategy_groups'), `网关策略路由分组读取不能扫描绑定表，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `网关策略路由分组绑定读取不应为排序创建临时 B-TREE，实际计划：${details}`)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
