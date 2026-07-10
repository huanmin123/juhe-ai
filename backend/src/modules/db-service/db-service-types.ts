import type { AccountSummary, AccountTestResult, GatewayRequestEndpointFamily } from '../../domain/types.js'
import type { AccountTestTaskRecord } from '../../storage/account-test-tasks.repository.js'
import type { AuditLogInput, GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret, OpenAIAccountsForGroupDiagnostics, OpenAIAccountsForGroupResult, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { RuntimeLogDetail, RuntimeLogFacets, RuntimeLogListOptions, RuntimeLogListResult } from '../../storage/runtime-logs.repository.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
import type { ActiveClientIpPolicy, ClientIpPolicyHitInput } from '../../storage/client-ip-stats.repository.js'
import type { ResponseInspectionPolicySummary } from '../../storage/response-inspection-policy.repository.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type {
  BackgroundDatasetWriteOperation,
  BackgroundDatasetWriteOperationResult
} from '../background/background-dataset-writer.js'
import type { ApiKeyQuotaDecision } from '../gateway/quota/api-key-quota.service.js'
import type { AccountErrorHandlingResult, GatewaySettings } from '../gateway/policy/account-error-policy.service.js'
import type { AuthorizationQuotaDecision } from '../gateway/quota/authorization-quota.service.js'
import type { OpenAIGatewayTrafficSource } from '../gateway/usage/traffic-source.js'
import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { ProviderModelCatalogItem } from '../model-pricing/model-catalog.service.js'
import type { AccountApiKeyRuntimeStatus } from '../../storage/account-api-key-rotation.js'
import type {
  CodexContextExpiredStateCleanupResult,
  CodexContextCompactReadResult,
  CodexContextCompactStateIndex,
  CodexContextCompactStateIndexInput,
  CodexContextResponseChainReadResult,
  CodexContextResponseStateIndex,
  CodexContextResponseStateIndexInput,
  CodexContextStateBoundary
} from '../../storage/codex-context-state.repository.js'
import type { CodexContextStateWriterPoolRuntime } from '../../storage/codex-context-state-writer-pool.js'
import type { SqliteReadWorkerPoolRuntime } from '../../storage/sqlite-read-worker-pool.js'
import type {
  OpenAICompatibleFileCreateInput,
  OpenAICompatibleFileListOptions,
  OpenAICompatibleFileListResult,
  OpenAICompatibleFileRecord
} from '../../storage/openai-compatible-files.repository.js'
import type {
  OpenAICompatibleVectorStoreCreateInput,
  OpenAICompatibleVectorStoreFileCreateInput,
  OpenAICompatibleVectorStoreFileChunkRecord,
  OpenAICompatibleVectorStoreFileListOptions,
  OpenAICompatibleVectorStoreFileListResult,
  OpenAICompatibleVectorStoreFileRecord,
  OpenAICompatibleVectorStoreListOptions,
  OpenAICompatibleVectorStoreListResult,
  OpenAICompatibleVectorStoreRecord,
  OpenAICompatibleVectorStoreSearchOptions,
  OpenAICompatibleVectorStoreSearchResult
} from '../../storage/openai-compatible-vector-stores.repository.js'

export type DbServiceRequestPriority = 'high' | 'normal' | 'low'

export type AccountRuntimeAvailabilityStatus = 'normal' | 'degraded' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  until?: string
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
  localFailureCount?: number
}

export interface AccountRuntimeAvailabilityClearTarget {
  accountId: string
  authorizedBinding?: {
    systemAccountId?: string
    groupId?: string
    accountAuthorizationId?: string
  }
}

export interface AccountRuntimeAvailabilityClearResult {
  cleared: boolean
  clearedKeys: string[]
}

