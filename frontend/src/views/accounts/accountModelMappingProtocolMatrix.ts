import type { ProviderDefinition } from '@/types/domain'
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
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import type { AccountFormModel } from './accountFormTypes'

export type AccountModelMappingSourceEndpointFamily = AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
export type AccountModelMappingUpstreamEndpointFamily = AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
export type AccountModelMappingProviderProfile = ProviderDefinition | ProviderDefinition['protocolProfiles'][number] | {
  id?: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
}

export type AccountModelMappingProtocolContext = {
  providerProfile?: AccountModelMappingProviderProfile
  supportedEndpointModes?: AccountFormModel['supportedEndpointModes']
}

type ProtocolProfileKind = 'openai' | 'anthropic' | 'gemini'

type ProtocolConversionRule = {
  source: AccountModelMappingSourceEndpointFamily
  upstream: AccountModelMappingUpstreamEndpointFamily
  upstreamProfile: ProtocolProfileKind
  requiresNativeResponses?: boolean
}

export const accountModelMappingProtocolRules: readonly ProtocolConversionRule[] = [
  { source: OPENAI_CHAT_COMPLETIONS_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
  { source: OPENAI_RESPONSES_FAMILY, upstream: OPENAI_CHAT_COMPLETIONS_FAMILY, upstreamProfile: 'openai' },
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

export function accountModelMappingProtocolValidationMessage(input: {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  enabled?: boolean
  context: AccountModelMappingProtocolContext
}): string | undefined {
  const { sourceEndpointFamily, upstreamEndpointFamily, context } = input
  const providerProfile = context.providerProfile
  const openAIProfile = isOpenAIProtocolProfile(providerProfile)
  const anthropicProfile = isAnthropicProtocolProfile(providerProfile)
  const geminiProfile = isGeminiProtocolProfile(providerProfile)
  const profileId = accountModelMappingProviderProfileId(providerProfile)
  const geminiOpenAIChatProfile = profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
  const geminiNativeProfile = profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID
  const hybridProfile = isHybridProviderProfile(providerProfile)

  if (hybridProfile) {
    const rule = hybridAccountModelMappingProtocolRules.find((item) => (
      item.source === sourceEndpointFamily && item.upstream === upstreamEndpointFamily
    ))
    if (!rule) {
      return unsupportedHybridProtocolConversionMessage(sourceEndpointFamily, upstreamEndpointFamily)
    }
    if (input.enabled !== false && !hasAccountModelMappingUpstreamEndpointFamilyCapability(
      upstreamEndpointFamily,
      context.supportedEndpointModes
    )) {
      return missingUpstreamEndpointFamilyCapabilityMessage(upstreamEndpointFamily)
    }
    return undefined
  }

  if (!openAIProfile && !anthropicProfile && !geminiProfile) {
    return '当前供应商协议不支持模型映射'
  }
  if (
    geminiOpenAIChatProfile
    && sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY
    && sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY
  ) {
    return 'Gemini OpenAI Chat 账号模型别名只能使用 Chat Completions 或 Responses 到 Chat bridge'
  }
  if (geminiOpenAIChatProfile && upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return 'Gemini OpenAI Chat 账号模型别名只能使用 Chat Completions'
  }
  if (geminiProfile && !geminiNativeProfile && !geminiOpenAIChatProfile) {
    return '当前 Gemini 协议档案暂不支持账号模型别名'
  }

  const rule = accountModelMappingProtocolRules.find((item) => (
    item.source === sourceEndpointFamily && item.upstream === upstreamEndpointFamily
  ))
  if (!rule) {
    return unsupportedProtocolConversionMessage(sourceEndpointFamily, upstreamEndpointFamily)
  }

  if (openAIProfile && upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && upstreamEndpointFamily !== OPENAI_RESPONSES_FAMILY) {
    return 'OpenAI 协议账号模型别名只能使用 Chat Completions 或 Responses'
  }
  if (anthropicProfile && upstreamEndpointFamily !== ANTHROPIC_MESSAGES_FAMILY) {
    return 'Anthropic 协议账号模型别名只能使用 Messages'
  }
  if (geminiNativeProfile && upstreamEndpointFamily !== GEMINI_GENERATE_CONTENT_FAMILY) {
    return 'Gemini native 账号模型别名只能使用 GenerateContent / StreamGenerateContent'
  }
  if (!openAIProfile && !anthropicProfile && !geminiNativeProfile) {
    return '当前供应商协议不支持账号模型别名'
  }
  if (input.enabled !== false && !hasAccountModelMappingUpstreamEndpointFamilyCapability(
    upstreamEndpointFamily,
    context.supportedEndpointModes
  )) {
    return rule.requiresNativeResponses
      ? 'Responses 模型别名只能用于账号真实支持 Responses API 的原生上游'
      : missingUpstreamEndpointFamilyCapabilityMessage(upstreamEndpointFamily)
  }
  return undefined
}

export function isAccountModelMappingProtocolAllowed(input: {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  enabled?: boolean
  context: AccountModelMappingProtocolContext
}): boolean {
  return !accountModelMappingProtocolValidationMessage(input)
}

export function isAccountModelMappingSourceEndpointFamilyAllowed(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  context: AccountModelMappingProtocolContext,
  enabled = true
): boolean {
  return accountModelMappingRulesForContext(context).some((rule) => (
    rule.source === sourceEndpointFamily
    && isAccountModelMappingProtocolAllowed({
      sourceEndpointFamily,
      upstreamEndpointFamily: rule.upstream,
      enabled,
      context
    })
  ))
}

export function defaultAccountModelMappingUpstreamEndpointFamily(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  context: AccountModelMappingProtocolContext
): AccountModelMappingUpstreamEndpointFamily {
  const preferred = preferredUpstreamFamilies(sourceEndpointFamily)
  return preferred.find((upstreamEndpointFamily) => isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily,
    upstreamEndpointFamily,
    context
  })) ?? OPENAI_CHAT_COMPLETIONS_FAMILY
}

