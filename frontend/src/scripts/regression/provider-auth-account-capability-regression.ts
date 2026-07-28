import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { buildAccountCredentials } from '../../views/accounts/accountCredentials'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { validateAccountSaveForm } from '../../views/accounts/accountSavePayload'
import { validateBasicEditCredentialFields } from '../../views/accounts/useAccountEditSaveFlow'
import { managedOAuthProviderKind } from '../../views/accounts/accountProviderCapabilities'
import { normalizeGrokSsoTokens } from '../../views/accounts/grokSsoTokens'

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
Object.assign(managedGoogleForm, {
  oauthType: 'code_assist',
  tierId: 'gcp_standard',
  oauthMode: 'access_token',
  accessToken: 'access',
  refreshToken: '',
  projectId: ''
})
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), 'Gemini Code Assist / Google One 直接 Token 需要 GCP Project ID', 'Gemini CLI 直接 Token 缺少 Project ID 时不得创建不可运行账户')
managedGoogleForm.projectId = 'project-direct-token'
assert.equal(validateAccountSaveForm({
  form: managedGoogleForm,
  hasAuthSession: false,
  errorPolicyRules: [],
  responseInspectionRules: [],
  providers: FALLBACK_PROVIDERS
}), undefined, 'Gemini CLI Access Token 携带 Project ID 后应保留直接录入路径')

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
assert.deepEqual(
  normalizeGrokSsoTokens('Cookie: sso=sso-1; Path=/\nsso-1\nsso-rw=sso-2; Secure'),
  ['sso-1', 'sso-2'],
  'Grok SSO 前端必须按后端 Cookie 语义 canonical 去重，保证部分失败索引可安全重试'
)

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

const oauthSectionSource = readFileSync(new URL('../../views/accounts/AccountOAuthSection.vue', import.meta.url), 'utf8')
const reauthorizeModalSource = readFileSync(new URL('../../views/accounts/AccountReauthorizeModal.vue', import.meta.url), 'utf8')
const authorizePanelSource = readFileSync(new URL('../../views/accounts/AccountOAuthAuthorizePanel.vue', import.meta.url), 'utf8')
assert.match(oauthSectionSource, /geminiCapabilitiesRequestId/, 'Gemini capabilities 请求必须使用代次保护')
assert.match(oauthSectionSource, /props\.form\.providerCode !== providerCode/, 'Gemini capabilities 返回后必须复核当前供应商')
assert.match(oauthSectionSource, /props\.form\.providerProtocolProfileId !== providerProtocolProfileId/, 'Gemini capabilities 返回后必须复核当前协议档案')
assert.match(oauthSectionSource, /disabled\s*\/><\/a-form-item>/, '已保存 Gemini 账户的 OAuth 类型必须锁定，类型迁移应走重新授权')
assert.match(oauthSectionSource, /geminiProfileDefaultEndpointModes/, 'Gemini OAuth 类型切换必须能恢复 AI Studio 协议档案端点能力')
assert.match(reauthorizeModalSource, /geminiRequiresClientCredentials && form\.oauthMode !== 'access_token'/, 'AI Studio Refresh Token 重新授权必须显示客户端凭据字段')
assert.match(authorizePanelSource, /overflow-wrap: anywhere/, 'OAuth 多模式控件必须提供移动端长文案换行保护')

console.log('provider auth account capability regression passed')
