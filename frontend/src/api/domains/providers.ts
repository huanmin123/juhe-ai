import type {
  ProviderDefinition,
  ProviderListItem,
  ProviderModelOption,
  ProviderModelCapabilities,
  ProviderModelMutationResult,
  ProviderOption,
  ProviderModelPatchPayload,
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
  /** 目录 mode 白名单（词表六值）；缺省不过滤（服务端按 mode IN 过滤）。 */
  modes?: string[]
}

/**
 * `/providers/models/options` 的返回列表（T25）：envelope data 数组 + 服务端
 * 截断元数据。`total` 是当前筛选（keyword/modes/scope）下的可见候选总数
 * （limit 截断前），`hasMore` = total > 返回条数。旧响应或测试 mock 未携带
 * 元数据时两者为 undefined，消费方按「无法判定 → 不提示」处理。
 */
export type ProviderModelOptionList = ProviderModelOption[] & {
  total?: number
  hasMore?: boolean
}

export const providersApi = {
  listItems: (params?: ListParams) => unwrap<ProviderListItem[]>(http.get('/providers/list', { params })),
  list: (params?: ListParams) => unwrap<ProviderDefinition[]>(http.get('/providers', { params })),
  detail: (code: string, params?: ListParams) => unwrap<ProviderDefinition>(http.get(`/providers/${code}`, { params })),
  options: (params?: ListParams) => unwrap<ProviderOption[]>(http.get('/providers/options', { params })),
  definitions: (params?: ListParams) => unwrap<ProviderDefinition[]>(http.get('/providers/definitions', { params })),
  // T25：envelope data 保持数组，total/hasMore 挂在返回列表上透出（旧调用方
  // 把返回值当数组消费不受影响；元数据缺失视为无法判定截断）。
  modelOptions: async (params?: ProviderModelOptionsParams) => {
    const response = await http.get<{ data: ProviderModelOption[]; total?: number; hasMore?: boolean }>('/providers/models/options', { params })
    const list = response.data.data as ProviderModelOptionList
    if (typeof response.data.total === 'number') list.total = response.data.total
    if (typeof response.data.hasMore === 'boolean') list.hasMore = response.data.hasMore
    return list
  },
  modelOptionProviderCodes: (params?: ProviderModelOptionsParams) => unwrap<{ providerCodes: string[] }>(http.get('/providers/models/options/provider-codes', { params })),
  modelCapabilities: (code: string, modelId: string, params?: ListParams) => unwrap<ProviderModelCapabilities>(http.get(`/providers/${code}/models/${encodeURIComponent(modelId)}/capabilities`, { params })),
  models: (code: string, params?: ProviderModelsParams) => unwrap<ProviderModelPricing[]>(http.get(`/providers/${code}/models`, { params })),
  createModel: async (code: string, payload: ProviderModelUpsertPayload, params?: ListParams) => {
    return unwrap<ProviderModelPricing>(http.post(`/providers/${code}/models`, payload, { params }))
  },
  updateModel: async (code: string, id: string, payload: ProviderModelPatchPayload) => {
    return unwrap<ProviderModelMutationResult>(http.patch(`/providers/${code}/models/${id}`, payload))
  },
  deleteModel: async (code: string, id: string) => {
    return unwrap<{ deleted: boolean }>(http.delete(`/providers/${code}/models/${id}`))
  }
}
