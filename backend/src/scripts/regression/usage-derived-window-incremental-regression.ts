import { strict as assert } from 'node:assert'
import { readFile } from 'node:fs/promises'

const repositorySource = await readFile(new URL('../../storage/usage-stats.repository.ts', import.meta.url), 'utf8')
const overviewSource = await readFile(new URL('../../storage/usage-overview-windows.repository.ts', import.meta.url), 'utf8')
const backgroundJobsSource = await readFile(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')

const postgresAiRefresh = repositorySource.match(/async function refreshAiPerformanceSummaryWindowSnapshotsAsync[\s\S]+?\n}\n\nasync function seedAiPerformanceSummaryRolloverDirtyAccountsAsync/)?.[0] ?? ''
assert.ok(postgresAiRefresh, '应保留 PostgreSQL AI 性能摘要增量刷新函数')
assert.doesNotMatch(postgresAiRefresh, /DELETE FROM \$\{statsTable\(client, 'ai_performance_summary_windows'\)}/, '在线 AI 性能摘要刷新不得全表删除')
assert.match(postgresAiRefresh, /LIMIT 10[\s\S]+FOR UPDATE SKIP LOCKED/, 'AI 性能摘要每轮应有有界 dirty account claim')
assert.match(postgresAiRefresh, /dirty\.generation = claimed\.generation/, 'AI 性能摘要 dirty 清理必须使用 generation CAS')
assert.match(repositorySource, /ON CONFLICT\(system_account_id, window_key\) DO UPDATE SET/, 'AI 性能摘要应使用幂等 upsert 发布局部窗口')

const postgresQuotaRefresh = repositorySource.match(/export async function refreshUsageQuotaHourlyWindowsCacheAsync[\s\S]+?\n}\n\nexport async function refreshHotUsageWindowSnapshots/)?.[0] ?? ''
assert.ok(postgresQuotaRefresh, '应保留 PostgreSQL quota 窗口刷新函数')
assert.doesNotMatch(postgresQuotaRefresh, /DELETE FROM juhe_stats\.usage_quota_hourly_windows\s*`/, '在线 quota 窗口刷新不得全表删除')
assert.match(postgresQuotaRefresh, /LIMIT \?[\s\S]+FOR UPDATE SKIP LOCKED/, 'quota 窗口刷新必须有动态 scope 预算')
assert.match(postgresQuotaRefresh, /dirty\.generation = claimed\.generation/, 'quota dirty 清理必须使用 generation CAS')

assert.match(overviewSource, /LIMIT 8[\s\S]+FOR UPDATE SKIP LOCKED/, '概览窗口刷新必须限制单轮 dirty scope 数')
assert.match(overviewSource, /dirty\.generation = claimed\.generation/, '概览窗口 dirty 清理必须使用 generation CAS')
assert.match(repositorySource, /usageRankSnapshotStagesHavePendingWorkAsync/, 'dirty 队列未排空时不能被 source watermark 跳过')
assert.match(backgroundJobsSource, /ai-performance-summary-windows-refresh[\s\S]+intervalMs: 5 \* minuteMs/, 'AI 性能摘要应拆为独立错峰任务')
assert.match(backgroundJobsSource, /postgresUsageRankSnapshotCoreStageNames/, 'PostgreSQL TopN 组合任务应排除 AI 性能摘要全量阶段')

console.log('usage derived window incremental regression passed')
