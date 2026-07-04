import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  accountTestDefaultPrompt,
  accountTestModelsPath,
  createAnthropicTestRequest,
  createGeminiTestRequest,
  createOpenAIChatCompletionsTestPayload,
  createOpenAIResponsesTestPayload,
  createOpenAITestRequest,
  testPathFromEndpointMode
} from '../../modules/accounts/account-test-request.js'

assert.equal(accountTestDefaultPrompt, '只输出 OK', '账号测试默认 prompt 应保持中文默认值')
assert.equal(accountTestModelsPath, '/v1/models', '模型列表探测路径应保持 /v1/models')

assert.equal(testPathFromEndpointMode('chat_json'), '/v1/chat/completions', 'Chat JSON 测试应使用 Chat Completions 路径')
assert.equal(testPathFromEndpointMode('chat_sse'), '/v1/chat/completions', 'Chat SSE 测试应使用 Chat Completions 路径')
assert.equal(testPathFromEndpointMode('responses_json'), '/v1/responses', 'Responses JSON 测试应使用 Responses 路径')
assert.equal(testPathFromEndpointMode('responses_sse'), '/v1/responses', 'Responses SSE 测试应使用 Responses 路径')
assert.equal(testPathFromEndpointMode('messages_sse'), '/v1/messages', 'Anthropic Messages 测试应使用 Messages 路径')
assert.equal(testPathFromEndpointMode('generate_content_json', 'gemini-2.5-pro'), '/v1beta/models/gemini-2.5-pro:generateContent', 'Gemini JSON 测试应使用 generateContent 路径')
assert.equal(testPathFromEndpointMode('generate_content_sse', 'gemini-2.5-pro'), '/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse', 'Gemini SSE 测试应使用 streamGenerateContent 路径')

const chatRequest = createOpenAITestRequest({
  explicitModel: '  gpt-5.5-chat  ',
  fallbackModel: 'fallback-model',
  prompt: 'ping',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  testEndpointMode: 'chat_json'
})
assert.equal(chatRequest.path, '/v1/chat/completions', 'Chat JSON 本次测试形态应生成 chat 测试路径')
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
}, 'Chat JSON 测试 payload 应保持非流式字段')

const chatSseRequest = createOpenAITestRequest({
  fallbackModel: 'chat-only-model',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  testEndpointMode: 'chat_sse'
})
assert.equal(chatSseRequest.path, '/v1/chat/completions', 'Chat SSE 测试应构造 chat completions 路径')
assert.equal(chatSseRequest.body.stream, true, 'Chat SSE 测试应使用 stream=true')

const oauthRequest = createOpenAITestRequest({
  fallbackModel: 'gpt-5.5-oauth',
  prompt: 'pong',
  isOAuth: true,
  clientCompatibility: 'codex_responses',
  testEndpointMode: 'responses_sse'
})
assert.equal(oauthRequest.path, '/v1/responses', 'OAuth Responses SSE 测试应走 Responses')
assert.equal(oauthRequest.model, 'gpt-5.5-oauth', '无显式模型时应使用 fallback model')
assert.equal(oauthRequest.body.model, 'gpt-5.5-oauth', 'Responses payload 应写入模型')
assert.equal(oauthRequest.body.stream, true, 'Responses SSE 测试应保持流式形态')
assert.equal(oauthRequest.body.store, false, 'OAuth Responses 测试不应存储')
assert.equal(oauthRequest.body.max_output_tokens, 1, 'OAuth Responses 测试应限制输出 token')
assert.deepEqual(oauthRequest.body.include, ['reasoning.encrypted_content'], 'Codex Responses SSE 测试应保留 encrypted reasoning include')

const responsesJsonPayload = createOpenAIResponsesTestPayload('gpt-5.5-json', 'ok', false, 'codex_responses', false)
assert.equal(responsesJsonPayload.stream, false, 'Responses JSON 测试不能被 Codex 画像强行改成 stream')
assert.equal(Object.prototype.hasOwnProperty.call(responsesJsonPayload, 'include'), false, 'Responses JSON 测试不应写入 SSE 专用 include')

const codexSsePayload = createOpenAIResponsesTestPayload('gpt-5.5-codex', 'ok', false, 'codex_responses', true)
assert.equal(codexSsePayload.stream, true, 'Codex Responses SSE 测试必须保持 stream')
assert.equal(codexSsePayload.store, false, 'Codex Responses SSE 测试不应存储')
assert.deepEqual(codexSsePayload.include, ['reasoning.encrypted_content'], 'Codex Responses SSE 测试应保留 encrypted reasoning include')

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

