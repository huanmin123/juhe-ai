import axios from 'axios'

import type {
  AccountSummary,
  AccountListResult,
  AccountTestResult,
  AccountTrafficMigrationResult,
  AccountTrafficMigrationSourceStatus,
  AccountUsageStatsOverview,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  AiPerformanceWindowKey,
  AnnouncementLevel,
  AnnouncementStatus,
  AnnouncementSummary,
  AuditLogDetail,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogRuntime,
  AuditLogSummary,
  AuditOutcome,
  AuthorizationResourceType,
  RequestQuotaLimits,
  ResourceAuthorizationSummary,
  ApiKeySummary,
  ApiKeyListResult,
  CaptchaChallengeSummary,
  CreatedApiKey,
  CurrentUserSummary,
  ErrorPolicySummary,
  GlobalSettings,
  GroupSummary,
  OpenAIAuthURLResult,
  OperationLogDetail,
  OperationLogListResult,
  ProviderDefinition,
  ProviderModelPricing,
  ProxyProfileOptionSummary,
  ProxyProfileSummary,
  ProxyTestReport,
  RuntimeLogFacets,
  RuntimeLogLevel,
  RuntimeLogGrepResult,
  RuntimeLogSearchResult,
  SystemTeamMemberSummary,
  SystemTeamSummary,
  SystemSettings,
  SystemAccountPrincipalSummary,
  SystemAccountSummary,
  SystemMetricsOverview,
  UsageOverviewWindowKey,
  UsageStatsOverview,
  UsageRecordListResult,
  UsageRecordSummary
} from '@/types/domain'

interface ApiResponse<T> {
  data: T
  message?: string
}

interface ListParams {
  systemAccountId?: string
}

interface RequestControlOptions {
  signal?: AbortSignal
}

interface UsageOverviewParams extends ListParams {
  window?: UsageOverviewWindowKey
}

interface AccountUsageStatsParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  type?: string
  startDate?: string
  endDate?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  limit?: number
}

interface AiPerformanceParams {
  window?: AiPerformanceWindowKey
  accountIds?: string[]
}

interface AiPerformanceAccountOptionsParams {
  keyword?: string
  accountIds?: string[]
  limit?: number
}

export type SortDirection = 'asc' | 'desc'
export type AccountListSortField = 'priority' | 'superPriority' | 'fallback' | 'qualityScore' | 'name' | 'type' | 'providerCode' | 'systemAccount' | 'concurrency' | 'status' | 'accountExpiresAt' | 'lastUsedAt' | 'notes'

export interface AccountListSortParam {
  field: AccountListSortField
  order: SortDirection
}

export interface AccountListParams extends ListParams {
  sorts?: AccountListSortParam[]
  page?: number
  pageSize?: number
  keyword?: string
  type?: string
  status?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  limit?: number
}

export interface ApiKeyListParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
  limit?: number
}

export interface UsageRecordListParams extends ListParams {
  page?: number
  pageSize?: number
  accountKeyword?: string
  result?: 'success' | 'failed' | 'all'
  statusCode?: number
  model?: string
  sortBy?: 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
  sortOrder?: SortDirection
  limit?: number
}

export interface AuditLogListParams extends ListParams {
  page?: number
  pageSize?: number
  traceId?: string
  outcome?: AuditOutcome | 'all'
  statusCode?: number
  path?: string
  model?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  clientIp?: string
  errorGroupId?: string
  limit?: number
}

export interface RuntimeLogGrepParams {
  keyword?: string[]
  keywords?: string
  limit?: number
}

export interface RuntimeLogListParams {
  page?: number
  pageSize?: number
  traceId?: string
  level?: RuntimeLogLevel | 'all'
  event?: string
  keyword?: string
  limit?: number
}

export interface OperationLogListParams {
  page?: number
  pageSize?: number
  keyword?: string
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
  limit?: number
}

