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
assert.match(chatViewSource, /loadMoreConversations/, '会话列表必须允许访问第 51 个及更早会话')
assert.match(chatViewSource, /beforeIsPinned:\s*last\.isPinned/, '加载更多必须传递完整置顶游标')
const loadStartIndex = chatViewSource.indexOf('const conversationLoad = startChatConversationLoad({')
const syncIndex = chatViewSource.indexOf('synchronizeChatConversation({', loadStartIndex)
const modelIndex = chatViewSource.indexOf('loadModels: () => (options.refreshModels', loadStartIndex)
assert.ok(loadStartIndex >= 0 && syncIndex > loadStartIndex && modelIndex > syncIndex, '页面必须在同一独立加载通道中启动 cache-first 消息同步与模型旁路')
const modelLoadSource = chatViewSource.slice(loadStartIndex - 500, modelIndex + 700)
assert.match(modelLoadSource, /cachedModels\s*=\s*modelLoadCoordinator\.peek/, '切换会话必须先读取已有模型快照')
assert.match(modelLoadSource, /options\.refreshModels[\s\S]*modelLoadCoordinator\.refresh[\s\S]*Promise\.resolve\(cachedModels\)[\s\S]*modelLoadCoordinator\.load/, '模型旁路必须支持新建强制刷新、已有快照复用和首次缺失加载')
assert.match(chatViewSource, /readConversation[\s\S]{0,900}messages\.value = cached\.value\.messages[\s\S]{0,1200}synchronizeChatConversation/, '页面必须先渲染 IndexedDB 可见历史，再请求轻量 sync head')
assert.match(chatViewSource, /await conversationLoad\.messages[\s\S]{0,500}messages\.value = messageItems[\s\S]{0,900}conversationLoad\.models\.then/, '页面必须先选择并渲染消息，再异步应用模型列表结果')
assert.match(chatViewSource, /hasOlderChatMessages\(messages\.value,\s*older\.length\)/, '向前分页必须把服务端空页结果写入可继续加载状态，避免保留断档无限请求')

console.log('AI 问答会话与历史加载 epoch 回归通过')
