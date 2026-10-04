import type { ProviderDefinition } from '@/types/domain'

export const OPENAI_PROTOCOL_CODE = 'openai'
export const OPENAI_PROTOCOL_VERSION = 'v1'
export const OPENAI_COMPATIBLE_PROVIDER_CODE = 'openai'
export const GPT_VENDOR_CODE = 'gpt'
export const XAI_PROVIDER_CODE = 'xai'
export const DEEPSEEK_PROVIDER_CODE = 'deepseek'
export const GLM_PROVIDER_CODE = 'glm'
export const MINIMAX_PROVIDER_CODE = 'minimax'
export const VOLCENGINE_PROVIDER_CODE = 'volcengine'
export const QWEN_PROVIDER_CODE = 'qwen'
export const ANTHROPIC_PROTOCOL_CODE = 'anthropic'
export const ANTHROPIC_PROTOCOL_VERSION = 'v1'
export const ANTHROPIC_PROVIDER_CODE = 'anthropic'
export const GEMINI_PROTOCOL_CODE = 'gemini'
export const GEMINI_PROTOCOL_VERSION = 'v1beta'
export const GEMINI_PROVIDER_CODE = 'gemini'
export const HYBRID_PROVIDER_CODE = 'hybrid'
export const OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID = 'profile_openai_openai_v1'
export const GPT_OPENAI_V1_PROFILE_ID = 'profile_gpt_openai_v1'
export const XAI_OPENAI_V1_PROFILE_ID = 'profile_xai_openai_v1'
export const DEEPSEEK_OPENAI_V1_PROFILE_ID = 'profile_deepseek_openai_v1'
export const DEEPSEEK_ANTHROPIC_V1_PROFILE_ID = 'profile_deepseek_anthropic_v1'
export const GLM_GENERAL_OPENAI_V1_PROFILE_ID = 'profile_glm_general_openai_v1'
export const GLM_CODING_OPENAI_V1_PROFILE_ID = 'profile_glm_coding_openai_v1'
export const GLM_CODING_ANTHROPIC_V1_PROFILE_ID = 'profile_glm_coding_anthropic_v1'
export const MINIMAX_OPENAI_V1_PROFILE_ID = 'profile_minimax_openai_v1'
export const VOLCENGINE_OPENAI_V1_PROFILE_ID = 'profile_volcengine_openai_v1'
export const QWEN_OPENAI_V1_PROFILE_ID = 'profile_qwen_openai_v1'
export const ANTHROPIC_ANTHROPIC_V1_PROFILE_ID = 'profile_anthropic_anthropic_v1'
export const GEMINI_NATIVE_V1BETA_PROFILE_ID = 'profile_gemini_native_v1beta'
export const GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID = 'profile_gemini_openai_chat_v1beta'
export const HYBRID_OPENAI_CHAT_V1_PROFILE_ID = 'profile_hybrid_openai_chat_v1'
export const HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID = 'profile_hybrid_anthropic_messages_v1'
export const HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID = 'profile_hybrid_gemini_native_v1beta'
export const OPENAI_CHAT_COMPLETIONS_FAMILY = 'chat_completions'
export const OPENAI_RESPONSES_FAMILY = 'responses'
// M3 媒体新增（媒体设计 §9/契约 §8）：openai 协议族的厂商原生媒体端点族——
// minimax 档案（profile_minimax_openai_v1）只挂这两个族。
export const OPENAI_VIDEO_GENERATION_FAMILY = 'video_generation'
export const OPENAI_TTS_FAMILY = 'tts'
export const OPENAI_AUDIO_TRANSCRIPTION_FAMILY = 'audio_transcription'
export const ANTHROPIC_MESSAGES_FAMILY = 'messages'
export const ANTHROPIC_MODELS_FAMILY = 'models'
export const ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY = 'message_token_counting'
export const GEMINI_GENERATE_CONTENT_FAMILY = 'generate_content'
export const GEMINI_STREAM_GENERATE_CONTENT_FAMILY = 'stream_generate_content'
export const GEMINI_INTERACTIONS_FAMILY = 'interactions'
export const GEMINI_COUNT_TOKENS_FAMILY = 'count_tokens'
export const GEMINI_EMBED_CONTENT_FAMILY = 'embed_content'

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

export function isGeminiProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return normalizeProviderToken(profile?.protocolCode) === GEMINI_PROTOCOL_CODE
    && normalizeProviderToken(profile?.protocolVersion) === GEMINI_PROTOCOL_VERSION
}

export function isGatewaySupportedProtocolProfile(profile?: { protocolCode?: string; protocolVersion?: string }): boolean {
  return isOpenAIProtocolProfile(profile) || isAnthropicProtocolProfile(profile) || isGeminiProtocolProfile(profile)
}

export function isGptVendorCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GPT_VENDOR_CODE
}

export function isXaiProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === XAI_PROVIDER_CODE
}

export function isDeepSeekProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === DEEPSEEK_PROVIDER_CODE
}

export function isGlmProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GLM_PROVIDER_CODE
}

export function isMinimaxProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === MINIMAX_PROVIDER_CODE
}

export function isVolcengineProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === VOLCENGINE_PROVIDER_CODE
}

export function isQwenProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === QWEN_PROVIDER_CODE
}

export function isGeminiProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === GEMINI_PROVIDER_CODE
}

export function isHybridProviderCode(value: unknown): boolean {
  return normalizeProviderToken(value) === HYBRID_PROVIDER_CODE
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
