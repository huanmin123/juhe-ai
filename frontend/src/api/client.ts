import axios from 'axios'

import type {
  AccountSummary,
  AccountTestResult,
  ApiKeySummary,
  CreatedApiKey,
  ErrorPolicySummary,
  GroupSummary,
  OpenAIAuthURLResult,
  ProviderDefinition,
  ProviderModelPricing,
  ProxyProfileSummary,
  SystemSettings,
  UsageRecordSummary
} from '@/types/domain'

interface ApiResponse<T> {
  data: T
  message?: string
}

const http = axios.create({
  baseURL: '/api',
  timeout: 15000
})

async function unwrap<T>(request: Promise<{ data: ApiResponse<T> }>): Promise<T> {
  const response = await request
  return response.data.data
}

export const api = {
  providers: {
    list: () => unwrap<ProviderDefinition[]>(http.get('/providers')),
    models: (code: string) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`))
  },
  errorPolicies: {
    list: () => unwrap<ErrorPolicySummary[]>(http.get('/error-policies'))
  },
  accounts: {
    list: () => unwrap<AccountSummary[]>(http.get('/accounts')),
    create: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/accounts', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<AccountSummary>(http.patch(`/accounts/${id}`, payload)),
    test: (id: string, payload?: { model?: string; prompt?: string }) => unwrap<AccountTestResult>(http.post(`/accounts/${id}/test`, payload ?? {}, { timeout: 130000 })),
    delete: (id: string) => http.delete(`/accounts/${id}`)
  },
  groups: {
    list: () => unwrap<GroupSummary[]>(http.get('/groups')),
    create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/groups', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<GroupSummary>(http.patch(`/groups/${id}`, payload)),
    delete: (id: string) => http.delete(`/groups/${id}`)
  },
  apiKeys: {
    list: () => unwrap<ApiKeySummary[]>(http.get('/api-keys')),
    create: (payload: Record<string, unknown>) => unwrap<CreatedApiKey>(http.post('/api-keys', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ApiKeySummary>(http.patch(`/api-keys/${id}`, payload)),
    delete: (id: string) => http.delete(`/api-keys/${id}`)
  },
  openaiOAuth: {
    authUrl: (payload: Record<string, unknown>) => unwrap<OpenAIAuthURLResult>(http.post('/openai-oauth/auth-url', payload)),
    createFromCode: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-code', payload)),
    createFromRefreshToken: (payload: Record<string, unknown>) => unwrap<AccountSummary>(http.post('/openai-oauth/create-from-refresh-token', payload)),
    refreshAccount: (id: string) => unwrap<AccountSummary>(http.post(`/openai-oauth/accounts/${id}/refresh`))
  },
  proxies: {
    list: () => unwrap<ProxyProfileSummary[]>(http.get('/proxies')),
    create: (payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.post('/proxies', payload)),
    update: (id: string, payload: Record<string, unknown>) => unwrap<ProxyProfileSummary>(http.patch(`/proxies/${id}`, payload)),
    delete: (id: string) => http.delete(`/proxies/${id}`)
  },
  usageRecords: {
    list: () => unwrap<UsageRecordSummary[]>(http.get('/usage-records'))
  },
  settings: {
    get: () => unwrap<SystemSettings>(http.get('/settings')),
    update: (payload: SystemSettings) => unwrap<SystemSettings>(http.patch('/settings', payload))
  }
}
