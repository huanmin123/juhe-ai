import {
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  XAI_PROVIDER_CODE,
  normalizeProviderToken
} from '../../domain/provider-protocol.js'

export const chatGenerationParameters = [
  'temperature',
  'topP',
  'frequencyPenalty',
  'presencePenalty',
  'maxOutputTokens',
  'seed'
] as const

export type ChatGenerationParameter = typeof chatGenerationParameters[number]
export type ChatGenerationProtocol = 'chat_completions' | 'responses'

export interface ChatGenerationParameterCapability {
  parameter: ChatGenerationParameter
  min: number
  max: number
  step: number
  defaultValue: number
}

export type ChatGenerationParameterCapabilities = Partial<Record<ChatGenerationProtocol, ChatGenerationParameterCapability[]>>
export type ChatGenerationParameters = Partial<Record<ChatGenerationParameter, number>>

export interface ChatGenerationRouteAccount {
  providerCode?: string
  type?: string
  modelMappings?: ReadonlyArray<{
    enabled?: boolean
    sourceModel: string
    sourceEndpointFamily: string
    upstreamModel: string
    upstreamEndpointFamily: string
  }>
}

const definitions: Record<ChatGenerationParameter, ChatGenerationParameterCapability> = {
  temperature: { parameter: 'temperature', min: 0, max: 2, step: 0.1, defaultValue: 1 },
  topP: { parameter: 'topP', min: 0, max: 1, step: 0.05, defaultValue: 1 },
  frequencyPenalty: { parameter: 'frequencyPenalty', min: -2, max: 2, step: 0.1, defaultValue: 0 },
  presencePenalty: { parameter: 'presencePenalty', min: -2, max: 2, step: 0.1, defaultValue: 0 },
  maxOutputTokens: { parameter: 'maxOutputTokens', min: 1, max: 128_000, step: 1, defaultValue: 4_096 },
  seed: { parameter: 'seed', min: 0, max: 2_147_483_647, step: 1, defaultValue: 0 }
}

export function generationParameterCapabilitiesForModel(input: {
  providerCode: string
  model: string
  maxOutputTokens?: number
}): ChatGenerationParameterCapabilities {
  const providerCode = normalizeProviderToken(input.providerCode)
  const model = input.model.trim().toLowerCase()
  const maxOutputTokens = positiveInteger(input.maxOutputTokens)
  const capability = (parameter: ChatGenerationParameter): ChatGenerationParameterCapability => ({
    ...definitions[parameter],
    ...(parameter === 'maxOutputTokens' && maxOutputTokens
      ? { max: maxOutputTokens, defaultValue: Math.min(definitions.maxOutputTokens.defaultValue, maxOutputTokens) }
      : {})
  })
  const select = (...parameters: ChatGenerationParameter[]) => parameters.map(capability)

  if (providerCode === GPT_VENDOR_CODE) {
    // Responses has no documented penalty or seed fields. Keep GPT-5 conservative because
    // several reasoning variants reject non-default sampling values.
    const chat = model.startsWith('gpt-5')
      ? select('frequencyPenalty', 'presencePenalty', 'maxOutputTokens', 'seed')
      : select(...chatGenerationParameters)
    const responses = model.startsWith('gpt-5')
      ? select('maxOutputTokens')
      : select('temperature', 'topP', 'maxOutputTokens')
    return { chat_completions: chat, responses }
  }
  if (providerCode === XAI_PROVIDER_CODE) {
    const reasoning = /(?:reasoning|think)/.test(model)
    return {
      chat_completions: reasoning
        ? select('temperature', 'topP', 'maxOutputTokens', 'seed')
        : select(...chatGenerationParameters),
      responses: select('temperature', 'topP', 'maxOutputTokens')
    }
  }
  if (providerCode === DEEPSEEK_PROVIDER_CODE) {
    // V4 defaults to thinking; its sampling fields are accepted but ignored in that mode.
    const supportsSampling = model === 'deepseek-chat'
    return { chat_completions: supportsSampling ? select('temperature', 'topP', 'maxOutputTokens') : select('maxOutputTokens') }
  }
  if (providerCode === ANTHROPIC_PROVIDER_CODE) {
    // Recent Claude models reject non-default sampling values; only output length is
    // safe to expose without the account's exact thinking/model-version policy.
    const samplingAllowed = !/(?:claude-(?:opus|sonnet)-4\.(?:7|8)|(?:fable|mythos|opus|sonnet)-5)/.test(model)
    return {
      chat_completions: samplingAllowed
        ? select('temperature', 'topP', 'maxOutputTokens')
        : select('maxOutputTokens')
    }
  }
  if (providerCode === GEMINI_PROVIDER_CODE) {
    // The current OpenAI-Gemini bridge only preserves these three fields. Native Gemini
    // supports additional controls, but advertising them here would silently drop them.
    const samplingDeprecated = /(?:^|[-_.])gemini-3(?:[-_.]|$)/.test(model)
    return {
      chat_completions: samplingDeprecated
        ? select('maxOutputTokens')
        : select('temperature', 'topP', 'maxOutputTokens'),
      responses: samplingDeprecated
        ? select('maxOutputTokens')
        : select('temperature', 'topP', 'maxOutputTokens')
    }
  }
  if (providerCode === GLM_PROVIDER_CODE) {
    const glmCapability = (parameter: ChatGenerationParameter): ChatGenerationParameterCapability => ({
      ...capability(parameter),
      ...(parameter === 'temperature' ? { max: 1 } : {}),
      ...(parameter === 'topP' ? { min: 0.01 } : {})
    })
    return { chat_completions: (['temperature', 'topP', 'maxOutputTokens'] as ChatGenerationParameter[]).map(glmCapability) }
  }
  return {}
}

