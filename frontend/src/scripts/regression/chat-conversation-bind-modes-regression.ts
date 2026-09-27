import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { applyDeletedChatConversation } from '../../views/chat/chatConversationPerformance'

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const createModalSource = readFileSync(new URL('../../views/chat/ChatCreateConversationModal.vue', import.meta.url), 'utf8')
const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')

// --- 类型契约 ---

assert.match(chatTypesSource, /export type ChatConversationBindMode = 'api_key' \| 'group' \| 'account'/, '必须导出三种绑定模式联合类型')
assert.match(
  chatTypesSource,
  /interface ChatConversation[\s\S]{0,600}bindMode: ChatConversationBindMode[\s\S]{0,300}bindGroupId\?: string[\s\S]{0,200}bindGroupName\?: string[\s\S]{0,200}bindAccountId\?: string[\s\S]{0,200}bindAccountName\?: string/,
  '会话类型必须携带绑定模式与对象快照字段'
)

// --- 创建接口契约：总是发送 JSON 体，bindMode 必填且三模式字段互斥 ---

assert.match(
  chatApiSource,
  /interface ChatConversationCreatePayload\s*\{\s*bindMode: ChatConversationBindMode\s*apiKeyId\?: string\s*groupId\?: string\s*accountId\?: string\s*\}/,
  '创建请求体契约必须声明必填 bindMode 与三个互斥的可选绑定对象字段'
)
assert.match(
  chatApiSource,
  /createConversation: \(payload: ChatConversationCreatePayload\) => unwrap<ChatConversation>\(http\.post\('\/my-chat\/conversations', payload\)\)/,
  '创建会话必须总是携带 JSON 请求体，不得保留无参自动绑定默认 Key 的调用'
)

// --- 弹窗组件：三模式 + 三个下拉 + 按模式构建互斥请求体 ---

