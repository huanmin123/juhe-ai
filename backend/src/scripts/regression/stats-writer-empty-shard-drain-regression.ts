import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-stats-writer-empty-shard-drain-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 64
runtimeConfig.secret = 'stats-writer-empty-shard-drain-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'temporary-maintenance-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  for (let shardId = 0; shardId < 20; shardId += 1) {
    const shardKey = `20240101:s${String(shardId).padStart(2, '0')}`
    const location = usageRecordShards.usageRecordShardLocationFromKey(shardKey)
    assert(location, `空 shard key 应可解析：${shardKey}`)
    usageRecordShards.getUsageRecordShardDatabase(location)
  }

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '统计 writer 空 shard 回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '统计 writer 空 shard 回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-stats-writer-empty-shard-drain',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '统计 writer 空 shard 回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)
  repositories.createUsageRecordsBatch([{
    traceId: 'trace-stats-writer-empty-shard-drain',
    trafficSource: 'gateway',
    clientIp: '203.0.113.10',
    apiKeyId: apiKey.id,
    groupId: group.id,
    accountId: account.id,
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 200,
    success: true,
    inputTokens: 10,
    outputTokens: 20,
    costUsd: 0.001,
    createdAt: new Date(Date.now() - 60_000).toISOString()
  }])
  assert(usageRecordShards.listUsageRecordShardLocations().length > 20, '真实数据 shard 必须排在前置空 shard 之后')

  runtimeConfig.workerRole = 'stats-worker'
  const statsWriter = await import('../../modules/background/background-stats-writer.js')
  const usageResult = await statsWriter.requestStatsWriter({
    type: 'aggregate_usage_stats',
    batchSize: 1000,
    maxBatches: 20,
    maxRunMs: 10_000
  })
  assert.equal(usageResult.processed, 1, 'usage stats-writer 必须越过前置空 shard 窗口处理后续真实数据')
  assert.equal(apiKeyStatsTotal(apiKey.id), 1, 'usage stats-writer 必须写入真实聚合结果')

  const clientIpResult = await statsWriter.requestStatsWriter({
    type: 'aggregate_client_ip_stats',
    batchSize: 1000,
    maxBatches: 20,
    maxRunMs: 10_000
  })
  assert.equal(clientIpResult.processed, 1, 'client IP stats-writer 必须越过前置空 shard 窗口处理后续真实数据')
  assert.equal(clientIpStatsTotal(), 1, 'client IP stats-writer 必须写入真实聚合结果')

  console.log('stats-writer 空 shard drain 回归通过：usage 与 client IP 聚合都会完整轮转到后续真实 shard')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function apiKeyStatsTotal(apiKeyId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT request_count FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ?")
    .get(apiKeyId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function clientIpStatsTotal(): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT SUM(request_count) AS total FROM client_ip_stats_daily')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
