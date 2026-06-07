export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const GPT_VENDOR_CODE = 'gpt'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'

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

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function normalizeProviderToken(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  return normalized || undefined
}
