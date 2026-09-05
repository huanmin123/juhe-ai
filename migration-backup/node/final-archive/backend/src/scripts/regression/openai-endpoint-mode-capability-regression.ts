import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { Request } from 'express'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import {
  assertAnthropicEndpointModesCompatible,
  defaultAnthropicEndpointModes,
  normalizeAnthropicEndpointModesForWrite
} from '../../domain/anthropic-endpoint-modes.js'
import {
  assertOpenAIEndpointModesCompatible,
  defaultOpenAIEndpointModes,
  normalizeOpenAIEndpointModesForWrite
} from '../../domain/openai-endpoint-modes.js'
import {
  assertGeminiEndpointModesCompatible,
  normalizeGeminiEndpointModesForWrite
} from '../../domain/gemini-endpoint-modes.js'
import type { AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import { mergeAccountCredentialsForUpdate } from '../../modules/accounts/account-credential-update.js'
import { filterGatewayAccountsByRequestCapability } from '../../modules/gateway/dispatch/account-capability-filter.js'
import { normalizeHybridEndpointModesForRuntime } from '../../modules/providers/drivers/hybrid/account-credentials.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/repositories.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key' }),
  ['chat_json', 'chat_sse'],
  '通用 OpenAI 兼容 API Key 默认只启用 Chat Completions JSON/Streaming'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'deepseek', accountType: 'api_key' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'DeepSeek API Key 默认启用 Chat Completion 与原生 Responses JSON/Streaming'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'openai', accountType: 'api_key', clientCompatibility: 'codex_responses' }),
  ['chat_json', 'chat_sse'],
  '通用 OpenAI-compatible API Key 即使传入内部 Codex 画像也只能按真实 Chat 能力落库'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({
    providerCode: 'glm',
    providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
    accountType: 'api_key',
    clientCompatibility: 'codex_responses'
  }),
  ['chat_json', 'chat_sse'],
  'GLM Coding OpenAI 档案默认只保存真实 OpenAI Chat Completions JSON/Streaming 能力'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    accountType: 'api_key',
    clientCompatibility: 'codex_responses'
  }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'DeepSeek OpenAI 档案默认保存 Chat Completion 与原生 Responses JSON/Streaming 能力'
)
assert.deepEqual(
  defaultAnthropicEndpointModes({
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION
  }),
  ['messages_json', 'messages_sse', 'message_token_counting'],
  '官方 Anthropic API Key 默认保留 Messages JSON/Streaming 和 count_tokens 能力'
)
assert.deepEqual(
  defaultAnthropicEndpointModes({
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    accountType: 'oauth',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION
  }),
  ['messages_json', 'messages_sse', 'message_token_counting'],
  '官方 Anthropic OAuth 默认应保留与 API Key 一致的 Messages JSON/Streaming 和 count_tokens 能力'
)
assert.deepEqual(
  defaultAnthropicEndpointModes({
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION
  }),
  ['messages_json', 'messages_sse'],
  'DeepSeek Claude Code 档案默认只启用 Messages JSON/Streaming'
)
assert.deepEqual(
  defaultAnthropicEndpointModes({
    providerCode: 'glm',
    providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION
  }),
  ['messages_json', 'messages_sse'],
  'GLM Coding Anthropic 档案默认只启用 Messages JSON/Streaming'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'gpt', accountType: 'api_key' }),
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'GPT API Key 默认启用四种 OpenAI v1 形态'
)
assert.deepEqual(
  defaultOpenAIEndpointModes({ providerCode: 'gpt', accountType: 'oauth' }),
  ['responses_json', 'responses_sse'],
  'GPT OAuth 默认只启用 Responses API JSON/Streaming'
)
assert.throws(
  () => normalizeOpenAIEndpointModesForWrite(['chat_json', 'bad_mode'], { providerCode: 'openai', accountType: 'api_key' }),
  /上游接口能力包含不支持的能力：bad_mode/,
  'OpenAI 上游接口能力写入必须拒绝未知枚举并返回统一文案'
)
assert.throws(
  () => normalizeAnthropicEndpointModesForWrite(['bad_mode']),
  /上游接口能力包含不支持的能力：bad_mode/,
  'Anthropic 上游接口能力写入必须拒绝未知枚举并返回统一文案'
)
assert.throws(
  () => normalizeGeminiEndpointModesForWrite(['bad_mode']),
  /上游接口能力包含不支持的能力：bad_mode/,
  'Gemini 上游接口能力写入必须拒绝未知枚举并返回统一文案'
)
assert.throws(
  () => assertOpenAIEndpointModesCompatible({ modes: ['chat_json'], accountType: 'oauth' }),
  /OAuth 账户上游接口能力只能选择/,
  'OAuth 上游接口能力兼容校验必须返回统一文案'
)
assert.throws(
  () => assertAnthropicEndpointModesCompatible({ modes: ['chat_json'], accountType: 'api_key' }),
  /Anthropic 账户上游接口能力不支持：chat_json/,
  'Anthropic 上游接口能力兼容校验必须返回统一文案'
)
assert.throws(
  () => assertGeminiEndpointModesCompatible({ modes: ['chat_json'], accountType: 'api_key' }),
  /Gemini 账户上游接口能力不支持：chat_json/,
  'Gemini 上游接口能力兼容校验必须返回统一文案'
)
assert.throws(
  () => assertAnthropicEndpointModesCompatible({ modes: ['message_token_counting'], accountType: 'api_key' }),
  /Anthropic 账户上游接口能力必须至少启用 Messages API/,
  'Anthropic 必选上游接口能力校验必须返回统一文案'
)
assert.doesNotThrow(
  () => assertAnthropicEndpointModesCompatible({ modes: ['messages_json', 'messages_sse'], accountType: 'oauth' }),
  'Anthropic OAuth 应允许保存 Messages JSON/Streaming 能力'
)
assert.throws(
  () => assertGeminiEndpointModesCompatible({ modes: ['count_tokens'], accountType: 'api_key' }),
  /Gemini 账户上游接口能力必须至少启用 generateContent、streamGenerateContent 或 Interactions/,
  'Gemini 必选上游接口能力校验必须返回统一文案'
)
assert.throws(
  () => assertOpenAIEndpointModesCompatible({ modes: ['responses_json'], accountType: 'oauth' }),
  /OAuth 账户上游接口能力必须启用 Responses API \(Streaming\)/,
  'OAuth 必选流式上游接口能力校验必须返回统一文案'
)
assert.throws(
  () => assertOpenAIEndpointModesCompatible({ modes: ['chat_json'], clientCompatibility: 'codex_responses' }),
  /Codex Responses 账户上游接口能力必须启用 Responses API \(Streaming\)/,
  'Codex Responses 必选上游接口能力校验必须返回统一文案'
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
  '账户部分凭据更新必须保留已有上游接口能力'
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
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'DeepSeek API Key 凭据归一化应通过 provider driver 使用 Chat 与原生 Responses 默认能力'
)
assert.doesNotThrow(
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
  'DeepSeek API Key 凭据归一化应允许 Responses 能力'
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
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-deepseek-ant', base_url: 'https://api.deepseek.com/anthropic' }, {
    providerCode: DEEPSEEK_PROVIDER_CODE,
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
  }).supported_endpoint_modes,
  ['messages_json', 'messages_sse'],
  'DeepSeek Claude Code 凭据归一化应通过 provider driver 使用 Messages 默认能力'
)
assert.throws(
  () => normalizeAccountCredentialsForWrite('api_key', {
    api_key: 'sk-deepseek-ant',
    base_url: 'https://api.deepseek.com/anthropic',
    supported_endpoint_modes: ['message_token_counting']
  }, {
    providerCode: DEEPSEEK_PROVIDER_CODE,
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
  }),
  /DeepSeek Anthropic 账户上游接口能力只支持 Messages API/,
  'DeepSeek Claude Code 凭据归一化应拒绝 count_tokens 能力'
)
assert.deepEqual(
  normalizeAccountCredentialsForWrite('api_key', { api_key: 'sk-glm-ant', base_url: 'https://open.bigmodel.cn/api/anthropic' }, {
    providerCode: 'glm',
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID
  }).supported_endpoint_modes,
  ['messages_json', 'messages_sse'],
  'GLM Coding Anthropic 凭据归一化应通过 provider driver 使用 Messages 默认能力'
)
assert.throws(
  () => normalizeAccountCredentialsForWrite('api_key', {
    api_key: 'sk-glm-ant',
    base_url: 'https://open.bigmodel.cn/api/anthropic',
    supported_endpoint_modes: ['message_token_counting']
  }, {
    providerCode: 'glm',
    accountType: 'api_key',
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID
  }),
  /智谱 GLM Coding Anthropic 账户上游接口能力只支持 Messages API/,
  'GLM Coding Anthropic 凭据归一化应拒绝 count_tokens 能力'
)

