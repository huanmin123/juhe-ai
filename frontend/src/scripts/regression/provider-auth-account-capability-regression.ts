import assert from 'node:assert/strict'

import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { validateAccountSaveForm } from '../../views/accounts/accountSavePayload'
import { validateBasicEditCredentialFields } from '../../views/accounts/useAccountEditSaveFlow'
import { managedOAuthProviderKind } from '../../views/accounts/accountProviderCapabilities'

const gpt = FALLBACK_PROVIDERS.find((provider) => provider.code === 'gpt')
const openAICompatible = FALLBACK_PROVIDERS.find((provider) => provider.code === 'openai')
assert.equal(gpt?.defaultSupportedModels.includes('codex-auto-review'), false, 'GPT 回退供应商不得保留资料不完整的 codex-auto-review')
assert.equal(openAICompatible?.defaultSupportedModels.includes('codex-auto-review'), false, '通用 OpenAI-compatible 回退供应商不得默认勾选 GPT 专属模型')
const gptOAuthForm = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS, 'profile_gpt_openai_v1')
assert.equal(gptOAuthForm.supportedModels.includes('codex-auto-review'), false, 'GPT OAuth 新建表单不得默认勾选资料不完整的模型')

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
assert.equal(googleCredentials.oauth_type, 'code_assist')
assert.equal(googleCredentials.tier_id, 'gcp_standard')
assert((googleCredentials.supported_endpoint_modes as string[]).includes('interactions_sse'))

const managedGoogleForm = defaultAccountForm('gemini', 'google_oauth', FALLBACK_PROVIDERS, 'profile_gemini_native_v1beta')
Object.assign(managedGoogleForm, {
  name: 'Gemini managed OAuth',
  groupId: 'group-1',
  baseUrl: 'https://generativelanguage.googleapis.com',
  supportedModels: ['gemini-2.5-pro'],
  healthCheckModel: 'gemini-2.5-pro',
  healthCheckEndpointMode: 'generate_content_json'
})
assert.equal(managedOAuthProviderKind({ provider: gemini, profile: geminiProfile }), 'gemini')
assert.equal(managedGoogleForm.oauthType, 'code_assist', 'Gemini OAuth 静态 fallback 默认应跟随后端 Code Assist 默认模式')
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), '请先生成授权链接', 'Gemini Code Assist 使用内置客户端，不应强制填写 Client ID 与 Client Secret')
Object.assign(managedGoogleForm, { oauthType: 'ai_studio', tierId: 'aistudio_free' })
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), 'Gemini AI Studio OAuth 需要 Client ID 和 Client Secret', 'Gemini AI Studio OAuth 必须要求用户自己的客户端凭据')
Object.assign(managedGoogleForm, { googleClientId: 'client', googleClientSecret: 'secret' })
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), '请先生成授权链接', 'Gemini AI Studio OAuth 必须先创建 PKCE 授权会话')
Object.assign(managedGoogleForm, { oauthMode: 'refresh_token', refreshToken: 'refresh' })
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Gemini Refresh Token 创建应携带客户端凭据直接保存')
Object.assign(managedGoogleForm, { oauthMode: 'access_token', accessToken: 'access', refreshToken: '' })
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Gemini Access Token 应保留直接录入路径')

const xai = FALLBACK_PROVIDERS.find((provider) => provider.code === 'xai')
const xaiProfile = xai?.protocolProfiles.find((profile) => profile.id === 'profile_xai_openai_v1')
assert.equal(managedOAuthProviderKind({ provider: xai, profile: xaiProfile }), 'grok', 'xAI OAuth 必须分派到 Grok OAuth API')
const grokAccessTokenForm = defaultAccountForm('xai', 'oauth', FALLBACK_PROVIDERS, 'profile_xai_openai_v1')
Object.assign(grokAccessTokenForm, {
  name: 'Grok direct token',
  groupId: 'group-1',
  oauthMode: 'access_token',
  accessToken: 'grok-access',
  supportedModels: ['grok-4.5'],
  healthCheckModel: 'grok-4.5',
  healthCheckEndpointMode: 'responses_json'
})
assert.equal(validateAccountSaveForm({
  form: grokAccessTokenForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Grok OAuth 应保留 Access Token 直接录入路径')
Object.assign(grokAccessTokenForm, { oauthMode: 'sso_cookie', accessToken: '', ssoTokens: '' })
assert.equal(validateAccountSaveForm({
  form: grokAccessTokenForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), '请填写至少一个 Grok Web SSO key', 'Grok SSO 批量导入必须提供至少一个 SSO key')
grokAccessTokenForm.ssoTokens = 'sso-1\nsso-2'
assert.equal(validateAccountSaveForm({
  form: grokAccessTokenForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Grok SSO 批量导入应复用 OAuth 账户公共配置')

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
