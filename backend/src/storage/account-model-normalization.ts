import type { AccountModelMapping, AccountSupportedEndpointMode } from '../domain/types.js'
import { listProviderModelCatalog, listProviderModelCatalogAsync } from '../modules/model-pricing/model-catalog.service.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  isGptVendorCode,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile,
  normalizeProviderToken,
  type ProviderProtocolProfileDefinition
} from '../domain/provider-protocol.js'
import { normalizeAccountModelMappingsInput } from './account-model-mappings.repository.js'
import {
  assertAccountModelMappingEndpointFamilyValues,
  assertHybridAccountModelMappingProtocolAllowed,
  assertAccountModelMappingProtocolAllowed
} from './account-model-mapping-protocol-matrix.js'
import { normalizeAccountSupportedModelsInput } from './account-supported-models.repository.js'
import type {
  AccountModelValidationContext,
  AccountModelValidationFact
} from './account-model-validation.repository.js'
import { isOpenAIProtocolProviderCode, isOpenAIProtocolProviderCodeAsync, listAnthropicProtocolProviderCodes, listAnthropicProtocolProviderCodesAsync, listGeminiProtocolProviderCodes, listGeminiProtocolProviderCodesAsync, listOpenAIProtocolProviderCodes, listOpenAIProtocolProviderCodesAsync } from './provider.repository.js'

export function normalizeAccountSupportedModelsForProvider(
  value: unknown,
  providerCode: string,
  systemAccountId: string,
  providerProfile?: ProviderProtocolProfileDefinition,
  filterIncompatibleDefaults = false
): string[] | undefined {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models
  if (isHybridProviderCode(providerCode)) return models

  const allProviderModels = listProviderModelCatalog({
    providerCode,
    systemAccountId,
    includeUnpriced: true
  })
  const providerModels = new Set(allProviderModels.filter((item) => providerModelSupportsProtocolProfile(item.supportedApiProtocols, providerProfile)).map((item) => item.model))
  const allModelIds = new Set(allProviderModels.map((item) => item.model))
  const invalidModels = models.filter((model) => !providerModels.has(model) && !(filterIncompatibleDefaults && allModelIds.has(model)))
  if (invalidModels.length > 0) {
    throw new Error(`账户支持模型不在供应商模型目录中：${invalidModels.slice(0, 5).join('、')}`)
  }
  return filterIncompatibleDefaults ? models.filter((model) => providerModels.has(model)) : models
}

export async function normalizeAccountSupportedModelsForProviderAsync(
  value: unknown,
  providerCode: string,
  systemAccountId: string,
  providerProfile?: ProviderProtocolProfileDefinition,
  filterIncompatibleDefaults = false,
  validationContext?: AccountModelValidationContext
): Promise<string[] | undefined> {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models
  if (isHybridProviderCode(providerCode)) return models

  const allProviderModels = validationContext
    ? models
        .map((model) => validationContext.accountModel(model))
        .filter((item): item is AccountModelValidationFact => item !== undefined)
    : await listProviderModelCatalogAsync({
        providerCode,
        systemAccountId,
        includeUnpriced: true
      })
  const providerModels = new Set(allProviderModels
    .filter((item) => providerModelSupportsProtocolProfile(item.supportedApiProtocols, providerProfile))
    .map((item) => item.model))
  const allModelIds = validationContext
    ? validationContext.accountModelIds()
    : new Set(allProviderModels.map((item) => item.model))
  const invalidModels = models.filter((model) => !providerModels.has(model) && !(filterIncompatibleDefaults && allModelIds.has(model)))
  if (invalidModels.length > 0) {
    throw new Error(`账户支持模型不在供应商模型目录中：${invalidModels.slice(0, 5).join('、')}`)
  }
  return filterIncompatibleDefaults ? models.filter((model) => providerModels.has(model)) : models
}

