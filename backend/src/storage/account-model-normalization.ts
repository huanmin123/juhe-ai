import type { AccountModelMapping, AccountSupportedEndpointMode } from '../domain/types.js'
import { listProviderModelCatalog } from '../modules/model-pricing/model-catalog.service.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  isAnthropicProtocolProfile,
  isOpenAIProtocolProfile,
  normalizeProviderToken,
  type ProviderProtocolProfileDefinition
} from '../domain/provider-protocol.js'
import { normalizeAccountModelMappingsInput } from './account-model-mappings.repository.js'
import { normalizeAccountSupportedModelsInput } from './account-supported-models.repository.js'
import { isOpenAIProtocolProviderCode, listAnthropicProtocolProviderCodes, listGeminiProtocolProviderCodes, listOpenAIProtocolProviderCodes } from './provider.repository.js'

export function normalizeAccountSupportedModelsForProvider(value: unknown, providerCode: string, systemAccountId: string): string[] | undefined {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models

  const providerModels = new Set(listProviderModelCatalog({
    providerCode,
    systemAccountId
  }).map((item) => item.model))
  const invalidModels = models.filter((model) => !providerModels.has(model))
  if (invalidModels.length > 0) {
    throw new Error(`账户支持模型不在供应商模型目录中：${invalidModels.slice(0, 5).join('、')}`)
  }
  return models
}

export function normalizeAccountModelMappingsForProvider(
  value: unknown,
  providerCode: string,
  systemAccountId: string,
  providerProfile?: ProviderProtocolProfileDefinition,
  options: {
    supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  } = {}
): AccountModelMapping[] | undefined {
  const mappings = normalizeAccountModelMappingsInput(value)
  if (!mappings?.length) return mappings

  const normalizedProfile = providerProfile ?? {
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  }
  const profileId = normalizedProfile.providerProtocolProfileId ?? normalizedProfile.id
  const geminiOpenAIChatProfile = profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
  const openAIProfile = isOpenAIProtocolProfile(normalizedProfile)
  const anthropicProfile = isAnthropicProtocolProfile(normalizedProfile)
  if (!openAIProfile && !anthropicProfile) {
    throw new Error('当前供应商协议不支持模型映射')
  }
  for (const mapping of mappings) {
    if (geminiOpenAIChatProfile && mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
      throw new Error('Gemini OpenAI Chat 档案不支持 Anthropic Messages 来源映射；Codex 使用 Gemini 时只配置 Responses 到 Chat Completions')
    }
    if (geminiOpenAIChatProfile && mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
      throw new Error('Gemini OpenAI Chat 档案的模型映射上游协议只能是 Chat Completions')
    }
    if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
      if (mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
        throw new Error('Anthropic Messages 下游协议当前只支持显式桥接到 Chat Completions 上游')
      }
      if (!openAIProfile) {
        throw new Error('Anthropic Messages 到 Chat Completions 桥接只能配置在 OpenAI 协议档案账号上')
      }
      continue
    }
    if (isGeminiGenerateContentMappingSource(mapping.sourceEndpointFamily)) {
      if (mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
        throw new Error('Gemini GenerateContent 下游协议当前只支持显式桥接到 Chat Completions 上游')
      }
      if (!openAIProfile) {
        throw new Error('Gemini GenerateContent 到 Chat Completions 桥接只能配置在 OpenAI 协议档案账号上')
      }
      continue
    }
    if (mapping.upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY && !anthropicProfile) {
      throw new Error('只有 Anthropic Messages 协议档案可以把上游协议配置为 Messages')
    }
    if ((mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY || mapping.upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY) && !openAIProfile) {
      throw new Error('当前供应商协议不支持 OpenAI 模型映射：只有 OpenAI 协议档案可以把上游协议配置为 Chat Completions 或 Responses')
    }
    if (mapping.upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY && !hasNativeResponsesEndpointMode(options.supportedEndpointModes)) {
      throw new Error('上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游')
    }
  }

  const invalidSourceModels = mappings
    .filter((mapping) => !sourceModelPoolForMapping(mapping, systemAccountId).has(mapping.sourceModel))
    .map((mapping) => mapping.sourceModel)
  if (invalidSourceModels.length > 0) {
    throw new Error(`映射下游模型不在对应协议客户端模型池中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  const upstreamModels = upstreamModelPoolForAccount(providerCode, systemAccountId, normalizedProfile)
  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !upstreamModels.has(model))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`映射上游模型不在当前账号可用模型池中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
  return mappings
}

export function assertAccountModelMappingUpstreamsAllowedBySupportedModels(
  mappings: AccountModelMapping[],
  supportedModels: string[]
): void {
  const supportedModelSet = new Set(supportedModels.map((model) => model.trim().toLowerCase()).filter(Boolean))
  if (!supportedModelSet.size || !mappings.length) return

  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !supportedModelSet.has(model.trim().toLowerCase()))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账户已配置支持模型时，映射上游模型只能选择支持模型：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
}

export function openAIProtocolModelPool(systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listOpenAIProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      models.add(item.model)
    }
  }
  return models
}

export function anthropicProtocolModelPool(systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listAnthropicProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      models.add(item.model)
    }
  }
  return models
}

export function geminiProtocolModelPool(systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listGeminiProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      models.add(item.model)
    }
  }
  return models
}

function sourceModelPoolForMapping(mapping: AccountModelMapping, systemAccountId: string): Set<string> {
  if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolModelPool(systemAccountId)
  }
  if (isGeminiGenerateContentMappingSource(mapping.sourceEndpointFamily)) {
    return geminiProtocolModelPool(systemAccountId)
  }
  return openAIProtocolModelPool(systemAccountId)
}

function upstreamModelPoolForAccount(providerCode: string, systemAccountId: string, providerProfile: ProviderProtocolProfileDefinition): Set<string> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (normalizedProviderCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return openAIProtocolModelPool(systemAccountId)
  }
  const models = new Set<string>()
  if (!normalizedProviderCode) {
    return models
  }
  if (!isOpenAIProtocolProviderCode(normalizedProviderCode) && !isAnthropicProtocolProfile(providerProfile)) {
    return models
  }
  for (const item of listProviderModelCatalog({
    providerCode: normalizedProviderCode,
    systemAccountId
  })) {
    models.add(item.model)
  }
  return models
}

export function assertAccountModelMappingEndpointFamilies(mappings: AccountModelMapping[]): void {
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
    ) {
      throw new Error(`映射上游协议不支持：${mapping.upstreamEndpointFamily}`)
    }
    if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY && mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
      throw new Error('Anthropic Messages 下游协议当前只支持桥接到 Chat Completions 上游')
    }
    if (isGeminiGenerateContentMappingSource(mapping.sourceEndpointFamily) && mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) {
      throw new Error('Gemini GenerateContent 下游协议当前只支持桥接到 Chat Completions 上游')
    }
  }
}

function isGeminiGenerateContentMappingSource(value: AccountModelMapping['sourceEndpointFamily']): boolean {
  return value === GEMINI_GENERATE_CONTENT_FAMILY || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

function hasNativeResponsesEndpointMode(value: readonly AccountSupportedEndpointMode[] | undefined): boolean {
  return (value ?? []).some((mode) => mode === 'responses_json' || mode === 'responses_sse')
}
