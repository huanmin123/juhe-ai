import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-writer-pool-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 8
runtimeConfig.usageRecordWriterPoolEnabled = true
runtimeConfig.usageRecordWriterPoolSize = 4
runtimeConfig.usageRecordWriterQueueMaxItems = 1000
runtimeConfig.secret = 'usage-record-writer-pool-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageRecordShards, usageWriterPool, usageRecordQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/usage-record-writer-pool.js'),
  import('../../modules/gateway/usage/record-queue.service.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: 'usage writer pool 回归分组', providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'usage writer pool 回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-writer-pool',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'usage writer pool 回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)

  const createdAtBase = Date.now() - 60_000
  const records = Array.from({ length: 80 }, (_, index) => {
    const createdAt = new Date(createdAtBase + index).toISOString()
    return {
      id: usageRecordShards.generateUsageRecordId(createdAt, `writer-pool-${index}`),
      traceId: `trace-usage-writer-pool-${index}`,
      trafficSource: 'gateway' as const,
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/chat/completions',
      providerCode: 'gpt',
      model: 'gpt-5.5-mini',
      stream: index % 2 === 0,
      statusCode: 200,
      success: true,
      durationMs: 100 + index,
      inputTokens: 20,
      outputTokens: 10,
      costUsd: 0.001,
      createdAt
    }
  })

  await repositories.createUsageRecordsBatchAsync(records)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET last_used_at = NULL, updated_at = ? WHERE id = ?')
    .run(new Date(createdAtBase + records.length + 1).toISOString(), account.id)
  await repositories.createUsageRecordsBatchAsync(records)

  const shutdownRecords = Array.from({ length: 40 }, (_, index) => {
    const createdAt = new Date(createdAtBase + records.length + 1_000 + index).toISOString()
    return {
      id: usageRecordShards.generateUsageRecordId(createdAt, `writer-pool-shutdown-${index}`),
      traceId: `trace-usage-writer-pool-shutdown-${index}`,
      trafficSource: 'gateway' as const,
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/chat/completions',
      providerCode: 'gpt',
      model: 'gpt-5.5-mini',
      stream: index % 2 === 1,
      statusCode: 200,
      success: true,
      durationMs: 150 + index,
      inputTokens: 30,
      outputTokens: 15,
      costUsd: 0.002,
      createdAt
    }
  })
  usageRecordQueue.enqueueUsageRecordsLocal(shutdownRecords)
  usageRecordQueue.flushUsageRecordQueue()
  await usageRecordQueue.flushUsageRecordQueueForShutdown()

  const runtime = usageWriterPool.getUsageRecordWriterPoolRuntime()
  const expectedRecordCount = records.length + shutdownRecords.length

  assert.equal(runtime.enabled, true, 'usage writer pool 应在 ingest-worker 中启用')
  assert(runtime.handledJobs > 1, 'usage writer pool 应按 shard 拆分处理多个写任务')
  assert.equal(runtime.failedJobs, 0, 'usage writer pool 写入不应失败')
  assert.equal(runtime.rejectedJobs, 0, 'usage writer pool 队列不应拒绝请求')
  assert.equal(usageRecordQueue.pendingUsageRecordCount(), 0, 'shutdown flush 应等待正在进行的 async flush 并清空本地队列')
  assert(usageRecordShards.listUsageRecordShardLocations().length > 1, 'writer pool 写入后 usage catalog 应注册多个 shard')
  assert.equal(shardUsageRecordCount(), expectedRecordCount, 'writer pool 写入应保持 usage_records 幂等')
  assert.equal(catalogUsageRecordCount(), expectedRecordCount, 'usage catalog 应由 ingest-worker 单写补齐')

  const detail = repositories.getUsageRecordDetail(records[13].id, access)
  assert.equal(detail?.traceId, records[13].traceId, 'writer pool 写入后详情应能通过 catalog 定位 shard')
  const shutdownDetail = repositories.getUsageRecordDetail(shutdownRecords[13].id, access)
  assert.equal(shutdownDetail?.traceId, shutdownRecords[13].traceId, 'shutdown 等待 active flush 后详情应可读')
  const accountAfterUse = repositories.findAccountSummary(account.id, access)
  assert.equal(accountAfterUse?.lastUsedAt, shutdownRecords[shutdownRecords.length - 1]?.createdAt, '账号 last_used_at 应由 ingest-worker 合并副作用写入业务库，重复 usage id 也能补回副作用')

  console.log('使用记录 writer pool 回归通过：明细 shard 并行写，usage catalog 单写补齐，账号副作用合并落库')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  await usageWriterPool.closeUsageRecordWriterPool()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function catalogUsageRecordCount(): number {
  const row = databaseModule.getUsageCatalogDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_record_shard_entries')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