const glmResponsesRequest = createOpenAITestRequest({
  fallbackModel: 'glm-5.2',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'codex_responses',
  testEndpointMode: 'responses_sse'
})
assert.equal(glmResponsesRequest.path, '/v1/responses', '显式 Responses SSE 测试请求必须走 Responses 下游路径')
assert.equal(glmResponsesRequest.body.stream, true, '显式 Responses SSE 测试请求必须使用 SSE')

const anthropicRequest = createAnthropicTestRequest({
  fallbackModel: 'claude-opus-4-8',
  prompt: 'ok',
  supportedEndpointModes: ['messages_json', 'messages_sse'],
  testEndpointMode: 'messages_json'
})
assert.equal(anthropicRequest.path, '/v1/messages', 'Anthropic Messages 测试应使用 Messages 路径')
assert.equal(anthropicRequest.body.stream, false, 'Messages JSON 测试应保持非流式')

const geminiRequest = createGeminiTestRequest({
  fallbackModel: 'gemini-2.5-pro',
  prompt: 'ok',
  testEndpointMode: 'generate_content_sse'
})
assert.equal(geminiRequest.path, '/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse', 'Gemini SSE 测试应使用 streamGenerateContent')
assert.deepEqual(geminiRequest.body, {
  contents: [
    {
      role: 'user',
      parts: [
        {
          text: 'ok'
        }
      ]
    }
  ],
  generationConfig: {
    maxOutputTokens: 1
  }
}, 'Gemini 测试 payload 应使用 generateContent 原生结构')

const requestSource = readFileSync(resolve('src/modules/accounts/account-test-request.ts'), 'utf8')
assert.doesNotMatch(requestSource, /handleOpenAIGatewayRequest|findAccountForTest|flushGatewayAccountSideEffects/, '测试请求 payload 模块不能依赖真实网关编排或账号解析')
const openAITestRequestInputSource = requestSource.match(/export type AccountTestRequestInput[\s\S]*?\n}\n/)?.[0] ?? ''
assert.doesNotMatch(openAITestRequestInputSource, /requestShape|supportedEndpointModes|providerProtocolProfileId/, 'OpenAI 测试请求模块只按本次 testEndpointMode 构造请求，不从真实请求或供应商档案改写')
assert.match(openAITestRequestInputSource, /testEndpointMode:\s*AccountSupportedEndpointMode/, 'OpenAI 测试请求输入必须显式接收本次测试 endpoint mode')
const serviceSource = readFileSync(resolve('src/modules/accounts/account-test.service.ts'), 'utf8')
assert.match(serviceSource, /normalizedAccountTestEndpointModes/, '测试服务必须从账号接口能力限制解析可测试形态')
assert.match(serviceSource, /resolveAccountTestEndpointMode/, '测试服务必须校验本次 testEndpointMode 是否被账号接口能力限制允许')
assert.match(serviceSource, /handleOpenAIGatewayRequest/, '真实网关测试编排仍应留在 account-test.service.ts')
assert.match(serviceSource, /candidateAccounts:\s*\[resolved\.account\]/, '测试服务仍应固定候选账号')
assert.match(serviceSource, /disableSessionAffinity:\s*true/, '测试服务仍应禁用 session affinity')
assert.match(serviceSource, /trafficSource:\s*input\.trafficSource\s*\?\?\s*'manual_account_test'/, '测试服务仍应保留 manual_account_test 默认来源')
const taskQueueSource = readFileSync(resolve('src/modules/accounts/account-test-task-queue.service.ts'), 'utf8')
assert.doesNotMatch(taskQueueSource, /requestShape:/, '管理端手动账号测试不得透传真实请求形态')
assert.doesNotMatch(taskQueueSource, /task\.clientCompatibility/, '管理端手动账号测试任务不得使用客户端画像作为测试请求形态')
assert.match(taskQueueSource, /testEndpointMode:\s*task\.testEndpointMode/, '管理端手动账号测试任务必须透传本次 testEndpointMode')

console.log('账号测试请求构造回归通过：endpoint mode、payload 字段、接口能力限制校验和真实网关编排边界均符合预期')
