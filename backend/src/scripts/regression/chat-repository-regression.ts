import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import {
  acceptChatTurn,
  cancelChatTurn,
  ChatConflictError,
  completeChatTurn,
  createChatConversation,
  deleteChatConversation,
  failChatTurn,
  getChatConversation,
  cleanupChatRetention,
  listChatContextMessages,
  listChatConversations,
  listChatMessages,
  updateChatConversation
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
assert.equal((await listChatConversations(client, { systemAccountId: 'sys_user_1', limit: 20 })).length, 1)
assert.equal((await listChatConversations(client, { systemAccountId: 'sys_user_2', limit: 20 })).length, 0)

const pinnedConversation = await createChatConversation(client, {
  id: 'chat_conv_pinned',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key',
  now: '2026-07-11T00:00:00.000Z'
})
const renamedPinned = await updateChatConversation(client, {
  conversationId: pinnedConversation.id,
  systemAccountId: 'sys_user_1',
  title: '置顶会话',
  isPinned: true,
  now: '2026-07-12T00:00:10.000Z'
})
assert.equal(renamedPinned?.title, '置顶会话')
assert.equal(renamedPinned?.isPinned, true)
assert.equal((await listChatConversations(client, { systemAccountId: 'sys_user_1', limit: 20 }))[0]?.id, pinnedConversation.id)
assert.equal(await deleteChatConversation(client, pinnedConversation.id, 'sys_user_1'), true)

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

const disposable = await createChatConversation(client, {
  id: 'chat_conv_delete',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key',
  now: '2026-07-12T00:08:00.000Z'
})
assert.equal(await deleteChatConversation(client, disposable.id, 'sys_user_1'), true)
assert.equal(await deleteChatConversation(client, disposable.id, 'sys_user_1'), false)

await completeChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  turnId: accepted.turnId,
  assistantContent: '你好，我是 Mock AI。',
  finishReason: 'stop',
  traceId: 'trace_chat_1',
  contentBlocks: [
    { type: 'reasoning', text: '先检索资料' },
    { type: 'tool_call', id: 'search_1', toolType: 'web_search_call', status: 'completed', item: { query: '测试' } }
  ],
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
assert.deepEqual(messages[1].contentBlocks, [
  { type: 'reasoning', text: '先检索资料' },
  { type: 'tool_call', id: 'search_1', toolType: 'web_search_call', status: 'completed', item: { query: '测试' } }
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

const failedTurn = await acceptChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_failed',
  userContent: '这轮会失败',
  model: 'mock-model',
  now: '2026-07-12T00:04:00.000Z',
  storageQuotaBytes: 1024
})
await failChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  turnId: failedTurn.turnId,
  assistantContent: '部分回答',
  errorCode: 'mock_interrupted',
  traceId: 'trace_chat_failed',
  now: '2026-07-12T00:05:00.000Z'
})
const contextAfterFailure = await listChatContextMessages(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  limitTurns: 64,
  now: '2026-07-12T00:06:00.000Z'
})
assert.equal(contextAfterFailure.length, 2, '失败轮次的一问一答都不能进入下一轮上下文')

const canceledTurn = await acceptChatTurn(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_canceled', userContent: '这轮会取消', model: 'mock-model', now: '2026-07-12T00:06:10.000Z', storageQuotaBytes: 1024
})
await cancelChatTurn(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', turnId: canceledTurn.turnId, assistantContent: '已生成的部分', traceId: 'trace_chat_canceled', now: '2026-07-12T00:06:20.000Z'
})
const contextAfterCancel = await listChatContextMessages(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', limitTurns: 64, now: '2026-07-12T00:06:30.000Z'
})
assert.equal(contextAfterCancel.length, 2, '取消轮次的用户问题与部分回答都不能进入下一轮上下文')

const stale = await createChatConversation(client, {
  id: 'chat_conv_stale', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-01T00:00:00.000Z'
})
await acceptChatTurn(client, {
  conversationId: stale.id, systemAccountId: 'sys_user_1', clientMessageId: 'stale_1', userContent: '过期问题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: 1024
})
const titleConversation = await createChatConversation(client, {
  id: 'chat_conv_title', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-01T00:00:00.000Z'
})
const oldTitleTurn = await acceptChatTurn(client, {
  conversationId: titleConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'title_old', userContent: '已经过期的旧标题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: 4096
})
await completeChatTurn(client, { conversationId: titleConversation.id, systemAccountId: 'sys_user_1', turnId: oldTitleTurn.turnId, assistantContent: '旧回答', finishReason: 'stop', traceId: 'trace_title_old', now: '2026-07-01T00:02:00.000Z' })
const newTitleTurn = await acceptChatTurn(client, {
  conversationId: titleConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'title_new', userContent: '仍在保留期的新标题', model: 'mock-model', now: '2026-07-11T00:01:00.000Z', storageQuotaBytes: 4096
})
await completeChatTurn(client, { conversationId: titleConversation.id, systemAccountId: 'sys_user_1', turnId: newTitleTurn.turnId, assistantContent: '新回答', finishReason: 'stop', traceId: 'trace_title_new', now: '2026-07-11T00:02:00.000Z' })
const cleanup = await cleanupChatRetention(client, {
  now: '2026-07-12T00:10:00.000Z', interruptedBefore: '2026-07-12T00:00:00.000Z', limit: 1000
})
assert.equal(cleanup.recoveredTurns, 1, '超时 streaming 轮次应先恢复为失败')
assert.equal(cleanup.deletedMessages, 4, '清理必须按完整轮次成对删除')
assert.equal(cleanup.deletedConversations, 1, '没有保留消息的会话应删除')
assert.equal((await getChatConversation(client, titleConversation.id, 'sys_user_1'))?.title, '仍在保留期的新标题', '标题来源过期后应使用最早保留用户消息重算')

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_user_1',
    clientMessageId: 'client_quota',
    userContent: '超限',
    model: 'mock-model',
    now: '2026-07-12T00:07:00.000Z',
    storageQuotaBytes: 1
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_storage_quota_exceeded'
)

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
