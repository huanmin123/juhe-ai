import type {
  ProviderDefinition,
  ProviderListItem,
  ProviderModelOption,
  ProviderModelCapabilities,
  ProviderModelMutationResult,
  ProviderModelPlaza,
  ProviderOption,
  ProviderModelPatchPayload,
  ProviderModelPricing,
  ProviderModelsParams,
  ProviderModelUpsertPayload
} from '@/types/domain'
import type { ListParams } from '../contracts'
import { http, unwrap } from '../http'

/** `/providers/models/plaza` 查询参数：参数序列化与 models 方法同口径（空值不传，'all' 归一由调用方完成）。 */
export interface ProviderModelPlazaParams {
  /** 管理面请求（单值 + admin 角色判定）；缺省为用户面窗口，后端把归属 pin 到本人。 */
  viewScope?: 'admin'
  /** 管理面归属过滤；非管理面请求后端忽略并恒 pin 本人。 */
  systemAccountId?: string
}

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
  /** 模型广场聚合读：providers 摘要 + 逐供应商目录行（跨供应商不合并），一次性加载 + 客户端筛选分页。 */
  modelPlaza: (params?: ProviderModelPlazaParams) => unwrap<ProviderModelPlaza>(http.get('/providers/models/plaza', { params })),
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
