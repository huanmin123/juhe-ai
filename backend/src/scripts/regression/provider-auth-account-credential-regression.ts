import assert from 'node:assert/strict'
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
  'Anthropic 档案应开放 API Key 与官方 token 型 OAuth'
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
assert.equal(anthropicProviderDriver.prepareAccountBeforeDispatch, undefined, 'Anthropic driver 当前不应暴露额外 token exchange 运行钩子')

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

const anthropicOauthParts = await anthropicProviderDriver.buildUpstreamRequestParts(
  requestFixture('/v1/messages', { model: 'claude-opus-4-8', messages: [{ role: 'user', content: 'OK' }] }),
  accountFixture({
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    protocolCode: 'anthropic',
    protocolVersion: 'v1',
    type: 'oauth',
    baseUrl: 'https://api.anthropic.com/v1',
    apiKey: 'anthropic-refresh-should-not-be-used',
    credentials: {
      access_token: 'anthropic-access-token',
      refresh_token: 'anthropic-refresh-token',
      base_url: 'https://api.anthropic.com/v1',
      supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
    },
    healthCheckEndpointMode: 'messages_json'
  }),
  { systemAccountId: 'sys', groupId: 'grp' },
  undefined
)
assert.equal(anthropicOauthParts.headers.get('authorization'), 'Bearer anthropic-access-token')
assert.equal(anthropicOauthParts.headers.get('x-api-key'), null)
assert.equal(anthropicOauthParts.headers.get('anthropic-version'), '2023-06-01')

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
