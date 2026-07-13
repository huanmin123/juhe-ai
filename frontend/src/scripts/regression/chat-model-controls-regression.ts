import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { defaultChatReasoningEffort, defaultChatServiceTier, reasoningEffortLabel, selectableChatReasoningEfforts } from '../../views/chat/composer/chatModelControls'

const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const capabilityBase = { supportedApiProtocols: ['responses'], inputModalities: ['text'], outputModalities: ['text'], supportedTools: [] }

assert.equal(reasoningEffortLabel('medium'), '中')
assert.deepEqual(selectableChatReasoningEfforts({ ...capabilityBase, id: 'test', supportedReasoningEfforts: ['low', 'medium', 'high'], supportedServiceTiers: [], contextWindowTokens: 128_000 }), ['low', 'medium', 'high'])
assert.equal(defaultChatReasoningEffort({ ...capabilityBase, id: 'test', supportedReasoningEfforts: ['low', 'medium', 'high'], defaultReasoningEffort: 'high', supportedServiceTiers: [] }), 'medium')
assert.equal(defaultChatReasoningEffort({ ...capabilityBase, id: 'test', supportedReasoningEfforts: ['low', 'high'], defaultReasoningEffort: 'high', supportedServiceTiers: [] }), 'high')
assert.equal(defaultChatServiceTier({ ...capabilityBase, id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: ['default', 'priority'] }), 'default')
assert.doesNotMatch(composerSource, /思考 自动|无思考|上下文 自动|aria-label="上下文大小"/, '模型控件不得补充自动、无思考或上下文选择')
assert.match(composerSource, /selectedModelOption\.value\?\.supportedServiceTiers/, '服务等级选项必须只读取模型接口返回能力')
console.log('chat model controls regression passed')
