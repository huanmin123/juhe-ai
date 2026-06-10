import { runtimeConfig } from './config/runtime.js'
import {
  type BackgroundWorkerRuntimeSnapshot,
  type BackgroundWorkerQueueRuntime,
  type BackgroundWorkerRuntimeLogQueueRuntime
} from './modules/background/background-ipc.js'
import { getBackgroundJobRuntimeSnapshots, startBackgroundJobs } from './modules/background/background-jobs.js'
import {
  enqueueAuditLogsLocal,
  flushAuditLogQueueForShutdown,
  getAuditLogQueueRuntime,
  installAuditLogQueueShutdownHooks
} from './modules/audit-logs/audit-log-queue.service.js'
import {
  enqueueOperationLogsLocal,
  flushOperationLogQueueForShutdown,
  getOperationLogQueueRuntime,
  installOperationLogQueueShutdownHooks
} from './modules/operation-logs/operation-log-queue.service.js'
import {
  enqueueRecordMaintenanceJobsLocal,
  flushRecordMaintenanceQueueForShutdown,
  getRecordMaintenanceQueueRuntime,
  installRecordMaintenanceQueueShutdownHooks,
  isRecordMaintenanceJob
} from './modules/record-maintenance/record-maintenance-queue.service.js'
import { startRuntimeLogFileImport } from './modules/runtime-logs/runtime-log-file-import.service.js'
import {
  enqueueRuntimeLogLineLocal,
  flushRuntimeLogIndexQueueForShutdown,
  getRuntimeLogIndexRuntime,
  installRuntimeLogIndexQueueShutdownHooks
} from './modules/runtime-logs/runtime-log-index-queue.service.js'
import {
  enqueueUsageRecordsLocal,
  flushUsageRecordQueueForShutdown,
  getUsageRecordQueueRuntime,
  installUsageRecordQueueShutdownHooks
} from './modules/gateway/usage-record-queue.service.js'
import { getCooldownAccountRetestQueueSnapshot } from './modules/background/cooldown-account-retest.service.js'
import {
  cancelAccountTestTaskLocal,
  enqueueAccountTestTaskLocal,
  getManualAccountTestQueueSnapshot,
  startAccountTestTaskQueue
} from './modules/accounts/account-test-task-queue.service.js'
import { datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, statsDatabasePath } from './storage/database.js'
import { errorLogFields, installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'

type WorkerIncomingMessage =
  | { type: 'background_worker_usage_records'; items: unknown[] }
  | { type: 'background_worker_audit_logs'; items: unknown[] }
  | { type: 'background_worker_operation_logs'; items: unknown[] }
  | { type: 'background_worker_record_maintenance'; items: unknown[] }
  | { type: 'background_worker_account_test_tasks'; taskIds: unknown[] }
  | { type: 'background_worker_account_test_cancel'; taskId: unknown }
  | { type: 'background_worker_runtime_log_line'; line: unknown; sourceKey?: unknown; logFile?: unknown; logOffset?: unknown; lineNumber?: unknown }
  | { type: 'background_worker_status_request'; requestId: unknown }

getBusinessDatabase()
getDatasetDatabase()
getStatsDatabase()
installProcessLogHandlers()
startProcessEventLoopMonitor()
startLogMaintenance()
installUsageRecordQueueShutdownHooks()
installOperationLogQueueShutdownHooks()
installRecordMaintenanceQueueShutdownHooks()
installRuntimeLogIndexQueueShutdownHooks()
installAuditLogQueueShutdownHooks()
installWorkerSignalShutdownHooks()
setRuntimeLogLineSink((line, options) => enqueueRuntimeLogLineLocal(line, options))
startRuntimeLogFileImport()
startBackgroundJobs()
startAccountTestTaskQueue()

let workerSignalShutdownInProgress = false

process.on('message', (message: unknown) => {
  if (!isWorkerIncomingMessage(message)) {
    return
  }

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
    case 'background_worker_runtime_log_line':
      if (typeof message.line === 'string') {
        enqueueRuntimeLogLineLocal(message.line, runtimeLogLineOptionsFromMessage(message))
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
    default:
      break
  }
})

sendWorkerMessage({
  type: 'background_worker_ready',
  pid: process.pid
})

logger.info({
  event: 'background_worker_started',
  pid: process.pid,
  processRole: runtimeConfig.processRole,
  databasePath: runtimeConfig.databasePath,
  datasetDatabasePath: datasetDatabasePath(),
  statsDatabasePath: statsDatabasePath()
}, '后台 worker 已启动')

function buildRuntimeSnapshot(): BackgroundWorkerRuntimeSnapshot {
  const auditRuntime = getAuditLogQueueRuntime()
  const runtimeLogRuntime = getRuntimeLogIndexRuntime()
  return {
    pid: process.pid,
    ready: true,
    processRole: 'worker',
    jobs: getBackgroundJobRuntimeSnapshots(),
    usageRecordQueue: queueRuntime(getUsageRecordQueueRuntime()),
    operationLogQueue: queueRuntime(getOperationLogQueueRuntime()),
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
      failureRetentionDays: auditRuntime.failureRetentionDays,
      errorGroupRetentionDays: auditRuntime.errorGroupRetentionDays
    }),
    runtimeLogIndexQueue: runtimeLogQueueRuntime(runtimeLogRuntime),
    cooldownAccountRetestQueue: getCooldownAccountRetestQueueSnapshot(),
    manualAccountTestQueue: getManualAccountTestQueueSnapshot()
  }
}

