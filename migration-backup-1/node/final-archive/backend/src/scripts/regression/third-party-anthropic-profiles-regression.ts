import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
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
const hybridClaudeBridgeAccount = hybridAnthropicMessagesProfileAccount({
  id: 'acc_hybrid_claude_code_bridge',
  baseUrl: 'https://www.micuapi.ai',
  apiKey: 'sk-hybrid-claude-code-bridge-upstream'
})

assert.equal(providerDriverForAccount(deepSeekAccount)?.id, 'anthropic', 'DeepSeek Claude Code profile 应复用 Anthropic provider driver')
assert.equal(providerDriverForAccount(glmCodingAccount)?.id, 'anthropic', 'GLM Coding Anthropic profile 应复用 Anthropic provider driver')
assert.equal(providerDriverForAccount(hybridClaudeBridgeAccount)?.id, 'hybrid', '混合供应商 Anthropic Messages 档案应使用 hybrid provider driver')
assert.equal(usageSemanticForProfile(deepSeekAccount), 'anthropic', 'DeepSeek Claude Code 使用记录应按 Anthropic usage 语义解析')
assert.equal(usageSemanticForProfile(glmCodingAccount), 'anthropic', 'GLM Coding Anthropic 使用记录应按 Anthropic usage 语义解析')
assert.equal(usageSemanticForProfile(hybridClaudeBridgeAccount), 'anthropic', '混合供应商 Anthropic Messages 档案使用记录应按 Anthropic usage 语义解析')

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