const chatOnly = account('chat-only', ['chat_json', 'chat_sse'])
const responsesOnly = account('responses-only', ['responses_json', 'responses_sse'])
const responsesJsonOnly = account('responses-json-only', ['responses_json'])
const responsesSseOnly = account('responses-sse-only', ['responses_sse'])
const jsonOnly = account('json-only', ['chat_json', 'responses_json'])
const codexCapableApiKey = gptApiKeyAccount('gpt-api-key-codex', ['responses_json', 'responses_sse'])
const gptResponsesJsonOnly = gptApiKeyAccount('gpt-responses-json-only', ['responses_json'])
const gptResponsesSseOnly = gptApiKeyAccount('gpt-responses-sse-only', ['responses_sse'])
const hybridResponsesJsonOnly = hybridOpenAIAccount('hybrid-responses-json-only', ['responses_json'])
const hybridResponsesSseOnly = hybridOpenAIAccount('hybrid-responses-sse-only', ['responses_sse'])
const deepSeekChatOnlyAccount = deepSeekApiKeyAccount('deepseek-chat-only', ['chat_json', 'chat_sse'], 'codex_responses')
const deepSeekResponsesToChatMappedAccount = deepSeekApiKeyAccount('deepseek-responses-to-chat', ['chat_json', 'chat_sse'], 'codex_responses', [
  {
    sourceModel: 'gpt-5.5',
    sourceEndpointFamily: 'responses',
    upstreamModel: 'deepseek-v4-flash',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }
])
const deepSeekHybridTargetMappedAccount = deepSeekApiKeyAccount('deepseek-hybrid-target-responses-to-chat', ['chat_json', 'chat_sse'], 'openai_standard', [
  {
    sourceModel: 'glm-5.1',
    sourceEndpointFamily: 'responses',
    upstreamModel: 'glm-5.1',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }
])
const hybridOpenAIToAnthropicMessagesAccount = hybridOpenAIToAnthropicAccount('hybrid-openai-chat-to-messages')

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
  '普通 OpenAI Responses 请求只能命中 OpenAI-compatible 账号，GPT API Key 可同时承接'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', false), [
    responsesJsonOnly,
    responsesSseOnly,
    gptResponsesJsonOnly,
    gptResponsesSseOnly,
    hybridResponsesJsonOnly,
    hybridResponsesSseOnly
  ], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['responses-sse-only', 'gpt-responses-sse-only', 'hybrid-responses-sse-only'],
  'Codex 原生 Responses API Key 请求即使原始 stream=false，也必须统一按最终 responses_sse 能力筛选'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [responsesOnly, codexCapableApiKey, oauthAccount('oauth-codex')], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['responses-only', 'gpt-api-key-codex', 'oauth-codex'],
  'Codex Responses 请求可命中具备原生 responses_sse 能力的普通 OpenAI-compatible 账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [responsesOnly, deepSeekChatOnlyAccount], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['responses-only'],
  '没有显式 Responses -> Chat 模型映射时，Codex Responses 请求仍不能命中普通 DeepSeek Chat-only 账号'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true, 'gpt-5.5'), [responsesOnly, deepSeekResponsesToChatMappedAccount], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  ['responses-only', 'deepseek-responses-to-chat'],
  'Codex Responses 请求可由原生 Responses 账号或显式 Responses -> Chat 映射账号承接'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true, 'gpt-5.4-mini'), [deepSeekHybridTargetMappedAccount], {
    requestClientCompatibility: 'codex_responses'
  }).accounts.map((item) => item.id),
  [],
  '没有目标模型覆盖时，客户端原始模型不应误命中混合路由目标模型映射'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true, 'gpt-5.4-mini'), [deepSeekHybridTargetMappedAccount], {
    requestClientCompatibility: 'codex_responses',
    requestModelOverride: 'glm-5.1'
  }).accounts.map((item) => item.id),
  ['deepseek-hybrid-target-responses-to-chat'],
  '混合路由目标选择应能按目标模型命中 Responses -> Chat 映射，而不是按客户端原始模型过滤'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/chat/completions', true, 'claude-sonnet-4-6'), [hybridOpenAIToAnthropicMessagesAccount], {
    requestClientCompatibility: 'openai_standard'
  }).accounts.map((item) => item.id),
  ['hybrid-openai-chat-to-messages'],
  '混合供应商 OpenAI 入口 profile 运行时必须保留 Messages 能力，才能承接 Chat -> Messages 映射'
)
assert.deepEqual(
  filterGatewayAccountsByRequestCapability(request('/v1/responses', true), [deepSeekChatOnlyAccount], {
    requestClientCompatibility: 'openai_standard'
  }).accounts.map((item) => item.id),
  [],
  '普通 OpenAI Responses 请求不应命中 DeepSeek Chat-only 账号'
)

