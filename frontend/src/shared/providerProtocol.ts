import type { ProviderDefinition } from '@/types/domain'

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

export function normalizeProviderToken(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

export function isOpenAIProtocolProvider(provider?: Pick<ProviderDefinition, 'protocolCode' | 'protocolVersion'>): boolean {
  return normalizeProviderToken(provider?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(provider?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isOpenAIProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === OPENAI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === OPENAI_PROTOCOL_VERSION
}

export function isAnthropicProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === ANTHROPIC_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === ANTHROPIC_PROTOCOL_VERSION
}

export function isGatewaySupportedProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return isOpenAIProtocolProfile(profile) || isAnthropicProtocolProfile(profile)
}

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function isOpenAICompatibleProviderCode(value: unknown): boolean {
  const normalized = normalizeProviderToken(value)
  return normalized === OPENAI_COMPATIBLE_PROVIDER_CODE || normalized === GPT_VENDOR_CODE
}

export function preferredDefaultProvider(providers: ProviderDefinition[]): ProviderDefinition | undefined {
  const enabledProviders = providers.filter((provider) => provider.enabled)
  return enabledProviders.find((provider) => isGptVendorCode(provider.code)) ?? enabledProviders[0]
}

export function preferredDefaultProviderCode(providers: ProviderDefinition[]): string {
  return preferredDefaultProvider(providers)?.code ?? ''
}

export function defaultProviderProtocolProfileId(provider?: Pick<ProviderDefinition, 'defaultProtocolProfileId' | 'protocolProfiles'>): string {
  if (!provider) return ''
  return provider.protocolProfiles.find((profile) => profile.id === provider.defaultProtocolProfileId)?.id
    || provider.protocolProfiles.find((profile) => profile.enabled)?.id
    || provider.protocolProfiles[0]?.id
    || provider.defaultProtocolProfileId
    || ''
}
