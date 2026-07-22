import type { AccountClientCompatibility, AccountModelMapping, AccountSupportedEndpointMode } from './types.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  HYBRID_PROVIDER_CODE,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_RESPONSES_FAMILY,
  normalizeProviderToken
} from './provider-protocol.js'

export const OPENAI_ENDPOINT_MODE_VALUES: readonly AccountSupportedEndpointMode[] = [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse'
] as const

export const OPENAI_CHAT_ENDPOINT_MODES: readonly AccountSupportedEndpointMode[] = [
  'chat_json',
  'chat_sse'
] as const

export const OPENAI_RESPONSES_ENDPOINT_MODES: readonly AccountSupportedEndpointMode[] = [
  'responses_json',
  'responses_sse'
] as const

const openAIEndpointModeSet = new Set<string>(OPENAI_ENDPOINT_MODE_VALUES)

export interface OpenAIRequestShape {
  endpoint: string
  model?: string
  stream: boolean
  createdAt: string
}

export interface OpenAIEndpointModeDefaultContext {
  providerCode?: string
  providerProtocolProfileId?: string
  accountType?: string
  clientCompatibility?: AccountClientCompatibility
}

export function defaultOpenAIEndpointModes(input: OpenAIEndpointModeDefaultContext): AccountSupportedEndpointMode[] {
  if (input.accountType === 'oauth') {
    return [...OPENAI_RESPONSES_ENDPOINT_MODES]
  }
  const providerCode = normalizeProviderToken(input.providerCode)
  if (providerCode === GPT_VENDOR_CODE) {
    return [...OPENAI_ENDPOINT_MODE_VALUES]
  }
  if (providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE || providerCode === DEEPSEEK_PROVIDER_CODE || providerCode === GLM_PROVIDER_CODE || providerCode === GEMINI_PROVIDER_CODE || providerCode === HYBRID_PROVIDER_CODE) {
    return [...OPENAI_CHAT_ENDPOINT_MODES]
  }
  if (input.clientCompatibility === 'codex_responses') {
    return [...OPENAI_ENDPOINT_MODE_VALUES]
  }
  return [...OPENAI_ENDPOINT_MODE_VALUES]
}

export function normalizeOpenAIEndpointModesForWrite(
  value: unknown,
  defaults: OpenAIEndpointModeDefaultContext,
  label = '上游接口能力'
): AccountSupportedEndpointMode[] {
  if (value === undefined) {
    return defaultOpenAIEndpointModes(defaults)
  }
  if (!Array.isArray(value)) {
    throw new Error(`${label}必须是数组`)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isOpenAIEndpointMode(item)) {
      throw new Error(`${label}包含不支持的能力：${String(item)}`)
    }
    if (seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  if (!output.length) {
    throw new Error(`${label}至少选择一项`)
  }
  return output
}

export function normalizeOpenAIEndpointModesForRuntime(
  value: unknown,
  defaults: OpenAIEndpointModeDefaultContext
): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) {
    return defaultOpenAIEndpointModes(defaults)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isOpenAIEndpointMode(item) || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : defaultOpenAIEndpointModes(defaults)
}

export function isOpenAIEndpointMode(value: unknown): value is AccountSupportedEndpointMode {
  return typeof value === 'string' && openAIEndpointModeSet.has(value)
}

export function openAIEndpointModeForRequestShape(input: {
  endpoint?: string
  stream: boolean
}): AccountSupportedEndpointMode | undefined {
  const endpointFamily = openAIEndpointFamilyFromPath(input.endpoint)
  if (endpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return input.stream ? 'chat_sse' : 'chat_json'
  }
  if (endpointFamily === OPENAI_RESPONSES_FAMILY) {
    return input.stream ? 'responses_sse' : 'responses_json'
  }
  return undefined
}

export function openAIEndpointFamilyFromPath(value: unknown): 'chat_completions' | 'responses' | undefined {
  if (typeof value !== 'string') return undefined
  const path = value.trim().toLowerCase()
  if (!path) return undefined
  if (path.includes('/chat/completions')) return OPENAI_CHAT_COMPLETIONS_FAMILY
  if (path.includes('/responses')) return OPENAI_RESPONSES_FAMILY
  return undefined
}

export function accountSupportsOpenAIEndpointMode(input: {
  supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  credentials?: Record<string, unknown>
  providerCode?: string
  providerProtocolProfileId?: string
  accountType?: string
  clientCompatibility?: AccountClientCompatibility
  mode: AccountSupportedEndpointMode
}): boolean {
  const supportedModes = input.supportedEndpointModes?.length
    ? [...input.supportedEndpointModes]
    : normalizeOpenAIEndpointModesForRuntime(input.credentials?.supported_endpoint_modes, {
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
  return supportedModes.includes(input.mode)
}

export function assertOpenAIEndpointModesCompatible(input: {
  modes: readonly AccountSupportedEndpointMode[]
  modelMappings?: readonly AccountModelMapping[]
  providerCode?: string
  providerProtocolProfileId?: string
  accountType?: string
  clientCompatibility?: AccountClientCompatibility
}): void {
  if (input.accountType === 'oauth') {
    const unsupported = input.modes.filter((mode) => !OPENAI_RESPONSES_ENDPOINT_MODES.includes(mode))
    if (unsupported.length) {
      throw new Error(`OAuth 账户上游接口能力只能选择 ${openAIEndpointModeLabel('responses_json', input)} 或 ${openAIEndpointModeLabel('responses_sse', input)}`)
    }
    if (!input.modes.includes('responses_sse')) {
      throw new Error(`OAuth 账户上游接口能力必须启用 ${openAIEndpointModeLabel('responses_sse', input)}`)
    }
  }
  if (input.clientCompatibility === 'codex_responses' && !input.modes.includes('responses_sse')) {
    throw new Error(`Codex Responses 账户上游接口能力必须启用 ${openAIEndpointModeLabel('responses_sse', input)}`)
  }
}

export function supportsCodexResponsesChatBridge(input: {
  providerProtocolProfileId?: string
}): boolean {
  return input.providerProtocolProfileId === OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    || input.providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID
    || input.providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID
    || input.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID
    || input.providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
}

function openAIEndpointModeLabel(
  mode: AccountSupportedEndpointMode,
  context: OpenAIEndpointModeDefaultContext = {}
): string {
  switch (mode) {
    case 'chat_json':
      return `${openAIChatCapabilityName(context)} (JSON)`
    case 'chat_sse':
      return `${openAIChatCapabilityName(context)} (Streaming)`
    case 'responses_json':
      return 'Responses API (JSON)'
    case 'responses_sse':
      return 'Responses API (Streaming)'
    default:
      return mode
  }
}

function openAIChatCapabilityName(context: OpenAIEndpointModeDefaultContext): string {
  const providerCode = normalizeProviderToken(context.providerCode)
  if (providerCode === DEEPSEEK_PROVIDER_CODE || context.providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID) {
    return 'Chat Completion'
  }
  if (providerCode === GLM_PROVIDER_CODE || context.providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID || context.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID) {
    return context.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID ? 'OpenAI Chat Completions' : '对话补全'
  }
  return 'Chat Completions'
}
