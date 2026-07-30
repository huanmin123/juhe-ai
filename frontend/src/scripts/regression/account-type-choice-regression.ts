import assert from 'node:assert/strict'

import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { accountTypeChoicesForProvider } from '../../views/accounts/accountEditFormDisplay'

const providers = FALLBACK_PROVIDERS.map(({ code, name }) => ({ code, name }))

assert.deepEqual(
  accountTypeChoicesForProvider(FALLBACK_PROVIDERS.find((provider) => provider.code === 'anthropic'), providers)
    .map((choice) => choice.type),
  ['api_key', 'oauth'],
  'Anthropic 新增账户必须显示 API Key 与 OAuth 两种账户类型'
)
assert.deepEqual(
  accountTypeChoicesForProvider(FALLBACK_PROVIDERS.find((provider) => provider.code === 'xai'), providers)
    .map((choice) => choice.type),
  ['api_key', 'oauth'],
  'xAI 新增账户必须显示 API Key 与 OAuth 两种账户类型'
)
assert.deepEqual(
  accountTypeChoicesForProvider(FALLBACK_PROVIDERS.find((provider) => provider.code === 'gemini'), providers)
    .map((choice) => `${choice.providerProtocolProfileId}:${choice.type}`),
  [
    'profile_gemini_native_v1beta:api_key',
    'profile_gemini_openai_chat_v1beta:api_key',
    'profile_gemini_native_v1beta:google_oauth'
  ],
  'Gemini 新增账户必须保留原生 OAuth 与 OpenAI Chat API Key 的协议档案边界'
)

console.log('账户类型选择器回归通过：Anthropic、xAI 和 Gemini OAuth 类型均可选择')
