import type { ProviderDefinition } from '@/types/domain'

export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const GPT_VENDOR_CODE = 'gpt'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'

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

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}
