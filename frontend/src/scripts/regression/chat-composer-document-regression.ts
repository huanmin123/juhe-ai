import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { composerDocumentToBlocks, composerTextToDocument } from '../../views/chat/composer/chatComposerDocument'

const blocks = composerDocumentToBlocks({ type: 'doc', content: [
  { type: 'paragraph', content: [{ type: 'text', text: '图片前', marks: [{ type: 'bold' }] }, { type: 'chatImageAttachment', attrs: { assetId: 'asset-inline', previewUrl: 'blob:inline', dataUrl: 'data:image/png;base64,AA==' } }, { type: 'text', text: '图片后', marks: [{ type: 'italic' }, { type: 'link', attrs: { href: 'https://example.com' } }] }] },
  { type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: '标题' }] },
  { type: 'blockquote', content: [{ type: 'paragraph', content: [{ type: 'text', text: '引用' }] }] },
  { type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '列表项' }] }] }] },
  { type: 'codeBlock', attrs: { language: 'ts' }, content: [{ type: 'text', text: 'const answer = 42' }] },
  { type: 'chatImageAttachment', attrs: { assetId: 'asset-1', previewUrl: 'blob:test' } }
] })

assert.equal(blocks[0]?.type, 'input_text')
assert.equal((blocks[0] as { text: string }).text, '**图片前**')
assert.deepEqual(blocks[1], { type: 'input_image', assetId: 'asset-inline', previewUrl: 'blob:inline', dataUrl: 'data:image/png;base64,AA==' })
assert.equal((blocks[2] as { text: string }).text, '[*图片后*](https://example.com)')
assert.match((blocks[3] as { text: string }).text, /^## 标题$/)
assert.match((blocks[4] as { text: string }).text, /^> 引用$/)
assert.match((blocks[5] as { text: string }).text, /- 列表项/)
assert.match((blocks[6] as { text: string }).text, /```ts\nconst answer = 42\n```/)
assert.deepEqual(blocks[7], { type: 'input_image', assetId: 'asset-1', previewUrl: 'blob:test', dataUrl: undefined })

const literalMarkdown = '**不是粗体节点**\r\n<script>alert("xss")</script>\r\n\r\n- 仍是字面 Markdown'
const literalDocument = composerTextToDocument(literalMarkdown)
assert.equal(JSON.stringify(literalDocument).includes('bold'), false, 'Markdown 回填必须保持字面文本，不能解析成富文本 mark')
assert.equal(JSON.stringify(literalDocument).includes('<script>'), true, 'HTML/XSS 字符串必须作为普通 text node 保存')
assert.equal(JSON.stringify(literalDocument).includes('script"'), false, '不得把用户字符串解析成 HTML 节点')
const literalRoundTrip = composerDocumentToBlocks(literalDocument)
assert.deepEqual(literalRoundTrip, [{
  type: 'input_text',
  text: '**不是粗体节点**\n<script>alert("xss")</script>\n\n- 仍是字面 Markdown'
}], 'CRLF、空行、Markdown 与 HTML 字符必须安全按文本往返')

const multipleBlankLines = '第一行\n\n\n第三行'
assert.equal((composerDocumentToBlocks(composerTextToDocument(multipleBlankLines))[0] as { text: string }).text, multipleBlankLines, '连续空行不得被静默折叠')

const composerSource = readFileSync('../frontend/src/views/chat/composer/AIComposer.vue', 'utf8')
assert.match(composerSource, /defineExpose\(\{\s*getSnapshot,\s*setText,\s*restore,\s*clear,\s*focus\s*\}\)/, 'AIComposer 必须暴露完整且受控的编辑接口')
assert.match(composerSource, /replaceEditorDocumentWithoutHistory\(editor\.value, composerTextToDocument\(content\)\)/, 'setText 必须用无历史边界写入 Tiptap JSON 字面文本')
assert.doesNotMatch(composerSource, /setText[\s\S]{0,300}setContent\(/, 'setText 绝不能把用户 Markdown 当 HTML 或可撤销 transaction 写入')
assert.match(composerSource, /replaceEditorDocumentWithoutHistory\(editor\.value, cloneDocument\(snapshot\)\)/, 'restore 也必须切断 displaced draft 的 UndoRedo 历史')
assert.match(composerSource, /contentRevision\.value \+= 1/, 'emitUpdate=false 的 setText/restore 仍必须显式驱动发送按钮状态刷新')
assert.match(composerSource, /const hasContent = computed\(\(\) => \{\s*contentRevision\.value/, 'hasContent 必须订阅编辑器内容修订号')
assert.match(composerSource, /const imageItems = computed\(\(\) => \{\s*contentRevision\.value/, '图片附件投影也必须订阅修订号，恢复含图草稿后不能沿用旧缓存')
console.log('chat composer document regression passed')
