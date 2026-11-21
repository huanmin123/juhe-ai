import type { ProviderModelPricing } from '@/types/domain'

export const modelCategoryOrder = ['text', 'image', 'audio', 'video'] as const

export type ModelCategoryKey = typeof modelCategoryOrder[number]

export const modelCategoryLabels: Record<ModelCategoryKey, string> = {
  text: '对话 / 编码',
  image: '图像',
  audio: '音频',
  video: '视频'
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

export function getModelCategoryFromPricing(
  item: Pick<ProviderModelPricing, 'mode' | 'model' | 'mediaPricing' | 'outputModalities'>
): ModelCategoryKey {
  const projected = categoryFromModeOrModel(item.mode, item.model)
  // mode 投影为 text 时，统一媒体模板与输出模态是更强的分类信号：Gemini
  // generate_content 图像行的 endpoint modes 投影为 chat，仅按 mode 会把
  // 内置图像行显示成文本分类，导致统一媒体价格不可见、保存被严格校验拒绝。
  if (projected !== 'text') return projected
  const unit = item.mediaPricing?.unit
  if (unit === 'image') return 'image'
  if (unit === 'call') return 'video'
  const outputs = item.outputModalities ?? []
  if (unit === 'second') {
    if (outputs.includes('video')) return 'video'
    if (outputs.includes('audio')) return 'audio'
  }
  if (outputs.includes('image')) return 'image'
  return projected
}

function categoryFromModeAlias(mode: string): ModelCategoryKey | undefined {
  if (mode === 'image_generation') return 'image'
  if (mode === 'chat' || mode === 'responses' || mode === 'completion') return 'text'
  // 向量/重排（2026-11-20 协议补齐批）：目录 mode 新值，均为文本 lane，
  // 归文本分类展示与编辑（与账户对话域模型白名单同口径）。
  if (mode === 'embedding' || mode === 'rerank') return 'text'
  // 后端目录读链（catalog.go）把 audio_speech / audio_transcription 归一为
  // audio 分类（音频设计 §3），前端别名表同步。
  if (mode === 'audio_speech' || mode === 'audio_transcription') return 'audio'
  return undefined
}
