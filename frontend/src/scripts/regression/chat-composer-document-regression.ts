import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { composerDocumentToBlocks, composerTextToDocument } from '../../views/chat/composer/chatComposerDocument'
import { selectChatImageFiles } from '../../views/chat/composer/chatImageSelection'

const blocks = composerDocumentToBlocks({ type: 'doc', content: [
  { type: 'paragraph', content: [{ type: 'text', text: '图片前', marks: [{ type: 'bold' }] }, { type: 'chatImageAttachment', attrs: { assetId: 'asset-inline', previewUrl: 'blob:inline', uploadStatus: 'uploaded' } }, { type: 'text', text: '图片后', marks: [{ type: 'italic' }, { type: 'link', attrs: { href: 'https://example.com' } }] }] },
  { type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: '标题' }] },
  { type: 'blockquote', content: [{ type: 'paragraph', content: [{ type: 'text', text: '引用' }] }] },
  { type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '列表项' }] }] }] },
  { type: 'codeBlock', attrs: { language: 'ts' }, content: [{ type: 'text', text: 'const answer = 42' }] },
  { type: 'chatImageAttachment', attrs: { assetId: 'asset-1', previewUrl: 'blob:test', uploadStatus: 'uploaded' } }
] })

assert.equal(blocks[0]?.type, 'input_text')
assert.equal((blocks[0] as { text: string }).text, '**图片前**')
assert.deepEqual(blocks[1], { type: 'input_image', assetId: 'asset-inline' })
const markdownAfterImage = (blocks[2] as { text: string }).text
assert.match(markdownAfterImage, /^\[\*图片后\*\]\(https:\/\/example\.com\)/)
assert.match(markdownAfterImage, /## 标题/)
assert.match(markdownAfterImage, /> 引用/)
assert.match(markdownAfterImage, /- 列表项/)
assert.match(markdownAfterImage, /```ts\nconst answer = 42\n```/)
assert.deepEqual(blocks[3], { type: 'input_image', assetId: 'asset-1' })
assert.throws(() => composerDocumentToBlocks({ type: 'doc', content: [{ type: 'chatImageAttachment', attrs: { assetId: '', uploadStatus: 'uploading' } }] }), /尚未上传完成/, '上传中或失败的图片不得进入聊天 JSON')

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

const manyParagraphs = composerDocumentToBlocks({
  type: 'doc',
  content: Array.from({ length: 20 }, (_item, index) => ({ type: 'paragraph', content: [{ type: 'text', text: `第 ${index + 1} 段` }] }))
})
assert.equal(manyParagraphs.length, 1, '没有图片分隔的相邻文本块必须合并，不能因段落数突破请求块上限')
assert.match((manyParagraphs[0] as { text: string }).text, /第 1 段[\s\S]*第 20 段/)

const composerSource = readFileSync('../frontend/src/views/chat/composer/AIComposer.vue', 'utf8')
const imageAttachmentSource = readFileSync('../frontend/src/views/chat/composer/ChatImageAttachment.ts', 'utf8')
const imageAttachmentViewSource = readFileSync('../frontend/src/views/chat/composer/ChatImageAttachmentView.vue', 'utf8')
const chatApiSource = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
const chatViewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
assert.match(composerSource, /defineExpose\(\{\s*getSnapshot,\s*setText,\s*setBlocks,\s*restore,\s*clear,\s*focus,\s*releaseSubmittedAssets\s*\}\)/, 'AIComposer 必须暴露文本与多模态草稿的完整受控编辑接口')
assert.match(composerSource, /function setBlocks\([\s\S]*chatImageAttachment[\s\S]*chatAssetContentUrl/, '最近多模态轮次必须按原顺序恢复已有图片资产')
assert.match(composerSource, /replaceEditorContentWithoutHistory\(editor\.value, composerTextToDocument\(content\)\)/, 'setText 必须用 Tiptap 同步且无历史的边界写入 JSON 字面文本')
assert.doesNotMatch(composerSource, /setText[\s\S]{0,300}commands\.setContent\(/, 'setText 不得绕过统一边界直接解析或写入用户字符串')
assert.match(composerSource, /replaceEditorContentWithoutHistory\(editor\.value, cloneDocument\(snapshot\)\)/, 'restore 也必须同步 Tiptap 并切断 displaced draft 的 UndoRedo 历史')
assert.match(composerSource, /contentRevision\.value \+= 1/, 'emitUpdate=false 的 setText/restore 仍必须显式驱动发送按钮状态刷新')
assert.match(composerSource, /const hasContent = computed\(\(\) => \{\s*contentRevision\.value/, 'hasContent 必须订阅编辑器内容修订号')
assert.match(composerSource, /const imageItems = computed\(\(\) => \{\s*contentRevision\.value/, '图片附件投影也必须订阅修订号，恢复含图草稿后不能沿用旧缓存')
assert.match(imageAttachmentViewSource, /<img :src="previewUrl"/, '图片附件必须把本地 object URL 渲染为预览')
assert.doesNotMatch(`${composerSource}\n${imageAttachmentSource}\n${chatViewSource}`, /dataUrl/, '图片不得继续进入 Data URL 或聊天 JSON 链路')
const imageFile = (name: string, size = 1024, type = 'image/png') => ({ name, size, type }) as File
assert.deepEqual(
  selectChatImageFiles([imageFile('1'), imageFile('2'), imageFile('3'), imageFile('4'), imageFile('5')], 0).map((file) => file.name),
  ['1', '2', '3', '4'],
  '一次选择超过 4 张时只能进入剩余槽位数量'
)
assert.deepEqual(selectChatImageFiles([imageFile('3'), imageFile('4')], 3).map((file) => file.name), ['3'], '已有图片必须占用槽位')
assert.deepEqual(selectChatImageFiles([imageFile('large', 32 * 1024 * 1024 + 1), imageFile('text', 1, 'text/plain')], 0), [], '非图片和超过 32 MiB 的文件必须在上传前过滤')
assert.match(composerSource, /for \(const file of selectedFiles\) insertImage\(file\)/, '多图必须同步按选择顺序插入文档，再独立上传')
assert.match(composerSource, /URL\.createObjectURL\(file\)/, '图片预览必须使用本地 object URL，不能把 base64 保存进文档')
assert.match(composerSource, /revokePreviewUrl\(record\.previewUrl\)/, '会话切换或组件卸载必须释放 object URL')
assert.match(composerSource, /record\.file = undefined[\s\S]{0,120}revokePreviewUrl\(previousPreviewUrl\)/, '上传完成后必须立即释放本地大文件引用并改用私有资产 URL')
assert.match(chatViewSource, /composer\.value\?\.releaseSubmittedAssets\(\)/, '消息被确认接收后必须释放已经离开编辑器的 object URL')
assert.match(chatApiSource, /FormData[\s\S]*body\.append\('file'/, '图片必须通过 multipart 独立上传')
assert.match(composerSource, /uploadStatus === 'uploaded'/, '只有上传完成并取得 assetId 的图片才能发送')
assert.match(composerSource, /function submit[\s\S]{0,600}replaceEditorContentWithoutHistory\(editor\.value, emptyComposerDocument\(\)\)/, '成功提交清空必须切断 UndoRedo 历史')
assert.match(composerSource, /conversationGeneration \+= 1[\s\S]{0,100}disposeImageUploadRecords\(\)/, '会话切换或清空必须中止旧会话上传任务')
assert.match(composerSource, /const canSubmit = computed\(\(\) => Boolean\(hasContent\.value && props\.modelValue && !props\.modelsLoading && imagesReady\.value/, '模型未选中、仍在加载或图片未上传完成时不得清空并提交草稿')
assert.match(chatViewSource, /contentBlocks: blocks\.map\(\(block\) => block\.type === 'input_image' \? \{ type: block\.type, assetId: block\.assetId \}/, '聊天提交必须只携带图片 assetId')
assert.match(chatViewSource, /function handleComposerSubmit[\s\S]{0,500}!selectedModel\.value[\s\S]{0,220}composer\.value\?\.restore\(payload\.snapshot\)/, '页面发送边界必须在模型不可用时恢复已清空快照')
console.log('chat composer document regression passed')
