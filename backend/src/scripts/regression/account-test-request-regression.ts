import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import {
  accountTestDefaultPrompt,
  accountTestModelsPath,
  createOpenAIChatCompletionsTestPayload,
  createOpenAIResponsesTestPayload,
  createOpenAITestRequest,
  testPathFromRecentShape
} from '../../modules/accounts/account-test-request.js'
import type { RecentOpenAIRequestShape } from '../../storage/repositories.js'

assert.equal(accountTestDefaultPrompt, '只输出 OK', '账号测试默认 prompt 应保持中文默认值')
assert.equal(accountTestModelsPath, '/v1/models', '模型列表探测路径应保持 /v1/models')

assert.equal(testPathFromRecentShape(undefined, true, 'openai_standard'), '/v1/responses', 'OAuth 测试必须固定走 Responses')
assert.equal(testPathFromRecentShape(recentShape('/v1/chat/completions', false), false, 'codex_responses'), '/v1/responses', 'Codex Responses 兼容必须固定走 Responses')
assert.equal(testPathFromRecentShape(recentShape('/v1/chat/completions', false), false, 'openai_standard'), '/v1/chat/completions', '普通 API Key 可沿用近期 chat completions 形态')
assert.equal(testPathFromRecentShape(recentShape('/v1/responses', true), false, 'openai_standard'), '/v1/responses', '普通 API Key 默认走 Responses')
assert.equal(testPathFromRecentShape(undefined, false, 'openai_standard', ['chat_json', 'chat_sse']), '/v1/chat/completions', 'Chat-only 账户测试不能默认走 Responses')
assert.equal(testPathFromRecentShape(recentShape('/v1/responses', true), false, 'openai_standard', ['chat_json']), '/v1/chat/completions', '近期 Responses 形态不应覆盖 Chat-only 能力限制')
assert.equal(
  testPathFromRecentShape(undefined, false, 'codex_responses', ['chat_json', 'chat_sse'], GLM_CODING_OPENAI_V1_PROFILE_ID),
  '/v1/responses',
  'GLM Coding Codex bridge 账户测试应从 Responses 入口进入桥接'
)
assert.equal(
  testPathFromRecentShape(undefined, false, 'codex_responses', ['chat_json', 'chat_sse'], DEEPSEEK_OPENAI_V1_PROFILE_ID),
  '/v1/responses',
  'DeepSeek Codex bridge 账户测试应从 Responses 入口进入桥接'
)

const chatRequest = createOpenAITestRequest({
  explicitModel: '  gpt-5.5-chat  ',
  fallbackModel: 'fallback-model',
  prompt: 'ping',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  requestShape: recentShape('/v1/chat/completions', false)
})
assert.equal(chatRequest.path, '/v1/chat/completions', 'chat completions recent shape 应生成 chat 测试路径')
assert.equal(chatRequest.model, 'gpt-5.5-chat', '显式模型应 trim 后优先使用')
assert.deepEqual(chatRequest.body, {
  model: 'gpt-5.5-chat',
  messages: [
    {
      role: 'user',
      content: 'ping'
    }
  ],
  max_tokens: 1,
  stream: false
}, 'chat completions 测试 payload 应保持原字段')

const oauthRequest = createOpenAITestRequest({
  fallbackModel: 'gpt-5.5-oauth',
  prompt: 'pong',
  isOAuth: true,
  clientCompatibility: 'openai_standard',
  requestShape: recentShape('/v1/chat/completions', false)
})
assert.equal(oauthRequest.path, '/v1/responses', 'OAuth 即使近期是 chat 也必须走 Responses')
assert.equal(oauthRequest.model, 'gpt-5.5-oauth', '无显式模型时应使用 fallback model')
assert.equal(oauthRequest.body.model, 'gpt-5.5-oauth', 'Responses payload 应写入模型')
assert.equal(oauthRequest.body.stream, true, 'OAuth Responses 测试应按有效 Codex SSE 形态执行')
assert.equal(oauthRequest.body.store, false, 'OAuth Responses 测试不应存储')
assert.equal(oauthRequest.body.max_output_tokens, 1, 'OAuth Responses 测试应限制输出 token')

