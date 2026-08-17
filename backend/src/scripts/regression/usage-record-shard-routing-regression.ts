import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-shard-routing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
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
  for (const createdAt of ['2026-08-16T00:00:00', 'not-a-time']) {
    assert.throws(
      () => usageRecordShards.generateUsageRecordId(createdAt, 'invalid-time'),
      /RFC3339/,
      `分片定位不得把无效 createdAt 退化为当前日期：${createdAt}`
    )
  }
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }
  const group = repositories.createGroup({ name: '分片写入回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '分片写入回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-shard-routing',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.5'],
    groupId: group.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '分片写入回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)
  const createdAtBase = Date.now() - 60_000
  const recordsLength = 40
  const records = Array.from({ length: recordsLength }, (_, index) => {
    const createdAt = new Date(createdAtBase + index).toISOString()
    return {
      id: usageRecordShards.generateUsageRecordId(createdAt, `routing-${index}`),
      traceId: `trace-usage-shard-routing-${index}`,
      trafficSource: 'gateway' as const,
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: index === 7 ? 'unknown-provider-for-locked-snapshot' : 'gpt',
      model: index === 7 ? 'unknown-model-for-locked-snapshot' : index % 2 === 0 ? 'gpt-5.5' : 'gpt-5.5-mini',
      requestedServiceTier: 'default' as const,
      effectiveServiceTier: 'default' as const,
      billedServiceTier: 'default' as const,
      requestedReasoningEffort: 'low' as const,
      effectiveReasoningEffort: 'high' as const,
      stream: false,
      statusCode: 200,
      success: true,
      inputTokens: 10 + index,
      outputTokens: 20 + index,
      costUsd: Number(((recordsLength - index) / 1000).toFixed(6)),
      createdAt
    }
  })

  repositories.createUsageRecordsBatch(records)
  repositories.createUsageRecordsBatch(records)

  assertCurrentShardRegistry()
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
    '使用记录列表应忽略非时间排序参数并保持 created_at DESC 排序'
  )
  const detail = repositories.getUsageRecordDetail(records[6].id, access)
  assert.equal(detail?.traceId, records[6].traceId, '使用记录详情应通过新格式 usage id 直接定位 shard')
  assert.equal(detail?.requestedReasoningEffort, 'low', 'SQLite 使用记录详情应保留请求思考级别')
  assert.equal(detail?.effectiveReasoningEffort, 'high', 'SQLite 使用记录详情应保留实际上游思考级别')
  assert.equal(detail?.pricingSnapshot?.accountChargeUsd, records[6].costUsd, 'SQLite 使用记录应固化请求时成本快照')
  assert.equal(detail?.pricingSnapshot?.serviceTierPricingSource, 'default', 'Default 请求应锁定标准价来源')
  const unknownPricingDetail = repositories.getUsageRecordDetail(records[7].id, access)
  assert.equal(unknownPricingDetail?.pricingSnapshot?.accountChargeUsd, records[7].costUsd, '目录未命中时仍应锁定写入时已有成本事实')
  assert.equal(unknownPricingDetail?.pricingSnapshot?.serviceTierPricingSource, 'unknown', '目录未命中时应写入 unknown 快照，禁止未来重新解释新记录')

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(1000), records.length, '统计聚合应从 usage shard 读取完整业务记录')
  assert.equal(apiKeyStatsTotal(apiKey.id), records.length, 'API Key 统计应按 shard 输入聚合到统计结果库')
  assert(usageShardCursorCount() > 1, '统计结果库应为多个 usage shard 维护独立游标')

  const offsetCreatedAt = '2026-08-16T09:00:00.000+09:00'
  const strictTimestampRecord = {
    ...records[0],
    id: usageRecordShards.generateUsageRecordId(offsetCreatedAt, 'offset-canonical'),
    traceId: 'trace-usage-shard-routing-offset-canonical',
    createdAt: offsetCreatedAt
  }
  repositories.createUsageRecord(strictTimestampRecord)
  assert.equal(
    repositories.getUsageRecordDetail(strictTimestampRecord.id, access)?.createdAt,
    '2026-08-16T00:00:00.000Z',
    '使用记录 numeric offset 写入后必须 canonical 为 UTC Z'
  )
  const corruptedRecordLocation = usageRecordShards.usageRecordShardLocationForRecord(strictTimestampRecord.id)
  usageRecordShards.getUsageRecordShardDatabase(corruptedRecordLocation)
    .prepare('UPDATE usage_records SET created_at = ? WHERE id = ?')
    .run('2026-08-16T00:00:00.000', strictTimestampRecord.id)
  assert.throws(
    () => repositories.getUsageRecordDetail(strictTimestampRecord.id, access),
    /使用记录 created_at必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '持久化裸 created_at 必须可见失败，不能在读取详情时按本机时区解释'
  )
  assert.throws(
    () => repositories.createUsageRecord({
      ...strictTimestampRecord,
      id: 'usage_invalid_created_at',
      apiKeyId: 'api-key-that-would-be-skipped',
      createdAt: '2026-08-16T09:00:00.000'
    }),
    /使用记录 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '使用记录裸 createdAt 即使该行会被跳过也必须可见失败，不能按本机时区或当前时间回退'
  )
  assert.throws(
    () => usageStatsRepository.aggregateUsageStatsBatch(1, '2026-08-16T09:00:00.000'),
    /用量统计 safeCreatedBefore必须是带 Z 或数值 offset 的 RFC3339 时间/,
    '统计聚合裸 safeCreatedBefore 必须可见失败'
  )

  console.log('使用记录分片写入回归通过：新 usage 落到多个 shard，usage catalog 独立建表，统计可从 shard 聚合')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertCurrentShardRegistry(): void {
  const tables = new Set(
    (databaseModule.getUsageCatalogDatabase()
      .prepare("SELECT name FROM sqlite_master WHERE type = 'table'")
      .all() as Array<{ name?: string }>)
      .map((row) => row.name)
      .filter((name): name is string => Boolean(name))
  )
  assert(tables.has('usage_record_shards'), '使用记录目录库应创建 usage shard 注册表')
  assert(tables.has('usage_record_shard_entries'), '使用记录目录库应创建 usage shard 记录索引表')
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
