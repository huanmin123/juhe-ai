import type { AccountModelMapping, AccountSupportedEndpointMode } from '../domain/types.js'
import { listProviderModelCatalog, listProviderModelCatalogAsync } from '../modules/model-pricing/model-catalog.service.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile,
  normalizeProviderToken,
  type ProviderProtocolProfileDefinition
} from '../domain/provider-protocol.js'
import { normalizeAccountModelMappingsInput } from './account-model-mappings.repository.js'
import {
  assertAccountModelMappingEndpointFamilyValues,
  assertAccountModelMappingProtocolAllowed
} from './account-model-mapping-protocol-matrix.js'
import { normalizeAccountSupportedModelsInput } from './account-supported-models.repository.js'
import { isOpenAIProtocolProviderCode, isOpenAIProtocolProviderCodeAsync, listAnthropicProtocolProviderCodes, listAnthropicProtocolProviderCodesAsync, listGeminiProtocolProviderCodes, listGeminiProtocolProviderCodesAsync, listOpenAIProtocolProviderCodes, listOpenAIProtocolProviderCodesAsync } from './provider.repository.js'

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

export async function normalizeAccountSupportedModelsForProviderAsync(value: unknown, providerCode: string, systemAccountId: string): Promise<string[] | undefined> {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models

  const providerModels = new Set((await listProviderModelCatalogAsync({
    providerCode,
    systemAccountId
  })).map((item) => item.model))
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
  for (const mapping of mappings) {
    assertAccountModelMappingProtocolAllowed(mapping, {
      providerProfile: normalizedProfile,
      supportedEndpointModes: options.supportedEndpointModes
    })
  }

  const accountModelPool = upstreamModelPoolForAccount(providerCode, systemAccountId, normalizedProfile)
  const invalidSourceModels = mappings
    .filter((mapping) => !accountModelPool.has(mapping.sourceModel))
    .map((mapping) => mapping.sourceModel)
  if (invalidSourceModels.length > 0) {
    throw new Error(`账号模型别名来源模型不在当前供应商模型目录中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !accountModelPool.has(model))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账号模型别名目标模型不在当前供应商模型目录中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
  return mappings
}

export async function normalizeAccountModelMappingsForProviderAsync(
  value: unknown,
  providerCode: string,
  systemAccountId: string,
  providerProfile?: ProviderProtocolProfileDefinition,
  options: {
    supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  } = {}
): Promise<AccountModelMapping[] | undefined> {
  const mappings = normalizeAccountModelMappingsInput(value)
  if (!mappings?.length) return mappings

  const normalizedProfile = providerProfile ?? {
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  }
  for (const mapping of mappings) {
    assertAccountModelMappingProtocolAllowed(mapping, {
      providerProfile: normalizedProfile,
      supportedEndpointModes: options.supportedEndpointModes
    })
  }

  const accountModelPool = await upstreamModelPoolForAccountAsync(providerCode, systemAccountId, normalizedProfile)
  const invalidSourceModels: string[] = []
  for (const mapping of mappings) {
    if (!accountModelPool.has(mapping.sourceModel)) {
      invalidSourceModels.push(mapping.sourceModel)
    }
  }
  if (invalidSourceModels.length > 0) {
    throw new Error(`账号模型别名来源模型不在当前供应商模型目录中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !accountModelPool.has(model))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账号模型别名目标模型不在当前供应商模型目录中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
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

export async function openAIProtocolModelPoolAsync(systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listOpenAIProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
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

export async function anthropicProtocolModelPoolAsync(systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listAnthropicProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
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

export async function geminiProtocolModelPoolAsync(systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listGeminiProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      models.add(item.model)
    }
  }
  return models
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
  if (!isOpenAIProtocolProviderCode(normalizedProviderCode) && !isAnthropicProtocolProfile(providerProfile) && !isGeminiProtocolProfile(providerProfile)) {
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

function sourceModelPoolForMapping(sourceEndpointFamily: AccountModelMapping['sourceEndpointFamily'], systemAccountId: string): Set<string> {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolModelPool(systemAccountId)
  }
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolModelPool(systemAccountId)
  }
  return openAIProtocolModelPool(systemAccountId)
}

async function upstreamModelPoolForAccountAsync(providerCode: string, systemAccountId: string, providerProfile: ProviderProtocolProfileDefinition): Promise<Set<string>> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (normalizedProviderCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return openAIProtocolModelPoolAsync(systemAccountId)
  }
  const models = new Set<string>()
  if (!normalizedProviderCode) {
    return models
  }
  if (!(await isOpenAIProtocolProviderCodeAsync(normalizedProviderCode)) && !isAnthropicProtocolProfile(providerProfile) && !isGeminiProtocolProfile(providerProfile)) {
    return models
  }
  for (const item of await listProviderModelCatalogAsync({
    providerCode: normalizedProviderCode,
    systemAccountId
  })) {
    models.add(item.model)
  }
  return models
}

async function sourceModelPoolForMappingAsync(sourceEndpointFamily: AccountModelMapping['sourceEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolModelPoolAsync(systemAccountId)
  }
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolModelPoolAsync(systemAccountId)
  }
  return openAIProtocolModelPoolAsync(systemAccountId)
}

export function assertAccountModelMappingEndpointFamilies(mappings: AccountModelMapping[]): void {
  assertAccountModelMappingEndpointFamilyValues(mappings)
}
