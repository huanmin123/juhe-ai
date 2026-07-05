import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { BackgroundTaskRunSummary } from '../../storage/background-task-runs.repository.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-temporary-maintenance-worker-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const recordMaintenanceQueueSource = readFileSync(new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url), 'utf8')
const temporaryMaintenanceWorkerSource = readFileSync(new URL('../../modules/record-maintenance/temporary-maintenance-worker-runner.ts', import.meta.url), 'utf8')
assert.match(recordMaintenanceQueueSource, /child\.on\('message'[\s\S]*handleTemporaryMaintenanceWorkerMessage/, '临时维护 worker 父进程必须接收子进程 IPC 请求')
assert.match(recordMaintenanceQueueSource, /background_worker_stats_write_request[\s\S]*requestStatsWriter[\s\S]*background_worker_stats_write_response/, '临时维护 worker stats-writer 请求必须由父进程转发并响应')
assert.match(recordMaintenanceQueueSource, /background_worker_db_service_request[\s\S]*requestBackgroundWorkerDbService[\s\S]*background_worker_db_service_response/, '临时维护 worker DB service 请求必须由父进程转发并响应')
assert.match(recordMaintenanceQueueSource, /await spawnTemporaryMaintenanceWorker\(run\.runId, job\)/, 'Redis Stream 数据维护消息必须等临时 worker 成功退出后才能 ACK')
assert.match(recordMaintenanceQueueSource, /function spawnTemporaryMaintenanceWorker[\s\S]*Promise<void>[\s\S]*child\.once\('exit'[\s\S]*code === 0[\s\S]*settle\(\)[\s\S]*settle\(new Error/, '临时维护 worker 非 0 退出必须让父任务失败，消息保持 pending 等待重投')
assert.match(recordMaintenanceQueueSource, /job\.type === 'usage_records_cleanup' \|\| job\.type === 'non_business_data_cleanup' \|\| job\.type === 'audit_retained_data_cleanup'/, '使用记录清理、非业务数据清理和审计保留清理必须走临时维护 worker，不能阻塞主 ingest-worker 消费')
assert.match(temporaryMaintenanceWorkerSource, /job\.type === 'usage_records_cleanup' \|\| job\.type === 'non_business_data_cleanup' \|\| job\.type === 'audit_retained_data_cleanup'/, '临时维护 worker runner 必须允许审计保留清理任务')

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
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
  assert(runId, '使用记录清理必须 fork 临时维护 worker')
  const run = await waitForTaskRun(runId)
  assert.equal(run.status, 'completed', '使用记录清理临时维护 worker 应执行成功')
  assert.equal(activeLeaseCount(runId), 0, '临时维护 worker 完成后不应残留租约')
  assert.equal(usageRecordCount('temporary_usage_cleanup_regression'), 0, '临时维护 worker 应删除符合条件的使用记录')
  assert.equal(eventLoopSampleCount('temporary-maintenance-worker'), 0, '临时维护 worker 禁止直接写入 stats 事件循环采样')

  console.log('临时维护 worker 回归通过：使用记录清理走临时 worker，不阻塞 ingest 且不直写 stats 采样')
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

function eventLoopSampleCount(processRole: string): number {
  const row = databaseModule.getStatsDatabase().prepare(`
    SELECT COUNT(*) AS count
    FROM process_event_loop_samples
    WHERE process_role = ?
  `).get(processRole) as { count?: number } | undefined
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
  const row = databaseModule.getUsageCatalogDatabase().prepare(`
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
