import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { rangeWindowKey } from '../../storage/usage-stats-window-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-window-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-metrics-window-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const database = databaseModule.getStatsDatabase()
  const range = usageStatsRepository.normalizeDefaultUsageStatsRange()
  const windowKey = rangeWindowKey(range)
  const bucketKey = `${range.endDate}T00`
  database.prepare(`
    INSERT INTO process_event_loop_trend_windows (
      window_key, start_date, end_date, bucket_key, process_role, sample_count,
      event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max, updated_at
    ) VALUES (?, ?, ?, ?, 'server', 3, 60, 3, 30, 128, 128, 64, 64, ?)
  `).run(windowKey, range.startDate, range.endDate, bucketKey, '2026-01-01T00:00:00.000Z')

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    if (/FROM\s+process_event_loop_samples\b[\s\S]*ORDER\s+BY\s+sampled_at\s+ASC,\s*process_role\s+ASC,\s*id\s+ASC/i.test(sql)) {
      throw new Error('系统指标接口不应按时间窗扫描进程事件循环原始采样')
    }
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    statement.get = ((...params: SQLInputValue[]) => {
      capturedCalls.push({ sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      capturedCalls.push({ sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare

  try {
    const overview = usageStatsRepository.getSystemMetricsOverview(range)
    assert.equal(overview.processEventLoopTrend.length, 1, '系统指标趋势应读取窗口缓存')
    assert.equal(overview.processEventLoopTrend[0]?.processRole, 'server', '窗口缓存应保留进程角色')
    assert.equal(overview.processEventLoopTrend[0]?.sampleCount, 3, '窗口缓存应保留采样数')
    assert.equal(overview.processEventLoopTrend[0]?.eventLoopLagMsAvg, 20, '窗口缓存应计算平均延迟')
    assert.equal(overview.processEventLoopTrend[0]?.eventLoopLagMsMax, 30, '窗口缓存应保留峰值延迟')
  } finally {
    database.prepare = originalPrepare
  }
  const latestMetricsCall = capturedCalls.find((call) => /\bFROM\s+system_metrics_samples\b/i.test(call.sql) && /\bORDER\s+BY\s+sampled_at\s+DESC,\s*id\s+DESC\b/i.test(call.sql))
  assert(latestMetricsCall, '系统指标接口应读取最新系统采样')
  const latestMetricsPlan = explainQueryPlan(database, latestMetricsCall.sql, latestMetricsCall.params)
  assertNoTempBtree(latestMetricsPlan, '最新系统指标采样查询')
  assert(latestMetricsPlan.includes('idx_system_metrics_samples_latest'), `最新系统指标采样应使用 latest 索引，实际计划：${latestMetricsPlan}`)

  const processLatestCall = capturedCalls.find((call) => /\bFROM\s+process_event_loop_samples\b/i.test(call.sql) && /\bORDER\s+BY\s+sampled_at\s+DESC,\s*id\s+DESC\b/i.test(call.sql))
  assert(processLatestCall, '系统指标接口应按进程角色读取最新事件循环采样')
  const processLatestPlan = explainQueryPlan(database, processLatestCall.sql, processLatestCall.params)
  assertNoTempBtree(processLatestPlan, '进程事件循环最新采样查询')
  assert(processLatestPlan.includes('idx_process_event_loop_samples_role_latest'), `进程事件循环最新采样应使用 role latest 索引，实际计划：${processLatestPlan}`)

  const processPeakCall = capturedCalls.find((call) => /\bFROM\s+process_event_loop_samples\b/i.test(call.sql) && /\bORDER\s+BY\s+event_loop_lag_ms\s+DESC,\s*sampled_at\s+DESC,\s*id\s+DESC\b/i.test(call.sql))
  assert(processPeakCall, '系统指标接口应按进程角色读取最近 24 小时事件循环峰值')
  const processPeakPlan = explainQueryPlan(database, processPeakCall.sql, processPeakCall.params)
  assertNoTempBtree(processPeakPlan, '进程事件循环峰值查询')
  assert(processPeakPlan.includes('idx_process_event_loop_samples_role_peak'), `进程事件循环峰值应使用 role peak 索引，实际计划：${processPeakPlan}`)

  console.log('系统指标窗口查询回归通过：统计概览不再按时间窗扫描进程事件循环原始采样')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function explainQueryPlan(database: DatabaseSync, sql: string, params: SQLInputValue[]): string {
  const rows = database
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').filter(Boolean).join('\n')
}

function assertNoTempBtree(details: string, label: string): void {
  assert(!/USE TEMP B-TREE/i.test(details), `${label}不应创建临时排序树，实际计划：${details}`)
}
