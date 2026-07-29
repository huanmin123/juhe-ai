import type {
  AnnouncementLevel,
  AnnouncementStatus,
  AccountSupportedEndpointMode,
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
  PublicApiLogResultFilter,
  RequestQuotaLimits,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyMatch,
  ResponseInspectionPolicyProtocolCode,
  ResponseInspectionPolicyScopeType,
  RuntimeLogLevel,
  UsageRecordTrafficSource
} from '@/types/domain'

export interface ListParams {
  systemAccountId?: string
  viewScope?: 'admin' | 'self'
}

export interface GroupListParams extends ListParams {
  page?: number
  pageSize?: number
}

export interface GroupOptionParams extends ListParams {
  ids?: string[]
  keyword?: string
  providerCode?: string
  limit?: number
  manageableOnly?: boolean
  preferDefault?: boolean
  purpose?: 'select' | 'account'
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
  selectedIds?: string[]
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
}

export interface AiPerformanceSeriesParams extends AiPerformanceParams {
  accountIds: string[]
}

export interface AiHealthParams extends ListParams {
  hours?: number
  keyword?: string
  page?: number
  pageSize?: number
}

export interface AiHealthHourDetailParams extends ListParams {
  accountId: string
  statHour: string
}

export interface AiPerformanceAccountOptionsParams extends ListParams {
  keyword?: string
  accountIds?: string[]
  limit?: number
}

export type SortDirection = 'asc' | 'desc'
export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt'

export interface AccountListSortParam {
  field: AccountListSortField
  order: SortDirection
}

export interface AccountListParams extends ListParams {
  ids?: string[]
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

export interface AccountUsageStatsOptionParams extends ListParams {
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export interface AccountTestOptionsParams extends ListParams {
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export interface AccountTestPayload {
  model?: string
  testEndpointMode?: AccountSupportedEndpointMode
  prompt?: string
  testSessionId?: string
  account?: AccountDraftTestAccountPayload
}

export interface AccountDraftTestAccountPayload {
  providerCode: string
  providerProtocolProfileId: string
  name: string
  type: string
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  healthCheckModel: string
  healthCheckEndpointMode: import('@/types/domain').AccountHealthCheckEndpointMode
  modelMappings: Array<{ sourceModel: string; sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'; upstreamModel: string; upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content'; enabled: boolean }>
  proxyProfileId?: string | null
  groupId: string
  accountExpiresAt?: string | null
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface AccountModelCatalogDiscoveryAccountPayload {
  providerCode: string
  providerProtocolProfileId: string
  type: string
  credentials: Record<string, unknown>
  name?: string
  groupId?: string
  proxyProfileId?: string | null
  supportedModels?: string[]
  healthCheckModel?: string
  healthCheckEndpointMode?: import('@/types/domain').AccountHealthCheckEndpointMode
}

export interface AccountDraftTestPayload {
  account: AccountDraftTestAccountPayload
  testEndpointMode?: AccountSupportedEndpointMode
  prompt?: string
  testSessionId?: string
}

export interface AccountBalanceDraftTestPayload {
  account: AccountDraftTestAccountPayload
  balanceQueryConfig: Record<string, unknown>
}

export interface ApiKeyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  routeStrategyId?: string
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
  sortBy?: 'createdAt'
  sortOrder?: SortDirection
}

export interface AuditLogListParams extends ListParams {
  page?: number
  pageSize?: number
  traceId?: string
  sessionId?: string
  sessionClientType?: string
  outcome?: AuditOutcome | 'all'
  statusCode?: number
  path?: string
  model?: string
  clientIp?: string
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

export interface TableMonitorOverviewParams {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface TableMonitorDatabaseHistoryParams {
  startAt?: string
  endAt?: string
  limit?: number
}

export interface TableMonitorHistoryParams extends TableMonitorDatabaseHistoryParams {
  databaseRole: MonitoredDatabaseRole
  tableName: string
}

export interface NonBusinessDataCleanupPayload {
  cutoffAt: string
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

export interface ResponseInspectionPolicyCreatePayload {
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: ResponseInspectionPolicyProtocolCode
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicyAction
  notes?: string
}

export interface ResponseInspectionPolicyProviderOptionsParams {
  protocolCode: ResponseInspectionPolicyProtocolCode
  scopeType: ResponseInspectionPolicyScopeType
  keyword?: string
}

export type ResponseInspectionPolicyPatchChanges = Partial<Omit<ResponseInspectionPolicyCreatePayload, 'providerCode' | 'notes'>> & {
  providerCode?: string | null
  notes?: string | null
}

export type ResponseInspectionPolicyPatchPayload = {
  expectedUpdatedAt: string
} & ResponseInspectionPolicyPatchChanges

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

export interface AuthorizationUsageRowsParams extends AuthorizationScopeParams {
  resourceType?: AuthorizationResourceType
  resourceId?: string
  granteeSystemAccountId?: string
  teamId?: string
  startDate?: string
  endDate?: string
  page?: number
  pageSize?: number
}

export type AuthorizationTeamUsageSummaryParams = Omit<AuthorizationUsageRowsParams, 'page' | 'pageSize' | 'granteeSystemAccountId'>
export type AuthorizationUserUsageSummaryParams = Omit<AuthorizationUsageRowsParams, 'page' | 'pageSize'>

export interface AnnouncementListParams {
  limit?: number
}

export interface AnnouncementPayload {
  title: string
  content: string
  level?: AnnouncementLevel
  status?: AnnouncementStatus
}

export interface AnnouncementPatchPayload extends Partial<AnnouncementPayload> {
  expectedRevision: string
}

export interface AnnouncementVersionPayload {
  expectedRevision: string
}

export interface AnnouncementReadResult {
  readAt: string
  count: number
}
