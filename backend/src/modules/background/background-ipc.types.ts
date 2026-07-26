import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { AccountRuntimeAvailabilityClearTarget, DbServiceOperation, DbServiceRequestPriority } from '../db-service/db-service-types.js'
import type {
  BackgroundDatasetWriteOperation,
  BackgroundDatasetWriteOperationResult
} from './background-dataset-writer.js'
import type {
  BackgroundStatsWriteOperation,
  BackgroundStatsWriteOperationResult
} from './background-stats-writer.js'
import type { GatewayQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { WorkerScheduledJobRuntimeSnapshot } from './worker-scheduler.js'
import type { AccountHealthCheckTriggerReason } from '../accounts/account-health-check-trigger.js'
import type { PageDataChangeEvent, PageDataDomain } from '../page-data/page-data-change.service.js'

export type BackgroundWorkerProcessRole =
  | 'worker'
  | 'ingest-worker'
  | 'usage-worker'
  | 'log-worker'
  | 'stats-worker'
  | 'ops-worker'

export interface BackgroundWorkerQueueRuntime {
  queueLength: number
  queueBytes?: number
  oldestCreatedAt?: string
  flushLastSuccessAt?: string
  flushLastError?: string
  completedCount?: number
  droppedCount?: number
  droppedSuccessCount?: number
  droppedFailureCount?: number
  droppedOverflowCount?: number
  droppedOversizeCount?: number
  retainedOverflowWarningCount?: number
  flushFailureCount?: number
  oldestQueuedMs?: number
  lastFlushMs?: number
  maxFlushMs?: number
  slowFlushCount?: number
  lastSlowFlushAt?: string
  writerPoolEnabled?: boolean
  writerPoolWorkerCount?: number
  writerPoolQueueLength?: number
  writerPoolActiveJobs?: number
  writerPoolHandledJobs?: number
  writerPoolFailedJobs?: number
  writerPoolRejectedJobs?: number
  writerPoolOldestQueuedMs?: number
  writerPoolMaxQueueWaitMs?: number
  writerPoolMaxRunMs?: number
  successHotRetentionHours?: number
  successRetentionDays?: number
  problemRetentionDays?: number
  successFullBodyLimitBytes?: number
  problemFullBodyLimitBytes?: number
}

export interface BackgroundWorkerRuntimeLogQueueRuntime extends BackgroundWorkerQueueRuntime {
  retentionDays: number
  discoveredFileCount?: number
  pendingFileCount?: number
  pendingBytes?: number
  oldestPendingMtime?: string
  currentFile?: string
  currentOffset?: number
  lastReadAt?: string
  lastCommitAt?: string
  lastError?: string
  protectedRotatedFileCount?: number
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
  normalRouteSpeedFirstRecoveryProbeQueue?: BackgroundWorkerRetryQueueRuntime
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
  pendingWriteRequestCount?: number
  oldestPendingWriteMs?: number
  rejectedWriteRequestCount?: number
  timedOutWriteRequestCount?: number
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

export interface BackgroundWorkerDbServiceRequestOptions {
  timeoutMs?: number
  priority?: DbServiceRequestPriority
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
  | { type: 'background_worker_account_health_check_trigger'; accountId: string; reason: AccountHealthCheckTriggerReason }
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
  | { type: 'background_worker_ingest_status_request'; requestId: string }
  | { type: 'background_worker_ingest_status_response'; requestId: string; status?: BackgroundWorkerIngestDrainStatus }
  | { type: 'background_worker_db_service_request'; requestId: string; operation: DbServiceOperation; options?: BackgroundWorkerDbServiceRequestOptions }
  | { type: 'background_worker_db_service_response'; requestId: string; ok: true; result: unknown }
  | { type: 'background_worker_db_service_response'; requestId: string; ok: false; errorMessage: string }
  | { type: 'background_worker_dataset_write_request'; requestId: string; operation: BackgroundDatasetWriteOperation }
  | { type: 'background_worker_dataset_write_response'; requestId: string; ok: true; result: BackgroundDatasetWriteOperationResult }
  | { type: 'background_worker_dataset_write_response'; requestId: string; ok: false; errorMessage: string }
  | { type: 'background_worker_stats_write_request'; requestId: string; operation: BackgroundStatsWriteOperation }
  | { type: 'background_worker_stats_write_response'; requestId: string; ok: true; result: BackgroundStatsWriteOperationResult }
  | { type: 'background_worker_stats_write_response'; requestId: string; ok: false; errorMessage: string }
  | { type: 'background_worker_process_event_loop_request'; requestId: string }
  | { type: 'background_worker_process_event_loop_response'; requestId: string; samples: ProcessEventLoopSample[] }
  | { type: 'server_account_runtime_clear'; target: AccountRuntimeAvailabilityClearTarget }
  | { type: 'gateway_runtime_cache_invalidate' }
  | { type: 'gateway_quota_snapshot_update'; snapshot: GatewayQuotaSnapshot }
  | { type: 'client_ip_policy_snapshot_update'; policies: ActiveClientIpPolicy[] }
  | { type: 'page_data_change_publish'; event: PageDataChangeEvent }
  | { type: 'page_data_change_dirty'; requestId: string; domains: PageDataDomain[] }
  | { type: 'page_data_change_dirty_ack'; requestId: string; ok: true }
  | { type: 'page_data_change_dirty_ack'; requestId: string; ok: false; errorMessage: string }

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
  ingestWorker?: BackgroundWorkerRoleState
  statsWorker?: BackgroundWorkerRoleState
  opsWorker?: BackgroundWorkerRoleState
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount: number
  pendingMessageBytes: number
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
  pendingDbServiceRequestCount?: number
  oldestPendingDbServiceRequestMs?: number
  rejectedDbServiceRequestCount?: number
  timedOutDbServiceRequestCount?: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
}
