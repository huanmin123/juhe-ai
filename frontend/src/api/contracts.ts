import type {
  AccountClientCompatibility,
  AnnouncementLevel,
  AnnouncementStatus,
  AuditOutcome,
  AuditTrafficSource,
  AuthorizationResourceType,
  ClientIpStatsSortField,
  ClientIpStatus,
  ExternalIntegrationSourceStatus,
  ModelCheckProgressEvent,
  ModelCheckRunDetail,
  ModelCheckRunListParams,
  MonitoredDatabaseRole,
  OpenAICompatibleMcpApprovalPolicy,
  OpenAICompatibleMcpApprovalStatus,
  OpenAICompatibleMcpExecutionStatus,
  PublicApiLogResultFilter,
  RequestQuotaLimits,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyMatch,
  ResponseInspectionPolicyScopeType,
  RuntimeLogLevel,
  UsageRecordTrafficSource
} from '@/types/domain'

export interface ListParams {
  systemAccountId?: string
}

export interface GroupListParams extends ListParams {
  page?: number
  pageSize?: number
}

export interface GroupOptionParams extends ListParams {
  ids?: string[]
  keyword?: string
  providerCode?: string
  providerProtocolProfileId?: string
  limit?: number
  manageableOnly?: boolean
  preferDefault?: boolean
}

export interface TeamListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface ProxyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface ProxyOptionParams {
  keyword?: string
  limit?: number
}

export interface SystemAccountOptionsParams {
  ids?: string[]
  keyword?: string
  limit?: number
}

export interface SystemAccountListParams {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface RequestControlOptions {
  signal?: AbortSignal
}

export interface UsageOverviewParams extends ListParams {
  startDate?: string
  endDate?: string
}

export interface AccountUsageStatsParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  startDate?: string
  endDate?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  accountIds?: string[]
}

export interface AiPerformanceParams extends ListParams {
  startDate?: string
  endDate?: string
  accountIds?: string[]
}

export interface AiPerformanceAccountOptionsParams extends ListParams {
  keyword?: string
  accountIds?: string[]
  limit?: number
}

export type SortDirection = 'asc' | 'desc'
export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'qualityScore' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt'

export interface AccountListSortParam {
  field: AccountListSortField
  order: SortDirection
}

export interface AccountListParams extends ListParams {
  sorts?: AccountListSortParam[]
  page?: number
  pageSize?: number
  keyword?: string
  providerCode?: string
  groupId?: string
  tagIds?: string[]
  type?: string
  status?: string | string[]
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
}

export type AccountExportFilters = Omit<AccountListParams, 'systemAccountId' | 'page' | 'pageSize'>
export type AccountExportPayload =
  | { accountIds: string[] }
  | { filters: AccountExportFilters }

export interface AccountOptionParams extends ListParams {
  ids?: string[]
  page?: number
  limit?: number
  keyword?: string
  providerCode?: string
  groupId?: string
  tagIds?: string[]
  type?: string
  status?: string | string[]
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
}

export interface AccountTestPayload {
  model?: string
  prompt?: string
  clientCompatibility?: AccountClientCompatibility
  testSessionId?: string
  account?: AccountDraftTestAccountPayload
}

export interface AccountDraftTestAccountPayload {
  providerCode: string
  providerProtocolProfileId?: string
  name: string
  type: string
  clientCompatibility?: AccountClientCompatibility
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  modelMappings: Array<{ sourceModel: string; sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'; upstreamModel: string; upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content'; enabled: boolean }>
  proxyProfileId?: string | null
  groupId: string
  accountExpiresAt?: string | null
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface AccountDraftTestPayload extends AccountTestPayload {
  account: AccountDraftTestAccountPayload
}

export interface ApiKeyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
}

export interface UsageRecordListParams extends ListParams {
  page?: number
  pageSize?: number
  traceId?: string
  accountKeyword?: string
  clientIp?: string
  result?: 'success' | 'failed' | 'all'
  statusCode?: number
  groupId?: string
  model?: string
  trafficSource?: UsageRecordTrafficSource
  startDate?: string
  endDate?: string
  sortBy?: 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
  sortOrder?: SortDirection
}

export interface OpenAICompatibleMcpServerListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  enabled?: 'all' | 'true' | 'false'
}

export interface OpenAICompatibleMcpServerPayload {
  label: string
  serverUrl: string
  description?: string | null
  enabled?: boolean
  allowedTools?: string[]
  defaultApprovalPolicy?: OpenAICompatibleMcpApprovalPolicy
  timeoutMs?: number | null
  maxRetries?: number | null
  retryDelayMs?: number | null
  maxBodyBytes?: number | null
  maxOutputBytes?: number | null
  allowRequestAuthorization?: boolean
  authorizationRef?: string | null
}

export interface OpenAICompatibleMcpServerDiagnosePayload {
  authorization?: string
}

export interface OpenAICompatibleMcpApprovalRequestListParams extends ListParams {
  page?: number
  pageSize?: number
  apiKeyId?: string
  groupId?: string
  traceId?: string
  serverLabel?: string
  toolName?: string
  status?: OpenAICompatibleMcpApprovalStatus | 'all'
  startAt?: string
  endAt?: string
}

