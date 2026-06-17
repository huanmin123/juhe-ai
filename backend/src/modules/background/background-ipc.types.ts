import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { AccountRuntimeAvailabilityClearTarget } from '../db-service/db-service-types.js'
import type { GatewayQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
import type { WorkerScheduledJobRuntimeSnapshot } from './worker-scheduler.js'

export type BackgroundWorkerProcessRole =
  | 'worker'
  | 'metrics-worker'
  | 'ingest-worker'
  | 'stats-worker'
  | 'snapshot-worker'
  | 'probe-worker'
  | 'maintenance-worker'

export interface BackgroundWorkerQueueRuntime {
  queueLength: number
  queueBytes?: number
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedCount?: number
  droppedSuccessCount?: number
  droppedFailureCount?: number
  droppedOverflowCount?: number
  droppedOversizeCount?: number
  retainedOverflowWarningCount?: number
  flushFailureCount?: number
  successHotRetentionHours?: number
  successRetentionDays?: number
  failureRetentionDays?: number
  errorGroupRetentionDays?: number
}

export interface BackgroundWorkerRuntimeLogQueueRuntime extends BackgroundWorkerQueueRuntime {
  retentionDays: number
}

export interface BackgroundWorkerRetryQueueRuntime {
  name: string
  pendingCount: number
  runningCount: number
  nextRunAt?: string
}

export interface BackgroundWorkerIpcQueueRuntime extends BackgroundWorkerQueueRuntime {
  rejectedCount?: number
}

export interface BackgroundWorkerIpcQueuesRuntime {
  usageRecords: BackgroundWorkerIpcQueueRuntime
  auditLogs: BackgroundWorkerIpcQueueRuntime
  operationLogs: BackgroundWorkerIpcQueueRuntime
  publicApiLogs: BackgroundWorkerIpcQueueRuntime
  recordMaintenance: BackgroundWorkerIpcQueueRuntime
  runtimeLogLines: BackgroundWorkerIpcQueueRuntime
  statusRequests: BackgroundWorkerIpcQueueRuntime
  processEventLoopRequests: BackgroundWorkerIpcQueueRuntime
  processEventLoopResponses: BackgroundWorkerIpcQueueRuntime
  gatewayRuntimeCacheInvalidations: BackgroundWorkerIpcQueueRuntime
  other: BackgroundWorkerIpcQueueRuntime
}

export interface BackgroundWorkerRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'worker'
  workerRole: BackgroundWorkerProcessRole
  jobs: WorkerScheduledJobRuntimeSnapshot[]
  usageRecordQueue: BackgroundWorkerQueueRuntime
  operationLogQueue: BackgroundWorkerQueueRuntime
  publicApiLogQueue: BackgroundWorkerQueueRuntime
  recordMaintenanceQueue: BackgroundWorkerQueueRuntime
  auditLogQueue: BackgroundWorkerQueueRuntime
  runtimeLogIndexQueue: BackgroundWorkerRuntimeLogQueueRuntime
  accountHealthCheckQueue?: BackgroundWorkerRetryQueueRuntime
  cooldownAccountRetestQueue?: BackgroundWorkerRetryQueueRuntime
  accountApiKeyCooldownRetestQueue?: BackgroundWorkerRetryQueueRuntime
  accountQualityFailurePrecheckQueue?: BackgroundWorkerRetryQueueRuntime
  manualAccountTestQueue?: BackgroundWorkerRetryQueueRuntime
}

export interface BackgroundWorkerRoleState {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount?: number
  pendingMessageBytes?: number
  pendingQueues?: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
}

export interface BackgroundWorkerIngestDrainStatus {
  pid?: number
  ready: boolean
  snapshot?: BackgroundWorkerRuntimeSnapshot
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
}

export type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number; workerRole?: BackgroundWorkerProcessRole }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_operation_logs'; items: OperationLogInput[] }
  | { type: 'background_worker_public_api_logs'; items: PublicApiLogInput[] }
  | { type: 'background_worker_record_maintenance'; items: RecordMaintenanceJob[] }
  | { type: 'background_worker_account_test_tasks'; taskIds: string[] }
  | { type: 'background_worker_account_test_cancel'; taskId: string }
  | ({ type: 'background_worker_runtime_log_line'; line: string } & RuntimeLogLineIndexOptions)
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
  | { type: 'background_worker_ingest_status_request'; requestId: string }
  | { type: 'background_worker_ingest_status_response'; requestId: string; status?: BackgroundWorkerIngestDrainStatus }
  | { type: 'background_worker_process_event_loop_request'; requestId: string }
  | { type: 'background_worker_process_event_loop_response'; requestId: string; samples: ProcessEventLoopSample[] }
  | { type: 'server_account_runtime_clear'; target: AccountRuntimeAvailabilityClearTarget }
  | { type: 'gateway_runtime_cache_invalidate' }
  | { type: 'gateway_quota_snapshot_update'; snapshot: GatewayQuotaSnapshot }
  | { type: 'client_ip_policy_snapshot_update'; policies: ActiveClientIpPolicy[] }

export interface PendingRequest {
  resolve: (snapshot: BackgroundWorkerRuntimeSnapshot | undefined) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

export interface PendingIngestStatusRequest {
  resolve: (status: BackgroundWorkerIngestDrainStatus | undefined) => void
  timeout: NodeJS.Timeout
}

export interface PendingProcessEventLoopRequest {
  resolve: (samples: ProcessEventLoopSample[] | undefined) => void
  timeout: NodeJS.Timeout
}

export interface BackgroundWorkerState {
  pid?: number
  ready: boolean
  metricsWorker?: BackgroundWorkerRoleState
  ingestWorker?: BackgroundWorkerRoleState
  statsWorker?: BackgroundWorkerRoleState
  snapshotWorker?: BackgroundWorkerRoleState
  probeWorker?: BackgroundWorkerRoleState
  maintenanceWorker?: BackgroundWorkerRoleState
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount: number
  pendingMessageBytes: number
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
}
