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
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import type { AccountFormModel } from './accountFormTypes'
import { responsesEndpointModes } from './accountProviderCapabilities'

export type AccountModelMappingSourceEndpointFamily = AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
export type AccountModelMappingUpstreamEndpointFamily = AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
export type AccountModelMappingProviderProfile = ProviderDefinition | ProviderDefinition['protocolProfiles'][number] | {
  id?: string
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
  { source: OPENAI_RESPONSES_FAMILY, upstream: OPENAI_RESPONSES_FAMILY, upstreamProfile: 'openai', requiresNativeResponses: true },
  { source: ANTHROPIC_MESSAGES_FAMILY, upstream: ANTHROPIC_MESSAGES_FAMILY, upstreamProfile: 'anthropic' },
  { source: GEMINI_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' },
  { source: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, upstream: GEMINI_GENERATE_CONTENT_FAMILY, upstreamProfile: 'gemini' }
] as const

export function accountModelMappingProtocolValidationMessage(input: {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
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

  if (!openAIProfile && !anthropicProfile && !geminiProfile) {
    return '当前供应商协议不支持模型映射'
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
  if (rule.requiresNativeResponses && !hasNativeResponsesEndpointMode(context.supportedEndpointModes ?? [])) {
    return 'Responses 模型别名只能用于账号真实支持 Responses API 的原生上游'
  }
  return undefined
}

export function isAccountModelMappingProtocolAllowed(input: {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  context: AccountModelMappingProtocolContext
}): boolean {
  return !accountModelMappingProtocolValidationMessage(input)
}

export function isAccountModelMappingSourceEndpointFamilyAllowed(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  context: AccountModelMappingProtocolContext
): boolean {
  return accountModelMappingProtocolRules.some((rule) => (
    rule.source === sourceEndpointFamily
    && isAccountModelMappingProtocolAllowed({
      sourceEndpointFamily,
      upstreamEndpointFamily: rule.upstream,
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
  return value.some((mode) => responsesEndpointModes.includes(mode))
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

function unsupportedProtocolConversionMessage(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): string {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return '账号模型别名不支持 Anthropic Messages 跨协议映射，请改用 API Key 显式混合路由'
  }
  if (isGeminiGenerateContentMappingSource(sourceEndpointFamily)) {
    return '账号模型别名不支持 Gemini GenerateContent 跨协议映射，请改用 API Key 显式混合路由'
  }
  return `账号模型别名只支持同协议映射；跨协议 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 到 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 请改用 API Key 显式混合路由`
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
    ? [OPENAI_RESPONSES_FAMILY]
    : [OPENAI_CHAT_COMPLETIONS_FAMILY]
}
