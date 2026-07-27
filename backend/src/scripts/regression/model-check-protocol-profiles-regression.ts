import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE
} from '../../domain/provider-protocol.js'
import {
  buildLongContextPrompt,
  createModelCheckLongContextRequest,
  createModelCheckProbeRequest,
  createModelCheckStructuredOutputRequest,
  createModelCheckToolCallingRequest
} from '../../modules/model-checks/model-checks.payloads.js'
import { longContextProbeDefinitions, longContextProbeDefinitionsForModel } from '../../modules/model-checks/model-checks.probes.js'
import { parseModelCheckProbeResponse } from '../../modules/model-checks/model-checks-response-parsing.js'
import {
  defaultModel,
  defaultProfile,
  findModelCheckProfileForAccountModel,
  modelCheckProtocolProfiles,
  modelCheckSourceEndpointFamilies,
  pairedModelForProfile,
  supportedModels
} from '../../modules/model-checks/model-checks.profiles.js'
import { hasFunctionCall } from '../../modules/model-checks/model-checks-parsing.js'
import { estimateTokenCountFromText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'

const sharedGoContract = JSON.parse(readFileSync(new URL(
  '../../../../backend-go/internal/modelcheckprofile/testdata/node-model-check-profile-contract.json',
  import.meta.url
), 'utf8')) as {
  defaultModel: string
  defaultProfile: string
  profiles: Array<{
    providerCode: string
    profileIds: string[]
    models: string[]
    sourceEndpointFamilies: string[]
  }>
}
assert.deepEqual({
  defaultModel,
  defaultProfile,
  profiles: modelCheckProtocolProfiles.map((profile) => ({
    providerCode: profile.providerCode,
    profileIds: [...profile.providerProtocolProfileIds],
    models: [...profile.models],
    sourceEndpointFamilies: modelCheckSourceEndpointFamilies(profile)
  }))
}, sharedGoContract, 'Node/Go 模型检测 profile、模型、source family 与默认值必须共用同一契约')

assert(supportedModels.includes('gpt-5.6-sol'), '应注册 GPT-5.6 Sol 完整模型 ID gpt-5.6-sol')
assert(supportedModels.includes('gpt-5.6-terra'), '应注册 GPT-5.6 Terra 完整模型 ID gpt-5.6-terra')
assert(supportedModels.includes('gpt-5.6-luna'), '应注册 GPT-5.6 Luna 完整模型 ID gpt-5.6-luna')
assert(supportedModels.includes('claude-opus-4-8'), '应注册 Anthropic 完整模型 ID claude-opus-4-8')
assert(supportedModels.includes('glm-5.2'), '应注册 GLM 完整模型 ID glm-5.2')
assert(supportedModels.includes('deepseek-v4-flash'), '应注册 DeepSeek 完整模型 ID deepseek-v4-flash')
assert(supportedModels.includes('gemini-3.5-flash'), '应注册 Gemini 完整模型 ID gemini-3.5-flash')
assert(!supportedModels.includes('opus-4-8'), '不应注册 Anthropic 缩写模型 ID opus-4-8')

const gptProfile = findModelCheckProfileForAccountModel({
  providerCode: GPT_VENDOR_CODE,
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID
}, 'gpt-5.6-sol')
assert.equal(gptProfile?.protocol, 'openai_responses', 'GPT profile 应走 OpenAI Responses 检测')
assert.equal(gptProfile?.defaultModel, 'gpt-5.6-sol', 'GPT profile 默认检测模型应跟随当前官方主模型')
assert.equal(gptProfile ? pairedModelForProfile(gptProfile, 'gpt-5.6-sol') : undefined, 'gpt-5.6-terra', 'Sol 辅助对照应优先使用 Terra')
assert.equal(gptProfile ? pairedModelForProfile(gptProfile, 'gpt-5.6-terra') : undefined, 'gpt-5.6-sol', 'Terra 辅助对照应优先使用 Sol')
assert.equal(gptProfile ? pairedModelForProfile(gptProfile, 'gpt-5.6-luna') : undefined, 'gpt-5.6-terra', 'Luna 辅助对照应优先使用 Terra')
assert.equal(gptProfile ? pairedModelForProfile(gptProfile, 'gpt-5.5') : undefined, 'gpt-5.4', '旧 GPT 5.5 辅助对照应保留 5.4')

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

let directJsonParseCount = 0
const countedJson = parseModelCheckProbeResponse({
  protocol: 'openai_responses',
  path: '/v1/responses',
  bodyText: JSON.stringify({
    model: 'gpt-5.6-sol',
    output_text: 'OK',
    usage: { total_tokens: 2 },
    system_fingerprint: 'fp-test'
  }),
  parseOptions: { onJsonParseAttempt: () => { directJsonParseCount += 1 } }
})
assert.equal(countedJson.outputText, 'OK')
assert.equal(countedJson.model, 'gpt-5.6-sol')
assert.equal(countedJson.usage?.total_tokens, 2)
assert.equal(countedJson.systemFingerprint, 'fp-test')
assert.equal(directJsonParseCount, 1, '模型检测 JSON 的输出、模型、用量和指纹必须共享一次解析')

for (const fixture of [
  {
    protocol: 'openai_chat' as const,
    path: '/v1/chat/completions',
    bodyText: 'event: chunk\r\ndata: {"model":"glm-5.2",\r\ndata: "choices":[{"delta":{"content":"OK"}}],"usage":{"total_tokens":3}}',
    expectedModel: 'glm-5.2',
    expectedUsageKey: 'total_tokens',
    expectedUsage: 3
  },
  {
    protocol: 'anthropic_messages' as const,
    path: '/v1/messages',
    bodyText: 'event: message_start\r\ndata: {"message":{"model":"claude-opus-4-8",\r\ndata: "usage":{"input_tokens":4}}}\r\n\r\nevent: content_block_delta\r\ndata: {"delta":{"text":"OK"}}',
    expectedModel: 'claude-opus-4-8',
    expectedUsageKey: 'input_tokens',
    expectedUsage: 4
  },
  {
    protocol: 'gemini_native' as const,
    path: '/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse',
    bodyText: 'data: {"modelVersion":"gemini-3.5-flash",\r\ndata: "candidates":[{"content":{"parts":[{"text":"OK"}]}}],"usageMetadata":{"totalTokenCount":5}}',
    expectedModel: 'gemini-3.5-flash',
    expectedUsageKey: 'totalTokenCount',
    expectedUsage: 5
  }
]) {
  let parseCount = 0
  const parsed = parseModelCheckProbeResponse({
    protocol: fixture.protocol,
    path: fixture.path,
    bodyText: fixture.bodyText,
    parseOptions: { onJsonParseAttempt: () => { parseCount += 1 } }
  })
  assert.equal(parsed.outputText, 'OK', `${fixture.protocol} 必须解析 multi-line data 与 EOF frame`)
  assert.equal(parsed.model, fixture.expectedModel, `${fixture.protocol} 必须从同一 parsed context 提取 model`)
  assert.equal(parsed.usage?.[fixture.expectedUsageKey], fixture.expectedUsage, `${fixture.protocol} 必须从同一 parsed context 提取 usage`)
  assert.equal(parseCount, fixture.protocol === 'anthropic_messages' ? 2 : 1, `${fixture.protocol} 每个 SSE data payload 只能解析一次`)
}

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
assert.equal(longContextTargets.get('context_low'), 8_000, 'GPT 目录 profile 的低档应保持 8k')
assert.equal(longContextTargets.get('context_medium'), 60_000, 'GPT 目录 profile 的中档应保持 60k')
assert((longContextTargets.get('context_high') ?? 0) > 60_000, '高档必须由模型 max input profile 动态计算')
const standardDefinitions = longContextProbeDefinitionsForModel('gpt', 'gpt-5.6-sol')
assert.equal(standardDefinitions.length, 3, '模型检测只保留低/中/高三档上下文探针')
assert.equal(standardDefinitions.some((definition) => definition.level === 'extreme'), false, '模型检测不得执行极限长上下文探针')

const openAiLongContextRequest = createModelCheckLongContextRequest('openai_responses', 'gpt-5.6-sol', longContextProbeDefinitions[2])
assert.equal(openAiLongContextRequest.path, '/v1/responses')
assert.equal(openAiLongContextRequest.body.max_output_tokens, 48)

console.log('模型检测多协议 profile 回归通过：完整模型 ID、请求构造和响应解析符合预期')
