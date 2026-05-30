import type { AccountSummary } from '../../domain/types.js'
import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret, OperationLogInput } from '../../storage/repositories.js'
import type { RuntimeLogDetail, RuntimeLogFacets, RuntimeLogListOptions, RuntimeLogListResult } from '../../storage/runtime-logs.repository.js'
import type { ActiveClientIpPolicy, ClientIpPolicyHitInput } from '../../storage/client-ip-stats.repository.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { ApiKeyQuotaDecision } from '../gateway/api-key-quota.service.js'
import type { AccountErrorHandlingResult, GatewaySettings } from '../gateway/account-error-policy.service.js'
import type { AuthorizationQuotaDecision } from '../gateway/authorization-quota.service.js'
import type { OpenAIGatewayTrafficSource } from '../gateway/openai-gateway-traffic-source.js'
import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { AuditFullBodyCaptureRuntimeConfig } from '../../config/runtime.js'

export type AccountRuntimeAvailabilityStatus = 'normal' | 'local_suppressed' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  until?: string
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
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

export interface DbServiceRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'db-service'
  httpHost?: string
  httpPort?: number
  eventLoopLagMs?: number
  pendingRequestCount: number
  handledRequestCount: number
  failedRequestCount: number
  lastRequestAt?: string
  lastError?: string
}

export interface DbServiceServerRuntimeSnapshot {
  accountConcurrency?: Record<string, number>
  accountRuntimeAvailability?: Record<string, AccountRuntimeAvailability>
  worker?: {
    pid?: number
    ready: boolean
    pendingMessageCount: number
    pendingMessageBytes?: number
    pendingQueues?: Record<string, DbServiceRuntimeQueueSnapshot>
    pendingSnapshotRequestCount?: number
    timedOutSnapshotRequestCount?: number
    rejectedSnapshotRequestCount?: number
    pendingProcessEventLoopRequestCount?: number
    timedOutProcessEventLoopRequestCount?: number
    failedProcessEventLoopRequestCount?: number
    snapshot?: {
      pid: number
      ready: boolean
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
      recordMaintenanceQueue: DbServiceRuntimeQueueSnapshot
      auditLogQueue: DbServiceRuntimeQueueSnapshot
      runtimeLogIndexQueue: DbServiceRuntimeQueueSnapshot & { retentionDays?: number }
      cooldownAccountRetestQueue?: {
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
    timedOutRequestCount: number
    rejectedRequestCount?: number
    failedRequestCount: number
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
  }
  gatewayAccountSideEffects?: Record<string, unknown>
  activeAuditCaptureCount?: number
  audit?: {
    fullBodyCaptureEnabled: boolean
    fullBodyCapture: AuditFullBodyCaptureRuntimeConfig
  }
}

export type DbServiceServerRuntimeSnapshotScope = 'full' | 'account_concurrency' | 'account_runtime'

export interface DbServiceRuntimeQueueSnapshot {
  queueLength?: number
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
  [key: string]: unknown
}

export interface DbServiceGatewayRuntime {
  apiKey?: GatewayApiKeyRow
  settings: GatewaySettings
  groupAccess?: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  clientIpPolicies?: ActiveClientIpPolicy[]
}

export type DbServiceOpenAIOAuthRefreshAccount = Pick<AccountSummary, 'id' | 'providerCode' | 'type' | 'credentials' | 'status' | 'name' | 'proxyProfileId' | 'lastErrorCode'> & {
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
  }
  | {
    type: 'read_gateway_runtime'
    key: string
    groupId?: string
    systemAccountId?: string
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
    type: 'record_account_stream_failure'
    input: {
      accountId: string
      account?: OpenAIAccountSecret
      thresholdCount: number
      thresholdWindowMinutes: number
      action: 'cooldown' | 'disable' | 'none'
      cooldownMinutes: number
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
    type: 'clear_gateway_runtime_cache'
  }
  | {
    type: 'list_active_client_ip_policies'
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
  T extends { type: 'read_gateway_runtime' } ? DbServiceGatewayRuntime :
  T extends { type: 'check_api_key_quota' } ? ApiKeyQuotaDecision :
  T extends { type: 'check_authorization_quota' } ? AuthorizationQuotaDecision :
  T extends { type: 'check_authorization_quota_batch' } ? AuthorizationQuotaDecision[] :
  T extends { type: 'update_openai_oauth_credentials' } ? { updated: boolean } :
  T extends { type: 'find_openai_oauth_account_for_refresh' } ? DbServiceOpenAIOAuthRefreshAccount | undefined :
  T extends { type: 'persist_openai_codex_usage_headers' } ? { persisted: boolean } :
  T extends { type: 'apply_account_error_handling' } ? AccountErrorHandlingResult :
  T extends { type: 'record_account_stream_failure' } ? { count: number; triggered: boolean } :
  T extends { type: 'mark_account_precheck_temporary_unavailable' } ? { updated: boolean; skippedReason?: string } :
  T extends { type: 'clear_account_stream_failure_state' } ? { changed: boolean } :
  T extends { type: 'clear_gateway_runtime_cache' } ? { cleared: true } :
  T extends { type: 'list_active_client_ip_policies' } ? ActiveClientIpPolicy[] :
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
    type: 'db_service_server_audit_full_body_capture_update_response'
    requestId: string
    ok: true
    result: {
      fullBodyCaptureEnabled: boolean
      fullBodyCapture: AuditFullBodyCaptureRuntimeConfig
    }
  }
  | {
    type: 'db_service_server_audit_full_body_capture_update_response'
    requestId: string
    ok: false
    errorMessage: string
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
    type: 'db_service_server_audit_full_body_capture_update_request'
    requestId: string
    config: AuditFullBodyCaptureRuntimeConfig
  }
  | {
    type: 'db_service_server_account_runtime_clear_request'
    requestId: string
    target: AccountRuntimeAvailabilityClearTarget
  }
  | {
    type: 'gateway_runtime_cache_invalidate'
  }
  | {
    type: 'client_ip_policy_cache_invalidate'
  }
  | {
    type: 'background_worker_operation_logs'
    items: OperationLogInput[]
  }
  | {
    type: 'background_worker_record_maintenance'
    items: RecordMaintenanceJob[]
  }
