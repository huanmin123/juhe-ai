import type {
  AccountModelMapping,
  AccountModelMappingEndpointFamily,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  AccountSupportedEndpointMode
} from '../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile,
  type ProviderProtocolProfileDefinition
} from '../domain/provider-protocol.js'

type ProtocolProfileKind = 'openai' | 'anthropic' | 'gemini'

type ProtocolConversionRule = {
  source: AccountModelMappingSourceEndpointFamily
  upstream: AccountModelMappingUpstreamEndpointFamily
  upstreamProfile: ProtocolProfileKind
  requiresNativeResponses?: boolean
}

export const accountModelMappingProtocolRules: readonly ProtocolConversionRule[] = [
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: OPENAI_RESPONSES_FAMILY, upstreamProfile: 'openai', requiresNativeResponses: true },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' }
] as const

export const hybridAccountModelMappingProtocolRules: readonly ProtocolConversionRule[] = [
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' }
] as const

export function assertSupportedAccountModelMappingEndpointFamilyConversion(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): void {
  const rule = accountModelMappingProtocolRules.find((item) => (
    item.source === sourceEndpointFamily && item.upstream === upstreamEndpointFamily
  ))
  if (!rule) {
    throw new Error(unsupportedProtocolConversionMessage(sourceEndpointFamily, upstreamEndpointFamily))
  }
}

export function assertAccountModelMappingProtocolAllowed(
  mapping: Pick<AccountModelMapping, 'sourceEndpointFamily' | 'upstreamEndpointFamily'>,
  options: {
    providerProfile: ProviderProtocolProfileDefinition
    supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  }
): void {
  const openAIProfile = isOpenAIProtocolProfile(options.providerProfile)
  const anthropicProfile = isAnthropicProtocolProfile(options.providerProfile)
  const geminiProfile = isGeminiProtocolProfile(options.providerProfile)
  const profileId = options.providerProfile.providerProtocolProfileId ?? options.providerProfile.id
  const geminiOpenAIChatProfile = profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
  const geminiNativeProfile = profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID

  if (!openAIProfile && !anthropicProfile && !geminiProfile) {
    throw new Error('当前供应商协议不支持模型映射')
  }
  if (geminiOpenAIChatProfile && mapping.sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
    throw new Error('Gemini OpenAI Chat 档案的账号模型别名只能使用 Chat Completions')
  }
  if (geminiOpenAIChatProfile && mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
    throw new Error('Gemini OpenAI Chat 档案的账号模型别名上游协议只能是 Chat Completions')
  }
  if (geminiProfile && !geminiNativeProfile && !geminiOpenAIChatProfile) {
    throw new Error('当前 Gemini 协议档案暂不支持账号模型别名')
  }

  const rule = accountModelMappingProtocolRules.find((item) => (
    item.source === mapping.sourceEndpointFamily && item.upstream === mapping.upstreamEndpointFamily
  ))
  if (!rule) {
    throw new Error(unsupportedProtocolConversionMessage(mapping.sourceEndpointFamily, mapping.upstreamEndpointFamily))
  }

  if ((mapping.sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY || mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) && !openAIProfile) {
    throw new Error('当前供应商协议不支持 OpenAI 账号模型别名')
  }
  if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY && !anthropicProfile) {
    throw new Error('当前供应商协议不支持 Anthropic Messages 账号模型别名')
  }
  if (isGeminiGenerateContentMappingSource(mapping.sourceEndpointFamily) && !geminiNativeProfile) {
    throw new Error('当前供应商协议不支持 Gemini native 账号模型别名')
  }
  if (rule.requiresNativeResponses && !hasNativeResponsesEndpointMode(options.supportedEndpointModes)) {
    throw new Error('上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游')
  }
}

