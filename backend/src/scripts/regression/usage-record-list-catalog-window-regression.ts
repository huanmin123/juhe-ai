import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-list-catalog-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
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
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }
  const group = repositories.createGroup({ name: '使用记录 catalog 窗口回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '使用记录 catalog 窗口回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-catalog-window',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
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
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
    assert.equal(deepPage.items[0]?.id, records[300].id, '深翻页应只读取固定 shard 候选窗口内的末尾记录')
    assert.equal(deepPage.hasMore, true, 'shard 多取一条应标记窗口内还有后续记录')
  } finally {
    for (const restore of shardRestorers) restore()
  }

  assert(shardCalls.length > 1, '使用记录列表应按日期窗口直接查询多个 shard')
  assert(shardCalls.every((call) => /\bFROM\s+usage_records\s+ur\b/i.test(call.sql)), '使用记录列表候选窗口应直接读取 shard 明细表')
  assert(shardCalls.every((call) => /\bur\.system_account_id\s+=\s+\?/i.test(call.sql)), '使用记录列表 shard 查询必须带系统账户过滤')
  assert(shardCalls.every((call) => Number(call.params.at(-1)) === 1001), '深翻页 shard 候选窗口应固定为 offset + pageSize + 1 且最大 1001')

  const planDetails = usageRecordShards.getUsageRecordShardDatabase(usageRecordShards.listUsageRecordShardLocations()[0])
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT id
      FROM usage_records ur
      WHERE ur.system_account_id = ?
      ORDER BY ur.created_at DESC, ur.id DESC
      LIMIT ?
    `)
    .all('sys_admin', 1001)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(planDetails.includes('idx_usage_records_system_account_created_sort'), `shard created_at 排序应使用用户时间索引，实际计划：${planDetails}`)

  console.log('使用记录列表 shard 窗口回归通过：请求列表按系统账户直接读取 usage shard 的固定候选窗口')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