export interface OpenAIAccountTrafficMigrationRuntimeScope {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface OpenAIAccountTrafficMigrationRuntimeRequest {
  sourceAccountId: string
  targetAccountId: string
  affinityScope?: Partial<OpenAIAccountTrafficMigrationRuntimeScope>
  preferenceScope?: Partial<OpenAIAccountTrafficMigrationRuntimeScope>
  preferMigratedSessions?: boolean
}

export interface OpenAIAccountTrafficMigrationRuntimeResult {
  migratedSessionCount: number
}

export interface DbServiceRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'db-service'
  httpHost?: string
  httpPort?: number
  eventLoopLagMs?: number
  pendingRequestCount: number
  queuedRequestCount?: number
  queuedRequestBytes?: number
  queuedHighRequestCount?: number
  queuedNormalRequestCount?: number
  queuedLowRequestCount?: number
  oldestQueuedMs?: number
  lastQueueWaitMs?: number
  maxQueueWaitMs?: number
  queueRejectedCount?: number
  queueExpiredCount?: number
  activeConcurrentRequestCount?: number
  maxActiveConcurrentRequestCount?: number
  lastExecMs?: number
  maxExecMs?: number
  slowOpCount?: number
  lastSlowOpType?: string
  lastSlowOpMs?: number
  lastSlowOpAt?: string
  handledRequestCount: number
  failedRequestCount: number
  lastRequestAt?: string
  lastError?: string
  codexContextStateWriterPool?: CodexContextStateWriterPoolRuntime
  sqliteReadWorkerPool?: SqliteReadWorkerPoolRuntime
}

export interface DbServiceServerRuntimeSnapshot {
  accountConcurrency?: Record<string, number>
  accountRuntimeAvailability?: Record<string, AccountRuntimeAvailability>
  ingestWorker?: {
    pid?: number
    ready: boolean
    pendingMessageCount?: number
    pendingMessageBytes?: number
    pendingQueues?: Record<string, DbServiceRuntimeQueueSnapshot>
    pendingWriteRequestCount?: number
    oldestPendingWriteMs?: number
    pendingSnapshotRequestCount?: number
    timedOutSnapshotRequestCount?: number
    rejectedSnapshotRequestCount?: number
    snapshot?: {
      pid: number
      ready: boolean
      workerRole?: string
      jobs?: Array<{
        name: string
        intervalMs: number
        running: boolean
        lastStartedAt?: string
        lastFinishedAt?: string
        lastSuccessAt?: string
        lastErrorAt?: string
        lastError?: string
        lastDurationMs?: number
        maxDurationMs?: number
        runCount: number
        successCount: number
        failureCount: number
        skippedCount: number
      }>
      usageRecordQueue: DbServiceRuntimeQueueSnapshot
      operationLogQueue: DbServiceRuntimeQueueSnapshot
      publicApiLogQueue: DbServiceRuntimeQueueSnapshot
      auditLogQueue: DbServiceRuntimeQueueSnapshot
      recordMaintenanceQueue: DbServiceRuntimeQueueSnapshot
      runtimeLogIndexQueue: DbServiceRuntimeQueueSnapshot & { retentionDays?: number }
    }
  }
  statsWorker?: {
    pid?: number
    ready: boolean
    pendingWriteRequestCount?: number
    oldestPendingWriteMs?: number
    pendingSnapshotRequestCount?: number
    timedOutSnapshotRequestCount?: number
    rejectedSnapshotRequestCount?: number
    snapshot?: {
      pid: number
      ready: boolean
      workerRole?: string
      jobs?: Array<{
        name: string
        intervalMs: number
        running: boolean
        lastStartedAt?: string
        lastFinishedAt?: string
        lastSuccessAt?: string
        lastErrorAt?: string
        lastError?: string
        lastDurationMs?: number
        maxDurationMs?: number
        runCount: number
        successCount: number
        failureCount: number
        skippedCount: number
      }>
      recordMaintenanceQueue: DbServiceRuntimeQueueSnapshot
      accountQualityFailurePrecheckQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
    }
  }
  opsWorker?: {
    pid?: number
    ready: boolean
    pendingMessageCount?: number
    pendingMessageBytes?: number
    pendingQueues?: Record<string, DbServiceRuntimeQueueSnapshot>
    pendingSnapshotRequestCount?: number
    timedOutSnapshotRequestCount?: number
    rejectedSnapshotRequestCount?: number
    snapshot?: {
      pid: number
      ready: boolean
      workerRole?: string
      jobs?: Array<{
        name: string
        intervalMs: number
        running: boolean
        lastStartedAt?: string
        lastFinishedAt?: string
        lastSuccessAt?: string
        lastErrorAt?: string
        lastError?: string
        lastDurationMs?: number
        maxDurationMs?: number
        runCount: number
        successCount: number
        failureCount: number
        skippedCount: number
      }>
      accountHealthCheckQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
      cooldownAccountRetestQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
      accountApiKeyCooldownRetestQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
      accountQualityFailurePrecheckQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
      manualAccountTestQueue?: {
        name: string
        pendingCount: number
        runningCount: number
        nextRunAt?: string
      }
    }
  }
  dbService?: {
    pid?: number
    ready: boolean
    pendingRequestCount: number
    pendingDatasetWriteRequestCount?: number
    oldestDatasetWriteRequestMs?: number
    timedOutDatasetWriteRequestCount?: number
    rejectedDatasetWriteRequestCount?: number
    timedOutRequestCount: number
    rejectedRequestCount?: number
    failedRequestCount: number
    queuedRequestCount?: number
    queuedRequestBytes?: number
    queuedHighRequestCount?: number
    queuedNormalRequestCount?: number
    queuedLowRequestCount?: number
    oldestQueuedMs?: number
    lastQueueWaitMs?: number
    maxQueueWaitMs?: number
    queueRejectedCount?: number
    queueExpiredCount?: number
    activeConcurrentRequestCount?: number
    maxActiveConcurrentRequestCount?: number
    lastExecMs?: number
    maxExecMs?: number
    slowOpCount?: number
    lastSlowOpType?: string
    lastSlowOpMs?: number
    lastSlowOpAt?: string
    pendingProcessEventLoopRequestCount?: number
    timedOutProcessEventLoopRequestCount?: number
    failedProcessEventLoopRequestCount?: number
    processEventLoopTimeoutStreak?: number
    pendingServerRuntimeRequestCount?: number
    timedOutServerRuntimeRequestCount?: number
    failedServerRuntimeRequestCount?: number
    unavailableCircuitOpenUntil?: string
    httpHost?: string
    httpPort?: number
    codexContextStateWriterPool?: CodexContextStateWriterPoolRuntime
    sqliteReadWorkerPool?: SqliteReadWorkerPoolRuntime
  }
  highConcurrencyQueues?: Array<{
    groupKey: string
    lane: string
    queueSize: number
    perApiKeyQueueSize: Record<string, number>
  }>
  gatewayAccountSideEffects?: Record<string, unknown>
  activeAuditCaptureCount?: number
}

