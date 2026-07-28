import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { Request } from 'express'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED,
  GEMINI_NATIVE_V1BETA_PROFILE_SEED
} from '../../storage/schema-defaults.js'
import {
  normalizeAccountCredentialsForWrite,
  requiredAccountCredentialSource
} from '../../storage/account-credentials-normalization.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { anthropicProviderDriver } from '../../modules/providers/drivers/anthropic/driver.js'
import { geminiProviderDriver } from '../../modules/providers/drivers/gemini/driver.js'
import { assertAccountGptRequestOverridesSupportedByCatalog } from '../../modules/accounts/account-gpt-request-overrides.validation.js'

assert(GEMINI_NATIVE_V1BETA_PROFILE_SEED.accountTypes.includes('google_oauth'))
assert.deepEqual(
  ANTHROPIC_ANTHROPIC_V1_PROFILE_SEED.accountTypes,
  ['api_key', 'oauth'],
  'Anthropic 官方档案应开放 API Key 与 OAuth Access Token 两种账户类型'
)

assert.throws(
  () => normalizeAccountCredentialsForWrite('workload_identity', {
    identity_token: 'idp.jwt.value',
    federation_rule_id: 'fdrl_example',
    organization_id: '00000000-0000-0000-0000-000000000000',
    service_account_id: 'svac_example',
    base_url: 'https://api.anthropic.com/v1'
  }),
  /不支持凭据写入/,
  '没有可信自动轮换 assertion source 时不得接受 workload_identity 凭据'
)
assert.equal(typeof anthropicProviderDriver.prepareAccountBeforeDispatch, 'function', 'Anthropic driver 必须暴露 OAuth access token 自动刷新钩子')

const anthropicOAuth = normalizeAccountCredentialsForWrite('oauth', {
  access_token: 'anthropic-access-token',
  base_url: 'https://api.anthropic.com/v1'
}, {
  accountType: 'oauth',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
})
assert.equal(anthropicOAuth.access_token, 'anthropic-access-token')
assert.deepEqual(anthropicOAuth.supported_endpoint_modes, ['messages_json', 'messages_sse', 'message_token_counting'])
assert.equal(requiredAccountCredentialSource('oauth', anthropicOAuth), 'anthropic-access-token')
assert.throws(
  () => normalizeAccountCredentialsForWrite('oauth', {
    refresh_token: 'anthropic-refresh-token',
    base_url: 'https://api.anthropic.com/v1'
  }, {
    accountType: 'oauth',
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    protocolCode: 'anthropic',
    protocolVersion: 'v1'
  }),
  /Anthropic OAuth Access Token 不能为空/
)

const anthropicOAuthWithRefresh = normalizeAccountCredentialsForWrite('oauth', {
  access_token: 'anthropic-access',
  refresh_token: 'anthropic-refresh',
  base_url: 'https://api.anthropic.com/v1'
}, {
  accountType: 'oauth',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
})
assert.equal(anthropicOAuthWithRefresh.access_token, 'anthropic-access')
assert.equal(anthropicOAuthWithRefresh.refresh_token, 'anthropic-refresh')
assert.deepEqual(anthropicOAuthWithRefresh.supported_endpoint_modes, ['messages_json', 'messages_sse', 'message_token_counting'])
assert.throws(
  () => normalizeAccountCredentialsForWrite('oauth', {
    refresh_token: 'anthropic-refresh-only',
    base_url: 'https://api.anthropic.com/v1'
  }, {
    accountType: 'oauth',
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    protocolCode: 'anthropic',
    protocolVersion: 'v1'
  }),
  /Anthropic OAuth Access Token 不能为空/,
  'Anthropic OAuth 账户必须显式提供 Access Token'
)

