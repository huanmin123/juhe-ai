import { runtimeConfig } from './config/runtime.js'
import {
  type BackgroundWorkerRuntimeSnapshot,
  type BackgroundWorkerQueueRuntime,
  type BackgroundWorkerRuntimeLogQueueRuntime
} from './modules/background/background-ipc.js'
import { getBackgroundJobRuntimeSnapshots, startBackgroundJobs } from './modules/background/background-jobs.js'
import { enqueueAuditLogsLocal, flushAllAuditLogQueue, getAuditLogQueueRuntime } from './modules/audit-logs/audit-log-queue.service.js'
import {
  enqueueOperationLogsLocal,
  getOperationLogQueueRuntime,
  installOperationLogQueueShutdownHooks
} from './modules/operation-logs/operation-log-queue.service.js'
import {
  enqueueRecordMaintenanceJobsLocal,
  getRecordMaintenanceQueueRuntime,
  installRecordMaintenanceQueueShutdownHooks,
  isRecordMaintenanceJob
} from './modules/record-maintenance/record-maintenance-queue.service.js'
import { startRuntimeLogFileImport } from './modules/runtime-logs/runtime-log-file-import.service.js'
import {
  enqueueRuntimeLogLineLocal,
  getRuntimeLogIndexRuntime,
  installRuntimeLogIndexQueueShutdownHooks
} from './modules/runtime-logs/runtime-log-index-queue.service.js'
import {
  enqueueUsageRecordsLocal,
  getUsageRecordQueueRuntime,
  installUsageRecordQueueShutdownHooks
} from './modules/gateway/usage-record-queue.service.js'
import { getBusinessDatabase, getRecordDatabase } from './storage/database.js'
import { installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'

type WorkerIncomingMessage =
  | { type: 'background_worker_usage_records'; items: unknown[] }
  | { type: 'background_worker_audit_logs'; items: unknown[] }
  | { type: 'background_worker_operation_logs'; items: unknown[] }
  | { type: 'background_worker_record_maintenance'; items: unknown[] }
  | { type: 'background_worker_runtime_log_line'; line: unknown }
  | { type: 'background_worker_status_request'; requestId: unknown }

getBusinessDatabase()
getRecordDatabase()
installProcessLogHandlers()
startProcessEventLoopMonitor()
startLogMaintenance()
installUsageRecordQueueShutdownHooks()
installOperationLogQueueShutdownHooks()
installRecordMaintenanceQueueShutdownHooks()
installRuntimeLogIndexQueueShutdownHooks()
process.once('beforeExit', flushAllAuditLogQueue)
process.once('exit', flushAllAuditLogQueue)
setRuntimeLogLineSink((line) => enqueueRuntimeLogLineLocal(line))
startRuntimeLogFileImport()
startBackgroundJobs()

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
    case 'background_worker_runtime_log_line':
      if (typeof message.line === 'string') {
        enqueueRuntimeLogLineLocal(message.line)
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
  databasePath: runtimeConfig.databasePath
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
      successRetentionDays: auditRuntime.successRetentionDays,
      failureRetentionDays: auditRuntime.failureRetentionDays,
      errorGroupRetentionDays: auditRuntime.errorGroupRetentionDays
    }),
    runtimeLogIndexQueue: runtimeLogQueueRuntime(runtimeLogRuntime)
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
  process.send(message)
}

function isWorkerIncomingMessage(message: unknown): message is WorkerIncomingMessage {
  return typeof message === 'object'
    && message !== null
    && !Array.isArray(message)
    && typeof (message as Record<string, unknown>).type === 'string'
}