export type DbServiceServerRuntimeSnapshotScope = 'full' | 'account_concurrency' | 'account_runtime'

export interface DbServiceRuntimeQueueSnapshot {
  queueLength?: number
  queueBytes?: number
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
  [key: string]: unknown
}

export interface DbServiceGatewayRuntime {
  apiKey?: GatewayApiKeyRow
  settings: GatewaySettings
  groupAccess?: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  accountDispatchDiagnostics?: OpenAIAccountsForGroupDiagnostics
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
}

export type DbServiceOpenAIOAuthRefreshAccount = Pick<AccountSummary, 'id' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type' | 'credentials' | 'status' | 'name' | 'proxyProfileId' | 'lastErrorCode'> & {
  proxyUrl?: string
}

export type DbServiceOperation =
  | {
    type: 'list_public_global_settings'
  }
  | {
    type: 'validate_gateway_api_key'
    key: string
  }
  | {
    type: 'read_gateway_settings'
  }
  | {
    type: 'resolve_group_usage_access'
    groupId: string
    systemAccountId: string
  }
  | {
    type: 'list_openai_accounts_for_group'
    groupId: string
    systemAccountId: string
    requestedModel?: string
    requestedEndpointFamily?: GatewayRequestEndpointFamily
  }
  | {
    type: 'list_openai_accounts_for_group_result'
    groupId: string
    systemAccountId: string
    requestedModel?: string
    requestedEndpointFamily?: GatewayRequestEndpointFamily
  }
  | {
    type: 'find_openai_account_for_group'
    groupId: string
    accountId: string
    systemAccountId: string
    includeUnavailable?: boolean
    ignoreAvailability?: boolean
  }
  | {
    type: 'list_recoverable_unavailable_openai_accounts_for_group'
    groupId: string
    systemAccountId: string
    requestedModel?: string
    requestedEndpointFamily?: GatewayRequestEndpointFamily
    windowMs?: number
  }
  | {
    type: 'read_gateway_runtime'
    key: string
    groupId?: string
    systemAccountId?: string
    skipDynamicRouteSelection?: boolean
  }
  | {
    type: 'create_openai_compatible_file'
    input: OpenAICompatibleFileCreateInput
  }
  | {
    type: 'list_openai_compatible_files'
    options: OpenAICompatibleFileListOptions
  }
  | {
    type: 'get_openai_compatible_file'
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'delete_openai_compatible_file'
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'create_openai_compatible_vector_store'
    input: OpenAICompatibleVectorStoreCreateInput
  }
  | {
    type: 'list_openai_compatible_vector_stores'
    options: OpenAICompatibleVectorStoreListOptions
  }
  | {
    type: 'get_openai_compatible_vector_store'
    vectorStoreId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'delete_openai_compatible_vector_store'
    vectorStoreId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'create_openai_compatible_vector_store_file'
    input: OpenAICompatibleVectorStoreFileCreateInput
  }
  | {
    type: 'list_openai_compatible_vector_store_files'
    options: OpenAICompatibleVectorStoreFileListOptions
  }
  | {
    type: 'get_openai_compatible_vector_store_file'
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'delete_openai_compatible_vector_store_file'
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'search_openai_compatible_vector_store'
    options: OpenAICompatibleVectorStoreSearchOptions
  }
  | {
    type: 'list_openai_compatible_vector_store_file_chunks'
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
    limit?: number
  }
  | {
    type: 'list_provider_model_catalog'
    providerCode: string
    systemAccountId?: string
    includeInactive?: boolean
    includeUnpriced?: boolean
  }
  | {
    type: 'check_api_key_quota'
    apiKey: GatewayApiKeyRow
  }
  | {
    type: 'check_authorization_quota'
    groupAuthorizationId?: string
    accountAuthorizationId?: string
  }
  | {
    type: 'check_authorization_quota_batch'
    groupAuthorizationId?: string
    accounts: Array<{
      accountId: string
      accountAuthorizationId?: string
    }>
  }
  | {
    type: 'update_openai_oauth_credentials'
    accountId: string
    credentials: Record<string, unknown>
  }
  | {
    type: 'find_openai_oauth_account_for_refresh'
    accountId: string
  }
  | {
    type: 'persist_openai_codex_usage_headers'
    accountId: string
    headers: Record<string, string>
    source: string
  }
  | {
    type: 'apply_account_error_handling'
    account: OpenAIAccountSecret
    input: {
      success: boolean
      statusCode?: number
      headers?: Record<string, string | string[]>
      bodyText?: string
      errorMessage?: string
      settings?: GatewaySettings
      trafficSource?: OpenAIGatewayTrafficSource
    }
  }
  | {
    type: 'record_account_api_key_failure'
    account: OpenAIAccountSecret
      input: {
        status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
        statusCode?: number
        errorCode?: string
        errorMessage?: string
        cooldownUntil?: string
        observedAt?: string
      }
    }
    | {
      type: 'record_account_api_key_success'
      account: OpenAIAccountSecret
      observedAt?: string
    }
  | {
    type: 'record_account_stream_failure'
    input: {
      accountId: string
      account?: OpenAIAccountSecret
      thresholdCount: number
      thresholdWindowMinutes: number
      action: 'cooldown' | 'disable' | 'none'
      reason: string
    }
  }
  | {
    type: 'clear_account_stream_failure_state'
    accountId?: string
    account?: OpenAIAccountSecret
  }
  | {
    type: 'mark_account_precheck_temporary_unavailable'
    account: OpenAIAccountSecret
    reason: string
    precheckStartedAt?: string
  }
  | {
    type: 'mark_account_temporary_unavailable'
    account: OpenAIAccountSecret
    reason: string
  }
  | {
    type: 'clear_account_failure_state'
    accountId: string
    allowPendingTestRestore?: boolean
    allowErrorRestore?: boolean
    authorizedBinding?: {
      systemAccountId: string
      groupId: string
      accountAuthorizationId: string
    }
  }
  | {
    type: 'mark_account_test_temporary_unavailable'
    accountId: string
    reason: string
    healthCheckGuard?: {
      configRevision: number
      checkedAt: string
      failureCount: number
      observedAt: string
    }
    access?: {
      systemAccountId: string
      role: 'super_admin' | 'admin' | 'user'
      systemAccountFilterId?: string
    }
  }
  | {
    type: 'find_account_for_test'
    accountId: string
    access?: {
      systemAccountId: string
      role: 'super_admin' | 'admin' | 'user'
      systemAccountFilterId?: string
    }
  }
  | {
    type: 'list_accounts_due_for_health_check'
    input: {
      limit: number
      intervalHours: number
      jitterMinutes: number
      failureThreshold: number
    }
  }
  | {
    type: 'find_account_for_health_check'
    accountId: string
  }
  | {
    type: 'record_account_health_check_success'
    accountId: string
    input: {
      intervalHours: number
      jitterMinutes: number
      failureThreshold: number
      statusCode?: number
      expectedConfigRevision?: number
    }
  }
  | {
    type: 'record_account_health_check_failure'
    accountId: string
    input: {
      intervalHours: number
      jitterMinutes: number
      failureThreshold: number
      statusCode?: number
      errorCode?: string
      errorMessage?: string
      countTowardsThreshold?: boolean
      expectedConfigRevision?: number
      observedAt?: string
    }
  }
  | {
    type: 'list_accounts_due_for_cooldown_retest'
    limit: number
  }
  | {
    type: 'find_account_for_cooldown_retest'
    accountId: string
  }
  | {
    type: 'record_cooldown_account_retest_failure'
    accountId: string
    input: {
      traceId?: string
      statusCode?: number
      errorCode?: string
      errorMessage?: string
      maxPauseMinutes?: number
      maxRecoveryHours?: number
      longTermIntervalHours?: number
    }
  }
  | {
    type: 'mark_account_exception'
    accountId: string
    errorCode: string
    reason: string
    preserveDisabled?: boolean
  }
  | {
    type: 'update_proxy_test_state'
    proxyId: string
    input: {
      testStatus: string
      latencyMs?: number | null
      outboundIp?: string | null
      outboundRegion?: string | null
      lastTestMessage?: string | null
      lastTestedAt?: string
    }
  }
  | {
    type: 'mark_all_group_account_stats_dirty'
    reason: string
  }
  | {
    type: 'delete_group_account_stats_dirty_rows'
    rows: Array<{
      groupId: string
      updatedAt: string
    }>
  }
  | {
    type: 'update_group_account_stats_all_cursor'
    cursorGroupId: string
  }
  | {
    type: 'sync_api_key_availability_schedule_statuses'
  }
  | {
    type: 'sync_account_availability_schedule_statuses'
  }
  | {
    type: 'expire_due_resource_authorizations'
  }
  | {
    type: 'cleanup_expired_deleted_accounts'
  }
  | {
    type: 'cleanup_expired_system_sessions'
    expiredBefore: string
    limit: number
  }
  | {
    type: 'save_codex_context_response_state'
    input: CodexContextResponseStateIndexInput
  }
  | {
    type: 'save_codex_context_compact_state'
    input: CodexContextCompactStateIndexInput
  }
  | {
    type: 'read_codex_context_response_chain'
    responseId: string
    boundary: CodexContextStateBoundary
    maxDepth?: number
    now?: string
    refreshExpiresAt?: string
  }
  | {
    type: 'read_codex_context_compact_state'
    compactId: string
    boundary: CodexContextStateBoundary
    now?: string
    refreshExpiresAt?: string
  }
  | {
    type: 'cleanup_expired_codex_context_states'
    expiredBefore: string
    limit: number
  }
  | {
    type: 'account_test_task_maintenance'
    action: 'start' | 'sweep'
    maxQueuedMs?: number
    sweepLimit?: number
    refillLimit?: number
  }
  | {
    type: 'mark_account_test_task_running'
    taskId: string
  }
  | {
    type: 'mark_account_test_task_canceled'
    taskId: string
    message: string
  }
  | {
    type: 'complete_account_test_task'
    taskId: string
    result: AccountTestResult
  }
  | {
    type: 'fail_account_test_task'
    taskId: string
    message: string
    result?: AccountTestResult
  }
  | {
    type: 'update_account_test_task_message'
    taskId: string
    message: string
  }
  | {
    type: 'is_account_test_task_cancel_requested'
    taskId: string
  }
  | {
    type: 'read_account_test_task_cancel_message'
    taskId: string
  }
  | {
    type: 'clear_gateway_runtime_cache'
  }
  | {
    type: 'list_active_client_ip_policies'
  }
  | {
    type: 'list_active_response_inspection_policies'
    protocolCode: string
    providerCode?: string
  }
  | {
    type: 'record_client_ip_policy_hits'
    hits: ClientIpPolicyHitInput[]
  }
  | {
    type: 'list_runtime_logs'
    options: RuntimeLogListOptions
  }
  | {
    type: 'get_runtime_log_detail'
    id: string
  }
  | {
    type: 'get_runtime_log_facets'
  }
  | {
    type: 'status'
  }