const accountCredentialsNormalizationSource = readFileSync(resolve('src/storage/account-credentials-normalization.ts'), 'utf8')
assert.match(accountCredentialsNormalizationSource, /providerAccountCredentialDriverForContext/)
assert.doesNotMatch(accountCredentialsNormalizationSource, /normalizeOpenAIEndpointModesForWrite/)
assert.doesNotMatch(accountCredentialsNormalizationSource, /normalizeAnthropicEndpointModesForWrite/)
assert.match(accountCredentialsNormalizationSource, /isAnthropicProtocolProfile/)
const accountCredentialDriverRegistrySource = readFileSync(resolve('src/modules/providers/drivers/account-credentials.registry.ts'), 'utf8')
assert.match(accountCredentialDriverRegistrySource, /openAICompatibleAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /gptAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /deepSeekAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /anthropicAccountCredentialDriver/)
assert.match(accountCredentialDriverRegistrySource, /glmAccountCredentialDriver/)

console.log('OpenAI/Anthropic 接口能力矩阵回归通过：默认值、写入校验和候选账号过滤均符合预期')

function request(path: string, stream: boolean, model = 'gpt-5.5'): Request {
  return {
    method: 'POST',
    path,
    originalUrl: path,
    body: { model, stream }
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

function deepSeekApiKeyAccount(
  id: string,
  modes: AccountSupportedEndpointMode[],
  clientCompatibility: 'openai_standard' | 'codex_responses',
  modelMappings: UpstreamAccount['modelMappings'] = []
): UpstreamAccount {
  return {
    ...account(id, modes),
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    baseUrl: 'https://api.deepseek.com',
    clientCompatibility,
    modelMappings
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

function hybridOpenAIAccount(id: string, modes: AccountSupportedEndpointMode[]): UpstreamAccount {
  return {
    ...account(id, modes),
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    supportedEndpointModes: modes,
    credentials: { supported_endpoint_modes: modes }
  } as unknown as UpstreamAccount
}

function hybridOpenAIToAnthropicAccount(id: string): UpstreamAccount {
  const modes = normalizeHybridEndpointModesForRuntime(['messages_json', 'messages_sse'])
  return {
    ...account(id, modes),
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
    supportedEndpointModes: modes,
    supportedModels: ['claude-sonnet-4-6'],
    credentials: { supported_endpoint_modes: modes },
    modelMappings: [{
      sourceModel: 'claude-sonnet-4-6',
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: 'claude-sonnet-4-6',
      upstreamEndpointFamily: 'messages',
      enabled: true
    }]
  } as unknown as UpstreamAccount
}
