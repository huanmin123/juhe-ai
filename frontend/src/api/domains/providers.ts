import type {
  ProviderDefinition,
  ProviderModelOption,
  ProviderModelPricing,
  ProviderModelsParams,
  ProviderModelUpsertPayload
} from '@/types/domain'
import type { ListParams } from '../contracts'
import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'
import { http, unwrap } from '../http'

const providerOptionsCache = createShortLivedRequestCache<ProviderDefinition[]>({ ttlMs: 30_000, maxEntries: 2 })
const providerModelOptionsCache = createShortLivedRequestCache<ProviderModelOption[]>({ ttlMs: 30_000, maxEntries: 2 })

export interface ProviderModelOptionsParams extends ListParams {
  protocol?: 'openai'
}

export const providersApi = {
  list: () => unwrap<ProviderDefinition[]>(http.get('/providers')),
  options: () => providerOptionsCache.load('providers/options', () => unwrap<ProviderDefinition[]>(http.get('/providers/options'))),
  modelOptions: (params?: ProviderModelOptionsParams) => providerModelOptionsCache.load(`providers/models/options:${params?.systemAccountId ?? ''}:${params?.protocol ?? 'all'}`, () => unwrap<ProviderModelOption[]>(http.get('/providers/models/options', { params }))),
  models: (code: string, params?: ProviderModelsParams) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`, { params })),
  createModel: async (code: string, payload: ProviderModelUpsertPayload) => {
    const model = await unwrap<ProviderModelPricing>(http.post(`/providers/${code}/models`, payload))
    providerModelOptionsCache.clear()
    return model
  },
  updateModel: async (code: string, id: string, payload: Partial<ProviderModelUpsertPayload>) => {
    const model = await unwrap<ProviderModelPricing>(http.patch(`/providers/${code}/models/${id}`, payload))
    providerModelOptionsCache.clear()
    return model
  },
  deleteModel: async (code: string, id: string) => {
    const result = await unwrap<{ deleted: boolean }>(http.delete(`/providers/${code}/models/${id}`))
    providerModelOptionsCache.clear()
    return result
  }
}