export type DbServiceOperationResult<T extends DbServiceOperation = DbServiceOperation> =
  T extends { type: 'list_public_global_settings' } ? Record<string, unknown> :
  T extends { type: 'validate_gateway_api_key' } ? GatewayApiKeyRow | undefined :
  T extends { type: 'read_gateway_settings' } ? GatewaySettings :
  T extends { type: 'resolve_group_usage_access' } ? GroupUsageAccessMetadata | undefined :
  T extends { type: 'list_openai_accounts_for_group' } ? OpenAIAccountSecret[] :
  T extends { type: 'list_openai_accounts_for_group_result' } ? OpenAIAccountsForGroupResult :
  T extends { type: 'find_openai_account_for_group' } ? OpenAIAccountSecret | undefined :
  T extends { type: 'list_recoverable_unavailable_openai_accounts_for_group' } ? OpenAIAccountSecret[] :
  T extends { type: 'read_gateway_runtime' } ? DbServiceGatewayRuntime :
  T extends { type: 'create_openai_compatible_file' } ? OpenAICompatibleFileRecord :
  T extends { type: 'list_openai_compatible_files' } ? OpenAICompatibleFileListResult :
  T extends { type: 'get_openai_compatible_file' } ? OpenAICompatibleFileRecord | undefined :
  T extends { type: 'delete_openai_compatible_file' } ? OpenAICompatibleFileRecord | undefined :
  T extends { type: 'create_openai_compatible_vector_store' } ? OpenAICompatibleVectorStoreRecord :
  T extends { type: 'list_openai_compatible_vector_stores' } ? OpenAICompatibleVectorStoreListResult :
  T extends { type: 'get_openai_compatible_vector_store' } ? OpenAICompatibleVectorStoreRecord | undefined :
  T extends { type: 'delete_openai_compatible_vector_store' } ? OpenAICompatibleVectorStoreRecord | undefined :
  T extends { type: 'create_openai_compatible_vector_store_file' } ? OpenAICompatibleVectorStoreFileRecord | undefined :
  T extends { type: 'list_openai_compatible_vector_store_files' } ? OpenAICompatibleVectorStoreFileListResult :
  T extends { type: 'get_openai_compatible_vector_store_file' } ? OpenAICompatibleVectorStoreFileRecord | undefined :
  T extends { type: 'delete_openai_compatible_vector_store_file' } ? OpenAICompatibleVectorStoreFileRecord | undefined :
  T extends { type: 'search_openai_compatible_vector_store' } ? OpenAICompatibleVectorStoreSearchResult[] :
  T extends { type: 'list_openai_compatible_vector_store_file_chunks' } ? OpenAICompatibleVectorStoreFileChunkRecord[] :
  T extends { type: 'list_provider_model_catalog' } ? ProviderModelCatalogItem[] :
  T extends { type: 'check_api_key_quota' } ? ApiKeyQuotaDecision :
  T extends { type: 'check_authorization_quota' } ? AuthorizationQuotaDecision :
  T extends { type: 'check_authorization_quota_batch' } ? AuthorizationQuotaDecision[] :
  T extends { type: 'update_openai_oauth_credentials' } ? { updated: boolean } :
  T extends { type: 'find_openai_oauth_account_for_refresh' } ? DbServiceOpenAIOAuthRefreshAccount | undefined :
  T extends { type: 'persist_openai_codex_usage_headers' } ? { persisted: boolean } :
  T extends { type: 'apply_account_error_handling' } ? AccountErrorHandlingResult :
  T extends { type: 'record_account_api_key_failure' } ? { changed: boolean; skippedReason?: string } :
  T extends { type: 'record_account_api_key_success' } ? { changed: boolean; skippedReason?: string } :
  T extends { type: 'record_account_stream_failure' } ? { count: number; triggered: boolean } :
  T extends { type: 'mark_account_precheck_temporary_unavailable' } ? { updated: boolean; skippedReason?: string } :
  T extends { type: 'mark_account_temporary_unavailable' } ? { updated: boolean } :
  T extends { type: 'clear_account_failure_state' } ? { changed: boolean; accountStatus?: string } :
  T extends { type: 'mark_account_test_temporary_unavailable' } ? { updated: boolean; accountStatus?: string } :
  T extends { type: 'find_account_for_test' } ? AccountSummary | undefined :
  T extends { type: 'list_accounts_due_for_health_check' } ? AccountSummary[] :
  T extends { type: 'find_account_for_health_check' } ? AccountSummary | undefined :
  T extends { type: 'record_account_health_check_success' } ? { changed: boolean } :
  T extends { type: 'record_account_health_check_failure' } ? { changed: boolean; failureCount: number; reachedThreshold: boolean; checkedAt: string; nextHealthCheckAt: string; errorCode: string; errorMessage: string } :
  T extends { type: 'list_accounts_due_for_cooldown_retest' } ? AccountSummary[] :
  T extends { type: 'find_account_for_cooldown_retest' } ? AccountSummary | undefined :
  T extends { type: 'record_cooldown_account_retest_failure' } ? { changed: boolean; failureCount: number; action: string; cooldownUntil?: string; backoffSeconds?: number; backoffMinutes?: number; recoveryStage?: string; fastThresholdSeconds?: number; maxPauseSeconds?: number; maxRecoverySeconds?: number; longTermIntervalSeconds?: number; maxedFailureCount?: number; observationStartedAt?: string; observationElapsedSeconds?: number; errorCode: string; errorMessage: string } :
  T extends { type: 'mark_account_exception' } ? { updated: boolean; accountStatus?: string } :
  T extends { type: 'update_proxy_test_state' } ? { updated: boolean; proxyStatus?: string } :
  T extends { type: 'mark_all_group_account_stats_dirty' } ? { marked: true } :
  T extends { type: 'delete_group_account_stats_dirty_rows' } ? { deleted: true } :
  T extends { type: 'update_group_account_stats_all_cursor' } ? { updated: true } :
  T extends { type: 'sync_api_key_availability_schedule_statuses' } ? import('../../storage/repositories.js').ApiKeyScheduleStatusSyncResult :
  T extends { type: 'sync_account_availability_schedule_statuses' } ? import('../../storage/repositories.js').AccountAvailabilityScheduleStatusSyncResult :
  T extends { type: 'expire_due_resource_authorizations' } ? { expired: number } :
  T extends { type: 'cleanup_expired_deleted_accounts' } ? import('../../storage/repositories.js').ExpiredDeletedAccountCleanupResult :
  T extends { type: 'cleanup_expired_system_sessions' } ? { deleted: number } :
  T extends { type: 'save_codex_context_response_state' } ? CodexContextResponseStateIndex :
  T extends { type: 'save_codex_context_compact_state' } ? CodexContextCompactStateIndex :
  T extends { type: 'read_codex_context_response_chain' } ? CodexContextResponseChainReadResult :
  T extends { type: 'read_codex_context_compact_state' } ? CodexContextCompactReadResult :
  T extends { type: 'cleanup_expired_codex_context_states' } ? CodexContextExpiredStateCleanupResult :
  T extends { type: 'account_test_task_maintenance' } ? { taskIds: string[]; canceledTaskIds: string[]; expiredQueuedTaskIds: string[] } :
  T extends { type: 'mark_account_test_task_running' } ? AccountTestTaskRecord | undefined :
  T extends { type: 'mark_account_test_task_canceled' } ? AccountTestTaskRecord | undefined :
  T extends { type: 'complete_account_test_task' } ? AccountTestTaskRecord | undefined :
  T extends { type: 'fail_account_test_task' } ? AccountTestTaskRecord | undefined :
  T extends { type: 'update_account_test_task_message' } ? AccountTestTaskRecord | undefined :
  T extends { type: 'is_account_test_task_cancel_requested' } ? { canceled: boolean } :
  T extends { type: 'read_account_test_task_cancel_message' } ? { message: string } :
  T extends { type: 'clear_account_stream_failure_state' } ? { changed: boolean } :
  T extends { type: 'clear_gateway_runtime_cache' } ? { cleared: true } :
  T extends { type: 'list_active_client_ip_policies' } ? ActiveClientIpPolicy[] :
  T extends { type: 'list_active_response_inspection_policies' } ? ResponseInspectionPolicySummary[] :
  T extends { type: 'record_client_ip_policy_hits' } ? { recorded: number } :
  T extends { type: 'list_runtime_logs' } ? RuntimeLogListResult :
  T extends { type: 'get_runtime_log_detail' } ? RuntimeLogDetail | undefined :
  T extends { type: 'get_runtime_log_facets' } ? RuntimeLogFacets :
  T extends { type: 'status' } ? DbServiceRuntimeSnapshot :
  unknown

