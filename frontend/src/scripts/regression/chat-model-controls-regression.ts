import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { defaultChatReasoningEffort, defaultChatServiceTier, normalizeChatModelControls, reasoningEffortLabel, selectableChatReasoningEfforts } from '../../views/chat/composer/chatModelControls'

const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const capabilityBase = { supportedApiProtocols: ['responses'], inputModalities: ['text'], outputModalities: ['text'], supportedTools: [] }

assert.equal(reasoningEffortLabel('medium'), '中')
assert.deepEqual(selectableChatReasoningEfforts({ ...capabilityBase, id: 'test', supportedReasoningEfforts: ['low', 'medium', 'high'], supportedServiceTiers: [], contextWindowTokens: 128_000 }), ['low', 'medium', 'high'])
assert.equal(defaultChatReasoningEffort({ ...capabilityBase, id: 'test', supportedReasoningEfforts: ['low', 'medium', 'high'], defaultReasoningEffort: 'high', supportedServiceTiers: [] }), 'low', '思考级别必须默认选择能力列表第一项')
assert.equal(defaultChatReasoningEffort({ ...capabilityBase, id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: [] }), '', '模型不支持思考级别时必须保持为空')
assert.deepEqual(normalizeChatModelControls({
  model: { ...capabilityBase, id: 'test', supportedReasoningEfforts: ['medium', 'high'], supportedServiceTiers: ['default', 'priority'] },
  reasoningEffort: 'high',
  serviceTier: 'priority'
}), { reasoningEffort: 'high', serviceTier: 'priority' }, '刷新同模型能力时必须保留仍然有效的用户选择')
assert.deepEqual(normalizeChatModelControls({
  model: { ...capabilityBase, id: 'test', supportedReasoningEfforts: ['medium'], supportedServiceTiers: ['default'] },
  reasoningEffort: 'high',
  serviceTier: 'priority'
}), { reasoningEffort: 'medium', serviceTier: 'default' }, '刷新同模型能力时必须清除失效值并回落能力列表第一项')
assert.equal(defaultChatServiceTier({ ...capabilityBase, id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: ['default', 'priority'] }), 'default', '服务等级必须默认选择能力列表第一项')
assert.equal(defaultChatServiceTier({ ...capabilityBase, id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: ['priority'] }), 'priority', '仅支持 Priority 时必须默认选择 Priority')
assert.equal(defaultChatServiceTier({ ...capabilityBase, id: 'test', supportedReasoningEfforts: [], supportedServiceTiers: [] }), '', '模型不支持服务等级时必须保持为空')
assert.match(composerSource, /<a-select v-if="reasoningOptions\.length"[^>]*allow-clear[^>]*@update:value="handleReasoningEffortUpdate"/, '思考级别必须可清除并回到由上游决定')
assert.match(composerSource, /<a-select v-if="serviceTierOptions\.length"[^>]*allow-clear[^>]*@update:value="handleServiceTierUpdate"/, '服务等级必须可清除并回到由上游决定')
assert.match(composerSource, /emit\('update:reasoningEffort', value \?\? ''\)/, '清除思考级别时必须归一为空字符串')
assert.match(composerSource, /emit\('update:serviceTier', value \?\? ''\)/, '清除服务等级时必须归一为空字符串')
assert.doesNotMatch(composerSource, /思考 自动|无思考|上下文 自动|aria-label="上下文大小"/, '模型控件不得补充自动、无思考或上下文选择')
assert.match(composerSource, /selectedModelOption\.value\?\.supportedServiceTiers/, '服务等级选项必须只读取模型接口返回能力')
assert.match(composerSource, /if \(!props\.contextStatus\) return '用量暂不可用'/, '缺少上下文状态时提示必须精简')
assert.match(composerSource, /contextStatusLoading/, '上下文状态请求期间必须保留独立加载态')
assert.match(composerSource, /正在加载上下文用量/, '上下文加载态必须使用中文提示')
assert.match(composerSource, /return `\$\{used\} \/ \$\{limit\}\$\{state\}`/, '正常上下文提示只能显示已用量和上限，压缩状态按需追加')
assert.doesNotMatch(composerSource, /return `上下文|上游用量|usageEstimated === false \? '上游用量' : '估算'/, '正常 tooltip 不得显示来源或重复上下文标题')
assert.match(composerSource, /:aria-label="`上下文用量 \$\{contextTooltip\}`"/, '圆环无障碍名称必须保留完整上下文语义')
assert.equal((composerSource.match(/<a-tooltip :title="contextTooltip">/g) ?? []).length, 1, '页面只能挂载一个上下文 tooltip')
console.log('chat model controls regression passed')
