import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const jobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
const writerSource = readFileSync(resolve('src/modules/background/background-stats-writer.ts'), 'utf8')
const repositorySource = readFileSync(resolve('src/storage/group-account-stats-cache.repository.ts'), 'utf8')
const registrySource = readFileSync(resolve('src/modules/background/background-job-registry.entries.ts'), 'utf8')

assert.match(
  jobsSource,
  /runWithPostgresScheduledLease\('group-account-stats-refresh', 2 \* minuteMs, signal, runGroupAccountStatsRefresh\)/,
  'group-account-stats-refresh 必须取得跨部署 scheduled lease'
)
assert.match(
  jobsSource,
  /requestStatsWriter\(\{ type: 'refresh_group_account_stats', scheduledLease \}\)/,
  'scheduler 必须把 fencing token 传到 stats writer'
)
assert.match(
  writerSource,
  /refreshGroupAccountStats\(requiredPostgresScheduledLease\(operation\)\)/,
  'PG stats writer 必须拒绝无 scheduled lease 的分组统计写请求'
)
assert.match(
  repositorySource,
  /client\.transaction\(async \(tx\) => \{\s*await pinScheduledJobLeaseInTransaction\(tx, scheduledLease\)\s*return await refreshDirtyGroupAccountStatsCacheInClient\(tx, normalizedLimit\)/,
  '缓存写入与 dirty CAS 删除必须位于 pin lease 的同一事务'
)
assert.match(
  registrySource,
  /jobName: 'group-account-stats-refresh'[\s\S]*?singleOwner: true,[\s\S]*?leaseRequired: true,[\s\S]*?writes: \['business:group_account_stats_dirty', 'stats:group_account_stats'\]/,
  'registry 必须与实际 single-owner lease 和写入目标一致'
)

console.log('分组账户统计 scheduled lease / fencing 静态回归通过')
