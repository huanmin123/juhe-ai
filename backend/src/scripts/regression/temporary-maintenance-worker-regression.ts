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
const backgroundTaskRunRepositorySource = readFileSync(new URL('../../storage/background-task-runs.repository.ts', import.meta.url), 'utf8')
const backgroundTaskRunReconcileJobSource = readFileSync(new URL('../../modules/background/background-task-run-reconcile.job.ts', import.meta.url), 'utf8')
assert.match(recordMaintenanceQueueSource, /child\.on\('message'[\s\S]*handleTemporaryMaintenanceWorkerMessage/, '临时维护 worker 父进程必须接收子进程 IPC 请求')
assert.match(recordMaintenanceQueueSource, /background_worker_stats_write_request[\s\S]*requestStatsWriter[\s\S]*background_worker_stats_write_response/, '临时维护 worker stats-writer 请求必须由父进程转发并响应')
assert.match(recordMaintenanceQueueSource, /background_worker_db_service_request[\s\S]*requestBackgroundWorkerDbService[\s\S]*background_worker_db_service_response/, '临时维护 worker DB service 请求必须由父进程转发并响应')
assert.match(recordMaintenanceQueueSource, /await spawnTemporaryMaintenanceWorker\(run\.runId, job\)/, 'Redis Stream 数据维护消息必须等临时 worker 成功退出后才能 ACK')
assert.match(recordMaintenanceQueueSource, /function spawnTemporaryMaintenanceWorker[\s\S]*Promise<void>[\s\S]*child\.once\('exit'[\s\S]*code === 0[\s\S]*settle\(\)[\s\S]*settle\(new Error/, '临时维护 worker 非 0 退出必须让父任务失败，消息保持 pending 等待重投')
assert.match(recordMaintenanceQueueSource, /job\.type === 'usage_records_cleanup' \|\| job\.type === 'non_business_data_cleanup' \|\| job\.type === 'audit_retained_data_cleanup'/, '使用记录清理、非业务数据清理和审计保留清理必须走临时维护 worker，不能阻塞主 ingest-worker 消费')
for (const token of [
  'JUHE_AI_RUNTIME_MODE: runtimeConfig.runtimeMode',
  'JUHE_AI_DATABASE_DRIVER: runtimeConfig.databaseDriver',
  'JUHE_AI_CACHE_DRIVER: runtimeConfig.cacheDriver',
  'JUHE_AI_RUNTIME_STATE_DRIVER: runtimeConfig.runtimeStateDriver',
  'JUHE_AI_QUEUE_DRIVER: runtimeConfig.queueDriver',
  'JUHE_AI_POSTGRES_URL: runtimeConfig.postgres.url',
  'JUHE_AI_REDIS_CACHE_URL: runtimeConfig.redis.cacheUrl',
  'JUHE_AI_REDIS_STATE_URL: runtimeConfig.redis.stateUrl',
  'JUHE_AI_REDIS_QUEUE_URL: runtimeConfig.redis.queueUrl'
]) {
  assert(recordMaintenanceQueueSource.includes(token), `临时维护 worker 子进程必须显式继承运行驱动配置：${token}`)
}
assert.match(temporaryMaintenanceWorkerSource, /job\.type === 'usage_records_cleanup' \|\| job\.type === 'non_business_data_cleanup' \|\| job\.type === 'audit_retained_data_cleanup'/, '临时维护 worker runner 必须允许审计保留清理任务')
assert.match(temporaryMaintenanceWorkerSource, /temporaryMaintenanceLeaseUnavailableExitCode = 75[\s\S]*return temporaryMaintenanceLeaseUnavailableExitCode/, '未获得 job 租约的 worker 必须非零退出，使 Redis 消息保持 pending')
assert.match(temporaryMaintenanceWorkerSource, /heartbeatFailure[\s\S]*heartbeatTemporaryBackgroundTaskRun[\s\S]*临时维护 worker 已失去任务租约/, '长任务必须记录心跳续租失败并禁止无租约成功完成')
assert.match(backgroundTaskRunRepositorySource, /SELECT run_id, job_name, lease_key[\s\S]*acquireBackgroundJobLease\([\s\S]*leaseKey: String\(row\.lease_key\)/, '任务启动必须使用 run 中声明的 job 级 leaseKey')
assert.match(backgroundTaskRunRepositorySource, /AND owner_id = \?[\s\S]*AND run_id = \?/, '租约续租和释放必须同时校验 ownerId 与 runId')
assert.match(backgroundTaskRunRepositorySource, /client\.transaction\(async \(tx\)[\s\S]*reconcileQueuedTaskRunsSql[\s\S]*reconcileRunningTaskRunsSql[\s\S]*deleteExpiredTemporaryLeasesSql/, 'PostgreSQL 陈旧任务回收必须在同一事务内收口任务并清理租约')
assert.match(backgroundTaskRunRepositorySource, /current_lease\.run_id = target\.run_id[\s\S]*current_lease\.lease_key = target\.lease_key[\s\S]*current_lease\.lease_until > \?/, '陈旧任务回收必须按 task 的 runId 与 job leaseKey 检查有效租约')
assert.match(backgroundTaskRunReconcileJobSource, /backgroundTaskRunReconcileInitialDelayMs = 2_000[\s\S]*backgroundTaskRunReconcileIntervalMs = 5 \* 60_000[\s\S]*backgroundTaskRunStaleAfterMs = 10 \* 60_000/, '陈旧任务回收必须在 worker 启动后执行并保持低频、带宽限的周期扫描')

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

assert.equal(recordMaintenanceQueue.isRecordMaintenanceJob({
  type: 'audit_retained_data_cleanup',
  nowAt: '2026-07-14T00:00:00.000Z',
  successHotRetentionHours: 1,
  successRetentionDays: 3,
  failureRetentionDays: 7,
  errorGroupRetentionDays: 7,
  successSampleBucketThreshold: 1000,
  batchSize: 100,
  maxBatches: 3
}), true, '审计保留任务必须继续接受既有 Redis wire 字段，避免升级时在途任务变成 poison message')

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
  verifyJobLeaseOwnership()
  verifyStaleBackgroundTaskRunReconciliation()

  console.log('临时维护 worker 回归通过：清理任务隔离执行，陈旧 queued/running 状态按心跳与租约条件安全回收')
} finally {
  await databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function verifyJobLeaseOwnership(): void {
  const first = taskRunsRepository.createBackgroundTaskRun({
    jobName: 'record-maintenance:lease-regression',
    jobType: 'lease-regression',
    workerRole: 'temporary-maintenance-worker',
    leaseKey: 'record-maintenance:lease-regression'
  })
  const second = taskRunsRepository.createBackgroundTaskRun({
    jobName: 'record-maintenance:lease-regression',
    jobType: 'lease-regression',
    workerRole: 'temporary-maintenance-worker',
    leaseKey: 'record-maintenance:lease-regression'
  })
  const now = '2026-07-14T12:00:00.000Z'
  const leaseUntil = '2026-07-14T12:05:00.000Z'
  assert.equal(taskRunsRepository.tryStartBackgroundTaskRun({ runId: first.runId, ownerId: 'owner:first', leaseUntil, now }), true, '第一个同类任务应取得 job 租约')
  assert.equal(taskRunsRepository.tryStartBackgroundTaskRun({ runId: second.runId, ownerId: 'owner:second', leaseUntil, now }), false, '同一 job leaseKey 不得并发启动第二个任务')
  assert.equal(taskRunsRepository.getBackgroundTaskRun(second.runId)?.status, 'queued', '租约竞争失败不得留下无租约 running 状态')
  assert.equal(taskRunsRepository.heartbeatBackgroundTaskRun(first.runId, 'owner:wrong', leaseUntil, now), false, '错误 owner 不得续租')
  assert.equal(taskRunsRepository.finishBackgroundTaskRun({ runId: first.runId, ownerId: 'owner:wrong', status: 'completed', finishedAt: now }), false, '错误 owner 不得完成任务')
  assert.equal(taskRunsRepository.heartbeatBackgroundTaskRun(first.runId, 'owner:first', leaseUntil, now), true, '正确 runId 与 ownerId 应成功续租')
  assert.equal(taskRunsRepository.finishBackgroundTaskRun({ runId: first.runId, ownerId: 'owner:first', status: 'completed', finishedAt: now }), true, '租约 owner 应完成并释放 job 租约')
  assert.equal(taskRunsRepository.tryStartBackgroundTaskRun({ runId: second.runId, ownerId: 'owner:second', leaseUntil, now }), true, '前一任务释放后下一同类任务应取得 job 租约')
  assert.equal(taskRunsRepository.finishBackgroundTaskRun({ runId: second.runId, ownerId: 'owner:second', status: 'completed', finishedAt: now }), true, '第二个任务应正常完成')
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

function verifyStaleBackgroundTaskRunReconciliation(): void {
  const now = '2026-07-14T12:00:00.000Z'
  const staleAt = '2026-07-14T11:00:00.000Z'
  const recentAt = '2026-07-14T11:59:00.000Z'
  const expiredLeaseAt = '2026-07-14T11:55:00.000Z'
  const activeLeaseAt = '2026-07-14T12:05:00.000Z'
  seedTaskRun({ runId: 'bgtask_reconcile_queued_stale', status: 'queued', submittedAt: staleAt, updatedAt: staleAt })
  seedTaskRun({ runId: 'bgtask_reconcile_queued_recent', status: 'queued', submittedAt: recentAt, updatedAt: recentAt })
  seedTaskRun({ runId: 'bgtask_reconcile_queued_active_lease', status: 'queued', submittedAt: staleAt, updatedAt: staleAt })
  seedTaskLease('bgtask_reconcile_queued_active_lease', activeLeaseAt, recentAt)
  seedTaskRun({ runId: 'bgtask_reconcile_running_expired_lease', status: 'running', submittedAt: staleAt, startedAt: staleAt, heartbeatAt: staleAt, updatedAt: staleAt })
  seedTaskLease('bgtask_reconcile_running_expired_lease', expiredLeaseAt, staleAt)
  seedTaskRun({ runId: 'bgtask_reconcile_running_missing_lease', status: 'running', submittedAt: staleAt, startedAt: staleAt, heartbeatAt: staleAt, updatedAt: staleAt })
  seedTaskRun({ runId: 'bgtask_reconcile_running_recent_expired_lease', status: 'running', submittedAt: staleAt, startedAt: staleAt, heartbeatAt: recentAt, updatedAt: recentAt })
  seedTaskLease('bgtask_reconcile_running_recent_expired_lease', expiredLeaseAt, recentAt)
  seedTaskRun({ runId: 'bgtask_reconcile_running_active_lease', status: 'running', submittedAt: staleAt, startedAt: staleAt, heartbeatAt: staleAt, updatedAt: staleAt })
  seedTaskLease('bgtask_reconcile_running_active_lease', activeLeaseAt, recentAt)
  seedTaskRun({ runId: 'bgtask_reconcile_completed_expired_lease', status: 'completed', submittedAt: staleAt, startedAt: staleAt, heartbeatAt: staleAt, updatedAt: staleAt })
  seedTaskLease('bgtask_reconcile_completed_expired_lease', expiredLeaseAt, staleAt)

  const result = taskRunsRepository.reconcileStaleBackgroundTaskRuns({
    queuedBefore: '2026-07-14T11:50:00.000Z',
    runningHeartbeatBefore: '2026-07-14T11:50:00.000Z',
    now,
    limit: 100
  })
  assert.deepEqual(result, {
    failedQueuedCount: 1,
    failedRunningCount: 2,
    deletedExpiredLeaseCount: 2
  }, '回收应只处理陈旧且无有效租约的任务，并清理对应终态过期租约')

  const staleQueued = taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_queued_stale')
  const staleRunning = taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_running_expired_lease')
  assert.equal(staleQueued?.status, 'failed', '超时未启动的 queued 任务应收口为 failed')
  assert.equal(staleQueued?.result.reconciledReason, 'worker_never_started', 'queued 回收应记录稳定机器原因')
  assert.equal(staleRunning?.status, 'failed', '心跳陈旧且租约过期的 running 任务应收口为 failed')
  assert.equal(staleRunning?.result.reconciledReason, 'lease_expired_after_worker_exit', 'running 回收应记录稳定机器原因')
  assert.equal(taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_queued_recent')?.status, 'queued', '近期 queued 任务不能被误回收')
  assert.equal(taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_queued_active_lease')?.status, 'queued', '持有有效租约的 queued 任务不能被误回收')
  assert.equal(taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_running_recent_expired_lease')?.status, 'running', '近期心跳必须保护仍运行的任务，即使旧租约刚过期')
  assert.equal(activeLeaseCount('bgtask_reconcile_running_recent_expired_lease'), 1, '近期 running 任务的过期租约必须保留，允许下一次心跳原地续约')
  assert.equal(taskRunsRepository.getBackgroundTaskRun('bgtask_reconcile_running_active_lease')?.status, 'running', '有效租约必须保护心跳暂时陈旧的 running 任务')
  assert.equal(activeLeaseCount('bgtask_reconcile_running_expired_lease'), 0, '已回收 running 任务的过期租约应删除')
  assert.equal(activeLeaseCount('bgtask_reconcile_completed_expired_lease'), 0, '终态任务遗留的过期租约应删除')

  assert.deepEqual(taskRunsRepository.reconcileStaleBackgroundTaskRuns({
    queuedBefore: '2026-07-14T11:50:00.000Z',
    runningHeartbeatBefore: '2026-07-14T11:50:00.000Z',
    now,
    limit: 100
  }), {
    failedQueuedCount: 0,
    failedRunningCount: 0,
    deletedExpiredLeaseCount: 0
  }, '陈旧任务回收必须幂等')
}

function seedTaskRun(input: {
  runId: string
  status: 'queued' | 'running' | 'completed'
  submittedAt: string
  updatedAt: string
  startedAt?: string
  heartbeatAt?: string
}): void {
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO background_task_runs (
      run_id, job_name, job_type, worker_role, status, lease_key, owner_id,
      params_json, result_json, submitted_at, started_at, heartbeat_at, finished_at, created_at, updated_at
    ) VALUES (?, 'record-maintenance:audit_retained_data_cleanup', 'audit_retained_data_cleanup',
      'temporary-maintenance-worker', ?, ?, ?, '{}', '{}', ?, ?, ?, ?, ?, ?)
  `).run(
    input.runId,
    input.status,
    taskLeaseKey(input.runId),
    input.status === 'queued' ? null : `owner:${input.runId}`,
    input.submittedAt,
    input.startedAt ?? null,
    input.heartbeatAt ?? null,
    input.status === 'completed' ? input.updatedAt : null,
    input.submittedAt,
    input.updatedAt
  )
}

function seedTaskLease(runId: string, leaseUntil: string, heartbeatAt: string): void {
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO background_job_leases (
      lease_key, job_name, shard_key, owner_id, run_id, lease_until, heartbeat_at, started_at, updated_at
    ) VALUES (?, 'record-maintenance:audit_retained_data_cleanup', ?, ?, ?, ?, ?, ?, ?)
  `).run(
    taskLeaseKey(runId),
    runId,
    `owner:${runId}`,
    runId,
    leaseUntil,
    heartbeatAt,
    heartbeatAt,
    heartbeatAt
  )
}

function taskLeaseKey(runId: string): string {
  return `record-maintenance:${runId}`
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
