import assert from 'node:assert/strict'

import { buildChatModelOptions, ChatModelCapabilityError, resolveChatModelRequestOptions } from '../../modules/chat/chat-model-options.js'

const options = buildChatModelOptions(['gpt-test', 'mixed-cache-model', 'custom-model'], [{
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
  supportedReasoningEfforts: ['medium', 'high', 'xhigh'],
  defaultReasoningEffort: 'high',
  supportedServiceTiers: ['priority'],
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
}])

assert.deepEqual(options, [
  {
    id: 'gpt-test',
    supportsPromptCaching: true,
    supportedReasoningEfforts: ['medium', 'high'],
    defaultReasoningEffort: 'medium',
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

const model = options[0]
assert(model)
assert.deepEqual(resolveChatModelRequestOptions(model, {}), {
  reasoningEffort: 'medium',
  serviceTier: 'default',
  contextWindowTokens: 64_000,
  maxInputTokens: 4_000
})
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