export type DbServiceParentMessage =
  | {
    type: 'db_service_request'
    requestId: string
    operation: DbServiceOperation
    priority?: DbServiceRequestPriority
    deadlineAtMs?: number
  }
  | {
    type: 'db_service_server_runtime_response'
    requestId: string
    ok: true
    result: DbServiceServerRuntimeSnapshot
  }
  | {
    type: 'db_service_server_runtime_response'
    requestId: string
    ok: false
    errorMessage: string
  }
  | {
    type: 'db_service_process_event_loop_request'
    requestId: string
  }
  | {
    type: 'db_service_server_account_runtime_clear_response'
    requestId: string
    ok: true
    result: AccountRuntimeAvailabilityClearResult
  }
  | {
    type: 'db_service_server_account_runtime_clear_response'
    requestId: string
    ok: false
    errorMessage: string
  }
  | {
    type: 'db_service_openai_traffic_migration_runtime_response'
    requestId: string
    ok: true
    result: OpenAIAccountTrafficMigrationRuntimeResult
  }
  | {
    type: 'db_service_openai_traffic_migration_runtime_response'
    requestId: string
    ok: false
    errorMessage: string
  }
  | {
    type: 'background_worker_dataset_write_response'
    requestId: string
    ok: true
    result: BackgroundDatasetWriteOperationResult
  }
  | {
    type: 'background_worker_dataset_write_response'
    requestId: string
    ok: false
    errorMessage: string
  }

