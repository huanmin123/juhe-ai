import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { initializeAcceptedChatTurn } from '../../modules/chat/chat-turn-initialization.js'
import { completeChatAssetProcessing, createChatAsset, getChatAsset } from '../../storage/chat-assets.repository.js'
import {
  acceptChatTurn,
  cancelChatTurn,
  ChatConflictError,
  completeChatTurn,
  createChatConversation,
  deleteChatConversation,
  failChatTurn,
  findChatTurnByClientMessageId,
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
const pinnedPage = await listChatConversations(client, { systemAccountId: 'sys_user_1', limit: 1 })
const unpinnedPage = await listChatConversations(client, {
  systemAccountId: 'sys_user_1',
  beforeIsPinned: pinnedPage[0]?.isPinned,
  beforeLastMessageAt: pinnedPage[0]?.lastMessageAt,
  beforeId: pinnedPage[0]?.id,
  limit: 1
})
assert.equal(unpinnedPage[0]?.id, conversation.id, '置顶游标之后必须继续返回时间更新的非置顶会话')
assert.equal(await deleteChatConversation(client, pinnedConversation.id, 'sys_user_1'), true)

assert.equal(await findChatTurnByClientMessageId(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1'
}), undefined)
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
assert.deepEqual(await findChatTurnByClientMessageId(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1'
}), { turnId: accepted.turnId })
assert.equal(await findChatTurnByClientMessageId(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_2',
  clientMessageId: 'client_1'
}), undefined, '只读幂等查询不得跨系统账户命中')

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

const replaceAccountId = 'sys_replace'
const replaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace',
  systemAccountId: replaceAccountId,
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key',
  now: '2026-07-13T00:00:00.000Z'
})
const replaceOriginal = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_original',
  userContent: '旧问题',
  contentBlocks: [{ type: 'input_text', text: '不会写入结构化标记' }],
  model: 'mock-model',
  now: '2026-07-13T00:01:00.000Z',
  storageQuotaBytes: 4096
})
await completeChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  turnId: replaceOriginal.turnId,
  assistantContent: '这是需要被原子移除的旧回答。'.repeat(12),
  finishReason: 'stop',
  traceId: 'trace_replace_original',
  now: '2026-07-13T00:02:00.000Z'
})
const oldStorageBytes = Number((database.prepare(`
  SELECT content_bytes FROM chat_user_storage_windows
  WHERE system_account_id = ? AND bucket_date = ?
`).get(replaceAccountId, '2026-07-13') as { content_bytes?: unknown })?.content_bytes ?? 0)
const replacementContent = '修正后的问题'
const replacementMarkerJson = JSON.stringify([
  { type: 'input_text', text: replacementContent, order: 0 }
])
const replacementUserBytes = Buffer.byteLength(replacementContent, 'utf8') + Buffer.byteLength(replacementMarkerJson, 'utf8')
assert(oldStorageBytes > replacementUserBytes, '测试前提：旧问答占用必须大于替换后的新问题')
const duplicateWithOldClientId = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_original',
  userContent: '复用旧 clientMessageId 不得触发替换',
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-07-13T00:59:00.000Z',
  storageQuotaBytes: 1,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(duplicateWithOldClientId.duplicate, true)
assert.equal(duplicateWithOldClientId.turnId, replaceOriginal.turnId)
const replacement = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_new',
  userContent: replacementContent,
  contentBlocks: [{ type: 'input_text', text: replacementContent }],
  model: 'mock-model',
  now: '2026-07-13T01:00:00.000Z',
  storageQuotaBytes: replacementUserBytes,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(replacement.userMessage.sequenceNo, replaceOriginal.userMessage.sequenceNo)
assert.equal(replacement.assistantMessage.sequenceNo, replaceOriginal.assistantMessage.sequenceNo)
assert.deepEqual(replacement.userMessage.contentBlocks, [
  { type: 'input_text', text: replacementContent, order: 0 }
])
const replacementReplay = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_new',
  userContent: replacementContent,
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-07-13T01:00:00.500Z',
  storageQuotaBytes: 1,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(replacementReplay.duplicate, true)
