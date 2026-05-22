import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-metrics-process-latest-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
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
    [
      { processRole: 'server', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null },
      { processRole: 'worker', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null },
      { processRole: 'db-service', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null }
    ],
    '无最新采样时应显式返回每个进程角色的不可用状态'
  )
  assert.deepEqual(
    emptyOverview.processEventLoopPeakStatus,
    [
      { processRole: 'server', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null },
      { processRole: 'worker', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null },
      { processRole: 'db-service', sampleAvailable: false, processPid: null, sampledAt: null, eventLoopLagMs: null }
    ],
    '无最近 24 小时采样时应显式返回每个进程角色的峰值不可用状态'
  )

  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1001,
    sampledAt: '2026-01-01T00:00:00.000Z',
    eventLoopLagMs: 11
  })
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'db-service',
    processPid: 3001,
    sampledAt: '2026-01-01T00:00:01.000Z',
    eventLoopLagMs: 31
  })
  for (let index = 0; index < 125; index += 1) {
    usageStatsRepository.insertProcessEventLoopSample({
      processRole: 'worker',
      processPid: 2000 + index,
      sampledAt: new Date(Date.UTC(2026, 0, 1, 0, 1, index)).toISOString(),
      eventLoopLagMs: 20 + index
    })
  }

  const overview = usageStatsRepository.getSystemMetricsOverview({
    startDate: '2026-01-01',
    endDate: '2026-01-01',
    days: 1,
    maxDays: 31
  })
  const latestStatusByRole = new Map(overview.processEventLoopLatestStatus.map((row) => [row.processRole, row]))
  assert.deepEqual([...latestStatusByRole.keys()], ['server', 'worker', 'db-service'], '最新进程样本可用性应固定覆盖所有角色')
  assert.equal(latestStatusByRole.get('server')?.sampleAvailable, true, 'server 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('server')?.eventLoopLagMs, 11, 'server 最新样本不应被 worker 连续样本挤掉')
  assert.equal(latestStatusByRole.get('worker')?.sampleAvailable, true, 'worker 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('worker')?.processPid, 2124, 'worker 可用性行应带最新 PID')
  assert.equal(latestStatusByRole.get('db-service')?.sampleAvailable, true, 'db-service 有最新采样时应显式标记可用')
  assert.equal(latestStatusByRole.get('db-service')?.eventLoopLagMs, 31, 'db-service 最新样本不应被 worker 连续样本挤掉')
  assert.equal(latestStatusByRole.get('worker')?.eventLoopLagMs, 144, 'worker 应返回自身最新样本')
  assert.deepEqual(overview.backgroundJobs, [], 'repository 层系统指标应提供空后台任务数组，路由层再补运行时快照')

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
    eventLoopLagMs: 10
  })
  const serverPeakAt = new Date(recentMinute.getTime() + 30_000).toISOString()
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'server',
    processPid: 1002,
    sampledAt: serverPeakAt,
    eventLoopLagMs: 20
  })
  const workerPeakAt = new Date(recentMinute.getTime() + 10_000).toISOString()
  usageStatsRepository.insertProcessEventLoopSample({
    processRole: 'worker',
    processPid: 2200,
    sampledAt: workerPeakAt,
    eventLoopLagMs: 42
  })
  const minuteOverview = usageStatsRepository.getSystemMetricsOverview({
    startDate: '2026-01-01',
    endDate: '2026-01-01',
    days: 1,
    maxDays: 31
  })
  const peakStatusByRole = new Map(minuteOverview.processEventLoopPeakStatus.map((row) => [row.processRole, row]))
  assert.equal(peakStatusByRole.get('server')?.sampleAvailable, true, 'server 最近 24 小时内有采样时应标记峰值可用')
  assert.equal(peakStatusByRole.get('server')?.eventLoopLagMs, 20, 'server 峰值应取最近 24 小时最大延迟，忽略窗口外高值')
  assert.equal(peakStatusByRole.get('server')?.sampledAt, serverPeakAt, 'server 峰值状态应返回最大值对应采样时间')
  assert.equal(peakStatusByRole.get('worker')?.eventLoopLagMs, 42, 'worker 峰值应独立按进程角色计算')
  assert.equal(peakStatusByRole.get('worker')?.sampledAt, workerPeakAt, 'worker 峰值状态应返回对应采样时间')
  assert.equal(peakStatusByRole.get('db-service')?.sampleAvailable, false, 'db-service 无最近 24 小时采样时不应使用旧样本伪装峰值')
  const serverMinuteBucket = minuteOverview.processEventLoopTrend.find((row) => row.processRole === 'server' && row.sampleCount === 2)
  assert(serverMinuteBucket, '进程事件循环趋势应按最近 24 小时分钟桶聚合')
  assert.match(serverMinuteBucket.statMinute, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/, '进程事件循环趋势桶应精确到分钟')
  assert.equal(serverMinuteBucket.eventLoopLagMsAvg, 15, '同一分钟内多个采样应按分钟计算平均延迟')
  assert.equal(serverMinuteBucket.eventLoopLagMsMax, 20, '分钟桶应保留峰值延迟，便于定位尖峰')

  console.log('系统指标进程事件循环回归通过：最新样本和 24 小时峰值按进程角色独立计算')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
