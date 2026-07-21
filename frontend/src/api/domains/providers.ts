import type {
  ProviderDefinition,
  ProviderDefaultHealthCheckModelResult,
  ProviderModelOption,
  ProviderModelCapabilities,
  ProviderOption,
  ProviderModelPricing,
  ProviderModelsParams,
  ProviderModelUpsertPayload
} from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

export interface ProviderModelOptionsParams extends ListParams {
  providerCode?: string
  protocol?: 'openai' | 'anthropic' | 'gemini'
  keyword?: string
  limit?: number
  selectedIds?: string[]
}

export const providersApi = {
  list: (params?: ListParams) => unwrap<ProviderDefinition[]>(http.get('/providers', { params })),
  options: (params?: ListParams) => unwrap<ProviderOption[]>(http.get('/providers/options', { params })),
  definitions: (params?: ListParams) => unwrap<ProviderDefinition[]>(http.get('/providers/definitions', { params })),
  modelOptions: (params?: ProviderModelOptionsParams) => unwrap<ProviderModelOption[]>(http.get('/providers/models/options', { params })),
  modelCapabilities: (code: string, modelId: string, params?: ListParams) => unwrap<ProviderModelCapabilities>(http.get(`/providers/${code}/models/${encodeURIComponent(modelId)}/capabilities`, { params })),
  models: (code: string, params?: ProviderModelsParams) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`, { params })),
  setDefaultHealthCheckModel: async (code: string, model: string, params?: ListParams) => {
    return unwrap<ProviderDefaultHealthCheckModelResult>(http.put(`/providers/${code}/default-health-check-model`, { model }, { params }))
  },
  createModel: async (code: string, payload: ProviderModelUpsertPayload, params?: ListParams) => {
    return unwrap<ProviderModelPricing>(http.post(`/providers/${code}/models`, payload, { params }))
  },
  updateModel: async (code: string, id: string, payload: Partial<ProviderModelUpsertPayload>) => {
    return unwrap<ProviderModelPricing>(http.patch(`/providers/${code}/models/${id}`, payload))
  },
  deleteModel: async (code: string, id: string) => {
    return unwrap<{ deleted: boolean }>(http.delete(`/providers/${code}/models/${id}`))
  }
}
