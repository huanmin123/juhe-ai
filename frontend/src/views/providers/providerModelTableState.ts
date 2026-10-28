import type { ProviderModelPricing } from '@/types/domain'

import {
  formatApiProtocol,
  formatModelCategory,
  formatModelModalities,
  formatModelScope,
  getModelCategory,
  modelCategoryLabels,
  modelCategoryOrder,
  modelToolsByProtocolMatchKeyword,
  type ModelCategoryKey
} from './providerModelFormatters'

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
      || modelToolsByProtocolMatchKeyword(item, keyword)
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
