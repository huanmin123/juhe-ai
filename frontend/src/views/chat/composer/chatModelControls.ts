import type { ChatGenerationParameter, ChatGenerationParameters, ChatModelCapabilities, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'

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

export function normalizeChatGenerationParameters(input: {
  model?: ChatModelCapabilities
  values: ChatGenerationParameters
}): ChatGenerationParameters {
  const capabilities = new Map((input.model?.generationParameters ?? []).map((item) => [item.parameter, item]))
  const output: ChatGenerationParameters = {}
  for (const [parameter, rawValue] of Object.entries(input.values) as Array<[ChatGenerationParameter, number]>) {
    const capability = capabilities.get(parameter)
    if (!capability || !Number.isFinite(rawValue) || rawValue < capability.min || rawValue > capability.max) continue
    output[parameter] = parameter === 'seed' || parameter === 'maxOutputTokens' ? Math.trunc(rawValue) : rawValue
  }
  if (output.temperature !== undefined && output.topP !== undefined) delete output.topP
  return output
}

export function chatGenerationParameterLabel(value: ChatGenerationParameter): string {
  return ({
    temperature: '温度',
    topP: 'Top P',
    frequencyPenalty: '频率惩罚',
    presencePenalty: '存在惩罚',
    maxOutputTokens: '最大 Tokens',
    seed: '随机种子'
  })[value]
}

export function chatGenerationParameterDescription(value: ChatGenerationParameter): string {
  return ({
    temperature: '控制输出的随机性与创造性。数值越低，回答通常越稳定。',
    topP: '按累计概率筛选候选词，适合精细控制采样范围。',
    frequencyPenalty: '降低已经频繁出现的词再次出现的概率，减少重复措辞。',
    presencePenalty: '鼓励模型引入尚未出现的新词或新方向，减少原地重复。',
    maxOutputTokens: '限制本次回复可生成的最长内容，不会改变模型的总上下文窗口。',
    seed: '为兼容模型固定随机起点，让相同请求的结果更容易复现；不保证绝对一致。'
  })[value]
}
