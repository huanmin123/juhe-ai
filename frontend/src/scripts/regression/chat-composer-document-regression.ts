import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { marked } from 'marked'
import { composerDocumentToBlocks, composerTextToDocument } from '../../views/chat/composer/chatComposerDocument'
import { selectChatImageFiles, selectChatImageFileSlots } from '../../views/chat/composer/chatImageSelection'
import { chatImageUploadJpegQuality, chatImageUploadMaxBytes, prepareChatImageForUpload, resolveChatImageUploadSize } from '../../views/chat/composer/chatImageProcessing'
import { chatMixedClipboardParts } from '../../views/chat/composer/chatMixedClipboard'

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
assert.throws(() => composerDocumentToBlocks({
  type: 'doc',
  content: Array.from({ length: 6 }, (_item, index) => ({ type: 'chatImageAttachment', attrs: { assetId: `asset-${index}`, uploadStatus: 'uploaded' } }))
}), /最多 5 张图片/, '文档序列化边界必须拒绝伪造的第 6 张图片')

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
const orderedList = composerDocumentToBlocks({ type: 'doc', content: [{
  type: 'orderedList', attrs: { start: 3 }, content: [
    { type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '第三项' }] }, { type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '嵌套项' }] }] }] }] },
    { type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '第四项' }] }] }
  ]
}] })
const orderedListMarkdown = (orderedList[0] as { text: string }).text
assert.equal(orderedListMarkdown, '3. 第三项\n    - 嵌套项\n4. 第四项', '有序列表编号和嵌套层级必须按 Markdown 保留')
const orderedListHtml = marked.parse(orderedListMarkdown) as string
assert.match(orderedListHtml, /<ol start="3">[\s\S]*<li>第三项[\s\S]*<ul>[\s\S]*<li>嵌套项<\/li>[\s\S]*<\/ul>[\s\S]*<\/li>[\s\S]*<li>第四项<\/li>[\s\S]*<\/ol>/, '发送后的 Markdown 必须能被标准解析器还原为嵌套列表')

const clipboardImage1 = { name: '图一.png', type: 'image/png', size: 8 } as File
const clipboardImage2 = { name: '图二.png', type: 'image/png', size: 8 } as File
const clipboardTree = {
  nodeType: 1,
  nodeName: 'BODY',
  childNodes: [{
    nodeType: 1,
    nodeName: 'P',
    childNodes: [
      { nodeType: 3, nodeName: '#text', nodeValue: '前文', childNodes: [] },
      { nodeType: 1, nodeName: 'IMG', childNodes: [] },
      { nodeType: 3, nodeName: '#text', nodeValue: '中间文字', childNodes: [] },
      { nodeType: 1, nodeName: 'IMG', childNodes: [] },
      { nodeType: 3, nodeName: '#text', nodeValue: '后文', childNodes: [] }
    ]
  }]
} as unknown as Node
assert.deepEqual(
  chatMixedClipboardParts(clipboardTree, [clipboardImage1, clipboardImage2]).map((part) => part.type === 'text' ? part.text : part.file.name),
  ['前文', '图一.png', '中间文字', '图二.png', '后文'],
  '富文本剪贴板必须按 DOM 中的文字与图片原顺序生成编辑器输入'
)
const oversizedClipboardImage = { name: '超大图.png', type: 'image/png', size: 32 * 1024 * 1024 + 1 } as File
const filteredImageSlots = selectChatImageFileSlots([oversizedClipboardImage, clipboardImage2], 0)
assert.deepEqual(filteredImageSlots, [undefined, clipboardImage2], '图片过滤必须保留剪贴板文件槽位，不能压缩后错绑到前一个 HTML 图片位置')
assert.deepEqual(
  chatMixedClipboardParts(clipboardTree, filteredImageSlots).map((part) => part.type === 'text' ? part.text : part.file.name),
  ['前文中间文字', '图二.png', '后文'],
  '前一张图片无效时，后一张合法图片仍必须留在自己的文字位置之后'
)
const remoteDecorationTree = {
  nodeType: 1,
  nodeName: 'BODY',
  childNodes: [{
    nodeType: 1,
    nodeName: 'P',
    childNodes: [
      { nodeType: 3, nodeName: '#text', nodeValue: '远程图前', childNodes: [] },
      { nodeType: 1, nodeName: 'IMG', childNodes: [], getAttribute: (name: string) => name === 'src' ? 'https://example.com/decoration.png' : null },
      { nodeType: 3, nodeName: '#text', nodeValue: '中间', childNodes: [] },
      { nodeType: 1, nodeName: 'IMG', childNodes: [], getAttribute: (name: string) => name === 'src' ? 'data:image/png;base64,AA==' : null },
      { nodeType: 3, nodeName: '#text', nodeValue: '后文', childNodes: [] }
    ]
  }]
} as unknown as Node
assert.deepEqual(
  chatMixedClipboardParts(remoteDecorationTree, [clipboardImage2]).map((part) => part.type === 'text' ? part.text : part.file.name),
  ['远程图前中间', '图二.png', '后文'],
  '没有本地 File 的远程装饰图片不得消耗后续内嵌图片的文件槽位'
)

