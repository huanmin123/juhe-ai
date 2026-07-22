import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const statsWriterSource = readFileSync(resolve('src/modules/background/background-stats-writer.ts'), 'utf8')
const backgroundJobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
const usageStatsRepositorySource = readFileSync(resolve('src/storage/usage-stats.repository.ts'), 'utf8')
const usageStatsWritersSource = readFileSync(resolve('src/storage/usage-stats-writers.ts'), 'utf8')
const usageRecordQueueSource = readFileSync(resolve('src/modules/gateway/usage/record-queue.service.ts'), 'utf8')
const backgroundIpcSource = readFileSync(resolve('src/modules/background/background-ipc.ts'), 'utf8')
const sqliteDoc = readFileSync(resolve('../docs/functions/SQLite存储说明.md'), 'utf8')
const coreDoc = readFileSync(resolve('../docs/functions/核心功能设计.md'), 'utf8')

assert(
  statsWriterSource.includes('const statsAggregationBatchPauseMs = 25'),
  'stats 聚合连续批次之间必须保留固定 SQLite 写入间隙'
)
assert(
  statsWriterSource.includes('const usageStatsAggregationOnlineBatchSizeCap = 1000'),
  'usage 统计在线聚合必须限制单批实际行数，避免设置值过大导致 stats-worker 单批长事务'
)
assert(
  statsWriterSource.includes('const usageStatsAggregationMaxRunMsCap = 60_000'),
  'usage 统计在线聚合必须限制单轮运行预算上限，避免 IPC 调用长期占用 stats-worker'
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
  statsWriterSource.includes('const result = await aggregateUsageStats(operation.batchSize, operation.maxBatches, operation.maxRunMs, operation.safeCreatedBefore)')
    && statsWriterSource.includes('return result'),
  'handleStatsWriteOperation 必须等待 usage 统计异步批次并传入运行预算和安全读取上界'
)
assert(
  statsWriterSource.includes('stoppedByTimeBudget') && statsWriterSource.includes('effectiveBatchSize'),
  'usage 统计聚合结果必须暴露时间预算截断和实际批次，便于压测与运行观测'
)
assert(
  backgroundJobsSource.includes('const usageStatsOnlineFreshnessMaxIntervalSeconds = 60')
    && backgroundJobsSource.includes('function usageStatsOnlineAggregationIntervalSeconds(): number')
    && backgroundJobsSource.includes("settingsNumber('statsAggregationIntervalSeconds', 5, 3600)")
    && backgroundJobsSource.includes('usageStatsOnlineFreshnessMaxIntervalSeconds'),
  'usage 在线统计聚合必须有 60 秒新鲜度上限，不能因系统设置误配 3600 秒造成新日空窗'
)
assert(
  backgroundJobsSource.includes("type: 'refresh_hot_usage_windows'")
    && backgroundJobsSource.includes("jobName: usageHotWindowRefreshJobName")
    && backgroundJobsSource.includes("reason !== 'date_changed'")
    && backgroundJobsSource.includes('usageHotWindowRefreshMinIntervalMs'),
  'usage 聚合后必须通过防抖热窗口刷新发布今日概览和范围窗口'
)
assert(
  backgroundJobsSource.includes('scheduleHotUsageWindowsAfterAggregation(result.processed)')
    && backgroundJobsSource.includes('function scheduleHotUsageWindowsAfterAggregation(processed: number): void')
    && backgroundJobsSource.includes('void refreshHotUsageWindowsAfterAggregation(processed).catch')
    && !backgroundJobsSource.includes('await refreshHotUsageWindowsAfterAggregation(result.processed)'),
  'usage 聚合主链路不能同步等待热窗口刷新，避免热窗口刷新慢或失败阻塞在线聚合'
)
assert(
  usageStatsRepositorySource.includes('aggregateUsageStatsRecords(database, rows, updatedAt, aggregationContext)')
    && usageStatsWritersSource.includes('export function aggregateUsageStatsRecords')
    && usageStatsWritersSource.includes('addAggregatedUsageStatsEntry')
    && usageStatsWritersSource.includes('upsertAggregatedLatencyEntries')
    && usageStatsWritersSource.includes('upsertAggregatedAccountQualityEntries'),
  'usage 统计增量聚合必须先在批内合并 scope、延迟桶和账号质量桶，避免高并发下逐条重复 upsert'
)
assert(
  !backgroundJobsSource.includes('pendingUsageQueueBlockReason')
    && backgroundJobsSource.includes('safeCreatedBefore: safety.safeCreatedBefore')
    && backgroundJobsSource.includes('oldestPendingUsageRecordCreatedAt(status)')
    && backgroundJobsSource.includes('oldestRedisStreamUsageRecordCreatedAtForStatsAggregation()')
    && backgroundJobsSource.includes('usageStatsSafeCreatedBeforeForPendingBacklog')
    && usageStatsRepositorySource.includes('export const usageStatsCursorSafetyDelaySeconds = 15'),
  'stats 聚合前不应要求队列或 Redis Stream 完全为空，但必须用最早 backlog createdAt 收窄 cursor safety 上界'
)
assert(
  usageRecordQueueSource.includes('oldestCreatedAt: oldestUsageRecordCreatedAt()'),
  'usage 本地队列 runtime 必须暴露最老业务 createdAt，便于识别超龄未落库 usage'
)
assert(
  backgroundIpcSource.includes('oldestIngestUsageRecordMessageCreatedAt'),
  'server 到 ingest-worker 的 usage IPC 队列必须暴露最老业务 createdAt，便于识别超龄未落库 usage'
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
  sqliteDoc.includes('常驻 stats-worker 在线聚合会再把 usage 单批实际处理量限制为 1000 条，并给每轮调度设置 4.5 秒运行预算'),
  'SQLite 存储说明必须记录 stats 在线聚合批次硬上限和运行预算'
)
assert(
  coreDoc.includes('常驻 stats-worker 在线聚合会再把 usage 单批实际处理量限制为 1000 条，并给每轮调度设置 4.5 秒运行预算'),
  '核心功能设计必须记录 stats 在线聚合批次硬上限和运行预算'
)
assert(
  coreDoc.includes('统计游标保留 15 秒安全延迟'),
  '核心功能设计必须记录持续写入下 stats 聚合用 cursor safety 吸收队列延迟'
)
assert(
  sqliteDoc.includes('若 pending usage 或 Redis Stream backlog 中存在超过 15 秒仍未落库的记录，本轮统计只会聚合早于该记录的安全窗口')
    && coreDoc.includes('若 pending usage 或 Redis Stream backlog 中存在超过 15 秒仍未落库的记录，stats-worker 只会聚合早于该记录的安全窗口'),
  '功能文档必须记录 backlog 会收窄本轮统计安全窗口，避免统计游标越过未落库记录'
)

console.log('统计聚合批间让出回归通过：usage/client IP 聚合不会同步连续占用 stats-worker，usage 在线聚合有批次硬上限和运行预算')
