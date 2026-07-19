import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type { AccountModelMappingSourceEndpointFamily, AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  listProviderModelCatalogAsync,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'
import type { ProviderModelApiProtocol } from '../model-pricing/provider-driver.types.js'
import { accountManualTestEndpointModes } from './account-test-endpoint-modes.js'
import { resolveOpenAIAccountModelMapping } from '../gateway/protocols/openai-v1/model-mapping.js'

export interface AccountManualTestOption {
  model: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  testEndpointModes: AccountSupportedEndpointMode[]
}

export interface AccountManualTestOptions {
  accountId: string
  defaultModel: string
  models: AccountManualTestOption[]
  testEndpointModes: AccountSupportedEndpointMode[]
  defaultTestEndpointMode: AccountSupportedEndpointMode
}

export async function accountManualTestOptionsAsync(account: AccountSummary): Promise<AccountManualTestOptions> {
  const systemAccountId = account.ownerSystemAccountId ?? account.systemAccountId
  if (!systemAccountId) {
    throw new Error('账户归属数据异常，无法读取测试模型')
  }
  const catalog = await listProviderModelCatalogAsync({
    providerCode: account.providerCode,
    systemAccountId,
    includeUnpriced: true
  })
  const accountEndpointModes = accountManualTestEndpointModes(account)
  const eligibleModels = catalog
    .filter((item) => item.status === 'active' && isAccountManualTestModel(item, account))
    .map((item) => ({
      model: item.model,
      supportedApiProtocols: [...(item.supportedApiProtocols ?? [])],
      testEndpointModes: accountManualTestEndpointModesForModel(account, item, catalog, accountEndpointModes)
    }))
  const defaultModel = eligibleModels.find((item) => item.model === account.healthCheckModel)
  if (!defaultModel) {
    throw new Error(`账户检查模型已不在当前供应商可用目录中，请先修正账户检查模型：${account.healthCheckModel}`)
  }
  const testEndpointModes = defaultModel.testEndpointModes
  const defaultTestEndpointMode = testEndpointModes[0]
  if (!defaultTestEndpointMode) {
    throw new Error('账户上游接口能力中没有可用于连接测试的请求形态')
  }
  const models = eligibleModels.filter((item) => item.testEndpointModes.length > 0)
  return {
    accountId: account.id,
    defaultModel: account.healthCheckModel,
    models,
    testEndpointModes,
    defaultTestEndpointMode
  }
}

export async function resolveAccountManualTestSelectionAsync(
  account: AccountSummary,
  modelInput: unknown,
  testEndpointMode?: AccountSupportedEndpointMode
): Promise<{ model: string; testEndpointMode: AccountSupportedEndpointMode }> {
  const model = typeof modelInput === 'string' ? modelInput.trim() : ''
  if (!model) {
    throw new Error('请选择测试模型')
  }
  const options = await accountManualTestOptionsAsync(account)
  const option = options.models.find((item) => item.model === model)
  if (!option) {
    throw new Error(`模型不在当前账户供应商可用目录中：${model}`)
  }
  const resolvedTestEndpointMode = testEndpointMode ?? option.testEndpointModes[0]
  if (!resolvedTestEndpointMode || !option.testEndpointModes.includes(resolvedTestEndpointMode)) {
    throw new Error(`模型 ${model} 不支持本次检查协议：${testEndpointMode ?? '未选择'}`)
  }
  return { model, testEndpointMode: resolvedTestEndpointMode }
}

export function accountManualTestEndpointModesForModel(
  account: AccountSummary,
  model: ProviderModelCatalogItem,
  catalog: ProviderModelCatalogItem[],
  accountEndpointModes = accountManualTestEndpointModes(account)
): AccountSupportedEndpointMode[] {
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
  if (mode === 'chat_json' || mode === 'chat_sse') return 'chat_completions'
  if (mode === 'responses_json' || mode === 'responses_sse') return 'responses'
  if (mode === 'messages_json' || mode === 'messages_sse') return 'messages'
  return mode === 'generate_content_sse' ? 'stream_generate_content' : 'generate_content'
}

function modelSupportsProtocol(item: ProviderModelCatalogItem, protocol: ProviderModelApiProtocol): boolean {
  const protocols = item.supportedApiProtocols ?? []
  return protocols.length === 0 || protocols.includes(protocol)
}

function isAccountManualTestModel(item: ProviderModelCatalogItem, account: AccountSummary): boolean {
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
