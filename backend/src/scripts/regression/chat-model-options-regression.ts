import assert from 'node:assert/strict'

import {
  constrainChatGenerationParametersForRoute,
  generationParameterCapabilitiesForModel,
  limitGenerationParameterMaxOutputTokens
} from '../../modules/chat/chat-generation-parameters.js'
import { buildChatModelOptions, ChatModelCapabilityError, chatModelCapabilities, chatModelListOptions, mergeChatModelCapabilities, resolveChatModelRequestOptions } from '../../modules/chat/chat-model-options.js'

const options = buildChatModelOptions(['gpt-test', 'mixed-cache-model', 'vendor-model', 'custom-model'], [{
  model: 'gpt-test',
  supportsPromptCaching: true,
  supportedReasoningEfforts: ['none', 'low', 'medium', 'high'],
  defaultReasoningEffort: 'high',
  supportedServiceTiers: ['priority', 'flex'],
  contextWindowTokens: 128_000,
  maxInputTokens: 96_000,
  maxOutputTokens: 32_000,
  supportedApiProtocols: ['chat_completions', 'responses'],
  inputModalities: ['text', 'image'],
  outputModalities: ['text'],
  supportedTools: ['web_search', 'file_search']
}, {
  model: 'gpt-test',
  supportsPromptCaching: true,
  supportedReasoningEfforts: ['medium', 'high', 'xhigh', 'vendor-experimental'],
  defaultReasoningEffort: 'high',
  supportedServiceTiers: ['priority', 'spot'],
  contextWindowTokens: 64_000,
  maxInputTokens: 48_000,
  maxOutputTokens: 16_000,
  supportedApiProtocols: ['responses'],
  inputModalities: ['text'],
  outputModalities: ['text'],
  supportedTools: ['web_search']
}, {
  model: 'gpt-test',
  supportsPromptCaching: true,
  supportedReasoningEfforts: ['medium', 'high'],
  defaultReasoningEffort: 'medium',
  supportedServiceTiers: ['priority'],
  contextWindowTokens: 64_000,
  maxOutputTokens: 60_000,
  supportedApiProtocols: ['responses'],
  inputModalities: ['text'],
  outputModalities: ['text'],
  supportedTools: ['web_search']
}, {
  model: 'mixed-cache-model',
  supportsPromptCaching: true,
  supportedReasoningEfforts: [],
  supportedServiceTiers: [],
  supportedApiProtocols: ['responses'],
  inputModalities: ['text'],
  outputModalities: ['text'],
  supportedTools: []
}, {
  model: 'mixed-cache-model',
  supportsPromptCaching: false,
  supportedReasoningEfforts: [],
  supportedServiceTiers: [],
  supportedApiProtocols: ['responses'],
  inputModalities: ['text'],
  outputModalities: ['text'],
  supportedTools: []
}, {
  model: 'vendor-model',
  supportsPromptCaching: false,
  supportedReasoningEfforts: ['medium', 'vendor-experimental'],
  defaultReasoningEffort: 'vendor-experimental',
  supportedServiceTiers: ['priority', 'spot'],
  supportedApiProtocols: ['responses'],
  inputModalities: ['text'],
  outputModalities: ['text'],
  supportedTools: []
}])

assert.deepEqual(options, [
  {
    id: 'gpt-test',
    supportsPromptCaching: true,
    supportedReasoningEfforts: ['medium', 'high'],
    supportedServiceTiers: ['default', 'priority'],
    contextWindowTokens: 64_000,
    maxInputTokens: 4_000,
    maxOutputTokens: 16_000,
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: ['web_search'],
    generationParameters: []
  },
  {
    id: 'mixed-cache-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: [],
    supportedServiceTiers: [],
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: [],
    generationParameters: []
  },
  {
    id: 'vendor-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: ['medium'],
    supportedServiceTiers: ['default', 'priority'],
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: [],
    generationParameters: []
  },
  {
    id: 'custom-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: [],
    supportedServiceTiers: [],
    supportedApiProtocols: [],
    inputModalities: [],
    outputModalities: [],
    supportedTools: [],
    generationParameters: []
  }
])

assert.deepEqual(chatModelListOptions(options), [
  { id: 'gpt-test', name: 'gpt-test' },
  { id: 'mixed-cache-model', name: 'mixed-cache-model' },
  { id: 'vendor-model', name: 'vendor-model' },
  { id: 'custom-model', name: 'custom-model' }
], '模型下拉投影只能返回 id 和 name')
assert.deepEqual(chatModelCapabilities(options[0]!), { ...options[0], name: 'gpt-test' }, '单模型能力详情必须保留能力并补充展示名称')
const mergedCapabilities = mergeChatModelCapabilities([options[0]!, {
  ...options[0]!,
  supportedReasoningEfforts: ['high'],
  supportedServiceTiers: ['default'],
  contextWindowTokens: 32_000,
  maxInputTokens: 24_000,
  maxOutputTokens: 8_000
}])
assert.deepEqual(mergedCapabilities && {
  id: mergedCapabilities.id,
  name: mergedCapabilities.name,
  supportedReasoningEfforts: mergedCapabilities.supportedReasoningEfforts,
  supportedServiceTiers: mergedCapabilities.supportedServiceTiers,
  contextWindowTokens: mergedCapabilities.contextWindowTokens,
  maxInputTokens: mergedCapabilities.maxInputTokens,
  maxOutputTokens: mergedCapabilities.maxOutputTokens
}, {
  id: 'gpt-test',
  name: 'gpt-test',
  supportedReasoningEfforts: ['high'],
  supportedServiceTiers: ['default'],
  contextWindowTokens: 32_000,
  maxInputTokens: 4_000,
  maxOutputTokens: 8_000
}, '多供应商同模型能力必须只暴露共同且保守的可用能力')