function queueRuntime(input: BackgroundWorkerQueueRuntime): BackgroundWorkerQueueRuntime {
  return {
    queueLength: Number(input.queueLength ?? 0),
    queueBytes: typeof input.queueBytes === 'number' ? input.queueBytes : undefined,
    flushLastSuccessAt: typeof input.flushLastSuccessAt === 'string' ? input.flushLastSuccessAt : undefined,
    flushLastError: typeof input.flushLastError === 'string' ? input.flushLastError : undefined,
    droppedCount: typeof input.droppedCount === 'number' ? input.droppedCount : undefined,
    droppedSuccessCount: typeof input.droppedSuccessCount === 'number' ? input.droppedSuccessCount : undefined,
    droppedFailureCount: typeof input.droppedFailureCount === 'number' ? input.droppedFailureCount : undefined,
    droppedOverflowCount: typeof input.droppedOverflowCount === 'number' ? input.droppedOverflowCount : undefined,
    droppedOversizeCount: typeof input.droppedOversizeCount === 'number' ? input.droppedOversizeCount : undefined,
    retainedOverflowWarningCount: typeof input.retainedOverflowWarningCount === 'number' ? input.retainedOverflowWarningCount : undefined,
    flushFailureCount: typeof input.flushFailureCount === 'number' ? input.flushFailureCount : undefined,
    successHotRetentionHours: typeof input.successHotRetentionHours === 'number' ? input.successHotRetentionHours : undefined,
    successRetentionDays: typeof input.successRetentionDays === 'number' ? input.successRetentionDays : undefined,
    failureRetentionDays: typeof input.failureRetentionDays === 'number' ? input.failureRetentionDays : undefined,
    errorGroupRetentionDays: typeof input.errorGroupRetentionDays === 'number' ? input.errorGroupRetentionDays : undefined
  }
}

function runtimeLogQueueRuntime(input: BackgroundWorkerRuntimeLogQueueRuntime): BackgroundWorkerRuntimeLogQueueRuntime {
  return {
    ...queueRuntime(input),
    retentionDays: input.retentionDays
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
  flushUsageRecordQueueForShutdown()
  flushOperationLogQueueForShutdown()
  await flushRecordMaintenanceQueueForShutdown()
  flushRuntimeLogIndexQueueForShutdown()
  await flushAuditLogQueueForShutdown()
}

function runtimeLogLineOptionsFromMessage(message: Extract<WorkerIncomingMessage, { type: 'background_worker_runtime_log_line' }>): Parameters<typeof enqueueRuntimeLogLineLocal>[1] {
  return {
    sourceKey: typeof message.sourceKey === 'string' ? message.sourceKey : undefined,
    logFile: typeof message.logFile === 'string' ? message.logFile : undefined,
    logOffset: typeof message.logOffset === 'number' ? message.logOffset : undefined,
    lineNumber: typeof message.lineNumber === 'number' ? message.lineNumber : undefined
  }
}

function isWorkerIncomingMessage(message: unknown): message is WorkerIncomingMessage {
  return typeof message === 'object'
    && message !== null
    && !Array.isArray(message)
    && typeof (message as Record<string, unknown>).type === 'string'
}
