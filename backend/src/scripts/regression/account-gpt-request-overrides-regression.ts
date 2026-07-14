import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  assertAccountGptRequestOverridesSupportedByCatalog
} from '../../modules/accounts/account-gpt-request-overrides.validation.js'
import type { ProviderModelCatalogItem } from '../../modules/model-pricing/model-catalog.service.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'
import { runtimeOpenAIAccountCredentials } from '../../storage/openai-account-selector.repository.js'
import { applyGptAccountRequestOverridesToBody } from '../../modules/providers/drivers/gpt/request-override-body.js'
import { GatewayRequestValidationError } from '../../modules/gateway/request/validation-error.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'

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
}), 'API Key 覆盖值必须被全部账户支持模型共同声明')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), /全部支持模型.*Flex|全部支持模型.*flex/, '任一模型缺少 Flex 时必须拒绝账户级覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { reasoningEffort: 'high' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), /全部支持模型.*high/, '任一模型缺少目标思考级别时必须拒绝账户级覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'default' },
  supportedModels: ['gpt-unknown'],
  catalog
}), /模型目录缺少账户支持模型.*gpt-unknown/, '只有未知支持模型时不能保存无能力依据的覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'priority' },
  supportedModels: ['gpt-a', 'gpt-unknown'],
  catalog
}), /模型目录缺少账户支持模型.*gpt-unknown/, '已知模型不能掩盖缺失目录的账户支持模型')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'oauth',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a'],
  catalog
}), /OAuth.*Flex/, 'OAuth 账户必须禁止 Flex')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'gemini',
  accountType: 'api_key',
  overrides: { serviceTier: 'priority' },
  supportedModels: ['gemini-test'],
  catalog: [{
    providerCode: 'gemini',
    model: 'gemini-test',
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low']
  } as unknown as ProviderModelCatalogItem]
}), /Gemini.*没有可确认的服务等级 wire 字段/, 'Gemini 服务等级必须在账户保存时拒绝，不能延迟到运行时失败')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'gemini',
  accountType: 'api_key',
  overrides: { reasoningEffort: 'low' },
  supportedModels: ['gemini-test'],
  catalog: [{
    providerCode: 'gemini',
    model: 'gemini-test',
    supportedServiceTiers: [],
    supportedReasoningEfforts: ['low']
  } as unknown as ProviderModelCatalogItem]
}), 'Gemini thinking level 有明确 wire 映射时应允许保存')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'deepseek',
  accountType: 'api_key',
  overrides: { reasoningEffort: 'high' },
  supportedModels: ['deepseek-test'],
  catalog: [{
    providerCode: 'deepseek',
    model: 'deepseek-test',
    supportedServiceTiers: [],
    supportedReasoningEfforts: ['high']
  } as unknown as ProviderModelCatalogItem]
}), /供应商 deepseek 没有可确认的账户请求覆盖 wire 映射/, '目录声明不能绕过 provider driver 映射边界')

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

const runtimeCredentials = runtimeOpenAIAccountCredentials({
  account_id: 'runtime-account',
  service_tier_override: 'priority',
  reasoning_effort_override: 'high',
  api_key: 'must-not-leak-through-runtime-projection'
})
assert.equal(runtimeCredentials.service_tier_override, 'priority', '运行时凭据投影必须保留服务档位覆盖')
assert.equal(runtimeCredentials.reasoning_effort_override, 'high', '运行时凭据投影必须保留思考级别覆盖')
assert.equal(runtimeCredentials.api_key, undefined, '运行时凭据投影不能因此放宽密钥字段白名单')

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

const gatewayAccount = {
  id: 'gpt-request-override-gateway-regression',
  credentials: {
    service_tier_override: 'priority'
  }
} as unknown as DispatchAccountSecret

await assert.rejects(
  applyGptAccountRequestOverridesToBody(JSON.stringify({ messages: [] }), {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'chat_completions'
  }),
  accountScopedOverrideValidationError,
  '请求缺少模型能力时必须返回 account-scoped 网关校验错误，不能裸 500'
)

await assert.rejects(
  applyGptAccountRequestOverridesToBody(JSON.stringify({ model: 'gpt-a', messages: [] }), {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'chat_completions',
    modelCapabilities: {
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  }),
  accountScopedOverrideValidationError,
  '目标模型不支持账户覆盖时必须返回 account-scoped 网关校验错误，允许继续切换账户'
)

const gptDriverSource = readFileSync(resolve('src/modules/providers/drivers/gpt/driver.ts'), 'utf8')
assert.match(gptDriverSource, /normalizeGptRequestOverrideCapabilitiesForGateway/, 'GPT OAuth driver 必须复用 request-override-body 的统一网关错误归一化')
assert.doesNotMatch(gptDriverSource, /from '\.\/request-override-capabilities\.js'/, 'GPT driver 不得在统一归一化范围外直接解析覆盖能力')

console.log('GPT 账户请求覆盖回归通过：配置按完整目录能力交集开放，OAuth 禁止 Flex，账户覆盖禁止 Ultra')

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

function accountScopedOverrideValidationError(error: unknown): boolean {
  return error instanceof GatewayRequestValidationError
    && error.accountScoped
    && error.code === 'account_request_override_unsupported'
}
