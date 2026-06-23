import type { BackgroundWorkerMessage, BackgroundWorkerProcessRole } from './background-ipc.types.js'

export type BackgroundWorkerQueueTargetRole = 'worker' | 'ingest-worker' | 'probe-worker' | 'maintenance-worker'
export type BackgroundWorkerSnapshotRole = Extract<BackgroundWorkerProcessRole, 'stats-worker' | 'snapshot-worker' | 'probe-worker' | 'maintenance-worker'>

export function workerMessageTargetRole(message: BackgroundWorkerMessage): BackgroundWorkerQueueTargetRole {
  switch (message.type) {
    case 'background_worker_usage_records':
    case 'background_worker_audit_logs':
    case 'background_worker_operation_logs':
    case 'background_worker_public_api_logs':
    case 'background_worker_runtime_log_line':
      return 'ingest-worker'
    case 'background_worker_account_test_tasks':
    case 'background_worker_account_test_cancel':
      return 'probe-worker'
    case 'background_worker_record_maintenance':
    case 'background_worker_dataset_write_request':
      return 'ingest-worker'
    default:
      return 'worker'
  }
}

export function isSnapshotRoleWorker(role: BackgroundWorkerProcessRole): role is BackgroundWorkerSnapshotRole {
  return role === 'stats-worker' || role === 'snapshot-worker' || role === 'probe-worker' || role === 'maintenance-worker'
}

export function processEventLoopWorkerRoles(): BackgroundWorkerProcessRole[] {
  return ['worker', 'metrics-worker', 'ingest-worker', 'stats-worker', 'snapshot-worker', 'probe-worker', 'maintenance-worker']
}
