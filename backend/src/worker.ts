import { runtimeConfig } from './config/runtime.js'
import {
  type BackgroundWorkerRuntimeSnapshot,
  type BackgroundWorkerQueueRuntime
} from './modules/background/background-ipc.js'
import { startBackgroundJobs } from './modules/background/background-jobs.js'
import { enqueueAuditLogsLocal, flushAllAuditLogQueue, getAuditLogQueueRuntime } from './modules/audit-logs/audit-log-queue.service.js'
import { startRuntimeLogFileImport } from './modules/runtime-logs/runtime-log-file-import.service.js'
import {
  enqueueRuntimeLogLineLocal,
  flushAllRuntimeLogIndexQueue,
  getRuntimeLogIndexRuntime,
  installRuntimeLogIndexQueueShutdownHooks
} from './modules/runtime-logs/runtime-log-index-queue.service.js'
import {
  enqueueUsageRecordsLocal,
  flushAllUsageRecordQueue,
  getUsageRecordQueueRuntime,
  installUsageRecordQueueShutdownHooks
} from './modules/gateway/usage-record-queue.service.js'
import { getDatabase } from './storage/database.js'
import { installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'

type WorkerIncomingMessage =
  | { type: 'background_worker_usage_records'; items: unknown[] }
  | { type: 'background_worker_audit_logs'; items: unknown[] }
  | { type: 'background_worker_runtime_log_line'; line: unknown }
  | { type: 'background_worker_status_request'; requestId: unknown }

getDatabase()
installProcessLogHandlers()
startLogMaintenance()
installUsageRecordQueueShutdownHooks()
installRuntimeLogIndexQueueShutdownHooks()
process.once('beforeExit', flushAllAuditLogQueue)
process.once('exit', flushAllAuditLogQueue)
process.once('beforeExit', flushAllRuntimeLogIndexQueue)
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
}, 'Background worker started')

function buildRuntimeSnapshot(): BackgroundWorkerRuntimeSnapshot {
  const auditRuntime = getAuditLogQueueRuntime()
  const runtimeLogRuntime = getRuntimeLogIndexRuntime()
  return {
    pid: process.pid,
    ready: true,
    processRole: 'worker',
    usageRecordQueue: queueRuntime(getUsageRecordQueueRuntime()),
    auditLogQueue: queueRuntime({
      queueLength: auditRuntime.queueLength,
      queueBytes: auditRuntime.queueBytes,
      flushLastSuccessAt: auditRuntime.flushLastSuccessAt,
      flushLastError: auditRuntime.flushLastError,
      droppedCount: auditRuntime.droppedSuccessCount + auditRuntime.droppedFailureCount + auditRuntime.droppedOverflowCount + auditRuntime.droppedOversizeCount
    }),
    runtimeLogIndexQueue: queueRuntime(runtimeLogRuntime)
  }
}

function queueRuntime(input: BackgroundWorkerQueueRuntime): BackgroundWorkerQueueRuntime {
  return {
    queueLength: Number(input.queueLength ?? 0),
    queueBytes: typeof input.queueBytes === 'number' ? input.queueBytes : undefined,
    flushLastSuccessAt: typeof input.flushLastSuccessAt === 'string' ? input.flushLastSuccessAt : undefined,
    flushLastError: typeof input.flushLastError === 'string' ? input.flushLastError : undefined,
    droppedCount: typeof input.droppedCount === 'number' ? input.droppedCount : undefined,
    retentionDays: typeof input.retentionDays === 'number' ? input.retentionDays : undefined
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
