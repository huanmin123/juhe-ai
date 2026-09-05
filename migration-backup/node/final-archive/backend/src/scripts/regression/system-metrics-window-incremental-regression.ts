import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageRankSnapshotStageName } from '../../storage/usage-stats.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-window-incremental-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-metrics-window-incremental-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository, usageStatsHelpers, usageStatsWindowHelpers, systemMetricsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/usage-stats-window-helpers.js'),
  import('../../storage/system-metrics.repository.js')
])

const jobName = 'system-metrics-trend-windows-refresh-incremental-regression'
const stageNames: UsageRankSnapshotStageName[] = ['system_metrics_trend_windows']
const sentinelUpdatedAt = '1999-12-31T00:00:00.000Z'
const changedSourceUpdatedAt = '2999-01-01T00:00:00.000Z'
const outsideRangeSourceUpdatedAt = '3000-01-01T00:00:00.000Z'
const rollbackWatermark = '3999-01-01T00:00:00.000Z'

try {
  const database = databaseModule.getStatsDatabase()
  const timezone = usageStatsHelpers.usageStatsTimezone()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate
  const fixedDates = usageStatsWindowHelpers.fixedUsageStatsDateKeys(timezone, today)
  const yesterday = fixedDates[fixedDates.length - 2]
  assert.ok(yesterday, '系统指标增量回归需要至少两个固定日期')
  const yesterdayHour = `${yesterday}T00`
  const todayHour = `${today}T00`

  seedSamples(yesterdayHour, timezone, 10, 15)
  seedSamples(todayHour, timezone, 20, 25)
  const initialSourceState = systemMetricsRepository.systemMetricsTrendSourceState(database)
  const initialSourceWatermark = initialSourceState.sourceWatermark
  assert.match(initialSourceWatermark, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, '系统指标源水位必须是独立 canonical UTC 时间')
  assert(!initialSourceWatermark.includes('|'), '系统指标源水位不得拼接 digest 或其他复合字段')
  assert.equal(systemMetricsRepository.systemMetricsTrendSourceWatermark(database), initialSourceWatermark, '水位读取接口应保持纯 RFC3339 语义')
  assert.match(initialSourceState.sourceVersion, /^v2:[a-f0-9]{64}$/, '系统指标源版本应使用独立摘要字段')

  const first = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(first.skipped, false, '首次系统指标趋势刷新不应跳过')
  assert.ok(systemTrendUpdatedAt(yesterday, yesterday), '首次刷新应生成昨日系统趋势窗口')
  assert.ok(processTrendUpdatedAt(yesterday, yesterday), '首次刷新应生成昨日进程趋势窗口')
  assertRefreshState(jobName, initialSourceState.sourceWatermark, today, initialSourceState.sourceVersion)

  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(`${initialSourceState.sourceWatermark}|${initialSourceState.sourceVersion}`, jobName)
  database.prepare(`
    DELETE FROM stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = ?
  `).run(jobName)
  const legacyLayoutRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(legacyLayoutRefresh.skipped, false, '旧版复合水位必须执行一次迁移刷新，不能误判为可跳过')
  assertRefreshState(jobName, initialSourceState.sourceWatermark, today, initialSourceState.sourceVersion)

  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(`${initialSourceState.sourceWatermark}|${initialSourceState.sourceVersion}`, jobName)
  const legacyLayoutWithSecondaryRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(legacyLayoutWithSecondaryRefresh.skipped, false, '旧版主状态与匹配 secondary 同存时也必须执行一次迁移刷新')
  assertRefreshState(jobName, initialSourceState.sourceWatermark, today, initialSourceState.sourceVersion)
  const postMigrationSkip = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(postMigrationSkip.skipped, true, '迁移完成后的第二次刷新才允许按未变化跳过')

  const mismatchedLegacyLayoutJobName = `${jobName}:mismatched-legacy-layout`
  await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName: mismatchedLegacyLayoutJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(`${initialSourceState.sourceWatermark}|${initialSourceState.sourceVersion}`, mismatchedLegacyLayoutJobName)
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_id = ?
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = ?
  `).run(`v2:${'0'.repeat(64)}`, mismatchedLegacyLayoutJobName)
  await assert.rejects(
    usageStatsRepository.refreshUsageRankSnapshotsInStages({
      stageNames,
      skipIfUnchanged: true,
      jobName: mismatchedLegacyLayoutJobName,
      yieldToEventLoop: async () => {}
    }),
    /legacy sourceVersion 状态与当前状态不一致/,
    'legacy 主状态与 secondary 不一致时必须可见失败'
  )
  const mismatchedPrimary = database.prepare(`
    SELECT cursor_created_at FROM stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).get(mismatchedLegacyLayoutJobName) as { cursor_created_at: string }
  const mismatchedSecondary = database.prepare(`
    SELECT cursor_id FROM stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = ?
  `).get(mismatchedLegacyLayoutJobName) as { cursor_id: string }
  assert.equal(mismatchedPrimary.cursor_created_at, `${initialSourceState.sourceWatermark}|${initialSourceState.sourceVersion}`, '不一致 legacy 主状态不得被部分覆盖')
  assert.equal(mismatchedSecondary.cursor_id, `v2:${'0'.repeat(64)}`, '不一致 legacy secondary 不得被部分覆盖')

  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare(`
    UPDATE system_metrics_hourly
    SET cpu_percent_sum = 90,
      cpu_percent_max = 90,
      updated_at = ?
    WHERE stat_hour = ?
  `).run(changedSourceUpdatedAt, todayHour)
  database.prepare(`
    UPDATE process_event_loop_hourly
    SET event_loop_lag_ms_sum = 45,
      event_loop_lag_ms_count = 1,
      event_loop_lag_ms_max = 45,
      updated_at = ?
    WHERE stat_hour = ?
      AND process_role = 'server'
  `).run(changedSourceUpdatedAt, todayHour)

  const incremental = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(incremental.skipped, false, '同日源水位变化后应执行系统指标增量刷新')
  assert.equal(systemTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '仅今日变化时不应重写昨日系统趋势窗口')
  assert.equal(processTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '仅今日变化时不应重写昨日进程趋势窗口')
  assert.equal(refreshedWindowCount('system_metrics_trend_windows'), 31, '今日变化应只重写 31 个系统趋势范围')
  assert.equal(refreshedWindowCount('process_event_loop_trend_windows'), 31, '今日变化应只重写 31 个进程趋势范围')

  const overview = usageStatsRepository.getSystemMetricsOverview({
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  })
  const systemBucket = overview.hourlyTrend.find((row) => row.statHour === todayHour)
  assert.equal(systemBucket?.cpuPercentAvg, 90, '增量系统趋势应读取更新后的系统小时聚合')
  const processBucket = overview.processEventLoopTrend.find((row) => row.statHour === todayHour && row.processRole === 'server')
  assert.equal(processBucket?.eventLoopLagMsAvg, 45, '增量进程趋势应读取更新后的进程小时聚合')

  const skipped = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, '系统指标源水位不变时应跳过')

  const sourceStateBeforeSameMillisecondTie = systemMetricsRepository.systemMetricsTrendSourceState(database)
  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare(`
    UPDATE system_metrics_hourly
    SET cpu_percent_sum = 70,
      cpu_percent_max = 70,
      updated_at = ?
    WHERE stat_hour = ?
  `).run(changedSourceUpdatedAt, todayHour)
  const sameMillisecondTieRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  const sourceStateAfterSameMillisecondTie = systemMetricsRepository.systemMetricsTrendSourceState(database)
  assert.equal(sameMillisecondTieRefresh.skipped, false, '同一 updated_at 下源行内容变化不应跳过')
  assert.equal(sameMillisecondTieRefresh.sourceWatermark, changedSourceUpdatedAt, '同毫秒变化不得污染纯 sourceWatermark')
  assert.equal(sourceStateAfterSameMillisecondTie.sourceWatermark, sourceStateBeforeSameMillisecondTie.sourceWatermark, '同毫秒变化的时间水位应保持不变')
  assert.notEqual(sourceStateAfterSameMillisecondTie.sourceVersion, sourceStateBeforeSameMillisecondTie.sourceVersion, '同毫秒变化必须更新独立 sourceVersion')
  assert.equal(refreshedWindowCount('system_metrics_trend_windows'), 31, '同毫秒变化应重写受影响的系统趋势范围')
  assert.equal(refreshedWindowCount('process_event_loop_trend_windows'), 31, '同毫秒变化应重写受影响的进程趋势范围')
  assertRefreshState(jobName, changedSourceUpdatedAt, today, sourceStateAfterSameMillisecondTie.sourceVersion)
  const sameMillisecondTieOverview = usageStatsRepository.getSystemMetricsOverview({
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  })
  assert.equal(sameMillisecondTieOverview.hourlyTrend.find((row) => row.statHour === todayHour)?.cpuPercentAvg, 70, '同毫秒变化应刷新系统趋势聚合')

  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare(`
    INSERT INTO system_metrics_hourly (stat_hour, sample_count, updated_at)
    VALUES ('1990-01-01T00', 1, ?)
  `).run(outsideRangeSourceUpdatedAt)
  const outsideRangeRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(outsideRangeRefresh.skipped, false, '范围外源变化仍应推进水位')
  assert.equal(refreshedWindowCount('system_metrics_trend_windows'), 0, 'changedDates 为空不应误退化为系统趋势全量刷新')
  assert.equal(refreshedWindowCount('process_event_loop_trend_windows'), 0, 'changedDates 为空不应误退化为进程趋势全量刷新')
  database.prepare("DELETE FROM system_metrics_hourly WHERE stat_hour = '1990-01-01T00'").run()

  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare("UPDATE stats_job_state SET cursor_id = ? WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?").run(yesterday, jobName)
  const dateRolloverRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(dateRolloverRefresh.skipped, false, '刷新日期变化时应执行全量重建')
  assert.notEqual(systemTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '刷新日期变化全量重建应重写昨日系统趋势窗口')
  assert.notEqual(processTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '刷新日期变化全量重建应重写昨日进程趋势窗口')

  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type IN ('global', 'usage_rank_snapshot_source_version')
      AND scope_id = ''
      AND job_name = ?
  `).run(rollbackWatermark, jobName)
  const rollbackRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(rollbackRefresh.skipped, false, '源水位倒退时应执行全量安全回退')
  assert.notEqual(systemTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '水位倒退全量回退应重写昨日系统趋势窗口')
  assert.notEqual(processTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '水位倒退全量回退应重写昨日进程趋势窗口')

  const missingVersionJobName = `${jobName}:missing-version`
  await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName: missingVersionJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    DELETE FROM stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version'
      AND scope_id = ''
      AND job_name = ?
  `).run(missingVersionJobName)
  await assert.rejects(
    usageStatsRepository.refreshUsageRankSnapshotsInStages({
      stageNames,
      skipIfUnchanged: true,
      jobName: missingVersionJobName,
      yieldToEventLoop: async () => {}
    }),
    /sourceVersion 状态缺失/,
    '缺失的独立 sourceVersion 状态必须可见失败'
  )

  const invalidVersionJobName = `${jobName}:invalid-version`
  await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName: invalidVersionJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_id = 'not-a-source-version'
    WHERE scope_type = 'usage_rank_snapshot_source_version'
      AND scope_id = ''
      AND job_name = ?
  `).run(invalidVersionJobName)
  await assert.rejects(
    usageStatsRepository.refreshUsageRankSnapshotsInStages({
      stageNames,
      skipIfUnchanged: true,
      jobName: invalidVersionJobName,
      yieldToEventLoop: async () => {}
    }),
    /sourceVersion 状态.*64 位小写十六进制摘要/,
    '非法的独立 sourceVersion 状态必须可见失败'
  )

  const invalidLegacyLayoutJobName = `${jobName}:invalid-legacy-layout`
  await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName: invalidLegacyLayoutJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(`${initialSourceState.sourceWatermark}|not-a-source-version`, invalidLegacyLayoutJobName)
  await assert.rejects(
    usageStatsRepository.refreshUsageRankSnapshotsInStages({
      stageNames,
      skipIfUnchanged: true,
      jobName: invalidLegacyLayoutJobName,
      yieldToEventLoop: async () => {}
    }),
    /legacy sourceVersion.*64 位小写十六进制摘要/,
    '未知 legacy 复合水位不得静默迁移'
  )

  const nonSystemJobName = `${jobName}:non-system-legacy-layout`
  await usageStatsRepository.refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName: nonSystemJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(`${initialSourceState.sourceWatermark}|${initialSourceState.sourceVersion}`, nonSystemJobName)
  await assert.rejects(
    usageStatsRepository.refreshHotUsageWindowSnapshots({
      skipIfUnchanged: true,
      jobName: nonSystemJobName,
      yieldToEventLoop: async () => {}
    }),
    /sourceWatermark必须是带 Z 或数值 offset 的 RFC3339 时间/,
    'legacy 复合水位不得泄漏到非系统指标任务'
  )

  const legacyEmptyWatermarkJobName = `${jobName}:legacy-empty-watermark`
  await usageStatsRepository.refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName: legacyEmptyWatermarkJobName,
    yieldToEventLoop: async () => {}
  })
  database.prepare(`
    UPDATE stats_job_state
    SET cursor_created_at = '0000-00-00T00:00:00.000Z'
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).run(legacyEmptyWatermarkJobName)
  await usageStatsRepository.refreshHotUsageWindowSnapshots({
    skipIfUnchanged: true,
    jobName: legacyEmptyWatermarkJobName,
    yieldToEventLoop: async () => {}
  })
  const migratedLegacyEmptyState = database.prepare(`
    SELECT cursor_created_at
    FROM stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).get(legacyEmptyWatermarkJobName) as { cursor_created_at?: string } | undefined
  assert.match(migratedLegacyEmptyState?.cursor_created_at ?? '', /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, '旧空水位应迁移为 canonical UTC')
  assert.notEqual(migratedLegacyEmptyState?.cursor_created_at, '0000-00-00T00:00:00.000Z', '旧空水位不得继续持久化')

  console.log('系统指标趋势窗口增量回归通过：纯 canonical 水位与独立 sourceVersion、同毫秒变更、范围外变化、日期变更和水位倒退均正确处理')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedSamples(statHour: string, timezone: string, cpuPercent: number, processLagMs: number): void {
  const sampledAt = sampledAtForStatHour(statHour, timezone)
  usageStatsRepository.insertSystemMetricsSample({
    sampledAt,
    cpuPercent,
    memoryUsedPercent: 50,
    eventLoopLagMs: cpuPercent
  })
  usageStatsRepository.insertProcessEventLoopSample({
    sampledAt,
    processRole: 'server',
    processPid: 12345,
    eventLoopLagMs: processLagMs,
    processRssBytes: 1024,
    processHeapUsedBytes: 512,
    processHeapTotalBytes: 768
  })
}

function sampledAtForStatHour(targetStatHour: string, timezone: string): string {
  const statDate = targetStatHour.slice(0, 10)
  const startMs = Date.parse(`${statDate}T00:00:00.000Z`) - 36 * 60 * 60 * 1000
  const endMs = startMs + 96 * 60 * 60 * 1000
  for (let timestamp = startMs; timestamp <= endMs; timestamp += 15 * 60 * 1000) {
    const candidate = new Date(timestamp)
    if (usageStatsHelpers.hourKey(candidate, timezone) === targetStatHour) {
      return candidate.toISOString()
    }
  }
  throw new Error(`无法为 ${targetStatHour} 生成匹配时区 ${timezone} 的 sampled_at`)
}

function systemTrendUpdatedAt(startDate: string, endDate: string): string | undefined {
  return trendUpdatedAt('system_metrics_trend_windows', startDate, endDate)
}

function processTrendUpdatedAt(startDate: string, endDate: string): string | undefined {
  return trendUpdatedAt('process_event_loop_trend_windows', startDate, endDate)
}

function trendUpdatedAt(tableName: string, startDate: string, endDate: string): string | undefined {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT updated_at AS updatedAt
    FROM ${tableName}
    WHERE window_key = ?
    LIMIT 1
  `).get(usageStatsWindowHelpers.rangeWindowKey({ startDate, endDate })) as { updatedAt?: string } | undefined
  return row?.updatedAt
}

