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

const [databaseModule, usageStatsRepository, usageStatsHelpers, usageStatsWindowHelpers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/usage-stats-window-helpers.js')
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

  const first = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(first.skipped, false, '首次系统指标趋势刷新不应跳过')
  assert.ok(systemTrendUpdatedAt(yesterday, yesterday), '首次刷新应生成昨日系统趋势窗口')
  assert.ok(processTrendUpdatedAt(yesterday, yesterday), '首次刷新应生成昨日进程趋势窗口')

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

  database.prepare('UPDATE system_metrics_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare('UPDATE process_event_loop_trend_windows SET updated_at = ?').run(sentinelUpdatedAt)
  database.prepare(`
    INSERT INTO process_event_loop_hourly (
      stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
      event_loop_lag_ms_count, event_loop_lag_ms_max, updated_at
    ) VALUES (?, 'stats-worker', 1, 35, 1, 35, ?)
  `).run(todayHour, changedSourceUpdatedAt)
  const sameMillisecondTieRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(sameMillisecondTieRefresh.skipped, false, '同毫秒新增源行时 MAX(updated_at) 未变化也必须刷新')
  assert.equal(processTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '同毫秒 tie 刷新不应重写昨日窗口')
  const tieOverview = usageStatsRepository.getSystemMetricsOverview({
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  })
  assert.equal(
    tieOverview.processEventLoopTrend.find((row) => row.statHour === todayHour && row.processRole === 'stats-worker')?.eventLoopLagMsAvg,
    35,
    '同毫秒 tie 指纹变化应刷新新增进程角色'
  )

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
  database.prepare('UPDATE stats_job_state SET cursor_id = ? WHERE job_name = ?').run(yesterday, jobName)
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
  database.prepare('UPDATE stats_job_state SET cursor_created_at = ? WHERE job_name = ?').run(rollbackWatermark, jobName)
  const rollbackRefresh = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(rollbackRefresh.skipped, false, '源水位倒退时应执行全量安全回退')
  assert.notEqual(systemTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '水位倒退全量回退应重写昨日系统趋势窗口')
  assert.notEqual(processTrendUpdatedAt(yesterday, yesterday), sentinelUpdatedAt, '水位倒退全量回退应重写昨日进程趋势窗口')

  console.log('系统指标趋势窗口增量回归通过：同日按变更日期刷新，同毫秒 tie 不漏刷，范围外变化不全量，水位倒退安全回退')
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
