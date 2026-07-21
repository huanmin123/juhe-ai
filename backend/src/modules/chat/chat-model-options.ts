export const chatReasoningEfforts = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export const chatServiceTiers = ['default', 'priority', 'flex'] as const

export type ChatReasoningEffort = typeof chatReasoningEfforts[number]
export type ChatServiceTier = typeof chatServiceTiers[number]

const chatReasoningEffortSet = new Set<string>(chatReasoningEfforts)
const chatServiceTierSet = new Set<string>(chatServiceTiers)

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

export interface ChatModelListOption {
  id: string
  name: string
}

export type ChatModelCapabilities = ChatModelOption & { name: string }

export function chatModelListOptions(models: readonly ChatModelOption[]): ChatModelListOption[] {
  return models.map((model) => ({ id: model.id, name: model.id }))
}

export function chatModelCapabilities(model: ChatModelOption): ChatModelCapabilities {
  return { ...model, name: model.id }
}

export function mergeChatModelCapabilities(models: readonly ChatModelOption[]): ChatModelCapabilities | undefined {
  const first = models[0]
  if (!first) return undefined
  const merged = buildChatModelOptions([first.id], models.map((model) => ({ model: model.id, ...model })))[0]
  return merged ? chatModelCapabilities(merged) : undefined
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
  supportedReasoningEfforts: readonly string[]
  defaultReasoningEffort?: string | null
  supportedServiceTiers: readonly string[]
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
      items.map((item) => item.supportedReasoningEfforts.filter(isChatReasoningEffort))
    )
    const defaultReasoningEffort = commonReasoningDefault(items, supportedReasoningEfforts)
    const catalogServiceTiers = intersectCapabilities(items.map((item) => item.supportedServiceTiers.filter(isChatServiceTier)))
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

function isChatReasoningEffort(value: string): value is ChatReasoningEffort {
  return chatReasoningEffortSet.has(value)
}

function isChatServiceTier(value: string): value is ChatServiceTier {
  return chatServiceTierSet.has(value)
}

function minimumKnownCapability(values: readonly (number | undefined)[]): number | undefined {
  return values.length > 0 && values.every((value): value is number => typeof value === 'number' && value > 0)
    ? Math.min(...values)
    : undefined
}

function commonReasoningDefault(
  items: readonly ChatModelCatalogCapability[],
  supportedReasoningEfforts: readonly ChatReasoningEffort[]
): ChatReasoningEffort | undefined {
  if (!items.length) return undefined
  const defaults = items.map((item) => item.defaultReasoningEffort)
  const first = defaults[0]
  return first
    && isChatReasoningEffort(first)
    && supportedReasoningEfforts.includes(first)
    && defaults.every((value) => value === first)
    ? first
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
  return {
    ...(input.reasoningEffort ? { reasoningEffort: input.reasoningEffort } : {}),
    ...(input.serviceTier ? { serviceTier: input.serviceTier } : {}),
    ...(model.contextWindowTokens ? { contextWindowTokens: model.contextWindowTokens } : {}),
    ...(model.maxInputTokens ? { maxInputTokens: model.maxInputTokens } : {})
  }
}
