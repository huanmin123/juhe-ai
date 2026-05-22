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

const [backgroundIpc, recordMaintenanceQueue, operationLogQueue, auditLogQueue, databaseModule] = await Promise.all([
  import('../../modules/background/background-ipc.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/database.js')
])

try {
  for (let index = 0; index < 5000; index += 1) {
    const result = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(index))
    assert.equal(result.queued, true, `记录库维护任务 ${index} 应被 server IPC 队列保留`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '应填满 regular IPC 队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.queueLength, 5000, 'server IPC runtime 应按类型展示维护任务排队数')

  const droppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  const protectedOverflow = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(5000))
  assert.equal(protectedOverflow.queued, false, '队列已满且没有可丢弃项时，新记录库维护任务应显式返回投递失败')
  assert.equal(protectedOverflow.droppedReason, 'worker_dispatch_failed', '显式失败应带上投递失败原因')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, droppedBefore + 1, '队列满导致的 server 投递失败应进入维护队列 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '队列满时不应为保护消息突破上限')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.rejectedCount, 1, 'server IPC runtime 应记录维护任务拒绝次数')

  const runtimeLogAccepted = backgroundIpc.sendRuntimeLogLineToWorker('{"level":"info","event":"runtime_log_after_protected_queue_full"}')
  assert.equal(runtimeLogAccepted, false, '队列已由不可丢弃任务占满时，低优先级运行日志应返回投递失败')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '低优先级消息被拒绝时不应挤掉维护任务')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.droppedCount, 1, 'server IPC runtime 应记录低优先级运行日志被丢弃次数')

  const operationDroppedBefore = operationLogQueue.getOperationLogQueueRuntime().droppedCount
  operationLogQueue.enqueueOperationLog({
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'server_ipc_queue_full',
    operationKey: 'regression.background_ipc_protected_queue',
    resourceType: 'operation_log',
    summary: 'server IPC 队列满时操作日志投递失败回归'
  })
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().droppedCount, operationDroppedBefore + 1, 'server 操作日志投递被拒绝时应进入 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '操作日志被拒绝时不应突破队列上限')
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
    errorCode: 'worker_queue_full',
    errorMessage: '后台 worker IPC 队列已满',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: []
  })
  assert.equal(auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount, auditDroppedBefore + 1, 'server 审计日志投递被拒绝时应进入失败丢弃指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '审计日志被拒绝时不应突破队列上限')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.auditLogs.rejectedCount, 1, 'server IPC runtime 应记录审计日志拒绝次数')

  console.log('后台 IPC 保护队列回归通过：记录库维护任务不会被 regular 队列溢出静默丢弃，队列满时会显式失败')
} finally {
  try {
    databaseModule.getDatabase().close()
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