export function providerModelSupportsProtocolProfile(
  modelProtocols: readonly string[],
  providerProfile?: ProviderProtocolProfileDefinition
): boolean {
  if (!providerProfile || modelProtocols.length === 0) return true
  if (isGptVendorCode(providerProfile.providerCode)) return true
  const profileProtocols = new Set<string>(
    (providerProfile.endpointFamilies ?? [])
      .map((family) => typeof family === 'string' ? family : family.code)
      .filter((value): value is string => Boolean(value))
  )
  if (profileProtocols.size === 0) {
    if (isOpenAIProtocolProfile(providerProfile)) {
      profileProtocols.add(OPENAI_CHAT_COMPLETIONS_FAMILY)
      profileProtocols.add(OPENAI_RESPONSES_FAMILY)
    } else if (isAnthropicProtocolProfile(providerProfile)) {
      profileProtocols.add(ANTHROPIC_MESSAGES_FAMILY)
    } else if (isGeminiProtocolProfile(providerProfile)) {
      profileProtocols.add(GEMINI_GENERATE_CONTENT_FAMILY)
      profileProtocols.add(GEMINI_STREAM_GENERATE_CONTENT_FAMILY)
      profileProtocols.add(GEMINI_COUNT_TOKENS_FAMILY)
      profileProtocols.add(GEMINI_EMBED_CONTENT_FAMILY)
      profileProtocols.add('interactions')
    }
  }
  return modelProtocols.some((protocol) => profileProtocols.has(protocol))
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
    providerCode,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  }
  if (isHybridProviderCode(providerCode)) {
    for (const mapping of mappings) {
      assertHybridAccountModelMappingProtocolAllowed(mapping, {
        providerProfile: normalizedProfile,
        supportedEndpointModes: options.supportedEndpointModes
      })
    }
    assertMappingModelsInProtocolPools(mappings, systemAccountId)
    return mappings
  }
  for (const mapping of mappings) {
    assertAccountModelMappingProtocolAllowed(mapping, {
      providerProfile: normalizedProfile,
      supportedEndpointModes: options.supportedEndpointModes
    })
  }

  const accountModelPool = upstreamModelPoolForAccount(providerCode, systemAccountId, normalizedProfile)
  const sourceModelPool = accountModelPool
  const invalidSourceModels = mappings
    .filter((mapping) => !sourceModelPool.has(mapping.sourceModel))
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
  const invalidUpstreamProtocolModels = mappings
    .filter((mapping) => !accountEndpointModelPoolForAccount(providerCode, systemAccountId, normalizedProfile, mapping.upstreamEndpointFamily).has(mapping.upstreamModel))
    .map((mapping) => mapping.upstreamModel)
  if (invalidUpstreamProtocolModels.length > 0) {
    throw new Error(`账号模型别名目标模型不支持对应上游协议：${invalidUpstreamProtocolModels.slice(0, 5).join('、')}`)
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
    validationContext?: AccountModelValidationContext
  } = {}
): Promise<AccountModelMapping[] | undefined> {
  const mappings = normalizeAccountModelMappingsInput(value)
  if (!mappings?.length) return mappings

  const normalizedProfile = providerProfile ?? {
    providerCode,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  }
  if (isHybridProviderCode(providerCode)) {
    for (const mapping of mappings) {
      assertHybridAccountModelMappingProtocolAllowed(mapping, {
        providerProfile: normalizedProfile,
        supportedEndpointModes: options.supportedEndpointModes
      })
    }
    if (options.validationContext) {
      assertMappingModelsInValidationContext(mappings, options.validationContext)
    } else {
      await assertMappingModelsInProtocolPoolsAsync(mappings, systemAccountId)
    }
    return mappings
  }
  for (const mapping of mappings) {
    assertAccountModelMappingProtocolAllowed(mapping, {
      providerProfile: normalizedProfile,
      supportedEndpointModes: options.supportedEndpointModes
    })
  }

  const selectedProfileSupportsMappings = isOpenAIProtocolProfile(normalizedProfile)
    || isAnthropicProtocolProfile(normalizedProfile)
    || isGeminiProtocolProfile(normalizedProfile)
  const accountModelPool = options.validationContext && selectedProfileSupportsMappings
    ? new Set(mappings
        .flatMap((mapping) => [mapping.sourceModel, mapping.upstreamModel])
        .filter((model) => options.validationContext?.accountModel(model)?.priced))
    : options.validationContext
      ? new Set<string>()
      : await upstreamModelPoolForAccountAsync(providerCode, systemAccountId, normalizedProfile)
  const sourceModelPool = accountModelPool
  const invalidSourceModels = mappings
    .filter((mapping) => !sourceModelPool.has(mapping.sourceModel))
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
  const invalidUpstreamProtocolModels: string[] = options.validationContext && selectedProfileSupportsMappings
    ? mappings
        .filter((mapping) => !options.validationContext?.endpointModel(mapping.upstreamEndpointFamily, mapping.upstreamModel)?.priced)
        .map((mapping) => mapping.upstreamModel)
    : options.validationContext ? mappings.map((mapping) => mapping.upstreamModel) : []
  if (!options.validationContext) {
    for (const mapping of mappings) {
      const upstreamModelPool = await accountEndpointModelPoolForAccountAsync(providerCode, systemAccountId, normalizedProfile, mapping.upstreamEndpointFamily)
      if (!upstreamModelPool.has(mapping.upstreamModel)) {
        invalidUpstreamProtocolModels.push(mapping.upstreamModel)
      }
    }
  }
  if (invalidUpstreamProtocolModels.length > 0) {
    throw new Error(`账号模型别名目标模型不支持对应上游协议：${invalidUpstreamProtocolModels.slice(0, 5).join('、')}`)
  }
  return mappings
}

