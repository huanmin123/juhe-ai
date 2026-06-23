import type { AccountModelMapping } from '../domain/types.js'
import { listProviderModelCatalog } from '../modules/model-pricing/model-catalog.service.js'
import {
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  isOpenAIProtocolProfile,
  normalizeProviderToken,
  type ProviderProtocolProfileDefinition
} from '../domain/provider-protocol.js'
import { normalizeAccountModelMappingsInput } from './account-model-mappings.repository.js'
import { normalizeAccountSupportedModelsInput } from './account-supported-models.repository.js'
import { isOpenAIProtocolProviderCode, listOpenAIProtocolProviderCodes } from './provider.repository.js'

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
  providerProfile?: ProviderProtocolProfileDefinition
): AccountModelMapping[] | undefined {
  const mappings = normalizeAccountModelMappingsInput(value)
  if (!mappings?.length) return mappings

  if (!isOpenAIProtocolProfile(providerProfile ?? {
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  })) {
    throw new Error('当前供应商协议不支持 OpenAI 模型映射')
  }

  const sourceModels = openAIProtocolModelPool(systemAccountId)
  const invalidSourceModels = mappings
    .map((mapping) => mapping.sourceModel)
    .filter((model) => !sourceModels.has(model))
  if (invalidSourceModels.length > 0) {
    throw new Error(`映射下游模型不在 OpenAI 协议客户端模型池中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  const upstreamModels = upstreamModelPoolForAccount(providerCode, systemAccountId)
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
      systemAccountId
    })) {
      models.add(item.model)
    }
  }
  return models
}

function upstreamModelPoolForAccount(providerCode: string, systemAccountId: string): Set<string> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (normalizedProviderCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return openAIProtocolModelPool(systemAccountId)
  }
  const models = new Set<string>()
  if (!normalizedProviderCode || !isOpenAIProtocolProviderCode(normalizedProviderCode)) {
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
    if (mapping.sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && mapping.sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) {
      throw new Error(`映射下游协议不支持：${mapping.sourceEndpointFamily}`)
    }
    if (mapping.upstreamEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && mapping.upstreamEndpointFamily !== OPENAI_RESPONSES_FAMILY) {
      throw new Error(`映射上游协议不支持：${mapping.upstreamEndpointFamily}`)
    }
  }
}
