import type {
  BackgroundWorkerIpcQueueRuntime,
  BackgroundWorkerIpcQueuesRuntime,
  BackgroundWorkerMessage
} from './background-ipc.types.js'

export type IpcQueueKey = keyof BackgroundWorkerIpcQueuesRuntime

export function clonePendingQueueRuntime(input: BackgroundWorkerIpcQueuesRuntime): BackgroundWorkerIpcQueuesRuntime {
  return {
    usageRecords: { ...input.usageRecords },
    auditLogs: { ...input.auditLogs },
    operationLogs: { ...input.operationLogs },
    publicApiLogs: { ...input.publicApiLogs },
    recordMaintenance: { ...input.recordMaintenance },
    statusRequests: { ...input.statusRequests },
    processEventLoopRequests: { ...input.processEventLoopRequests },
    processEventLoopResponses: { ...input.processEventLoopResponses },
    gatewayRuntimeCacheInvalidations: { ...input.gatewayRuntimeCacheInvalidations },
    other: { ...input.other }
  }
}

export function ipcQueueKeyForMessage(message: BackgroundWorkerMessage): IpcQueueKey {
  switch (message.type) {
    case 'background_worker_usage_records':
      return 'usageRecords'
    case 'background_worker_audit_logs':
      return 'auditLogs'
    case 'background_worker_operation_logs':
      return 'operationLogs'
    case 'background_worker_public_api_logs':
      return 'publicApiLogs'
    case 'background_worker_record_maintenance':
      return 'recordMaintenance'
    case 'background_worker_account_test_tasks':
    case 'background_worker_account_test_cancel':
      return 'other'
    case 'background_worker_status_request':
      return 'statusRequests'
    case 'background_worker_process_event_loop_request':
      return 'processEventLoopRequests'
    case 'background_worker_process_event_loop_response':
      return 'processEventLoopResponses'
    case 'server_account_runtime_clear':
      return 'other'
    case 'gateway_runtime_cache_invalidate':
      return 'gatewayRuntimeCacheInvalidations'
    case 'gateway_quota_snapshot_update':
      return 'other'
    default:
      return 'other'
  }
}

export function emptyIpcQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  const emptyQueueRuntime = (): BackgroundWorkerIpcQueueRuntime => ({
    queueLength: 0,
    queueBytes: 0,
    droppedCount: 0,
    rejectedCount: 0
  })
  return {
    usageRecords: emptyQueueRuntime(),
    auditLogs: emptyQueueRuntime(),
    operationLogs: emptyQueueRuntime(),
    publicApiLogs: emptyQueueRuntime(),
    recordMaintenance: emptyQueueRuntime(),
    statusRequests: emptyQueueRuntime(),
    processEventLoopRequests: emptyQueueRuntime(),
    processEventLoopResponses: emptyQueueRuntime(),
    gatewayRuntimeCacheInvalidations: emptyQueueRuntime(),
    other: emptyQueueRuntime()
  }
}

export function ipcQueueKeys(): IpcQueueKey[] {
  return [
    'usageRecords',
    'auditLogs',
    'operationLogs',
    'publicApiLogs',
    'recordMaintenance',
    'statusRequests',
    'processEventLoopRequests',
    'processEventLoopResponses',
    'gatewayRuntimeCacheInvalidations',
    'other'
  ]
}

export function mergePendingQueuesRuntime(
  left: BackgroundWorkerIpcQueuesRuntime,
  right: BackgroundWorkerIpcQueuesRuntime
): BackgroundWorkerIpcQueuesRuntime {
  const output = emptyIpcQueuesRuntime()
  for (const key of ipcQueueKeys()) {
    output[key] = {
      queueLength: left[key].queueLength + right[key].queueLength,
      queueBytes: (left[key].queueBytes ?? 0) + (right[key].queueBytes ?? 0),
      oldestCreatedAt: oldestCreatedAt(left[key].oldestCreatedAt, right[key].oldestCreatedAt),
      droppedCount: (left[key].droppedCount ?? 0) + (right[key].droppedCount ?? 0),
      rejectedCount: (left[key].rejectedCount ?? 0) + (right[key].rejectedCount ?? 0)
    }
  }
  return output
}

function oldestCreatedAt(left?: string, right?: string): string | undefined {
  if (!left) return right
  if (!right) return left
  return left <= right ? left : right
}
