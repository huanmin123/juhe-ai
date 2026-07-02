import type { SQLInputValue } from 'node:sqlite'

import { getBusinessDatabase } from '../../storage/database.js'
import {
  listUsageRecordShardLocations,
  getUsageRecordShardDatabase
} from '../../storage/usage-record-shards.js'
import { chunks, idPrefix, type CreatedMockdata } from './mockdata/shared.js'

type BusinessDatabase = ReturnType<typeof getBusinessDatabase>

export function assertMockdataCoverage(created: CreatedMockdata): void {
  const database = getBusinessDatabase()
  assertBusinessCoverage(database, created)
  assertUsageCoverage()
  assertCreatedShape(created)
}

function assertBusinessCoverage(database: BusinessDatabase, created: CreatedMockdata): void {
  const accountIds = Object.values(created.accounts).map((account) => account.id)
  const apiKeyIds = Object.values(created.apiKeys).map((apiKey) => apiKey.id)
  const customProviderModelIds = customProviderModelIdsForCreated(database)
  assertPresent(
    'AI 账户状态覆盖不完整',
    accountStatuses(database, accountIds),
    ['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']
  )
  assertPresent(
    'AI 账户类型覆盖不完整',
    textValuesForIds(database, 'accounts', 'id', 'type', accountIds),
    ['api_key', 'oauth']
  )
  assertPresent(
    'AI 账户客户端兼容覆盖不完整',
    textValuesForIds(database, 'accounts', 'id', 'client_compatibility', accountIds),
    ['openai_standard', 'codex_responses']
  )
  assertPresent(
    'API Key 路由策略覆盖不完整',
    textValuesForIds(database, `
      SELECT DISTINCT route_strategies.mode AS value
      FROM api_keys
      INNER JOIN route_strategies ON route_strategies.id = api_keys.route_strategy_id
      WHERE api_keys.id IN ({placeholders})
    `, apiKeyIds),
    ['normal', 'round_robin', 'weighted']
  )
  assertPresent(
    'API Key 绑定状态覆盖不完整',
    textValuesForIds(database, `
      SELECT DISTINCT route_strategy_groups.status AS value
      FROM api_keys
      INNER JOIN route_strategy_groups ON route_strategy_groups.route_strategy_id = api_keys.route_strategy_id
      WHERE api_keys.id IN ({placeholders})
    `, apiKeyIds),
    ['active', 'disabled']
  )
  assertPresent(
    '账户内 API Key 运行态覆盖不完整',
    textValuesForIds(database, `
      SELECT DISTINCT states.status AS value
      FROM account_api_key_runtime_states states
      WHERE states.account_id IN ({placeholders})
    `, accountIds),
    ['temporary_unavailable', 'rate_limited', 'error']
  )
  assertPresent(
    '自定义模型状态覆盖不完整',
    textValuesForIds(database, 'custom_provider_models', 'id', 'status', customProviderModelIds),
    ['active', 'draft', 'disabled']
  )
  assertPresent(
    '自定义模型范围覆盖不完整',
    textValuesForIds(database, 'custom_provider_models', 'id', 'scope', customProviderModelIds),
    customProviderModelScopeExpectations(database)
  )
  assertMinimum('AI 账户标签样本缺失', scalar(database, 'SELECT COUNT(*) AS value FROM account_tags WHERE name IN (?, ?, ?, ?)', '多Key', '图像生成', '时间计划', '模型映射'), 4)
  assertMinimum('账户模型映射样本缺失', scalar(database, `
    SELECT COUNT(*) AS value
    FROM account_model_mappings
    WHERE account_id IN (${placeholders(accountIds)})
  `, ...accountIds), 2)
  assertMinimum('账户时间计划样本缺失', scalar(database, `SELECT COUNT(*) AS value FROM accounts WHERE id IN (${placeholders(accountIds)}) AND availability_schedule_json IS NOT NULL AND status = 'disabled'`, ...accountIds), 1)
  assertMinimum('API Key 时间计划样本缺失', scalar(database, `SELECT COUNT(*) AS value FROM api_keys WHERE id IN (${placeholders(apiKeyIds)}) AND availability_schedule_json IS NOT NULL`, ...apiKeyIds), 1)
  assertMinimum('系统账号图像权限样本缺失', scalar(database, "SELECT COUNT(*) AS value FROM system_accounts WHERE username LIKE 'mockdata_%' AND image_generation_enabled = 1"), 1)
}