export function normalizeChatGenerationParameters(input: ChatGenerationParameters | undefined): ChatGenerationParameters {
  if (!input) return {}
  return Object.fromEntries(Object.entries(input).filter(([, value]) => typeof value === 'number' && Number.isFinite(value))) as ChatGenerationParameters
}

export function limitGenerationParameterMaxOutputTokens(
  capabilities: ChatGenerationParameterCapabilities,
  maxOutputTokens: number | undefined
): ChatGenerationParameterCapabilities {
  const limit = positiveInteger(maxOutputTokens)
  if (!limit) return capabilities
  return Object.fromEntries(Object.entries(capabilities).map(([protocol, items]) => [
    protocol,
    items.flatMap((item) => {
      if (item.parameter !== 'maxOutputTokens') return [{ ...item }]
      const max = Math.min(item.max, limit)
      return max >= item.min ? [{ ...item, max, defaultValue: Math.min(item.defaultValue, max) }] : []
    })
  ])) as ChatGenerationParameterCapabilities
}

export function constrainChatGenerationParametersForRoute(input: {
  capabilities: readonly ChatGenerationParameterCapability[]
  model: string
  protocol: ChatGenerationProtocol
  accounts: readonly ChatGenerationRouteAccount[]
}): ChatGenerationParameterCapability[] {
  if (!input.accounts.length) return []
  return input.capabilities.flatMap((capability) => {
    const routeCapabilities = input.accounts.map((account) => routeCapabilityForAccount({ ...input, account, capability }))
    if (routeCapabilities.some((item) => !item)) return []
    const resolved = routeCapabilities as ChatGenerationParameterCapability[]
    const min = Math.max(...resolved.map((item) => item.min))
    const max = Math.min(...resolved.map((item) => item.max))
    if (min > max) return []
    return [{
      ...capability,
      min,
      max,
      defaultValue: Math.min(Math.max(capability.defaultValue, min), max)
    }]
  })
}

export function intersectGenerationParameterCapabilityLists(
  lists: readonly (readonly ChatGenerationParameterCapability[])[]
): ChatGenerationParameterCapability[] {
  if (!lists.length || lists.some((list) => !list.length)) return []
  const [first, ...rest] = lists
  return first.flatMap((candidate) => {
    const matching = lists.map((list) => list.find((item) => item.parameter === candidate.parameter))
    if (matching.some((item) => !item)) return []
    const capabilities = matching as ChatGenerationParameterCapability[]
    const min = Math.max(...capabilities.map((item) => item.min))
    const max = Math.min(...capabilities.map((item) => item.max))
    if (min > max) return []
    return [{
      ...candidate,
      min,
      max,
      defaultValue: Math.min(Math.max(candidate.defaultValue, min), max)
    }]
  })
}

export function intersectGenerationParameterCapabilities(
  items: readonly ChatGenerationParameterCapabilities[]
): ChatGenerationParameterCapabilities {
  if (!items.length) return {}
  const protocols: ChatGenerationProtocol[] = ['chat_completions', 'responses']
  return Object.fromEntries(protocols.flatMap((protocol) => {
    const lists = items.map((item) => item[protocol] ?? [])
    if (lists.some((list) => !list.length)) return []
    const first = lists[0]
    const capabilities = first.filter((candidate) => lists.every((list) => list.some((item) => item.parameter === candidate.parameter)))
      .map((candidate) => ({
        ...candidate,
        min: Math.max(...lists.map((list) => list.find((item) => item.parameter === candidate.parameter)?.min ?? candidate.min)),
        max: Math.min(...lists.map((list) => list.find((item) => item.parameter === candidate.parameter)?.max ?? candidate.max))
      }))
      .filter((item) => item.min <= item.max)
    return capabilities.length ? [[protocol, capabilities] as const] : []
  }))
}

function positiveInteger(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

function routeCapabilityForAccount(input: {
  account: ChatGenerationRouteAccount
  capability: ChatGenerationParameterCapability
  model: string
  protocol: ChatGenerationProtocol
}): ChatGenerationParameterCapability | undefined {
  const { account, capability, model, protocol } = input
  if (normalizeProviderToken(account.providerCode) === GPT_VENDOR_CODE && account.type === 'oauth') return undefined
  const mapping = account.modelMappings?.find((item) => (
    item.enabled !== false
    && item.sourceModel === model
    && item.sourceEndpointFamily === protocol
  ))
  if (!mapping) return capability
  if (mapping.upstreamEndpointFamily !== protocol) {
    return bridgePreservedParameters.has(capability.parameter) ? capability : undefined
  }
  if (mapping.upstreamModel === model) return capability
  return generationParameterCapabilitiesForModel({
    providerCode: account.providerCode ?? '',
    model: mapping.upstreamModel
  })[protocol]?.find((item) => item.parameter === capability.parameter)
}

const bridgePreservedParameters = new Set<ChatGenerationParameter>(['temperature', 'topP', 'maxOutputTokens'])
