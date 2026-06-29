import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-batch-statement-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-stats-batch-statement.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-stats-batch-statement-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '统计批量 statement 分组', providerCode: 'gpt', providerProtocolProfileId: 'profile_gpt_openai_v1' }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '统计批量 statement Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const mixedGroup = repositories.createGroup({ name: '统计账号类型合并分组', providerCode: 'gpt', providerProtocolProfileId: 'profile_gpt_openai_v1' }, access)
  const mixedApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '统计账号类型合并 Key',
    groupBindings: [{ groupId: mixedGroup.id, priority: 1, status: 'active' }],
  }, access)
  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '统计合并 OAuth 账户',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-usage-stats-mixed-oauth',
      access_token: 'access-usage-stats-mixed-oauth',
      expires_at: new Date(Date.now() + 3600_000).toISOString(),
      base_url: 'https://api.openai.com/v1'
    },
    groupId: mixedGroup.id
  }, access)
  const apiKeyAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '统计合并 API Key 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-stats-mixed-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: mixedGroup.id
  }, access)
  const createdAtBase = Date.now() - 60_000
  repositories.createUsageRecordsBatch([
    ...Array.from({ length: 8 }, (_, index) => ({
      id: `usage_stats_batch_statement_${index}`,
      traceId: `trace-usage-stats-batch-statement-${index}`,
      trafficSource: 'gateway' as const,
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      groupId: group.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      success: true,
      statusCode: 200,
      durationMs: 100 + index,
      firstTokenMs: 30 + index,
      inputTokens: 100 + index,
      outputTokens: 20 + index,
      cacheReadTokens: 10,
      costUsd: 0.001,
      createdAt: new Date(createdAtBase + index).toISOString()
    })),
    {
      id: 'usage_stats_gateway_error_bucket',
      traceId: 'trace-usage-stats-gateway-error-bucket',
      trafficSource: 'gateway' as const,
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      groupId: group.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      success: false,
      statusCode: 429,
      errorCode: 'rate_limit_exceeded',
      errorMessage: 'rate limited',
      durationMs: 450,
      inputTokens: 11,
      outputTokens: 0,
      costUsd: 0,
      createdAt: new Date(createdAtBase + 9).toISOString()
    },
    {
      id: 'usage_stats_cooldown_retest_included',
      traceId: 'trace-usage-stats-cooldown-retest-included',
      trafficSource: 'cooldown_retest' as const,
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      groupId: group.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      success: false,
      statusCode: 503,
      durationMs: 300,
      inputTokens: 999,
      outputTokens: 999,
      costUsd: 9,
      createdAt: new Date(createdAtBase + 10).toISOString()
    },
    {
      id: 'usage_stats_mixed_oauth_account',
      traceId: 'trace-usage-stats-mixed-oauth-account',
      trafficSource: 'gateway' as const,
      systemAccountId: 'sys_admin',
      apiKeyId: mixedApiKey.id,
      groupId: mixedGroup.id,
      accountId: oauthAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      success: true,
      statusCode: 200,
      durationMs: 210,
      firstTokenMs: 60,
      inputTokens: 200,
      outputTokens: 30,
      cacheReadTokens: 20,
      costUsd: 0.002,
      createdAt: new Date(createdAtBase + 20).toISOString()
    },
    {
      id: 'usage_stats_mixed_api_key_account',
      traceId: 'trace-usage-stats-mixed-api-key-account',
      trafficSource: 'gateway' as const,
      systemAccountId: 'sys_admin',
      apiKeyId: mixedApiKey.id,
      groupId: mixedGroup.id,
      accountId: apiKeyAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.1',
      success: true,
      statusCode: 200,
      durationMs: 220,
      firstTokenMs: 65,
      inputTokens: 300,
      outputTokens: 40,
      cacheReadTokens: 30,
      costUsd: 0.003,
      createdAt: new Date(createdAtBase + 21).toISOString()
    }
  ])

  const statsDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  const prepareCounts = new Map<string, number>()
  statsDatabase.prepare = ((sql: string) => {
    const tableName = usageStatsUpsertTableName(sql)
    if (tableName) {
      prepareCounts.set(tableName, (prepareCounts.get(tableName) ?? 0) + 1)
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare

  try {
    const processed = usageStatsRepository.aggregateUsageStatsBatch(100)
    assert.equal(processed, 12, '统计聚合应处理本批使用记录，包含恢复探活真实上游调用')
  } finally {
    statsDatabase.prepare = originalPrepare
  }

  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    assert.equal(prepareCounts.get(tableName), 1, `${tableName} upsert statement 应在批量聚合中复用`)
  }

  const total = statsDatabase
    .prepare("SELECT request_count, input_tokens, output_tokens FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ?")
    .get(apiKey.id) as { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined
  assert.equal(total?.request_count, 10, 'API Key 总量聚合应累计请求数，包含恢复探活真实上游调用')
  assert.equal(total?.input_tokens, 1838, 'API Key 总量聚合应累计输入 token，包含恢复探活真实上游调用')
  assert.equal(total?.output_tokens, 1187, 'API Key 总量聚合应累计输出 token，包含恢复探活真实上游调用')
  const modelDaily = statsDatabase
    .prepare("SELECT SUM(request_count) AS request_count FROM usage_model_daily WHERE system_account_id = 'sys_admin' AND provider_code = 'gpt' AND model = 'gpt-5.1'")
    .get() as { request_count?: number } | undefined
  assert.equal(modelDaily?.request_count, 12, '模型日统计应聚合本批使用记录，包含恢复探活真实上游调用')
  const errorDaily = statsDatabase
    .prepare("SELECT SUM(error_count) AS error_count FROM usage_error_daily WHERE system_account_id = 'sys_admin' AND provider_code = 'gpt' AND error_code = 'rate_limit_exceeded' AND status_code = 429")
    .get() as { error_count?: number } | undefined
  assert.equal(errorDaily?.error_count, 1, '错误日统计应聚合网关失败业务记录')
  const cooldownErrorDaily = statsDatabase
    .prepare("SELECT SUM(error_count) AS error_count FROM usage_error_daily WHERE system_account_id = 'sys_admin' AND provider_code = 'gpt' AND error_code = '503' AND status_code = 503")
    .get() as { error_count?: number } | undefined
  assert.equal(cooldownErrorDaily?.error_count, 1, '错误日统计应包含恢复探活失败记录')
  const latencyDaily = statsDatabase
    .prepare("SELECT SUM(sample_count) AS sample_count FROM usage_latency_daily WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ? AND metric_type = 'duration_ms'")
    .get(apiKey.id) as { sample_count?: number } | undefined
  assert.equal(latencyDaily?.sample_count, 10, '延迟日统计应按 API Key 维度聚合请求耗时样本，包含恢复探活')
  const jobState = statsDatabase
    .prepare("SELECT cursor_id, lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as { cursor_id?: string; lag_seconds?: number } | undefined
  assert.equal(jobState?.cursor_id, 'usage_stats_mixed_api_key_account', '统计聚合应推进到本批最新使用记录')

  const mixedApiKeyTotal = usageStatsTotal(statsDatabase, 'api_key', mixedApiKey.id)
  assert.equal(mixedApiKeyTotal?.request_count, 2, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计请求数')
  assert.equal(mixedApiKeyTotal?.input_tokens, 500, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计输入 token')
  assert.equal(mixedApiKeyTotal?.output_tokens, 70, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计输出 token')
  const mixedGroupTotal = usageStatsTotal(statsDatabase, 'group', mixedGroup.id)
  assert.equal(mixedGroupTotal?.request_count, 2, '同一分组下 OAuth/API Key 账号命中应合并统计请求数')
  assert.equal(mixedGroupTotal?.input_tokens, 500, '同一分组下 OAuth/API Key 账号命中应合并统计输入 token')
  assert.equal(mixedGroupTotal?.output_tokens, 70, '同一分组下 OAuth/API Key 账号命中应合并统计输出 token')
  assert.equal(usageStatsTotal(statsDatabase, 'account', oauthAccount.id)?.request_count, 1, 'OAuth 账户自身维度仍应保留账号质量统计')
  assert.equal(usageStatsTotal(statsDatabase, 'account', apiKeyAccount.id)?.request_count, 1, 'API Key 账户自身维度仍应保留账号质量统计')
  const accountTypeScopeCount = statsDatabase
    .prepare("SELECT COUNT(*) AS total FROM usage_stats_totals WHERE scope_type IN ('oauth', 'account_type')")
    .get() as { total?: number } | undefined
  assert.equal(accountTypeScopeCount?.total, 0, '统计聚合不应新增 OAuth/API Key 账户类型分片')

  repositories.createUsageRecordsBatch([{
    id: 'usage_stats_cooldown_retest_tail',
    traceId: 'trace-usage-stats-cooldown-retest-tail',
    trafficSource: 'cooldown_retest',
    systemAccountId: 'sys_admin',
    apiKeyId: mixedApiKey.id,
    groupId: mixedGroup.id,
    accountId: apiKeyAccount.id,
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.1',
    success: false,
    statusCode: 503,
    durationMs: 300,
    inputTokens: 999,
    outputTokens: 999,
    costUsd: 9,
    createdAt: new Date(createdAtBase + 40).toISOString()
  }])
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100), 1, '仅剩恢复探活时也应写入统计事实')
  const ignoredOnlyJobState = statsDatabase
    .prepare("SELECT cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as { cursor_id?: string } | undefined
  assert.equal(ignoredOnlyJobState?.cursor_id, 'usage_stats_cooldown_retest_tail', '仅剩恢复探活时应推进统计游标，避免阻塞明细清理')
  const apiKeyAccountQuality = statsDatabase
    .prepare("SELECT SUM(request_count) AS request_count FROM account_quality_minute_stats WHERE account_id = ?")
    .get(apiKeyAccount.id) as { request_count?: number } | undefined
  assert.equal(apiKeyAccountQuality?.request_count, 1, '恢复探活应进入用量统计，但不应写入账号质量分钟样本')

  console.log('用量统计批量 statement 回归通过：基础统计 upsert statement 在 batch 内复用，OAuth/API Key 账号命中按本地 API Key 和分组合并')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function usageStatsUpsertTableName(sql: string): string | undefined {
  const match = /^\s*INSERT\s+INTO\s+(usage_stats_(?:totals|minute|hourly|daily|weekly|monthly))\b/i.exec(sql)
  return match?.[1]
}

function usageStatsTotal(
  statsDatabase: ReturnType<typeof databaseModule.getStatsDatabase>,
  scopeType: string,
  scopeId: string
): { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined {
  return statsDatabase
    .prepare("SELECT request_count, input_tokens, output_tokens FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = ? AND scope_id = ?")
    .get(scopeType, scopeId) as { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined
}
