import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-shard-routing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 4
runtimeConfig.secret = 'usage-record-shard-routing-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '分片写入回归分组', providerCode: 'openai', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '分片写入回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-shard-routing',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = repositories.createApiKeyRecord({ name: '分片写入回归 Key', groupId: group.id }, access)
  const createdAtBase = Date.now() - 60_000
  const recordsLength = 40
  const records = Array.from({ length: recordsLength }, (_, index) => ({
    id: `usage_shard_routing_${String(index).padStart(2, '0')}`,
    traceId: `trace-usage-shard-routing-${index}`,
    apiKeyId: apiKey.id,
    groupId: group.id,
    accountId: account.id,
    endpoint: '/v1/responses',
    providerCode: 'openai',
    model: index % 2 === 0 ? 'gpt-5.5' : 'gpt-5.5-mini',
    stream: false,
    statusCode: 200,
    success: true,
    inputTokens: 10 + index,
    outputTokens: 20 + index,
    costUsd: Number(((recordsLength - index) / 1000).toFixed(6)),
    createdAt: new Date(createdAtBase + index).toISOString()
  }))

  repositories.createUsageRecordsBatch(records)
  repositories.createUsageRecordsBatch(records)

  assert.equal(datasetUsageRecordCount(), 0, '新 usage 不应再写入数据集目录库的旧 usage_records 单表')
  assert.equal(shardUsageRecordCount(), records.length, '新 usage 应写入 usage shard，重复投递应保持幂等')
  assert(usageRecordShards.listUsageRecordShardLocations().length > 1, '固定 4 个 shard 时批量记录应分散到多个 SQLite 文件')

  const list = repositories.listUsageRecords(access, { page: 1, pageSize: 5 })
  assert.deepEqual(
    list.items.map((item) => item.id),
    records.slice(-5).reverse().map((record) => record.id),
    '使用记录列表应跨 shard 合并并保持 created_at DESC 排序'
  )
  const costAscList = repositories.listUsageRecords(access, { page: 1, pageSize: 5, sortBy: 'costUsd', sortOrder: 'asc' })
  assert.deepEqual(
    costAscList.items.map((item) => item.id),
    records.slice(-5).reverse().map((record) => record.id),
    '使用记录列表应跨 shard 合并并保持 cost_usd ASC 排序'
  )
  const detail = repositories.getUsageRecordDetail(records[7].id, access)
  assert.equal(detail?.traceId, records[7].traceId, '旧格式显式 id 也应能通过受控 shard 扫描读取详情')

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(1000), records.length, '统计聚合应从 usage shard 读取完整业务记录')
  assert.equal(apiKeyStatsTotal(apiKey.id), records.length, 'API Key 统计应按 shard 输入聚合到统计结果库')
  assert(usageShardCursorCount() > 1, '统计结果库应为多个 usage shard 维护独立游标')
  const recentShape = repositories.findRecentOpenAIRequestShapeForAccount(account.id, group.id)
  assert.equal(recentShape?.model, records[records.length - 1].model, '恢复探活应从最近日期 shard 学习真实请求形态')

  const staleShapeAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '旧请求形态窗口回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-stale-shape',
      base_url: 'https://api.openai.com/v1'
    }
  }, access)
  repositories.createUsageRecordsBatch([{
    id: 'usage_shard_stale_request_shape',
    traceId: 'trace-usage-shard-stale-request-shape',
    apiKeyId: apiKey.id,
    accountId: staleShapeAccount.id,
    endpoint: '/v1/responses',
    providerCode: 'openai',
    model: 'gpt-stale-shape',
    stream: false,
    statusCode: 200,
    success: true,
    createdAt: new Date(Date.now() - 14 * 24 * 60 * 60 * 1000).toISOString()
  }])
  assert.equal(
    repositories.findRecentOpenAIRequestShapeForAccount(staleShapeAccount.id),
    undefined,
    '恢复探活请求形态学习应限制最近窗口，避免扫描全历史 shard'
  )

  console.log('使用记录分片写入回归通过：新 usage 落到多个 shard，旧单表不再热写，统计可从 shard 聚合')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function datasetUsageRecordCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_records')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function shardUsageRecordCount(): number {
  return usageRecordShards.listUsageRecordShardLocations()
    .reduce((total, location) => {
      const row = usageRecordShards.getUsageRecordShardDatabase(location)
        .prepare('SELECT COUNT(*) AS total FROM usage_records')
        .get() as { total?: number } | undefined
      return total + Number(row?.total ?? 0)
    }, 0)
}

function apiKeyStatsTotal(apiKeyId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT request_count FROM usage_stats_totals WHERE system_account_id = 'sys_admin' AND scope_type = 'api_key' AND scope_id = ?")
    .get(apiKeyId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageShardCursorCount(): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT COUNT(*) AS total FROM stats_job_state WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation' AND cursor_id IS NOT NULL")
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
