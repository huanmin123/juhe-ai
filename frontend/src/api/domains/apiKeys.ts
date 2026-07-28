import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyListResult,
  ApiKeyMutationResult,
  ApiKeyQuotaLimits,
  ApiKeySecretResult,
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

export interface ApiKeyUpdatePayload extends ApiKeyMutationPayload {
  expectedRevision: string
}

export const apiKeysApi = {
  list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/api-keys', { params })),
  create: (payload: ApiKeyMutationPayload, params?: ListParams) => unwrap<CreatedApiKey>(http.post('/api-keys', payload, { params })),
  update: (id: string, payload: ApiKeyUpdatePayload, params?: ListParams) => unwrap<ApiKeyMutationResult>(http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })),
  secret: (id: string, params?: ListParams) => unwrap<ApiKeySecretResult>(http.get(`/api-keys/${encodeURIComponent(id)}/secret`, { params })),
  refreshKey: (id: string, params?: ListParams) => unwrap<CreatedApiKey>(http.post(`/api-keys/${encodeURIComponent(id)}/refresh-key`, {}, { params })),
  delete: (id: string, params?: ListParams) => http.delete(`/api-keys/${encodeURIComponent(id)}`, { params })
}

export const myApiKeysApi = {
  list: (params?: ApiKeyListParams) => unwrap<ApiKeyListResult>(http.get('/my-api-keys', { params: stripSystemAccountParam(params) })),
  create: (payload: ApiKeyMutationPayload) => unwrap<CreatedApiKey>(http.post('/my-api-keys', payload)),
  update: (id: string, payload: ApiKeyUpdatePayload) => unwrap<ApiKeyMutationResult>(http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)),
  secret: (id: string) => unwrap<ApiKeySecretResult>(http.get(`/my-api-keys/${encodeURIComponent(id)}/secret`)),
  refreshKey: (id: string) => unwrap<CreatedApiKey>(http.post(`/my-api-keys/${encodeURIComponent(id)}/refresh-key`, {})),
  delete: (id: string) => http.delete(`/my-api-keys/${encodeURIComponent(id)}`)
}
