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
  usageStatsWindowHelpers
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../storage/usage-stats-window-helpers.js')
])

const simulatedScopeCount = 80
const maxAllowedEventLoopGapMs = 1000

try {
  assertSourceGuards()

  const statsDatabase = databaseModule.getStatsDatabase()
  const today = usageStatsRepository.normalizeDefaultUsageStatsRange().endDate
  const dates = usageStatsWindowHelpers.fixedUsageStatsDateKeys(usageStatsHelpers.usageStatsTimezone(), today)
  const rangeCount = usageStatsWindowHelpers.hotUsageStatsRanges(usageStatsHelpers.usageStatsTimezone(), today).length
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

  assert.equal(windowRows, expectedWindowRows, 'usage scope 范围窗口应完整生成热窗口模拟数据')
  assert.ok(yieldCount >= rangeCount * 2, 'usage scope 范围窗口构建和发布都应按热窗口 yield')
  assert.ok(usageLoop.ticks > 0, 'usage scope 范围窗口刷新期间事件循环应持续推进')
  assert.ok(
    usageLoop.maxGapMs < maxAllowedEventLoopGapMs,
    `usage scope 范围窗口刷新事件循环最大间隔过高：${usageLoop.maxGapMs.toFixed(1)}ms`
  )

  console.log(
    [
      '后台 worker 性能回归通过',
      `usage_scope_range_windows scopes=${simulatedScopeCount}`,
      `ranges=${rangeCount}`,
      `rows=${windowRows}`,
      `durationMs=${usageDurationMs.toFixed(1)}`,
      `maxEventLoopGapMs=${usageLoop.maxGapMs.toFixed(1)}`,
      `yieldCount=${yieldCount}`,
      `stages=${usageResult.stages.map((stage) => `${stage.name}:${stage.durationMs}ms`).join(',')}`
    ].join(' ')
  )
} finally {
  try {
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
  const rangeWindowSource = readFileSync(resolve('src/storage/usage-range-windows.repository.ts'), 'utf8')
  assert.match(
    rangeWindowSource,
    /DELETE FROM usage_scope_range_windows WHERE end_date = \? AND start_date = \?/,
    'usage scope 范围窗口发布必须按 start_date/end_date 分片删除'
  )
  assert.match(
    rangeWindowSource,
    /WHERE end_date = \? AND start_date = \?/,
    'usage scope 范围窗口发布必须按 start_date/end_date 分片插入'
  )

  const runtimeFileImporterSource = readFileSync(resolve('src/modules/runtime-logs/runtime-log-file-import.service.ts'), 'utf8')
  assert.match(runtimeFileImporterSource, /runtimeLogTailMaxBytesPerFile/, '运行日志文件消费每轮必须有字节预算')
  assert.match(runtimeFileImporterSource, /runtimeLogTailMaxLinesPerFile/, '运行日志文件消费每轮必须有行数预算')
  assert.match(runtimeFileImporterSource, /nextOffset >= input\.endOffset/, '运行日志文件消费必须在完整行边界按预算让出')

  const cooldownRetestSource = readFileSync(resolve('src/modules/background/cooldown-account-retest.service.ts'), 'utf8')
  assert.match(
    cooldownRetestSource,
    /logger\.debug\(\{\s*event: 'background_cooldown_account_retest_discarded'/s,
    '冷却复测正常丢弃候选不能写入生产 info 日志'
  )
}
