import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createApiKeyRecordAsync,
  createGroupAsync,
  createRouteStrategyAsync,
  listApiKeysPageAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'API Key 列表 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `api_key_list_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'admin' }
const createdApiKeyIds: string[] = []
const createdRouteStrategyIds: string[] = []
const createdGroupIds: string[] = []

try {
  await cleanupSmokeRows()

  const group = await createGroupAsync({
    name: `API Key 列表 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const routeStrategy = await createRouteStrategyAsync({
    name: `API Key 列表 PG smoke 策略 ${marker}`,
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 10, status: 'active' }]
  }, access)
  createdRouteStrategyIds.push(routeStrategy.id)

  const keyword = `检索目标 Key ${marker}`
  const matchedByName = await createApiKeyRecordAsync({
    name: keyword,
    description: `说明不参与搜索 ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  createdApiKeyIds.push(matchedByName.id)

  const matchedByNamePrefix = await createApiKeyRecordAsync({
    name: `${keyword} 扩展`,
    description: `说明不参与搜索扩展 ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  createdApiKeyIds.push(matchedByNamePrefix.id)

  const middleNameOnly = await createApiKeyRecordAsync({
    name: `普通 ${keyword}`,
    description: `中间包含不应命中 ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  createdApiKeyIds.push(middleNameOnly.id)

  const disabledKey = await createApiKeyRecordAsync({
    name: `停用 API Key ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'disabled'
  }, access)
  createdApiKeyIds.push(disabledKey.id)

  const wildcardLiteral = await createApiKeyRecordAsync({
    name: `percent%literal ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  createdApiKeyIds.push(wildcardLiteral.id)

  const wildcardNeighbor = await createApiKeyRecordAsync({
    name: `percentXliteral ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  createdApiKeyIds.push(wildcardNeighbor.id)
  await seedApiKeyListUsage(matchedByName.id)

  const keywordResult = await listApiKeysPageAsync(access, { keyword, page: 1, pageSize: 20 })
  const keywordIds = keywordResult.items.map((item) => item.id)
  const keywordDiagnostics = () => JSON.stringify({
    expected: [matchedByName.id, matchedByNamePrefix.id],
    actual: keywordResult.items.map((item) => ({ id: item.id, name: item.name, status: item.status }))
  })
  assert(keywordIds.includes(matchedByName.id), `PG API Key 列表 keyword 应命中名称精确值：${keywordDiagnostics()}`)
  assert(keywordIds.includes(matchedByNamePrefix.id), `PG API Key 列表 keyword 应命中名称前缀值：${keywordDiagnostics()}`)
  assert(!keywordIds.includes(middleNameOnly.id), 'PG API Key 列表 keyword 不应命中名称中间包含值')
  assert.equal(Object.prototype.hasOwnProperty.call(keywordResult.items.find((item) => item.id === matchedByName.id) ?? {}, 'key'), false, 'PG API Key 列表不应返回完整密钥字段')
  assert.equal(keywordResult.items.find((item) => item.id === matchedByName.id)?.usage.requestCount, 12, 'PG API Key 列表应返回累计用量')

  const wildcardResult = await listApiKeysPageAsync(access, { keyword: `percent%literal ${marker}`, page: 1, pageSize: 20 })
  const wildcardIds = wildcardResult.items.map((item) => item.id)
  assert(wildcardIds.includes(wildcardLiteral.id), 'PG API Key 列表应把 % 当作字面量前缀处理')
  assert(!wildcardIds.includes(wildcardNeighbor.id), 'PG API Key 列表不应把用户输入的 % 当作 LIKE 通配符')

  const routeStrategyResult = await listApiKeysPageAsync(access, { routeStrategyId: routeStrategy.id, status: 'active', page: 1, pageSize: 50 })
  const routeStrategyIds = routeStrategyResult.items.map((item) => item.id)
  assert(routeStrategyIds.includes(matchedByName.id), 'PG API Key 列表按策略路由筛选应命中 active Key')
  assert(!routeStrategyIds.includes(disabledKey.id), 'PG API Key 列表 active 状态筛选不应返回停用 Key')

  await assertApiKeyListIndexedPlans(access.systemAccountId, routeStrategy.id, keyword)

  console.log(JSON.stringify({
    message: 'API Key 列表 PG smoke 通过',
    groupId: group.id,
    routeStrategyId: routeStrategy.id,
    matchedApiKeyId: matchedByName.id,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function assertApiKeyListIndexedPlans(systemAccountId: string, routeStrategyId: string, keywordValue: string): Promise<void> {
  await assertIndexedPlan(
    'API Key 默认列表排序 PG 查询',
    `
      WITH page_api_key_ids AS MATERIALIZED (
        SELECT api_keys.id
        FROM juhe_business.api_keys api_keys
        WHERE api_keys.system_account_id = $1
        ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
        LIMIT 20
      )
      SELECT api_keys.id
      FROM page_api_key_ids
      INNER JOIN juhe_business.api_keys api_keys
        ON api_keys.id = page_api_key_ids.id
      INNER JOIN juhe_business.route_strategies route_strategies
        ON route_strategies.id = api_keys.route_strategy_id
        AND route_strategies.system_account_id = api_keys.system_account_id
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
    `,
    [systemAccountId],
    ['idx_api_keys_system_account_default_updated']
  )
  await assertIndexedPlan(
    'API Key 名称前缀 PG 查询',
    `
      WITH matched_api_key_ids AS MATERIALIZED (
        SELECT keyword_api_keys.id
        FROM juhe_business.api_keys keyword_api_keys
        WHERE keyword_api_keys.system_account_id = $1
          AND keyword_api_keys.name COLLATE "C" >= $2
          AND keyword_api_keys.name COLLATE "C" < $3
          AND starts_with(keyword_api_keys.name, $2)
      )
      SELECT api_keys.id
      FROM matched_api_key_ids
      INNER JOIN juhe_business.api_keys api_keys ON api_keys.id = matched_api_key_ids.id
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
      LIMIT 20
    `,
    [systemAccountId, keywordValue, apiKeyTextPrefixUpperBound(keywordValue)],
    ['idx_api_keys_system_account_name_c_lookup', 'idx_api_keys_owner_name_unique']
  )
  await assertIndexedPlan(
    'API Key 名称前缀索引窗口 PG 查询',
    `
      SELECT api_keys.id
      FROM juhe_business.api_keys api_keys
      WHERE api_keys.system_account_id = $1
        AND api_keys.name COLLATE "C" >= $2
        AND api_keys.name COLLATE "C" < $3
        AND starts_with(api_keys.name, $2)
      ORDER BY api_keys.name COLLATE "C" ASC, api_keys.id ASC
      LIMIT 20
    `,
    [systemAccountId, keywordValue, apiKeyTextPrefixUpperBound(keywordValue)],
    ['idx_api_keys_system_account_name_c_lookup', 'idx_api_keys_owner_name_unique']
  )
  await assertIndexedPlan(
    'API Key 策略路由筛选 PG 查询',
    `
      SELECT api_keys.id
      FROM juhe_business.api_keys api_keys
      WHERE api_keys.system_account_id = $1
        AND api_keys.route_strategy_id = $2
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
      LIMIT 50
    `,
    [systemAccountId, routeStrategyId],
    ['idx_api_keys_route_strategy', 'idx_api_keys_system_account_default_updated']
  )
}

async function seedApiKeyListUsage(apiKeyId: string): Promise<void> {
  const pool = await getPostgresPool()
  const updatedAt = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      last_used_at, updated_at
    ) VALUES ($1, 'api_key', $2, 12, 11, 1, 120, 60, 8, 0.008, 0.234, $3, $3)
  `, [access.systemAccountId, apiKeyId, updatedAt])
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const pool = await getPostgresPool()
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

function apiKeyTextPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\uffff`
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const apiKeyIds = [...new Set(createdApiKeyIds)]
  await pool.query("DELETE FROM juhe_stats.usage_stats_totals WHERE scope_type = 'api_key' AND scope_id = ANY($1::text[])", [apiKeyIds])
  if (apiKeyIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.api_keys WHERE id = ANY($1::text[])', [apiKeyIds])
  }
  const routeStrategyIds = [...new Set(createdRouteStrategyIds)]
  if (routeStrategyIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = ANY($1::text[])', [routeStrategyIds])
    await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = ANY($1::text[])', [routeStrategyIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.api_keys WHERE position($1 in name) > 0', [marker])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id IN (SELECT id FROM juhe_business.route_strategies WHERE position($1 in name) > 0)', [marker])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE position($1 in name) > 0', [marker])
  await pool.query('DELETE FROM juhe_business.groups WHERE position($1 in name) > 0', [marker])
}