const model = options[0]
assert(model)
assert.deepEqual(resolveChatModelRequestOptions(model, {}), {
  generationParameters: {},
  contextWindowTokens: 64_000,
  maxInputTokens: 4_000
}, '中转请求未显式选择时不得主动补思考级别或服务等级')
assert.deepEqual(resolveChatModelRequestOptions(model, { reasoningEffort: 'high', serviceTier: 'priority' }), {
  reasoningEffort: 'high',
  serviceTier: 'priority',
  generationParameters: {},
  contextWindowTokens: 64_000,
  maxInputTokens: 4_000
})
assert.throws(
  () => resolveChatModelRequestOptions(model, { reasoningEffort: 'max' }),
  (error) => error instanceof ChatModelCapabilityError && error.code === 'chat_model_capability_mismatch'
)

const parameterModel = buildChatModelOptions(['parameter-model'], [{
  model: 'parameter-model', supportsPromptCaching: false, supportedReasoningEfforts: [], supportedServiceTiers: [],
  supportedApiProtocols: ['chat_completions'], inputModalities: ['text'], outputModalities: ['text'], supportedTools: [],
  generationParameterCapabilities: {
    chat_completions: [
      { parameter: 'temperature', min: 0, max: 2, step: 0.1, defaultValue: 1 },
      { parameter: 'topP', min: 0, max: 1, step: 0.05, defaultValue: 1 },
      { parameter: 'maxOutputTokens', min: 1, max: 4096, step: 1, defaultValue: 1024 }
    ]
  }
}])[0]!
assert.deepEqual(parameterModel.generationParameters.map((item) => item.parameter), ['temperature', 'topP', 'maxOutputTokens'])
assert.deepEqual(resolveChatModelRequestOptions(parameterModel, { generationParameters: { temperature: 0.7, maxOutputTokens: 512 } }).generationParameters, { temperature: 0.7, maxOutputTokens: 512 })
assert.throws(() => resolveChatModelRequestOptions(parameterModel, { generationParameters: { temperature: 0.7, topP: 0.8 } }), ChatModelCapabilityError)
assert.throws(() => resolveChatModelRequestOptions(parameterModel, { generationParameters: { seed: 1 } }), ChatModelCapabilityError)

assert.deepEqual(
  generationParameterCapabilitiesForModel({ providerCode: 'gpt', model: 'gpt-5.4', maxOutputTokens: 8_192 }).responses?.map((item) => item.parameter),
  ['maxOutputTokens'],
  'GPT-5 Responses 仅展示可保真透传的最大输出 Tokens'
)
assert.deepEqual(
  generationParameterCapabilitiesForModel({ providerCode: 'gemini', model: 'gemini-2.5-pro' }).chat_completions?.map((item) => item.parameter),
  ['temperature', 'topP', 'maxOutputTokens'],
  'Gemini 只展示当前 OpenAI 兼容桥能保真转发的参数'
)
assert.deepEqual(
  generationParameterCapabilitiesForModel({ providerCode: 'custom-compatible', model: 'unknown-model' }),
  {},
  '未知兼容模型不得猜测生成参数能力'
)

const catalogLimitedCapabilities = limitGenerationParameterMaxOutputTokens({
  responses: [{ parameter: 'maxOutputTokens', min: 1, max: 128_000, step: 1, defaultValue: 4_096 }]
}, 1_024)
assert.deepEqual(catalogLimitedCapabilities.responses, [
  { parameter: 'maxOutputTokens', min: 1, max: 1_024, step: 1, defaultValue: 1_024 }
], '目录手工覆盖的 maxOutputTokens 必须收紧生成参数上限')

const gptChatCapabilities = generationParameterCapabilitiesForModel({ providerCode: 'gpt', model: 'gpt-4.1' }).chat_completions ?? []
assert.deepEqual(
  constrainChatGenerationParametersForRoute({
    capabilities: gptChatCapabilities,
    model: 'gpt-4.1',
    protocol: 'chat_completions',
    accounts: [{
      providerCode: 'gpt',
      modelMappings: [{
        sourceModel: 'gpt-4.1',
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'claude-test',
        upstreamEndpointFamily: 'messages'
      }]
    }]
  }).map((item) => item.parameter),
  ['temperature', 'topP', 'maxOutputTokens'],
  '跨协议桥接只能展示明确可保真转发的生成参数'
)
assert.deepEqual(
  constrainChatGenerationParametersForRoute({
    capabilities: gptChatCapabilities,
    model: 'gpt-4.1',
    protocol: 'chat_completions',
    accounts: [{ providerCode: 'gpt', type: 'oauth' }]
  }),
  [],
  'GPT OAuth 归一化会移除生成参数，详情和发送校验必须一并隐藏'
)

console.log('chat model options regression passed')
