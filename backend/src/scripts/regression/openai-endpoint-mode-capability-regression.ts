import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { Request } from 'express'

import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import {
  defaultOpenAIEndpointModes,
  normalizeOpenAIEndpointModesForWrite
} from '../../domain/openai-endpoint-modes.js'
import type { AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import { mergeAccountCredentialsForUpdate } from '../../modules/accounts/account-credential-update.js'
import { filterGatewayAccountsByRequestCapability } from '../../modules/gateway/dispatch/account-capability-filter.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/repositories.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key' }),
  ['chat_json', 'chat_sse'],
  '通用 OpenAI 兼容 API Key 默认只启用 Chat JSON/SSE'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'deepseek', accountType: 'api_key' }),
  ['chat_json', 'chat_sse'],
  'DeepSeek API Key 默认只启用 Chat JSON/SSE'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key', clientCompatibility: 'codex_responses' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'Codex Responses 兼容能力默认必须包含 Responses SSE'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({
    providerCode: 'glm',
    providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
    accountType: 'api_key',
    clientCompatibility: 'codex_responses'
  }),
  ['chat_json', 'chat_sse'],
  'GLM Coding Codex bridge 账号默认仍保存真实 Chat JSON/SSE 能力'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    accountType: 'api_key',
    clientCompatibility: 'codex_responses'
  }),
  ['chat_json', 'chat_sse'],
  'DeepSeek Codex bridge 账号默认仍保存真实 Chat JSON/SSE 能力'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'gpt', accountType: 'api_key' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'GPT API Key 默认启用四种 OpenAI v1 形态'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'gpt', accountType: 'oauth' }),
  ['responses_json', 'responses_sse'],
  'GPT OAuth 默认只启用 Responses JSON/SSE'
)
assert.throws(
  () => normalizeOpenAIEndpointModesForWrite(['chat_json', 'bad_mode'], { providerCode: 'openai', accountType: 'api_key' }),
  /不支持的能力/,
  '接口能力写入必须拒绝未知枚举'
)
assert.deepEqual(
  mergeAccountCredentialsForUpdate({
    type: 'api_key',
    credentials: {
      api_key: 'sk-old',
      base_url: 'https://example.com/v1',
      supported_endpoint_modes: ['chat_json']
    }
  } as AccountSummary, {
    api_key: 'sk-new'
  }).supported_endpoint_modes,
  ['chat_json'],
  '账户部分凭据更新必须保留已有接口能力限制'
)
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-openai', base_url: 'https://example.com/v1' }, {
    providerCode: 'openai',
    accountType: 'api_key',
    protocolCode: 'openai',
    protocolVersion: 'v1'
  }).supported_endpoint_modes,
  ['chat_json', 'chat_sse'],
  '通用 OpenAI-compatible 凭据归一化应通过 provider driver 使用 Chat 默认能力'
)
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-gpt', base_url: 'https://api.openai.com/v1' }, {
    providerCode: 'gpt',
    accountType: 'api_key',
    protocolCode: 'openai',
    protocolVersion: 'v1'
  }).supported_endpoint_modes,
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'GPT API Key 凭据归一化应通过 provider driver 使用 OpenAI v1 四项默认能力'
)
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-deepseek', base_url: 'https://api.deepseek.com' }, {
    providerCode: 'deepseek',
    accountType: 'api_key',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    providerProtocolProfileId: 'profile_deepseek_openai_v1'
  }).supported_endpoint_modes,
  ['chat_json', 'chat_sse'],
  'DeepSeek API Key 凭据归一化应通过 provider driver 使用 Chat 默认能力'
)
assert.throws(
  () => normalizeAccountCredentialsForWrite('api_key', {
    api_key: 'sk-deepseek',
    base_url: 'https://api.deepseek.com',
    supported_endpoint_modes: ['responses_json']
  }, {
    providerCode: 'deepseek',
    accountType: 'api_key',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    providerProtocolProfileId: 'profile_deepseek_openai_v1'
  }),
  /DeepSeek 账户接口能力只支持 Chat JSON 或 Chat SSE/,
  'DeepSeek API Key 凭据归一化应拒绝 Responses 能力'
)
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-ant', base_url: 'https://api.anthropic.com/v1' }, {
    providerCode: 'anthropic',
    accountType: 'api_key',
    protocolCode: 'anthropic',
    protocolVersion: 'v1'
  }).supported_endpoint_modes,
  ['messages_json', 'messages_sse', 'message_token_counting'],
  'Anthropic API Key 凭据归一化应通过 provider driver 使用 Anthropic 默认能力'
)

