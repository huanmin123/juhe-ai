import type { SQLInputValue } from 'node:sqlite'

import {
  codexContextStateShardIndexes,
  getBusinessDatabase,
  getCodexContextStateShardDatabase,
  getDatasetDatabase,
  getStatsDatabase,
  getUsageCatalogDatabase
} from '../../storage/database.js'
import {
  listUsageRecordShardLocations,
  getUsageRecordShardDatabase
} from '../../storage/usage-record-shards.js'
import * as repositories from '../../storage/repositories.js'
import { chunks, idPrefix, tracePrefix, type CreatedMockdata, type MockdataOptions } from './mockdata/shared.js'

type BusinessDatabase = ReturnType<typeof getBusinessDatabase>

const allowedEmptyTables = new Set([
  'stats.model_trust_latest_dirty_accounts',
  'stats.usage_range_window_requests',
  'stats.background_job_leases',
  // 下列游标、dirty、receipt 和 cleanup queue 都按真实请求或后台消费按需写入；Mockdata 不伪造待处理工作。
  'business.account_api_key_pool_probe_cursors',
  // 账户可用性投影只由 PostgreSQL 高性能路径消费；SQLite Mockdata 保留 schema parity，不伪造无效投影行。
  'business.account_list_availability_dirty',
  'business.account_list_availability_projection_dependency_health',
  'business.account_list_availability_projection_index',
  'business.account_list_availability_projection_search_terms',
  'business.account_list_availability_projection_tags',
  'business.account_list_availability_projection_viewer_health',
  'business.account_list_availability_projections',
  'business.account_list_availability_runtime_overlays',
  // OIDC Provider 未启用时不会生成签名密钥、Client 或授权状态；启用时由 assertOidcProviderCoverage 强制校验。
  'business.oauth_access_tokens',
  'business.oauth_authorization_code_oidc_contexts',
  'business.oauth_authorization_codes',
  'business.oauth_authorization_transactions',
  'business.oauth_clients',
  'business.oauth_device_authorizations',
  'business.oauth_grants',
  'business.oauth_signing_keys',
  'stats.ai_performance_summary_dirty_system_accounts',
  'stats.model_trust_observation_receipts',
  'stats.usage_overview_dirty_scopes',
  'stats.usage_quota_hourly_window_dirty_scopes'
])

export function assertMockdataCoverage(created: CreatedMockdata, options: MockdataOptions): void {
  const database = getBusinessDatabase()
  assertBusinessCoverage(database, created)
  assertUsageCoverage(created)
  assertAccountHealthMonitorCoverage(created, options)
  assertCreatedShape(created)
  assertModelTrustCoverage()
  assertOidcProviderCoverage(database, created)
  assertApplicationTablesHaveRows()
}

function assertAccountHealthMonitorCoverage(created: CreatedMockdata, options: MockdataOptions): void {
  const database = getStatsDatabase()
  const summary = database.prepare(`
    SELECT
      COUNT(*) AS total_rows,
      COUNT(DISTINCT account_id) AS account_count,
      SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_rows,
      SUM(CASE WHEN status = 'failure' THEN 1 ELSE 0 END) AS failure_rows,
      MIN(stat_hour) AS first_hour,
      MAX(stat_hour) AS last_hour
    FROM account_health_hourly
    WHERE last_record_id LIKE 'mockdata_usage_health_%'
  `).get() as {
    total_rows?: number
    account_count?: number
    success_rows?: number
    failure_rows?: number
    first_hour?: string
    last_hour?: string
  } | undefined
  const expectedAccounts = Object.keys(created.accounts).length
  const expectedHours = Math.min(options.days * 24, 31 * 24)
  const totalRows = Number(summary?.total_rows ?? 0)
  assertMinimum('AI 健康监控账户样本不足，无法验证分页', Number(summary?.account_count ?? 0), expectedAccounts)
  assertMinimum('AI 健康监控成功小时样本缺失', Number(summary?.success_rows ?? 0), 1)
  assertMinimum('AI 健康监控失败小时样本缺失', Number(summary?.failure_rows ?? 0), 1)
  assertMinimum('AI 健康监控小时样本密度不足', totalRows, Math.floor(expectedAccounts * expectedHours * 0.9))
  if (expectedHours > 2 && totalRows >= expectedAccounts * expectedHours) {
    throw new Error('AI 健康监控无记录小时样本缺失')
  }
  if (expectedHours >= 31 * 24) {
    const firstAt = Date.parse(`${summary?.first_hour ?? ''}:00:00Z`)
    const lastAt = Date.parse(`${summary?.last_hour ?? ''}:00:00Z`)
    if (!Number.isFinite(firstAt) || !Number.isFinite(lastAt)) {
      throw new Error('AI 健康监控小时范围格式无效')
    }
    const spanHours = (lastAt - firstAt) / (60 * 60 * 1000)
    assertMinimum('AI 健康监控近 31 天时间跨度不足', spanHours, 31 * 24 - 2)
  }
}

