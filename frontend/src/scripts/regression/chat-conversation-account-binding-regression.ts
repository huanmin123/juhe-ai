import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// 会话账户唯一绑定 + 模型工具绑定的前端契约回归（AI 问答会话账户唯一绑定设计
// §5/§6 + AI 问答工具体系与主子模型设计 §8/§9/§10）：免弹窗直进、账户→模型→
// 发送顺序流、绑定引导、归档只读与时间线来源展示。

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const bindingDialogSource = readFileSync(new URL('../../views/chat/ChatToolBindingDialog.vue', import.meta.url), 'utf8')
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
assert.match(composerSource, /:disabled="disabled \|\| !accountValue"/, '模型选择器在未选账户时必须禁用')
assert.match(composerSource, /if \(!props\.accountValue\) return '请先选择 AI 账户'/, '发送按钮提示必须引导先选账户')
assert.match(chatViewSource, /async function changeAccount[\s\S]{0,900}chatApi\.updateConversation\(conversation\.id, \{ accountId \}\)/, '账户选择必须经 PATCH accountId 写入')
assert.match(chatViewSource, /if \(conversation\.lastModel && !updated\.lastModel\)[\s\S]{0,400}selectedModel\.value = undefined/, '切换账户导致 lastModel 联动清空时必须重置本地模型选择')
assert.match(composerSource, /@accounts-open|emit\('accounts-open'\)/, '账户下拉展开必须触发按需刷新')

// --- 归档只读：输入禁用、模型不预填、详情隐藏绑定入口 ---

assert.match(chatViewSource, /const conversationArchived = computed\(\(\) => Boolean\(selectedConversation\.value\?\.archived\)\)/, '必须计算归档只读状态')
assert.match(chatViewSource, /:disabled="generating \|\| submissionBlocked \|\| conversationActionLoading \|\| conversationArchived"/, '归档会话必须禁用输入区')
assert.match(chatViewSource, /conversation\.archived \? undefined : conversation\.lastModel/, '归档会话不得预填模型')
assert.match(chatViewSource, /if \(conversationArchived\.value\)[\s\S]{0,220}message\.warning\('当前会话已归档，仅供查看/, '发送预检必须拦截归档会话')
assert.match(chatViewSource, /tool\.kind === 'model' && !detailConversation\.archived/, '归档会话详情不得展示绑定设置入口')

// --- 工具绑定面板与引导（工具体系设计 §8/§9/§10） ---

assert.match(bindingDialogSource, /chatApi\.getToolBindings\(props\.conversation\.id\)/, '绑定弹窗必须从 tool-bindings 端点拉取候选与状态')
assert.match(bindingDialogSource, /searchBinding: candidate \? \{ accountId: candidate\.accountId, modelId: candidate\.modelId \} : null/, '搜索绑定必须以「账户+模型」二元组整体写入或解绑')
assert.match(bindingDialogSource, /imageBinding: candidate \? \{ accountId: candidate\.accountId \} : null/, '生图绑定必须只写账户键')
assert.match(bindingDialogSource, /candidate\.modelId !== props\.conversation\.defaultImageModel[\s\S]{0,300}defaultImageModel/, '生图绑定选定模型与默认图像模型不同时必须一并更新 defaultImageModel')
assert.match(chatStreamSource, /event\.type === 'tool\.binding_required'/, 'SSE 层必须处理 tool.binding_required 事件（否则协议错误中断流）')
assert.match(chatViewSource, /toolEvent\.item\?\.errorCode !== 'tool_binding_required'[\s\S]{0,700}openToolBindingDialog\(/, 'binding_required 事件必须 toast 提示并打开绑定弹窗')
assert.match(chatViewSource, /bindingPromptedKeys/, 'binding_required 引导必须按事件去重')

// --- 时间线：搜索来源与来源计数（§10.3） ---

assert.match(processSource, /if \(tool\.type === 'web_search'\)/, '时间线必须识别应用层 web_search 工具条目')
assert.match(processSource, /statusDetail: `\$\{sourceCount\} 个来源`/, '搜索条目摘要行必须展示来源计数')
assert.match(processSource, /item\.sources[\s\S]{0,200}\.slice\(0, 8\)/, '搜索来源 URL 必须进入可展开明细（限量）')

console.log('会话账户唯一绑定与工具绑定契约回归通过')