function refreshedWindowCount(tableName: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT COUNT(DISTINCT window_key) AS count
    FROM ${tableName}
    WHERE updated_at <> ?
  `).get(sentinelUpdatedAt) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function assertRefreshState(jobName: string, sourceWatermark: string, refreshDate: string, sourceVersion: string): void {
  const database = databaseModule.getStatsDatabase()
  const mainState = database.prepare(`
    SELECT cursor_created_at, cursor_id
    FROM stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
  `).get(jobName) as { cursor_created_at?: string; cursor_id?: string } | undefined
  assert.equal(mainState?.cursor_created_at, sourceWatermark, '主刷新状态 cursor_created_at 必须仅保存 sourceWatermark')
  assert.equal(mainState?.cursor_id, refreshDate, '主刷新状态 cursor_id 必须保留 refreshDate 语义')
  assert.match(mainState?.cursor_created_at ?? '', /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, '主刷新状态时间必须是 canonical UTC')
  assert(!mainState?.cursor_created_at?.includes('|'), '主刷新状态时间字段不得混入版本摘要')

  const versionState = database.prepare(`
    SELECT cursor_created_at, cursor_id
    FROM stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = ?
  `).get(jobName) as { cursor_created_at?: string; cursor_id?: string } | undefined
  assert.equal(versionState?.cursor_created_at, sourceWatermark, '独立版本状态仍必须用 canonical 水位关联主状态')
  assert.equal(versionState?.cursor_id, sourceVersion, '独立版本状态必须保存 sourceVersion')
  assert.match(versionState?.cursor_id ?? '', /^v2:[a-f0-9]{64}$/, '独立版本状态不得写入时间字段或裸文本')
}
