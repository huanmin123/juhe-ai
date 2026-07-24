import type { ProviderModelPricing } from '@/types/domain'

import {
  formatApiProtocol,
  formatModelCategory,
  formatModelModalities,
  formatModelScope,
  formatModelTools,
  getModelCategory,
  modelCatalogDisplaySections,
  modelCategoryLabels,
  modelCategoryOrder,
  type ModelCategoryKey
} from './providerModelFormatters'

export const baseModelColumns = [
  { title: '模型', key: 'model', width: 260 },
  { title: '范围', key: 'scope', width: 100 },
  { title: '状态', key: 'status', width: 90 },
  { title: '发布时间', key: 'releaseDate', width: 120 },
  { title: '用途', key: 'category', width: 120 },
  { title: '接口协议', key: 'protocols', width: 230 },
  { title: '操作', key: 'actions', width: 116, fixed: 'right' }
]

export function buildProviderModelColumns(_category: ModelCategoryKey, rows: ProviderModelPricing[]) {
  const visibleKeys = new Set(['model', 'scope', 'status', 'releaseDate', 'category', 'protocols', 'actions'])

  const commonColumns = baseModelColumns.filter((column) => column.key !== 'actions' && visibleKeys.has(column.key))
  const displayColumns = new Map<string, { title: string; key: string; width: number; catalogDisplaySectionKey: string }>()
  for (const row of rows) {
    for (const section of modelCatalogDisplaySections(row)) {
      if (displayColumns.has(section.key)) continue
      displayColumns.set(section.key, {
        title: section.label,
        key: `catalogDisplay:${section.key}`,
        width: 320,
        catalogDisplaySectionKey: section.key
      })
    }
  }

  return [
    ...commonColumns,
    ...displayColumns.values(),
    ...baseModelColumns.filter((column) => column.key === 'actions')
  ]
}

export function buildModelCategoryTabs(models: ProviderModelPricing[]) {
  const counts = new Map<ModelCategoryKey, number>()
  for (const key of modelCategoryOrder) {
    counts.set(key, 0)
  }

  for (const item of models) {
    const key = getModelCategory(item)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }

  return modelCategoryOrder
    .filter((key) => (counts.get(key) ?? 0) > 0)
    .map((key) => ({
      key,
      label: `${modelCategoryLabels[key]} (${counts.get(key) ?? 0})`
    }))
}

export function filterProviderModelsByKeyword(models: ProviderModelPricing[], keywordValue: string): ProviderModelPricing[] {
  const keyword = keywordValue.trim().toLowerCase()
  return models.filter((item) => {
    const keywordMatches = !keyword
      || item.model.toLowerCase().includes(keyword)
      || formatModelCategory(item).toLowerCase().includes(keyword)
      || (item.supportedApiProtocols ?? []).some((protocol) => formatApiProtocol(protocol).toLowerCase().includes(keyword))
      || formatModelModalities(item.inputModalities).toLowerCase().includes(keyword)
      || formatModelModalities(item.outputModalities).toLowerCase().includes(keyword)
      || formatModelTools(item.supportedTools).toLowerCase().includes(keyword)
    return keywordMatches
  })
}

export function buildConfigurationTemplateOptions(
  models: ProviderModelPricing[],
  currentModel: string,
  category: ModelCategoryKey,
  targetScope: 'personal' | 'global' = 'personal'
) {
  const normalizedCurrentModel = currentModel.trim()
  return models
    .filter((item) => item.model.trim() !== normalizedCurrentModel)
    .filter((item) => getModelCategory(item) === category)
    .filter((item) => (item.status ?? 'active') === 'active')
    .filter((item) => targetScope !== 'global' || item.scope !== 'personal')
    .filter((item) => Boolean(item.id))
    .map((item) => ({
      value: item.id as string,
      label: `${item.model}（${formatModelScope(item.scope)}）`
    }))
}
