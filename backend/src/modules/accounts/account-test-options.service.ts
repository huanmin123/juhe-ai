import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type { AccountSummary } from '../../domain/types.js'
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
  return {
    accountId: account.id,
    defaultModel: account.healthCheckModel,
    models
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
