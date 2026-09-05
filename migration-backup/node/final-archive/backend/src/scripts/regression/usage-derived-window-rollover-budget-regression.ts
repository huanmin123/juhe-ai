import { strict as assert } from 'node:assert'
import { readFile } from 'node:fs/promises'

import {
  derivedWindowRolloverSeedMaxPages,
  derivedWindowRolloverSeedPageSize,
  runDerivedWindowRolloverSeedPages
} from '../../storage/usage-derived-window-rollover.js'

const overviewSource = await readFile(new URL('../../storage/usage-overview-windows.repository.ts', import.meta.url), 'utf8')
const repositorySource = await readFile(new URL('../../storage/usage-stats.repository.ts', import.meta.url), 'utf8')
const schemaSource = await readFile(new URL('../../storage/schema/stats-schema.ts', import.meta.url), 'utf8')
const backgroundJobsSource = await readFile(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')

const allAccounts = [...Array.from({ length: 5000 }, (_, index) => ({
  system_account_id: `sys_${String(index).padStart(5, '0')}`
})), { system_account_id: 'global' }].sort((left, right) => left.system_account_id.localeCompare(right.system_account_id))
let cursor = ''
const seeded = new Set<string>()
let runCount = 0
do {
  const progress = await runDerivedWindowRolloverSeedPages({
    cursor,
    loadPage: async (pageCursor, pageSize) => allAccounts
      .filter((row) => row.system_account_id !== 'global' && row.system_account_id > pageCursor)
      .slice(0, pageSize),
    seedPage: async (rows) => {
      rows.forEach((row) => seeded.add(row.system_account_id))
    }
  })
  assert.ok(progress.rowCount <= derivedWindowRolloverSeedPageSize * derivedWindowRolloverSeedMaxPages)
  cursor = progress.nextCursor
  runCount += 1
} while (cursor !== '__done__')

assert.equal(seeded.size, 5000, 'bounded keyset seed 必须覆盖 5000 个账户')
assert.equal(runCount, 3, '2048 行单轮预算应在三轮内 seed 5000 个账户')
assert.match(overviewSource, /system_account_id <> \?[\s\S]+system_account_id > \?[\s\S]+LIMIT \?/, 'overview seed 查询必须在 keyset LIMIT 前排除 global')
assert.match(repositorySource, /usage_stats_totals'\)\}[\s\S]+scope_type = 'system_account'[\s\S]+system_account_id <> \?[\s\S]+UNION[\s\S]+ai_performance_summary_windows'\)\}[\s\S]+system_account_id <> \?/, 'AI seed 两个来源必须在分页前排除 global')
assert.match(overviewSource, /usageOverviewRolloverRowsPerScope = 1185[\s\S]+usageOverviewRolloverSnapshotRowBudget = 33_180[\s\S]+usageOverviewDirtyClaimLimit = Math\.floor/, 'overview 必须保留 28 scope rollover 基准预算')
assert.match(overviewSource, /usageOverviewDirtyCandidateLimit = usageOverviewDirtyClaimLimit \* 4[\s\S]+LIMIT \$\{usageOverviewDirtyCandidateLimit\}[\s\S]+snapshotRowCount \+ estimatedRows > usageOverviewRolloverSnapshotRowBudget/, 'overview 必须从有界候选中按每个 dirty 的实际窗口行数动态 claim')
assert.match(overviewSource, /rangesBySystemAccountId[\s\S]+minEndDateBySystemAccountId/, 'overview 必须按账户自己的 dirty 日期重算和删除窗口')
assert.equal((overviewSource.match(/INNER JOIN selected_scopes selected/g) ?? []).length, 2, 'overview daily/hourly 源读取必须按本轮 scope 批量查询')
const modelRefresh = overviewSource.match(/async function refreshUsageModelRankWindowSnapshotsAsync[\s\S]+?\n}/)?.[0] ?? ''
const errorRefresh = overviewSource.match(/async function refreshUsageErrorRankWindowSnapshotsAsync[\s\S]+?\n}/)?.[0] ?? ''
assert.match(modelRefresh, /system_account_id = ANY\(\?::text\[\]\)/, 'overview model 源读取必须按本轮账户批量查询')
assert.match(errorRefresh, /system_account_id = ANY\(\?::text\[\]\)/, 'overview error 源读取必须按本轮账户批量查询')
assert.match(repositorySource, /aiPerformanceRolloverRowsPerAccount = 31[\s\S]+aiPerformanceRolloverSnapshotRowBudget = 1984[\s\S]+aiPerformanceDirtyClaimLimit = Math\.floor[\s\S]+LIMIT \$\{aiPerformanceDirtyClaimLimit\}[\s\S]+FOR UPDATE SKIP LOCKED/, 'AI 性能摘要单轮必须按 snapshot row budget 动态计算 64 account claim 上限')
const aiRefresh = repositorySource.match(/async function refreshAiPerformanceSummaryWindowSnapshotsAsync[\s\S]+?\n}\n\nasync function seedAiPerformanceSummaryRolloverDirtyAccountsAsync/)?.[0] ?? ''
assert.equal((aiRefresh.match(/FROM \$\{statsTable\(client, 'usage_stats_daily'\)\}/g) ?? []).length, 1, 'AI 性能摘要本轮账户必须合并为一次源查询')
assert.match(aiRefresh, /system_account_id = ANY\(\?::text\[\]\)/, 'AI 性能摘要源查询必须按本轮账户数组读取')
assert.match(aiRefresh, /const insertRows = dirtyWork\.flatMap[\s\S]+insertAiPerformanceSummaryWindowRowsAsync\(client, insertRows\)/, 'AI 性能摘要发布也必须合并为有界批量写入')
assert.match(backgroundJobsSource, /usageOverviewWindowRefreshIntervalMs = 5 \* minuteMs[\s\S]+usage-overview-windows-refresh'[\s\S]+intervalMs: usageOverviewWindowRefreshIntervalMs/, 'PG overview dirty drain 必须使用独立 5 分钟周期')
assert.ok(28 * (24 * 60 / 5) >= 5000, 'overview rollover 独立任务每日预算必须覆盖 5000 账户并保留失败余量')
assert.ok(64 * (24 * 60 / 5) >= 5000, 'AI rollover 每日预算必须覆盖 5000 账户')
assert.match(schemaSource, /idx_usage_stats_totals_scope_seed[\s\S]+usage_stats_totals\(scope_type, system_account_id, scope_id\)/, 'usage_stats_totals seed 索引必须以 scope_type 开头')
assert.match(schemaSource, /idx_usage_overview_dirty_first_dirty[\s\S]+usage_overview_dirty_scopes\(first_dirty_at, system_account_id\)/, 'overview first_dirty 索引必须使用新名称以便已有库创建')
assert.match(schemaSource, /idx_ai_performance_summary_dirty_first_dirty[\s\S]+ai_performance_summary_dirty_system_accounts\(first_dirty_at, system_account_id\)/, 'AI first_dirty 索引必须使用新名称以便已有库创建')

console.log('usage derived window rollover budget regression passed')
