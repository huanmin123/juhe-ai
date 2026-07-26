import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { getBackgroundJobRegistryEntry } from '../../modules/background/background-job-registry.js'

const rangeWindowSource = readSource('../../storage/client-ip-usage-range-windows.repository.ts')
const aggregationSource = readSource('../../storage/client-ip-stats-aggregation.repository.ts')
const statsWriterSource = readSource('../../modules/background/background-stats-writer.ts')
const backgroundJobsSource = readSource('../../modules/background/background-jobs.ts')
const statsSchemaSource = readSource('../../storage/schema/stats-schema.ts')

assert.match(
  statsSchemaSource,
  /CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips[\s\S]*?generation INTEGER NOT NULL DEFAULT 1[\s\S]*?first_dirty_at TEXT NOT NULL/,
  'client_ip dirty 表必须保存 generation 和 first_dirty_at'
)
assert.match(
  statsSchemaSource,
  /CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips[\s\S]*?generation INTEGER NOT NULL DEFAULT 1[\s\S]*?first_dirty_at TEXT NOT NULL/,
  'client_ip_account dirty 表必须保存 generation 和 first_dirty_at'
)
assert.match(rangeWindowSource, /generation = client_ip_range_window_dirty_ips\.generation \+ 1/, 'client_ip 重复标脏必须递增 generation')
assert.match(rangeWindowSource, /generation = client_ip_account_range_window_dirty_ips\.generation \+ 1/, 'client_ip_account 重复标脏必须递增 generation')
assert.match(rangeWindowSource, /ORDER BY MIN\(first_dirty_at\) ASC, ip_hash ASC/, 'dirty 领取必须按首次标脏时间公平排序')
assert.equal(countMatches(rangeWindowSource, /FOR UPDATE SKIP LOCKED/g) >= 2, true, '两张 PG dirty 表领取都必须锁住已读取代次')
assert.equal(countMatches(rangeWindowSource, /dirty\.generation = claimed\.generation/g) >= 1, true, 'PG dirty 清理必须按 generation CAS')
assert.doesNotMatch(rangeWindowSource, /clearAllClientIpRangeWindowDirtyIpHashes/, 'full 和 stale 自愈不得无条件清空并发 dirty marker')

assert.match(
  backgroundJobsSource,
  /backgroundScheduledJobName\('client-ip-stats-aggregation'\)[\s\S]*?runWithPostgresScheduledLease\('client-ip-stats-aggregation', minuteMs, signal, runClientIpStatsAggregation\)/,
  'client-ip stats scheduled job 必须获取共享 PostgreSQL lease'
)
assert.match(
  statsWriterSource,
  /case 'aggregate_client_ip_stats':[\s\S]*?requiredPostgresScheduledLease\(operation\)/,
  'stats writer 必须要求 client-ip operation 携带 scheduled lease'
)
assert.match(
  aggregationSource,
  /client\.transaction\(async \(tx\) => \{[\s\S]*?pinScheduledJobLeaseInTransaction\(tx, scheduledLease\)[\s\S]*?postgresClientIpStatsJobState\(tx\)/,
  'client-ip 聚合写事务必须先 pin scheduled lease'
)
assert.match(
  rangeWindowSource,
  /client\.transaction\(async \(tx\) => \{[\s\S]*?pinScheduledJobLeaseInTransaction\(tx, options\.scheduledLease\)[\s\S]*?takeClientIpRangeWindowDirtyIpHashesAsync/,
  'client-ip 范围窗口事务必须在领取 dirty 前 pin scheduled lease'
)

const registryEntry = getBackgroundJobRegistryEntry('client-ip-stats-aggregation')
assert(registryEntry, 'client-ip stats job 必须登记')
assert.equal(registryEntry.leaseRequired, true)
assert.equal(registryEntry.singleOwner, true, 'generation CAS 使用可复用整数代次时必须由单一 fencing lease 消费')
assert.equal(registryEntry.shardable, false, '全局 client-ip 聚合游标和范围窗口刷新不可按 worker 分片')

console.log('客户端 IP 范围窗口 dirty generation CAS 与 scheduled lease 回归通过')

function countMatches(source: string, pattern: RegExp): number {
  let count = 0
  while (pattern.exec(source) !== null) count += 1
  return count
}

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}
