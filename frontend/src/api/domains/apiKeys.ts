import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyListResult,
  ApiKeyQuotaLimits,
  ApiKeySecretResult,
  ApiKeySummary,
  ApiKeyUsageResult,
  CreatedApiKey
} from '@/types/domain'
import type { ApiKeyListParams, ApiKeyUsageParams, ListParams } from '../contracts'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

export interface ApiKeyMutationPayload {
  name?: string
  description?: string | null
  routeStrategyId?: string
  status?: 'active' | 'disabled'
  expiresAt?: string | null
  quotaLimits?: ApiKeyQuotaLimits | null
  availabilitySchedule?: ApiKeyAvailabilitySchedule | null
}

export const apiKeysApi = {
  list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/api-keys', { params })),
  usage: (params: ApiKeyUsageParams) => unwrap<ApiKeyUsageResult>(http.get('/api-keys/usage', { params: apiKeyUsageSearchParams(params, true) })),
  create: (payload: ApiKeyMutationPayload, params?: ListParams) => unwrap<CreatedApiKey>(http.post('/api-keys', payload, { params })),
  update: (id: string, payload: ApiKeyMutationPayload, params?: ListParams) => unwrap<ApiKeySummary>(http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })),
  secret: (id: string, params?: ListParams) => unwrap<ApiKeySecretResult>(http.get(`/api-keys/${encodeURIComponent(id)}/secret`, { params })),
  refreshKey: (id: string, params?: ListParams) => unwrap<CreatedApiKey>(http.post(`/api-keys/${encodeURIComponent(id)}/refresh-key`, {}, { params })),
  delete: (id: string, params?: ListParams) => http.delete(`/api-keys/${encodeURIComponent(id)}`, { params })
}

export const myApiKeysApi = {
  list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/my-api-keys', { params: stripSystemAccountParam(params) })),
  usage: (params: ApiKeyUsageParams) => unwrap<ApiKeyUsageResult>(http.get('/my-api-keys/usage', { params: apiKeyUsageSearchParams(params, false) })),
  create: (payload: ApiKeyMutationPayload) => unwrap<CreatedApiKey>(http.post('/my-api-keys', payload)),
  update: (id: string, payload: ApiKeyMutationPayload) => unwrap<ApiKeySummary>(http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)),
  secret: (id: string) => unwrap<ApiKeySecretResult>(http.get(`/my-api-keys/${encodeURIComponent(id)}/secret`)),
  refreshKey: (id: string) => unwrap<CreatedApiKey>(http.post(`/my-api-keys/${encodeURIComponent(id)}/refresh-key`, {})),
  delete: (id: string) => http.delete(`/my-api-keys/${encodeURIComponent(id)}`)
}

function apiKeyUsageSearchParams(params: ApiKeyUsageParams, includeSystemAccount: boolean): URLSearchParams {
  const searchParams = new URLSearchParams()
  if (includeSystemAccount && params.systemAccountId) searchParams.set('systemAccountId', params.systemAccountId)
  for (const id of params.ids) searchParams.append('ids', id)
  return searchParams
}
