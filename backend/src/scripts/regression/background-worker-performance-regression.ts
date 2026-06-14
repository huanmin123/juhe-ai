import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-background-worker-performance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'background-worker-performance-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  usageStatsRepository,
  usageStatsHelpers,
  usageStatsWindowHelpers,
  runtimeLogIndexQueue,
  runtimeLogsRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/usage-stats-window-helpers.js'),
  import('../../modules/runtime-logs/runtime-log-index-queue.service.js'),
  import('../../storage/runtime-logs.repository.js')
])

const simulatedScopeCount = 80
const runtimeLogCount = 1200
const maxAllowedEventLoopGapMs = 1000

try {
  assertSourceGuards()

  const statsDatabase = databaseModule.getStatsDatabase()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate
  const dates = usageStatsWindowHelpers.fixedUsageStatsDateKeys(usageStatsHelpers.usageStatsTimezone(), today)
  const rangeCount = dates.length * (dates.length + 1) / 2
  const expectedWindowRows = rangeCount * simulatedScopeCount
  seedUsageStatsDaily(dates)

  let yieldCount = 0
  const usageMonitor = startEventLoopGapMonitor()
  const usageStartedAt = performance.now()
  const usageResult = await usageStatsRepository.refreshUsageRankSnapshotsInStages({
    stageNames: ['usage_scope_range_windows'],
    yieldToEventLoop: () => new Promise<void>((resolve) => {
      yieldCount += 1
      setImmediate(resolve)
    })
  })
  const usageDurationMs = performance.now() - usageStartedAt
  const usageLoop = usageMonitor.stop()
  const windowRows = Number((statsDatabase.prepare('SELECT COUNT(*) AS count FROM usage_scope_range_windows').get() as { count?: number }).count ?? 0)

  assert.equal(windowRows, expectedWindowRows, 'usage scope 范围窗口应完整生成模拟数据')
  assert.ok(yieldCount >= rangeCount * 2, 'usage scope 范围窗口构建和发布都应按日期区间 yield')
  assert.ok(usageLoop.ticks > 0, 'usage scope 范围窗口刷新期间事件循环应持续推进')
  assert.ok(
    usageLoop.maxGapMs < maxAllowedEventLoopGapMs,
    `usage scope 范围窗口刷新事件循环最大间隔过高：${usageLoop.maxGapMs.toFixed(1)}ms`
  )

  const runtimeLogResult = assertRuntimeLogFlushIsBounded()

  console.log(
    [
      '后台 worker 性能回归通过',
      `usage_scope_range_windows scopes=${simulatedScopeCount}`,
      `ranges=${rangeCount}`,
      `rows=${windowRows}`,
      `durationMs=${usageDurationMs.toFixed(1)}`,
      `maxEventLoopGapMs=${usageLoop.maxGapMs.toFixed(1)}`,
      `yieldCount=${yieldCount}`,
      `runtimeLogs=${runtimeLogCount}`,
      `firstRuntimeLogFlush=${runtimeLogResult.firstFlushCount}`,
      `runtimeLogFirstFlushMs=${runtimeLogResult.firstFlushMs.toFixed(1)}`,
      `runtimeLogFlushAllMs=${runtimeLogResult.flushAllMs.toFixed(1)}`,
      `stages=${usageResult.stages.map((stage) => `${stage.name}:${stage.durationMs}ms`).join(',')}`
    ].join(' ')
  )
} finally {
  try {
    runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageStatsDaily(dates: string[]): void {
  const database = databaseModule.getStatsDatabase()
  const transactionStarted = databaseModule.beginDatabaseTransaction(database)
  const insert = database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  try {
    for (let scopeIndex = 0; scopeIndex < simulatedScopeCount; scopeIndex += 1) {
      const scopeId = `sys_worker_perf_${String(scopeIndex).padStart(4, '0')}`
      for (let dateIndex = 0; dateIndex < dates.length; dateIndex += 1) {
        const statDate = dates[dateIndex]
        const requestCount = 10 + dateIndex
        const successCount = requestCount - 1
        insert.run(
          scopeId,
          scopeId,
          statDate,
          requestCount,
          successCount,
          1,
          requestCount * 100,
          requestCount * 40,
          requestCount * 10,
          requestCount * 0.0001,
          requestCount * 0.001,
          requestCount * 120,
          requestCount,
          450 + dateIndex,
          requestCount * 12,
          requestCount,
          80 + dateIndex,
          `${statDate}T12:00:00.000Z`,
          `${statDate}T12:00:01.000Z`
        )
      }
    }
    databaseModule.commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    databaseModule.rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function assertRuntimeLogFlushIsBounded(): { firstFlushCount: number; firstFlushMs: number; flushAllMs: number } {
  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
  for (let index = 0; index < runtimeLogCount; index += 1) {
    runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(runtimeLogLine(index), {
      sourceKey: `worker-performance-runtime-log-${index}`,
      logFile: 'worker-performance.log',
      logOffset: index * 2048,
      lineNumber: index + 1
    })
  }

  const beforeFlush = runtimeLogIndexQueue.getRuntimeLogIndexRuntime()
  assert.equal(beforeFlush.queueLength, runtimeLogCount, '运行日志性能测试应先制造完整积压队列')

  const firstFlushStartedAt = performance.now()
  assert.equal(
    runtimeLogIndexQueue.flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false }),
    true,
    '运行日志默认 drain 刷写应成功'
  )
  const firstFlushMs = performance.now() - firstFlushStartedAt
  const afterFirstFlush = runtimeLogIndexQueue.getRuntimeLogIndexRuntime()
  const firstFlushCount = beforeFlush.queueLength - afterFirstFlush.queueLength
  assert.ok(firstFlushCount > 0, '运行日志默认 drain 应至少刷写一批')
  assert.ok(firstFlushCount <= 500, '运行日志默认 drain 单轮不应超过条数批次上限')
  assert.ok(afterFirstFlush.queueLength > 0, '运行日志默认 drain 不能一次性刷完整个积压队列')

  const flushAllStartedAt = performance.now()
  assert.equal(runtimeLogIndexQueue.flushAllRuntimeLogIndexQueue(), true, '运行日志显式 flushAll 应刷完积压队列')
  const flushAllMs = performance.now() - flushAllStartedAt
  const afterFlushAll = runtimeLogIndexQueue.getRuntimeLogIndexRuntime()
  assert.equal(afterFlushAll.queueLength, 0, '运行日志显式 flushAll 后队列应清空')
  assert.equal(runtimeLogsRepository.getRuntimeLogFacets().totalIndexed, runtimeLogCount, '运行日志索引应完整写入模拟数据')

  return { firstFlushCount, firstFlushMs, flushAllMs }
}

function runtimeLogLine(index: number): string {
  const time = new Date(Date.now() - (runtimeLogCount - index) * 1000).toISOString()
  return JSON.stringify({
    time,
    level: 'info',
    traceId: `worker_perf_trace_${index}`,
    event: 'background_worker_performance_runtime_log',
    msg: `runtime log pressure ${index} ${'x'.repeat(1500)}`
  })
}

function startEventLoopGapMonitor(): { stop: () => { maxGapMs: number; ticks: number } } {
  let active = true
  let ticks = 0
  let maxGapMs = 0
  let lastTickAt = performance.now()
  const tick = (): void => {
    const now = performance.now()
    maxGapMs = Math.max(maxGapMs, now - lastTickAt)
    lastTickAt = now
    ticks += 1
    if (active) {
      setImmediate(tick)
    }
  }
  setImmediate(tick)
  return {
    stop: () => {
      active = false
      return { maxGapMs, ticks }
    }
  }
}

function assertSourceGuards(): void {
  const usageStatsSource = readFileSync(resolve('src/storage/usage-stats.repository.ts'), 'utf8')
  assert.match(
    usageStatsSource,
    /DELETE FROM usage_scope_range_windows WHERE end_date = \? AND start_date = \?/,
    'usage scope 范围窗口发布必须按 start_date/end_date 分片删除'
  )
  assert.match(
    usageStatsSource,
    /WHERE end_date = \? AND start_date = \?/,
    'usage scope 范围窗口发布必须按 start_date/end_date 分片插入'
  )

  const runtimeQueueSource = readFileSync(resolve('src/modules/runtime-logs/runtime-log-index-queue.service.ts'), 'utf8')
  assert.match(runtimeQueueSource, /runtimeLogBatchMaxBytes/, '运行日志索引批次必须有字节上限')
  assert.match(runtimeQueueSource, /runtimeLogDefaultFlushMaxBatches = 1/, '运行日志默认 flush 必须限制单轮批次数')
  assert.match(runtimeQueueSource, /peekRuntimeLogFlushBatch/, '运行日志索引必须通过有界 peek 取批次')
  assert.match(
    runtimeQueueSource,
    /flushRuntimeLogIndexQueue\(\{ drain: true, retryOnFailure: false, maxBatches: Number\.POSITIVE_INFINITY \}\)/,
    '只有显式 flushAll 才能无限 drain 运行日志队列'
  )

  const cooldownRetestSource = readFileSync(resolve('src/modules/background/cooldown-account-retest.service.ts'), 'utf8')
  assert.match(
    cooldownRetestSource,
    /logger\.debug\(\{\s*event: 'background_cooldown_account_retest_discarded'/s,
    '冷却复测正常丢弃候选不能写入生产 info 日志'
  )
}
