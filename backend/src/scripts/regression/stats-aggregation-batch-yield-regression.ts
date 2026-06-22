import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const statsWriterSource = readFileSync(resolve('src/modules/background/background-stats-writer.ts'), 'utf8')
const sqliteDoc = readFileSync(resolve('../docs/functions/SQLite存储说明.md'), 'utf8')
const coreDoc = readFileSync(resolve('../docs/functions/核心功能设计.md'), 'utf8')

assert(
  statsWriterSource.includes('const statsAggregationBatchPauseMs = 25'),
  'stats 聚合连续批次之间必须保留固定 SQLite 写入间隙'
)
assert(
  statsWriterSource.includes('async function aggregateUsageStats('),
  'usage 统计聚合必须是异步批次，不能用同步 for 循环连续占用 stats-worker'
)
assert(
  statsWriterSource.includes('async function aggregateClientIpStats('),
  'client IP 统计聚合必须是异步批次，不能用同步 for 循环连续占用 stats-worker'
)
assert(
  statsWriterSource.includes('return await aggregateUsageStats(operation.batchSize, operation.maxBatches)'),
  'handleStatsWriteOperation 必须等待 usage 统计异步批次'
)
assert(
  statsWriterSource.includes('return await aggregateClientIpStats(operation.batchSize, operation.maxBatches, operation.maxRunMs)'),
  'handleStatsWriteOperation 必须等待 client IP 统计异步批次'
)
assert(
  statsWriterSource.includes('await yieldToEventLoop()') && statsWriterSource.includes('await pauseBetweenStatsAggregationBatches()'),
  'stats 聚合继续下一批前必须让出事件循环并短暂停顿'
)
assert(
  sqliteDoc.includes('统计缓存每轮聚合批量上限；连续批次之间让出事件循环，并在继续下一批前固定等待 25ms'),
  'SQLite 存储说明必须记录 stats 聚合批间让出和节流口径'
)
assert(
  coreDoc.includes('连续批次之间必须让出事件循环，并在继续下一批前固定等待 25ms'),
  '核心功能设计必须记录 stats 聚合批间让出和节流口径'
)

console.log('统计聚合批间让出回归通过：usage/client IP 聚合不会同步连续占用 stats-worker')
