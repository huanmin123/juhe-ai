const chatCatalogReasoningEfforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export const chatReasoningEfforts = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export const chatServiceTiers = ['default', 'priority', 'flex'] as const

export type ChatReasoningEffort = typeof chatReasoningEfforts[number]
type ChatCatalogReasoningEffort = typeof chatCatalogReasoningEfforts[number]
export type ChatServiceTier = typeof chatServiceTiers[number]

export interface ChatModelOption {
  id: string
  supportsPromptCaching: boolean
  supportedReasoningEfforts: ChatReasoningEffort[]
  defaultReasoningEffort?: ChatReasoningEffort
  supportedServiceTiers: ChatServiceTier[]
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  supportedApiProtocols: string[]
  inputModalities: string[]
  outputModalities: string[]
  supportedTools: string[]
}

export class ChatModelCapabilityError extends Error {
  readonly code = 'chat_model_capability_mismatch'

  constructor(message: string) {
    super(message)
    this.name = 'ChatModelCapabilityError'
  }
}

interface ChatModelCatalogCapability {
  model: string
  supportsPromptCaching?: boolean
  supportedReasoningEfforts: readonly ChatCatalogReasoningEffort[]
  defaultReasoningEffort?: ChatCatalogReasoningEffort
  supportedServiceTiers: readonly ChatServiceTier[]
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  supportedApiProtocols?: readonly string[]
  inputModalities?: readonly string[]
  outputModalities?: readonly string[]
  supportedTools?: readonly string[]
}

export function buildChatModelOptions(modelIds: readonly string[], catalog: readonly ChatModelCatalogCapability[]): ChatModelOption[] {
  const byModel = new Map<string, ChatModelCatalogCapability[]>()
  for (const item of catalog) {
    const items = byModel.get(item.model)
    if (items) items.push(item)
    else byModel.set(item.model, [item])
  }
  return [...new Set(modelIds.filter(Boolean))].map((id) => {
    const items = byModel.get(id) ?? []
    const supportedReasoningEfforts = intersectCapabilities(
      items.map((item) => item.supportedReasoningEfforts.filter((value): value is ChatReasoningEffort => value !== 'none'))
    )
    const catalogDefaultReasoningEffort = items
      .map((item) => item.defaultReasoningEffort)
      .find((value): value is ChatReasoningEffort => Boolean(value && value !== 'none' && supportedReasoningEfforts.includes(value)))
    const defaultReasoningEffort = supportedReasoningEfforts.includes('medium')
      ? 'medium'
      : catalogDefaultReasoningEffort
        ? catalogDefaultReasoningEffort
        : supportedReasoningEfforts[0]
    const catalogServiceTiers = intersectCapabilities(items.map((item) => item.supportedServiceTiers))
    const supportedServiceTiers = catalogServiceTiers.length
      ? [...new Set<ChatServiceTier>(['default', ...catalogServiceTiers])]
      : []
    const contextWindowTokens = minimumKnownCapability(items.map((item) => item.contextWindowTokens))
    const maxOutputTokens = minimumKnownCapability(items.map((item) => item.maxOutputTokens))
    const maxInputTokens = minimumKnownCapability(items.map((item) => item.maxInputTokens
      ?? (item.contextWindowTokens && item.maxOutputTokens
        ? item.contextWindowTokens - item.maxOutputTokens
        : undefined)))
    const supportedApiProtocols = intersectCapabilities(items.map((item) => item.supportedApiProtocols ?? []))
    const inputModalities = intersectCapabilities(items.map((item) => item.inputModalities ?? []))
    const outputModalities = intersectCapabilities(items.map((item) => item.outputModalities ?? []))
    const supportedTools = intersectCapabilities(items.map((item) => item.supportedTools ?? []))
    return {
      id,
      supportsPromptCaching: items.length > 0 && items.every((item) => item.supportsPromptCaching === true),
      supportedReasoningEfforts,
      ...(defaultReasoningEffort ? { defaultReasoningEffort } : {}),
      supportedServiceTiers,
      ...(contextWindowTokens ? { contextWindowTokens } : {}),
      ...(maxInputTokens && maxInputTokens > 0 ? { maxInputTokens } : {}),
      ...(maxOutputTokens ? { maxOutputTokens } : {}),
      supportedApiProtocols,
      inputModalities,
      outputModalities,
      supportedTools
    }
  })
}

function minimumKnownCapability(values: readonly (number | undefined)[]): number | undefined {
  return values.length > 0 && values.every((value): value is number => typeof value === 'number' && value > 0)
    ? Math.min(...values)
    : undefined
}

function intersectCapabilities<TValue extends string>(values: readonly (readonly TValue[])[]): TValue[] {
  if (!values.length) return []
  const [first, ...rest] = values
  return [...new Set(first)].filter((value) => rest.every((items) => items.includes(value)))
}

export function resolveChatModelRequestOptions(
  model: ChatModelOption,
  input: { reasoningEffort?: ChatReasoningEffort; serviceTier?: ChatServiceTier }
): { reasoningEffort?: ChatReasoningEffort; serviceTier?: ChatServiceTier; contextWindowTokens?: number; maxInputTokens?: number } {
  if (input.reasoningEffort && !model.supportedReasoningEfforts.includes(input.reasoningEffort)) {
    throw new ChatModelCapabilityError('当前模型不支持所选思考级别，请重新选择')
  }
  if (input.serviceTier && !model.supportedServiceTiers.includes(input.serviceTier)) {
    throw new ChatModelCapabilityError('当前模型不支持所选服务等级，请重新选择')
  }
  const reasoningEffort = input.reasoningEffort ?? model.defaultReasoningEffort
  const serviceTier = input.serviceTier ?? (model.supportedServiceTiers.includes('default') ? 'default' : undefined)
  return {
    ...(reasoningEffort ? { reasoningEffort } : {}),
    ...(serviceTier ? { serviceTier } : {}),
    ...(model.contextWindowTokens ? { contextWindowTokens: model.contextWindowTokens } : {}),
    ...(model.maxInputTokens ? { maxInputTokens: model.maxInputTokens } : {})
  }
}