function assertUsageCoverage(): void {
  const trafficSources = new Set<string>()
  const endpoints = new Set<string>()
  let imageTokenRows = 0
  let modelMappingRows = 0
  for (const location of listUsageRecordShardLocations()) {
    const database = getUsageRecordShardDatabase(location)
    for (const row of database.prepare("SELECT DISTINCT traffic_source AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) trafficSources.add(row.value)
    }
    for (const row of database.prepare("SELECT DISTINCT endpoint AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) endpoints.add(row.value)
    }
    imageTokenRows += scalar(database, "SELECT COUNT(*) AS value FROM usage_records WHERE id LIKE 'mockdata_%' AND (COALESCE(input_image_tokens, 0) > 0 OR COALESCE(output_image_tokens, 0) > 0)")
    modelMappingRows += scalar(database, "SELECT COUNT(*) AS value FROM usage_records WHERE id LIKE 'mockdata_%' AND model_mapping_applied = 1")
  }
  assertPresent('使用记录来源覆盖不完整', trafficSources, ['gateway', 'manual_account_test', 'runtime_recovery_probe', 'cooldown_retest', 'hybrid_scoring', 'hybrid_quality_scoring'])
  assertPresent('使用记录端点覆盖不完整', endpoints, ['GET /v1/models', 'POST /v1/responses', 'POST /v1/chat/completions', 'POST /v1/images/generations'])
  assertMinimum('图片 token 使用记录样本缺失', imageTokenRows, 1)
  assertMinimum('模型映射使用记录样本缺失', modelMappingRows, 1)
}

function assertCreatedShape(created: CreatedMockdata): void {
  assertMinimum('Mockdata 账户对象数量不足', Object.keys(created.accounts).length, 24)
  assertMinimum('Mockdata API Key 对象数量不足', Object.keys(created.apiKeys).length, 18)
}

function accountStatuses(database: BusinessDatabase, accountIds: string[]): Set<string> {
  return new Set((database.prepare(`
    SELECT DISTINCT CASE
      WHEN cooldown_until IS NOT NULL AND cooldown_until > strftime('%Y-%m-%dT%H:%M:%fZ', 'now') THEN status
      ELSE status
    END AS value
    FROM accounts
    WHERE id IN (${placeholders(accountIds)})
  `).all(...accountIds) as Array<{ value?: string }>).map((row) => row.value).filter((value): value is string => Boolean(value)))
}

function customProviderModelIdsForCreated(database: BusinessDatabase): string[] {
  return (database.prepare("SELECT id FROM custom_provider_models WHERE model LIKE 'mockdata-%'").all() as Array<{ id?: string }>)
    .map((row) => row.id)
    .filter((id): id is string => Boolean(id))
}

function customProviderModelScopeExpectations(database: BusinessDatabase): string[] {
  const table = database
    .prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'custom_provider_models'")
    .get() as { sql?: string } | undefined
  if (table?.sql?.includes("CHECK (scope = 'personal')")) {
    return ['personal']
  }
  return ['global', 'personal']
}

function textValuesForIds(database: BusinessDatabase, tableName: string, idColumn: string, valueColumn: string, ids: string[]): Set<string>
function textValuesForIds(database: BusinessDatabase, sqlTemplate: string, ids: string[]): Set<string>
function textValuesForIds(database: BusinessDatabase, tableOrSql: string, idColumnOrIds: string | string[], valueColumn?: string, ids?: string[]): Set<string> {
  const actualIds = Array.isArray(idColumnOrIds) ? idColumnOrIds : ids ?? []
  const values = new Set<string>()
  for (const chunk of chunks(actualIds, 800)) {
    if (!chunk.length) continue
    const sql = Array.isArray(idColumnOrIds)
      ? tableOrSql.replace('{placeholders}', placeholders(chunk))
      : `SELECT DISTINCT ${valueColumn} AS value FROM ${tableOrSql} WHERE ${idColumnOrIds} IN (${placeholders(chunk)})`
    for (const row of database.prepare(sql).all(...chunk) as Array<{ value?: string }>) {
      if (row.value) values.add(row.value)
    }
  }
  return values
}

function scalar(database: BusinessDatabase, sql: string, ...params: SQLInputValue[]): number {
  const row = database.prepare(sql).get(...params) as { value?: unknown } | undefined
  const value = Number(row?.value ?? 0)
  return Number.isFinite(value) ? value : 0
}

function assertPresent(label: string, actualValues: Set<string>, expectedValues: string[]): void {
  const missing = expectedValues.filter((value) => !actualValues.has(value))
  if (missing.length) {
    throw new Error(`${label}，缺少：${missing.join('、')}；当前：${[...actualValues].join('、') || '空'}`)
  }
}

function assertMinimum(label: string, actual: number, minimum: number): void {
  if (actual < minimum) {
    throw new Error(`${label}，期望至少 ${minimum}，实际 ${actual}`)
  }
}

function placeholders(ids: unknown[]): string {
  if (!ids.length) throw new Error('Mockdata 覆盖校验缺少 ID 输入')
  return ids.map(() => '?').join(',')
}