export function assertAccountModelMappingUpstreamsAllowedBySupportedModels(
  mappings: AccountModelMapping[],
  supportedModels: string[]
): void {
  const supportedModelSet = new Set(supportedModels.map((model) => model.trim()).filter(Boolean))
  if (!supportedModelSet.size || !mappings.length) return

  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !supportedModelSet.has(model.trim()))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`映射上游模型只能选择账户支持模型：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
}

export function assertAccountSupportedModelsRequired(supportedModels: readonly string[]): void {
  if (supportedModels.some((model) => model.trim())) return
  throw new Error('账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型')
}

function openAIProtocolEndpointModelPool(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listOpenAIProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

async function openAIProtocolEndpointModelPoolAsync(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listOpenAIProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

function anthropicProtocolEndpointModelPool(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listAnthropicProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

async function anthropicProtocolEndpointModelPoolAsync(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listAnthropicProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

function geminiProtocolEndpointModelPool(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Set<string> {
  const models = new Set<string>()
  for (const providerCode of listGeminiProtocolProviderCodes()) {
    for (const item of listProviderModelCatalog({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

async function geminiProtocolEndpointModelPoolAsync(endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  const models = new Set<string>()
  for (const providerCode of await listGeminiProtocolProviderCodesAsync()) {
    for (const item of await listProviderModelCatalogAsync({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })) {
      if (item.supportedApiProtocols.includes(endpointFamily)) {
        models.add(item.model)
      }
    }
  }
  return models
}

function upstreamModelPoolForAccount(providerCode: string, systemAccountId: string, providerProfile: ProviderProtocolProfileDefinition): Set<string> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
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

function accountEndpointModelPoolForAccount(
  providerCode: string,
  systemAccountId: string,
  providerProfile: ProviderProtocolProfileDefinition,
  endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily']
): Set<string> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
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
    if (item.supportedApiProtocols.includes(endpointFamily)) {
      models.add(item.model)
    }
  }
  return models
}

function sourceModelPoolForMapping(sourceEndpointFamily: AccountModelMapping['sourceEndpointFamily'], systemAccountId: string): Set<string> {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolEndpointModelPool(sourceEndpointFamily, systemAccountId)
  }
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolEndpointModelPool(sourceEndpointFamily, systemAccountId)
  }
  return openAIProtocolEndpointModelPool(sourceEndpointFamily, systemAccountId)
}

function upstreamProtocolModelPoolForMapping(upstreamEndpointFamily: AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Set<string> {
  if (upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolEndpointModelPool(upstreamEndpointFamily, systemAccountId)
  }
  if (upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolEndpointModelPool(upstreamEndpointFamily, systemAccountId)
  }
  return openAIProtocolEndpointModelPool(upstreamEndpointFamily, systemAccountId)
}

function assertMappingModelsInProtocolPools(mappings: AccountModelMapping[], systemAccountId: string): void {
  const invalidSourceModels: string[] = []
  const invalidUpstreamModels: string[] = []
  for (const mapping of mappings) {
    if (!sourceModelPoolForMapping(mapping.sourceEndpointFamily, systemAccountId).has(mapping.sourceModel)) {
      invalidSourceModels.push(mapping.sourceModel)
    }
    if (!upstreamProtocolModelPoolForMapping(mapping.upstreamEndpointFamily, systemAccountId).has(mapping.upstreamModel)) {
      invalidUpstreamModels.push(mapping.upstreamModel)
    }
  }
  if (invalidSourceModels.length > 0) {
    throw new Error(`账号模型别名来源模型不在对应协议模型池中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账号模型别名目标模型不在对应上游协议模型池中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
}

