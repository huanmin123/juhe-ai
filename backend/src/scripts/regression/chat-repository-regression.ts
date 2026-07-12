import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import {
  acceptChatTurn,
  ChatConflictError,
  completeChatTurn,
  createChatConversation,
  listChatContextMessages,
  listChatMessages
} from '../../storage/chat.repository.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)

const conversation = await createChatConversation(client, {
  id: 'chat_conv_1',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key',
  now: '2026-07-12T00:00:00.000Z'
})
assert.equal(conversation.title, '新对话')

const accepted = await acceptChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1',
  userContent: '你好',
  model: 'mock-model',
  now: '2026-07-12T00:01:00.000Z',
  storageQuotaBytes: 1024
})
assert.equal(accepted.userMessage.sequenceNo, 1)
assert.equal(accepted.assistantMessage.sequenceNo, 2)
assert.equal(accepted.duplicate, false)

const duplicate = await acceptChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1',
  userContent: '你好',
  model: 'mock-model',
  now: '2026-07-12T00:01:01.000Z',
  storageQuotaBytes: 1024
})
assert.equal(duplicate.duplicate, true)
assert.equal(duplicate.turnId, accepted.turnId)

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_user_1',
    clientMessageId: 'client_2',
    userContent: '并发问题',
    model: 'mock-model',
    now: '2026-07-12T00:01:02.000Z',
    storageQuotaBytes: 1024
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_message_in_progress'
)

await completeChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  turnId: accepted.turnId,
  assistantContent: '你好，我是 Mock AI。',
  finishReason: 'stop',
  traceId: 'trace_chat_1',
  now: '2026-07-12T00:02:00.000Z'
})

const messages = await listChatMessages(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  limit: 20,
  now: '2026-07-12T00:03:00.000Z'
})
assert.deepEqual(messages.map((message) => [message.role, message.status, message.contentText]), [
  ['user', 'completed', '你好'],
  ['assistant', 'completed', '你好，我是 Mock AI。']
])

const context = await listChatContextMessages(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  limitTurns: 64,
  now: '2026-07-12T00:03:00.000Z'
})
assert.deepEqual(context.map((message) => [message.role, message.content]), [
  ['user', '你好'],
  ['assistant', '你好，我是 Mock AI。']
])

await assert.rejects(
  listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_user_2',
    limit: 20,
    now: '2026-07-12T00:03:00.000Z'
  }),
  /会话不存在/
)

console.log('AI 问答 repository 回归通过')
