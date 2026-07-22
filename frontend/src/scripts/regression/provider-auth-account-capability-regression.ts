import assert from 'node:assert/strict'

import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { validateAccountSaveForm } from '../../views/accounts/accountSavePayload'
import { validateBasicEditCredentialFields } from '../../views/accounts/useAccountEditSaveFlow'

const gpt = FALLBACK_PROVIDERS.find((provider) => provider.code === 'gpt')
const openAICompatible = FALLBACK_PROVIDERS.find((provider) => provider.code === 'openai')
assert(gpt?.defaultSupportedModels.includes('codex-auto-review'), 'GPT 回退供应商应默认勾选 codex-auto-review')
assert.equal(openAICompatible?.defaultSupportedModels.includes('codex-auto-review'), false, '通用 OpenAI-compatible 回退供应商不得默认勾选 GPT 专属模型')
const gptOAuthForm = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS, 'profile_gpt_openai_v1')
assert(gptOAuthForm.supportedModels.includes('codex-auto-review'), 'GPT OAuth 新建表单应默认勾选 codex-auto-review')

const gemini = FALLBACK_PROVIDERS.find((provider) => provider.code === 'gemini')
const geminiProfile = gemini?.protocolProfiles.find((profile) => profile.id === 'profile_gemini_native_v1beta')
assert(geminiProfile?.accountTypes.includes('google_oauth'))
const anthropic = FALLBACK_PROVIDERS.find((provider) => provider.code === 'anthropic')
const anthropicProfile = anthropic?.protocolProfiles.find((profile) => profile.id === 'profile_anthropic_anthropic_v1')
assert.deepEqual(anthropic?.accountTypes, ['api_key', 'oauth'], 'Anthropic 回退供应商应开放 API Key 与 Bearer Token OAuth')
assert.deepEqual(anthropicProfile?.accountTypes, ['api_key', 'oauth'], 'Anthropic 回退协议档案应开放 API Key 与 Bearer Token OAuth')
const openAICompatibleProfile = openAICompatible?.protocolProfiles.find((profile) => profile.id === 'profile_openai_openai_v1')
assert.deepEqual(openAICompatible?.accountTypes, ['api_key'], '通用 OpenAI-compatible 回退供应商不得开放 OAuth 账户类型')
assert.deepEqual(openAICompatibleProfile?.accountTypes, ['api_key'], '通用 OpenAI-compatible 回退协议档案不得开放 OAuth 账户类型')

const anthropicOAuthForm = defaultAccountForm('anthropic', 'oauth', FALLBACK_PROVIDERS, 'profile_anthropic_anthropic_v1')
Object.assign(anthropicOAuthForm, {
  name: 'Anthropic OAuth',
  groupId: 'group-1',
  baseUrl: 'https://api.anthropic.com/v1',
  accessToken: 'anthropic-access',
  supportedModels: ['claude-opus-4-8'],
  healthCheckModel: 'claude-opus-4-8',
  healthCheckEndpointMode: 'messages_json'
})
assert.equal(validateAccountSaveForm({
  form: anthropicOAuthForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Anthropic OAuth 新建表单应允许直接录入 Access Token 保存')

const googleForm = defaultAccountForm('gemini', 'google_oauth', FALLBACK_PROVIDERS, 'profile_gemini_native_v1beta')
Object.assign(googleForm, {
  accessToken: 'access',
  refreshToken: 'refresh',
  googleClientId: 'client',
  googleClientSecret: 'secret',
  googleQuotaProjectId: 'quota',
  baseUrl: 'https://generativelanguage.googleapis.com'
})
const googleCredentials = buildAccountCredentials({ errorPolicyRules: [], responseInspectionRules: [], form: googleForm })
assert.equal(googleCredentials.client_secret, 'secret')
assert.equal(googleCredentials.quota_project_id, 'quota')
assert((googleCredentials.supported_endpoint_modes as string[]).includes('interactions_sse'))

const editingGoogleForm = defaultAccountForm('gemini', 'google_oauth', FALLBACK_PROVIDERS, 'profile_gemini_native_v1beta')
Object.assign(editingGoogleForm, {
  name: '已存 Google OAuth',
  groupId: 'group-1',
  baseUrl: 'https://generativelanguage.googleapis.com',
  supportedModels: ['gemini-2.5-pro'],
  healthCheckModel: 'gemini-2.5-pro',
  healthCheckEndpointMode: 'generate_content_json'
})
assert.equal(validateBasicEditCredentialFields(editingGoogleForm), undefined, 'Google OAuth 基础编辑不得强制重填已存 token/client_secret')
assert.equal(validateAccountSaveForm({
  editingId: 'account-google-existing',
  form: editingGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Google OAuth 编辑保存不得强制重填已存 secret')

console.log('provider auth account capability regression passed')
