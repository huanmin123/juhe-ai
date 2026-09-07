import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-route-strategy-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'route-strategy-list-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '策略路由列表查询防护分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const matchedByName = repositories.createRouteStrategy({
    name: '检索策略',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)
  const matchedByNamePrefix = repositories.createRouteStrategy({
    name: '检索策略扩展',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)
  const middleNameOnly = repositories.createRouteStrategy({
    name: '普通检索策略',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)
  const disabledMatch = repositories.createRouteStrategy({
    name: '检索策略停用',
    mode: 'normal',
    status: 'disabled',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)
  const wildcardLiteral = repositories.createRouteStrategy({
    name: 'route%literal',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)
  const wildcardNeighbor = repositories.createRouteStrategy({
    name: 'routeXliteral',
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }]
  }, access)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+route_strategies\b/i.test(sql) && /\bORDER\s+BY\s+route_strategies\./i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const page = repositories.listRouteStrategyListItemsPage(access, { keyword: '检索策略', mode: 'normal', status: 'active', page: 1, pageSize: 20 })
    const pageIds = page.items.map((item) => item.id)
    assert(pageIds.includes(matchedByName.id), '策略路由列表搜索应命中名称精确值')
    assert(pageIds.includes(matchedByNamePrefix.id), '策略路由列表搜索应命中名称前缀值')
    assert(!pageIds.includes(middleNameOnly.id), '策略路由列表搜索不应命中名称中间包含值')
    assert(!pageIds.includes(disabledMatch.id), '策略路由列表状态筛选应排除停用策略')

    const options = repositories.listRouteStrategyOptions(access, { keyword: '检索策略', activeOnly: true, limit: 20 })
    const optionIds = options.map((item) => item.id)
    assert(optionIds.includes(matchedByName.id), '策略路由 options 搜索应命中名称精确值')
    assert(optionIds.includes(matchedByNamePrefix.id), '策略路由 options 搜索应命中名称前缀值')
    assert(!optionIds.includes(middleNameOnly.id), '策略路由 options 搜索不应命中名称中间包含值')
    assert(!optionIds.includes(disabledMatch.id), '策略路由 options activeOnly 应排除停用策略')

    const wildcardResult = repositories.listRouteStrategyListItemsPage(access, { keyword: 'route%', page: 1, pageSize: 20 })
    const wildcardIds = wildcardResult.items.map((item) => item.id)
    assert(wildcardIds.includes(wildcardLiteral.id), '策略路由搜索应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(wildcardNeighbor.id), '策略路由搜索不应把用户输入的 % 当作 LIKE 通配符')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 3, '回归应捕获策略路由列表和 options SQL')
  for (const call of capturedCalls) {
    assert(!/\bdescription\s+(?:COLLATE|LIKE|ILIKE)\b/i.test(call.sql), '策略路由搜索不应扫描说明字段')
    assert(!/\bLIKE\s+\?/i.test(call.sql), '策略路由名称搜索不应使用 LIKE，避免大小写折叠或通配符语义')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '策略路由搜索不应传入前导通配符参数')
    assert(!/\bCOUNT\s*\(/i.test(call.sql), '策略路由基础列表和 options SQL 不得执行动态计数')
  }
  const keywordPlans = capturedCalls
    .filter((call) => call.params.some((param) => param === '检索策略' || param === '检索策略%'))
    .map((call) => explainBusinessQuery(call.sql, call.params as SQLInputValue[]))
    .join('\n')
  assert.match(
    keywordPlans,
    /idx_route_strategies_system_account_name_lookup|idx_route_strategies_name_lookup|idx_route_strategies_owner_mode/,
    `策略路由关键词列表应命中 route_strategies lookup 索引，实际计划：${keywordPlans}`
  )
  assertBusinessIndexExists('idx_route_strategies_name_lookup')
  assertBusinessIndexExists('idx_route_strategies_system_account_name_lookup')
  assertBusinessIndexExists('idx_route_strategies_owner_mode')
  assertBusinessIndexExists('idx_route_strategy_groups_group_strategy')
  assertRouteStrategyReadPathsDoNotEnsureDefaults()
  assertRouteStrategyBaseProjectionIsStatic()

  console.log('策略路由列表查询防护回归通过：搜索仅按名称精确/前缀匹配，列表筛选命中索引，读路径不补默认数据')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  const rows = databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params) as Array<{ detail?: string }>
  return rows.map((row) => String(row.detail ?? '')).join('\n')
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function assertRouteStrategyReadPathsDoNotEnsureDefaults(): void {
  const source = readFileSync(resolve('src/storage/route-strategy.repository.ts'), 'utf8')
  for (const name of [
    'listRouteStrategiesPage',
    'listRouteStrategyListItemsPage',
    'listRouteStrategiesPageAsync',
    'listRouteStrategyListItemsPageAsync',
    'listRouteStrategyOptions',
    'listRouteStrategyOptionsAsync'
  ]) {
    const start = source.indexOf(`function ${name}`)
    assert.notEqual(start, -1, `应能找到策略路由读函数 ${name}`)
    const nextFunction = source.slice(start + 1).search(/\n(?:export\s+)?(?:async\s+)?function\s+/)
    const end = nextFunction === -1 ? undefined : start + 1 + nextFunction
    const snippet = source.slice(start, end)
    assert(!snippet.includes('ensureDefaultRouteStrategy'), `${name} 不允许在读路径补默认策略路由`)
  }
}

function assertRouteStrategyBaseProjectionIsStatic(): void {
  const source = readFileSync(resolve('src/storage/route-strategy.repository.ts'), 'utf8')
  for (const functionName of ['routeStrategyListItemColumns', 'routeStrategyListItemColumnsForClient']) {
    const snippet = sourceFunction(source, functionName)
    assert(!/\bCOUNT\s*\(/i.test(snippet), `${functionName} 不得包含相关 COUNT 子查询`)
    assert(!/\bapi_keys\b/i.test(snippet), `${functionName} 不得读取 API Key 动态计数`)
    assert(!/\broute_strategy_groups\b/i.test(snippet), `${functionName} 不得读取分组动态计数`)
  }
  for (const functionName of ['routeStrategyListItemsFromRows', 'routeStrategyListItemsFromRowsAsync']) {
    const snippet = sourceFunction(source, functionName)
    assert(!snippet.includes('loadRouteStrategyGroupBindingPreviewSummariesByRouteStrategyIds'), `${functionName} 不得同步加载分组预览`)
  }
}

function sourceFunction(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert.notEqual(start, -1, `应能找到函数 ${functionName}`)
  const nextFunction = source.slice(start + 1).search(/\n(?:export\s+)?(?:async\s+)?function\s+/)
  return source.slice(start, nextFunction === -1 ? undefined : start + 1 + nextFunction)
}
