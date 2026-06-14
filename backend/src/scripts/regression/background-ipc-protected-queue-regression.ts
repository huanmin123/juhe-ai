import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-background-ipc-protected-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'background-ipc-protected-queue-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [backgroundIpc, recordMaintenanceQueue, operationLogQueue, auditLogQueue, runtimeLogIndexQueue, databaseModule] = await Promise.all([
  import('../../modules/background/background-ipc.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/runtime-logs/runtime-log-index-queue.service.js'),
  import('../../storage/database.js')
])

try {
  for (let index = 0; index < 5000; index += 1) {
    const result = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(index))
    assert.equal(result.queued, true, `数据维护任务 ${index} 应被 server IPC 队列保留`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, 'regular IPC 队列达到保护上限前应全部保留')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.queueLength, 5000, 'server IPC runtime 应按类型展示维护任务排队数')

  const droppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  const protectedOverflow = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(5000))
  assert.equal(protectedOverflow.queued, false, '超过 regular IPC 队列上限后数据维护任务应快速拒绝')
  assert.equal(protectedOverflow.droppedReason, 'worker_dispatch_failed', '拒绝排队应返回投递失败原因')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, droppedBefore + 1, '超过 IPC 上限应进入维护队列 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '超过 IPC 上限后 pending 数不应继续增长')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.rejectedCount, 1, 'server IPC runtime 应记录维护任务拒绝次数')

  const runtimeLogAccepted = backgroundIpc.sendRuntimeLogLineToWorker('{"level":"info","event":"runtime_log_after_default_queue_limit"}')
  assert.equal(runtimeLogAccepted, true, '默认 worker regular IPC 队列满时运行日志仍应进入 ingest-worker 队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5001, '运行日志进入独立 ingest 队列后总 pending 数应增长')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.queueLength, 5000, '运行日志不应挤掉默认 worker 维护任务')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.queueLength, 1, 'server IPC runtime 应按类型展示 ingest 运行日志排队数')
  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
  runtimeLogIndexQueue.enqueueRuntimeLogLine('{"level":"info","event":"runtime_log_server_dispatch_to_ingest"}')
  assert.equal(runtimeLogIndexQueue.getRuntimeLogIndexRuntime().queueLength, 0, 'server 运行日志只投递 ingest-worker，不应回退到本地 SQLite 队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.queueLength, 2, '运行日志索引队列应同样投递到 ingest-worker IPC')

  for (let index = 3; index <= 5000; index += 1) {
    const accepted = backgroundIpc.sendRuntimeLogLineToWorker(`{"level":"info","event":"runtime_log_fill_ingest_queue","index":${index}}`)
    assert.equal(accepted, true, `ingest regular IPC 队列达到保护上限前运行日志 ${index} 应被保留`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.queueLength, 5000, 'ingest regular IPC 队列应独立达到保护上限')
  const runtimeLogOverflow = backgroundIpc.sendRuntimeLogLineToWorker('{"level":"info","event":"runtime_log_after_ingest_queue_limit"}')
  assert.equal(runtimeLogOverflow, false, '超过 ingest regular IPC 队列上限后运行日志应快速拒绝')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 10_000, 'ingest regular 队列溢出后 pending 数不应继续增长')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.rejectedCount, 1, 'server IPC runtime 应记录运行日志拒绝次数')
  runtimeLogIndexQueue.enqueueRuntimeLogLine('{"level":"info","event":"runtime_log_ingest_fallback_blocked"}')
  assert.equal(runtimeLogIndexQueue.getRuntimeLogIndexRuntime().queueLength, 0, 'server worker IPC 拒绝时不应回退到本地运行日志 SQLite 队列')

  const operationDroppedBefore = operationLogQueue.getOperationLogQueueRuntime().droppedCount
  operationLogQueue.enqueueOperationLog({
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'server_ipc_queue_limit',
    operationKey: 'regression.background_ipc_protected_queue',
    resourceType: 'operation_log',
    summary: 'server IPC 队列满时操作日志投递失败回归'
  })
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().droppedCount, operationDroppedBefore + 1, '超过 IPC 上限后 server 操作日志应进入 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 10_000, '操作日志不应突破 ingest regular IPC 队列上限继续排队')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.operationLogs.rejectedCount, 1, 'server IPC runtime 应记录操作日志拒绝次数')

  const auditDroppedBefore = auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount
  auditLogQueue.enqueueAuditLog({
    traceId: 'trace-background-ipc-protected-audit',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'gateway_failed',
    success: false,
    finalStatusCode: 503,
    errorPhase: 'gateway',
    errorCode: 'worker_ipc_queue_limit',
    errorMessage: '后台 worker IPC 超过队列上限',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: []
  })
  assert.equal(auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount, auditDroppedBefore + 1, '超过 IPC 上限后 server 审计日志应进入失败丢弃指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 10_000, '审计日志不应突破 ingest regular IPC 队列上限继续排队')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.auditLogs.rejectedCount, 1, 'server IPC runtime 应记录审计日志拒绝次数')

  for (let index = 0; index < 10_000; index += 1) {
    assert.equal(backgroundIpc.sendUsageRecordsToWorker([buildUsageRecord(index)]), true, `使用记录 ${index} 应进入独立 usage IPC 队列`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.queueLength, 10_000, 'usage IPC 队列应独立达到保护上限')
  const usageOverflowAccepted = backgroundIpc.sendUsageRecordsToWorker([buildUsageRecord(10_000)])
  assert.equal(usageOverflowAccepted, false, '超过 usage IPC 队列上限后使用记录应快速拒绝')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.rejectedCount, 1, 'server IPC runtime 应记录使用记录拒绝次数')

  console.log('后台 IPC 队列回归通过：server 到默认 worker 与 ingest-worker 的 regular/usage IPC 队列达到上限后会快速拒绝并记录指标，避免请求侧副作用无限堆积')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildUsageRecordsCleanupJob(index: number) {
  return {
    type: 'usage_records_cleanup' as const,
    id: `recmaint_ipc_protected_${index}`,
    cutoffAt: '2000-01-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  }
}

function buildUsageRecord(index: number) {
  return {
    traceId: `trace-background-ipc-usage-${index}`,
    trafficSource: 'gateway' as const,
    systemAccountId: 'sys_admin',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: true,
    stream: false,
    statusCode: 200,
    durationMs: 1,
    inputTokens: 1,
    outputTokens: 1,
    costUsd: 0,
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}
