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
  const latestByRole = new Map(overview.processEventLoopLatest.map((row) => [row.processRole, row]))
  assert.equal(latestByRole.size, 3, '各进程角色的最新事件循环样本都应可见')
  assert.equal(latestByRole.get('server')?.eventLoopLagMs, 11, 'server 最新样本不应被 worker 连续样本挤掉')
  assert.equal(latestByRole.get('db-service')?.eventLoopLagMs, 31, 'db-service 最新样本不应被 worker 连续样本挤掉')
  assert.equal(latestByRole.get('worker')?.eventLoopLagMs, 144, 'worker 应返回自身最新样本')
  assert.deepEqual([...latestByRole.keys()], ['server', 'worker', 'db-service'], '最新进程样本应按固定角色顺序返回')
  assert.deepEqual(overview.backgroundJobs, [], 'repository 层系统指标应提供空后台任务数组，路由层再补运行时快照')

  console.log('系统指标进程最新样本回归通过：每个进程角色独立取最新样本')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