const deepSeekClaudeCodeSignatureRequest = anthropicRequest({
  body: {
    model: 'deepseek-v4-flash',
    max_tokens: 32000,
    messages: [{ role: 'user', content: [{ type: 'text', text: 'hello real claude code signature' }] }],
    stream: true
  },
  headers: {
    accept: 'text/event-stream',
    'user-agent': 'claude-cli/2.1.201 (external, sdk-cli)',
    'anthropic-beta': 'claude-code-20250219,custom-beta',
    'x-claude-code-session-id': 'session_driver_001'
  },
  originalUrl: '/v1/messages?trace=driver'
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(deepSeekAccount, deepSeekClaudeCodeSignatureRequest),
  ['https://api.deepseek.com/anthropic/v1/messages?trace=driver&beta=true'],
  'DeepSeek Claude Code 多信号请求缺少 beta=true 时应在上游 URL 补齐'
)
assert.equal(
  accountSupportsGatewayRequest(deepSeekClaudeCodeSignatureRequest, deepSeekAccount, { requestClientCompatibility: 'claude_code' }),
  true,
  'DeepSeek Claude Code 多信号请求应按 claude_code 兼容能力筛选账号'
)

const hybridGenericBridgeRequest = openAIChatRequest({
  body: {
    model: 'opencode-claude-alias',
    messages: [{ role: 'user', content: 'hello generic opencode bridge' }],
    stream: true
  },
  headers: {
    accept: 'text/event-stream',
    'user-agent': 'ai-sdk/openai-compatible/1.0.29 runtime/bun/1.3.5'
  },
  originalUrl: '/v1/chat/completions?trace=bridge'
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(hybridClaudeBridgeAccount, hybridGenericBridgeRequest),
  ['https://www.micuapi.ai/v1/messages?trace=bridge'],
  '普通 OpenAI Chat -> Anthropic Messages 桥接不应默认追加 Claude Code beta=true'
)

const hybridClaudeCodeBridgeRequest = openAIChatRequest({
  body: {
    model: 'opencode-claude-alias',
    messages: [{ role: 'user', content: 'hello opencode claude bridge' }],
    stream: true,
    tools: [{
      type: 'function',
      function: {
        name: 'todowrite',
        description: 'opencode todo writer',
        parameters: {
          type: 'object',
          properties: {},
          additionalProperties: false
        }
      }
    }],
    tool_choice: 'auto'
  },
  headers: {
    accept: 'text/event-stream',
    'user-agent': 'ai-sdk/openai-compatible/1.0.29 runtime/bun/1.3.5',
    'x-juhe-client-profile': 'claude_code'
  },
  originalUrl: '/v1/chat/completions?trace=bridge'
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(hybridClaudeBridgeAccount, hybridClaudeCodeBridgeRequest),
  ['https://www.micuapi.ai/v1/messages?trace=bridge&beta=true'],
  '显式 claude_code 的 OpenAI Chat -> Anthropic Messages 桥接应按上游 Messages 目标补齐 beta=true'
)
assert.equal(
  accountSupportsGatewayRequest(hybridClaudeCodeBridgeRequest, hybridClaudeBridgeAccount),
  true,
  '混合供应商 Anthropic Messages 账号应承接显式映射的 OpenAI Chat 桥接请求'
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

const deepSeekClaudeCodeParts = await buildGatewayUpstreamRequestParts(deepSeekClaudeCodeSignatureRequest, deepSeekAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_deepseek_anthropic'
}, undefined, {
  requestClientCompatibility: 'claude_code'
})
assert.equal(deepSeekClaudeCodeParts.headers.get('user-agent'), 'claude-cli/2.1.201 (external, sdk-cli)', 'Claude Code 真实 User-Agent 应保留')
assert.equal(
  deepSeekClaudeCodeParts.headers.get('anthropic-beta'),
  'claude-code-20250219,custom-beta,interleaved-thinking-2025-05-14,effort-2025-11-24',
  'Claude Code 上游请求应合并客户端 beta 与默认 beta，且不重复'
)
assert.equal(bodyJson(deepSeekClaudeCodeParts.body).thinking, undefined, '网关不应为合成请求伪造 Claude Code thinking body')

const hybridGenericBridgeParts = await buildGatewayUpstreamRequestParts(hybridGenericBridgeRequest, hybridClaudeBridgeAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_hybrid_claude_code_bridge'
})
assert.equal(hybridGenericBridgeParts.headers.get('user-agent'), 'ai-sdk/openai-compatible/1.0.29 runtime/bun/1.3.5', '普通桥接应保留下游 OpenAI-compatible User-Agent')
assert.equal(hybridGenericBridgeParts.headers.get('anthropic-beta'), null, '普通桥接不应默认注入 Claude Code beta')
assert.equal(bodyJson(hybridGenericBridgeParts.body).thinking, undefined, '普通桥接不应默认注入 Claude Code thinking body')

const hybridClaudeCodeBridgeParts = await buildGatewayUpstreamRequestParts(hybridClaudeCodeBridgeRequest, hybridClaudeBridgeAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_hybrid_claude_code_bridge'
})
assert.equal(hybridClaudeCodeBridgeParts.headers.get('x-api-key'), 'sk-hybrid-claude-code-bridge-upstream', '混合 Anthropic Messages 上游应使用 x-api-key')
assert.equal(hybridClaudeCodeBridgeParts.headers.get('authorization'), null, '混合 Anthropic Messages 上游不应收到本地 Authorization')
assert.equal(hybridClaudeCodeBridgeParts.headers.get('user-agent'), 'claude-cli/2.1.201 (external, sdk-cli)', '显式 claude_code 桥接应改写 Claude Code User-Agent')
assert.equal(
  hybridClaudeCodeBridgeParts.headers.get('anthropic-beta'),
  'claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24',
  '显式 claude_code 桥接应补齐 Claude Code beta header'
)
assert.equal(typeof hybridClaudeCodeBridgeParts.headers.get('x-claude-code-session-id'), 'string', '显式 claude_code 桥接应补齐 Claude Code session header')
assert.equal(hybridClaudeCodeBridgeParts.headers.get('accept'), 'text/event-stream', '显式 claude_code Chat SSE 桥接应请求上游 Messages SSE')
const hybridClaudeCodeBridgeBody = bodyJson(hybridClaudeCodeBridgeParts.body)
assert.equal(hybridClaudeCodeBridgeBody.model, 'claude-sonnet-4-6', '显式桥接请求体应改写为 Anthropic Messages 上游模型')
assert.equal(Array.isArray(hybridClaudeCodeBridgeBody.system), true, '显式 claude_code 桥接应把上游 system 调整为 Claude Code 兼容数组')
assert.match(JSON.stringify(hybridClaudeCodeBridgeBody.system), /Claude Agent SDK/, '显式 claude_code 桥接应使用真实 Claude Code SDK system 文案')
assert.deepEqual(hybridClaudeCodeBridgeBody.thinking, { type: 'adaptive' }, '显式 claude_code 桥接应补齐 Claude Code thinking body')
assert.deepEqual(hybridClaudeCodeBridgeBody.output_config, { effort: 'high' }, '显式 claude_code 桥接应补齐 Claude Code output_config')
assert.equal(typeof (hybridClaudeCodeBridgeBody.metadata as { user_id?: unknown } | undefined)?.user_id, 'string', '显式 claude_code 桥接应补齐 metadata.user_id')
const hybridClaudeCodeBridgeMetadataUserId = JSON.parse(String((hybridClaudeCodeBridgeBody.metadata as { user_id: string }).user_id)) as { session_id?: string }
assert.equal(
  hybridClaudeCodeBridgeMetadataUserId.session_id,
  hybridClaudeCodeBridgeParts.headers.get('x-claude-code-session-id'),
  '显式 claude_code 桥接的 metadata.user_id.session_id 应与上游 session header 一致'
)
assert.equal(Array.isArray(hybridClaudeCodeBridgeBody.tools), true, '显式 claude_code 桥接应保留 OpenAI function tools 到 Anthropic tools 的正常转换')
assert.equal((hybridClaudeCodeBridgeBody.tools as Array<{ name?: string }>)[0]?.name, 'todowrite', '显式 claude_code 桥接不应丢失 opencode function tool')

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

function hybridAnthropicMessagesProfileAccount(input: {
  id: string
  baseUrl: string
  apiKey: string
}): DispatchAccountSecret {
  return {
    id: input.id,
    providerCode: HYBRID_PROVIDER_CODE,
    providerProtocolProfileId: HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
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
    supportedModels: ['claude-sonnet-4-6'],
    modelMappings: [{
      sourceModel: 'opencode-claude-alias',
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: 'claude-sonnet-4-6',
      upstreamEndpointFamily: 'messages',
      enabled: true
    }],
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

function openAIChatRequest(input: {
  body: Record<string, unknown>
  headers?: Record<string, string>
  originalUrl: string
}): Request {
  const headers: Record<string, string> = {
    authorization: 'Bearer local-gateway-key',
    'content-type': 'application/json',
    ...input.headers
  }
  const path = input.originalUrl.split('?', 1)[0] || input.originalUrl
  const rawBody = Buffer.from(JSON.stringify(input.body), 'utf8')
  return {
    method: 'POST',
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