export type DbServiceChildMessage =
  | {
    type: 'db_service_ready'
    pid: number
    httpHost?: string
    httpPort?: number
  }
  | {
    type: 'db_service_response'
    requestId: string
    ok: true
    result: unknown
  }
  | {
    type: 'db_service_response'
    requestId: string
    ok: false
    errorMessage: string
  }
  | {
    type: 'db_service_server_runtime_request'
    requestId: string
    scope?: DbServiceServerRuntimeSnapshotScope
  }
  | {
    type: 'db_service_process_event_loop_response'
    requestId: string
    sample?: ProcessEventLoopSample
  }
  | {
    type: 'db_service_server_account_runtime_clear_request'
    requestId: string
    target: AccountRuntimeAvailabilityClearTarget
  }
  | {
    type: 'db_service_openai_traffic_migration_runtime_request'
    requestId: string
    input: OpenAIAccountTrafficMigrationRuntimeRequest
  }
  | {
    type: 'gateway_runtime_cache_invalidate'
  }
  | {
    type: 'authorization_quota_cache_invalidate'
  }
  | {
    type: 'client_ip_policy_cache_invalidate'
  }
  | {
    type: 'background_worker_usage_records'
    items: UsageRecordInput[]
  }
  | {
    type: 'background_worker_audit_logs'
    items: AuditLogInput[]
  }
  | {
    type: 'background_worker_operation_logs'
    items: OperationLogInput[]
  }
  | {
    type: 'background_worker_public_api_logs'
    items: PublicApiLogInput[]
  }
  | ({
    type: 'background_worker_runtime_log_line'
    line: string
  } & RuntimeLogLineIndexOptions)
  | {
    type: 'background_worker_record_maintenance'
    items: RecordMaintenanceJob[]
  }
  | {
    type: 'background_worker_account_test_tasks'
    taskIds: string[]
  }
  | {
    type: 'background_worker_account_test_cancel'
    taskId: string
  }
  | {
    type: 'background_worker_dataset_write_request'
    requestId: string
    operation: BackgroundDatasetWriteOperation
  }
