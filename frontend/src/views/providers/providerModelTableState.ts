import type { ProviderModelPricing } from '@/types/domain'

import {
  formatApiProtocol,
  formatModelCategory,
  formatModelModalities,
  formatModelScope,
  formatModelTools,
  getModelCategory,
  hasAnyNumber,
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
  { title: '模态与工具', key: 'modalities', width: 280 },
  { title: '服务等级', key: 'serviceTiers', width: 280 },
  { title: '思考级别', key: 'reasoningEfforts', width: 360 },
  { title: '计费', key: 'prices', width: 230 },
  { title: '缓存写入', key: 'cacheWrite', width: 180 },
  { title: '图片 token 价格', key: 'imageTokenPrice', width: 180 },
  { title: '音频 token 价格', key: 'audioTokenPrice', width: 180 },
  { title: '每张价格', key: 'imageUnitPrice', width: 130 },
  { title: '上下文 / 输入 / 输出', key: 'context', width: 210 },
  { title: '操作', key: 'actions', width: 116, fixed: 'right' }
]

export function buildProviderModelColumns(category: ModelCategoryKey, rows: ProviderModelPricing[]) {
  const visibleKeys = new Set(['model', 'scope', 'status', 'releaseDate', 'category', 'protocols', 'actions'])

  if (category === 'text') {
    visibleKeys.add('serviceTiers')
    visibleKeys.add('reasoningEfforts')
    visibleKeys.add('prices')
    visibleKeys.add('cacheWrite')
  }
  if (category === 'image') {
    visibleKeys.add('imageTokenPrice')
    visibleKeys.add('imageUnitPrice')
  }
  if (category === 'audio') {
    visibleKeys.add('audioTokenPrice')
  }
  if (rows.some((item) => hasAnyNumber(item.maxInputTokens, item.contextWindowTokens, item.maxOutputTokens))) {
    visibleKeys.add('context')
  }
  if (rows.some((item) => item.inputModalities?.length || item.outputModalities?.length || item.supportedTools?.length)) {
    visibleKeys.add('modalities')
  }

  return baseModelColumns.filter((column) => visibleKeys.has(column.key))
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