async function upstreamModelPoolForAccountAsync(providerCode: string, systemAccountId: string, providerProfile: ProviderProtocolProfileDefinition): Promise<Set<string>> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
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

async function accountEndpointModelPoolForAccountAsync(
  providerCode: string,
  systemAccountId: string,
  providerProfile: ProviderProtocolProfileDefinition,
  endpointFamily: AccountModelMapping['sourceEndpointFamily'] | AccountModelMapping['upstreamEndpointFamily']
): Promise<Set<string>> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
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
    if (item.supportedApiProtocols.includes(endpointFamily)) {
      models.add(item.model)
    }
  }
  return models
}

async function sourceModelPoolForMappingAsync(sourceEndpointFamily: AccountModelMapping['sourceEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolEndpointModelPoolAsync(sourceEndpointFamily, systemAccountId)
  }
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolEndpointModelPoolAsync(sourceEndpointFamily, systemAccountId)
  }
  return openAIProtocolEndpointModelPoolAsync(sourceEndpointFamily, systemAccountId)
}

async function upstreamProtocolModelPoolForMappingAsync(upstreamEndpointFamily: AccountModelMapping['upstreamEndpointFamily'], systemAccountId: string): Promise<Set<string>> {
  if (upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicProtocolEndpointModelPoolAsync(upstreamEndpointFamily, systemAccountId)
  }
  if (upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY) {
    return geminiProtocolEndpointModelPoolAsync(upstreamEndpointFamily, systemAccountId)
  }
  return openAIProtocolEndpointModelPoolAsync(upstreamEndpointFamily, systemAccountId)
}

async function assertMappingModelsInProtocolPoolsAsync(mappings: AccountModelMapping[], systemAccountId: string): Promise<void> {
  const invalidSourceModels: string[] = []
  const invalidUpstreamModels: string[] = []
  for (const mapping of mappings) {
    const sourceModelPool = await sourceModelPoolForMappingAsync(mapping.sourceEndpointFamily, systemAccountId)
    if (!sourceModelPool.has(mapping.sourceModel)) {
      invalidSourceModels.push(mapping.sourceModel)
    }
    const upstreamModelPool = await upstreamProtocolModelPoolForMappingAsync(mapping.upstreamEndpointFamily, systemAccountId)
    if (!upstreamModelPool.has(mapping.upstreamModel)) {
      invalidUpstreamModels.push(mapping.upstreamModel)
    }
  }
  if (invalidSourceModels.length > 0) {
    throw new Error(`账号模型别名来源模型不在对应协议模型池中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账号模型别名目标模型不在对应上游协议模型池中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
}

function assertMappingModelsInValidationContext(
  mappings: readonly AccountModelMapping[],
  context: AccountModelValidationContext
): void {
  const invalidSourceModels = mappings
    .filter((mapping) => !context.endpointModel(mapping.sourceEndpointFamily, mapping.sourceModel)?.priced)
    .map((mapping) => mapping.sourceModel)
  const invalidUpstreamModels = mappings
    .filter((mapping) => !context.endpointModel(mapping.upstreamEndpointFamily, mapping.upstreamModel)?.priced)
    .map((mapping) => mapping.upstreamModel)
  if (invalidSourceModels.length > 0) {
    throw new Error(`账号模型别名来源模型不在对应协议模型池中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`账号模型别名目标模型不在对应上游协议模型池中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
}

export function assertAccountModelMappingEndpointFamilies(mappings: AccountModelMapping[]): void {
  assertAccountModelMappingEndpointFamilyValues(mappings)
}
