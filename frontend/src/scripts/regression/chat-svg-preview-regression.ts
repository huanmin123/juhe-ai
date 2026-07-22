import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { isCompleteStaticSvg, resolveChatSvgPreviewSize } from '../../views/chat/chatSvgPreview'

const source = readFileSync('../frontend/src/views/chat/ChatMarkdown.vue', 'utf8')
assert.equal(isCompleteStaticSvg('<svg viewBox="0 0 320 180"><rect width="320" height="180" /></svg>'), true)
assert.equal(isCompleteStaticSvg('<svg><foreignObject><div>原型</div></foreignObject></svg>'), true)
assert.equal(isCompleteStaticSvg('<svg><rect /></svg'), false)
assert.deepEqual(resolveChatSvgPreviewSize('<svg viewBox="0 0 320 180"></svg>'), { width: 320, height: 180 })
assert.deepEqual(resolveChatSvgPreviewSize('<svg></svg>'), { width: 640, height: 360 })
assert.match(source, /frame\.setAttribute\('sandbox', 'allow-scripts'\)/, 'SVG 必须放在隔离 iframe，同时允许原型脚本动效渲染')
assert.match(source, /frame\.srcdoc = svg/, 'SVG 预览必须使用 srcdoc，不注入聊天主 DOM')
assert.match(source, /version !== renderVersion/, '旧异步渲染不得覆盖新内容')
assert.doesNotMatch(source, /if\s*\(!sources\.length\)\s*return/, '没有 Mermaid 时也必须继续处理 fenced SVG')
console.log('AI 问答静态 SVG 预览回归通过')
