import type { ChatModelCapabilities, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'

export function selectableChatReasoningEfforts(model?: ChatModelCapabilities): ChatReasoningEffort[] {
  return [...(model?.supportedReasoningEfforts ?? [])]
}

export function defaultChatReasoningEffort(model?: ChatModelCapabilities): ChatReasoningEffort | '' {
  return model?.supportedReasoningEfforts[0] ?? ''
}

export function defaultChatServiceTier(model?: ChatModelCapabilities): ChatServiceTier | '' {
  return model?.supportedServiceTiers[0] ?? ''
}

export function normalizeChatModelControls(input: {
  model?: ChatModelCapabilities
  reasoningEffort: ChatReasoningEffort | ''
  serviceTier: ChatServiceTier | ''
}): { reasoningEffort: ChatReasoningEffort | ''; serviceTier: ChatServiceTier | '' } {
  return {
    reasoningEffort: input.reasoningEffort && input.model?.supportedReasoningEfforts.includes(input.reasoningEffort)
      ? input.reasoningEffort
      : defaultChatReasoningEffort(input.model),
    serviceTier: input.serviceTier && input.model?.supportedServiceTiers.includes(input.serviceTier)
      ? input.serviceTier
      : defaultChatServiceTier(input.model)
  }
}

export function reasoningEffortLabel(value: ChatReasoningEffort): string {
  return ({ minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' })[value]
}
