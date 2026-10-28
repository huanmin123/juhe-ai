import type { ProviderPlazaMeta, ProviderModelPricing } from '@/types/domain'

import { getModelCategory, type ModelCategoryKey } from './providerModelFormatters'
import { filterProviderModelsByKeyword } from './providerModelTableState'

/** 广场厂商标签（设计 §4.1）：「全部」+ providers 摘要逐家带数量；管理面 disabled 供应商照常出标签，由 UI 灰显。 */
export interface PlazaVendorTab {
  /** 'all' 或供应商 code；'all' 由 isAllVendor 判定。 */
  code: string
  name: string
  count: number
  enabled: boolean
}

export const plazaAllVendors = 'all'

export function isAllVendor(code: string): boolean {
  return code === plazaAllVendors
}

/** 厂商标签构建：顺序跟随后端 providers 摘要（启用注册表顺序）；数量为该供应商目录行数。
 * items 中出现的 providerCode 必然来自 providers 摘要窗口（聚合接口同源），不产生幽灵标签。 */
export function buildPlazaVendorTabs(providers: ProviderPlazaMeta[], items: ProviderModelPricing[]): PlazaVendorTab[] {
  const counts = new Map<string, number>()
  for (const item of items) {
    counts.set(item.providerCode, (counts.get(item.providerCode) ?? 0) + 1)
  }
  return [
    { code: plazaAllVendors, name: '全部', count: items.length, enabled: true },
    ...providers.map((provider) => ({
      code: provider.code,
      name: provider.name,
      count: counts.get(provider.code) ?? 0,
      enabled: provider.enabled
    }))
  ]
}

/** providers 摘要映射：卡片/抽屉供应商徽标与「默认检查」徽标按 item.providerCode 查表。 */
export function buildPlazaProvidersMap(providers: ProviderPlazaMeta[]): Map<string, ProviderPlazaMeta> {
  return new Map(providers.map((provider) => [provider.code, provider]))
}

export interface PlazaFilterOptions {
  /** 厂商筛选：'all' 不过滤。 */
  vendor: string
  category: ModelCategoryKey
  keyword: string
}

/** 广场筛选管线（设计 §4.1，客户端组合过滤，不发起请求）：
 * 厂商 → 用途 → 关键词（关键词命中判定复用目录弹窗既有口径：模型名/用途/协议/模态/工具）。 */
export function filterPlazaModels(items: ProviderModelPricing[], options: PlazaFilterOptions): ProviderModelPricing[] {
  const vendorMatched = isAllVendor(options.vendor)
    ? items
    : items.filter((item) => item.providerCode === options.vendor)
  const categoryMatched = vendorMatched.filter((item) => getModelCategory(item) === options.category)
  return filterProviderModelsByKeyword(categoryMatched, options.keyword)
}

/** 分页越界收敛（设计 §7）：筛选/删除后 page 超出尾页时收敛到最后一页，不渲染空页。 */
export function clampPlazaPage(page: number, pageSize: number, total: number): number {
  const maxPage = Math.max(1, Math.ceil(total / pageSize))
  return Math.min(Math.max(1, page), maxPage)
}
