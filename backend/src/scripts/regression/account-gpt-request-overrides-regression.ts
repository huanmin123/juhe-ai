import assert from 'node:assert/strict'

import {
  assertAccountGptRequestOverridesSupportedByCatalog
} from '../../modules/accounts/account-gpt-request-overrides.validation.js'
import type { ProviderModelCatalogItem } from '../../modules/model-pricing/model-catalog.service.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'

const catalog = [
  modelItem('gpt-a', ['priority', 'flex'], ['low', 'high']),
  modelItem('gpt-b', ['priority'], ['low', 'medium'])
]

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: {
    serviceTier: 'priority',
    reasoningEffort: 'low'
  },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), 'API Key 覆盖必须按全部支持模型的能力交集校验')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), /gpt-b/, '任一支持模型缺少 Flex 时账户不能保存 Flex 覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { reasoningEffort: 'high' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), /gpt-b/, '任一支持模型缺少思考级别时账户不能保存该覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'default' },
  supportedModels: ['gpt-unknown'],
  catalog
}), /模型目录缺少/, '未知支持模型不能绕过能力交集校验')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'oauth',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a'],
  catalog
}), /OAuth.*Flex/, 'OAuth 账户必须禁止 Flex')

const normalizedApiKeyCredentials = normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  service_tier_override: 'priority',
  reasoning_effort_override: 'low'
}, {
  providerCode: 'gpt',
  accountType: 'api_key',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
assert.equal(normalizedApiKeyCredentials.service_tier_override, 'priority')
assert.equal(normalizedApiKeyCredentials.reasoning_effort_override, 'low')

assert.throws(() => normalizeAccountCredentialsForWrite('oauth', {
  refresh_token: 'refresh-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  service_tier_override: 'flex'
}, {
  providerCode: 'gpt',
  accountType: 'oauth',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}), /OAuth.*Flex/)

assert.throws(() => normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  reasoning_effort_override: 'ultra'
}, {
  providerCode: 'gpt',
  accountType: 'api_key',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}), /思考级别覆盖无效/, 'Ultra 只能用于 Codex 模型能力，不能作为账户 wire 覆盖')

console.log('GPT 账户请求覆盖回归通过：能力按全部支持模型取交集，OAuth 禁止 Flex，账户覆盖禁止 Ultra')

function modelItem(
  model: string,
  supportedServiceTiers: Array<'priority' | 'flex'>,
  supportedReasoningEfforts: Array<'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'>
): ProviderModelCatalogItem {
  return {
    providerCode: 'gpt',
    model,
    supportedServiceTiers,
    supportedReasoningEfforts
  } as ProviderModelCatalogItem
}
