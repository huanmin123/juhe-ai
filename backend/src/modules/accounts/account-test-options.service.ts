import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type { AccountModelMappingSourceEndpointFamily, AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  findProviderModelTestCatalogItemAsync,
  type ProviderModelTestCatalogItem,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'
import {
  listProviderModelOptionRowsAsync,
  mergeProviderModelOptionRows,
  normalizeProviderModelOptionQuery,
  type ProviderModelOptionQuery,
  type ProviderModelOptionRow
} from '../providers/provider-model-options.service.js'
import type { ProviderModelApiProtocol } from '../model-pricing/provider-driver.types.js'
import type {
  AccountManualTestCapabilitiesContext
} from '../../storage/account-manual-test-context.repository.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { accountManualTestEndpointModes } from './account-test-endpoint-modes.js'
import { resolveOpenAIAccountModelMapping } from '../gateway/protocols/openai-v1/model-mapping.js'

export interface AccountManualTestOption {
  id: string
  name: string
}

export interface AccountManualTestModelCapabilities extends AccountManualTestOption {
  testEndpointModes: AccountSupportedEndpointMode[]
}

export type AccountManualTestOptionsQuery = Pick<ProviderModelOptionQuery, 'keyword' | 'limit' | 'selectedIds'>

export function normalizeAccountManualTestOptionsQuery(query: Record<string, unknown>): AccountManualTestOptionsQuery {
  const normalized = normalizeProviderModelOptionQuery(query)
  return {
    ...(normalized.keyword ? { keyword: normalized.keyword } : {}),
    limit: normalized.limit,
    selectedIds: normalized.selectedIds
  }
}

type AccountManualTestCatalogContext = Pick<
  AccountSummary,
  'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type'
> & {
  ownerSystemAccountId?: string
  systemAccountId?: string
  healthCheckModel?: string
}

export async function accountManualTestOptionsAsync(
  account: AccountManualTestCatalogContext,
  query: AccountManualTestOptionsQuery = { limit: 50, selectedIds: [] }
): Promise<AccountManualTestOption[]> {
  const systemAccountId = account.ownerSystemAccountId ?? account.systemAccountId
  if (!systemAccountId) throw new Error('账户归属数据异常，无法读取测试模型')
  const selectedIds = [...new Set([
    ...query.selectedIds,
    typeof account.healthCheckModel === 'string' ? account.healthCheckModel.trim() : ''
  ].filter(Boolean))]
  const catalog = await listProviderModelOptionRowsAsync({
    providerCode: account.providerCode,
    systemAccountId,
    ...query,
    selectedIds
  })
  const eligible = catalog.filter((item) => isAccountManualTestModel(item, account))
  return mergeProviderModelOptionRows(eligible, {
    providerCode: account.providerCode,
    ...(query.keyword ? { keyword: query.keyword } : {}),
    limit: query.limit,
    selectedIds
  })
}

export async function resolveAccountManualTestSelectionAsync(
  account: AccountSummary | AccountManualTestCapabilitiesContext,
  modelInput: unknown,
  testEndpointMode?: AccountSupportedEndpointMode
): Promise<{ model: string; testEndpointMode: AccountSupportedEndpointMode }> {
  const model = typeof modelInput === 'string' ? modelInput.trim() : ''
  if (!model) {
    throw new Error('请选择测试模型')
  }
  const option = await accountManualTestModelCapabilitiesAsync(account, model)
  const resolvedTestEndpointMode = option.testEndpointModes.includes('images_json')
    ? 'images_json'
    : testEndpointMode ?? option.testEndpointModes[0]
  if (!resolvedTestEndpointMode || !option.testEndpointModes.includes(resolvedTestEndpointMode)) {
    throw new Error(`模型 ${model} 不支持本次检查协议：${testEndpointMode ?? '未选择'}`)
  }
  return { model, testEndpointMode: resolvedTestEndpointMode }
}

export function accountManualTestCapabilitiesContextFromDraft(
  account: AccountTestDraftSnapshot
): AccountManualTestCapabilitiesContext {
  return {
    id: account.id,
    factAccountId: account.stateTargetAccountId ?? account.id,
    ownerSystemAccountId: account.ownerSystemAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    clientCompatibility: account.clientCompatibility,
    healthCheckModel: account.healthCheckModel,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    supportedEndpointModes: accountManualTestEndpointModes(account),
    modelMappings: account.modelMappings ?? []
  }
}

export async function accountManualTestModelCapabilitiesAsync(
  account: AccountSummary | AccountManualTestCapabilitiesContext,
  modelInput: string
): Promise<AccountManualTestModelCapabilities> {
  const model = modelInput.trim()
  if (!model) throw new Error('请选择测试模型')
  const systemAccountId = account.ownerSystemAccountId ?? ('systemAccountId' in account ? account.systemAccountId : undefined)
  if (!systemAccountId) {
    throw new Error('账户归属数据异常，无法读取测试模型')
  }
  const item = await findProviderModelTestCatalogItemAsync({
    providerCode: account.providerCode,
    systemAccountId,
    model,
    protocolsOnly: true
  })
  if (!item || !isAccountManualTestModel(item, account)) {
    throw new Error(`模型不在当前账户供应商可用目录中：${model}`)
  }
  const testEndpointModes = await accountManualTestEndpointModesForTargetModelAsync(account, item, systemAccountId)
  if (!testEndpointModes.length) {
    throw new Error('账户上游接口能力中没有可用于连接测试的请求形态')
  }
  return { id: item.model, name: item.model, testEndpointModes }
}

async function accountManualTestEndpointModesForTargetModelAsync(
  account: AccountSummary | AccountManualTestCapabilitiesContext,
  model: ProviderModelTestCatalogItem,
  systemAccountId: string
): Promise<AccountSupportedEndpointMode[]> {
  if (isImageGenerationManualTestModel(model, account)) return ['images_json']
  const accountEndpointModes = accountManualTestEndpointModes({
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    clientCompatibility: account.clientCompatibility,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    credentials: {
      supported_endpoint_modes: 'supportedEndpointModes' in account
        ? account.supportedEndpointModes
        : account.credentials.supported_endpoint_modes
    }
  })
  const upstreamModels = new Map<string, ProviderModelTestCatalogItem | undefined>()
  const output: AccountSupportedEndpointMode[] = []
  for (const mode of accountEndpointModes) {
    if (mode === 'interactions_json' || mode === 'interactions_sse') {
      if (modelSupportsProtocol(model, 'interactions')) output.push(mode)
      continue
    }
    const sourceFamily = endpointModeProtocol(mode)
    const mapping = resolveOpenAIAccountModelMapping(account, model.model, sourceFamily)
    if (!mapping) {
      if (modelSupportsProtocol(model, sourceFamily)) output.push(mode)
      continue
    }
    let upstreamModel = upstreamModels.get(mapping.upstreamModel)
    if (!upstreamModels.has(mapping.upstreamModel)) {
      upstreamModel = await findProviderModelTestCatalogItemAsync({
        providerCode: account.providerCode,
        systemAccountId,
        model: mapping.upstreamModel,
        protocolsOnly: true
      })
      upstreamModels.set(mapping.upstreamModel, upstreamModel)
    }
    if (
      modelSupportsProtocol(model, sourceFamily)
      && (!upstreamModel || modelSupportsProtocol(upstreamModel, mapping.upstreamEndpointFamily))
    ) {
      output.push(mode)
    }
  }
  return output
}

export function accountManualTestEndpointModesForModel(
  account: AccountSummary,
  model: ProviderModelCatalogItem,
  catalog: ProviderModelCatalogItem[],
  accountEndpointModes = accountManualTestEndpointModes(account)
): AccountSupportedEndpointMode[] {
  if (isImageGenerationManualTestModel(model, account)) return ['images_json']
  return accountEndpointModes.filter((mode) => {
    if (mode === 'interactions_json' || mode === 'interactions_sse') {
      return modelSupportsProtocol(model, 'interactions')
    }
    const sourceFamily = endpointModeProtocol(mode)
    const mapping = resolveOpenAIAccountModelMapping(account, model.model, sourceFamily)
    if (!mapping) return modelSupportsProtocol(model, sourceFamily)
    const upstreamModel = catalog.find((item) => item.model === mapping.upstreamModel)
    return modelSupportsProtocol(model, sourceFamily)
      && (!upstreamModel || modelSupportsProtocol(upstreamModel, mapping.upstreamEndpointFamily))
  })
}

function endpointModeProtocol(mode: AccountSupportedEndpointMode): AccountModelMappingSourceEndpointFamily {
  if (mode === 'images_json') throw new Error('图片生成测试不使用文本模型映射协议')
  if (mode === 'chat_json' || mode === 'chat_sse') return 'chat_completions'
  if (mode === 'responses_json' || mode === 'responses_sse') return 'responses'
  if (mode === 'messages_json' || mode === 'messages_sse') return 'messages'
  return mode === 'generate_content_sse' ? 'stream_generate_content' : 'generate_content'
}

function modelSupportsProtocol(
  item: Pick<ProviderModelCatalogItem, 'supportedApiProtocols'> | ProviderModelTestCatalogItem,
  protocol: ProviderModelApiProtocol
): boolean {
  const protocols = item.supportedApiProtocols ?? []
  return protocols.length === 0 || protocols.includes(protocol)
}

function isAccountManualTestModel(
  item: Pick<ProviderModelCatalogItem, 'mode' | 'supportedApiProtocols'> | ProviderModelTestCatalogItem | ProviderModelOptionRow,
  account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type'>
): boolean {
  if (isImageGenerationManualTestModel(item, account)) return true
  if (item.mode === 'image' || item.mode === 'audio') return false
  const protocols = item.supportedApiProtocols ?? []
  if (!protocols.length) return true
  if (isHybridProviderCode(account.providerCode)) {
    return protocols.some((protocol) => (
      protocol === 'chat_completions'
      || protocol === 'responses'
      || protocol === 'messages'
      || protocol === 'generate_content'
      || protocol === 'stream_generate_content'
    ))
  }
  if (isOpenAIProtocolProfile(account)) {
    return protocols.some((protocol) => protocol === 'chat_completions' || protocol === 'responses')
  }
  if (isAnthropicProtocolProfile(account)) {
    return protocols.includes('messages')
  }
  if (isGeminiProtocolProfile(account)) {
    return protocols.some((protocol) => protocol === 'generate_content' || protocol === 'stream_generate_content' || protocol === 'interactions')
  }
  return false
}

function isImageGenerationManualTestModel(
  item: Pick<ProviderModelCatalogItem, 'mode' | 'supportedApiProtocols'> | ProviderModelTestCatalogItem | ProviderModelOptionRow,
  account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type'>
): boolean {
  return account.type === 'api_key'
    && isOpenAIProtocolProfile(account)
    && (item.mode === 'image_generation' || item.supportedApiProtocols?.includes('images') === true)
}
