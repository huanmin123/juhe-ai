export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const OPENAI_COMPATIBLE_PROVIDER_CODE = 'openai'
export const GPT_VENDOR_CODE = 'gpt'
export const DEEPSEEK_PROVIDER_CODE = 'deepseek'
export const GLM_PROVIDER_CODE = 'glm'
export const ANTHROPIC_PROTOCOL_CODE = 'anthropic'
export const ANTHROPIC_PROTOCOL_VERSION = 'v1'
export const ANTHROPIC_PROVIDER_CODE = 'anthropic'
export const GEMINI_PROTOCOL_CODE = 'gemini'
export const GEMINI_PROTOCOL_VERSION = 'v1beta'
export const GEMINI_PROVIDER_CODE = 'gemini'
export const HYBRID_PROVIDER_CODE = 'hybrid'
export const OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID = 'profile_openai_openai_v1'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const DEEPSEEK_OPENAI_V1_PROFILE_ID = 'profile_deepseek_openai_v1'
export const DEEPSEEK_ANTHROPIC_V1_PROFILE_ID = 'profile_deepseek_anthropic_v1'
export const GLM_GENERAL_OPENAI_V1_PROFILE_ID = 'profile_glm_general_openai_v1'
export const GLM_CODING_OPENAI_V1_PROFILE_ID = 'profile_glm_coding_openai_v1'
export const GLM_CODING_ANTHROPIC_V1_PROFILE_ID = 'profile_glm_coding_anthropic_v1'
export const ANTHROPIC_ANTHROPIC_V1_PROFILE_ID = 'profile_anthropic_anthropic_v1'
export const GEMINI_NATIVE_V1BETA_PROFILE_ID = 'profile_gemini_native_v1beta'
export const GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID = 'profile_gemini_openai_chat_v1beta'
export const HYBRID_OPENAI_CHAT_V1_PROFILE_ID = 'profile_hybrid_openai_chat_v1'
export const HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID = 'profile_hybrid_anthropic_messages_v1'
export const HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID = 'profile_hybrid_gemini_native_v1beta'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'
export const ANTHROPIC_MESSAGES_FAMILY = 'messages'
export const ANTHROPIC_MODELS_FAMILY = 'models'
export const ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY = 'message_token_counting'
export const GEMINI_MODELS_FAMILY = 'models'
export const GEMINI_GENERATE_CONTENT_FAMILY = 'generate_content'
export const GEMINI_STREAM_GENERATE_CONTENT_FAMILY = 'stream_generate_content'
export const GEMINI_COUNT_TOKENS_FAMILY = 'count_tokens'
export const GEMINI_EMBED_CONTENT_FAMILY = 'embed_content'

export interface ProviderProtocolDefinition {
  code?: string
  protocolCode?: string
  protocolVersion?: string
}

export interface ProviderProtocolProfileDefinition {
  id?: string
  providerProtocolProfileId?: string
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

export function isGeminiProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return normalizeProviderToken(profile?.protocolCode) === GEMINI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === GEMINI_PROTOCOL_VERSION
}

export function isGatewaySupportedProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return isOpenAIProtocolProfile(profile) || isAnthropicProtocolProfile(profile) || isGeminiProtocolProfile(profile)
}

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function isDeepSeekProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === DEEPSEEK_PROVIDER_CODE
}

export function isGlmProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GLM_PROVIDER_CODE
}

export function isOpenAICompatibleProviderCode(value: unknown): boolean {
  const normalized = normalizeProviderToken(value)
  return normalized === OPENAI_COMPATIBLE_PROVIDER_CODE || normalized === GPT_VENDOR_CODE
}

export function isGeminiProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GEMINI_PROVIDER_CODE
}

export function isHybridProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === HYBRID_PROVIDER_CODE
}

export function normalizeProviderToken(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  return normalized || undefined
}
