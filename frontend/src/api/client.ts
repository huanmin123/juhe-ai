import axios from 'axios'

import type {
  AccountSummary,
  AccountTestResult,
  ApiKeySummary,
  CaptchaChallengeSummary,
  CreatedApiKey,
  CurrentUserSummary,
  ErrorPolicySummary,
  GlobalSettings,
  GroupSummary,
  OpenAIAuthURLResult,
  ProviderDefinition,
  ProviderModelPricing,
  ProxyProfileSummary,
  SystemSettings,
  SystemAccountSummary,
  SystemMetricsOverview,
  UsageStatsOverview,
  UsageRecordSummary
} from '@/types/domain'

interface ApiResponse<T> {
  data: T
  message?: string
}

interface ListParams {
  systemAccountId?: string
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
  providers: {
    list: () => unwrap<ProviderDefinition[]>(http.get('/providers')),
    models: (code: string) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`))
  },
  errorPolicies: {
    list: () => unwrap<ErrorPolicySummary[]>(http.get('/error-policies'))
  },
  accounts: {
    list: (params?: ListParams) => unwrap<AccountSummary[]>(http.get('/accounts', { params })),
    create: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.patch(`/accounts/${id}`, payload)),
    test: (id: string, payload?: { model?: string; prompt?: string }) => unwrap<AccountTestResult>(http.post(`/accounts/${id}/test`, payload ?? {}, { timeout: 130000 })),
    delete: (id: string) => http.delete(`/accounts/${id}`)
  },
  groups: {
    list: (params?: ListParams) => unwrap<GroupSummary[]>(http.get('/groups', { params })),
    create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/groups', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<GroupSummary>(http.patch(`/groups/${id}`, payload)),
    delete: (id: string) => http.delete(`/groups/${id}`)
  },
  apiKeys: {
    list: (params?: ListParams) => unwrap<ApiKeySummary[]>(http.get('/api-keys', { params })),
    create: (payload: Record<string, unknown>) => unwrap<CreatedApiKey>(http.post('/api-keys', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ApiKeySummary>(http.patch(`/api-keys/${id}`, payload)),
    delete: (id: string) => http.delete(`/api-keys/${id}`)
  },
  openaiOAuth: {
    authUrl: (payload: Record<string, unknown>) => unwrap<OpenAIAuthURLResult>(http.post('/openai-oauth/auth-url', payload)),
    createFromCode: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-code', payload)),
    createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-refresh-token', payload))
  },
  proxies: {
    list: (params?: ListParams) => unwrap<ProxyProfileSummary[]>(http.get('/proxies', { params })),
    create: (payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.post('/proxies', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.patch(`/proxies/${id}`, payload)),
    delete: (id: string) => http.delete(`/proxies/${id}`)
  },
  usageRecords: {
    list: (params?: ListParams) => unwrap<UsageRecordSummary[]>(http.get('/usage-records', { params }))
  },
  stats: {
    usageOverview: () => unwrap<UsageStatsOverview>(http.get('/stats/usage-overview')),
    systemMetrics: () => unwrap<SystemMetricsOverview>(http.get('/stats/system-metrics'))
  },
  settings: {
    public: () => unwrap<GlobalSettings>(http.get('/settings/public')),
    global: () => unwrap<GlobalSettings>(http.get('/settings/global')),
    updateGlobal: (payload: GlobalSettings) => unwrap<GlobalSettings>(http.patch('/settings/global', payload)),
    get: () => unwrap<SystemSettings>(http.get('/settings')),
    update: (payload: SystemSettings) => unwrap<SystemSettings>(http.patch('/settings', payload))
  }
}
