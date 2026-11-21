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
  MediaJobKind,
  MediaJobStatus,
  PublicApiLogResultFilter,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyMatch,
  ResponseInspectionPolicyProtocolCode,
  ResponseInspectionPolicyScopeType,
  UsageRecordTrafficSource
} from '@/types/domain'
import type { AccountCapabilityDomain } from '@/views/accounts/accountCapabilityDomains'

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
  groupKind?: 'standard' | 'super'
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
  /** 账户下拉（§8.2）：缺省 profile 保持既有调用方行为；pool 项仅在显式请求时返回。 */
  kinds?: Array<'profile' | 'pool'>
  /** 与 selectedIds 分开：只匹配代理池，均最多 20 个。 */
  selectedPoolIds?: string[]
}

export interface ProxyPoolListParams {
  page?: number
  pageSize?: number
  keyword?: string
}

export interface ProxyPoolMemberOptionsParams {
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export interface ProxyPoolMemberPayload {
  proxyProfileId: string
  weight: number
  memberEnabled: boolean
}

export interface ProxyPoolCreatePayload {
  name: string
  description?: string
  strategy: 'round_robin' | 'weighted'
  enabled: boolean
  members: ProxyPoolMemberPayload[]
}

export interface ProxyPoolPatchPayload {
  name?: string
  /** members 出现即完整替换成员集合（§8.1）；CAS 版本必带。 */
  members?: ProxyPoolMemberPayload[]
  description?: string | null
  strategy?: 'round_robin' | 'weighted'
  enabled?: boolean
  expectedUpdatedAt: string
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
  // 读通道：按能力域过滤 AI 性能 base/series（六域 chat/embedding/rerank/video/audio/image；缺省=不过滤，前端 'all' 哨兵不下发）。
  domain?: AccountCapabilityDomain
}

export interface AiPerformanceSeriesParams extends AiPerformanceParams {
  accountIds: string[]
}

export interface AiHealthParams extends ListParams {
  hours?: number
  keyword?: string
  page?: number
  pageSize?: number
  // 读通道：按能力域过滤健康列表（缺省=不过滤，前端 'all' 哨兵不下发）。
  domain?: AccountCapabilityDomain
}

export interface AiHealthHourDetailParams extends ListParams {
  accountId: string
  statHour: string
}

export interface AiPerformanceAccountOptionsParams extends ListParams {
  keyword?: string
  accountIds?: string[]
  limit?: number
  // 读通道：按能力域过滤账户候选（缺省=不过滤，前端 'all' 哨兵不下发）。
  domain?: AccountCapabilityDomain
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
  /** 2026-11-07 裁决：enabled 包含时段外（gate=0），out_of_window 值已退役。 */
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  // M1 读通道：按能力域过滤列表/导出（六域 chat/embedding/rerank/video/audio/image；all=不过滤，前端六域菜单恒传具体域）。
  domain?: AccountCapabilityDomain
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
  /** options API 只接受 all/enabled/disabled/cooling；out_of_window 于 2026-11-07 退役。 */
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
  // providerProtocolProfileId 已随 B4 两表退役批停发：后端草稿链
  // （prepareAccountDraftTestSnapshot）改由 vendor 语义推导协议上下文，
  // 该键仅为滚动期旧部署的接受但忽略入参保留可选形状。
  providerProtocolProfileId?: string
  /** 草稿能力域（五域退役修订，AI账户上游接口能力设计 §3/§5）：后端草稿链按入参域走退役臂并判定测试方式；chat 域行为不变。 */
  capabilityDomain?: AccountCapabilityDomain
  /** 连接测试规格查找所用连接 preset；连接测试草稿必填，模型测试草稿可省略。 */
  connectionPresetCode?: string
  name: string
  type: string
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  /** 连接测试草稿可省略；模型测试草稿必填。 */
  healthCheckModel?: string
  /** 连接测试草稿可省略；模型测试草稿必填。 */
  healthCheckEndpointMode?: import('@/types/domain').AccountHealthCheckEndpointMode
  modelMappings: Array<{ sourceModel: string; sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content' | 'video_generation' | 'tts'; upstreamModel: string; upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'video_generation' | 'tts'; enabled: boolean }>
  proxyProfileId?: string | null
  proxyPoolId?: string | null
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
  model?: string
  testEndpointMode?: AccountSupportedEndpointMode
  prompt?: string
  testSessionId?: string
}

export interface AccountTestDraftOptionsPayload {
  account: AccountDraftTestAccountPayload
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export interface AccountBalanceDraftTestPayload {
  account: AccountDraftTestAccountPayload
  balanceQueryConfig: Record<string, unknown>
}

export interface ApiKeyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  /** out_of_window 服务端虚拟筛选值于 2026-11-07 退役：启用筛选天然包含时段外（gate=0），服务端已按不过滤归一。 */
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

/** 媒体任务列表查询参数；键名与后端 `/media-jobs` 查询串契约一致（limit/offset + snake_case 过滤键）。 */
export interface MediaJobListParams {
  limit?: number
  offset?: number
  status?: MediaJobStatus
  kind?: MediaJobKind
  api_key_id?: string
  account_id?: string
  /** 管理面专属：按任务归属 API Key 的属主用户过滤；my 端点不接受该参数（归属恒钉死调用者）。 */
  system_account_id?: string
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

export interface AuthorizationUsageRowsParams extends ListParams {
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
