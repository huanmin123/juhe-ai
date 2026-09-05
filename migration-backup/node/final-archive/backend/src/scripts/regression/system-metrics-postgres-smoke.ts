import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  getSystemMetricsOverviewAsync,
  insertSystemMetricsSampleBatchAsync,
  refreshUsageRankSnapshotsInStages,
  type UsageRankSnapshotStageName
} from '../../storage/usage-stats.repository.js'
import { hourKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from '../../storage/usage-stats-window-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '系统指标 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `system_metrics_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const jobName = `system-metrics-trend-windows-refresh:${marker}`
const stageNames: UsageRankSnapshotStageName[] = ['system_metrics_trend_windows']
let statHour = ''
let statDate = ''
let sampledAt = ''
let unaffectedDate = ''

const sentinelUpdatedAt = '1999-12-31T00:00:00.000Z'
const changedSourceUpdatedAt = '2999-01-01T00:00:00.000Z'

try {
  const timezone = await usageStatsTimezoneAsync()
  const pool = await getPostgresPool()
  const candidate = await findEmptyStatHour()
  statHour = candidate.statHour
  statDate = candidate.statDate
  sampledAt = sampledAtForStatHour(statHour, timezone)
  unaffectedDate = fixedUsageStatsDateKeys(timezone).find((date) => date !== statDate) ?? ''
  assert.ok(unaffectedDate, 'PG system metrics trend 增量 smoke 需要一个不包含变更日期的固定窗口')

  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
  await insertSystemMetricsSampleBatchAsync({
    sampledAt,
    cpuPercent: 40,
    memoryUsedPercent: 60,
    memoryTotalBytes: 1024 * 1024,
    memoryFreeBytes: 512 * 1024,
    processRssBytes: 2048,
    processHeapUsedBytes: 1024,
    processHeapTotalBytes: 2048,
    eventLoopLagMs: 20,
    networkRxBytesPerSecond: 100,
    networkTxBytesPerSecond: 200,
    networkRxTotalBytes: 9000,
    networkTxTotalBytes: 12000,
    dbFileBytes: 512000,
    statsLagSeconds: 9
  }, [{
    sampledAt,
    processRole: 'server',
    processPid: 12345,
    eventLoopLagMs: 25,
    processRssBytes: 200,
    processHeapUsedBytes: 80,
    processHeapTotalBytes: 120,
    processExternalBytes: 12,
    processArrayBuffersBytes: 6
  }])

  const refreshed = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG system metrics trend refresh 不应跳过')
  assert.deepEqual(refreshed.stages.map((stage) => stage.name), stageNames, 'PG system metrics trend refresh 应执行系统指标趋势阶段')

  const primaryState = await pool.query(`
    SELECT cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = $1
  `, [jobName])
  const sourceVersionState = await pool.query(`
    SELECT cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = $1
  `, [jobName])
  const legacySourceWatermark = String(primaryState.rows[0]?.cursor_created_at ?? '')
  const legacySourceVersion = String(sourceVersionState.rows[0]?.cursor_id ?? '')
  assert.match(legacySourceWatermark, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, 'PG 初始状态必须保存 canonical sourceWatermark')
  assert.match(legacySourceVersion, /^v2:[a-f0-9]{64}$/, 'PG 初始状态必须保存独立 sourceVersion')
  await pool.query(`
    UPDATE juhe_stats.stats_job_state
    SET cursor_created_at = $1
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = $2
  `, [`${legacySourceWatermark}|${legacySourceVersion}`, jobName])
  await pool.query(`
    DELETE FROM juhe_stats.stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = $1
  `, [jobName])
  const legacyLayoutRefresh = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(legacyLayoutRefresh.skipped, false, 'PG legacy 复合游标必须执行一次迁移刷新，不能误判为可跳过')
  const migratedPrimaryState = await pool.query(`
    SELECT cursor_created_at
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = $1
  `, [jobName])
  const migratedSourceVersionState = await pool.query(`
    SELECT cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'usage_rank_snapshot_source_version' AND scope_id = '' AND job_name = $1
  `, [jobName])
  assert.equal(migratedPrimaryState.rows[0]?.cursor_created_at, legacySourceWatermark, 'PG 迁移后主状态必须写回 canonical sourceWatermark')
  assert.equal(migratedSourceVersionState.rows[0]?.cursor_created_at, legacySourceWatermark, 'PG 迁移后独立版本状态必须匹配主状态水位')
  assert.equal(migratedSourceVersionState.rows[0]?.cursor_id, legacySourceVersion, 'PG 迁移后独立版本状态必须保留 sourceVersion')

  const overview = await getSystemMetricsOverviewAsync({
    startDate: statDate,
    endDate: statDate,
    days: 1,
    maxDays: 31
  })
  const systemBucket = overview.hourlyTrend.find((bucket) => bucket.statHour === statHour)
  assert.ok(systemBucket, 'PG system metrics overview 应读取系统指标趋势窗口')
  assert.equal(systemBucket.sampleCount, 1, '系统指标趋势窗口应保留 sample_count')
  assert.equal(systemBucket.cpuPercentAvg, 40, '系统指标趋势窗口应计算 CPU 平均值')
  assert.equal(systemBucket.eventLoopLagMsAvg, 20, '系统指标趋势窗口应计算事件循环平均值')
  assert.equal(systemBucket.networkRxBytesPerSecondAvg, 100, '系统指标趋势窗口应计算网络 RX 平均值')

  const processBucket = overview.processEventLoopTrend.find((bucket) => bucket.statHour === statHour && bucket.processRole === 'server')
  assert.ok(processBucket, 'PG system metrics overview 应读取进程事件循环趋势窗口')
  assert.equal(processBucket.sampleCount, 1, '进程事件循环趋势窗口应保留 sample_count')
  assert.equal(processBucket.eventLoopLagMsAvg, 25, '进程事件循环趋势窗口应计算平均延迟')
  assert.equal(processBucket.eventLoopLagMsMax, 25, '进程事件循环趋势窗口应保留最大延迟')

  await assertSystemMetricsExplainPlans(statDate, statHour)

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG system metrics trend refresh 源水位不变时应跳过')

  await seedUnaffectedTrendSentinels(unaffectedDate)
  await pool.query(`
    UPDATE juhe_stats.system_metrics_hourly
    SET cpu_percent_sum = 90,
      cpu_percent_max = 90,
      updated_at = $1
    WHERE stat_hour = $2
  `, [changedSourceUpdatedAt, statHour])
  await pool.query(`
    UPDATE juhe_stats.process_event_loop_hourly
    SET event_loop_lag_ms_sum = 45,
      event_loop_lag_ms_count = 1,
      event_loop_lag_ms_max = 45,
      updated_at = $1
    WHERE stat_hour = $2
      AND process_role = 'server'
  `, [changedSourceUpdatedAt, statHour])
  const incremental = await refreshUsageRankSnapshotsInStages({
    stageNames,
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(incremental.skipped, false, 'PG system metrics trend 同日源水位变化后应执行增量刷新')
  assert.equal(await trendSentinelUpdatedAt('system_metrics_trend_windows', unaffectedDate), sentinelUpdatedAt, 'PG 系统趋势增量不应重写无关日期窗口')
  assert.equal(await trendSentinelUpdatedAt('process_event_loop_trend_windows', unaffectedDate), sentinelUpdatedAt, 'PG 进程趋势增量不应重写无关日期窗口')
  const incrementalOverview = await getSystemMetricsOverviewAsync({
    startDate: statDate,
    endDate: statDate,
    days: 1,
    maxDays: 31
  })
  assert.equal(incrementalOverview.hourlyTrend.find((bucket) => bucket.statHour === statHour)?.cpuPercentAvg, 90, 'PG 系统趋势增量应读取更新后的小时聚合')
  assert.equal(incrementalOverview.processEventLoopTrend.find((bucket) => bucket.statHour === statHour && bucket.processRole === 'server')?.eventLoopLagMsAvg, 45, 'PG 进程趋势增量应读取更新后的小时聚合')

  console.log(JSON.stringify({
    message: '系统指标趋势 PG smoke 通过',
    statHour,
    sampledAt,
    hourlyTrend: overview.hourlyTrend.length,
    processEventLoopTrend: overview.processEventLoopTrend.length,
    explainIndexed: true,
    incrementalPreservedUnaffectedWindow: true,
    skipped: skipped.skipped === true,
    timezone
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

function sampledAtForStatHour(targetStatHour: string, timezone: string): string {
  const statDate = targetStatHour.slice(0, 10)
  const startMs = Date.parse(`${statDate}T00:00:00.000Z`) - 36 * 60 * 60 * 1000
  const endMs = startMs + 96 * 60 * 60 * 1000
  for (let timestamp = startMs; timestamp <= endMs; timestamp += 15 * 60 * 1000) {
    const candidate = new Date(timestamp)
    if (hourKey(candidate, timezone) === targetStatHour) {
      return candidate.toISOString()
    }
  }
  throw new Error(`无法为 ${targetStatHour} 生成匹配时区 ${timezone} 的 sampled_at`)
}

async function assertSystemMetricsExplainPlans(rangeDate: string, targetStatHour: string): Promise<void> {
  const windowKey = `${rangeDate}:${rangeDate}`
  await assertIndexedPlan(
    '系统指标趋势窗口 PG 查询',
    `
      SELECT bucket_key
      FROM juhe_stats.system_metrics_trend_windows
      WHERE window_key = $1 AND start_date = $2 AND end_date = $3
      ORDER BY bucket_key ASC
    `,
    [windowKey, rangeDate, rangeDate],
    ['idx_system_metrics_trend_windows_lookup', 'system_metrics_trend_windows_pkey']
  )
  await assertIndexedPlan(
    '进程事件循环各角色最新采样 PG 查询',
    `
      SELECT DISTINCT ON (process_role) process_role, sampled_at
      FROM juhe_stats.process_event_loop_samples
      WHERE process_role IN ($1, $2, $3, $4, $5)
      ORDER BY process_role, sampled_at DESC, id DESC
    `,
    ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service'],
    ['idx_process_event_loop_samples_role_latest']
  )
  await assertIndexedPlan(
    '进程事件循环各角色峰值采样 PG 查询',
    `
      SELECT DISTINCT ON (process_role) process_role, sampled_at
      FROM juhe_stats.process_event_loop_samples
      WHERE process_role IN ($1, $2, $3, $4, $5)
        AND sampled_at >= $6
        AND event_loop_lag_ms IS NOT NULL
      ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
    `,
    ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service', sampledAt],
    [
      'idx_process_event_loop_samples_role_peak',
      'idx_process_event_loop_samples_role_latest'
    ]
  )
  await assertIndexedPlan(
    '进程事件循环趋势窗口 PG 查询',
    `
      SELECT bucket_key
      FROM juhe_stats.process_event_loop_trend_windows
      WHERE window_key = $1 AND start_date = $2 AND end_date = $3
      ORDER BY bucket_key ASC, process_role ASC
    `,
    [windowKey, rangeDate, rangeDate],
    ['idx_process_event_loop_trend_windows_lookup', 'process_event_loop_trend_windows_pkey']
  )
  assert.equal(targetStatHour.length, 13, '测试小时桶格式应为 YYYY-MM-DDTHH')
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const pool = await getPostgresPool()
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function findEmptyStatHour(): Promise<{ statDate: string; statHour: string }> {
  const timezone = await usageStatsTimezoneAsync()
  const dates = fixedUsageStatsDateKeys(timezone)
  const pool = await getPostgresPool()
  for (const statDate of dates) {
    for (let hour = 0; hour < 24; hour += 1) {
      const candidate = `${statDate}T${String(hour).padStart(2, '0')}`
      const exists = await pool.query(`
        SELECT 1
        FROM juhe_stats.system_metrics_hourly
        WHERE stat_hour = $1
        UNION ALL
        SELECT 1
        FROM juhe_stats.process_event_loop_hourly
        WHERE stat_hour = $1
        LIMIT 1
      `, [candidate])
      if (exists.rowCount === 0) {
        return { statDate, statHour: candidate }
      }
    }
  }
  throw new Error('最近 31 天内没有可用于系统指标 PG smoke 的空闲小时桶')
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (unaffectedDate) {
    const windowKey = `${unaffectedDate}:${unaffectedDate}`
    await pool.query('DELETE FROM juhe_stats.system_metrics_trend_windows WHERE window_key = $1', [windowKey])
    await pool.query('DELETE FROM juhe_stats.process_event_loop_trend_windows WHERE window_key = $1', [windowKey])
  }
  if (statHour) {
    if (sampledAt) {
      await pool.query('DELETE FROM juhe_stats.system_metrics_samples WHERE sampled_at = $1', [sampledAt])
      await pool.query('DELETE FROM juhe_stats.process_event_loop_samples WHERE sampled_at = $1 AND process_role = $2', [sampledAt, 'server'])
    }
    await pool.query('DELETE FROM juhe_stats.system_metrics_hourly WHERE stat_hour = $1', [statHour])
    await pool.query('DELETE FROM juhe_stats.process_event_loop_hourly WHERE stat_hour = $1', [statHour])
    await refreshUsageRankSnapshotsInStages({
      stageNames,
      skipIfUnchanged: false,
      jobName,
      yieldToEventLoop: async () => {}
    }).catch(() => undefined)
  }
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
}

async function seedUnaffectedTrendSentinels(statDateValue: string): Promise<void> {
  const pool = await getPostgresPool()
  const windowKey = `${statDateValue}:${statDateValue}`
  const bucketKey = `${statDateValue}T00`
  await pool.query(`
    INSERT INTO juhe_stats.system_metrics_trend_windows (
      window_key, start_date, end_date, bucket_key, sample_count, updated_at
    ) VALUES ($1, $2, $2, $3, 0, $4)
    ON CONFLICT(window_key, bucket_key) DO UPDATE SET updated_at = excluded.updated_at
  `, [windowKey, statDateValue, bucketKey, sentinelUpdatedAt])
  await pool.query(`
    INSERT INTO juhe_stats.process_event_loop_trend_windows (
      window_key, start_date, end_date, bucket_key, process_role, sample_count, updated_at
    ) VALUES ($1, $2, $2, $3, 'server', 0, $4)
    ON CONFLICT(window_key, bucket_key, process_role) DO UPDATE SET updated_at = excluded.updated_at
  `, [windowKey, statDateValue, bucketKey, sentinelUpdatedAt])
}

async function trendSentinelUpdatedAt(tableName: 'system_metrics_trend_windows' | 'process_event_loop_trend_windows', statDateValue: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT updated_at
    FROM juhe_stats.${tableName}
    WHERE window_key = $1
      AND bucket_key = $2
    LIMIT 1
  `, [`${statDateValue}:${statDateValue}`, `${statDateValue}T00`])
  const updatedAt = result.rows[0]?.updated_at
  return typeof updatedAt === 'string' ? updatedAt : undefined
}