const chatOnly = account('chat-only', ['chat_json', 'chat_sse'])
const responsesOnly = account('responses-only', ['responses_json', 'responses_sse'])
const jsonOnly = account('json-only', ['chat_json', 'responses_json'])
const codexCapableApiKey = gptApiKeyAccount('gpt-api-key-codex', ['responses_json', 'responses_sse'])
const deepSeekCodexBridgeAccount = deepSeekApiKeyAccount('deepseek-codex-bridge', ['chat_json', 'chat_sse'], 'codex_responses')

assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/chat/completions', true), [chatOnly, responsesOnly, jsonOnly]).accounts.map((item) => item.id),
  ['chat-only'],
  'Chat SSE 请求只能命中支持 chat_sse 的账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', false), [chatOnly, responsesOnly, jsonOnly]).accounts.map((item) => item.id),
  ['responses-only', 'json-only'],
  'Responses JSON 请求只能命中支持 responses_json 的账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/embeddings', false), [chatOnly]).accounts.map((item) => item.id),
  ['chat-only'],
  '未知 OpenAI v1 路径对 API Key 仍保持透传，不受四项能力矩阵拦截'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/chat/completions', false), [oauthAccount('oauth')]).accounts.map((item) => item.id),
  [],
  'OAuth 账号仍不能承接 Chat Completions 路径'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', false), [oauthAccount('oauth-json-only', ['responses_json'])]).accounts.map((item) => item.id),
  [],
  'OAuth Responses 普通请求按有效 SSE 能力筛选'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', false), [responsesOnly, codexCapableApiKey, oauthAccount('oauth-codex')], {
    requestClientCompatibility: 'openai_standard'
  }).accounts.map((item) => item.id),
  ['responses-only', 'gpt-api-key-codex'],
  '普通 OpenAI Responses 请求只能命中 OpenAI 标准兼容账号，GPT API Key 可同时承接'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [responsesOnly, codexCapableApiKey, oauthAccount('oauth-codex')], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['gpt-api-key-codex', 'oauth-codex'],
  'Codex Responses 请求只能命中具备 Codex 兼容能力的账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [responsesOnly, deepSeekCodexBridgeAccount], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['deepseek-codex-bridge'],
  'Codex Responses 请求应能命中 DeepSeek Chat SSE 桥接账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [deepSeekCodexBridgeAccount], {
    requestClientCompatibility: 'openai_standard'
  }).accounts.map((item) => item.id),
  [],
  '普通 OpenAI Responses 请求不应命中 DeepSeek Chat SSE 桥接账号'
)

const accountCredentialsNormalizationSource = readFileSync(resolve('src/storage/account-credentials-normalization.ts'), 'utf8')
assert.match(accountCredentialsNormalizationSource, /providerAccountCredentialDriverForContext/)
assert.doesNotMatch(accountCredentialsNormalizationSource, /normalizeOpenAIEndpointModesForWrite/)
assert.doesNotMatch(accountCredentialsNormalizationSource, /normalizeAnthropicEndpointModesForWrite/)
assert.doesNotMatch(accountCredentialsNormalizationSource, /isAnthropicProtocolProfile/)
const accountCredentialDriverRegistrySource = readFileSync(resolve('src/modules/providers/drivers/account-credentials.registry.ts'), 'utf8')
assert.match(accountCredentialDriverRegistrySource, /openAICompatibleAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /gptAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /deepSeekAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /anthropicAccountCredentialDriver/)

console.log('OpenAI 接口能力矩阵回归通过：默认值、写入校验和候选账号过滤均符合预期')

function request(path: string, stream: boolean): Request {
  return {
    method: 'POST',
    path,
    originalUrl: path,
    body: { stream }
  } as Request
}

function account(id: string, modes: AccountSupportedEndpointMode[]): UpstreamAccount {
  return {
    id,
    type: 'api_key',
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://example.com/v1',
    supportedEndpointModes: modes,
    credentials: { supported_endpoint_modes: modes },
    clientCompatibility: 'openai_standard'
  } as unknown as UpstreamAccount
}

function gptApiKeyAccount(id: string, modes: AccountSupportedEndpointMode[]): UpstreamAccount {
  return {
    ...account(id, modes),
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    clientCompatibility: 'codex_responses'
  } as unknown as UpstreamAccount
}

function deepSeekApiKeyAccount(id: string, modes: AccountSupportedEndpointMode[], clientCompatibility: 'openai_standard' | 'codex_responses'): UpstreamAccount {
  return {
    ...account(id, modes),
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    baseUrl: 'https://api.deepseek.com',
    clientCompatibility
  } as unknown as UpstreamAccount
}

function oauthAccount(id: string, modes: AccountSupportedEndpointMode[] = ['responses_json', 'responses_sse']): UpstreamAccount {
  return {
    ...account(id, modes),
    type: 'oauth',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    clientCompatibility: 'codex_responses'
  } as unknown as UpstreamAccount
}
