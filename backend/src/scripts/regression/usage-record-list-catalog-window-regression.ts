import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-list-catalog-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 8
runtimeConfig.secret = 'usage-record-list-catalog-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '使用记录 catalog 窗口回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: '使用记录 catalog 窗口回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-catalog-window',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '使用记录 catalog 窗口回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)

  const records = Array.from({ length: 1300 }, (_, index) => {
    const createdAt = new Date(Date.UTC(2026, 0, 1, 0, 0, 0, index)).toISOString()
    return {
      id: usageRecordShards.generateUsageRecordId(createdAt, `catalog-window-${index}`),
      traceId: `trace-usage-catalog-window-${index}`,
      trafficSource: 'gateway' as const,
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5',
      stream: false,
      statusCode: index % 3 === 0 ? 429 : 200,
      success: index % 3 !== 0,
      firstTokenMs: index,
      durationMs: 1300 - index,
      costUsd: index / 1_000_000,
      createdAt
    }
  })
  for (let index = 0; index < records.length; index += 200) {
    repositories.createUsageRecordsBatch(records.slice(index, index + 200))
  }
  assert(usageRecordShards.listUsageRecordShardLocations().length > 1, '回归需要多 usage shard 样本')

  const datasetDatabase = databaseModule.getDatasetDatabase()
  const originalDatasetPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  const listCalls: Array<{ sql: string; params: unknown[] }> = []
  datasetDatabase.prepare = ((sql: string) => {
    const statement = originalDatasetPrepare(sql)
    if (/\bFROM\s+usage_record_shard_entries\s+ue\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        listCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof datasetDatabase.prepare

  const shardCalls: Array<{ sql: string; params: unknown[] }> = []
  const shardRestorers: Array<() => void> = []
  for (const location of usageRecordShards.listUsageRecordShardLocations()) {
    const shardDatabase = usageRecordShards.getUsageRecordShardDatabase(location)
    const originalShardPrepare = shardDatabase.prepare.bind(shardDatabase) as typeof shardDatabase.prepare
    shardRestorers.push(() => {
      shardDatabase.prepare = originalShardPrepare
    })
    shardDatabase.prepare = ((sql: string) => {
      const statement = originalShardPrepare(sql)
      if (/\bFROM\s+usage_records\s+ur\b/i.test(sql)) {
        const originalAll = statement.all.bind(statement) as typeof statement.all
        statement.all = ((...params: SQLInputValue[]) => {
          shardCalls.push({ sql, params })
          return originalAll(...params)
        }) as typeof statement.all
      }
      return statement
    }) as typeof shardDatabase.prepare
  }

  try {
    const deepPage = repositories.listUsageRecords(access, { page: 999999, pageSize: 1 })
    assert.equal(deepPage.page, 1000, '使用记录深翻页应按固定候选窗口收敛到最多 1000 页')
    assert.equal(deepPage.items.length, 1, '深翻页仍应返回窗口内最后一条记录')
    assert.equal(deepPage.items[0]?.id, records[300].id, '深翻页应只读取固定 catalog 窗口内的末尾记录')
    assert.equal(deepPage.hasMore, true, 'catalog 多取一条应标记窗口内还有后续记录')
  } finally {
    datasetDatabase.prepare = originalDatasetPrepare
    for (const restore of shardRestorers) restore()
  }

  assert.equal(listCalls.length, 1, '使用记录列表应只查一次 catalog，不应逐 shard 查询候选窗口')
  assert(/\bFROM\s+usage_record_shard_entries\s+ue\b/i.test(listCalls[0].sql), '使用记录列表应先读取全局 shard entry catalog')
  assert(!/\bFROM\s+usage_records\s+ur\b/i.test(listCalls[0].sql), '使用记录列表候选窗口不应直接扫 shard 明细表')
  assert.equal(Number(listCalls[0].params.at(-1)), 1001, '深翻页 catalog 候选窗口应固定为 offset + pageSize + 1 且最大 1001')
  assert(shardCalls.length > 0 && shardCalls.length <= usageRecordShards.listUsageRecordShardLocations().length, '详情补取只应访问当前页命中的 shard')
  assert(shardCalls.every((call) => call.params.length <= 1), 'pageSize=1 时 shard 补取只应按当前页 ID 查询')

  const planDetails = datasetDatabase
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT usage_id, shard_key, created_at
      FROM usage_record_shard_entries ue
      ORDER BY ue.created_at DESC, ue.usage_id DESC
      LIMIT ?
    `)
    .all(1001)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(planDetails.includes('idx_usage_record_shard_entries_created_sort'), `catalog created_at 排序应使用目标索引，实际计划：${planDetails}`)

  console.log('使用记录列表 catalog 窗口回归通过：请求列表只读取固定 entry 窗口，再按当前页 ID 补取 shard 明细')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