export function accountModelMappingEndpointFamilyText(
  value: AccountModelMappingSourceEndpointFamily | AccountModelMappingUpstreamEndpointFamily
): string {
  if (value === ANTHROPIC_MESSAGES_FAMILY) return 'Messages'
  if (value === GEMINI_GENERATE_CONTENT_FAMILY) return 'Gemini GenerateContent'
  if (value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) return 'Gemini StreamGenerateContent'
  return value === OPENAI_RESPONSES_FAMILY ? 'Responses' : 'Chat Completions'
}

export function isGeminiGenerateContentMappingSource(value: AccountModelMappingSourceEndpointFamily): boolean {
  return value === GEMINI_GENERATE_CONTENT_FAMILY || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

export function hasNativeResponsesEndpointMode(value: AccountFormModel['supportedEndpointModes']): boolean {
  return hasAccountModelMappingUpstreamEndpointFamilyCapability(OPENAI_RESPONSES_FAMILY, value)
}

export function hasAccountModelMappingUpstreamEndpointFamilyCapability(
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily,
  value: AccountFormModel['supportedEndpointModes'] | undefined
): boolean {
  const modes = value ?? []
  if (upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return modes.some((mode) => mode === 'chat_json' || mode === 'chat_sse')
  }
  if (upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    return modes.some((mode) => mode === 'responses_json' || mode === 'responses_sse')
  }
  if (upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return modes.some((mode) => mode === 'messages_json' || mode === 'messages_sse')
  }
  return modes.some((mode) => mode === 'generate_content_json' || mode === 'generate_content_sse')
}

function accountModelMappingProviderProfileId(providerProfile?: AccountModelMappingProviderProfile): string | undefined {
  if (providerProfile && 'id' in providerProfile && typeof providerProfile.id === 'string') {
    return providerProfile.id
  }
  if (providerProfile && 'providerProtocolProfileId' in providerProfile && typeof providerProfile.providerProtocolProfileId === 'string') {
    return providerProfile.providerProtocolProfileId
  }
  return undefined
}

function isHybridProviderProfile(providerProfile?: AccountModelMappingProviderProfile): boolean {
  return isHybridProviderCode(providerProfile && 'providerCode' in providerProfile ? providerProfile.providerCode : undefined)
}

function accountModelMappingRulesForContext(context: AccountModelMappingProtocolContext): readonly ProtocolConversionRule[] {
  return isHybridProviderProfile(context.providerProfile)
    ? hybridAccountModelMappingProtocolRules
    : accountModelMappingProtocolRules
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
  return `账号模型别名只支持同协议映射；跨协议 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 请改用混合供应商账户`
}

function unsupportedHybridProtocolConversionMessage(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  if (upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    return '混合供应商账户不合成 Responses 上游状态机；Responses 只能由真实支持 Responses 的普通账户原生承接'
  }
  return `混合供应商账户暂不支持 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 的跨协议转换`
}

function preferredUpstreamFamilies(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
): AccountModelMappingUpstreamEndpointFamily[] {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return [ANTHROPIC_MESSAGES_FAMILY]
  }
  if (isGeminiGenerateContentMappingSource(sourceEndpointFamily)) {
    return [GEMINI_GENERATE_CONTENT_FAMILY]
  }
  return sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
    : [OPENAI_CHAT_COMPLETIONS_FAMILY]
}

function missingUpstreamEndpointFamilyCapabilityMessage(
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  return `启用的模型映射上游协议 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 要求账户至少启用一种对应的上游接口能力`
}