assert.match(createModalSource, /a-segmented/, '新建会话必须用分段控件选择绑定模式')
assert.match(
  createModalSource,
  /\{ label: 'API Key', value: 'api_key' \}[\s\S]{0,200}\{ label: '分组', value: 'group' \}[\s\S]{0,200}\{ label: '账户', value: 'account' \}/,
  '分段控件必须提供中文标注的三种绑定模式'
)
assert.match(createModalSource, /bindMode === 'api_key'[\s\S]{0,600}:options="apiKeyOptions"/, 'API Key 模式必须渲染 API Key 下拉')
assert.match(createModalSource, /bindMode === 'group'[\s\S]{0,600}:options="groupOptions"/, '分组模式必须渲染分组下拉')
assert.match(createModalSource, /:options="accountOptions"/, '账户模式必须渲染账户下拉')
assert.equal((createModalSource.match(/show-search/g) ?? []).length, 3, '三个下拉都必须支持前端搜索过滤')
assert.match(createModalSource, /api\.myApiKeys\.list\(\{ status: 'active' \}\)/, 'Key 下拉必须来自 self 域可用 Key 列表')
// 分组与账户下拉来自登录用户可用的绑定选项端点（仅启用对象），不再依赖管理员专用的分组/账户 options 接口。
assert.match(
  chatApiSource,
  /getConversationBindOptions: \(\) => unwrap<ChatConversationBindOptions>\(http\.get\('\/my-chat\/conversation-bind-options'\)\)/,
  'chatApi 必须提供登录用户可用的绑定选项方法（GET /my-chat/conversation-bind-options）'
)
assert.match(createModalSource, /chatApi\.getConversationBindOptions\(\)/, '分组与账户下拉必须来自登录用户可用的绑定选项端点（仅启用对象）')
assert.doesNotMatch(createModalSource, /api\.groups\.options/, '弹窗不得再调用管理员专用的分组选项端点')
assert.doesNotMatch(createModalSource, /api\.accounts\.options/, '弹窗不得再调用管理员专用的账户选项端点')
assert.match(
  createModalSource,
  /const payload: ChatConversationCreatePayload = \{ bindMode: bindMode\.value \}[\s\S]{0,200}bindMode\.value === 'api_key'[\s\S]{0,120}payload\.apiKeyId = selectedObjectId\.value[\s\S]{0,200}payload\.groupId = selectedObjectId\.value[\s\S]{0,200}payload\.accountId = selectedObjectId\.value/,
  '创建请求体必须按模式携带对应绑定对象且互斥'
)
assert.match(createModalSource, /:ok-button-props="\{ disabled: !selectedObjectId \}"/, '对象未选择时创建按钮必须禁用')
// 创建在途封死全部关闭通道：结果不得在弹窗关闭后仍在途 emit，也不得借关闭
// 重开重置 creating 造成叠发。
assert.match(createModalSource, /:closable="!creating"/, '创建在途时右上角关闭必须禁用')
assert.match(createModalSource, /:mask-closable="!creating"/, '创建在途时点击遮罩不得关闭')
assert.match(createModalSource, /:keyboard="!creating"/, '创建在途时 Esc 不得关闭')
assert.match(createModalSource, /:cancel-button-props="\{ disabled: creating \}"/, '创建在途时取消按钮必须禁用')
assert.match(createModalSource, /function handleBindModeChange[\s\S]{0,300}selectedObjectId\.value = undefined/, '切换绑定模式必须清空已选对象')
assert.match(createModalSource, /watch\(\(\) => props\.open[\s\S]{0,600}bindMode\.value = 'api_key'[\s\S]{0,300}selectedObjectId\.value = undefined/, '每次打开弹窗不得记忆上次选择')
assert.match(createModalSource, /emit\('created', conversation\)/, '创建成功必须向父组件投递新会话')

// --- ChatView 三处入口统一打开弹窗 ---

const turnLimitBarIndex = chatViewSource.indexOf('class="turn-limit-bar"')
const turnLimitModalIndex = chatViewSource.indexOf('@click="openCreateConversationModal"', turnLimitBarIndex)
assert.ok(turnLimitBarIndex >= 0 && turnLimitModalIndex > turnLimitBarIndex, '轮次上限条的新建入口必须打开绑定模式弹窗')
const startStateIndex = chatViewSource.indexOf('class="chat-start-state"')
const startStateModalIndex = chatViewSource.indexOf('@click="openCreateConversationModal"', startStateIndex)
assert.ok(startStateIndex >= 0 && startStateModalIndex > startStateIndex, '空状态的新建入口必须打开绑定模式弹窗')
assert.match(chatViewSource, /onClick: createConversationFromPane/, '会话面板工具栏新建必须走统一入口')
assert.match(chatViewSource, /function createConversationFromPane[\s\S]{0,600}openCreateConversationModal\(\)/, '面板新建入口必须打开绑定模式弹窗')
assert.match(chatViewSource, /function handleConversationDrawerAfterOpenChange[\s\S]{0,300}openCreateConversationModal\(\)/, '手机抽屉关闭后必须打开绑定模式弹窗')
assert.doesNotMatch(chatViewSource, /chatApi\.createConversation/, '页面容器不得再直接调用创建接口自动绑定默认 Key')
assert.match(chatViewSource, /function handleConversationCreated[\s\S]{0,600}selectConversation\(item\.id\)/, '弹窗创建成功后必须沿用 unshift 加选中的既有路径')

// --- 详情弹窗绑定行 ---

assert.match(chatViewSource, /<a-descriptions-item label="绑定">\{\{ conversationBindLabel\(detailConversation\) \}\}<\/a-descriptions-item>/, '会话详情必须展示绑定行')
assert.match(chatViewSource, /bindMode === 'group'[\s\S]{0,120}分组：\$\{item\.bindGroupName \|\| '已删除'\}/, '分组模式详情必须显示分组名快照，缺失时回退已删除')
assert.match(chatViewSource, /bindMode === 'account'[\s\S]{0,120}账户：\$\{item\.bindAccountName \|\| '已删除'\}/, '账户模式详情必须显示账户名快照，缺失时回退已删除')
assert.match(chatViewSource, /API Key：\$\{item\.apiKeyNameSnapshot \|\| '已删除'\}/, 'API Key 模式详情必须显示 Key 名称快照，缺失时回退已删除')

// --- 去默认会话：进入页面与删除后停留空状态 ---

assert.doesNotMatch(chatViewSource, /conversationItems\[0\]/, '进入页面与待确认会话失效后不得自动选中首个会话')
assert.doesNotMatch(chatViewSource, /nextConversationId/, '删除当前会话后不得自动选中下一项')
assert.match(chatViewSource, /availability === 'not_found'[\s\S]{0,300}clearPendingConfirmation\(storedPending\.request\.systemAccountId\)/, '待确认会话已删除时只清理 pending 并停留空状态')
const deleted = applyDeletedChatConversation({ conversations: [{ id: 'conv_a' }, { id: 'conv_b' }], selectedConversationId: 'conv_a', deletedConversationId: 'conv_a' })
assert.deepEqual(deleted.selectedConversationId, undefined, '删除当前会话后必须解除选择回到空状态')
assert.equal('nextConversationId' in deleted, false, '删除结果不得再携带自动选中的下一项')

console.log('chat conversation bind modes regression passed')