export function assertHybridAccountModelMappingProtocolAllowed(
  mapping: Pick<AccountModelMapping, 'sourceEndpointFamily' | 'upstreamEndpointFamily'>,
  options: {
    providerProfile: ProviderProtocolProfileDefinition
    supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  }
): void {
  const profileId = options.providerProfile.providerProtocolProfileId ?? options.providerProfile.id
  const openAIProfile = isOpenAIProtocolProfile(options.providerProfile)
  const anthropicProfile = isAnthropicProtocolProfile(options.providerProfile)
  const geminiProfile = isGeminiProtocolProfile(options.providerProfile)
  const rule = hybridAccountModelMappingProtocolRules.find((item) => (
    item.source === mapping.sourceEndpointFamily && item.upstream === mapping.upstreamEndpointFamily
  ))
  if (!rule) {
    throw new Error(unsupportedHybridProtocolConversionMessage(mapping.sourceEndpointFamily, mapping.upstreamEndpointFamily))
  }
  if (rule.upstreamProfile === 'openai' && !openAIProfile) {
    throw new Error('混合供应商当前账户真实上游不是 OpenAI Chat，不能配置目标为 Chat Completions 的映射')
  }
  if (rule.upstreamProfile === 'anthropic' && !anthropicProfile) {
    throw new Error('混合供应商当前账户真实上游不是 Anthropic Messages，不能配置目标为 Messages 的映射')
  }
  if (rule.upstreamProfile === 'gemini' && !geminiProfile) {
    throw new Error('混合供应商当前账户真实上游不是 Gemini native，不能配置目标为 Gemini GenerateContent 的映射')
  }
  if (rule.requiresNativeResponses && !hasNativeResponsesEndpointMode(options.supportedEndpointModes)) {
    throw new Error('上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游')
  }
  if (profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID) {
    throw new Error('混合供应商不能使用 Gemini OpenAI Chat 普通档案，请选择混合供应商真实上游档案')
  }
}

export function assertAccountModelMappingEndpointFamilyValues(mappings: AccountModelMapping[]): void {
  for (const mapping of mappings) {
    if (
      mapping.sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY
      && mapping.sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY
      && mapping.sourceEndpointFamily !== ANTHROPIC_MESSAGES_FAMILY
      && mapping.sourceEndpointFamily !== GEMINI_GENERATE_CONTENT_FAMILY
      && mapping.sourceEndpointFamily !== GEMINI_STREAM_GENERATE_CONTENT_FAMILY
    ) {
      throw new Error(`映射下游协议不支持：${mapping.sourceEndpointFamily}`)
    }
    if (
      mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY
      && mapping.upstreamEndpointFamily !== OPENAI_RESPONSES_FAMILY
      && mapping.upstreamEndpointFamily !== ANTHROPIC_MESSAGES_FAMILY
      && mapping.upstreamEndpointFamily !== GEMINI_GENERATE_CONTENT_FAMILY
    ) {
      throw new Error(`映射上游协议不支持：${mapping.upstreamEndpointFamily}`)
    }
  }
}

export function isGeminiGenerateContentMappingSource(value: AccountModelMappingSourceEndpointFamily): boolean {
  return value === GEMINI_GENERATE_CONTENT_FAMILY || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

export function hasNativeResponsesEndpointMode(value: readonly AccountSupportedEndpointMode[] | undefined): boolean {
  return (value ?? []).some((mode) => mode === 'responses_json' || mode === 'responses_sse')
}

export function accountModelMappingEndpointFamilyLabel(value: AccountModelMappingEndpointFamily): string {
  if (value === OPENAI_RESPONSES_FAMILY) return 'Responses'
  if (value === ANTHROPIC_MESSAGES_FAMILY) return 'Messages'
  if (value === GEMINI_GENERATE_CONTENT_FAMILY) return 'Gemini GenerateContent'
  if (value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) return 'Gemini StreamGenerateContent'
  return 'Chat Completions'
}

function unsupportedProtocolConversionMessage(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return '账号模型别名不支持 Anthropic Messages 跨协议映射，请改用混合供应商账户'
  }
  if (isGeminiGenerateContentMappingSource(sourceEndpointFamily)) {
    return '账号模型别名不支持 Gemini GenerateContent 跨协议映射，请改用混合供应商账户'
  }
  return `账号模型别名只支持同协议映射；跨协议 ${accountModelMappingEndpointFamilyLabel(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyLabel(upstreamEndpointFamily)} 请改用混合供应商账户`
}

function unsupportedHybridProtocolConversionMessage(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  return `混合供应商账户暂不支持 ${accountModelMappingEndpointFamilyLabel(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyLabel(upstreamEndpointFamily)} 的协议转换`
}
