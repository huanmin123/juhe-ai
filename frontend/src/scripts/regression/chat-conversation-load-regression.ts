import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import * as conversationLoadModule from '../../views/chat/chatConversationLoad'
import { reconcileChatPendingSubmissionRecovery } from '../../views/chat/chatTurnReconciliation'
import type { ChatMessage } from '../../types/domain/chat'
import type { ChatPendingSubmission } from '../../views/chat/chatPendingSubmissionStorage'

const { isCurrentChatConversationLoad } = conversationLoadModule

assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 2, currentEpoch: 2, disposed: false }), true)
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_a', selectedConversationId: 'conv_b', epoch: 1, currentEpoch: 2, disposed: false }), false, '旧会话响应不能覆盖新选择')
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 1, currentEpoch: 2, disposed: false }), false, '同会话旧 epoch 也不能覆盖较新加载')
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 2, currentEpoch: 2, disposed: true }), false, '卸载后不能写入页面状态')

type ConversationLoadChannels = <TMessage, TModel>(input: {
  loadMessages: () => Promise<TMessage[]>
  loadModels: () => Promise<TModel[]>
}) => {
  messages: Promise<TMessage[]>
  models: Promise<{ ok: true; value: TModel[] } | { ok: false; error: unknown }>
}
const startConversationLoad = (conversationLoadModule as unknown as { startChatConversationLoad?: ConversationLoadChannels }).startChatConversationLoad
assert.equal(typeof startConversationLoad, 'function', '会话消息与模型列表必须通过可测试的独立加载通道启动')
if (!startConversationLoad) throw new Error('startChatConversationLoad 未实现')

const terminalMessages: ChatMessage[] = [{
  id: 'assistant_terminal',
  conversationId: 'conv_pending',
  turnId: 'turn_terminal',
  sequenceNo: 2,
  role: 'assistant',
  status: 'completed',
  contentText: '已经完成',
  contentBlocks: [],
  model: 'mock',
  createdAt: '2026-07-14T00:00:00.000Z',
  expiresAt: '2026-07-21T00:00:00.000Z'
}]
let rejectModels!: (error: unknown) => void
const modelRequest = new Promise<string[]>((_, reject) => { rejectModels = reject })
const channels = startConversationLoad({
  loadMessages: async () => terminalMessages,
  loadModels: () => modelRequest
})
let modelChannelSettled = false
void channels.models.then(() => { modelChannelSettled = true })
const loadedMessages = await channels.messages
assert.deepEqual(loadedMessages, terminalMessages, '消息可读时必须立即交给会话渲染，不能等待模型列表')
assert.equal(modelChannelSettled, false, '持续挂起的模型列表不得阻塞消息主通道')
rejectModels(new Error('模型列表持续失败'))
const failedModels = await channels.models
assert.equal(failedModels.ok, false, '模型列表失败必须收敛为旁路结果，不能拒绝消息主通道')

const recoverySnapshot = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '需要恢复的草稿' }] }] }
const pending: ChatPendingSubmission = {
  request: {
    systemAccountId: 'sys_1',
    conversationId: 'conv_pending',
    clientMessageId: 'client_pending',
    snapshot: recoverySnapshot
  },
  streamStarted: false,
  silent: false,
  errorMessage: '连接中断'
}
const terminalRecovery = await reconcileChatPendingSubmissionRecovery({
  pending,
  ensureConversation: async () => loadedMessages.length ? 'ready' : 'retry',
  reconcile: async () => ({
    messages: loadedMessages,
    confirmed: true,
    accepted: true,
    terminal: true,
    turnId: 'turn_terminal',
    assistantStatus: 'completed'
  })
})
assert.equal(terminalRecovery.action, 'apply', '模型列表失败时 terminal 消息仍必须完成恢复并解除 pending')
assert.deepEqual(terminalRecovery.reconciliation?.messages, terminalMessages)

const notFoundRecovery = await reconcileChatPendingSubmissionRecovery({
  pending,
  ensureConversation: async () => loadedMessages.length ? 'ready' : 'retry',
  reconcile: async () => ({ messages: loadedMessages, confirmed: true, accepted: false, terminal: false, submissionState: 'not_found' })
})
assert.equal(notFoundRecovery.action, 'apply', '模型列表失败时权威 not_found 仍必须允许恢复草稿并解除 pending')
assert.deepEqual(notFoundRecovery.pending.request.snapshot, recoverySnapshot)

const chatViewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const chatApiSource = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
assert.match(chatApiSource, /beforeIsPinned\?:\s*boolean/, '会话 API 游标必须包含置顶维度')
assert.match(chatViewSource, /const CHAT_CONVERSATION_PAGE_SIZE\s*=\s*30/, '首页会话摘要首批必须使用有界轻量页')
assert.match(chatViewSource, /listConversations\(\{ limit: CHAT_CONVERSATION_PAGE_SIZE \}\)/, '首页只请求有界会话摘要，不得无界读取')
assert.match(chatViewSource, /loadMoreConversations/, '会话列表必须允许访问第 51 个及更早会话')
assert.match(chatViewSource, /beforeIsPinned:\s*last\.isPinned/, '加载更多必须传递完整置顶游标')
const selectStartIndex = chatViewSource.indexOf('async function selectConversation(')
const selectEndIndex = chatViewSource.indexOf('async function ensurePendingConversationAvailability', selectStartIndex)
const selectSource = chatViewSource.slice(selectStartIndex, selectEndIndex)
assert.ok(selectStartIndex >= 0 && selectEndIndex > selectStartIndex, '必须能定位会话选择流程')
assert.doesNotMatch(selectSource, /chatApi\.listModels|modelLoadCoordinator\.(?:load|refresh|refreshIfExpired)/, '切换会话与首屏消息同步不得预取模型列表')
assert.match(selectSource, /selectedModel\.value = conversation\.lastModel/, '已有会话必须直接恢复最后使用模型')
assert.match(chatViewSource, /async function loadModelsOnOpen[\s\S]{0,500}modelLoadCoordinator\.refreshIfExpired/, '模型列表必须在下拉展开后才读取并复用短时缓存')
assert.match(chatViewSource, /readConversation[\s\S]{0,900}messages\.value = cached\.value\.messages[\s\S]{0,1200}synchronizeChatConversation/, '页面必须先渲染 IndexedDB 可见历史，再请求轻量 sync head')
assert.match(selectSource, /const messageItems = await[\s\S]{0,4200}messages\.value = messageItems/, '页面必须独立完成消息同步，不等待模型列表')
assert.match(chatViewSource, /hasOlderChatMessages\(messages\.value,\s*older\.length\)/, '向前分页必须把服务端空页结果写入可继续加载状态，避免保留断档无限请求')
assert.match(chatViewSource, /availability === 'not_found'[\s\S]{0,360}conversationItems\[0\][\s\S]{0,180}selectConversation\(conversationItems\[0\]\.id\)/, '待确认会话已删除时必须清理 pending 并回退首个可用会话')
assert.match(chatViewSource, /async function stopGeneration[\s\S]{0,1800}catch \(error\)[\s\S]{0,180}停止生成失败/, '停止请求失败必须提供明确中文反馈，不能产生未处理 Promise rejection')

console.log('AI 问答会话与历史加载 epoch 回归通过')
