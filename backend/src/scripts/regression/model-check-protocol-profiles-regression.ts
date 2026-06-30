import { strict as assert } from 'node:assert'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import {
  buildLongContextPrompt,
  createModelCheckLongContextRequest,
  createModelCheckProbeRequest,
  createModelCheckStructuredOutputRequest,
  createModelCheckToolCallingRequest
} from '../../modules/model-checks/model-checks.payloads.js'
import { longContextProbeDefinitions } from '../../modules/model-checks/model-checks.probes.js'
import { parseModelCheckProbeResponse } from '../../modules/model-checks/model-checks-response-parsing.js'
import {
  findModelCheckProfileForAccountModel,
  supportedModels
} from '../../modules/model-checks/model-checks.profiles.js'
import { hasFunctionCall } from '../../modules/model-checks/model-checks-parsing.js'
import { estimateTokenCountFromText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'

assert(supportedModels.includes('claude-opus-4-8'), '应注册 Anthropic 完整模型 ID claude-opus-4-8')
assert(supportedModels.includes('glm-5.2'), '应注册 GLM 完整模型 ID glm-5.2')
assert(supportedModels.includes('deepseek-v4-flash'), '应注册 DeepSeek 完整模型 ID deepseek-v4-flash')
assert(supportedModels.includes('gemini-3.5-flash'), '应注册 Gemini 完整模型 ID gemini-3.5-flash')
assert(!supportedModels.includes('opus-4-8'), '不应注册 Anthropic 缩写模型 ID opus-4-8')

const glmProfile = findModelCheckProfileForAccountModel({
  providerCode: GLM_PROVIDER_CODE,
  providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID
}, 'glm-5.2')
assert.equal(glmProfile?.protocol, 'openai_chat', 'GLM general profile 应走 OpenAI Chat 检测')

const deepSeekProfile = findModelCheckProfileForAccountModel({
  providerCode: DEEPSEEK_PROVIDER_CODE,
  providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID
}, 'deepseek-v4-flash')
assert.equal(deepSeekProfile?.protocol, 'openai_chat', 'DeepSeek OpenAI profile 应走 OpenAI Chat 检测')

const anthropicProfile = findModelCheckProfileForAccountModel({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
}, 'claude-opus-4-8')
assert.equal(anthropicProfile?.protocol, 'anthropic_messages', 'Anthropic profile 应走 Messages 检测')

const geminiProfile = findModelCheckProfileForAccountModel({
  providerCode: GEMINI_PROVIDER_CODE,
  providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID
}, 'gemini-3.5-flash')
assert.equal(geminiProfile?.protocol, 'gemini_native', 'Gemini native profile 应走 Gemini native 检测')

const chatRequest = createModelCheckProbeRequest('openai_chat', 'glm-5.2', '只输出 OK', { maxOutputTokens: 16, stream: false })
assert.equal(chatRequest.path, '/v1/chat/completions')
assert.equal(chatRequest.body.model, 'glm-5.2')
assert.equal(chatRequest.body.max_tokens, 64, 'OpenAI Chat 短探针应保留足够输出 token，避免 reasoning tokens 挤占可见输出')

const anthropicRequest = createModelCheckProbeRequest('anthropic_messages', 'claude-opus-4-8', '只输出 OK', { maxOutputTokens: 16, stream: false })
assert.equal(anthropicRequest.path, '/v1/messages')
assert.equal(anthropicRequest.body.model, 'claude-opus-4-8')
assert.equal(anthropicRequest.body.temperature, undefined, 'Anthropic Messages 探针不应发送通用 temperature 参数')

const geminiRequest = createModelCheckProbeRequest('gemini_native', 'gemini-3.5-flash', '只输出 OK', { maxOutputTokens: 16, stream: false })
assert.equal(geminiRequest.path, '/v1beta/models/gemini-3.5-flash:generateContent')
assert.deepEqual(geminiRequest.body.generationConfig, {
  temperature: 0,
  maxOutputTokens: 128
}, 'Gemini native 短探针应保留足够输出 token，兼容强制 thinking mode 的模型')

const chatParsed = parseModelCheckProbeResponse({
  protocol: 'openai_chat',
  path: '/v1/chat/completions',
  bodyText: JSON.stringify({
    model: 'glm-5.2',
    choices: [{ message: { content: 'OK-MODEL-CHECK' } }],
    usage: { prompt_tokens: 8, completion_tokens: 2, total_tokens: 10 }
  })
})
assert.equal(chatParsed.model, 'glm-5.2')
assert.equal(chatParsed.outputText, 'OK-MODEL-CHECK')
assert.equal(chatParsed.usage?.total_tokens, 10)

const anthropicParsed = parseModelCheckProbeResponse({
  protocol: 'anthropic_messages',
  path: '/v1/messages',
  bodyText: JSON.stringify({
    type: 'message',
    model: 'claude-opus-4-8',
    content: [{ type: 'text', text: 'OK-MODEL-CHECK' }],
    usage: { input_tokens: 8, output_tokens: 2 }
  })
})
assert.equal(anthropicParsed.model, 'claude-opus-4-8')
assert.equal(anthropicParsed.outputText, 'OK-MODEL-CHECK')
assert.equal(anthropicParsed.usage?.input_tokens, 8)

const geminiParsed = parseModelCheckProbeResponse({
  protocol: 'gemini_native',
  path: '/v1beta/models/gemini-3.5-flash:generateContent',
  bodyText: JSON.stringify({
    modelVersion: 'gemini-3.5-flash',
    candidates: [{ content: { parts: [{ text: 'OK-MODEL-CHECK' }] } }],
    usageMetadata: { promptTokenCount: 8, candidatesTokenCount: 2, totalTokenCount: 10 }
  })
})
assert.equal(geminiParsed.model, 'gemini-3.5-flash')
assert.equal(geminiParsed.outputText, 'OK-MODEL-CHECK')
assert.equal(geminiParsed.usage?.totalTokenCount, 10)

const chatToolRequest = createModelCheckToolCallingRequest('openai_chat', 'glm-5.2')
assert.equal(chatToolRequest.path, '/v1/chat/completions')
assert(hasFunctionCall({
  choices: [{
    message: {
      tool_calls: [{ function: { name: 'record_model_check', arguments: '{"code":"ok","count":1}' } }]
    }
  }]
}, 'record_model_check'), 'OpenAI Chat tool_calls 应可被评分器识别')

const anthropicToolRequest = createModelCheckToolCallingRequest('anthropic_messages', 'claude-opus-4-8')
assert.equal(anthropicToolRequest.path, '/v1/messages')
assert(hasFunctionCall({
  content: [{ type: 'tool_use', name: 'record_model_check', input: { code: 'ok', count: 1 } }]
}, 'record_model_check'), 'Anthropic tool_use 应可被评分器识别')

const geminiStructuredRequest = createModelCheckStructuredOutputRequest('gemini_native', 'gemini-3.5-flash')
assert.equal(geminiStructuredRequest.path, '/v1beta/models/gemini-3.5-flash:generateContent')

const longContextTargets = new Map(longContextProbeDefinitions.map((definition) => [
  definition.key,
  estimateTokenCountFromText(buildLongContextPrompt(definition))
]))
assert.equal(longContextTargets.get('context_8k'), 8_000, '8k 长上下文窗口应按统一估算器生成到 8000 输入 token')
assert.equal(longContextTargets.get('context_20k'), 20_000, '20k 长上下文窗口应按统一估算器生成到 20000 输入 token')
assert.equal(longContextTargets.get('context_60k'), 60_000, '60k 长上下文窗口应按统一估算器生成到 60000 输入 token')

const openAiLongContextRequest = createModelCheckLongContextRequest('openai_responses', 'gpt-5.5', longContextProbeDefinitions[2])
assert.equal(openAiLongContextRequest.path, '/v1/responses')
assert.equal(openAiLongContextRequest.body.max_output_tokens, 48)

console.log('模型检测多协议 profile 回归通过：完整模型 ID、请求构造和响应解析符合预期')
