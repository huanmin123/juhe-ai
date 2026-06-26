import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamRequestParts,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount,
  usageSemanticForProfile
} from '../../modules/providers/drivers/registry.js'

const deepSeekAccount = anthropicProfileAccount({
  id: 'acc_deepseek_anthropic',
  providerCode: DEEPSEEK_PROVIDER_CODE,
  providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  baseUrl: 'https://api.deepseek.com/anthropic',
  apiKey: 'sk-deepseek-anthropic-upstream'
})
const glmCodingAccount = anthropicProfileAccount({
  id: 'acc_glm_coding_anthropic',
  providerCode: GLM_PROVIDER_CODE,
  providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  baseUrl: 'https://open.bigmodel.cn/api/anthropic',
  apiKey: 'sk-glm-coding-anthropic-upstream'
})

assert.equal(providerDriverForAccount(deepSeekAccount)?.id, 'anthropic', 'DeepSeek Claude Code profile 应复用 Anthropic provider driver')
assert.equal(providerDriverForAccount(glmCodingAccount)?.id, 'anthropic', 'GLM Coding Anthropic profile 应复用 Anthropic provider driver')
assert.equal(usageSemanticForProfile(deepSeekAccount), 'anthropic', 'DeepSeek Claude Code 使用记录应按 Anthropic usage 语义解析')
assert.equal(usageSemanticForProfile(glmCodingAccount), 'anthropic', 'GLM Coding Anthropic 使用记录应按 Anthropic usage 语义解析')

const deepSeekMessagesRequest = anthropicRequest({
  body: {
    model: 'deepseek-v4-flash',
    max_tokens: 8,
    messages: [{ role: 'user', content: 'hello deepseek claude code' }],
    stream: false
  },
  originalUrl: '/v1/messages?trace=driver'
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(deepSeekAccount, deepSeekMessagesRequest),
  ['https://api.deepseek.com/anthropic/v1/messages?trace=driver'],
  'DeepSeek Claude Code Base URL 应拼接到 /anthropic/v1/messages 并保留查询参数'
)
assert.equal(
  accountSupportsGatewayRequest(deepSeekMessagesRequest, deepSeekAccount),
  true,
  'DeepSeek Claude Code profile 应支持 Messages JSON'
)

const glmSseRequest = anthropicRequest({
  body: {
    model: 'glm-5.2',
    max_tokens: 8,
    messages: [{ role: 'user', content: 'hello glm claude code' }],
    stream: true
  },
  headers: {
    accept: 'text/event-stream'
  },
  originalUrl: '/v1/messages'
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(glmCodingAccount, glmSseRequest),
  ['https://open.bigmodel.cn/api/anthropic/v1/messages'],
  'GLM Coding Anthropic Base URL 应拼接到 /api/anthropic/v1/messages'
)
assert.equal(
  accountSupportsGatewayRequest(glmSseRequest, glmCodingAccount),
  true,
  'GLM Coding Anthropic profile 应支持 Messages Streaming'
)

const countTokensRequest = anthropicRequest({
  body: {
    model: 'glm-5.2',
    messages: [{ role: 'user', content: 'count tokens should not dispatch' }]
  },
  originalUrl: '/v1/messages/count_tokens'
})
assert.equal(
  accountSupportsGatewayRequest(countTokensRequest, deepSeekAccount),
  false,
  'DeepSeek Claude Code profile 不应默认承接 Anthropic count_tokens'
)
assert.equal(
  accountSupportsGatewayRequest(countTokensRequest, glmCodingAccount),
  false,
  'GLM Coding Anthropic profile 不应默认承接 Anthropic count_tokens'
)

const modelsRequest = anthropicRequest({
  body: undefined,
  method: 'GET',
  originalUrl: '/v1/models'
})
assert.equal(
  accountSupportsGatewayRequest(modelsRequest, deepSeekAccount),
  true,
  'DeepSeek Claude Code profile 应支持本地模型列表透传'
)
assert.equal(
  accountSupportsGatewayRequest(modelsRequest, glmCodingAccount),
  true,
  'GLM Coding Anthropic profile 应支持本地模型列表透传'
)

const deepSeekParts = await buildGatewayUpstreamRequestParts(deepSeekMessagesRequest, deepSeekAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_deepseek_anthropic'
})
assert.equal(deepSeekParts.headers.get('x-api-key'), 'sk-deepseek-anthropic-upstream', 'DeepSeek Anthropic-compatible 上游应使用 x-api-key')
assert.equal(deepSeekParts.headers.get('authorization'), null, 'DeepSeek Anthropic-compatible 上游不应收到本地 Authorization')
assert.equal(deepSeekParts.headers.get('anthropic-version'), '2023-06-01', 'DeepSeek Anthropic-compatible 上游应补默认 anthropic-version')
assert.equal(deepSeekParts.headers.get('accept'), 'application/json', 'DeepSeek Messages JSON 上游 accept 应默认为 application/json')
assert.equal(bodyJson(deepSeekParts.body).model, 'deepseek-v4-flash', 'DeepSeek Anthropic-compatible 上游请求体应原样保留模型')

const glmParts = await buildGatewayUpstreamRequestParts(glmSseRequest, glmCodingAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_glm_coding_anthropic'
})
assert.equal(glmParts.headers.get('authorization'), 'Bearer sk-glm-coding-anthropic-upstream', 'GLM Coding Anthropic-compatible 上游应使用 Bearer Authorization')
assert.equal(glmParts.headers.get('x-api-key'), null, 'GLM Coding Anthropic-compatible 上游不应收到本地 x-api-key')
assert.equal(glmParts.headers.get('accept'), 'text/event-stream', 'GLM Coding Messages Streaming 上游 accept 应保留 SSE')
assert.equal(bodyJson(glmParts.body).model, 'glm-5.2', 'GLM Coding Anthropic-compatible 上游请求体应原样保留模型')

console.log('第三方 Anthropic profile 回归通过：DeepSeek Claude Code / GLM Coding Anthropic 的 URL、鉴权、能力和 usage 语义符合预期')

function anthropicProfileAccount(input: {
  id: string
  providerCode: string
  providerProtocolProfileId: string
  baseUrl: string
  apiKey: string
}): DispatchAccountSecret {
  return {
    id: input.id,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: input.id,
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: ['messages_json', 'messages_sse'],
    baseUrl: input.baseUrl,
    apiKey: input.apiKey,
    streamFailureCount: 0,
    credentials: {
      api_key: input.apiKey,
      base_url: input.baseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    }
  } as unknown as DispatchAccountSecret
}

function anthropicRequest(input: {
  body?: Record<string, unknown>
  headers?: Record<string, string>
  method?: string
  originalUrl: string
}): Request {
  const headers: Record<string, string> = {
    authorization: 'Bearer local-gateway-key',
    'x-api-key': 'local-gateway-key',
    'content-type': 'application/json',
    ...input.headers
  }
  const path = input.originalUrl.split('?', 1)[0] || input.originalUrl
  const rawBody = input.body === undefined ? undefined : Buffer.from(JSON.stringify(input.body), 'utf8')
  return {
    method: input.method ?? 'POST',
    path,
    originalUrl: input.originalUrl,
    headers,
    body: input.body,
    rawBody,
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function bodyJson(body: Buffer | string | undefined): Record<string, unknown> {
  assert(body !== undefined, '上游请求体不应为空')
  const text = Buffer.isBuffer(body) ? body.toString('utf8') : body
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), '上游请求体应是 JSON object')
  return parsed as Record<string, unknown>
}
