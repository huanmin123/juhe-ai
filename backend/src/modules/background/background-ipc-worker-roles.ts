import type { BackgroundWorkerMessage, BackgroundWorkerProcessRole } from './background-ipc.types.js'

export type BackgroundWorkerQueueTargetRole = 'worker' | 'ingest-worker' | 'ops-worker'
export type BackgroundWorkerSnapshotRole = Extract<BackgroundWorkerProcessRole, 'stats-worker' | 'ops-worker'>

export function workerMessageTargetRole(message: BackgroundWorkerMessage): BackgroundWorkerQueueTargetRole {
  switch (message.type) {
    case 'background_worker_usage_records':
    case 'background_worker_audit_logs':
    case 'background_worker_operation_logs':
    case 'background_worker_public_api_logs':
    case 'background_worker_account_test_tasks':
    case 'background_worker_account_test_cancel':
    case 'background_worker_account_health_check_trigger':
      return 'ops-worker'
    case 'background_worker_record_maintenance':
    case 'background_worker_dataset_write_request':
      return 'ingest-worker'
    default:
      return 'worker'
  }
}

export function isSnapshotRoleWorker(role: BackgroundWorkerProcessRole): role is BackgroundWorkerSnapshotRole {
  return role === 'stats-worker' || role === 'ops-worker'
}

export function processEventLoopWorkerRoles(): BackgroundWorkerProcessRole[] {
  return ['ingest-worker', 'stats-worker', 'ops-worker']
}
