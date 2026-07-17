import type { ChatModelOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'

export function selectableChatReasoningEfforts(model?: ChatModelOption): ChatReasoningEffort[] {
  return [...(model?.supportedReasoningEfforts ?? [])]
}

export function defaultChatReasoningEffort(model?: ChatModelOption): ChatReasoningEffort | '' {
  const supported = selectableChatReasoningEfforts(model)
  if (supported.includes('medium')) return 'medium'
  if (model?.defaultReasoningEffort && supported.includes(model.defaultReasoningEffort)) return model.defaultReasoningEffort
  return supported[0] ?? ''
}

export function defaultChatServiceTier(model?: ChatModelOption): ChatServiceTier | '' {
  const supported = model?.supportedServiceTiers ?? []
  if (supported.includes('default')) return 'default'
  return supported[0] ?? ''
}

export function reasoningEffortLabel(value: ChatReasoningEffort): string {
  return ({ minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' })[value]
}