assert.equal(replacementReplay.turnId, replacement.turnId)
assert.equal((await listChatMessages(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  limit: 20,
  now: '2026-07-13T01:00:01.000Z'
})).some((message) => message.turnId === replaceOriginal.turnId), false)
assert.equal(await findChatTurnByClientMessageId(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_original'
}), undefined, '替换必须删除旧 clientMessageId 幂等登记')
assert.equal((database.prepare('SELECT next_sequence_no FROM chat_conversations WHERE id = ?').get(replaceConversation.id) as { next_sequence_no?: unknown })?.next_sequence_no, 3)
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.title, replacementContent, '标题来源被替换时必须按新内容重算')
assert.equal(Number((database.prepare(`
  SELECT content_bytes FROM chat_user_storage_windows
  WHERE system_account_id = ? AND bucket_date = ?
`).get(replaceAccountId, '2026-07-13') as { content_bytes?: unknown })?.content_bytes ?? 0), replacementUserBytes, '容量窗口必须先扣除旧问答再加入新用户消息')

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: replaceConversation.id,
    systemAccountId: replaceAccountId,
    clientMessageId: 'client_replace_while_active',
    userContent: '活动会话不得替换',
    contentBlocks: [{ type: 'input_text' }],
    model: 'mock-model',
    now: '2026-07-13T01:00:02.000Z',
    storageQuotaBytes: 4096,
    replaceTurnId: replacement.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict',
  '存在 active turn 时替换必须返回专用冲突码'
)
await completeChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  turnId: replacement.turnId,
  assistantContent: '新回答',
  finishReason: 'stop',
  traceId: 'trace_replace_new',
  now: '2026-07-13T01:01:00.000Z'
})
const replaceLatest = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_latest',
  userContent: '第二轮',
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-07-13T01:02:00.000Z',
  storageQuotaBytes: 4096
})
await completeChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  turnId: replaceLatest.turnId,
  assistantContent: '第二轮回答',
  finishReason: 'stop',
  traceId: 'trace_replace_latest',
  now: '2026-07-13T01:03:00.000Z'
})
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: replaceConversation.id,
    systemAccountId: replaceAccountId,
    clientMessageId: 'client_replace_not_latest',
    userContent: '不能修改旧轮次',
    contentBlocks: [{ type: 'input_text' }],
    model: 'mock-model',
    now: '2026-07-13T01:04:00.000Z',
    storageQuotaBytes: 4096,
    replaceTurnId: replacement.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict'
)

const replacementStateBeforeQuotaFailure = {
  messages: await listChatMessages(client, { conversationId: replaceConversation.id, systemAccountId: replaceAccountId, limit: 20, now: '2026-07-13T01:05:00.000Z' }),
  storageBytes: Number((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(replaceAccountId) as { total?: unknown })?.total ?? 0),
  conversation: database.prepare('SELECT title, title_source_message_id, next_sequence_no, active_turn_id FROM chat_conversations WHERE id = ?').get(replaceConversation.id)
}
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: replaceConversation.id, systemAccountId: replaceAccountId, clientMessageId: 'client_replace_quota_failure', userContent: '容量不足时必须完整回滚', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T01:05:01.000Z', storageQuotaBytes: 1, replaceTurnId: replaceLatest.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_storage_quota_exceeded'
)
assert.deepEqual(await listChatMessages(client, { conversationId: replaceConversation.id, systemAccountId: replaceAccountId, limit: 20, now: '2026-07-13T01:05:02.000Z' }), replacementStateBeforeQuotaFailure.messages, '替换容量失败不得删除旧消息')
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(replaceAccountId) as { total?: unknown })?.total ?? 0), replacementStateBeforeQuotaFailure.storageBytes, '替换容量失败不得改变容量窗口')
assert.deepEqual(database.prepare('SELECT title, title_source_message_id, next_sequence_no, active_turn_id FROM chat_conversations WHERE id = ?').get(replaceConversation.id), replacementStateBeforeQuotaFailure.conversation, '替换容量失败不得改变会话状态')

const failedReplaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_failed', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-13T02:00:00.000Z'
})
const failedReplaceTurn = await acceptChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_failed', userContent: '失败轮次', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T02:01:00.000Z', storageQuotaBytes: 4096
})
await failChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: failedReplaceTurn.turnId, assistantContent: '失败回答', errorCode: 'mock_failed', now: '2026-07-13T02:02:00.000Z'
})
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_failed_new', userContent: '不能替换失败轮次', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T02:03:00.000Z', storageQuotaBytes: 4096, replaceTurnId: failedReplaceTurn.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict'
)

const malformedMarkerConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_malformed_marker', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-13T03:10:00.000Z'
})
const malformedMarkerTurn = await acceptChatTurn(client, {
  conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_malformed_marker', userContent: '标记损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:11:00.000Z', storageQuotaBytes: 4096
})
await completeChatTurn(client, { conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', turnId: malformedMarkerTurn.turnId, assistantContent: '回答', finishReason: 'stop', traceId: 'trace_malformed_marker', now: '2026-07-13T03:12:00.000Z' })
database.prepare("UPDATE chat_messages SET content_blocks_json = '{malformed' WHERE id = ?").run(malformedMarkerTurn.userMessage.id)
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_malformed_marker_new', userContent: '损坏标记不得降级成纯文本', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:13:00.000Z', storageQuotaBytes: 4096, replaceTurnId: malformedMarkerTurn.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict',
  '未知或畸形输入标记必须 fail closed'
)

const missingWindowConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_missing_window', systemAccountId: 'sys_missing_window', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-13T03:20:00.000Z'
})
const missingWindowTurn = await acceptChatTurn(client, {
  conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', clientMessageId: 'client_replace_missing_window', userContent: '容量窗口损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:21:00.000Z', storageQuotaBytes: 4096
})
await completeChatTurn(client, { conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', turnId: missingWindowTurn.turnId, assistantContent: '回答', finishReason: 'stop', traceId: 'trace_missing_window', now: '2026-07-13T03:22:00.000Z' })
database.prepare('DELETE FROM chat_user_storage_windows WHERE system_account_id = ?').run('sys_missing_window')
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', clientMessageId: 'client_replace_missing_window_new', userContent: '不得掩盖容量窗口损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:23:00.000Z', storageQuotaBytes: 4096, replaceTurnId: missingWindowTurn.turnId
  }),
  /容量窗口数据不一致/
)
assert.equal((await listChatMessages(client, { conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', limit: 20, now: '2026-07-13T03:24:00.000Z' }))[0]?.turnId, missingWindowTurn.turnId, '容量窗口损坏时旧轮次必须保留')

const crossDayAccountId = 'sys_replace_cross_day'
const crossDayConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_cross_day', systemAccountId: crossDayAccountId, apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-13T23:50:00.000Z'
})
const crossDayOriginal = await acceptChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, clientMessageId: 'client_replace_cross_day_old', userContent: '前一天的问题', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T23:58:00.000Z', storageQuotaBytes: 4096
})
await completeChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, turnId: crossDayOriginal.turnId, assistantContent: '跨过午夜完成的旧回答', finishReason: 'stop', traceId: 'trace_replace_cross_day_old', now: '2026-07-14T00:01:00.000Z'
})
assert(Number((database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-13') as { content_bytes?: unknown })?.content_bytes ?? 0) > 0, '旧轮次应计入创建时所在日桶')
const crossDayNewContent = '次日修正的问题'
const crossDayMarkerJson = JSON.stringify([{ type: 'input_text', text: '', order: 0 }])
const crossDayNewUserBytes = Buffer.byteLength(crossDayNewContent, 'utf8') + Buffer.byteLength(crossDayMarkerJson, 'utf8')
await acceptChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, clientMessageId: 'client_replace_cross_day_new', userContent: crossDayNewContent, contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-14T00:02:00.000Z', storageQuotaBytes: crossDayNewUserBytes, replaceTurnId: crossDayOriginal.turnId
})
assert.equal(database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-13'), undefined, '跨日替换必须准确扣除并删除旧日空桶')
assert.equal(Number((database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-14') as { content_bytes?: unknown })?.content_bytes ?? 0), crossDayNewUserBytes, '跨日替换的新日桶只能增加新用户消息字节')
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(crossDayAccountId) as { total?: unknown })?.total ?? 0), crossDayNewUserBytes, '跨日替换后的总额必须等于净额')

const imageReplaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_image', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', now: '2026-07-13T03:00:00.000Z'
})
const imageAssetId = 'chat_asset_11111111111111111111111111111111'
await createChatAsset(client, {
  id: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  originalFilename: 'repository-test.png',
  originalMimeType: 'image/png',
  originalWidth: 1,
  originalHeight: 1,
  originalBytes: 68,
  originalSha256: '1'.repeat(64),
  now: '2026-07-13T03:00:10.000Z'
})
await completeChatAssetProcessing(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  processedMimeType: 'image/png',
  processedWidth: 1,
  processedHeight: 1,
  processedBytes: 68,
  processedSha256: '2'.repeat(64),
  storageKey: 'repository-tests/ready-image.png',
  now: '2026-07-13T03:00:20.000Z'
})
const imageReplaceTurn = await acceptChatTurn(client, {
  conversationId: imageReplaceConversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_replace_image',
  userContent: '文字 图片 文字',
  contentBlocks: [
    { type: 'input_text', text: '前文' },
    { type: 'input_image', assetId: imageAssetId },
    { type: 'input_text', text: '后文' }
  ],
  model: 'mock-model',
  now: '2026-07-13T03:01:00.000Z',
  storageQuotaBytes: 4096
})
await completeChatTurn(client, {
  conversationId: imageReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: imageReplaceTurn.turnId, assistantContent: '图片回答', finishReason: 'stop', traceId: 'trace_replace_image', now: '2026-07-13T03:02:00.000Z'
})
const imageMessages = await listChatMessages(client, {
  conversationId: imageReplaceConversation.id, systemAccountId: 'sys_user_1', limit: 20, now: '2026-07-13T03:03:00.000Z'
})
assert.deepEqual(imageMessages[0]?.contentBlocks, [
  { type: 'input_text', text: '前文', order: 0 },
  { type: 'input_image', assetId: imageAssetId, order: 1 },
  { type: 'input_text', text: '后文', order: 2 }
])
const committedImageAsset = await getChatAsset(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id
})
assert.deepEqual(
  [committedImageAsset?.turnId, committedImageAsset?.messageId],
  [imageReplaceTurn.turnId, imageReplaceTurn.userMessage.id],
  '图片资产必须在接受轮次的同一事务中绑定用户消息'
)
const replacedImageTurn = await acceptChatTurn(client, {
  conversationId: imageReplaceConversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_replace_image_new',
  userContent: '修正后的文字 图片 文字',
  contentBlocks: [
    { type: 'input_text', text: '修正前文' },
    { type: 'input_image', assetId: imageAssetId },
    { type: 'input_text', text: '修正后文' }
  ],
  model: 'mock-model',
  now: '2026-07-13T03:04:00.000Z',
  storageQuotaBytes: 4096,
  replaceTurnId: imageReplaceTurn.turnId
})
assert.notEqual(replacedImageTurn.turnId, imageReplaceTurn.turnId)
const reboundImageAsset = await getChatAsset(client, { assetId: imageAssetId, systemAccountId: 'sys_user_1', conversationId: imageReplaceConversation.id })
assert.deepEqual([reboundImageAsset?.turnId, reboundImageAsset?.messageId], [replacedImageTurn.turnId, replacedImageTurn.userMessage.id], '含图片的最近一轮重新生成时必须把原资产原子改绑到新轮次')
await completeChatTurn(client, {
  conversationId: imageReplaceConversation.id,
  systemAccountId: 'sys_user_1',
  turnId: replacedImageTurn.turnId,
  assistantContent: '修正后的图片回答',
  finishReason: 'stop',
  traceId: 'trace_replace_image_new',
  now: '2026-07-13T03:05:00.000Z'
})

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: replaceConversation.id, systemAccountId: replaceAccountId, clientMessageId: 'client_replace_other_conversation', userContent: '跨会话替换', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T04:00:00.000Z', storageQuotaBytes: 4096, replaceTurnId: imageReplaceTurn.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict'
)

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