export interface AuthorizationListParams extends ListParams {
  resourceType?: AuthorizationResourceType
  resourceId?: string
  granteeSystemAccountId?: string
  teamId?: string
  status?: 'active' | 'paused' | 'expired' | 'revoked' | 'all'
  direction?: 'all' | 'outbound' | 'inbound'
  startDate?: string
  endDate?: string
}

export type AuthorizationScopeParams = ListParams

export interface AuthorizationUsageParams extends AuthorizationScopeParams {
  startDate?: string
  endDate?: string
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

const http = axios.create({
  baseURL: normalizeApiBaseUrl(import.meta.env.VITE_JUHE_AI_API_BASE_URL as string | undefined),
  timeout: 15000,
  withCredentials: true
})

function normalizeApiBaseUrl(value?: string): string {
  const text = value?.trim()
  if (!text) return '/api'
  return text.replace(/\/+$/, '') || '/api'
}

async function unwrap<T>(request: Promise<{ data: ApiResponse<T> }>): Promise<T> {
  const response = await request
  return response.data.data
}

const noTimeout = { timeout: 0 }

export const api = {
  auth: {
    captcha: () => unwrap<CaptchaChallengeSummary>(http.get('/auth/captcha')),
    login: (payload: { username: string; password: string; captchaId: string; captchaCode: string }) => unwrap<CurrentUserSummary>(http.post('/auth/login', payload)),
    logout: () => unwrap<{ loggedOut: boolean }>(http.post('/auth/logout')),
    me: () => unwrap<CurrentUserSummary>(http.get('/auth/me')),
    changePassword: (payload: { oldPassword?: string; newPassword: string }) => unwrap<CurrentUserSummary>(http.post('/auth/change-password', payload))
  },
  systemAccounts: {
    list: () => unwrap<SystemAccountSummary[]>(http.get('/system-accounts')),
    create: (payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.post('/system-accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.patch(`/system-accounts/${id}`, payload))
  },
  authorizationOptions: {
    granteeAccounts: () => unwrap<SystemAccountPrincipalSummary[]>(http.get('/authorization-options/grantee-accounts'))
  },
  announcements: {
    publicList: (params?: AnnouncementListParams) => unwrap<AnnouncementSummary[]>(http.get('/announcements/public', { params })),
    markRead: (payload: { announcementIds: string[] }) => unwrap<AnnouncementReadResult>(http.post('/announcements/public/read', payload)),
    list: () => unwrap<AnnouncementSummary[]>(http.get('/announcements')),
    create: (payload: AnnouncementPayload) => unwrap<AnnouncementSummary>(http.post('/announcements', payload)),
    update: (id: string, payload: Partial<AnnouncementPayload>) => unwrap<AnnouncementSummary>(http.patch(`/announcements/${id}`, payload)),
    publish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/publish`)),
    unpublish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/unpublish`)),
    delete: (id: string) => http.delete(`/announcements/${id}`)
  },
  myAuthorizationOptions: {
    granteeAccounts: () => unwrap<SystemAccountPrincipalSummary[]>(http.get('/my-authorization-options/grantee-accounts'))
  },
  providers: {
    list: () => unwrap<ProviderDefinition[]>(http.get('/providers')),
    models: (code: string) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`))
  },
  errorPolicies: {
    list: () => unwrap<ErrorPolicySummary[]>(http.get('/error-policies'))
  },
  accounts: {
    list: (params?: AccountListParams) => unwrap<AccountListResult>(http.get('/accounts', { params: accountListParams(params) })),
    create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/accounts', payload, { params })),
    update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.patch(`/accounts/${id}`, payload, { params })),
    updateAuthorizedDispatch: (id: string, payload: { superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean }, params?: ListParams) => unwrap<AccountSummary>(http.patch(`/accounts/${id}/authorized-dispatch`, payload, { params })),
    bindGroup: (id: string, payload: { groupId: string }, params?: ListParams) => unwrap<AccountSummary>(http.post(`/accounts/${id}/group`, payload, { params })),
    migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }, params?: ListParams) => unwrap<AccountTrafficMigrationResult>(http.post(`/accounts/${id}/traffic-migration`, payload, { params })),
    test: (id: string, payload?: { model?: string; prompt?: string }, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestResult>(http.post(`/accounts/${id}/test`, payload ?? {}, { params, timeout: 130000, signal: options?.signal })),
    delete: (id: string, params?: ListParams) => http.delete(`/accounts/${id}`, { params })
  },
  myAccounts: {
    list: (params?: AccountListParams) => unwrap<AccountListResult>(http.get('/my-accounts', { params: accountListParams(params, false) })),
    create: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.patch(`/my-accounts/${id}`, payload)),
    updateAuthorizedDispatch: (id: string, payload: { superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean }) => unwrap<AccountSummary>(http.patch(`/my-accounts/${id}/authorized-dispatch`, payload)),
    bindGroup: (id: string, payload: { groupId: string }) => unwrap<AccountSummary>(http.post(`/my-accounts/${id}/group`, payload)),
    migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }) => unwrap<AccountTrafficMigrationResult>(http.post(`/my-accounts/${id}/traffic-migration`, payload)),
    test: (id: string, payload?: { model?: string; prompt?: string }, options?: RequestControlOptions) => unwrap<AccountTestResult>(http.post(`/my-accounts/${id}/test`, payload ?? {}, { timeout: 130000, signal: options?.signal })),
    delete: (id: string) => http.delete(`/my-accounts/${id}`)
  },
  groups: {
    list: (params?: ListParams) => unwrap<GroupSummary[]>(http.get('/groups', { params })),
    create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.post('/groups', payload, { params })),
    update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.patch(`/groups/${id}`, payload, { params })),
    delete: (id: string, params?: ListParams) => http.delete(`/groups/${id}`, { params })
  },
  myGroups: {
    list: () => unwrap<GroupSummary[]>(http.get('/my-groups')),
    create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/my-groups', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<GroupSummary>(http.patch(`/my-groups/${id}`, payload)),
    delete: (id: string) => http.delete(`/my-groups/${id}`)
  },
  systemTeams: {
    list: (params?: ListParams) => unwrap<SystemTeamSummary[]>(http.get('/system-teams', { params })),
    create: (payload: { name: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.post('/system-teams', payload)),
    update: (id: string, payload: { name?: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.patch(`/system-teams/${id}`, payload)),
    addMembers: (id: string, payload: { systemAccountIds: string[] }) => unwrap<SystemTeamSummary>(http.post(`/system-teams/${id}/members`, payload)),
    removeMember: (id: string, memberId: string) => unwrap<SystemTeamSummary>(http.delete(`/system-teams/${id}/members/${memberId}`))
  },
  myTeams: {
    list: () => unwrap<SystemTeamSummary[]>(http.get('/my-teams'))
  },
  authorizations: {
    list: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationSummary[]>(http.get('/authorizations', { params })),
    create: (payload: {
      resourceType: AuthorizationResourceType
      resourceId: string
      granteeType: 'system_account' | 'team'
      granteeId: string
      remark?: string
      expiresAt?: string
      limits?: RequestQuotaLimits
    }, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.post('/authorizations', payload, { params })),
    update: (id: string, payload: { status?: 'active' | 'paused'; expiresAt?: string | null; limits?: RequestQuotaLimits | null }, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.patch(`/authorizations/${id}`, payload, { params })),
    updateExpire: (id: string, payload: { expiresAt: string | null; limits?: RequestQuotaLimits | null }, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.patch(`/authorizations/${id}/expire`, payload, { params })),
    revoke: (id: string, payload?: { sourceType?: 'manual' | 'team'; sourceTeamId?: string }, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.delete(`/authorizations/${id}`, { data: payload, params })),
    usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/authorizations/${id}/usage`, { params }))
  },
  myAuthorizations: {
    list: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationSummary[]>(http.get('/my-authorizations', { params: stripSystemAccountParam(params) })),
    create: (payload: {
      resourceType: AuthorizationResourceType
      resourceId: string
      granteeType: 'system_account' | 'team'
      granteeId: string
      remark?: string
      expiresAt?: string
      limits?: RequestQuotaLimits
    }) => unwrap<ResourceAuthorizationSummary>(http.post('/my-authorizations', payload)),
    update: (id: string, payload: { status?: 'active' | 'paused'; expiresAt?: string | null; limits?: RequestQuotaLimits | null }) => unwrap<ResourceAuthorizationSummary>(http.patch(`/my-authorizations/${id}`, payload)),
    updateExpire: (id: string, payload: { expiresAt: string | null; limits?: RequestQuotaLimits | null }) => unwrap<ResourceAuthorizationSummary>(http.patch(`/my-authorizations/${id}/expire`, payload)),
    revoke: (id: string, payload?: { sourceType?: 'manual' | 'team'; sourceTeamId?: string }) => unwrap<ResourceAuthorizationSummary>(http.delete(`/my-authorizations/${id}`, { data: payload })),
    usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/my-authorizations/${id}/usage`, { params: stripSystemAccountParam(params) }))
  },
  apiKeys: {
    list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/api-keys', { params })),
    create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<CreatedApiKey>(http.post('/api-keys', payload, { params })),
    update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<ApiKeySummary>(http.patch(`/api-keys/${id}`, payload, { params })),
    delete: (id: string, params?: ListParams) => http.delete(`/api-keys/${id}`, { params })
  },
  myApiKeys: {
    list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/my-api-keys', { params: stripSystemAccountParam(params) })),
    create: (payload: Record<string, unknown>) => unwrap<CreatedApiKey>(http.post('/my-api-keys', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ApiKeySummary>(http.patch(`/my-api-keys/${id}`, payload)),
    delete: (id: string) => http.delete(`/my-api-keys/${id}`)
  },
  openaiOAuth: {
    authUrl: (payload: Record<string, unknown>) => unwrap<OpenAIAuthURLResult>(http.post('/openai-oauth/auth-url', payload)),
    createFromCode: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-code', payload, { params })),
    createFromRefreshToken: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-refresh-token', payload, { params })),
    refreshToken: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/refresh-token`, {}, { params, timeout: 130000 })),
    reauthorizeFromCode: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/reauthorize-from-code`, payload, { params })),
    reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload, { params }))
  },
  myOpenaiOAuth: {
    authUrl: (payload: Record<string, unknown>) => unwrap<OpenAIAuthURLResult>(http.post('/my-openai-oauth/auth-url', payload)),
    createFromCode: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-openai-oauth/create-from-code', payload)),
    createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-openai-oauth/create-from-refresh-token', payload)),
    refreshToken: (id: string) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/refresh-token`, {}, { timeout: 130000 })),
    reauthorizeFromCode: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/reauthorize-from-code`, payload)),
    reauthorizeFromRefreshToken: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post(`/my-openai-oauth/accounts/${id}/reauthorize-from-refresh-token`, payload))
  },
  proxies: {
    list: () => unwrap<ProxyProfileSummary[]>(http.get('/proxies')),
    options: () => unwrap<ProxyProfileOptionSummary[]>(http.get('/proxies/options')),
    create: (payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.post('/proxies', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.patch(`/proxies/${id}`, payload)),
    test: (id: string) => unwrap<ProxyTestReport>(http.post(`/proxies/${id}/test`, {}, { timeout: 120000 })),
    delete: (id: string) => http.delete(`/proxies/${id}`)
  },
  usageRecords: {
    list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/usage-records', { params })),
    detail: (id: string, params?: ListParams) => unwrap<UsageRecordSummary>(http.get(`/usage-records/${id}`, { params }))
  },
  myUsageRecords: {
    list: (params?: UsageRecordListParams) => unwrap<UsageRecordListResult>(http.get('/my-usage-records', { params: stripSystemAccountParam(params) })),
    detail: (id: string) => unwrap<UsageRecordSummary>(http.get(`/my-usage-records/${id}`))
  },
  auditLogs: {
    list: (params?: AuditLogListParams) => unwrap<AuditLogListResult>(http.get('/audit-logs', { params, ...noTimeout })),
    runtime: () => unwrap<AuditLogRuntime>(http.get('/audit-logs/runtime', noTimeout)),
    detail: (id: string) => unwrap<AuditLogDetail>(http.get(`/audit-logs/${id}`, noTimeout)),
    payload: (id: string, payloadId: string) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, noTimeout))
  },
  runtimeLogs: {
    list: (params?: RuntimeLogListParams) => unwrap<RuntimeLogSearchResult>(http.get('/runtime-logs', { params })),
    facets: () => unwrap<RuntimeLogFacets>(http.get('/runtime-logs/facets')),
    grep: (params?: RuntimeLogGrepParams) => unwrap<RuntimeLogGrepResult>(http.get('/runtime-logs/grep', { params, ...noTimeout }))
  },
  operationLogs: {
    list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/operation-logs', { params })),
    detail: (id: string) => unwrap<OperationLogDetail>(http.get(`/operation-logs/${id}`))
  },
  myOperationLogs: {
    list: (params?: OperationLogListParams) => unwrap<OperationLogListResult>(http.get('/my-operation-logs', { params: stripAdminOperationLogParams(params) })),
    detail: (id: string) => unwrap<OperationLogDetail>(http.get(`/my-operation-logs/${id}`))
  },
  stats: {
    usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/stats/usage-overview', { params })),
    accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/stats/account-usage', { params })),
    systemMetrics: (params?: Pick<UsageOverviewParams, 'window'>) => unwrap<SystemMetricsOverview>(http.get('/stats/system-metrics', { params }))
  },
  myStats: {
    usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/my-stats/usage-overview', { params: stripSystemAccountParam(params) })),
    accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/my-stats/account-usage', { params: stripSystemAccountParam(params) })),
    aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/my-stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params) })),
    aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceOverview>(http.get('/my-stats/ai-performance', { params: aiPerformanceParams(params) }))
  },
  settings: {
    public: () => unwrap<GlobalSettings>(http.get('/settings/public')),
    global: () => unwrap<GlobalSettings>(http.get('/settings/global')),
    updateGlobal: (payload: GlobalSettings) => unwrap<GlobalSettings>(http.patch('/settings/global', payload)),
    get: () => unwrap<SystemSettings>(http.get('/settings')),
    update: (payload: SystemSettings) => unwrap<SystemSettings>(http.patch('/settings', payload))
  }
}

function stripSystemAccountParam<T extends object>(params?: T): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output = { ...params } as Record<string, unknown>
  delete output.systemAccountId
  return Object.keys(output).length ? output : undefined
}

function stripAdminOperationLogParams(params?: OperationLogListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output = { ...params } as Record<string, unknown>
  delete output.actorSystemAccountId
  delete output.affectedSystemAccountId
  delete output.operationScopeSystemAccountId
  return Object.keys(output).length ? output : undefined
}

function accountListParams(params?: AccountListParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  if (params.keyword) output.keyword = params.keyword
  if (params.type && params.type !== 'all') output.type = params.type
  if (params.status && params.status !== 'all') output.status = params.status
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  if (params.sorts?.length) {
    output.sorts = params.sorts.map((sort) => `${sort.field}:${sort.order}`).join(',')
  }
  return output
}

function aiPerformanceParams(params?: AiPerformanceParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.window) output.window = params.window
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  return Object.keys(output).length ? output : undefined
}

function aiPerformanceAccountOptionsParams(params?: AiPerformanceAccountOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}
