import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import { normalizeAnthropicEndpointModesForRuntime } from '../../domain/anthropic-endpoint-modes.js'
import { normalizeGeminiEndpointModesForRuntime } from '../../domain/gemini-endpoint-modes.js'
import { normalizeOpenAIEndpointModesForRuntime } from '../../domain/openai-endpoint-modes.js'
import type { AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  listProviderModelCatalogAsync,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'
import type { ProviderModelApiProtocol } from '../model-pricing/provider-driver.types.js'

export interface AccountManualTestOption {
  model: string
  supportedApiProtocols: ProviderModelApiProtocol[]
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
  const models = catalog
    .filter((item) => item.status === 'active' && isAccountManualTestModel(item, account))
    .map((item) => ({
      model: item.model,
      supportedApiProtocols: [...(item.supportedApiProtocols ?? [])]
    }))
  if (!models.some((item) => item.model === account.healthCheckModel)) {
    throw new Error(`账户检查模型已不在当前供应商可用目录中，请先修正账户检查模型：${account.healthCheckModel}`)
  }
  const testEndpointModes = accountManualTestEndpointModes(account)
  const defaultTestEndpointMode = testEndpointModes[0]
  if (!defaultTestEndpointMode) {
    throw new Error('账户接口能力限制中没有可用于连接测试的请求形态')
  }
  return {
    accountId: account.id,
    defaultModel: account.healthCheckModel,
    models,
    testEndpointModes,
    defaultTestEndpointMode
  }
}

export async function assertAccountManualTestModelAsync(account: AccountSummary, modelInput: unknown): Promise<string> {
  const model = typeof modelInput === 'string' ? modelInput.trim() : ''
  if (!model) {
    throw new Error('请选择测试模型')
  }
  const options = await accountManualTestOptionsAsync(account)
  if (!options.models.some((item) => item.model === model)) {
    throw new Error(`模型不在当前账户供应商可用目录中：${model}`)
  }
  return model
}

function isAccountManualTestModel(item: ProviderModelCatalogItem, account: AccountSummary): boolean {
  if (item.mode === 'image' || item.mode === 'audio') return false
  const protocols = item.supportedApiProtocols ?? []
  if (!protocols.length) return true
  if (isOpenAIProtocolProfile(account)) {
    return protocols.some((protocol) => protocol === 'chat_completions' || protocol === 'responses')
  }
  if (isAnthropicProtocolProfile(account)) {
    return protocols.includes('messages')
  }
  if (isGeminiProtocolProfile(account)) {
    return protocols.some((protocol) => protocol === 'generate_content' || protocol === 'stream_generate_content')
  }
  return false
}

function accountManualTestEndpointModes(account: AccountSummary): AccountSupportedEndpointMode[] {
  const modes = normalizedAccountEndpointModes(account)
  return accountTestEndpointModeOrder(account).filter((mode) => modes.includes(mode))
}

function normalizedAccountEndpointModes(account: AccountSummary): AccountSupportedEndpointMode[] {
  if (isAnthropicProtocolProfile(account)) {
    return normalizeAnthropicEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      providerProtocolProfileId: account.providerProtocolProfileId
    })
  }
  if (isGeminiProtocolProfile(account)) {
    return normalizeGeminiEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      providerProtocolProfileId: account.providerProtocolProfileId
    })
  }
  if (isOpenAIProtocolProfile(account)) {
    return normalizeOpenAIEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }
  return []
}

function accountTestEndpointModeOrder(account: AccountSummary): AccountSupportedEndpointMode[] {
  if (isAnthropicProtocolProfile(account)) {
    return ['messages_sse', 'messages_json']
  }
  if (isGeminiProtocolProfile(account)) {
    return ['generate_content_sse', 'generate_content_json']
  }
  if (account.type === 'oauth') {
    return ['responses_sse', 'responses_json']
  }
  return ['chat_sse', 'responses_sse', 'chat_json', 'responses_json']
}
