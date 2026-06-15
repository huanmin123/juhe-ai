import type {
  ProviderDefinition,
  ProviderModelOption,
  ProviderModelPricing,
  ProviderModelsParams,
  ProviderModelUpsertPayload
} from '@/types/domain'
import { http, unwrap } from '../http'

export const providersApi = {
  list: () => unwrap<ProviderDefinition[]>(http.get('/providers')),
  options: () => unwrap<ProviderDefinition[]>(http.get('/providers/options')),
  modelOptions: () => unwrap<ProviderModelOption[]>(http.get('/providers/models/options')),
  models: (code: string, params?: ProviderModelsParams) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`, { params })),
  createModel: (code: string, payload: ProviderModelUpsertPayload) => unwrap<ProviderModelPricing>(http.post(`/providers/${code}/models`, payload)),
  updateModel: (code: string, id: string, payload: Partial<ProviderModelUpsertPayload>) => unwrap<ProviderModelPricing>(http.patch(`/providers/${code}/models/${id}`, payload)),
  deleteModel: (code: string, id: string) => unwrap<{ deleted: boolean }>(http.delete(`/providers/${code}/models/${id}`))
}
