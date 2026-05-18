import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-batch-statement-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'usage-stats-batch-statement.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'usage-stats-batch-statement-records.sqlite3')
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
  const createdAtBase = Date.now() - 60_000
  repositories.createUsageRecordsBatch(Array.from({ length: 8 }, (_, index) => ({
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
  })))

  const recordDatabase = databaseModule.getRecordDatabase()
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
    assert.equal(processed, 8, '统计聚合应处理本批使用记录')
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

  console.log('用量统计批量 statement 回归通过：基础统计 upsert statement 在 batch 内复用')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function usageStatsUpsertTableName(sql: string): string | undefined {
  const match = /^\s*INSERT\s+INTO\s+(usage_stats_(?:totals|minute|hourly|daily|weekly|monthly))\b/i.exec(sql)
  return match?.[1]
}