const initializationConversation = await createChatConversation(client, {
  id: 'chat_conv_initialization_failure',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key',
  now: '2026-07-12T01:00:00.000Z'
})
const initializationTurn = await acceptChatTurn(client, {
  conversationId: initializationConversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_initialization_failure',
  userContent: '触发初始化失败',
  model: 'mock-model',
  now: '2026-07-12T01:00:01.000Z',
  storageQuotaBytes: 4096
})
let upstreamRequests = 0
await assert.rejects((async () => {
  await initializeAcceptedChatTurn({
    initialize: async () => { throw new Error('受控历史读取失败') },
    failAcceptedTurn: async () => {
      await failChatTurn(client, {
        conversationId: initializationConversation.id,
        systemAccountId: 'sys_user_1',
        turnId: initializationTurn.turnId,
        assistantContent: '',
        errorCode: 'chat_initialization_failed',
        traceId: 'trace_initialization_failure',
        now: '2026-07-12T01:00:02.000Z'
      })
    }
  })
  upstreamRequests += 1
})(), /受控历史读取失败/)
assert.equal(upstreamRequests, 0, '接受轮次后的初始化失败不得请求上游')
assert.equal((await getChatConversation(client, initializationConversation.id, 'sys_user_1'))?.activeTurnId, undefined, '初始化失败必须清除 active_turn_id')
const initializationMessages = await listChatMessages(client, {
  conversationId: initializationConversation.id,
  systemAccountId: 'sys_user_1',
  limit: 10,
  now: '2026-07-12T01:00:03.000Z'
})
assert.deepEqual(initializationMessages.map((message) => [message.role, message.status, message.errorCode]), [
  ['user', 'completed', undefined],
  ['assistant', 'failed', 'chat_initialization_failed']
])

const initializationError = new Error('初始化读取失败')
const finalizationError = new Error('终结写入失败')
await assert.rejects(
  initializeAcceptedChatTurn({
    initialize: async () => { throw initializationError },
    failAcceptedTurn: async () => { throw finalizationError }
  }),
  (error) => error instanceof AggregateError
    && error.message === '聊天轮次初始化失败，且终结失败'
    && error.errors.length === 2
    && error.errors[0] === initializationError
    && error.errors[1] === finalizationError,
  '初始化与终结同时失败时必须保留两个原始错误供外层日志记录'
)

console.log('AI 问答 repository 回归通过')
