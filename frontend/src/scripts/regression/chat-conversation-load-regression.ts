import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { isCurrentChatConversationLoad } from '../../views/chat/chatConversationLoad'

assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 2, currentEpoch: 2, disposed: false }), true)
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_a', selectedConversationId: 'conv_b', epoch: 1, currentEpoch: 2, disposed: false }), false, '旧会话响应不能覆盖新选择')
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 1, currentEpoch: 2, disposed: false }), false, '同会话旧 epoch 也不能覆盖较新加载')
assert.equal(isCurrentChatConversationLoad({ conversationId: 'conv_b', selectedConversationId: 'conv_b', epoch: 2, currentEpoch: 2, disposed: true }), false, '卸载后不能写入页面状态')

const chatViewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const chatApiSource = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
assert.match(chatApiSource, /beforeIsPinned\?:\s*boolean/, '会话 API 游标必须包含置顶维度')
assert.match(chatViewSource, /loadMoreConversations/, '会话列表必须允许访问第 51 个及更早会话')
assert.match(chatViewSource, /beforeIsPinned:\s*last\.isPinned/, '加载更多必须传递完整置顶游标')

console.log('AI 问答会话与历史加载 epoch 回归通过')
