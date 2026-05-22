import axios from 'axios'

import type {
  AccountSummary,
  AccountListResult,
  AccountGroupOptionSummary,
  AccountOptionSummary,
  AccountTestResult,
  AccountTrafficMigrationResult,
  AccountTrafficMigrationSourceStatus,
  AccountUsageStatsOverview,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
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
  AuthorizationTeamUsageOverview,
  AuthorizationUserUsageOverview,
  RequestQuotaLimits,
  ResourceAuthorizationSummary,
  ApiKeySummary,
  ApiKeyListResult,
  CaptchaChallengeSummary,
  CreatedApiKey,
  CurrentUserSummary,
  ErrorPolicySummary,
  GlobalSettings,
  GroupListResult,
  GroupOptionSummary,
  GroupSummary,
  OpenAIAuthURLResult,
  OperationLogDetail,
  OperationLogListResult,
  ProviderDefinition,
  ProviderModelPricing,
  ProxyProfileOptionSummary,
  ProxyProfileListResult,
  ProxyProfileSummary,
  ProxyTestReport,
  RuntimeLogFacets,
  RuntimeLogGrepResult,
  RuntimeLogLevel,
  RuntimeLogSummary,
  RuntimeLogSearchResult,
  UpstreamErrorFeatureRuleCatalogItem,
  ResourceAuthorizationListResult,
  SystemTeamMemberSummary,
  SystemTeamListResult,
  SystemTeamPrincipalSummary,
  SystemTeamSummary,
  SystemSettings,
  SystemAccountPrincipalSummary,
  SystemAccountListResult,
  SystemAccountSummary,
  SystemMetricsOverview,
  ApiKeyRecordCleanupQueueTarget,
  ModelCheckOptions,
  ModelCheckProgressEvent,
  ModelCheckRunDetail,
  ModelCheckRunListParams,
  ModelCheckRunListResult,
  ModelCheckRunPayload,
  MonitoredDatabaseRole,
  TableStorageOverview,
  TableStorageSnapshotSummary,
  UsageRecordsCleanupResult,
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

interface GroupListParams extends ListParams {
  page?: number
  pageSize?: number
  limit?: number
}

interface GroupOptionParams extends ListParams {
  keyword?: string
  providerCode?: string
  limit?: number
  manageableOnly?: boolean
  preferDefault?: boolean
}

interface TeamListParams extends ListParams {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
}

interface ProxyListParams extends ListParams {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
}

interface SystemAccountOptionsParams {
  keyword?: string
  limit?: number
}

interface SystemAccountListParams {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
}

interface RequestControlOptions {
  signal?: AbortSignal
}

interface UsageOverviewParams extends ListParams {
  startDate?: string
  endDate?: string
}

interface AccountUsageStatsParams extends ListParams {
  page?: number
  pageSize?: number
  keyword?: string
  startDate?: string
  endDate?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  accountIds?: string[]
  limit?: number
}

interface AiPerformanceParams extends ListParams {
  startDate?: string
  endDate?: string
  accountIds?: string[]
}

interface AiPerformanceAccountOptionsParams extends ListParams {
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
  groupId?: string
  type?: string
  status?: string | string[]
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
  startDate?: string
  endDate?: string
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
  apiKeyId?: string
  groupId?: string
  accountId?: string
  errorGroupId?: string
  limit?: number
}

export interface AuditLogPayloadParams {
  offset?: number
  limit?: number
}

export interface RuntimeLogGrepParams {
  keyword?: string[]
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

interface TableMonitorHistoryParams {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  startAt?: string
  endAt?: string
  limit?: number
}

interface TableMonitorOverviewParams {
  startAt?: string
  endAt?: string
  limit?: number
}

interface TableMonitorCleanupTargetsParams {
  limit?: number
}

interface UsageRecordsCleanupPayload {
  cutoffAt: string
  batchSize?: number
  maxBatches?: number
}

export type ModelCheckListParams = ModelCheckRunListParams

export interface ModelCheckStreamOptions extends RequestControlOptions {
  onProgress?: (event: ModelCheckProgressEvent) => void
  onComplete?: (detail: ModelCheckRunDetail) => void
  onError?: (error: { message?: string; statusCode?: number }) => void
}

export interface AuthorizationListParams extends ListParams {
  resourceType?: AuthorizationResourceType
  resourceId?: string
  granteeSystemAccountId?: string
  teamId?: string
  status?: 'active' | 'paused' | 'expired' | 'revoked' | 'all'
  direction?: 'all' | 'outbound' | 'inbound'
  sourceType?: 'all' | 'manual' | 'team'
  startDate?: string
  endDate?: string
  page?: number
  pageSize?: number
}

export type AuthorizationScopeParams = ListParams

interface AuthorizationPrincipalOptionsParams {
  keyword?: string
  limit?: number
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

const http = axios.create({
  baseURL: normalizeApiBaseUrl(import.meta.env.VITE_JUHE_AI_API_BASE_URL as string | undefined),
  timeout: 15000,
  withCredentials: true
})

function normalizeApiBaseUrl(value?: string): string {
  const text = value?.trim()
  if (!text) return '/__aisys__/api'
  return text.replace(/\/+$/, '') || '/__aisys__/api'
}

async function unwrap<T>(request: Promise<{ data: ApiResponse<T> }>): Promise<T> {
  const response = await request
  return response.data.data
}

const noTimeout = { timeout: 0 }

async function runModelCheckStream(path: string, payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions): Promise<ModelCheckRunDetail> {
  const response = await fetch(`${normalizeApiBaseUrl(import.meta.env.VITE_JUHE_AI_API_BASE_URL as string | undefined)}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers: {
      accept: 'text/event-stream',
      'content-type': 'application/json'
    },
    body: JSON.stringify(payload),
    signal: options?.signal
  })
  if (!response.ok) {
    throw new Error(await readFetchErrorMessage(response))
  }
  if (!response.body) {
    throw new Error('模型检测进度流不可用')
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let completedDetail: ModelCheckRunDetail | undefined
  const handleMessage = (raw: string): void => {
    const event = parseServerSentEvent(raw)
    if (!event.data) return
    const payload = parseJsonPayload(event.data)
    if (event.event === 'progress') {
      options?.onProgress?.(payload as ModelCheckProgressEvent)
      return
    }
    if (event.event === 'complete') {
      completedDetail = payload as ModelCheckRunDetail
      options?.onComplete?.(completedDetail)
      return
    }
    if (event.event === 'error') {
      const error = payload as { message?: string; statusCode?: number }
      options?.onError?.(error)
      throw new Error(error.message || '模型检测失败')
    }
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    buffer = flushServerSentEvents(buffer, handleMessage)
  }
  buffer += decoder.decode()
  flushServerSentEvents(buffer, handleMessage, true)
  if (!completedDetail) {
    throw new Error('模型检测进度流未返回完成结果')
  }
  return completedDetail
}

function flushServerSentEvents(buffer: string, handleMessage: (raw: string) => void, flushRemaining = false): string {
  let normalized = buffer.replace(/\r\n/g, '\n')
  let separatorIndex = normalized.indexOf('\n\n')
  while (separatorIndex >= 0) {
    const raw = normalized.slice(0, separatorIndex)
    if (raw.trim()) {
      handleMessage(raw)
    }
    normalized = normalized.slice(separatorIndex + 2)
    separatorIndex = normalized.indexOf('\n\n')
  }
  if (flushRemaining && normalized.trim()) {
    handleMessage(normalized)
    return ''
  }
  return normalized
}

function parseServerSentEvent(raw: string): { event: string; data: string } {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  return { event, data: dataLines.join('\n') }
}

function parseJsonPayload(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return { message: text }
  }
}

async function readFetchErrorMessage(response: Response): Promise<string> {
  const text = await response.text()
  if (!text.trim()) return `请求失败：HTTP ${response.status}`
  try {
    const json = JSON.parse(text) as { message?: string; data?: { message?: string } }
    return json.message || json.data?.message || text
  } catch {
    return text
  }
}

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
    listPage: (params?: SystemAccountListParams) => unwrap<SystemAccountListResult>(http.get('/system-accounts', { params: systemAccountListParams(params) })),
    options: (params?: SystemAccountOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/system-accounts/options', { params: systemAccountOptionsParams(params) })),
    create: (payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.post('/system-accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.patch(`/system-accounts/${id}`, payload))
  },
  authorizationOptions: {
    granteeAccounts: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/authorization-options/grantee-accounts', { params: authorizationPrincipalOptionsParams(params) })),
    granteeTeams: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemTeamPrincipalSummary[]>(http.get('/authorization-options/grantee-teams', { params: authorizationPrincipalOptionsParams(params) }))
  },
  announcements: {
    publicList: (params?: AnnouncementListParams) => unwrap<AnnouncementSummary[]>(http.get('/announcements/public', { params })),
    markRead: (payload: { announcementIds: string[] }) => unwrap<AnnouncementReadResult>(http.post('/announcements/public/read', payload)),
    list: () => unwrap<AnnouncementSummary[]>(http.get('/announcements')),
    detail: (id: string) => unwrap<AnnouncementSummary>(http.get(`/announcements/${id}`)),
    create: (payload: AnnouncementPayload) => unwrap<AnnouncementSummary>(http.post('/announcements', payload)),
    update: (id: string, payload: Partial<AnnouncementPayload>) => unwrap<AnnouncementSummary>(http.patch(`/announcements/${id}`, payload)),
    publish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/publish`)),
    unpublish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/unpublish`)),
    delete: (id: string) => http.delete(`/announcements/${id}`)
  },
  myAuthorizationOptions: {
    granteeAccounts: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/my-authorization-options/grantee-accounts', { params: authorizationPrincipalOptionsParams(params) })),
    granteeTeams: (params?: AuthorizationPrincipalOptionsParams) => unwrap<SystemTeamPrincipalSummary[]>(http.get('/my-authorization-options/grantee-teams', { params: authorizationPrincipalOptionsParams(params) }))
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
    options: (params?: AccountListParams) => unwrap<AccountOptionSummary[]>(http.get('/accounts/options', { params: accountOptionsParams(params) })),
    detail: (id: string, params?: ListParams) => unwrap<AccountSummary>(http.get(`/accounts/${id}`, { params })),
    create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.post('/accounts', payload, { params })),
    update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<AccountSummary>(http.patch(`/accounts/${id}`, payload, { params })),
    updateAuthorizedDispatch: (id: string, payload: { superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean }, params?: ListParams) => unwrap<AccountSummary>(http.patch(`/accounts/${id}/authorized-dispatch`, payload, { params })),
    bindGroup: (id: string, payload: { groupId: string; dispatchWeight?: number; softConcurrencyLimit?: number | null }, params?: ListParams) => unwrap<AccountSummary>(http.post(`/accounts/${id}/group`, payload, { params })),
    migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }, params?: ListParams) => unwrap<AccountTrafficMigrationResult>(http.post(`/accounts/${id}/traffic-migration`, payload, { params })),
    test: (id: string, payload?: { model?: string; prompt?: string }, params?: ListParams, options?: RequestControlOptions) => unwrap<AccountTestResult>(http.post(`/accounts/${id}/test`, payload ?? {}, { params, timeout: 130000, signal: options?.signal })),
    delete: (id: string, params?: ListParams) => http.delete(`/accounts/${id}`, { params })
  },
  myAccounts: {
    list: (params?: AccountListParams) => unwrap<AccountListResult>(http.get('/my-accounts', { params: accountListParams(params, false) })),
    options: (params?: AccountListParams) => unwrap<AccountOptionSummary[]>(http.get('/my-accounts/options', { params: accountOptionsParams(params, false) })),
    detail: (id: string) => unwrap<AccountSummary>(http.get(`/my-accounts/${id}`)),
    create: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/my-accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.patch(`/my-accounts/${id}`, payload)),
    updateAuthorizedDispatch: (id: string, payload: { superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean }) => unwrap<AccountSummary>(http.patch(`/my-accounts/${id}/authorized-dispatch`, payload)),
    bindGroup: (id: string, payload: { groupId: string; dispatchWeight?: number; softConcurrencyLimit?: number | null }) => unwrap<AccountSummary>(http.post(`/my-accounts/${id}/group`, payload)),
    migrateTraffic: (id: string, payload: { targetAccountId: string; sourceStatus?: AccountTrafficMigrationSourceStatus }) => unwrap<AccountTrafficMigrationResult>(http.post(`/my-accounts/${id}/traffic-migration`, payload)),
    test: (id: string, payload?: { model?: string; prompt?: string }, options?: RequestControlOptions) => unwrap<AccountTestResult>(http.post(`/my-accounts/${id}/test`, payload ?? {}, { timeout: 130000, signal: options?.signal })),
    delete: (id: string) => http.delete(`/my-accounts/${id}`)
  },
  groups: {
    list: (params?: ListParams) => unwrap<GroupSummary[]>(http.get('/groups', { params })),
    listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/groups', { params: groupListParams(params) })),
    options: (params?: GroupOptionParams) => unwrap<GroupOptionSummary[]>(http.get('/groups/options', { params: groupOptionParams(params) })),
    accountOptions: (params?: GroupOptionParams) => unwrap<AccountGroupOptionSummary[]>(http.get('/groups/account-options', { params: groupOptionParams(params) })),
    create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.post('/groups', payload, { params })),
    update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.patch(`/groups/${id}`, payload, { params })),
    delete: (id: string, params?: ListParams) => http.delete(`/groups/${id}`, { params })
  },
  myGroups: {
    list: () => unwrap<GroupSummary[]>(http.get('/my-groups')),
    listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/my-groups', { params: groupListParams(params, false) })),
    options: (params?: Pick<GroupOptionParams, 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault'>) => unwrap<GroupOptionSummary[]>(http.get('/my-groups/options', { params: groupOptionParams(params, false) })),
    accountOptions: (params?: Pick<GroupOptionParams, 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault'>) => unwrap<AccountGroupOptionSummary[]>(http.get('/my-groups/account-options', { params: groupOptionParams(params, false) })),
    create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/my-groups', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<GroupSummary>(http.patch(`/my-groups/${id}`, payload)),
    delete: (id: string) => http.delete(`/my-groups/${id}`)
  },
  systemTeams: {
    list: (params?: TeamListParams) => unwrap<SystemTeamListResult>(http.get('/system-teams', { params: teamListParams(params) })),
    create: (payload: { name: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.post('/system-teams', payload)),
    update: (id: string, payload: { name?: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamSummary>(http.patch(`/system-teams/${id}`, payload)),
    addMembers: (id: string, payload: { systemAccountIds: string[] }) => unwrap<SystemTeamSummary>(http.post(`/system-teams/${id}/members`, payload)),
    removeMember: (id: string, memberId: string) => unwrap<SystemTeamSummary>(http.delete(`/system-teams/${id}/members/${memberId}`))
  },
  myTeams: {
    list: (params?: Omit<TeamListParams, 'systemAccountId'>) => unwrap<SystemTeamListResult>(http.get('/my-teams', { params: teamListParams(params, false) }))
  },
  authorizations: {
    list: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationSummary[]>(http.get('/authorizations', { params })),
    listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/authorizations', { params })),
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
    usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/authorizations/${id}/usage`, { params })),
    teamUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationTeamUsageOverview>(http.get('/authorizations/usage/team-details', { params })),
    userUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationUserUsageOverview>(http.get('/authorizations/usage/user-details', { params }))
  },
  myAuthorizations: {
    list: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationSummary[]>(http.get('/my-authorizations', { params: stripSystemAccountParam(params) })),
    listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/my-authorizations', { params: stripSystemAccountParam(params) })),
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
    usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/my-authorizations/${id}/usage`, { params: stripSystemAccountParam(params) })),
    teamUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationTeamUsageOverview>(http.get('/my-authorizations/usage/team-details', { params: stripSystemAccountParam(params) })),
    userUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationUserUsageOverview>(http.get('/my-authorizations/usage/user-details', { params: stripSystemAccountParam(params) }))
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
    list: (params?: ProxyListParams) => unwrap<ProxyProfileListResult>(http.get('/proxies', { params })),
    options: (params?: Pick<ProxyListParams, 'keyword' | 'limit'>) => unwrap<ProxyProfileOptionSummary[]>(http.get('/proxies/options', { params })),
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
    payload: (id: string, payloadId: string, params?: AuditLogPayloadParams) => unwrap<AuditLogPayloadDetail>(http.get(`/audit-logs/${id}/payloads/${payloadId}`, { params, ...noTimeout }))
  },
  runtimeLogs: {
    list: (params?: RuntimeLogListParams) => unwrap<RuntimeLogSearchResult>(http.get('/runtime-logs', { params })),
    facets: () => unwrap<RuntimeLogFacets>(http.get('/runtime-logs/facets')),
    detail: (id: string) => unwrap<RuntimeLogSummary>(http.get(`/runtime-logs/${id}`)),
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
    accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/stats/account-usage', { params: accountUsageStatsParams(params) })),
    aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params) })),
    aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceOverview>(http.get('/stats/ai-performance', { params: aiPerformanceParams(params) })),
    systemMetrics: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>) => unwrap<SystemMetricsOverview>(http.get('/stats/system-metrics', { params }))
  },
  tableMonitor: {
    overview: (params?: TableMonitorOverviewParams) => unwrap<TableStorageOverview>(http.get('/table-monitor/overview', { params })),
    history: (params: TableMonitorHistoryParams) => unwrap<TableStorageSnapshotSummary[]>(http.get('/table-monitor/history', { params })),
    apiKeyCleanupTargets: (params?: TableMonitorCleanupTargetsParams) => unwrap<ApiKeyRecordCleanupQueueTarget[]>(http.get('/table-monitor/record-maintenance/api-key-cleanup-targets', { params })),
    cleanupUsageRecords: (payload: UsageRecordsCleanupPayload) => unwrap<UsageRecordsCleanupResult>(http.post('/table-monitor/usage-records/cleanup', payload, noTimeout))
  },
  modelChecks: {
    options: () => unwrap<ModelCheckOptions>(http.get('/model-checks/options')),
    run: (payload: ModelCheckRunPayload) => unwrap<ModelCheckRunDetail>(http.post('/model-checks/run', payload, noTimeout)),
    runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions) => runModelCheckStream('/model-checks/run/stream', payload, options),
    list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/model-checks/runs', { params: modelCheckRunListParams(params) })),
    detail: (id: string) => unwrap<ModelCheckRunDetail>(http.get(`/model-checks/runs/${id}`))
  },
  myModelChecks: {
    options: () => unwrap<ModelCheckOptions>(http.get('/my-model-checks/options')),
    run: (payload: ModelCheckRunPayload) => unwrap<ModelCheckRunDetail>(http.post('/my-model-checks/run', payload, noTimeout)),
    runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions) => runModelCheckStream('/my-model-checks/run/stream', payload, options),
    list: (params?: ModelCheckRunListParams) => unwrap<ModelCheckRunListResult>(http.get('/my-model-checks/runs', { params: modelCheckRunListParams(params) })),
    detail: (id: string) => unwrap<ModelCheckRunDetail>(http.get(`/my-model-checks/runs/${id}`))
  },
  myStats: {
    usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/my-stats/usage-overview', { params: stripSystemAccountParam(params) })),
    accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/my-stats/account-usage', { params: accountUsageStatsParams(params, false) })),
    aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/my-stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params, false) })),
    aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceOverview>(http.get('/my-stats/ai-performance', { params: aiPerformanceParams(params, false) }))
  },
  featureRules: {
    upstreamErrorFeatureRules: () => unwrap<UpstreamErrorFeatureRuleCatalogItem[]>(http.get('/feature-rules/upstream-error-feature-rules'))
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
  if (params.groupId) output.groupId = params.groupId
  if (params.type && params.type !== 'all') output.type = params.type
  const status = joinedListParam(params.status)
  if (status) output.status = status
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  if (params.sorts?.length) {
    output.sorts = params.sorts.map((sort) => `${sort.field}:${sort.order}`).join(',')
  }
  return output
}

function accountOptionsParams(params?: AccountListParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  if (params.keyword) output.keyword = params.keyword
  if (params.groupId) output.groupId = params.groupId
  if (params.type && params.type !== 'all') output.type = params.type
  const status = joinedListParam(params.status)
  if (status) output.status = status
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  return Object.keys(output).length ? output : undefined
}

function joinedListParam(value?: string | string[]): string | undefined {
  const values = Array.isArray(value) ? value : value ? [value] : []
  const normalizedValues = values
    .flatMap((item) => item.split(','))
    .map((item) => item.trim())
    .filter((item) => item && item !== 'all')
  return normalizedValues.length ? [...new Set(normalizedValues)].join(',') : undefined
}

function groupListParams(params?: GroupListParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

function groupOptionParams(params?: GroupOptionParams | Pick<GroupOptionParams, 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault'>, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && 'systemAccountId' in params && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.providerCode?.trim()) output.providerCode = params.providerCode.trim()
  if (params.limit) output.limit = params.limit
  if (typeof params.manageableOnly === 'boolean') output.manageableOnly = params.manageableOnly
  if (typeof params.preferDefault === 'boolean') output.preferDefault = params.preferDefault
  return Object.keys(output).length ? output : undefined
}

function teamListParams(params?: TeamListParams | Omit<TeamListParams, 'systemAccountId'>, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && 'systemAccountId' in params && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  return Object.keys(output).length ? output : undefined
}

function authorizationPrincipalOptionsParams(params?: AuthorizationPrincipalOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

function systemAccountOptionsParams(params?: SystemAccountOptionsParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

function systemAccountListParams(params?: SystemAccountListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  return Object.keys(output).length ? output : undefined
}

function accountUsageStatsParams(params?: AccountUsageStatsParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.limit) output.limit = params.limit
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.startDate) output.startDate = params.startDate
  if (params.endDate) output.endDate = params.endDate
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  if (params.schedulable && params.schedulable !== 'all') output.schedulable = params.schedulable
  return Object.keys(output).length ? output : undefined
}

function aiPerformanceParams(params?: AiPerformanceParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.startDate) output.startDate = params.startDate
  if (params.endDate) output.endDate = params.endDate
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  return Object.keys(output).length ? output : undefined
}

function aiPerformanceAccountOptionsParams(params?: AiPerformanceAccountOptionsParams, includeSystemAccount = true): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (includeSystemAccount && params.systemAccountId) output.systemAccountId = params.systemAccountId
  if (params.keyword?.trim()) output.keyword = params.keyword.trim()
  if (params.accountIds?.length) output.accountIds = params.accountIds.join(',')
  if (params.limit) output.limit = params.limit
  return Object.keys(output).length ? output : undefined
}

function modelCheckRunListParams(params?: ModelCheckRunListParams): Record<string, unknown> | undefined {
  if (!params) return undefined
  const output: Record<string, unknown> = {}
  if (params.page) output.page = params.page
  if (params.pageSize) output.pageSize = params.pageSize
  if (params.targetType) output.targetType = params.targetType
  if (params.targetId?.trim()) output.targetId = params.targetId.trim()
  if (params.model) output.model = params.model
  if (params.level) output.level = params.level
  if (params.status) output.status = params.status
  return Object.keys(output).length ? output : undefined
}