const composerSource = readFileSync('../frontend/src/views/chat/composer/AIComposer.vue', 'utf8')
const imageProcessingSource = readFileSync('../frontend/src/views/chat/composer/chatImageProcessing.ts', 'utf8')
const imagePreparationStateUrl = new URL('../../views/chat/composer/chatImagePreparationState.ts', import.meta.url)
assert.equal(existsSync(fileURLToPath(imagePreparationStateUrl)), true, '图片异步准备必须使用可独立验证的 generation/token 状态 helper')
const { createChatImagePreparationState } = await import('../../views/chat/composer/chatImagePreparationState')
const keyDownHandlerSource = readFileSync('../frontend/src/views/chat/composer/chatComposerKeyDownHandler.ts', 'utf8')
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
  selectChatImageFiles([imageFile('1'), imageFile('2'), imageFile('3'), imageFile('4'), imageFile('5'), imageFile('6')], 0).map((file) => file.name),
  ['1', '2', '3', '4', '5'],
  '一次选择超过 5 张时只能进入剩余槽位数量'
)
assert.deepEqual(selectChatImageFiles([imageFile('4'), imageFile('5'), imageFile('6')], 4).map((file) => file.name), ['4'], '已有图片和多次粘贴必须共享 5 张总槽位')
assert.deepEqual(selectChatImageFiles([imageFile('large', 32 * 1024 * 1024 + 1), imageFile('text', 1, 'text/plain')], 0), [], '非图片和超过 32 MiB 的文件必须在上传前过滤')
assert.equal(chatImageUploadJpegQuality, 0.85)
assert.equal(chatImageUploadMaxBytes, 1024 * 1024)
assert.deepEqual(resolveChatImageUploadSize(4096, 2048), { width: 1024, height: 512 })
assert.deepEqual(resolveChatImageUploadSize(320, 240), { width: 320, height: 240 })
const preparedImage = await prepareChatImageForUpload(
  new File([new Uint8Array([1, 2, 3])], '透明截图.png', { type: 'image/png', lastModified: 123 }),
  {
    decode: async () => ({ width: 2048, height: 4096 }),
    encodeJpeg: async (_source, size, quality) => {
      assert.deepEqual(size, { width: 512, height: 1024 })
      assert.equal(quality, 0.85)
      return new Blob([new Uint8Array([4, 5])], { type: 'image/jpeg' })
    }
  }
)
assert.deepEqual(
  { name: preparedImage.name, type: preparedImage.type, size: preparedImage.size, lastModified: preparedImage.lastModified },
  { name: '透明截图.jpg', type: 'image/jpeg', size: 2, lastModified: 123 },
  '上传前必须缩放并统一转换为 JPEG 85'
)
await assert.rejects(
  prepareChatImageForUpload(
    new File([new Uint8Array([1])], '超限.png', { type: 'image/png' }),
    {
      decode: async () => ({ width: 1024, height: 1024 }),
      encodeJpeg: async () => new Blob([new Uint8Array(chatImageUploadMaxBytes + 1)], { type: 'image/jpeg' })
    }
  ),
  /压缩后仍超过 1 MiB/,
  '压缩后超过 1 MiB 必须拒绝，不能进入编辑器或上传'
)
const preparationState = createChatImagePreparationState()
assert.equal(typeof preparationState.snapshot, 'function', '图片准备状态必须提供生产可用 snapshot，统一驱动 generation、pending 与有界性观测')
let finishOldPreparation!: () => void
const oldPreparationDeferred = new Promise<void>((resolve) => { finishOldPreparation = resolve })
const oldPreparationToken = preparationState.begin()
const oldPreparationTask = oldPreparationDeferred.finally(() => preparationState.release(oldPreparationToken))
assert.equal(preparationState.pendingCount(), 1, '当前代次新建 token 必须立即进入 pending')
assert.deepEqual(preparationState.snapshot(), { generation: 0, pendingCount: 1, activeTokenCount: 1 })
assert.equal(preparationState.isCurrent(oldPreparationToken), true)
const nextGeneration = preparationState.advanceGeneration()
assert.deepEqual(nextGeneration, { generation: 1, pendingCount: 0, activeTokenCount: 0 }, '推进代次必须物理清除旧 token，避免永不 settle 的 Compressor 任务令 Map 无界增长')
assert.equal(preparationState.pendingCount(), 0, '切换会话或 clear 后旧代次 token 不得阻塞新会话')
assert.equal(preparationState.isCurrent(oldPreparationToken), false)
const newPreparationToken = preparationState.begin()
assert.equal(preparationState.pendingCount(), 1)
assert.deepEqual(preparationState.snapshot(), { generation: 1, pendingCount: 1, activeTokenCount: 1 })
finishOldPreparation()
await oldPreparationTask
assert.equal(preparationState.pendingCount(), 1, '旧任务 finally 只能释放自身 token，不能扣减新代次 pending')
assert.equal(preparationState.snapshot().activeTokenCount, 1, '旧 token 已在推进代次时清除，迟到 release 不能删除新 token')
preparationState.release(oldPreparationToken)
assert.equal(preparationState.pendingCount(), 1, '重复释放旧 token 必须幂等')
preparationState.release(newPreparationToken)
assert.equal(preparationState.pendingCount(), 0)
assert.equal(preparationState.snapshot().activeTokenCount, 0)
preparationState.release(newPreparationToken)
assert.equal(preparationState.pendingCount(), 0, '重复释放当前 token 不能产生负数')
assert.match(composerSource, /for \(const file of selectedFiles\) await insertImage\(file\)/, '多图必须按选择顺序完成压缩门禁后再插入文档')
assert.match(composerSource, /imageItems\.value\.length >= maxChatImageCount/, '逐张插入也必须在编辑器边界复核图片总数，防止并发粘贴突破 5 张')
assert.match(composerSource, /async function insertImage[\s\S]{0,800}await prepareChatImageForUpload[\s\S]{0,800}crypto\.randomUUID/, '图片必须压缩并通过 1 MiB 门禁后才插入编辑器')
assert.match(composerSource, /const pendingImagePreparationCount = ref\(initialImagePreparationSnapshot\.pendingCount\)/, '异步图片准备数量必须由生产 snapshot 初始化为响应式状态，供发送门禁即时消费')
assert.match(composerSource, /const canSubmit = computed\(\(\) => Boolean\([\s\S]{0,320}pendingImagePreparationCount\.value === 0/, '仍有图片压缩任务时必须禁止发送，不能提交半条混合消息')
assert.match(composerSource, /pendingImagePreparationCount\.value > 0[\s\S]{0,120}图片正在压缩/, '图片压缩期间发送按钮必须给出明确中文提示')
assert.match(
  composerSource,
  /async function insertImage[\s\S]{0,420}const preparationConversationId = props\.conversationId[\s\S]{0,180}const preparationGeneration = conversationGeneration[\s\S]{0,180}const preparationEditor = editor\.value[\s\S]{0,600}await prepareChatImageForUpload[\s\S]{0,420}props\.conversationId !== preparationConversationId[\s\S]{0,180}conversationGeneration !== preparationGeneration[\s\S]{0,180}editor\.value !== preparationEditor/,
  '图片压缩完成后必须复核发起时的会话、代次和编辑器实例，丢弃切换会话或 clear 后的迟到结果'
)
assert.match(composerSource, /const preparationToken = imagePreparationState\.begin\(\)[\s\S]{0,500}finally \{[\s\S]{0,180}imagePreparationState\.release\(preparationToken\)[\s\S]{0,120}syncPendingImagePreparationCount\(\)/, '组件必须用独立 token 跟踪每个准备任务，并在 finally 幂等释放后同步当前代次 pending')
assert.match(composerSource, /async function insertPlainClipboardParts[\s\S]{0,260}for \(const file of files\) await insertImage\(file\)/, '普通剪贴板多图也必须串行准备，保持选择顺序')
assert.match(composerSource, /void insertPlainClipboardParts\(event\.clipboardData\?\.getData\('text\/plain'\) \?\? '', selectedFiles\)/, '普通图片粘贴必须通过串行剪贴板入口，不能并发 fire-and-forget 每张图片')
assert.match(imageProcessingSource, /压缩后仍超过 1 MiB/, '前端必须提示压缩后仍超限，不能静默失败')
assert.match(composerSource, /URL\.createObjectURL\(file\)/, '图片预览必须使用本地 object URL，不能把 base64 保存进文档')
assert.match(composerSource, /revokePreviewUrl\(record\.previewUrl\)/, '会话切换或组件卸载必须释放 object URL')
assert.match(composerSource, /record\.file = undefined[\s\S]{0,120}revokePreviewUrl\(previousPreviewUrl\)/, '上传完成后必须立即释放本地大文件引用并改用私有资产 URL')
assert.match(chatViewSource, /composer\.value\?\.releaseSubmittedAssets\(\)/, '消息被确认接收后必须释放已经离开编辑器的 object URL')
assert.match(chatApiSource, /FormData[\s\S]*body\.append\('file'/, '图片必须通过 multipart 独立上传')
assert.match(composerSource, /uploadStatus === 'uploaded'/, '只有上传完成并取得 assetId 的图片才能发送')
assert.match(composerSource, /function submit[\s\S]{0,1000}replaceEditorContentWithoutHistory\(editor\.value, emptyComposerDocument\(\)\)/, '成功提交清空必须切断 UndoRedo 历史')
assert.match(composerSource, /function advanceConversationGeneration[\s\S]{0,220}const snapshot = imagePreparationState\.advanceGeneration\(\)[\s\S]{0,120}conversationGeneration = snapshot\.generation[\s\S]{0,120}pendingImagePreparationCount\.value = snapshot\.pendingCount/, '会话切换或 clear 必须消费有界 snapshot，推进图片准备代次并立即清空当前 pending 展示')
assert.match(composerSource, /advanceConversationGeneration\(\)[\s\S]{0,100}disposeImageUploadRecords\(true\)/, '会话切换或清空必须失效旧准备任务并清理未提交资产')
assert.match(composerSource, /getData\('text\/html'\)[\s\S]{0,420}chatMixedClipboardParts/, '富文本混合剪贴板必须使用 DOM 顺序保留文字和图片')
assert.match(composerSource, /onBeforeUnmount\(\(\) => \{[\s\S]{0,120}disposeImageUploadRecords\(true\)/, '离开 AI 问答页面必须删除已上传但未提交的草稿图片')
assert.match(composerSource, /if \(!isCurrentUploadRecord\(record\) \|\| record\.controller !== controller\) \{[\s\S]{0,180}chatApi\.deleteAsset/, '上传完成与组件卸载竞态必须删除迟到的未提交资产')
assert.match(composerSource, /for \(const \[, record\] of detached\)[\s\S]{0,180}record\.controller\?\.abort\(\)/, '键盘删除图片后必须取消已经脱离文档的上传')
assert.match(composerSource, /for \(const item of pending\) patchImageNode[\s\S]{0,80}pruneDetachedImageRecords\(\)/, '普通 Tiptap 文档更新必须执行脱离图片记录清理')
assert.match(composerSource, /const canSubmit = computed\(\(\) => Boolean\(hasContent\.value && props\.modelValue && !props\.modelsLoading && imagesReady\.value/, '模型未选中、仍在加载或图片未上传完成时不得清空并提交草稿')
assert.match(chatViewSource, /contentBlocks: blocks\.map\(\(block\) => block\.type === 'input_image' \? \{ type: block\.type, assetId: block\.assetId \}/, '聊天提交必须只携带图片 assetId')
assert.match(chatViewSource, /function handleComposerSubmit[\s\S]{0,500}!selectedModel\.value[\s\S]{0,220}composer\.value\?\.restore\(payload\.snapshot\)/, '页面发送边界必须在模型不可用时恢复已清空快照')
assert.match(composerSource, /import \{[^\n]*findChatComposerCommandQuery[^\n]*\} from '\.\/chatComposerCommands'/, 'AIComposer 必须接入基于 EditorState 的命令查询')
assert.match(composerSource, /import \{[^\n]*createChatComposerKeyDownHandler[^\n]*\} from '\.\/chatComposerKeyDownHandler'/, 'AIComposer 必须接入基于事件 EditorState 的键盘处理器')
assert.match(composerSource, /onUpdate: \(\{ editor: nextEditor \}\) => \{[\s\S]{0,220}syncCommandQuery\(nextEditor\)/, '文档更新必须通过统一函数同步命令查询')
assert.match(composerSource, /onSelectionUpdate: \(\{ editor: nextEditor \}\) => \{[\s\S]{0,120}syncCommandQuery\(nextEditor\)/, '光标移动也必须通过统一函数同步命令查询')
assert.match(composerSource, /function syncCommandQuery\([^)]*\): void \{[\s\S]{0,260}findChatComposerCommandQuery\([^)]*\.state\)[\s\S]{0,260}commandOpen\.value = false[\s\S]{0,120}commandQuery\.value = ''[\s\S]{0,120}commandIndex\.value = 0/, '统一命令查询必须在无匹配时关闭菜单并重置状态')
assert.match(keyDownHandlerSource, /structuredBlockNames[\s\S]{0,240}'bulletList'[\s\S]{0,240}'orderedList'[\s\S]{0,240}'blockquote'[\s\S]{0,240}'codeBlock'/, '结构块集合必须覆盖列表、引用和代码块')
assert.match(keyDownHandlerSource, /state\.selection[\s\S]{0,220}\$from\.node\(depth\)/, '键盘决策必须读取本次事件 view.state 的选区祖先，不能依赖异步 editor ref')
assert.match(keyDownHandlerSource, /const action = resolveChatComposerKeyAction\([\s\S]{0,520}if \(action === 'delegate'\) return false/, 'handleKeyDown 必须调用纯决策，delegate 直接交还编辑器')
assert.match(keyDownHandlerSource, /typeof action === 'object'[\s\S]{0,180}event\.preventDefault\(\)[\s\S]{0,180}return true/, '命令移动必须阻止默认行为并消费事件')
assert.match(keyDownHandlerSource, /action === 'select-command'[\s\S]{0,240}context\.selectCommand\(\)[\s\S]{0,160}event\.preventDefault\(\)[\s\S]{0,100}return true/, '命令选择必须先确认候选仍存在再消费事件')
assert.match(keyDownHandlerSource, /action === 'close-command'[\s\S]{0,160}context\.closeCommand\(\)[\s\S]{0,80}return true/, '关闭命令 action 必须只关闭菜单并消费事件')
assert.match(keyDownHandlerSource, /event\.preventDefault\(\)[\s\S]{0,80}context\.submit\(\)[\s\S]{0,80}return true/, '发送 action 必须阻止默认换行并提交')
const placeholderMatch = /placeholder: \(\) => props\.imageInputSupported \? '([^']+)' : '([^']+)'/.exec(composerSource)
assert.ok(placeholderMatch, 'AIComposer 必须为支持与不支持图片的模型提供 placeholder')
const [, imagePlaceholder, textPlaceholder] = placeholderMatch
for (const placeholder of [imagePlaceholder, textPlaceholder]) {
  assert.match(placeholder, /Enter 发送/)
  assert.match(placeholder, /Shift\+Enter 换行/)
  assert.match(placeholder, /Markdown/)
  assert.match(placeholder, /\/ 命令/)
  assert.ok([...placeholder].length <= 50, `placeholder 必须保持简洁，当前长度 ${[...placeholder].length}`)
}
assert.match(imagePlaceholder, /图片/, '支持图片的模型必须提示图片输入')
assert.doesNotMatch(textPlaceholder, /图片/, '不支持图片的模型不得提示图片输入')
assert.match(composerSource, /function selectCommand[\s\S]{0,180}const command = findChatComposerCommandQuery\(editor\.value\.state\)[\s\S]{0,180}if \(!command\)[\s\S]{0,180}return[\s\S]{0,180}if \(item\.key === 'clear'\)/, '所有命令包括 clear 都必须先用最新 EditorState query 通过门禁')
assert.match(composerSource, /function selectCommand[\s\S]{0,520}deleteRange\(command\.range\)/, '非 clear 命令必须只删除最新命令 range')
assert.doesNotMatch(composerSource, /chatComposerCommandQueryRange/, 'AIComposer 不得再引用旧的光标减长度 range API')
assert.doesNotMatch(composerSource, /getText\(\)[\s\S]{0,120}\/(?:\(\?:\^\|\\s\)|\[\^\\s\])/, '命令查询不得再对 editor.getText() 做全文尾正则')
console.log('chat composer document regression passed')
