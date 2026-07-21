import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { chatComposerControlWidths } from '../../views/chat/composer/chatComposerControlWidths'

const source = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')

assert.deepEqual(chatComposerControlWidths('model', '', []), { triggerWidth: 112, popupWidth: 112 }, '空模型值必须使用模型最小宽度')
assert.deepEqual(chatComposerControlWidths('reasoning', '思考 中', ['思考 低', '思考 中', '思考 高']), { triggerWidth: 92, popupWidth: 92 }, '中文短标签必须使用思考控件最小宽度')
assert.deepEqual(chatComposerControlWidths('service', 'Service', ['Service', 'Priority']), { triggerWidth: 104, popupWidth: 104 }, 'ASCII 短标签必须使用服务控件最小宽度')
assert.deepEqual(chatComposerControlWidths('model', 'gpt-5.1-codex', ['gpt-5.1-codex', 'gpt-5.1-codex-long']), { triggerWidth: 127, popupWidth: 162 }, 'ASCII 标签按约 7px 加选择器余量估算，弹层按最长选项扩展')
assert.deepEqual(chatComposerControlWidths('service', '服务 优先', ['服务 默认', '服务 优先级扩展']), { triggerWidth: 104, popupWidth: 141 }, 'CJK/全角字符必须按约 14px 估算')
assert.deepEqual(chatComposerControlWidths('model', '👨‍👩‍👧‍👦'.repeat(6), []), { triggerWidth: 120, popupWidth: 112 }, '家庭 ZWJ emoji 必须按一个宽 grapheme 估算')
assert.deepEqual(chatComposerControlWidths('model', '🇨🇳'.repeat(6), []), { triggerWidth: 120, popupWidth: 112 }, '区域指示符组成的旗帜必须按一个宽 grapheme 估算')
assert.deepEqual(chatComposerControlWidths('model', '©️'.repeat(6), []), { triggerWidth: 120, popupWidth: 112 }, 'emoji 与 variation selector 必须按一个宽 grapheme 估算，包括 Latin-1 内的 emoji 基础码点')
assert.deepEqual(chatComposerControlWidths('model', 'e\u0301'.repeat(12), []), { triggerWidth: 120, popupWidth: 112 }, 'ASCII 字母与 combining mark 必须按一个窄 grapheme 估算')
assert.deepEqual(chatComposerControlWidths('service', '中文ABC-123', []), { triggerWidth: 113, popupWidth: 104 }, '中文与 ASCII 混合标签必须分别按宽窄视觉单元估算')
assert.deepEqual(chatComposerControlWidths('model', 'x'.repeat(100), ['中文'.repeat(100)]), { triggerWidth: 200, popupWidth: 200 }, '触发器和弹层都不得超过 200px')

assert.match(source, /const modelControlWidths = computed\(/)
assert.match(source, /const reasoningControlWidths = computed\(/)
assert.match(source, /const serviceTierControlWidths = computed\(/)
assert.match(source, /aria-label="选择模型"[^>]*:style="\{ width: `\$\{modelControlWidths\.triggerWidth\}px` \}"[^>]*:dropdown-match-select-width="modelControlWidths\.popupWidth"/)
assert.match(source, /aria-label="思考级别"[^>]*:style="\{ width: `\$\{reasoningControlWidths\.triggerWidth\}px` \}"[^>]*:dropdown-match-select-width="reasoningControlWidths\.popupWidth"/)
assert.match(source, /aria-label="服务等级"[^>]*:style="\{ width: `\$\{serviceTierControlWidths\.triggerWidth\}px` \}"[^>]*:dropdown-match-select-width="serviceTierControlWidths\.popupWidth"/)
assert.doesNotMatch(source, /popup-match-select-width/, 'Ant Design Vue 4.2.6 不支持 React API 的 popupMatchSelectWidth')
assert.match(source, /modelOptions\.map\(\(item\) => \(\{ label: item\.name, value: item\.id, title: item\.name \}\)\)/, '模型长选项必须使用轻量 name 并保留完整 title')
assert.match(source, /reasoningEffortLabel\(value\)[\s\S]{0,100}title:/, '思考选项必须保留完整 title')
assert.match(source, /supportedServiceTiers[\s\S]{0,220}title:/, '服务选项必须保留完整 title')
assert.match(source, /<\/div>\s*<a-tooltip :title="contextTooltip">[\s\S]*?<\/a-tooltip>\s*<a-tooltip v-if="stoppable"/, '上下文圆环必须位于模型控件之后、发送或停止按钮之前')
assert.match(source, /@media \(max-width: 520px\)/)
assert.match(source, /\.ai-composer-model-controls\s*\{[^}]*min-width:\s*0;[^}]*flex:\s*1;[^}]*overflow-x:\s*auto;/s)
assert.doesNotMatch(source, /@media \(max-width: 520px\)[\s\S]*?\.ai-composer-model-controls\s*\{[^}]*flex-wrap:\s*wrap;/s)
assert.doesNotMatch(source, /font-size:\s*clamp\(|font-size:\s*\d+(?:\.\d+)?vw/)
console.log('chat composer responsive regression passed')
