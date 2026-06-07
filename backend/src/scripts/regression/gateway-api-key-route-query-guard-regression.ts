import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { loadActiveGatewayApiKeyGroupBindings } from '../../storage/gateway-api-key.repository.js'
import { maxApiKeyGroupBindings } from '../../storage/api-key-group-binding-limits.js'

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
  const groups = Array.from({ length: maxApiKeyGroupBindings + 1 }, (_, index) => repositories.createGroup({
    name: `网关 API Key 路由查询防护分组 ${String(index + 1).padStart(2, '0')}`,
    providerCode: 'gpt',
    enabled: true
  }, access))
  const apiKey = repositories.createApiKeyRecord({
    name: '网关 API Key 路由查询防护 Key',
    groupBindings: [{ groupId: groups[0].id, priority: 1, weight: 1, status: 'active' }]
  }, access)
  const database = databaseModule.getBusinessDatabase()
  const now = new Date().toISOString()
  const insertBinding = database.prepare(`
    INSERT INTO api_key_group_bindings (id, api_key_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, 1, 'active', ?, ?)
  `)
  for (let index = 1; index < groups.length; index += 1) {
    insertBinding.run(`akgb_route_guard_${index}`, apiKey.id, 'sys_admin', groups[index].id, index + 1, now, now)
  }

  assertGatewayRouteBindingQueryPlan()
  const bindings = loadActiveGatewayApiKeyGroupBindings(apiKey.id, 'sys_admin')
  assert.equal(bindings.length, maxApiKeyGroupBindings, '网关运行态单次只应读取固定上限的 API Key 分组绑定')
  assert.deepEqual(bindings.map((binding) => binding.priority), Array.from({ length: maxApiKeyGroupBindings }, (_, index) => index + 1), '网关 API Key 分组绑定应按优先级稳定返回固定窗口')

  console.log('网关 API Key 路由绑定查询防护回归通过：读取固定 20 条窗口并命中路由索引，无全表扫描或临时排序')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGatewayRouteBindingQueryPlan(): void {
  const details = explainBusinessQuery(`
    SELECT
      api_key_group_bindings.id,
      api_key_group_bindings.api_key_id,
      api_key_group_bindings.system_account_id,
      api_key_group_bindings.group_id,
      api_key_group_bindings.priority,
      api_key_group_bindings.weight,
      api_key_group_bindings.status,
      groups.provider_code,
      groups.enabled AS group_enabled
    FROM api_key_group_bindings
    INNER JOIN groups
      ON groups.id = api_key_group_bindings.group_id
    LEFT JOIN resource_authorizations group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = groups.id
      AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
      AND group_authorization.status = 'active'
      AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
    WHERE api_key_group_bindings.api_key_id = ?
      AND api_key_group_bindings.system_account_id = ?
      AND api_key_group_bindings.status = 'active'
      AND groups.enabled = 1
      AND (
        groups.system_account_id = api_key_group_bindings.system_account_id
        OR group_authorization.id IS NOT NULL
      )
    ORDER BY api_key_group_bindings.priority ASC, api_key_group_bindings.created_at ASC, api_key_group_bindings.id ASC
    LIMIT ?
  `, [new Date().toISOString(), 'key_query_plan_guard', 'sys_admin', maxApiKeyGroupBindings])
  assert(details.includes('idx_api_key_group_bindings_gateway_route'), `网关 API Key 分组绑定读取应命中路由窗口索引，实际计划：${details}`)
  assert(!details.includes('SCAN api_key_group_bindings'), `网关 API Key 分组绑定读取不能扫描绑定表，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `网关 API Key 分组绑定读取不应为排序创建临时 B-TREE，实际计划：${details}`)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
