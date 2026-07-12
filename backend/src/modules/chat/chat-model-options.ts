export const chatReasoningEfforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export const chatServiceTiers = ['priority', 'flex'] as const

export type ChatReasoningEffort = typeof chatReasoningEfforts[number]
export type ChatServiceTier = typeof chatServiceTiers[number]

export interface ChatModelOption {
  id: string
  supportedReasoningEfforts: ChatReasoningEffort[]
  defaultReasoningEffort?: ChatReasoningEffort
  supportedServiceTiers: ChatServiceTier[]
  contextWindowTokens?: number
}

interface ChatModelCatalogCapability {
  model: string
  supportedReasoningEfforts: readonly ChatReasoningEffort[]
  defaultReasoningEffort?: ChatReasoningEffort
  supportedServiceTiers: readonly ChatServiceTier[]
  contextWindowTokens?: number
  maxInputTokens?: number
}

export function buildChatModelOptions(modelIds: readonly string[], catalog: readonly ChatModelCatalogCapability[]): ChatModelOption[] {
  const byModel = new Map(catalog.map((item) => [item.model, item]))
  return [...new Set(modelIds.filter(Boolean))].map((id) => {
    const item = byModel.get(id)
    return {
      id,
      supportedReasoningEfforts: item ? [...item.supportedReasoningEfforts] : [],
      ...(item?.defaultReasoningEffort ? { defaultReasoningEffort: item.defaultReasoningEffort } : {}),
      supportedServiceTiers: item ? [...item.supportedServiceTiers] : [],
      ...((item?.contextWindowTokens ?? item?.maxInputTokens) ? { contextWindowTokens: item?.contextWindowTokens ?? item?.maxInputTokens } : {})
    }
  })
}