const googleOAuth = normalizeAccountCredentialsForWrite('google_oauth', {
  access_token: 'google-access',
  refresh_token: 'google-refresh',
  expires_at: '2026-07-18T12:00:00.000Z',
  client_id: 'client.apps.googleusercontent.com',
  client_secret: 'google-client-secret',
  quota_project_id: 'quota-project',
  service_tier_override: 'priority',
  reasoning_effort_override: 'high',
  base_url: 'https://generativelanguage.googleapis.com/v1beta'
}, {
  accountType: 'google_oauth',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
})
assert.equal(googleOAuth.client_secret, 'google-client-secret')
assert.equal(googleOAuth.quota_project_id, 'quota-project')
assert.equal(googleOAuth.service_tier_override, 'priority')
assert.equal(googleOAuth.reasoning_effort_override, 'high')
const interactionsOnlyGoogleOAuth = normalizeAccountCredentialsForWrite('google_oauth', {
  access_token: 'google-access',
  base_url: 'https://generativelanguage.googleapis.com/v1beta',
  supported_endpoint_modes: ['interactions_json', 'interactions_sse'],
  service_tier_override: 'priority',
  reasoning_effort_override: 'high'
}, {
  accountType: 'google_oauth',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
})
assert.equal(interactionsOnlyGoogleOAuth.service_tier_override, undefined)
assert.equal(interactionsOnlyGoogleOAuth.reasoning_effort_override, undefined)
assert.throws(
  () => assertAccountGptRequestOverridesSupportedByCatalog({
    providerCode: 'gemini',
    accountType: 'google_oauth',
    overrides: { serviceTier: 'priority', reasoningEffort: 'high' },
    supportedEndpointModes: ['interactions_json', 'interactions_sse'],
    supportedModels: ['gemini-3.5-flash'],
    catalog: [{
      providerCode: 'gemini', model: 'gemini-3.5-flash', status: 'active', scope: 'built_in',
      supportedServiceTiers: ['priority'], supportedReasoningEfforts: ['high']
    } as never]
  }),
  /Interactions-only Gemini 账户不能配置 GenerateContent 请求覆盖/,
  'Interactions-only Gemini 账户必须拒绝永远不会生效的请求覆盖'
)
assert.deepEqual(googleOAuth.supported_endpoint_modes, ['generate_content_json', 'generate_content_sse', 'count_tokens', 'interactions_json', 'interactions_sse'])
assert.equal(requiredAccountCredentialSource('google_oauth', googleOAuth), 'google-refresh')

assert.throws(
  () => normalizeAccountCredentialsForWrite('google_oauth', {
    client_id: 'client',
    client_secret: 'secret',
    base_url: 'https://generativelanguage.googleapis.com/v1beta'
  }),
  /Google OAuth 凭据不能为空/
)

const preparedGoogle = await geminiProviderDriver.prepareAccountBeforeDispatch!(accountFixture({
  id: 'google-oauth-driver-account',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  type: 'google_oauth',
  baseUrl: 'https://generativelanguage.googleapis.com',
  apiKey: 'google-access',
  credentials: { ...googleOAuth, expires_at: '2099-07-18T12:00:00.000Z' }
}), {})
const interactionsRequest = requestFixture('/v1beta/interactions', { model: 'gemini-3.5-flash', input: 'OK' })
const googleParts = await geminiProviderDriver.buildUpstreamRequestParts(interactionsRequest, preparedGoogle, { systemAccountId: 'sys', groupId: 'grp' })
assert.equal(googleParts.headers.get('authorization'), 'Bearer google-access')
assert.equal(googleParts.headers.get('x-goog-api-key'), null)
assert.equal(googleParts.headers.get('x-goog-user-project'), 'quota-project')

