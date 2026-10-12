import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// 会话账户唯一绑定的前端契约回归（AI 问答会话账户唯一绑定设计 §5/§6 + AI 问答
// 简化设计 D2/D5）：免弹窗直进、账户→模型→发送顺序流、锁定禁用态与工具体系退场。

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const chatStreamSource = readFileSync(new URL('../../views/chat/chatStream.ts', import.meta.url), 'utf8')
const processSource = readFileSync(new URL('../../views/chat/chatMessageProcess.ts', import.meta.url), 'utf8')

// --- 免弹窗直进：创建即进入，会话内完成「选账户 → 选模型 → 发送」 ---

assert.match(chatViewSource, /async function createConversationDirectly[\s\S]{0,500}chatApi\.createConversation\(\)/, '新建会话必须免请求体直进空会话')
assert.match(chatViewSource, /@click="createConversationDirectly"/, '新建入口必须直接创建并进入会话')
assert.doesNotMatch(chatViewSource, /ChatCreateConversationModal/, '不得再挂载新建会话弹窗组件')
assert.doesNotMatch(chatTypesSource, /ChatConversationBindMode|bindGroupId|bindGroupName\?:/, '类型层不得保留旧绑定模式与分组快照字段')
assert.match(chatTypesSource, /archived: boolean/, '会话类型必须携带归档只读标记')
assert.doesNotMatch(chatApiSource, /conversation-bind-options|getConversationBindOptions|bindMode/, 'API 层不得保留旧绑定选项端点与 bindMode 契约')
assert.match(chatApiSource, /listChatAccounts: \(\) => unwrap<ChatAccountOption\[\]>\(http\.get\('\/my-chat\/accounts'\)\)/, 'chatApi 必须提供授权范围账户列表方法')

// --- 顺序流：未绑定账户禁发引导，切换账户联动重置模型 ---

assert.match(chatViewSource, /const accountUnbound = computed\(\(\) => Boolean\(selectedConversation\.value && !selectedConversation\.value\.archived && !selectedConversation\.value\.bindAccountId\)\)/, '必须从会话权威字段计算未绑定状态')
assert.match(chatViewSource, /v-else-if="accountUnbound"[\s\S]{0,200}先在下方选择 AI 账户/, '未绑定账户时输入区上方必须显示选择账户引导')
assert.match(chatViewSource, /if \(accountUnbound\.value\)[\s\S]{0,220}message\.warning\('请先选择 AI 账户，再选择模型发送'\)/, '发送预检必须拦截未绑定账户')
assert.match(composerSource, /:disabled="disabled \|\| selectorsLocked \|\| !accountValue"/, '模型选择器在未选账户或会话锁定时必须禁用')
assert.match(composerSource, /if \(!props\.accountValue\) return '请先选择 AI 账户'/, '发送按钮提示必须引导先选账户')
assert.match(chatViewSource, /async function changeAccount[\s\S]{0,1200}chatApi\.updateConversation\(conversation\.id, \{ accountId \}\)/, '账户选择必须经 PATCH accountId 写入')
assert.match(chatViewSource, /if \(selectorsLocked\.value\)[\s\S]{0,200}message\.warning\('会话已绑定账户与模型，更换请新建会话'\)/, '锁定会话的账户切换必须被前端守卫拦截')
assert.match(chatViewSource, /if \(conversation\.lastModel && !updated\.lastModel\)[\s\S]{0,400}selectedModel\.value = undefined/, '切换账户导致 lastModel 联动清空时必须重置本地模型选择')
assert.match(composerSource, /@accounts-open|emit\('accounts-open'\)/, '账户下拉展开必须触发按需刷新')

// --- 锁定（AI 问答简化设计 D5）：lastModel 非空即锁定，账户/模型选择器禁用 ---

assert.match(chatViewSource, /const selectorsLocked = computed\(\(\) => Boolean\(selectedConversation\.value && !selectedConversation\.value\.archived && selectedConversation\.value\.lastModel\)\)/, '锁定状态必须由会话 lastModel 非空派生（归档会话除外）')
assert.match(chatViewSource, /:selectors-locked="selectorsLocked"/, '锁定状态必须下传输入区组件')
assert.match(composerSource, /:disabled="disabled \|\| selectorsLocked"/, '锁定后账户选择器必须禁用')
assert.match(composerSource, /selectorsLocked \? selectorsLockedTooltip : ''/, '锁定后账户/模型选择器必须挂载锁定 tooltip')
assert.match(composerSource, /const selectorsLockedTooltip = '会话已绑定账户与模型，更换请新建会话'/, '锁定 tooltip 必须使用 D5 交互状态表文案')
assert.match(chatViewSource, /chat_conversation_locked/, 'PATCH accountId 的 409 chat_conversation_locked 必须映射为引导文案')
assert.match(chatViewSource, /chat_conversation_binding_changed[\s\S]{0,700}refreshConversationSummary\(request\.conversationId\)/, 'chat_conversation_binding_changed 必须刷新会话详情后恢复输入')

// --- 归档只读：输入禁用、模型不预填 ---

assert.match(chatViewSource, /const conversationArchived = computed\(\(\) => Boolean\(selectedConversation\.value\?\.archived\)\)/, '必须计算归档只读状态')
assert.match(chatViewSource, /:disabled="generating \|\| submissionBlocked \|\| conversationActionLoading \|\| conversationArchived"/, '归档会话必须禁用输入区')
assert.match(chatViewSource, /conversation\.archived \? undefined : conversation\.lastModel/, '归档会话不得预填模型')
assert.match(chatViewSource, /if \(conversationArchived\.value\)[\s\S]{0,220}message\.warning\('当前会话已归档，仅供查看/, '发送预检必须拦截归档会话')

// --- 工具体系退场（AI 问答简化设计 D2）：弹窗、端点、引导事件不得回归 ---

assert.doesNotMatch(chatViewSource, /ChatToolBindingDialog|toolBindingDialogOpen|toolBindingsSummary|handleToolBindingSaved|refreshToolBindingsSummary/, '工具绑定弹窗与摘要链路必须保持退场状态')
assert.doesNotMatch(chatApiSource, /tool-preferences|tool-bindings|media-tasks|getToolBindings|getToolPreferences|updateToolPreferences|mediaTask/, '工具偏好/绑定/媒体任务端点必须保持退场状态')
assert.doesNotMatch(chatStreamSource, /tool\.binding_required|tool_binding_required/, 'SSE 层必须保持拒绝 tool.binding_required 引导事件')
assert.doesNotMatch(chatViewSource, /bindingPromptedKeys|binding_required/, 'binding_required 引导链路必须保持退场状态')
assert.doesNotMatch(chatTypesSource, /ChatToolBindingCandidate|ChatToolPreferencesPatch|ChatToolBindingPatch|ChatMediaTaskSnapshot|ChatConversationToolCapabilit|supportedToolsByProtocol/, '类型层不得保留工具绑定与媒体任务契约')
assert.doesNotMatch(processSource, /tool\.type === 'web_search'|tool\.type === 'generate_image'|tool\.type === 'generate_video'|tool\.type === 'generate_audio'|个来源/, '时间线必须保持删除站内工具投影分支')
assert.match(chatTypesSource, /builtinWebSearch\?: boolean/, '模型能力类型必须携带内置联网搜索标记')

console.log('会话账户唯一绑定与锁定契约回归通过')
