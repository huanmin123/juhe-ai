import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-process-latest-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-metrics-process-latest-secret'
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
  const emptyOverview = usageStatsRepository.getSystemMetricsOverview({
    startDate: '2026-01-01',
    endDate: '2026-01-01',
    days: 1,
    maxDays: 31
  })
  assert.deepEqual(
    emptyOverview.processEventLoopLatestStatus,
    expectedUnavailableProcessStatus(),
    '无最新采样时应显式返回每个进程角色的不可用状态'
  )
  assert.deepEqual(
    emptyOverview.processEventLoopPeakStatus,
    expectedUnavailableProcessStatus(),
    '无最近 24 小时采样时应显式返回每个进程角色的峰值不可用状态'
  )

  const latestBaseAtMs = Date.now() - 30_000
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1001,
    sampledAt: new Date(latestBaseAtMs).toISOString(),
    eventLoopLagMs: 11,
    processRssBytes: 1100,
    processHeapUsedBytes: 510,
    processHeapTotalBytes: 900
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'db-service',
    processPid: 3001,
    sampledAt: new Date(latestBaseAtMs + 1_000).toISOString(),
    eventLoopLagMs: 31
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'ingest-worker',
    processPid: 5001,
    sampledAt: new Date(latestBaseAtMs + 3_000).toISOString(),
    eventLoopLagMs: 9
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'stats-worker',
    processPid: 5101,
    sampledAt: new Date(latestBaseAtMs + 4_000).toISOString(),
    eventLoopLagMs: 10
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'ops-worker',
    processPid: 5201,
    sampledAt: new Date(latestBaseAtMs + 5_000).toISOString(),
    eventLoopLagMs: 12
  })

  const overview = usageStatsRepository.getSystemMetricsOverview({
    startDate: '2026-01-01',
    endDate: '2026-01-01',
    days: 1,
    maxDays: 31
  })
  const latestStatusByRole = new Map(overview.processEventLoopLatestStatus.map((row) => [row.processRole, row]))
  assert.deepEqual([...latestStatusByRole.keys()], ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service'], '最新进程样本可用性应固定覆盖当前进程角色')
  assert.equal(latestStatusByRole.get('server')?.sampleAvailable, true, 'server 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('server')?.eventLoopLagMs, 11, 'server 最新样本应按自身角色读取')
  assert.equal(latestStatusByRole.get('server')?.processRssBytes, 1100, 'server 最新样本应带进程 RSS 内存')
  assert.equal(latestStatusByRole.get('server')?.processHeapUsedBytes, 510, 'server 最新样本应带进程 heap used')
  assert.equal(latestStatusByRole.get('server')?.processHeapTotalBytes, 900, 'server 最新样本应带进程 heap total')
  assert.equal(latestStatusByRole.get('ingest-worker')?.sampleAvailable, true, 'ingest-worker 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('ingest-worker')?.eventLoopLagMs, 9, 'ingest-worker 应返回自身最新样本')
  assert.equal(latestStatusByRole.get('stats-worker')?.sampleAvailable, true, 'stats-worker 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('stats-worker')?.eventLoopLagMs, 10, 'stats-worker 应返回自身最新样本')
  assert.equal(latestStatusByRole.get('ops-worker')?.sampleAvailable, true, 'ops-worker 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('ops-worker')?.eventLoopLagMs, 12, 'ops-worker 应返回自身最新样本')
  assert.equal(latestStatusByRole.get('db-service')?.sampleAvailable, true, 'db-service 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('db-service')?.eventLoopLagMs, 31, 'db-service 最新样本应按自身角色读取')

  databaseModule.getStatsDatabase().prepare('DELETE FROM process_event_loop_samples').run()
  databaseModule.getStatsDatabase().prepare('DELETE FROM process_event_loop_hourly').run()
  const recentMinute = new Date(Date.now() - 60_000)
  recentMinute.setSeconds(0, 0)
  const staleHighLagAt = new Date(Date.now() - 25 * 60 * 60 * 1000).toISOString()
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1000,
    sampledAt: staleHighLagAt,
    eventLoopLagMs: 999
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1002,
    sampledAt: recentMinute.toISOString(),
    eventLoopLagMs: 10,
    processRssBytes: 100 * 1024 * 1024,
    processHeapUsedBytes: 40 * 1024 * 1024,
    processHeapTotalBytes: 80 * 1024 * 1024
  })
  const serverPeakAt = new Date(recentMinute.getTime() + 30_000).toISOString()
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1002,
    sampledAt: serverPeakAt,
    eventLoopLagMs: 20,
    processRssBytes: 130 * 1024 * 1024,
    processHeapUsedBytes: 50 * 1024 * 1024,
    processHeapTotalBytes: 90 * 1024 * 1024
  })
  usageStatsRepository.refreshUsageRankSnapshots()
  const todayRange = usageStatsRepository.normalizeDefaultUsageStatsRange()
  const minuteOverview = usageStatsRepository.getSystemMetricsOverview(todayRange)
  const peakStatusByRole = new Map(minuteOverview.processEventLoopPeakStatus.map((row) => [row.processRole, row]))
  assert.equal(peakStatusByRole.get('server')?.sampleAvailable, true, 'server 最近 24 小时内有采样时应标记峰值可用')
  assert.equal(peakStatusByRole.get('server')?.eventLoopLagMs, 20, 'server 峰值应取最近 24 小时最大延迟，忽略窗口外高值')
  assert.equal(peakStatusByRole.get('server')?.sampledAt, serverPeakAt, 'server 峰值状态应返回最大值对应采样时间')
  assert.equal(peakStatusByRole.get('ingest-worker')?.sampleAvailable, false, 'ingest-worker 无最近 24 小时采样时不应使用过期样本伪装峰值')
  assert.equal(peakStatusByRole.get('stats-worker')?.sampleAvailable, false, 'stats-worker 无最近 24 小时采样时不应使用过期样本伪装峰值')
  assert.equal(peakStatusByRole.get('ops-worker')?.sampleAvailable, false, 'ops-worker 无最近 24 小时采样时不应使用过期样本伪装峰值')
  assert.equal(peakStatusByRole.get('db-service')?.sampleAvailable, false, 'db-service 无最近 24 小时采样时不应使用过期样本伪装峰值')
  const serverMinuteBucket = minuteOverview.processEventLoopTrend.find((row) => row.processRole === 'server' && row.sampleCount === 2)
  assert(serverMinuteBucket, '进程事件循环趋势应读取后台窗口缓存')
  assert.match(serverMinuteBucket.statMinute, /^\d{4}-\d{2}-\d{2}T\d{2}$/, '单日窗口内事件循环趋势桶应精确到小时')
  assert.equal(serverMinuteBucket.eventLoopLagMsAvg, 15, '同一窗口桶内多个采样应按缓存计算平均延迟')
  assert.equal(serverMinuteBucket.eventLoopLagMsMax, 20, '窗口桶应保留峰值延迟，便于定位尖峰')
  assert.equal(serverMinuteBucket.processRssBytesMax, 130 * 1024 * 1024, '窗口桶应保留进程 RSS 峰值，便于发现 worker 内存爬升')
  assert.equal(serverMinuteBucket.processHeapUsedBytesMax, 50 * 1024 * 1024, '窗口桶应保留进程 heap used 峰值')

  console.log('系统指标进程事件循环回归通过：最新样本和 24 小时峰值按进程角色独立计算')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function expectedUnavailableProcessStatus() {
  return [
    'server',
    'ingest-worker',
    'stats-worker',
    'ops-worker',
    'db-service'
  ].map((processRole) => ({
    processRole,
    sampleAvailable: false,
    processPid: null,
    sampledAt: null,
    eventLoopLagMs: null,
    processRssBytes: null,
    processHeapUsedBytes: null,
    processHeapTotalBytes: null,
    processExternalBytes: null,
    processArrayBuffersBytes: null
  }))
}
