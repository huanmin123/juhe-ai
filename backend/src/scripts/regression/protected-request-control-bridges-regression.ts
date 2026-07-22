import assert from 'node:assert/strict'

import type { Request } from 'express'

import { buildOpenAIOrAnthropicToGeminiNativeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-gemini-native-bridge.js'
import { buildGeminiGenerateContentChatBridgeBody } from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import { buildGeminiGenerateContentAnthropicMessagesBridgeBody } from '../../modules/providers/drivers/_shared/gemini-anthropic-messages-bridge.js'
import type { ResolvedOpenAIModelMapping } from '../../modules/gateway/protocols/openai-v1/model-mapping.js'

const chatToGemini = mapping('chat_completions')
const responsesToGemini = mapping('responses')

for (const [field, value] of [
  ['service_tier', 'priority'],
  ['reasoning_effort', 'high']
] as const) {
  await assert.rejects(
    buildOpenAIOrAnthropicToGeminiNativeBody(request({
      model: 'client-chat-model',
      messages: [{ role: 'user', content: 'hello' }],
      [field]: value
    }, '/v1/chat/completions'), { mapping: chatToGemini }),
    /Gemini native 上游不能保真映射 OpenAI.*字段/,
    `Chat Completions ${field} 不能在 Gemini bridge 中静默丢失`
  )
}

await assert.doesNotReject(
  buildOpenAIOrAnthropicToGeminiNativeBody(request({
    model: 'client-chat-model',
    messages: [{ role: 'user', content: 'hello' }],
    service_tier: null,
    reasoning: null,
    reasoning_effort: null,
    thinking: null
  }, '/v1/chat/completions'), { mapping: chatToGemini }),
  'Chat Completions 受保护控制显式 null 应视为 no-op'
)

await assert.doesNotReject(
  buildOpenAIOrAnthropicToGeminiNativeBody(request({
    model: 'client-responses-model',
    input: 'hello',
    service_tier: null,
    reasoning: null,
    reasoning_effort: null,
    thinking: null
  }, '/v1/responses'), { mapping: responsesToGemini }),
  'Responses 受保护控制显式 null 应视为 no-op'
)

for (const [field, value] of [
  ['service_tier', 'priority'],
  ['reasoning', { effort: 'high' }]
] as const) {
  await assert.rejects(
    buildOpenAIOrAnthropicToGeminiNativeBody(request({
      model: 'client-responses-model',
      input: 'hello',
      [field]: value
    }, '/v1/responses'), { mapping: responsesToGemini }),
    /Gemini native 上游不能保真映射 OpenAI.*字段/,
    `Responses ${field} 不能在 Gemini bridge 中静默丢失`
  )
}

const geminiThinkingBody = {
  contents: [{ role: 'user', parts: [{ text: 'hello' }] }],
  generationConfig: {
    thinkingConfig: { thinkingBudget: 1024 }
  }
}

await assert.rejects(
  buildGeminiGenerateContentChatBridgeBody(
    request(geminiThinkingBody, '/v1beta/models/gemini-test:generateContent'),
    { defaultModel: 'chat-target' }
  ),
  /Chat Completions 上游不能保真承载 Gemini thinkingConfig/,
  'Gemini thinkingConfig 到 Chat bridge 必须明确失败'
)

const geminiNullThinkingBody = {
  contents: [{ role: 'user', parts: [{ text: 'hello' }] }],
  generationConfig: {
    thinkingConfig: null
  }
}

await assert.doesNotReject(
  buildGeminiGenerateContentChatBridgeBody(
    request(geminiNullThinkingBody, '/v1beta/models/gemini-test:generateContent'),
    { defaultModel: 'chat-target' }
  ),
  'Gemini thinkingConfig=null 到 Chat bridge 应视为 no-op'
)

await assert.doesNotReject(
  buildGeminiGenerateContentAnthropicMessagesBridgeBody(
    request(geminiNullThinkingBody, '/v1beta/models/gemini-test:generateContent'),
    { defaultModel: 'messages-target' }
  ),
  'Gemini thinkingConfig=null 到 Messages bridge 应视为 no-op'
)

await assert.rejects(
  buildGeminiGenerateContentAnthropicMessagesBridgeBody(
    request(geminiThinkingBody, '/v1beta/models/gemini-test:generateContent'),
    { defaultModel: 'messages-target' }
  ),
  /Anthropic Messages 上游不能保真承载 Gemini thinkingConfig/,
  'Gemini thinkingConfig 到 Messages bridge 必须明确失败'
)

console.log('受保护请求控制 bridge 回归通过：无法保真映射时明确拒绝，不静默丢字段')

function mapping(sourceEndpointFamily: 'chat_completions' | 'responses'): ResolvedOpenAIModelMapping {
  return {
    sourceModel: `client-${sourceEndpointFamily}`,
    sourceEndpointFamily,
    upstreamModel: 'gemini-upstream',
    upstreamEndpointFamily: 'generate_content'
  }
}

function request(body: Record<string, unknown>, path: string): Request {
  return {
    body,
    headers: {},
    method: 'POST',
    originalUrl: path,
    path
  } as unknown as Request
}