const anthropicOauthAccount = accountFixture({
  id: 'anthropic-oauth-driver-account',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  type: 'oauth',
  baseUrl: 'https://api.anthropic.com/v1',
  apiKey: '',
  credentials: anthropicOAuth
})
const anthropicMessagesRequest = requestFixture('/v1/messages', {
  model: 'claude-opus-4-8',
  max_tokens: 16,
  messages: [{ role: 'user', content: 'hello' }]
})
const anthropicOauthParts = await anthropicProviderDriver.buildUpstreamRequestParts(anthropicMessagesRequest, anthropicOauthAccount, { systemAccountId: 'sys', groupId: 'grp' })
assert.equal(anthropicOauthParts.headers.get('authorization'), 'Bearer anthropic-access-token')
assert.equal(anthropicOauthParts.headers.get('x-api-key'), null)
assert.match(anthropicOauthParts.headers.get('anthropic-beta') ?? '', /(?:^|,)oauth-2025-04-20(?:,|$)/u, 'Anthropic OAuth 请求必须补齐 oauth beta')
assert.match(anthropicOauthParts.headers.get('anthropic-beta') ?? '', /(?:^|,)claude-code-20250219(?:,|$)/u, 'Anthropic OAuth 请求必须使用 Claude Code 订阅流量契约')
assert.equal(anthropicOauthParts.headers.get('content-type'), 'application/json')
assert.deepEqual(
  Object.fromEntries([
    'user-agent',
    'x-stainless-lang',
    'x-stainless-package-version',
    'x-stainless-os',
    'x-stainless-arch',
    'x-stainless-runtime',
    'x-stainless-runtime-version',
    'x-stainless-retry-count',
    'x-stainless-timeout',
    'x-app',
    'anthropic-dangerous-direct-browser-access'
  ].map((name) => [name, anthropicOauthParts.headers.get(name)])),
  {
    'user-agent': 'claude-cli/2.1.161 (external, cli)',
    'x-stainless-lang': 'js',
    'x-stainless-package-version': '0.94.0',
    'x-stainless-os': 'Linux',
    'x-stainless-arch': 'arm64',
    'x-stainless-runtime': 'node',
    'x-stainless-runtime-version': 'v24.3.0',
    'x-stainless-retry-count': '0',
    'x-stainless-timeout': '600',
    'x-app': 'cli',
    'anthropic-dangerous-direct-browser-access': 'true'
  },
  'Anthropic OAuth 绑定后的首次请求必须完整模拟 Claude CLI identity，不能只携带 beta'
)

const anthropicOAuthRefreshSource = readFileSync(resolve('src/modules/anthropic-oauth/anthropic-oauth.routes.ts'), 'utf8')
assert.match(anthropicOAuthRefreshSource, /post\('\/accounts\/:id\/refresh-token'[\s\S]*Anthropic OAuth 账户缺少 Refresh Token/, 'Anthropic OAuth 手动刷新必须拒绝缺少 refresh_token 的账户')
assert.match(anthropicOAuthRefreshSource, /runWithProviderOAuthRefreshLock\([\s\S]*findEditableAnthropicOAuthAccount\(account\.id, requestAccess\)[\s\S]*refreshAnthropicAuthToken\([\s\S]*refreshToken: currentRefreshToken,[\s\S]*clientId: stringCredential\(current\.credentials, 'client_id'\)/, 'Anthropic OAuth 手动刷新必须在共享锁内重新读取并使用当前 refresh_token 和 client_id')
assert.doesNotMatch(anthropicOAuthRefreshSource, /clearAccountFailureStateAsync/, 'Anthropic OAuth 手动刷新不得无 provenance 清除账户业务状态')
assert.match(anthropicOAuthRefreshSource, /sanitizeAccountCredentialCarrierResponse\(updatedAccount\)/, 'Anthropic OAuth 手动刷新响应必须继续走凭据脱敏输出')

console.log('provider auth account credential regression passed')

function accountFixture(overrides: Partial<DispatchAccountSecret>): DispatchAccountSecret {
  return {
    id: 'account',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'sys',
    accountOwnerSystemAccountId: 'sys',
    groupOwnerSystemAccountId: 'sys',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'account',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'chat_json',
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'key',
    streamFailureCount: 0,
    credentials: {},
    ...overrides
  }
}

function requestFixture(path: string, body: Record<string, unknown>): Request {
  return {
    method: 'POST',
    originalUrl: path,
    path,
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    body,
    rawBody: Buffer.from(JSON.stringify(body))
  } as unknown as Request
}
