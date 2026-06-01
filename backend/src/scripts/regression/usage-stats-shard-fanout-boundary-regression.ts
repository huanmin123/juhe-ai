import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-shard-fanout-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 64
runtimeConfig.secret = 'usage-stats-shard-fanout-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  const registeredShardCount = 40
  const bucketDateKey = utcDateKey(new Date(Date.now() - 24 * 60 * 60 * 1000))
  for (let shardId = 0; shardId < registeredShardCount; shardId += 1) {
    const shardKey = `${bucketDateKey}:s${String(shardId).padStart(2, '0')}`
    const location = usageRecordShards.usageRecordShardLocationFromKey(shardKey)
    assert(location, `测试 shard key 应可解析：${shardKey}`)
    usageRecordShards.getUsageRecordShardDatabase(location)
  }
  assert.equal(usageRecordShards.listUsageRecordShardLocations().length, registeredShardCount, '回归需要大量历史 active usage shard')

  const registryReads = captureUsageShardRegistryReads()
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100), 0, '空历史 shard 不应产生统计记录')
  const firstVisitedCount = usageShardJobStateCount()
  assert(firstVisitedCount > 0, '聚合应至少检查一个 usage shard')
  assert(firstVisitedCount < registeredShardCount, '单次聚合不应访问全部历史 active usage shard')
  assert(firstVisitedCount <= 16, '单次聚合访问 shard 数应受固定窗口上限约束')

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100), 0, '第二轮空历史 shard 仍不应产生统计记录')
  const secondVisitedCount = usageShardJobStateCount()
  assert(secondVisitedCount > firstVisitedCount, '第二轮聚合应轮转检查下一批 usage shard')
  assert(secondVisitedCount <= 32, '两轮聚合访问 shard 数应按有界窗口增长，而不是一次 fan-out 到全部 shard')
  assert(registryReads.length > 0, '回归应捕获用量统计 active shard registry 查询')
  assert(registryReads.every((call) => /\bLIMIT\s+\?/i.test(call.sql)), '用量统计 active shard registry 查询必须带 LIMIT')
  assert(registryReads.every((call) => call.rowCount < registeredShardCount), '用量统计 active shard registry 查询不应单次返回全部 active shard')
  assert(registryReads.every((call) => call.rowCount <= 17), '用量统计 active shard registry 查询只允许读取窗口大小加一行')

  console.log('用量统计 shard fan-out 边界回归通过：单次聚合只访问有界 shard 窗口并轮转推进')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function captureUsageShardRegistryReads(): Array<{ sql: string; rowCount: number; params: unknown[] }> {
  const database = databaseModule.getDatasetDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: Array<{ sql: string; rowCount: number; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bSELECT\s+shard_key\b/i.test(sql) && /\bFROM\s+usage_record_shards\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const rows = originalAll(...params) as unknown[]
        calls.push({ sql, rowCount: rows.length, params })
        return rows
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare
  return calls
}

function usageShardJobStateCount(): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT COUNT(*) AS total FROM stats_job_state WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation'")
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function utcDateKey(date: Date): string {
  return `${date.getUTCFullYear()}${String(date.getUTCMonth() + 1).padStart(2, '0')}${String(date.getUTCDate()).padStart(2, '0')}`
}