const codexPayload = createOpenAIResponsesTestPayload('gpt-5.5-codex', 'ok', false, 'codex_responses', false)
assert.equal(codexPayload.stream, true, 'Codex Responses 测试必须强制 stream')
assert.equal(codexPayload.store, false, 'Codex Responses 测试不应存储')
assert.deepEqual(codexPayload.include, ['reasoning.encrypted_content'], 'Codex Responses 测试应保留 encrypted reasoning include')

assert.deepEqual(
  createOpenAIChatCompletionsTestPayload('gpt-5.5-chat', 'ok', true),
  {
    model: 'gpt-5.5-chat',
    messages: [
      {
        role: 'user',
        content: 'ok'
      }
    ],
    max_tokens: 1,
    stream: true
  },
  'chat completions payload helper 应保持原字段'
)

const chatOnlyRequest = createOpenAITestRequest({
  fallbackModel: 'chat-only-model',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  supportedEndpointModes: ['chat_json'],
  requestShape: recentShape('/v1/responses', true)
})
assert.equal(chatOnlyRequest.path, '/v1/chat/completions', 'Chat JSON-only 账户应构造 chat completions 测试路径')
assert.equal(chatOnlyRequest.body.stream, false, 'Chat JSON-only 账户测试必须使用非流式 JSON')

const glmCodexBridgeRequest = createOpenAITestRequest({
  fallbackModel: 'glm-5.2',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'codex_responses',
  providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
  supportedEndpointModes: ['chat_json', 'chat_sse']
})
assert.equal(glmCodexBridgeRequest.path, '/v1/responses', 'GLM Coding Codex bridge 测试请求必须走 Responses 下游路径')
assert.equal(glmCodexBridgeRequest.body.stream, true, 'GLM Coding Codex bridge 测试请求必须使用 Responses SSE')

const deepSeekCodexBridgeRequest = createOpenAITestRequest({
  fallbackModel: 'deepseek-v4-flash',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'codex_responses',
  providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
  supportedEndpointModes: ['chat_json', 'chat_sse']
})
assert.equal(deepSeekCodexBridgeRequest.path, '/v1/responses', 'DeepSeek Codex bridge 测试请求必须走 Responses 下游路径')
assert.equal(deepSeekCodexBridgeRequest.body.stream, true, 'DeepSeek Codex bridge 测试请求必须使用 Responses SSE')

const requestSource = readFileSync(resolve('src/modules/accounts/account-test-request.ts'), 'utf8')
assert.doesNotMatch(requestSource, /handleOpenAIGatewayRequest|findAccountForTest|flushGatewayAccountSideEffects/, '测试请求 payload 模块不能依赖真实网关编排或账号解析')
const serviceSource = readFileSync(resolve('src/modules/accounts/account-test.service.ts'), 'utf8')
assert.match(serviceSource, /handleOpenAIGatewayRequest/, '真实网关测试编排仍应留在 account-test.service.ts')
assert.match(serviceSource, /candidateAccounts:\s*\[resolved\.account\]/, '测试服务仍应固定候选账号')
assert.match(serviceSource, /disableSessionAffinity:\s*true/, '测试服务仍应禁用 session affinity')
assert.match(serviceSource, /trafficSource:\s*input\.trafficSource\s*\?\?\s*'manual_account_test'/, '测试服务仍应保留 manual_account_test 默认来源')

console.log('账号测试请求构造回归通过：路径选择、payload 字段、Codex Responses include 和真实网关编排边界均符合预期')

function recentShape(endpoint: string, stream: boolean): RecentOpenAIRequestShape {
  return {
    endpoint,
    stream,
    createdAt: '2026-06-16T00:00:00.000Z'
  }
}
