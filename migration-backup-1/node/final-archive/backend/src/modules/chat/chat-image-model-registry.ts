import type { ChatImageModel } from '../../storage/chat.repository.js'

export interface ChatImageModelDefinition {
  id: ChatImageModel
  label: string
  canGenerate: boolean
  canEdit: boolean
  maxReferenceImages: number
}

const imageModels: readonly ChatImageModelDefinition[] = [{
  id: 'gpt-image-2',
  label: 'GPT Image 2',
  canGenerate: true,
  canEdit: true,
  maxReferenceImages: 5
}]

export function listChatImageModels(): ChatImageModelDefinition[] {
  return imageModels.map((model) => ({ ...model }))
}

export function resolveChatImageModel(requested: unknown, fallback: ChatImageModel): ChatImageModelDefinition {
  const modelId = typeof requested === 'string' && requested.trim() ? requested.trim() : fallback
  const definition = imageModels.find((model) => model.id === modelId)
  if (!definition) throw new Error(`不支持的图像模型：${modelId}`)
  return definition
}
