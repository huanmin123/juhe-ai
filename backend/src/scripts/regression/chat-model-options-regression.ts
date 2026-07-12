import assert from 'node:assert/strict'

import { buildChatModelOptions } from '../../modules/chat/chat-model-options.js'

const options = buildChatModelOptions(['gpt-test', 'custom-model'], [{
  model: 'gpt-test',
  supportedReasoningEfforts: ['low', 'medium', 'high'],
  defaultReasoningEffort: 'medium',
  supportedServiceTiers: ['priority', 'flex'],
  contextWindowTokens: 128_000
}])

assert.deepEqual(options, [
  {
    id: 'gpt-test',
    supportedReasoningEfforts: ['low', 'medium', 'high'],
    defaultReasoningEffort: 'medium',
    supportedServiceTiers: ['priority', 'flex'],
    contextWindowTokens: 128_000
  },
  {
    id: 'custom-model',
    supportedReasoningEfforts: [],
    supportedServiceTiers: []
  }
])

console.log('chat model options regression passed')
