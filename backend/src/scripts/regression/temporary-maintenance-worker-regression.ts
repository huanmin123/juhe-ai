import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { BackgroundTaskRunSummary } from '../../storage/background-task-runs.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-temporary-maintenance-worker-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  recordMaintenanceQueue,
  taskRunsRepository,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../storage/background-task-runs.repository.js'),
  import('../../storage/repositories.js')
])

try {
  seedUsageRecord('temporary_usage_cleanup_regression', '2000-01-01T00:00:00.000Z')
  seedUsageStatsCleanupCursors('2000-01-01T00:00:01.000Z', 'temporary_usage_cleanup_regression')

  const beforeCount = usageRecordCount('temporary_usage_cleanup_regression')
  assert.equal(beforeCount, 1, '测试前应存在 1 条待清理使用记录')
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_temporary_usage_cleanup_regression',
    cutoffAt: '2000-01-02T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  })
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()

  const runId = latestTemporaryRunId('record-maintenance:usage_records_cleanup')
  assert(runId, '临时维护任务应创建 background_task_runs 记录')
  const completed = await waitForTaskRun(runId)
  assert.equal(completed.status, 'completed', `临时维护任务应完成：${JSON.stringify(completed)}`)
  assert.equal(Number(completed.result.deletedRows ?? 0), 1, `临时维护任务应实际删除 1 条记录：${JSON.stringify(completed)}`)
  assert.equal(usageRecordCount('temporary_usage_cleanup_regression'), 0, '临时维护 worker 应删除符合条件的使用记录')
  assert.equal(activeLeaseCount(runId), 0, '临时维护 worker 完成后应释放租约')

  console.log('临时维护 worker 回归通过：常驻 worker 只投递任务，临时进程执行、记录状态并释放租约')
} finally {
  await databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function waitForTaskRun(runId: string): Promise<BackgroundTaskRunSummary> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 20_000) {
    const run = taskRunsRepository.getBackgroundTaskRun(runId)
    if (run && run.status !== 'queued' && run.status !== 'running') {
      return run
    }
    await sleep(100)
  }
  const run = taskRunsRepository.getBackgroundTaskRun(runId)
  throw new Error(`等待临时维护任务完成超时：${JSON.stringify(run)}`)
}

function latestTemporaryRunId(jobName: string): string | undefined {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT run_id AS runId
    FROM background_task_runs
    WHERE job_name = ?
    ORDER BY created_at DESC, run_id DESC
    LIMIT 1
  `).get(jobName) as { runId?: string } | undefined
  return row?.runId
}

function activeLeaseCount(runId: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT COUNT(*) AS count
    FROM background_job_leases
    WHERE run_id = ?
  `).get(runId) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function seedUsageRecord(id: string, createdAt: string): void {
  repositories.createUsageRecordsBatch([{
    id,
    traceId: id,
    systemAccountId: 'sys_admin',
    model: 'gpt-5.4-mini',
    success: true,
    statusCode: 200,
    inputTokens: 1,
    outputTokens: 2,
    cacheReadTokens: 0,
    costUsd: 0.000001,
    durationMs: 10,
    createdAt,
    trafficSource: 'gateway'
  }])
}

function seedUsageStatsCleanupCursors(cursorCreatedAt: string, cursorId: string): void {
  const now = new Date().toISOString()
  const shardKey = usageRecordShardKey(cursorId)
  const statement = databaseModule.getStatsDatabase().prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
    ) VALUES ('usage_shard', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      updated_at = excluded.updated_at
  `)
  statement.run(shardKey, 'usage_stats_aggregation', cursorCreatedAt, cursorId, now, now)
  statement.run(shardKey, 'client_ip_stats_aggregation', cursorCreatedAt, cursorId, now, now)
}

function usageRecordCount(id: string): number {
  return repositories.listUsageRecords(undefined, { traceId: id, pageSize: 10 }).items.length
}

function usageRecordShardKey(id: string): string {
  const row = databaseModule.getDatasetDatabase().prepare(`
    SELECT shard_key AS shardKey
    FROM usage_record_shard_entries
    WHERE usage_id = ?
  `).get(id) as { shardKey?: string } | undefined
  assert(row?.shardKey, '测试使用记录应登记分片目录')
  return row.shardKey
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