function assertModelTrustCoverage(): void {
  assertMinimum('模型可信 observation 样本缺失', scalar(getDatasetDatabase(), 'SELECT COUNT(*) AS value FROM model_check_observations'), 1)
  assertMinimum('模型可信身份基线样本缺失', scalar(getStatsDatabase(), 'SELECT COUNT(*) AS value FROM model_identity_baseline_versions'), 1)
  assertMinimum('模型可信最新结果样本缺失', scalar(getStatsDatabase(), 'SELECT COUNT(*) AS value FROM model_account_trust_results'), 1)
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

function assertUsageCoverage(created: CreatedMockdata): void {
  const trafficSources = new Set<string>()
  const endpoints = new Set<string>()
  const billedServiceTiers = new Set<string>()
  const requestedReasoningEfforts = new Set<string>()
  const effectiveReasoningEfforts = new Set<string>()
  let imageTokenRows = 0
  let modelMappingRows = 0
  let pricingSnapshotRows = 0
  for (const location of listUsageRecordShardLocations()) {
    const database = getUsageRecordShardDatabase(location)
    for (const row of database.prepare("SELECT DISTINCT traffic_source AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) trafficSources.add(row.value)
    }
    for (const row of database.prepare("SELECT DISTINCT endpoint AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) endpoints.add(row.value)
    }
    for (const row of database.prepare("SELECT DISTINCT billed_service_tier AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) billedServiceTiers.add(row.value)
    }
    for (const row of database.prepare("SELECT DISTINCT requested_reasoning_effort AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) requestedReasoningEfforts.add(row.value)
    }
    for (const row of database.prepare("SELECT DISTINCT effective_reasoning_effort AS value FROM usage_records WHERE id LIKE 'mockdata_%'").all() as Array<{ value?: string }>) {
      if (row.value) effectiveReasoningEfforts.add(row.value)
    }
    imageTokenRows += scalar(database, "SELECT COUNT(*) AS value FROM usage_records WHERE id LIKE 'mockdata_%' AND (COALESCE(input_image_tokens, 0) > 0 OR COALESCE(output_image_tokens, 0) > 0)")
    modelMappingRows += scalar(database, "SELECT COUNT(*) AS value FROM usage_records WHERE id LIKE 'mockdata_%' AND model_mapping_applied = 1")
    pricingSnapshotRows += scalar(database, "SELECT COUNT(*) AS value FROM usage_records WHERE id LIKE 'mockdata_%' AND cost_breakdown_snapshot_json IS NOT NULL")
  }
  assertPresent('使用记录来源覆盖不完整', trafficSources, ['gateway', 'manual_account_test', 'account_health_check', 'runtime_recovery_probe', 'cooldown_retest', 'hybrid_scoring', 'hybrid_quality_scoring'])
  assertPresent('使用记录端点覆盖不完整', endpoints, ['GET /v1/models', 'POST /v1/responses', 'POST /v1/chat/completions', 'POST /v1/images/generations'])
  assertPresent('使用记录实际服务档位覆盖不完整', billedServiceTiers, ['priority', 'flex'])
  assertPresent('使用记录请求思考强度覆盖不完整', requestedReasoningEfforts, ['low', 'medium'])
  assertPresent('使用记录最终思考强度覆盖不完整', effectiveReasoningEfforts, ['high'])
  assertMinimum('图片 token 使用记录样本缺失', imageTokenRows, 1)
  assertMinimum('模型映射使用记录样本缺失', modelMappingRows, 1)
  assertMinimum('使用记录写入时计价快照样本缺失', pricingSnapshotRows, 1)
  assertUpstreamResponseModelCoverage(created)
}

function assertUpstreamResponseModelCoverage(created: CreatedMockdata): void {
  const samples = [
    {
      id: `${idPrefix}usage_coverage_upstream_response_model_match`,
      traceId: `${tracePrefix}usage-coverage_upstream_response_model_match`,
      model: 'mockdata-global-long-context',
      upstreamModel: 'gpt-5.4-mini',
      upstreamResponseModel: 'gpt-5.4-mini',
      upstreamModelMismatch: false,
      modelMappingApplied: true
    },
    {
      id: `${idPrefix}usage_coverage_upstream_response_model_mismatch`,
      traceId: `${tracePrefix}usage-coverage_upstream_response_model_mismatch`,
      model: 'mockdata-global-long-context',
      upstreamModel: 'gpt-5.4-mini',
      upstreamResponseModel: 'gpt-5.4-mini-2026-03-17',
      upstreamModelMismatch: true,
      modelMappingApplied: true
    },
    {
      id: `${idPrefix}usage_coverage_upstream_response_model_unmapped_mismatch`,
      traceId: `${tracePrefix}usage-coverage_upstream_response_model_unmapped_mismatch`,
      model: 'gpt-5.4-mini',
      upstreamModel: 'gpt-5.4-mini',
      upstreamResponseModel: 'gpt-5.4-mini-2026-03-17',
      upstreamModelMismatch: true,
      modelMappingApplied: false
    }
  ] as const
  const access = { systemAccountId: created.users.admin.id, role: 'admin' as const }
  for (const sample of samples) {
    const listItem = repositories.listUsageRecords(access, { traceId: sample.traceId, pageSize: 10 }).items
      .find((item) => item.id === sample.id)
    if (
      listItem?.model !== sample.model
      || listItem.upstreamModel !== sample.upstreamModel
      || listItem.upstreamResponseModel !== sample.upstreamResponseModel
      || listItem.upstreamModelMismatch !== sample.upstreamModelMismatch
      || listItem.modelMappingApplied !== sample.modelMappingApplied
    ) {
      throw new Error(`上游响应模型使用记录列表映射不完整：${sample.id}`)
    }
    const detail = repositories.getUsageRecordDetail(sample.id, access)
    if (
      detail?.model !== sample.model
      || detail.upstreamModel !== sample.upstreamModel
      || detail.upstreamResponseModel !== sample.upstreamResponseModel
      || detail.upstreamModelMismatch !== sample.upstreamModelMismatch
      || detail.modelMappingApplied !== sample.modelMappingApplied
      || detail.pricingModel !== sample.upstreamModel
    ) {
      throw new Error(`上游响应模型使用记录详情映射不完整：${sample.id}`)
    }
  }

}

function assertCreatedShape(created: CreatedMockdata): void {
  assertMinimum('Mockdata 账户对象数量不足', Object.keys(created.accounts).length, 24)
  assertMinimum('Mockdata API Key 对象数量不足', Object.keys(created.apiKeys).length, 18)
}

function assertOidcProviderCoverage(database: BusinessDatabase, created: CreatedMockdata): void {
  if (!created.oidc) return
  const clientIds = [created.oidc.browserClientId, created.oidc.serviceClientId]
  assertMinimum('OIDC Client Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value
    FROM oauth_clients
    WHERE client_id IN (${placeholders(clientIds)})
  `, ...clientIds), 2)
  assertMinimum('OIDC 已授权应用 Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value
    FROM oauth_grants
    WHERE client_id = ? AND system_account_id = ? AND revoked_at IS NULL
  `, created.oidc.browserClientId, created.users.admin.id), 1)
  assertMinimum('OIDC access token Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value
    FROM oauth_access_tokens
    WHERE client_id = ? AND revoked_at IS NULL AND replaced_at IS NULL
  `, created.oidc.browserClientId), 1)
  assertMinimum('OIDC 授权码上下文 Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value
    FROM oauth_authorization_code_oidc_contexts contexts
    INNER JOIN oauth_authorization_codes codes ON codes.id = contexts.code_id
    WHERE codes.client_id = ?
  `, created.oidc.browserClientId), 1)
  assertMinimum('OIDC 授权事务 Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value FROM oauth_authorization_transactions WHERE client_id = ?
  `, created.oidc.serviceClientId), 1)
  assertMinimum('OIDC Device Flow Mockdata 缺失', scalar(database, `
    SELECT COUNT(*) AS value FROM oauth_device_authorizations WHERE client_id = ? AND status = 'approved'
  `, created.oidc.browserClientId), 1)
  assertMinimum('OIDC 签名密钥缺失', scalar(database, 'SELECT COUNT(*) AS value FROM oauth_signing_keys WHERE status = ?', 'active'), 1)
}

function assertApplicationTablesHaveRows(): void {
  const emptyTables: string[] = []
  collectEmptyTables(emptyTables, 'business', getBusinessDatabase())
  collectEmptyTables(emptyTables, 'dataset', getDatasetDatabase())
  collectEmptyTables(emptyTables, 'usage-catalog', getUsageCatalogDatabase())
  collectEmptyTables(emptyTables, 'stats', getStatsDatabase())
  for (const shardIndex of codexContextStateShardIndexes()) {
    collectEmptyTables(emptyTables, `codex-context-state:${String(shardIndex).padStart(3, '0')}`, getCodexContextStateShardDatabase(shardIndex))
  }
  if (emptyTables.length) {
    throw new Error(`Mockdata 应用表覆盖不完整，空表：${emptyTables.join('、')}`)
  }
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

function collectEmptyTables(emptyTables: string[], databaseRole: string, database: BusinessDatabase): void {
  const tables = database
    .prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name ASC")
    .all() as Array<{ name?: string }>
  for (const table of tables) {
    const tableName = table.name
    if (!tableName) continue
    const row = database.prepare(`SELECT COUNT(*) AS value FROM ${quoteIdentifier(tableName)}`).get() as { value?: number } | undefined
    const allowedEmpty = allowedEmptyTables.has(`${databaseRole}.${tableName}`)
      || (databaseRole.startsWith('codex-context-state:') && tableName === 'codex_context_storage_cleanup_queue')
    if (Number(row?.value ?? 0) === 0 && !allowedEmpty) {
      emptyTables.push(`${databaseRole}.${tableName}`)
    }
  }
}

function quoteIdentifier(value: string): string {
  return `"${value.replaceAll('"', '""')}"`
}

function placeholders(ids: unknown[]): string {
  if (!ids.length) throw new Error('Mockdata 覆盖校验缺少 ID 输入')
  return ids.map(() => '?').join(',')
}
