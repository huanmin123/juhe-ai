import type { ProviderModelPricing } from '@/types/domain'

export const modelCategoryOrder = ['text', 'image'] as const

export type ModelCategoryKey = typeof modelCategoryOrder[number]

export const modelCategoryLabels: Record<ModelCategoryKey, string> = {
  text: '对话 / 编码',
  image: '图像'
}

type ModelNameCategoryRule = {
  category: ModelCategoryKey
  matches: (model: string) => boolean
}

const modelNameCategoryRules: ModelNameCategoryRule[] = [
  {
    category: 'image',
    matches: (model) => model.startsWith('gpt-image') || model.startsWith('dall-e')
  },
  {
    category: 'text',
    matches: (model) => model.includes('codex')
      || model.startsWith('deepseek-')
      || model.startsWith('deepseek-ai-')
      || model.startsWith('gpt-')
      || model.startsWith('claude-')
      || model.startsWith('o')
  }
]

export function isModelCategoryKey(value: string): value is ModelCategoryKey {
  return (modelCategoryOrder as readonly string[]).includes(value)
}

export function categoryFromModeOrModel(modeValue: string | undefined, modelValue: string): ModelCategoryKey {
  const mode = (modeValue ?? '').trim().toLowerCase()
  if (isModelCategoryKey(mode)) {
    return mode
  }

  const modeCategory = categoryFromModeAlias(mode)
  if (modeCategory) {
    return modeCategory
  }

  const model = modelValue.trim().toLowerCase()
  return modelNameCategoryRules.find((rule) => rule.matches(model))?.category ?? 'text'
}

export function getModelCategoryFromPricing(item: Pick<ProviderModelPricing, 'mode' | 'model'>): ModelCategoryKey {
  return categoryFromModeOrModel(item.mode, item.model)
}

function categoryFromModeAlias(mode: string): ModelCategoryKey | undefined {
  if (mode === 'image_generation') return 'image'
  if (mode === 'chat' || mode === 'responses' || mode === 'completion') return 'text'
  return undefined
}
