import assert from 'node:assert/strict'
import type { Request } from 'express'

import {
  defaultOpenAIEndpointModes,
  normalizeOpenAIEndpointModesForWrite
} from '../../domain/openai-endpoint-modes.js'
import type { AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import { mergeAccountCredentialsForUpdate } from '../../modules/accounts/account-credential-update.js'
import { filterGatewayAccountsByRequestCapability } from '../../modules/gateway/dispatch/account-capability-filter.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key' }),
  ['chat_json', 'chat_sse'],
  '通用 OpenAI 兼容 API Key 默认只启用 Chat JSON/SSE'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key', clientCompatibility: 'codex_responses' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'Codex Responses 兼容模式默认必须包含 Responses SSE'
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

const chatOnly = account('chat-only', ['chat_json', 'chat_sse'])
const responsesOnly = account('responses-only', ['responses_json', 'responses_sse'])
const jsonOnly = account('json-only', ['chat_json', 'responses_json'])

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

function oauthAccount(id: string, modes: AccountSupportedEndpointMode[] = ['responses_json', 'responses_sse']): UpstreamAccount {
  return {
    ...account(id, modes),
    type: 'oauth',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    clientCompatibility: 'codex_responses'
  } as unknown as UpstreamAccount
}
