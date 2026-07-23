import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyListResult,
  ApiKeyQuotaLimits,
  ApiKeySecretResult,
  ApiKeySummary,
  CreatedApiKey
} from '@/types/domain'
import type { ApiKeyListParams, ListParams } from '../contracts'
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
  create: (payload: ApiKeyMutationPayload, params?: ListParams) => unwrap<CreatedApiKey>(http.post('/api-keys', payload, { params })),
  update: (id: string, payload: ApiKeyMutationPayload, params?: ListParams) => unwrap<ApiKeySummary>(http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })),
  secret: (id: string, params?: ListParams) => unwrap<ApiKeySecretResult>(http.get(`/api-keys/${encodeURIComponent(id)}/secret`, { params })),
  refreshKey: (id: string, params?: ListParams) => unwrap<CreatedApiKey>(http.post(`/api-keys/${encodeURIComponent(id)}/refresh-key`, {}, { params })),
  delete: (id: string, params?: ListParams) => http.delete(`/api-keys/${encodeURIComponent(id)}`, { params })
}

export const myApiKeysApi = {
  list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/my-api-keys', { params: stripSystemAccountParam(params) })),
  create: (payload: ApiKeyMutationPayload) => unwrap<CreatedApiKey>(http.post('/my-api-keys', payload)),
  update: (id: string, payload: ApiKeyMutationPayload) => unwrap<ApiKeySummary>(http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)),
  secret: (id: string) => unwrap<ApiKeySecretResult>(http.get(`/my-api-keys/${encodeURIComponent(id)}/secret`)),
  refreshKey: (id: string) => unwrap<CreatedApiKey>(http.post(`/my-api-keys/${encodeURIComponent(id)}/refresh-key`, {})),
  delete: (id: string) => http.delete(`/my-api-keys/${encodeURIComponent(id)}`)
}
