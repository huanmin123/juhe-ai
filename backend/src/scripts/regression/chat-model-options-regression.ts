import assert from 'node:assert/strict'

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
    supportedTools: ['web_search']
  },
  {
    id: 'mixed-cache-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: [],
    supportedServiceTiers: [],
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: []
  },
  {
    id: 'vendor-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: ['medium'],
    supportedServiceTiers: ['default', 'priority'],
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: []
  },
  {
    id: 'custom-model',
    supportsPromptCaching: false,
    supportedReasoningEfforts: [],
    supportedServiceTiers: [],
    supportedApiProtocols: [],
    inputModalities: [],
    outputModalities: [],
    supportedTools: []
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
  contextWindowTokens: 64_000,
  maxInputTokens: 4_000
}, '中转请求未显式选择时不得主动补思考级别或服务等级')
assert.deepEqual(resolveChatModelRequestOptions(model, { reasoningEffort: 'high', serviceTier: 'priority' }), {
  reasoningEffort: 'high',
  serviceTier: 'priority',
  contextWindowTokens: 64_000,
  maxInputTokens: 4_000
})
assert.throws(
  () => resolveChatModelRequestOptions(model, { reasoningEffort: 'max' }),
  (error) => error instanceof ChatModelCapabilityError && error.code === 'chat_model_capability_mismatch'
)

console.log('chat model options regression passed')
