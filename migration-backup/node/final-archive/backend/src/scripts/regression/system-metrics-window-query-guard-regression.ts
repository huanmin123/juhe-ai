import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
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

const repositorySource = readFileSync(resolve('src/storage/system-metrics.repository.ts'), 'utf8')
assert.match(repositorySource, /Promise\.all\(\[[\s\S]*processEventLoopLatestRowsAsync\(client\)[\s\S]*loadProcessEventLoopTrendWindowRowsAsync\(client, range\)[\s\S]*processEventLoopPeakRowsAsync\(client, processEventLoopStartedAt\)/, 'PG 系统指标四类读取必须并行')
assert.match(repositorySource, /SELECT DISTINCT ON \(process_role\)[\s\S]*ORDER BY process_role, sampled_at DESC, id DESC/, 'PG latest 必须单查询按角色取最新采样')
assert.match(repositorySource, /SELECT DISTINCT ON \(process_role\)[\s\S]*ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC/, 'PG peak 必须单查询按角色取峰值采样')
assert.doesNotMatch(repositorySource, /for \(const role of PROCESS_EVENT_LOOP_ROLES\)[\s\S]*await client\.one/, 'PG latest / peak 不得按角色串行往返')
assert.match(repositorySource, /ROW_NUMBER\(\) OVER \([\s\S]*PARTITION BY process_role[\s\S]*ORDER BY sampled_at DESC, id DESC/, 'SQLite latest 必须用集合查询按角色取最新采样')
assert.match(repositorySource, /ROW_NUMBER\(\) OVER \([\s\S]*PARTITION BY process_role[\s\S]*ORDER BY event_loop_lag_ms DESC, sampled_at DESC, id DESC/, 'SQLite peak 必须用集合查询按角色取峰值采样')
assert.doesNotMatch(repositorySource, /processEventLoopObservedRoles/, 'SQLite latest / peak 不得先发现角色再逐角色查询')
assert.match(repositorySource, /FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_sampled_at\s+WHERE sampled_at >= \?/, 'SQLite latest / peak 必须先按时间窗口缩小原始采样集合')
assert.doesNotMatch(repositorySource, /INDEXED BY idx_process_event_loop_samples_role_(?:latest|peak)/, 'SQLite 时间窗口查询不得强制扫描 role 前导索引')

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
  const sampledAt = new Date().toISOString()
  database.prepare(`
    INSERT INTO process_event_loop_samples (
      sampled_at, process_role, process_pid, event_loop_lag_ms, id, created_at
    ) VALUES (?, 'server', 12345, 12, 'process_metric_query_guard', ?)
  `).run(sampledAt, sampledAt)
  const insertRoleSample = database.prepare(`
    INSERT INTO process_event_loop_samples (
      sampled_at, process_role, process_pid, event_loop_lag_ms, id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `)
  for (let index = 1; index <= 12; index += 1) {
    insertRoleSample.run(
      sampledAt,
      `gateway:query-guard-${index}`,
      20_000 + index,
      index,
      `process_metric_query_guard_${index}`,
      sampledAt
    )
  }

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
    capturedCalls.length = 0
    const trend = usageStatsRepository.getSystemMetricsTrend(range)
    assert.deepEqual(
      Object.keys(trend.processEventLoopTrend[0] ?? {}).sort(),
      ['eventLoopLagMsAvg', 'eventLoopLagMsMax', 'processRole', 'processRssBytesAvg', 'processRssBytesMax', 'statMinute'].sort(),
      '趋势场景 DTO 不得映射未展示的 heap 等窗口字段'
    )
    const latest = trend.processEventLoopLatestStatus.find((row) => row.processRole === 'server')
    assert(latest, '趋势场景应返回 server 最新状态')
    assert.deepEqual(
      Object.keys(latest).sort(),
      ['eventLoopLagMs', 'processHeapTotalBytes', 'processHeapUsedBytes', 'processPid', 'processRole', 'processRssBytes', 'sampleAvailable', 'sampledAt'].sort(),
      '最新状态场景 DTO 不得映射页面未展示的 external / array buffer 字段'
    )
    const peak = trend.processEventLoopPeakStatus.find((row) => row.processRole === 'server')
    assert(peak, '趋势场景应返回 server 峰值状态')
    assert.deepEqual(
      Object.keys(peak).sort(),
      ['eventLoopLagMs', 'processPid', 'processRole', 'sampleAvailable', 'sampledAt'].sort(),
      '峰值状态场景 DTO 只保留表格消费字段'
    )
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(
    capturedCalls.some((call) => /\bFROM\s+system_metrics_samples\b/i.test(call.sql)),
    false,
    '系统指标首屏不应查询前端未使用的最新系统采样'
  )

  const processSampleCalls = capturedCalls.filter((call) => /\bFROM\s+process_event_loop_samples\b/i.test(call.sql))
  assert.equal(processSampleCalls.length, 2, 'SQLite trend 的 latest / peak 查询总数必须固定为 2，不得随角色数量增长')
  assert(processSampleCalls.every((call) => call.params.length === 1), 'SQLite latest / peak 只能绑定时间边界，不得逐角色绑定查询')

  const processLatestCall = capturedCalls.find((call) => /\bFROM\s+process_event_loop_samples\b/i.test(call.sql) && /\bORDER\s+BY\s+sampled_at\s+DESC,\s*id\s+DESC\b/i.test(call.sql))
  assert(processLatestCall, '系统指标接口应按进程角色读取最新事件循环采样')
  const processLatestPlan = explainQueryPlan(database, processLatestCall.sql, processLatestCall.params)
  assertTimeWindowIndexSearch(processLatestPlan, '进程事件循环最新采样查询')

  const processPeakCall = capturedCalls.find((call) => /\bFROM\s+process_event_loop_samples\b/i.test(call.sql) && /\bORDER\s+BY\s+event_loop_lag_ms\s+DESC,\s*sampled_at\s+DESC,\s*id\s+DESC\b/i.test(call.sql))
  assert(processPeakCall, '系统指标接口应按进程角色读取最近 24 小时事件循环峰值')
  const processPeakPlan = explainQueryPlan(database, processPeakCall.sql, processPeakCall.params)
  assertTimeWindowIndexSearch(processPeakPlan, '进程事件循环峰值查询')

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

function assertTimeWindowIndexSearch(details: string, label: string): void {
  const sourceSteps = details
    .split('\n')
    .filter((detail) => /\bprocess_event_loop_samples\b/i.test(detail))
  assert(sourceSteps.length > 0, `${label}必须读取进程事件循环采样，实际计划：${details}`)
  assert(
    sourceSteps.every((detail) => /\bSEARCH process_event_loop_samples USING INDEX idx_process_event_loop_samples_sampled_at \(sampled_at>\?\)/i.test(detail)),
    `${label}必须按 sampled_at 时间边界执行索引 SEARCH，不能 SCAN 全量历史索引，实际计划：${details}`
  )
}