export interface OpenAICompatibleMcpApprovalRejectPayload {
  rejectReason?: string
}

export interface OpenAICompatibleMcpExecutionRecordListParams extends ListParams {
  page?: number
  pageSize?: number
  apiKeyId?: string
  groupId?: string
  traceId?: string
  approvalRequestId?: string
  serverLabel?: string
  toolName?: string
  status?: OpenAICompatibleMcpExecutionStatus | 'all'
  startAt?: string
  endAt?: string
}

export interface AuditLogListParams extends ListParams {
  page?: number
  pageSize?: number
  traceId?: string
  outcome?: AuditOutcome | 'all'
  statusCode?: number
  path?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  errorGroupId?: string
  trafficSource?: AuditTrafficSource
}

export interface AuditLogHotSearchParams {
  keywords?: string
  limit?: number
  startAt?: string
  endAt?: string
}

export interface AuditLogPayloadParams {
  offset?: number
  limit?: number
}

export interface PublicApiLogListParams {
  page?: number
  pageSize?: number
  traceId?: string
  sourceRefId?: string
  path?: string
  result?: PublicApiLogResultFilter
  statusCode?: number
  clientIp?: string
  startAt?: string
  endAt?: string
}

export interface RuntimeLogGrepParams {
  keywords?: string
  startAt?: string
  endAt?: string
  limit?: number
}

export interface RuntimeLogListParams {
  page?: number
  pageSize?: number
  traceId?: string
  level?: RuntimeLogLevel | 'all'
  event?: string
  keyword?: string
  startAt?: string
  endAt?: string
}

export interface OperationLogListParams {
  page?: number
  pageSize?: number
  summaryKeyword?: string
  module?: string
  action?: string
  resourceType?: string
  resourceId?: string
  traceId?: string
  startAt?: string
  endAt?: string
  actorSystemAccountId?: string
  affectedSystemAccountId?: string
  operationScopeSystemAccountId?: string
}

export interface TableMonitorHistoryParams {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}

export interface TableMonitorOverviewParams {
  startAt?: string
  endAt?: string
  limit?: number
}

export interface TableMonitorDatabaseHistoryParams {
  startAt?: string
  endAt?: string
  limit?: number
}

export interface NonBusinessDataCleanupPayload {
  cutoffAt: string
  batchSize?: number
  maxBatches?: number
}

export interface ClientIpStatsListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ClientIpStatus
  startDate?: string
  endDate?: string
  sortField?: ClientIpStatsSortField
  sortOrder?: SortDirection
}

export interface ClientIpStatsDetailParams {
  page?: number
  pageSize?: number
  startDate?: string
  endDate?: string
  sortField?: ClientIpStatsSortField
  sortOrder?: SortDirection
}

export interface ClientIpPolicyPayload {
  reason?: string
  durationMinutes?: number
  durationDays?: number
}

export interface ExternalIntegrationSourceListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ExternalIntegrationSourceStatus | 'all'
}

export interface ResponseInspectionPolicyPayload {
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicyAction
  notes?: string
}

export interface ModelCheckScopeParams {
  systemAccountId?: string
}

export type ModelCheckListParams = ModelCheckRunListParams

export interface ModelCheckStreamOptions extends RequestControlOptions {
  onProgress?: (event: ModelCheckProgressEvent) => void
  onComplete?: (detail: ModelCheckRunDetail) => void
  onError?: (error: { message?: string; statusCode?: number }) => void
}

export interface AuthorizationListParams extends ListParams {
  keyword?: string
  resourceType?: AuthorizationResourceType
  resourceId?: string
  resourceOwnerSystemAccountId?: string
  granteeSystemAccountId?: string
  teamId?: string
  status?: 'active' | 'paused' | 'expired' | 'revoked' | 'returned' | 'all'
  direction?: 'all' | 'outbound' | 'inbound'
  sourceType?: 'all' | 'manual' | 'team'
  startDate?: string
  endDate?: string
  page?: number
  pageSize?: number
}

export type AuthorizationScopeParams = ListParams

export interface AuthorizationPrincipalOptionsParams {
  ids?: string[]
  keyword?: string
  limit?: number
}

export interface AuthorizationGranteeGroupOptionsParams extends AuthorizationPrincipalOptionsParams {
  granteeSystemAccountId: string
  providerCode?: string
  preferDefault?: boolean
}

export interface AuthorizationUsageParams extends AuthorizationScopeParams {
  startDate?: string
  endDate?: string
  page?: number
  pageSize?: number
}

export interface AuthorizationUsageOverviewParams extends AuthorizationScopeParams {
  resourceType?: AuthorizationResourceType
  resourceId?: string
  granteeSystemAccountId?: string
  teamId?: string
  startDate?: string
  endDate?: string
  page?: number
  pageSize?: number
}

export interface AnnouncementListParams {
  limit?: number
}

export interface AnnouncementPayload {
  title: string
  content: string
  level?: AnnouncementLevel
  status?: AnnouncementStatus
}

export interface AnnouncementReadResult {
  readAt: string
  count: number
}
