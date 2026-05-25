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
    assert.equal(result.queued, true, `数据维护任务 ${index} 应被 server IPC 队列保留`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '旧 regular IPC 上限数量应全部保留')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.queueLength, 5000, 'server IPC runtime 应按类型展示维护任务排队数')

  const droppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  const protectedOverflow = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(5000))
  assert.equal(protectedOverflow.queued, true, '超过旧 IPC 队列上限后数据维护任务仍应排队')
  assert.equal(protectedOverflow.droppedReason, undefined, '成功排队不应带投递失败原因')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, droppedBefore, '超过旧 IPC 上限不应进入维护队列 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5001, '超过旧 IPC 上限后应继续增长')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.rejectedCount, 0, 'server IPC runtime 不应记录旧上限拒绝次数')

  const runtimeLogAccepted = backgroundIpc.sendRuntimeLogLineToWorker('{"level":"info","event":"runtime_log_after_old_limit"}')
  assert.equal(runtimeLogAccepted, true, '超过旧 IPC 队列上限后低优先级运行日志仍应排队')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5002, '低优先级消息不应挤掉维护任务')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.runtimeLogLines.droppedCount, 0, 'server IPC runtime 不应记录旧上限导致的运行日志丢弃')

  const operationDroppedBefore = operationLogQueue.getOperationLogQueueRuntime().droppedCount
  operationLogQueue.enqueueOperationLog({
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'server_ipc_old_limit',
    operationKey: 'regression.background_ipc_protected_queue',
    resourceType: 'operation_log',
    summary: 'server IPC 队列满时操作日志投递失败回归'
  })
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().droppedCount, operationDroppedBefore, '超过旧 IPC 上限后 server 操作日志不应进入 dropped 指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5003, '操作日志应突破旧队列上限继续排队')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.operationLogs.rejectedCount, 0, 'server IPC runtime 不应记录操作日志旧上限拒绝次数')

  const auditDroppedBefore = auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount
  auditLogQueue.enqueueAuditLog({
    traceId: 'trace-background-ipc-protected-audit',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'gateway_failed',
    success: false,
    finalStatusCode: 503,
    errorPhase: 'gateway',
    errorCode: 'worker_ipc_old_limit',
    errorMessage: '后台 worker IPC 超过旧上限',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: []
  })
  assert.equal(auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount, auditDroppedBefore, '超过旧 IPC 上限后 server 审计日志不应进入失败丢弃指标')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5004, '审计日志应突破旧队列上限继续排队')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.auditLogs.rejectedCount, 0, 'server IPC runtime 不应记录审计日志旧上限拒绝次数')

  console.log('后台 IPC 队列回归通过：超过旧上限后继续排队，不再因为人工队列阈值丢弃或拒绝')
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
