import { runtimeConfig } from './config/runtime.js'
import {
  type BackgroundWorkerRuntimeSnapshot,
  type BackgroundWorkerQueueRuntime,
  type BackgroundWorkerRuntimeLogQueueRuntime
} from './modules/background/background-ipc.js'
import { getBackgroundJobRuntimeSnapshots, startBackgroundJobs, triggerAccountHealthCheckNow } from './modules/background/background-jobs.js'
import {
  enqueueAuditLogsLocal,
  flushAuditLogQueueForShutdown,
  getAuditLogQueueRuntime,
  installAuditLogQueueShutdownHooks,
  startAuditLogRedisStreamConsumer,
  stopAuditLogRedisStreamConsumer
} from './modules/audit-logs/audit-log-queue.service.js'
import {
  enqueueOperationLogsLocal,
  flushOperationLogQueueForShutdown,
  getOperationLogQueueRuntime,
  installOperationLogQueueShutdownHooks,
  startOperationLogRedisStreamConsumer,
  stopOperationLogRedisStreamConsumer
} from './modules/operation-logs/operation-log-queue.service.js'
import {
  enqueuePublicApiLogsLocal,
  flushPublicApiLogQueueForShutdown,
  getPublicApiLogQueueRuntime,
  installPublicApiLogQueueShutdownHooks,
  startPublicApiLogRedisStreamConsumer,
  stopPublicApiLogRedisStreamConsumer
} from './modules/public-api-logs/public-api-log-queue.service.js'
import {
  enqueueRecordMaintenanceJobsLocal,
  flushRecordMaintenanceQueueForShutdown,
  getRecordMaintenanceQueueRuntime,
  installRecordMaintenanceQueueShutdownHooks,
  isRecordMaintenanceJob,
  startRecordMaintenanceRedisStreamConsumer,
  stopRecordMaintenanceRedisStreamConsumer
} from './modules/record-maintenance/record-maintenance-queue.service.js'
import { getRuntimeLogFileImportRuntime, startRuntimeLogFileImport } from './modules/runtime-logs/runtime-log-file-import.service.js'
import {
  enqueueUsageRecordsLocal,
  flushUsageRecordQueueForShutdown,
  getUsageRecordQueueRuntime,
  installUsageRecordQueueShutdownHooks,
  startUsageRecordRedisStreamConsumer,
  stopUsageRecordRedisStreamConsumer
} from './modules/gateway/usage/record-queue.service.js'
import { closeUsageRecordWriterPool } from './storage/usage-record-writer-pool.js'
import { getCooldownAccountRetestQueueSnapshot } from './modules/background/cooldown-account-retest.service.js'
import { getAccountApiKeyCooldownRetestQueueSnapshot } from './modules/background/account-api-key-cooldown-retest.service.js'
import { getAccountHealthCheckQueueSnapshot } from './modules/background/account-health-check.service.js'
import { getAccountQualityFailurePrecheckQueueSnapshot } from './modules/background/account-quality-failure-precheck.service.js'
import { getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot } from './modules/background/normal-route-speed-first-recovery-probe.service.js'
import { handleStatsWriteOperation, type BackgroundStatsWriteOperation } from './modules/background/background-stats-writer.js'
import { handleDatasetWriteOperation, type BackgroundDatasetWriteOperation } from './modules/background/background-dataset-writer.js'
import {
  cancelAccountTestTaskLocal,
  enqueueAccountTestTaskLocal,
  getManualAccountTestQueueSnapshot,
  startAccountTestTaskQueue
} from './modules/accounts/account-test-task-queue.service.js'
import { datasetDatabasePath, getDatasetDatabase, getUsageCatalogDatabase, statsDatabasePath, usageCatalogDatabasePath } from './storage/database.js'
import { errorLogFields, installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { buildProcessEventLoopSample, startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { isAccountHealthCheckTriggerReason } from './modules/accounts/account-health-check-trigger.js'

type WorkerIncomingMessage =
  | { type: 'background_worker_usage_records'; items: unknown[] }
  | { type: 'background_worker_audit_logs'; items: unknown[] }
  | { type: 'background_worker_operation_logs'; items: unknown[] }
  | { type: 'background_worker_public_api_logs'; items: unknown[] }
  | { type: 'background_worker_record_maintenance'; items: unknown[] }
  | { type: 'background_worker_account_test_tasks'; taskIds: unknown[] }
  | { type: 'background_worker_account_test_cancel'; taskId: unknown }
  | { type: 'background_worker_account_health_check_trigger'; accountId: unknown; reason: unknown }
  | { type: 'background_worker_status_request'; requestId: unknown }
  | { type: 'background_worker_dataset_write_request'; requestId: unknown; operation: unknown }
  | { type: 'background_worker_stats_write_request'; requestId: unknown; operation: unknown }
  | { type: 'background_worker_process_event_loop_request'; requestId: unknown }

installProcessLogHandlers()
startProcessEventLoopMonitor()
installWorkerSignalShutdownHooks()
if (isIngestWorker()) {
  if (runtimeConfig.databaseDriver === 'sqlite') {
    getDatasetDatabase()
    getUsageCatalogDatabase()
  }
  startLogMaintenance()
  installUsageRecordQueueShutdownHooks()
  installOperationLogQueueShutdownHooks()
  installAuditLogQueueShutdownHooks()
  installPublicApiLogQueueShutdownHooks()
  installRecordMaintenanceQueueShutdownHooks()
  startUsageRecordRedisStreamConsumer()
  startOperationLogRedisStreamConsumer()
  startPublicApiLogRedisStreamConsumer()
  startRecordMaintenanceRedisStreamConsumer()
  startAuditLogRedisStreamConsumer()
  startRuntimeLogFileImport()
} else if (isOpsWorker()) {
  startAccountTestTaskQueue()
}
startBackgroundJobs()

let workerSignalShutdownInProgress = false

process.on('message', (message: unknown) => {
  if (!isWorkerIncomingMessage(message)) {
    return
  }
  if (isIngestWorker() && !isIngestWorkerMessage(message)) {
    return
  }
  if (isOpsWorker() && !isOpsWorkerMessage(message)) {
    return
  }
  if (isStatsWorker() && !isWorkerControlMessage(message)) {
    return
  }
  assertLocalQueueIpcAllowed(message)

  switch (message.type) {
    case 'background_worker_usage_records':
      enqueueUsageRecordsLocal(message.items as Parameters<typeof enqueueUsageRecordsLocal>[0])
      break
    case 'background_worker_audit_logs':
      enqueueAuditLogsLocal(message.items as Parameters<typeof enqueueAuditLogsLocal>[0])
      break
    case 'background_worker_operation_logs':
      enqueueOperationLogsLocal(message.items as Parameters<typeof enqueueOperationLogsLocal>[0])
      break
    case 'background_worker_public_api_logs':
      enqueuePublicApiLogsLocal(message.items as Parameters<typeof enqueuePublicApiLogsLocal>[0])
      break
    case 'background_worker_record_maintenance':
      enqueueRecordMaintenanceJobsLocal(message.items.filter(isRecordMaintenanceJob))
      break
    case 'background_worker_account_test_tasks':
      for (const taskId of message.taskIds) {
        if (typeof taskId === 'string') {
          enqueueAccountTestTaskLocal(taskId)
        }
      }
      break
    case 'background_worker_account_test_cancel':
      if (typeof message.taskId === 'string') {
        cancelAccountTestTaskLocal(message.taskId)
      }
      break
    case 'background_worker_account_health_check_trigger':
      if (typeof message.accountId === 'string' && isAccountHealthCheckTriggerReason(message.reason)) {
        void triggerAccountHealthCheckNow(message.accountId, message.reason).catch((error) => {
          logger.warn(errorLogFields(error, {
            event: 'background_account_health_check_trigger_failed',
            accountId: message.accountId
          }), '立即触发账户健康检查失败，等待周期任务兜底')
        })
      }
      break
    case 'background_worker_status_request':
      if (typeof message.requestId === 'string') {
        sendWorkerMessage({
          type: 'background_worker_status_response',
          requestId: message.requestId,
          snapshot: buildRuntimeSnapshot()
        })
      }
      break
    case 'background_worker_dataset_write_request':
      if (typeof message.requestId === 'string' && isIngestWorker()) {
        void respondToDatasetWriteRequest(message.requestId, message.operation)
      }
      break
    case 'background_worker_stats_write_request':
      if (typeof message.requestId === 'string' && isStatsWorker()) {
        void respondToStatsWriteRequest(message.requestId, message.operation)
      }
      break
    case 'background_worker_process_event_loop_request':
      if (typeof message.requestId === 'string') {
        sendWorkerMessage({
          type: 'background_worker_process_event_loop_response',
          requestId: message.requestId,
          samples: [buildProcessEventLoopSample()]
        })
      }
      break
    default:
      break
  }
})

sendWorkerMessage({
  type: 'background_worker_ready',
  pid: process.pid,
  workerRole: runtimeConfig.workerRole
})

logger.info({
  event: 'background_worker_started',
  pid: process.pid,
  processRole: runtimeConfig.processRole,
  workerRole: runtimeConfig.workerRole,
  databasePath: runtimeConfig.databasePath,
  datasetDatabasePath: datasetDatabasePath(),
  usageCatalogDatabasePath: usageCatalogDatabasePath(),
  statsDatabasePath: statsDatabasePath()
}, workerStartedMessage())

function buildRuntimeSnapshot(): BackgroundWorkerRuntimeSnapshot {
  const auditRuntime = getAuditLogQueueRuntime()
  const runtimeLogRuntime = getRuntimeLogFileImportRuntime()
  return {
    pid: process.pid,
    ready: true,
    processRole: 'worker',
    workerRole: currentBackgroundWorkerRole(),
    jobs: getBackgroundJobRuntimeSnapshots(),
    usageRecordQueue: queueRuntime(getUsageRecordQueueRuntime()),
    operationLogQueue: queueRuntime(getOperationLogQueueRuntime()),
    publicApiLogQueue: queueRuntime(getPublicApiLogQueueRuntime()),
    recordMaintenanceQueue: queueRuntime(getRecordMaintenanceQueueRuntime()),
    auditLogQueue: queueRuntime({
      queueLength: auditRuntime.queueLength,
      queueBytes: auditRuntime.queueBytes,
      flushLastSuccessAt: auditRuntime.flushLastSuccessAt,
      flushLastError: auditRuntime.flushLastError,
      droppedCount: auditRuntime.droppedSuccessCount
        + auditRuntime.droppedFailureCount
        + auditRuntime.droppedOverflowCount
        + auditRuntime.droppedOversizeCount,
      droppedSuccessCount: auditRuntime.droppedSuccessCount,
      droppedFailureCount: auditRuntime.droppedFailureCount,
      droppedOverflowCount: auditRuntime.droppedOverflowCount,
      droppedOversizeCount: auditRuntime.droppedOversizeCount,
      successHotRetentionHours: auditRuntime.successHotRetentionHours,
      successRetentionDays: auditRuntime.successRetentionDays,
      problemRetentionDays: auditRuntime.problemRetentionDays,
      successFullBodyLimitBytes: auditRuntime.successFullBodyLimitBytes,
      problemFullBodyLimitBytes: auditRuntime.problemFullBodyLimitBytes
    }),
    runtimeLogIndexQueue: runtimeLogQueueRuntime(runtimeLogRuntime),
    accountHealthCheckQueue: getAccountHealthCheckQueueSnapshot(),
    cooldownAccountRetestQueue: getCooldownAccountRetestQueueSnapshot(),
    accountApiKeyCooldownRetestQueue: getAccountApiKeyCooldownRetestQueueSnapshot(),
    normalRouteSpeedFirstRecoveryProbeQueue: getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot(),
    accountQualityFailurePrecheckQueue: getAccountQualityFailurePrecheckQueueSnapshot(),
    manualAccountTestQueue: getManualAccountTestQueueSnapshot()
  }
}

function currentBackgroundWorkerRole(): BackgroundWorkerRuntimeSnapshot['workerRole'] {
  if (runtimeConfig.workerRole === 'ingest-worker'
    || runtimeConfig.workerRole === 'stats-worker'
    || runtimeConfig.workerRole === 'ops-worker') {
    return runtimeConfig.workerRole
  }
  return 'worker'
}

async function respondToStatsWriteRequest(requestId: string, operation: unknown): Promise<void> {
  try {
    const result = await handleStatsWriteOperation(operation as BackgroundStatsWriteOperation)
    sendWorkerMessage({
      type: 'background_worker_stats_write_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendWorkerMessage({
      type: 'background_worker_stats_write_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function respondToDatasetWriteRequest(requestId: string, operation: unknown): Promise<void> {
  try {
    const result = await handleDatasetWriteOperation(operation as BackgroundDatasetWriteOperation)
    sendWorkerMessage({
      type: 'background_worker_dataset_write_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendWorkerMessage({
      type: 'background_worker_dataset_write_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

function queueRuntime(input: BackgroundWorkerQueueRuntime): BackgroundWorkerQueueRuntime {
  return {
    queueLength: Number(input.queueLength ?? 0),
    queueBytes: typeof input.queueBytes === 'number' ? input.queueBytes : undefined,
    flushLastSuccessAt: typeof input.flushLastSuccessAt === 'string' ? input.flushLastSuccessAt : undefined,
    flushLastError: typeof input.flushLastError === 'string' ? input.flushLastError : undefined,
    completedCount: typeof input.completedCount === 'number' ? input.completedCount : undefined,
    droppedCount: typeof input.droppedCount === 'number' ? input.droppedCount : undefined,
    droppedSuccessCount: typeof input.droppedSuccessCount === 'number' ? input.droppedSuccessCount : undefined,
    droppedFailureCount: typeof input.droppedFailureCount === 'number' ? input.droppedFailureCount : undefined,
    droppedOverflowCount: typeof input.droppedOverflowCount === 'number' ? input.droppedOverflowCount : undefined,
    droppedOversizeCount: typeof input.droppedOversizeCount === 'number' ? input.droppedOversizeCount : undefined,
    retainedOverflowWarningCount: typeof input.retainedOverflowWarningCount === 'number' ? input.retainedOverflowWarningCount : undefined,
    flushFailureCount: typeof input.flushFailureCount === 'number' ? input.flushFailureCount : undefined,
    oldestQueuedMs: typeof input.oldestQueuedMs === 'number' ? input.oldestQueuedMs : undefined,
    lastFlushMs: typeof input.lastFlushMs === 'number' ? input.lastFlushMs : undefined,
    maxFlushMs: typeof input.maxFlushMs === 'number' ? input.maxFlushMs : undefined,
    slowFlushCount: typeof input.slowFlushCount === 'number' ? input.slowFlushCount : undefined,
    lastSlowFlushAt: typeof input.lastSlowFlushAt === 'string' ? input.lastSlowFlushAt : undefined,
    writerPoolEnabled: typeof input.writerPoolEnabled === 'boolean' ? input.writerPoolEnabled : undefined,
    writerPoolWorkerCount: typeof input.writerPoolWorkerCount === 'number' ? input.writerPoolWorkerCount : undefined,
    writerPoolQueueLength: typeof input.writerPoolQueueLength === 'number' ? input.writerPoolQueueLength : undefined,
    writerPoolActiveJobs: typeof input.writerPoolActiveJobs === 'number' ? input.writerPoolActiveJobs : undefined,
    writerPoolHandledJobs: typeof input.writerPoolHandledJobs === 'number' ? input.writerPoolHandledJobs : undefined,
    writerPoolFailedJobs: typeof input.writerPoolFailedJobs === 'number' ? input.writerPoolFailedJobs : undefined,
    writerPoolRejectedJobs: typeof input.writerPoolRejectedJobs === 'number' ? input.writerPoolRejectedJobs : undefined,
    writerPoolOldestQueuedMs: typeof input.writerPoolOldestQueuedMs === 'number' ? input.writerPoolOldestQueuedMs : undefined,
    writerPoolMaxQueueWaitMs: typeof input.writerPoolMaxQueueWaitMs === 'number' ? input.writerPoolMaxQueueWaitMs : undefined,
    writerPoolMaxRunMs: typeof input.writerPoolMaxRunMs === 'number' ? input.writerPoolMaxRunMs : undefined,
    successHotRetentionHours: typeof input.successHotRetentionHours === 'number' ? input.successHotRetentionHours : undefined,
    successRetentionDays: typeof input.successRetentionDays === 'number' ? input.successRetentionDays : undefined,
    problemRetentionDays: typeof input.problemRetentionDays === 'number' ? input.problemRetentionDays : undefined,
    successFullBodyLimitBytes: typeof input.successFullBodyLimitBytes === 'number' ? input.successFullBodyLimitBytes : undefined,
    problemFullBodyLimitBytes: typeof input.problemFullBodyLimitBytes === 'number' ? input.problemFullBodyLimitBytes : undefined
  }
}

function runtimeLogQueueRuntime(input: BackgroundWorkerRuntimeLogQueueRuntime): BackgroundWorkerRuntimeLogQueueRuntime {
  return {
    ...queueRuntime(input),
    retentionDays: input.retentionDays,
    discoveredFileCount: typeof input.discoveredFileCount === 'number' ? input.discoveredFileCount : 0,
    pendingFileCount: typeof input.pendingFileCount === 'number' ? input.pendingFileCount : 0,
    pendingBytes: typeof input.pendingBytes === 'number' ? input.pendingBytes : 0,
    oldestPendingMtime: typeof input.oldestPendingMtime === 'string' ? input.oldestPendingMtime : undefined,
    currentFile: typeof input.currentFile === 'string' ? input.currentFile : undefined,
    currentOffset: typeof input.currentOffset === 'number' ? input.currentOffset : 0,
    lastReadAt: typeof input.lastReadAt === 'string' ? input.lastReadAt : undefined,
    lastCommitAt: typeof input.lastCommitAt === 'string' ? input.lastCommitAt : undefined,
    lastError: typeof input.lastError === 'string' ? input.lastError : undefined,
    protectedRotatedFileCount: typeof input.protectedRotatedFileCount === 'number' ? input.protectedRotatedFileCount : 0
  }
}

function sendWorkerMessage(message: Record<string, unknown>): void {
  if (!process.send) {
    return
  }
  try {
    process.send(message, (error) => {
      if (error) {
        logger.error(errorLogFields(error, {
          event: 'background_worker_child_ipc_send_failed',
          messageType: typeof message.type === 'string' ? message.type : undefined
        }), '后台 worker 向父进程发送 IPC 消息失败，进程将退出等待 supervisor 重启')
        process.exit(1)
      }
    })
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'background_worker_child_ipc_send_failed',
      messageType: typeof message.type === 'string' ? message.type : undefined
    }), '后台 worker 向父进程发送 IPC 消息失败，进程将退出等待 supervisor 重启')
    process.exit(1)
  }
}

function installWorkerSignalShutdownHooks(): void {
  process.once('SIGINT', () => {
    void exitAfterWorkerQueueFlush(0)
  })
  process.once('SIGTERM', () => {
    void exitAfterWorkerQueueFlush(0)
  })
}

async function exitAfterWorkerQueueFlush(exitCode: number): Promise<never> {
  if (workerSignalShutdownInProgress) {
    process.exit(exitCode)
  }
  workerSignalShutdownInProgress = true
  try {
    await flushWorkerQueuesForShutdown()
  } finally {
    process.exit(exitCode)
  }
}

async function flushWorkerQueuesForShutdown(): Promise<void> {
  if (isIngestWorker()) {
    await stopUsageRecordRedisStreamConsumer()
    await stopOperationLogRedisStreamConsumer()
    await stopPublicApiLogRedisStreamConsumer()
    await stopRecordMaintenanceRedisStreamConsumer()
    await stopAuditLogRedisStreamConsumer()
    await flushUsageRecordQueueForShutdown()
    await closeUsageRecordWriterPool()
    flushOperationLogQueueForShutdown()
    flushPublicApiLogQueueForShutdown()
    await flushRecordMaintenanceQueueForShutdown()
    await flushAuditLogQueueForShutdown()
    return
  }
}

function isIngestWorker(): boolean {
  return runtimeConfig.workerRole === 'ingest-worker'
}

function isStatsWorker(): boolean {
  return runtimeConfig.workerRole === 'stats-worker'
}

function isOpsWorker(): boolean {
  return runtimeConfig.workerRole === 'ops-worker'
}

function isWorkerControlMessage(message: WorkerIncomingMessage): boolean {
  return message.type === 'background_worker_status_request'
    || message.type === 'background_worker_stats_write_request'
    || message.type === 'background_worker_process_event_loop_request'
}

function isIngestWorkerMessage(message: WorkerIncomingMessage): boolean {
  return isWorkerControlMessage(message)
    || message.type === 'background_worker_usage_records'
    || message.type === 'background_worker_audit_logs'
    || message.type === 'background_worker_operation_logs'
    || message.type === 'background_worker_public_api_logs'
    || message.type === 'background_worker_record_maintenance'
    || message.type === 'background_worker_dataset_write_request'
}

function assertLocalQueueIpcAllowed(message: WorkerIncomingMessage): void {
  if (runtimeConfig.queueDriver !== 'redis_stream') return
  if (!isLocalQueueIpcMessage(message)) return
  throw new Error(`Redis Stream queue driver 下禁止消费后台 IPC 本地队列消息：${message.type}`)
}

function isLocalQueueIpcMessage(message: WorkerIncomingMessage): boolean {
  return message.type === 'background_worker_usage_records'
    || message.type === 'background_worker_audit_logs'
    || message.type === 'background_worker_operation_logs'
    || message.type === 'background_worker_public_api_logs'
    || message.type === 'background_worker_record_maintenance'
}

function isOpsWorkerMessage(message: WorkerIncomingMessage): boolean {
  return isWorkerControlMessage(message)
    || message.type === 'background_worker_account_test_tasks'
    || message.type === 'background_worker_account_test_cancel'
    || message.type === 'background_worker_account_health_check_trigger'
}

function workerStartedMessage(): string {
  if (isIngestWorker()) return '后台 ingest-worker 已启动'
  if (isStatsWorker()) return '后台 stats-worker 已启动'
  if (isOpsWorker()) return '后台 ops-worker 已启动'
  return '后台 worker 已启动'
}

function isWorkerIncomingMessage(message: unknown): message is WorkerIncomingMessage {
  return typeof message === 'object'
    && message !== null
    && !Array.isArray(message)
    && typeof (message as Record<string, unknown>).type === 'string'
}
