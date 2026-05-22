import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

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
  const group = repositories.createGroup({ name: '统计批量 statement 分组', providerCode: 'openai' }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '统计批量 statement Key',
    groupId: group.id
  }, access)
  const mixedGroup = repositories.createGroup({ name: '统计账号类型合并分组', providerCode: 'openai' }, access)
  const mixedApiKey = repositories.createApiKeyRecord({
    name: '统计账号类型合并 Key',
    groupId: mixedGroup.id
  }, access)
  const oauthAccount = repositories.createAccount({
    providerCode: 'openai',
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
    providerCode: 'openai',
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
      systemAccountId: 'sys_admin',
      apiKeyId: apiKey.id,
      groupId: group.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
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
      id: 'usage_stats_mixed_oauth_account',
      traceId: 'trace-usage-stats-mixed-oauth-account',
      systemAccountId: 'sys_admin',
      apiKeyId: mixedApiKey.id,
      groupId: mixedGroup.id,
      accountId: oauthAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
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
      systemAccountId: 'sys_admin',
      apiKeyId: mixedApiKey.id,
      groupId: mixedGroup.id,
      accountId: apiKeyAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
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

  const recordDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  const prepareCounts = new Map<string, number>()
  recordDatabase.prepare = ((sql: string) => {
    const tableName = usageStatsUpsertTableName(sql)
    if (tableName) {
      prepareCounts.set(tableName, (prepareCounts.get(tableName) ?? 0) + 1)
    }
    return originalPrepare(sql)
  }) as typeof recordDatabase.prepare

  try {
    const processed = usageStatsRepository.aggregateUsageStatsBatch(100)
    assert.equal(processed, 10, '统计聚合应处理本批使用记录')
  } finally {
    recordDatabase.prepare = originalPrepare
  }

  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    assert.equal(prepareCounts.get(tableName), 1, `${tableName} upsert statement 应在批量聚合中复用`)
  }

  const total = recordDatabase
    .prepare("SELECT request_count, input_tokens, output_tokens FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ?")
    .get(apiKey.id) as { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined
  assert.equal(total?.request_count, 8, 'API Key 总量聚合应累计请求数')
  assert.equal(total?.input_tokens, 828, 'API Key 总量聚合应累计输入 token')
  assert.equal(total?.output_tokens, 188, 'API Key 总量聚合应累计输出 token')

  const mixedApiKeyTotal = usageStatsTotal(recordDatabase, 'api_key', mixedApiKey.id)
  assert.equal(mixedApiKeyTotal?.request_count, 2, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计请求数')
  assert.equal(mixedApiKeyTotal?.input_tokens, 500, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计输入 token')
  assert.equal(mixedApiKeyTotal?.output_tokens, 70, '同一本地 API Key 下 OAuth/API Key 账号命中应合并统计输出 token')
  const mixedGroupTotal = usageStatsTotal(recordDatabase, 'group', mixedGroup.id)
  assert.equal(mixedGroupTotal?.request_count, 2, '同一分组下 OAuth/API Key 账号命中应合并统计请求数')
  assert.equal(mixedGroupTotal?.input_tokens, 500, '同一分组下 OAuth/API Key 账号命中应合并统计输入 token')
  assert.equal(mixedGroupTotal?.output_tokens, 70, '同一分组下 OAuth/API Key 账号命中应合并统计输出 token')
  assert.equal(usageStatsTotal(recordDatabase, 'account', oauthAccount.id)?.request_count, 1, 'OAuth 账户自身维度仍应保留账号质量统计')
  assert.equal(usageStatsTotal(recordDatabase, 'account', apiKeyAccount.id)?.request_count, 1, 'API Key 账户自身维度仍应保留账号质量统计')
  const accountTypeScopeCount = recordDatabase
    .prepare("SELECT COUNT(*) AS total FROM usage_stats_totals WHERE scope_type IN ('oauth', 'account_type')")
    .get() as { total?: number } | undefined
  assert.equal(accountTypeScopeCount?.total, 0, '统计聚合不应新增 OAuth/API Key 账户类型分片')

  console.log('用量统计批量 statement 回归通过：基础统计 upsert statement 在 batch 内复用，OAuth/API Key 账号命中按本地 API Key 和分组合并')
} finally {
  try {
    databaseModule.getDatabase().close()
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
  recordDatabase: ReturnType<typeof databaseModule.getStatsDatabase>,
  scopeType: string,
  scopeId: string
): { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined {
  return recordDatabase
    .prepare("SELECT request_count, input_tokens, output_tokens FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = ? AND scope_id = ?")
    .get(scopeType, scopeId) as { request_count?: number; input_tokens?: number; output_tokens?: number } | undefined
}
