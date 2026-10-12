import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { chatComposerCommands } from '../../views/chat/composer/chatComposerCommands'
import { chatErrorMessage } from '../../views/chat/chatErrorMessage'

// AI 问答简化设计（PLAN-20261012T121500000Z）综合回归：工具体系退场（D2）、
// 联网内置透传开关（D3）、会话锁定交互状态（D5）、存量生成媒体块占位（D2）。

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const assistantContentSource = readFileSync(new URL('../../views/chat/ChatAssistantMessageContent.vue', import.meta.url), 'utf8')
const messageListSource = readFileSync(new URL('../../views/chat/ChatMessageList.vue', import.meta.url), 'utf8')
const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const runtimeSource = readFileSync(new URL('../../views/chat/chatGenerationRuntime.ts', import.meta.url), 'utf8')

// --- D2 工具体系退场 ---

for (const source of [chatViewSource, composerSource]) {
  assert.doesNotMatch(source, /tool-defaults|toolBindingsSummary|ChatToolBindingDialog|open-tool-bindings/, '工具绑定入口必须保持退场状态')
}
assert.doesNotMatch(chatComposerCommands.map((item) => item.key).join(','), /tool-defaults/, '命令菜单不得保留 /tool-defaults')
assert.deepEqual(chatComposerCommands.map((item) => item.key), ['image', 'parameters', 'compact', 'clear'], '命令菜单必须收敛为图片/生成参数/压缩/清空')
assert.match(composerSource, /key="image"/, '工具箱必须保留「添加图片」入口（D6 用户图片输入不退场）')
assert.match(composerSource, /handlePaste[\s\S]{0,400}insertMixedClipboardParts|insertPlainClipboardParts/, '图片粘贴链路必须保留')
assert.doesNotMatch(chatTypesSource, /'output_audio'|'output_media_task'/, '类型层必须删除 output_audio/output_media_task 成员')
assert.match(chatTypesSource, /type: 'output_image'/, '类型层必须保留 output_image（历史占位）')
assert.match(chatTypesSource, /ChatImagePolicy/, '图片政策类型必须保留（D6）')
assert.match(chatApiSource, /uploadAsset:/, '用户图片上传端点必须保留（D6）')
assert.match(chatApiSource, /deleteAsset:/, '用户资产删除端点必须保留（D6）')
assert.match(chatApiSource, /chatAssetContentUrl/, '资产内容 URL 必须保留（D6）')
assert.match(chatApiSource, /getImagePolicy:/, '图片政策端点必须保留（D6）')
assert.match(chatApiSource, /listChatAccounts:/, '账户列表端点必须保留（D6）')

// --- D2 存量生成媒体块占位 ---

assert.match(assistantContentSource, /isRetiredMediaBlock\(entry\.block\)[\s\S]{0,200}（媒体生成工具已下线）/, '退役媒体块必须以统一占位文案渲染且不报错')
assert.match(assistantContentSource, /new Set\(\['output_image', 'output_audio', 'output_media_task'\]\)/, '占位判定必须覆盖三类存量生成媒体块')
assert.doesNotMatch(messageListSource, /DownloadOutlined|复制生成图片|downloadGeneratedImage|copyGeneratedImageToClipboard/, '消息列表不得保留生成图下载/复制动作')
assert.match(messageListSource, /aria-label="复制这条消息"/, '用户消息文本复制必须保留')

// --- D3 联网内置透传 ---

assert.match(chatTypesSource, /builtinWebSearch\?: boolean/, '模型能力类型必须携带 builtinWebSearch')
assert.match(composerSource, /<a-checkbox v-if="webSearchAvailable"[\s\S]{0,200}>联网<\/a-checkbox>/, '联网开关必须为可清除勾选项且仅在能力支持时渲染')
assert.match(composerSource, /const webSearchAvailable = computed\(\(\) => selectedModelOption\.value\?\.builtinWebSearch === true\)/, '联网开关显隐必须只依据 builtinWebSearch 能力')
assert.match(composerSource, /const webSearchEnabled = ref\(false\)/, '联网开关必须默认不勾选')
assert.match(composerSource, /watch\(webSearchAvailable, \(available\) => \{\s*if \(!available\) webSearchEnabled\.value = false\s*\}\)/, '切换模型能力不支持时联网开关必须复位为未勾选')
assert.match(composerSource, /webSearchEnabled\.value = false/, '切换会话时联网开关必须复位')
assert.match(composerSource, /enableWebSearch: webSearchEnabled\.value \|\| undefined/, '提交载荷必须仅在勾选时携带 enableWebSearch')
assert.match(chatApiSource, /enableWebSearch\?: boolean/, 'stream 请求体类型必须携带可选 enableWebSearch')
assert.match(chatApiSource, /\.\.\.\(input\.enableWebSearch \? \{ enableWebSearch: true \} : \{\}\)/, '未勾选时 stream 请求体不得携带 enableWebSearch 字段')
assert.match(runtimeSource, /enableWebSearch: input\.enableWebSearch/, '生成运行时必须透传 enableWebSearch 到 stream 依赖')

// --- D5 会话锁定 ---

assert.match(chatViewSource, /const selectorsLocked = computed\(\(\) => Boolean\(selectedConversation\.value && !selectedConversation\.value\.archived && selectedConversation\.value\.lastModel\)\)/, '锁定必须按 lastModel 非空派生（归档会话保持只读链路）')
assert.match(composerSource, /:disabled="disabled \|\| selectorsLocked"/, '锁定后账户选择器必须禁用')
assert.match(composerSource, /:disabled="disabled \|\| selectorsLocked \|\| !accountValue"/, '锁定后模型选择器必须禁用')
assert.match(composerSource, /const selectorsLockedTooltip = '会话已绑定账户与模型，更换请新建会话'/, '锁定 tooltip 必须使用 D5 文案')
assert.match(chatViewSource, /'会话已锁定，切换账户请新建会话'/, 'PATCH accountId 收到 409 必须按映射文案提示且无乐观状态需要回滚')
assert.match(messageListSource, /message\.errorMessage \|\| chatErrorMessage\(message\.errorCode\)/, 'chat_model_locked 等流式失败码经服务端 message 优先 + 映射兜底展示')
assert.match(chatViewSource, /chat_conversation_binding_changed[\s\S]{0,700}refreshConversationSummary/, '绑定并发变化必须刷新会话详情后恢复输入')

// --- 错误码映射（D5 三码 + 兜底） ---

assert.equal(chatErrorMessage('chat_model_locked'), '当前会话已绑定模型，如需更换请新建会话')
assert.equal(chatErrorMessage('chat_conversation_locked'), '会话已锁定，切换账户请新建会话')
assert.equal(chatErrorMessage('chat_conversation_binding_changed'), '会话绑定已变化，请刷新后重试')
assert.equal(chatErrorMessage('undefined_code'), '生成任务异常结束，请重新发送')

console.log('AI 问答简化设计（D2/D3/D5）综合回归通过')
