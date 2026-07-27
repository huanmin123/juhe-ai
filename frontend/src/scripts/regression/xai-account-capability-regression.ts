import assert from 'node:assert/strict'

import {
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE,
  isXaiProviderCode
} from '../../shared/providerProtocol'
import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { providerModelsForProtocolProfile } from '../../views/accounts/accountEditFormPayload'
import { accountTypeChoicesForProvider } from '../../views/accounts/accountEditFormDisplay'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import {
  accountClientCompatibilityCapabilities,
  canCreateOAuthAccount,
  defaultEndpointModesForAccount,
  managedOAuthProviderKind
} from '../../views/accounts/accountProviderCapabilities'
import { FALLBACK_PROVIDERS, XAI_PROVIDER } from '../../views/accounts/accountOptions'

assert.equal(isXaiProviderCode(XAI_PROVIDER_CODE), true)
assert(FALLBACK_PROVIDERS.some((provider) => provider.code === XAI_PROVIDER_CODE), '账户表单 fallback 供应商应包含 xAI')
assert.equal(XAI_PROVIDER.defaultProtocolProfileId, XAI_OPENAI_V1_PROFILE_ID)
assert.deepEqual(XAI_PROVIDER.accountTypes, ['api_key', 'oauth'])
assert.deepEqual(XAI_PROVIDER.protocolProfiles[0]?.accountTypes, ['api_key', 'oauth'])

const profile = XAI_PROVIDER.protocolProfiles[0]
assert(profile, 'xAI fallback provider 应包含 OpenAI v1 档案')
assert.equal(canCreateOAuthAccount({ provider: XAI_PROVIDER, profile }), false, 'xAI 不应误走 GPT OAuth 创建入口')
assert.equal(managedOAuthProviderKind({ provider: XAI_PROVIDER, profile }), 'grok', 'xAI OAuth 应使用 Grok 托管授权接口')
assert.deepEqual(
  defaultEndpointModesForAccount({ provider: XAI_PROVIDER, profile, type: 'api_key' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
)
assert.deepEqual(
  accountClientCompatibilityCapabilities({
    ...profile,
    type: 'api_key'
  }),
  ['openai_standard', 'codex_responses'],
  'xAI 原生 Responses 应同时支持 OpenAI-compatible 与 Codex Responses 请求形态'
)

const choices = accountTypeChoicesForProvider(XAI_PROVIDER, FALLBACK_PROVIDERS)
assert.equal(choices.length, 2)
assert.equal(choices[0]?.label, 'xAI 官方 API Key')
assert.equal(choices[0]?.tag, 'OpenAI v1')
assert.match(choices[0]?.description ?? '', /Chat Completions.*Responses/)
assert.equal(choices[1]?.label, 'Grok OAuth')
assert.equal(choices[1]?.tag, 'Grok OAuth')
assert.match(choices[1]?.description ?? '', /托管授权.*Refresh Token.*Access Token/)
assert.deepEqual(
  defaultEndpointModesForAccount({ provider: XAI_PROVIDER, profile, type: 'oauth' }),
  ['responses_json', 'responses_sse'],
  'Grok OAuth 的 Responses-only 必须保存在端点模式中'
)
assert.deepEqual(
  accountClientCompatibilityCapabilities({ ...profile, type: 'oauth' }),
  ['openai_standard'],
  'Grok OAuth 不得被标记为 Codex Responses 客户端'
)

const form = defaultAccountForm(XAI_PROVIDER_CODE, 'api_key', FALLBACK_PROVIDERS, XAI_OPENAI_V1_PROFILE_ID)
form.apiKey = 'xai-form-key'
form.apiKeys = ['xai-form-key']
const credentials = buildAccountCredentials({
  errorPolicyRules: [],
  responseInspectionRules: [],
  form
})
assert.equal(credentials.api_key, 'xai-form-key')
assert.equal(credentials.base_url, 'https://api.x.ai/v1')
assert.deepEqual(credentials.supported_endpoint_modes, ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'])

assert.deepEqual(
  providerModelsForProtocolProfile([
    { label: 'grok-4.5', value: 'grok-4.5', supportedApiProtocols: ['chat_completions', 'responses'] },
    { label: 'grok-imagine-image', value: 'grok-imagine-image', supportedApiProtocols: ['images'] }
  ], profile).map((item) => item.value),
  ['grok-4.5'],
  'xAI OpenAI v1 文本档案不得把 image-only 模型暴露给 supportedModels 或 healthCheckModel'
)

console.log('xAI 前端账户能力回归通过：API Key、Grok OAuth、端点模式与客户端兼容语义符合预期')
