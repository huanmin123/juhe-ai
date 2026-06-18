export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const OPENAI_COMPATIBLE_PROVIDER_CODE = 'openai'
export const GPT_VENDOR_CODE = 'gpt'
export const ANTHROPIC_PROTOCOL_CODE = 'anthropic'
export const ANTHROPIC_PROTOCOL_VERSION = 'v1'
export const ANTHROPIC_PROVIDER_CODE = 'anthropic'
export const OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID = 'profile_openai_openai_v1'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const ANTHROPIC_ANTHROPIC_V1_PROFILE_ID = 'profile_anthropic_anthropic_v1'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'
export const ANTHROPIC_MESSAGES_FAMILY = 'messages'
export const ANTHROPIC_MODELS_FAMILY = 'models'
export const ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY = 'message_token_counting'

export interface ProviderProtocolDefinition {
  code?: string
  protocolCode?: string
  protocolVersion?: string
}

export interface ProviderProtocolProfileDefinition {
  id?: string
  providerCode?: string
  protocolCode?: string
  protocolVersion?: string
}

export function isOpenAIProtocolProvider(provider: ProviderProtocolDefinition | undefined): boolean {
  return normalizeProviderToken(provider?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(provider?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isOpenAIProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return normalizeProviderToken(profile?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isAnthropicProtocolProvider(provider: ProviderProtocolDefinition | undefined): boolean {
  return normalizeProviderToken(provider?.protocolCode) === ANTHROPIC_PROTOCOL_CODE
    && normalizeProviderToken(provider?.protocolVersion) === ANTHROPIC_PROTOCOL_VERSION
}

export function isAnthropicProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return normalizeProviderToken(profile?.protocolCode) === ANTHROPIC_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === ANTHROPIC_PROTOCOL_VERSION
}

export function isGatewaySupportedProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return isOpenAIProtocolProfile(profile) || isAnthropicProtocolProfile(profile)
}

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function isOpenAICompatibleProviderCode(value: unknown): boolean {
  const normalized = normalizeProviderToken(value)
  return normalized === OPENAI_COMPATIBLE_PROVIDER_CODE || normalized === GPT_VENDOR_CODE
}

export function normalizeProviderToken(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  return normalized || undefined
}
