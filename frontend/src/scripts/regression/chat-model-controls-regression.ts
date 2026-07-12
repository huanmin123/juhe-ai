import assert from 'node:assert/strict'
import { chatContextOptions, reasoningEffortLabel } from '../../views/chat/composer/chatModelControls'

assert.equal(reasoningEffortLabel('medium'), '中')
assert.deepEqual(chatContextOptions(), [{ label: '上下文 自动', value: 0 }])
assert.deepEqual(chatContextOptions({ id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: [], contextWindowTokens: 128_000 }).map((item) => item.value), [0, 16_000, 32_000, 64_000, 128_000])
console.log('chat model controls regression passed')
