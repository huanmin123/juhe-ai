import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { listActiveChatObservationTasks, trackActiveChatObservation } from '../../modules/chat/chat-active-observations.js'
import { applyChatSchema } from '../../storage/schema.js'
import { initializeAcceptedChatTurn } from '../../modules/chat/chat-turn-initialization.js'
import { claimChatAssetObservation, completeChatAssetProcessing, createChatAsset, getChatAsset, setChatAssetObservation } from '../../storage/chat-assets.repository.js'
import {
  acceptChatTurn,
  ChatAssistantStorageLimitError,
  chatAssistantStorageReservationBytes,
  cancelActiveChatTurnIfMatches,
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
const testStorageQuotaBytes = 64 * 1024 * 1024
for (const [rawCount, expected] of [['0', 0], ['50', 50]] as const) {
  assert.equal((await getChatConversation(chatConversationRowClient(rawCount), 'turn_count', 'turn_owner'))?.userTurnCount, expected, '合法 PostgreSQL bigint 字符串必须规范化为 number')
}
for (const rawCount of [null, undefined, '', '1.5', '-1', '9007199254740992', true, {}]) {
  await assert.rejects(getChatConversation(chatConversationRowClient(rawCount), 'turn_count', 'turn_owner'), /聊天会话轮次计数无效/, `无效用户轮次计数必须 fail-fast：${String(rawCount)}`)
}

function chatConversationRowClient(userTurnCount: unknown): DatabaseClient {
  return {
    ...client,
    one: async () => ({
      id: 'turn_count', system_account_id: 'turn_owner', api_key_id: 'key', api_key_name_snapshot: 'Key',
      title: '计数测试', is_pinned: 0, last_model: null, active_turn_id: null, user_turn_count: userTurnCount, message_revision: 0,
      last_message_at: '2026-07-10T00:00:00.000Z', created_at: '2026-07-10T00:00:00.000Z', updated_at: '2026-07-10T00:00:00.000Z'
    })
  } as unknown as DatabaseClient
}

const conversationLimitAccountId = 'sys_conversation_limit'
const limitedConversations = []
for (let index = 0; index < 50; index += 1) {
  limitedConversations.push(await createChatConversation(client, {
    id: `chat_conv_limit_${String(index).padStart(2, '0')}`,
    systemAccountId: conversationLimitAccountId,
    apiKeyId: 'key_limit',
    apiKeyNameSnapshot: '会话上限 Key' ,
    now: `2026-07-10T00:${String(index).padStart(2, '0')}:00.000Z`,
    maxConversationsPerUser: 50
  }))
}
await assert.rejects(createChatConversation(client, {
  id: 'chat_conv_limit_50',
  systemAccountId: conversationLimitAccountId,
  apiKeyId: 'key_limit',
  apiKeyNameSnapshot: '会话上限 Key' ,
  now: '2026-07-10T01:00:00.000Z',
  maxConversationsPerUser: 50
}), (error) => error instanceof ChatConflictError && error.code === 'chat_conversation_limit_exceeded', '第 51 个会话必须被后端权威拒绝')
assert.equal(await deleteChatConversation(client, limitedConversations[0].id, conversationLimitAccountId), true)
await createChatConversation(client, {
  id: 'chat_conv_limit_recreated',
  systemAccountId: conversationLimitAccountId,
  apiKeyId: 'key_limit',
  apiKeyNameSnapshot: '会话上限 Key' ,
  now: '2026-07-10T01:01:00.000Z',
  maxConversationsPerUser: 50
})
assert.equal((database.prepare('SELECT COUNT(*) AS total FROM chat_conversations WHERE system_account_id = ?').get(conversationLimitAccountId) as { total?: unknown }).total, 50, '删除一个会话后应能恢复创建但总数仍不超过上限')
for (const conversation of limitedConversations.slice(1)) await deleteChatConversation(client, conversation.id, conversationLimitAccountId)
await deleteChatConversation(client, 'chat_conv_limit_recreated', conversationLimitAccountId)

const concurrentConversationOwnerId = 'sys_conversation_limit_concurrent'
for (let index = 0; index < 49; index += 1) {
  await createChatConversation(client, {
    id: `chat_conv_concurrent_seed_${String(index).padStart(2, '0')}`,
    systemAccountId: concurrentConversationOwnerId, apiKeyId: 'key_limit', apiKeyNameSnapshot: '并发会话上限 Key' ,
    now: '2026-07-10T01:30:00.000Z', maxConversationsPerUser: 50
  })
}
const concurrentCreates = await Promise.allSettled([
  createChatConversation(client, { id: 'chat_conv_concurrent_a', systemAccountId: concurrentConversationOwnerId, apiKeyId: 'key_limit', apiKeyNameSnapshot: '并发会话上限 Key' , now: '2026-07-10T01:31:00.000Z', maxConversationsPerUser: 50 }),
  createChatConversation(client, { id: 'chat_conv_concurrent_b', systemAccountId: concurrentConversationOwnerId, apiKeyId: 'key_limit', apiKeyNameSnapshot: '并发会话上限 Key' , now: '2026-07-10T01:31:00.000Z', maxConversationsPerUser: 50 })
])
assert.equal(concurrentCreates.filter((result) => result.status === 'fulfilled').length, 1, '49 个会话后两个并发创建必须恰好成功一个')
const concurrentRejected = concurrentCreates.find((result): result is PromiseRejectedResult => result.status === 'rejected')
assert(concurrentRejected?.reason instanceof ChatConflictError && concurrentRejected.reason.code === 'chat_conversation_limit_exceeded', '另一个并发创建必须返回稳定会话上限冲突')
assert.equal((database.prepare('SELECT COUNT(*) AS total FROM chat_conversations WHERE system_account_id = ?').get(concurrentConversationOwnerId) as { total?: unknown }).total, 50, '并发创建后最终会话数必须严格为 50')
for (let index = 0; index < 49; index += 1) await deleteChatConversation(client, `chat_conv_concurrent_seed_${String(index).padStart(2, '0')}`, concurrentConversationOwnerId)
await deleteChatConversation(client, 'chat_conv_concurrent_a', concurrentConversationOwnerId)
await deleteChatConversation(client, 'chat_conv_concurrent_b', concurrentConversationOwnerId)

const turnLimitAccountId = 'sys_turn_limit'
const turnLimitConversation = await createChatConversation(client, {
  id: 'chat_conv_turn_limit', systemAccountId: turnLimitAccountId, apiKeyId: 'key_limit',
  apiKeyNameSnapshot: '轮次上限 Key' , now: '2026-07-10T02:00:00.000Z', maxConversationsPerUser: 50
})
const firstLimitedTurn = await acceptChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'turn_limit_1', userContent: '第一轮', model: 'mock-model',
  now: '2026-07-10T02:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 2
})
await completeChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId, turnId: firstLimitedTurn.turnId,
  assistantContent: '第一轮回答', finishReason: 'stop', traceId: 'trace_turn_limit_1', now: '2026-07-10T02:01:30.000Z'
})
const replacedLimitedTurn = await acceptChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'turn_limit_1_replace', userContent: '替换第一轮', model: 'mock-model',
  now: '2026-07-10T02:02:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 2, replaceTurnId: firstLimitedTurn.turnId
})
await completeChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId, turnId: replacedLimitedTurn.turnId,
  assistantContent: '替换回答', finishReason: 'stop', traceId: 'trace_turn_limit_replace', now: '2026-07-10T02:02:30.000Z'
})
assert.equal((await getChatConversation(client, turnLimitConversation.id, turnLimitAccountId))?.userTurnCount, 1, 'replace 不得增加用户轮次计数')
const failedLimitedTurn = await acceptChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'turn_limit_2', userContent: '第二轮失败', model: 'mock-model',
  now: '2026-07-10T02:03:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 2
})
await failChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId, turnId: failedLimitedTurn.turnId,
  assistantContent: '', errorCode: 'mock_failed', traceId: 'trace_turn_limit_2', now: '2026-07-10T02:03:30.000Z'
})
assert.equal((await getChatConversation(client, turnLimitConversation.id, turnLimitAccountId))?.userTurnCount, 2, '已接受后失败的轮次仍必须计数')
const duplicateLimitedTurn = await acceptChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'turn_limit_2', userContent: '第二轮失败', model: 'mock-model',
  now: '2026-07-10T02:04:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 2
})
assert.equal(duplicateLimitedTurn.duplicate, true, '达到轮次上限后相同 clientMessageId 仍必须允许幂等重放')
const beforeTurnLimitReject = database.prepare(`
  SELECT
    (SELECT COUNT(*) FROM chat_messages WHERE conversation_id = ?) AS messages,
    (SELECT COUNT(*) FROM chat_message_idempotency WHERE conversation_id = ?) AS idempotency,
    (SELECT COUNT(*) FROM chat_assets WHERE conversation_id = ?) AS assets,
    (SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0) FROM chat_user_storage_windows WHERE system_account_id = ?) AS storage
`).get(turnLimitConversation.id, turnLimitConversation.id, turnLimitConversation.id, turnLimitAccountId) as Record<string, unknown>
await assert.rejects(acceptChatTurn(client, {
  conversationId: turnLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'turn_limit_3', userContent: '第三轮', model: 'mock-model',
  now: '2026-07-10T02:05:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 2
}), (error) => error instanceof ChatConflictError && error.code === 'chat_turn_limit_exceeded', '第 3 个普通轮次必须在任何持久化副作用前拒绝')
const afterTurnLimitReject = database.prepare(`
  SELECT
    (SELECT COUNT(*) FROM chat_messages WHERE conversation_id = ?) AS messages,
    (SELECT COUNT(*) FROM chat_message_idempotency WHERE conversation_id = ?) AS idempotency,
    (SELECT COUNT(*) FROM chat_assets WHERE conversation_id = ?) AS assets,
    (SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0) FROM chat_user_storage_windows WHERE system_account_id = ?) AS storage
`).get(turnLimitConversation.id, turnLimitConversation.id, turnLimitConversation.id, turnLimitAccountId) as Record<string, unknown>
assert.deepEqual(afterTurnLimitReject, beforeTurnLimitReject, '轮次上限拒绝不得写入消息、幂等、资产或容量占用')
assert.equal((await getChatConversation(client, turnLimitConversation.id, turnLimitAccountId))?.activeTurnId, undefined, '轮次上限拒绝不得留下 active turn')

const canceledLimitConversation = await createChatConversation(client, {
  id: 'chat_conv_canceled_turn_limit', systemAccountId: turnLimitAccountId, apiKeyId: 'key_limit',
  apiKeyNameSnapshot: '取消轮次上限 Key' , now: '2026-07-10T03:00:00.000Z', maxConversationsPerUser: 50
})
const canceledLimitedTurn = await acceptChatTurn(client, {
  conversationId: canceledLimitConversation.id, systemAccountId: turnLimitAccountId,
  clientMessageId: 'canceled_limit_1', userContent: '会被取消', model: 'mock-model',
  now: '2026-07-10T03:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes ,
  retentionDays: 3, maxTurnsPerConversation: 1
})
await cancelChatTurn(client, {
  conversationId: canceledLimitConversation.id, systemAccountId: turnLimitAccountId,
  turnId: canceledLimitedTurn.turnId, assistantContent: '', traceId: 'trace_canceled_limit', now: '2026-07-10T03:01:30.000Z'
})
assert.equal((await getChatConversation(client, canceledLimitConversation.id, turnLimitAccountId))?.userTurnCount, 1, '已接受后取消的轮次仍必须计数')
await deleteChatConversation(client, turnLimitConversation.id, turnLimitAccountId)
await deleteChatConversation(client, canceledLimitConversation.id, turnLimitAccountId)

const quotaReservationAccountId = 'sys_quota_reservation'
const quotaReservationConversation = await createChatConversation(client, {
  id: 'chat_conv_quota_reservation',
  systemAccountId: quotaReservationAccountId,
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
  now: '2026-07-12T00:00:00.000Z'
})
const quotaReservationUserContent = 'Q'
const quotaReservationUserBlocks = [{ type: 'input_text' as const }]
const quotaReservationUserBytes = Buffer.byteLength(quotaReservationUserContent, 'utf8')
  + Buffer.byteLength(JSON.stringify([{ type: 'input_text', text: '', order: 0 }]), 'utf8')
const quotaReservationAssistantBytes = Buffer.byteLength('A', 'utf8') + Buffer.byteLength('[]', 'utf8')
const quotaReservationBytes = quotaReservationUserBytes + quotaReservationAssistantBytes - 1
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: quotaReservationConversation.id,
    systemAccountId: quotaReservationAccountId,
    clientMessageId: 'client_quota_reservation',
    userContent: quotaReservationUserContent,
    contentBlocks: quotaReservationUserBlocks,
    model: 'mock-model',
    now: '2026-07-12T00:00:01.000Z',
    storageQuotaBytes: quotaReservationBytes, retentionDays: 7, maxTurnsPerConversation: 1000
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_storage_quota_exceeded',
  '接受用户消息时必须为助手回答预留可证明的存储上界，不能等 finalize 突破硬配额'
)
assert.equal((database.prepare('SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ?').get(quotaReservationConversation.id) as { total?: unknown }).total, 0, '配额拒绝不得留下部分轮次')
assert.equal((database.prepare('SELECT COUNT(*) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(quotaReservationAccountId) as { total?: unknown }).total, 0, '配额拒绝不得留下容量窗口占用')
assert.equal(chatAssistantStorageReservationBytes, 448 * 1024, '助手回答预留上界必须固定为 448 KiB')

const quotaSettlementConversation = await createChatConversation(client, {
  id: 'chat_conv_quota_settlement',
  systemAccountId: quotaReservationAccountId,
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
  now: '2026-07-12T00:01:00.000Z'
})
const quotaSettlementTurn = await acceptChatTurn(client, {
  conversationId: quotaSettlementConversation.id,
  systemAccountId: quotaReservationAccountId,
  clientMessageId: 'client_quota_settlement',
  userContent: quotaReservationUserContent,
  contentBlocks: quotaReservationUserBlocks,
  model: 'mock-model',
  now: '2026-07-12T00:01:01.000Z',
  storageQuotaBytes: quotaReservationUserBytes + chatAssistantStorageReservationBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.equal((database.prepare('SELECT storage_reserved_bytes FROM chat_messages WHERE id = ?').get(quotaSettlementTurn.assistantMessage.id) as { storage_reserved_bytes?: unknown }).storage_reserved_bytes, chatAssistantStorageReservationBytes, 'streaming 助手消息必须记录本轮预留')
const acceptedQuotaWindow = database.prepare('SELECT content_bytes, reserved_bytes FROM chat_user_storage_windows WHERE system_account_id = ?').get(quotaReservationAccountId) as { content_bytes?: unknown; reserved_bytes?: unknown }
assert.deepEqual([Number(acceptedQuotaWindow.content_bytes), Number(acceptedQuotaWindow.reserved_bytes)], [
  quotaReservationUserBytes,
  chatAssistantStorageReservationBytes
], '接受阶段窗口必须同时记录用户实际字节和助手预留')
await completeChatTurn(client, {
  conversationId: quotaSettlementConversation.id,
  systemAccountId: quotaReservationAccountId,
  turnId: quotaSettlementTurn.turnId,
  assistantContent: 'A',
  finishReason: 'stop',
  traceId: 'trace_quota_settlement',
  now: '2026-07-12T00:01:02.000Z'
})
assert.equal((database.prepare('SELECT storage_reserved_bytes FROM chat_messages WHERE id = ?').get(quotaSettlementTurn.assistantMessage.id) as { storage_reserved_bytes?: unknown }).storage_reserved_bytes, 0, '终态助手消息不得保留 reservation')
const completedQuotaWindow = database.prepare('SELECT content_bytes, reserved_bytes FROM chat_user_storage_windows WHERE system_account_id = ?').get(quotaReservationAccountId) as { content_bytes?: unknown; reserved_bytes?: unknown }
assert.deepEqual([Number(completedQuotaWindow.content_bytes), Number(completedQuotaWindow.reserved_bytes)], [
  quotaReservationUserBytes + quotaReservationAssistantBytes,
  0
], 'complete 必须把预留原子结算为实际助手字节并释放差额')
assert.equal((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_messages WHERE system_account_id = ?').get(quotaReservationAccountId) as { total?: unknown }).total, quotaReservationUserBytes + quotaReservationAssistantBytes, '容量窗口实际字节必须与消息表一致')
assertStorageLedgerInvariant(database, quotaReservationAccountId, 'complete 结算')

const oversizedAccountId = 'sys_quota_oversized_finalize'
const oversizedConversation = await createChatConversation(client, {
  id: 'chat_conv_quota_oversized_finalize',
  systemAccountId: oversizedAccountId,
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
  now: '2026-07-12T00:02:00.000Z'
})
const oversizedTurn = await acceptChatTurn(client, {
  conversationId: oversizedConversation.id,
  systemAccountId: oversizedAccountId,
  clientMessageId: 'client_quota_oversized_finalize',
  userContent: 'oversized',
  model: 'mock-model',
  now: '2026-07-12T00:02:01.000Z',
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await assert.rejects(
  completeChatTurn(client, {
    conversationId: oversizedConversation.id,
    systemAccountId: oversizedAccountId,
    turnId: oversizedTurn.turnId,
    assistantContent: 'x'.repeat(chatAssistantStorageReservationBytes),
    finishReason: 'stop',
    traceId: 'trace_quota_oversized_finalize',
    now: '2026-07-12T00:02:02.000Z'
  }),
  (error) => error instanceof ChatAssistantStorageLimitError && error.code === 'chat_assistant_storage_limit_exceeded',
  '超过 reservation 时必须在终态事务提交后向调用方抛出稳定错误'
)
const oversizedAssistantState = database.prepare(`
  SELECT status, error_code, storage_reserved_bytes
  FROM chat_messages WHERE id = ?
`).get(oversizedTurn.assistantMessage.id) as { status?: unknown; error_code?: unknown; storage_reserved_bytes?: unknown }
assert.deepEqual([oversizedAssistantState.status, oversizedAssistantState.error_code, Number(oversizedAssistantState.storage_reserved_bytes)], [
  'failed', 'chat_assistant_storage_limit_exceeded', 0
], '超限 finalize 不能回滚已终结的 failed 状态')
assert.equal((await getChatConversation(client, oversizedConversation.id, oversizedAccountId))?.activeTurnId, undefined, '超限 finalize 必须清理 active_turn_id')
assertStorageLedgerInvariant(database, oversizedAccountId, '超限 finalize')

const partialRetentionAccountId = 'sys_quota_partial_retention'
const partialRetentionExpiredConversation = await createChatConversation(client, {
  id: 'chat_conv_quota_partial_retention_expired', systemAccountId: partialRetentionAccountId,
  apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-01T00:00:00.000Z'
})
await acceptChatTurn(client, {
  conversationId: partialRetentionExpiredConversation.id, systemAccountId: partialRetentionAccountId,
  clientMessageId: 'client_quota_partial_retention_expired', userContent: '当日早期轮次', model: 'mock-model',
  now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const partialRetentionActiveConversation = await createChatConversation(client, {
  id: 'chat_conv_quota_partial_retention_active', systemAccountId: partialRetentionAccountId,
  apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-01T23:00:00.000Z'
})
const partialRetentionActiveTurn = await acceptChatTurn(client, {
  conversationId: partialRetentionActiveConversation.id, systemAccountId: partialRetentionAccountId,
  clientMessageId: 'client_quota_partial_retention_active', userContent: '当日晚期轮次', model: 'mock-model',
  now: '2026-07-01T23:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await cleanupChatRetention(client, {
  retentionDays: 7,
  now: '2026-07-08T00:02:00.000Z',
  interruptedBefore: '2026-06-30T00:00:00.000Z',
  limit: 100
})
assert.equal((await listChatMessages(client, {
  conversationId: partialRetentionActiveConversation.id,
  systemAccountId: partialRetentionAccountId,
  limit: 10,
  now: '2026-07-08T00:02:00.000Z'
}))[1]?.turnId, partialRetentionActiveTurn.turnId, '部分 retention 不得删除同日仍在保留期的活动轮次')
assertStorageLedgerInvariant(database, partialRetentionAccountId, '同日部分 retention')
await cancelChatTurn(client, {
  conversationId: partialRetentionActiveConversation.id,
  systemAccountId: partialRetentionAccountId,
  turnId: partialRetentionActiveTurn.turnId,
  assistantContent: '',
  now: '2026-07-08T00:03:00.000Z'
})
assert.equal(await deleteChatConversation(client, partialRetentionActiveConversation.id, partialRetentionAccountId), true, '部分 retention 夹具必须清理剩余活动会话，避免影响后续 stale 计数')

const retentionBacklogAccountId = 'sys_quota_retention_backlog'
for (let index = 0; index < 3; index += 1) {
  const conversationId = `chat_conv_quota_retention_backlog_${index}`
  await createChatConversation(client, {
    id: conversationId,
    systemAccountId: retentionBacklogAccountId,
    apiKeyId: 'key_1',
    apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
    now: `2026-07-01T00:0${index}:00.000Z`
  })
  await acceptChatTurn(client, {
    conversationId,
    systemAccountId: retentionBacklogAccountId,
    clientMessageId: `client_quota_retention_backlog_${index}`,
    userContent: `积压轮次 ${index}`,
    model: 'mock-model',
    now: `2026-07-01T00:0${index}:01.000Z`,
    storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
  })
}
const firstBacklogCleanup = await cleanupChatRetention(client, {
  retentionDays: 7,
  now: '2026-07-10T00:00:00.000Z',
  interruptedBefore: '2026-07-09T00:00:00.000Z',
  limit: 2
})
assert.equal(firstBacklogCleanup.hasMore, true, '小批次清理必须报告仍有积压')
await cleanupChatRetention(client, {
  retentionDays: 7,
  now: '2026-07-10T00:01:00.000Z',
  interruptedBefore: '2026-07-09T00:00:00.000Z',
  limit: 2
})
await cleanupChatRetention(client, {
  retentionDays: 7,
  now: '2026-07-10T00:02:00.000Z',
  interruptedBefore: '2026-07-09T00:00:00.000Z',
  limit: 2
})
assert.equal((database.prepare('SELECT COUNT(*) AS total FROM chat_messages WHERE system_account_id = ?').get(retentionBacklogAccountId) as { total?: unknown }).total, 0, '多批次清理最终必须收口全部旧轮次')
assertStorageLedgerInvariant(database, retentionBacklogAccountId, '多批次 retention 积压清理')

const conversation = await createChatConversation(client, {
  id: 'chat_conv_1',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
  now: '2026-07-12T00:00:00.000Z'
})
assert.equal(conversation.title, '新对话')
assert.equal(conversation.messageRevision, 0, '新会话的可见消息 revision 必须从 0 开始')
assert.equal((await listChatConversations(client, { systemAccountId: 'sys_user_1', limit: 20 })).length, 1)
assert.equal((await listChatConversations(client, { systemAccountId: 'sys_user_2', limit: 20 })).length, 0)

const pinnedConversation = await createChatConversation(client, {
  id: 'chat_conv_pinned',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
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
assert.equal(renamedPinned?.messageRevision, 0, '重命名和置顶不得推进可见消息 revision')
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.equal(accepted.userMessage.sequenceNo, 1)
assert.equal(accepted.assistantMessage.sequenceNo, 2)
assert.equal(accepted.duplicate, false)
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, 1, '接受新轮次必须推进一次可见消息 revision')
assert.deepEqual(await findChatTurnByClientMessageId(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1'
}), { turnId: accepted.turnId, assistantStatus: 'streaming' })
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.equal(duplicate.duplicate, true)
assert.equal(duplicate.turnId, accepted.turnId)
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, 1, '幂等 accept 重放不得推进可见消息 revision')

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_user_1',
    clientMessageId: 'client_2',
    userContent: '并发问题',
    model: 'mock-model',
    now: '2026-07-12T00:01:02.000Z',
    storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_message_in_progress'
)
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, 1, '活动轮次冲突不得推进可见消息 revision')

const disposable = await createChatConversation(client, {
  id: 'chat_conv_delete',
  systemAccountId: 'sys_user_1',
  apiKeyId: 'key_1',
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
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
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, 2, '完成回答必须推进一次可见消息 revision')
assert.deepEqual(await findChatTurnByClientMessageId(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_1'
}), { turnId: accepted.turnId, assistantStatus: 'completed' }, '提交状态查询必须返回助手权威终态')

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
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.messageRevision, 1)
await completeChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  turnId: replaceOriginal.turnId,
  assistantContent: '这是需要被原子移除的旧回答。'.repeat(12),
  finishReason: 'stop',
  traceId: 'trace_replace_original',
  now: '2026-07-13T00:02:00.000Z'
})
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.messageRevision, 2)
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
  storageQuotaBytes: 1, retentionDays: 7, maxTurnsPerConversation: 1000,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(duplicateWithOldClientId.duplicate, true)
assert.equal(duplicateWithOldClientId.turnId, replaceOriginal.turnId)
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.messageRevision, 2, '旧 clientMessageId 幂等重放不得推进 revision')
const replacement = await acceptChatTurn(client, {
  conversationId: replaceConversation.id,
  systemAccountId: replaceAccountId,
  clientMessageId: 'client_replace_new',
  userContent: replacementContent,
  contentBlocks: [{ type: 'input_text', text: replacementContent }],
  model: 'mock-model',
  now: '2026-07-13T01:00:00.000Z',
  storageQuotaBytes: replacementUserBytes + chatAssistantStorageReservationBytes, retentionDays: 7, maxTurnsPerConversation: 1000,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(replacement.userMessage.sequenceNo, replaceOriginal.userMessage.sequenceNo)
assert.equal(replacement.assistantMessage.sequenceNo, replaceOriginal.assistantMessage.sequenceNo)
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.messageRevision, 3, '替换最近轮次的删旧写新事务只能推进一次 revision')
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
  storageQuotaBytes: 1, retentionDays: 7, maxTurnsPerConversation: 1000,
  replaceTurnId: replaceOriginal.turnId
})
assert.equal(replacementReplay.duplicate, true)
assert.equal(replacementReplay.turnId, replacement.turnId)
assert.equal((await getChatConversation(client, replaceConversation.id, replaceAccountId))?.messageRevision, 3, '替换请求幂等重放不得推进 revision')
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
assert.equal(Number((database.prepare(`
  SELECT reserved_bytes FROM chat_user_storage_windows
  WHERE system_account_id = ? AND bucket_date = ?
`).get(replaceAccountId, '2026-07-13') as { reserved_bytes?: unknown })?.reserved_bytes ?? 0), chatAssistantStorageReservationBytes, '替换轮次必须重新预留助手容量')
assertStorageLedgerInvariant(database, replaceAccountId, '替换轮次接受')

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: replaceConversation.id,
    systemAccountId: replaceAccountId,
    clientMessageId: 'client_replace_while_active',
    userContent: '活动会话不得替换',
    contentBlocks: [{ type: 'input_text' }],
    model: 'mock-model',
    now: '2026-07-13T01:00:02.000Z',
    storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000,
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
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
    storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000,
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
    conversationId: replaceConversation.id, systemAccountId: replaceAccountId, clientMessageId: 'client_replace_quota_failure', userContent: '容量不足时必须完整回滚', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T01:05:01.000Z', storageQuotaBytes: 1, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: replaceLatest.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_storage_quota_exceeded'
)
assert.deepEqual(await listChatMessages(client, { conversationId: replaceConversation.id, systemAccountId: replaceAccountId, limit: 20, now: '2026-07-13T01:05:02.000Z' }), replacementStateBeforeQuotaFailure.messages, '替换容量失败不得删除旧消息')
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(replaceAccountId) as { total?: unknown })?.total ?? 0), replacementStateBeforeQuotaFailure.storageBytes, '替换容量失败不得改变容量窗口')
assert.deepEqual(database.prepare('SELECT title, title_source_message_id, next_sequence_no, active_turn_id FROM chat_conversations WHERE id = ?').get(replaceConversation.id), replacementStateBeforeQuotaFailure.conversation, '替换容量失败不得改变会话状态')

const failedReplaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_failed', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T02:00:00.000Z'
})
const failedReplaceTurn = await acceptChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_failed', userContent: '失败轮次', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T02:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await failChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: failedReplaceTurn.turnId, assistantContent: '失败回答', errorCode: 'mock_failed', now: '2026-07-13T02:02:00.000Z'
})
const failedReplacement = await acceptChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_failed_new', userContent: '失败后重新生成', contentBlocks: [{ type: 'input_text', text: '失败后重新生成' }], model: 'mock-model', now: '2026-07-13T02:03:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: failedReplaceTurn.turnId
})
assert.deepEqual(
  [failedReplacement.userMessage.sequenceNo, failedReplacement.assistantMessage.sequenceNo],
  [failedReplaceTurn.userMessage.sequenceNo, failedReplaceTurn.assistantMessage.sequenceNo],
  '失败轮替换必须复用原消息序号'
)
assert.equal(await findChatTurnByClientMessageId(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_failed'
}), undefined, '失败轮替换必须删除旧幂等登记')
assert.equal(Number((database.prepare("SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ? AND role = 'user'").get(failedReplaceConversation.id) as { total?: unknown }).total), 1, '失败轮替换不能增加 userTurnCount')
assert.equal(Number((database.prepare('SELECT next_sequence_no FROM chat_conversations WHERE id = ?').get(failedReplaceConversation.id) as { next_sequence_no?: unknown }).next_sequence_no), 3)
assert.equal(Number((database.prepare('SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ? AND turn_id = ?').get(failedReplaceConversation.id, failedReplaceTurn.turnId) as { total?: unknown }).total), 0, '失败轮替换后旧消息对不得遗留')
assertStorageLedgerInvariant(database, 'sys_user_1', '失败轮替换接受')
await completeChatTurn(client, {
  conversationId: failedReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: failedReplacement.turnId,
  assistantContent: '重新生成成功', finishReason: 'stop', traceId: 'trace_replace_failed_success', now: '2026-07-13T02:04:00.000Z'
})
assert.equal(Number((database.prepare("SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ? AND role = 'assistant' AND status = 'completed'").get(failedReplaceConversation.id) as { total?: unknown }).total), 1)
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(reserved_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get('sys_user_1') as { total?: unknown }).total), 0, '失败轮替换完成后 reservation 必须归零')
assertStorageLedgerInvariant(database, 'sys_user_1', '失败轮替换完成')

const canceledReplaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_canceled', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T02:10:00.000Z'
})
const canceledReplaceTurn = await acceptChatTurn(client, {
  conversationId: canceledReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_canceled', userContent: '取消轮次', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T02:11:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await cancelChatTurn(client, {
  conversationId: canceledReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: canceledReplaceTurn.turnId, assistantContent: '', traceId: 'trace_replace_canceled', now: '2026-07-13T02:12:00.000Z'
})
const canceledReplacement = await acceptChatTurn(client, {
  conversationId: canceledReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_canceled_new', userContent: '取消后显式重新生成', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T02:13:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: canceledReplaceTurn.turnId
})
assert.deepEqual([canceledReplacement.userMessage.sequenceNo, canceledReplacement.assistantMessage.sequenceNo], [canceledReplaceTurn.userMessage.sequenceNo, canceledReplaceTurn.assistantMessage.sequenceNo], '取消轮替换必须复用原序号')
assert.equal(Number((database.prepare("SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ? AND role = 'user'").get(canceledReplaceConversation.id) as { total?: unknown }).total), 1, '取消轮替换不能增加 userTurnCount')
assert.equal(Number((database.prepare('SELECT COUNT(*) AS total FROM chat_messages WHERE conversation_id = ? AND turn_id = ?').get(canceledReplaceConversation.id, canceledReplaceTurn.turnId) as { total?: unknown }).total), 0, '取消轮替换后旧消息对必须完整删除')
assert.equal(await findChatTurnByClientMessageId(client, { conversationId: canceledReplaceConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_canceled' }), undefined, '取消轮替换必须删除旧幂等登记')
await completeChatTurn(client, { conversationId: canceledReplaceConversation.id, systemAccountId: 'sys_user_1', turnId: canceledReplacement.turnId, assistantContent: '停止后重新生成成功', finishReason: 'stop', traceId: 'trace_replace_canceled_success', now: '2026-07-13T02:14:00.000Z' })
assertStorageLedgerInvariant(database, 'sys_user_1', '取消轮替换完成')

const malformedMarkerConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_malformed_marker', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T03:10:00.000Z'
})
const malformedMarkerTurn = await acceptChatTurn(client, {
  conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_malformed_marker', userContent: '标记损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:11:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, { conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', turnId: malformedMarkerTurn.turnId, assistantContent: '回答', finishReason: 'stop', traceId: 'trace_malformed_marker', now: '2026-07-13T03:12:00.000Z' })
database.prepare("UPDATE chat_messages SET content_blocks_json = '{malformed' WHERE id = ?").run(malformedMarkerTurn.userMessage.id)
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: malformedMarkerConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_replace_malformed_marker_new', userContent: '损坏标记不得降级成纯文本', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:13:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: malformedMarkerTurn.turnId
  }),
  (error) => error instanceof ChatConflictError && error.code === 'chat_replace_conflict',
  '未知或畸形输入标记必须 fail closed'
)

const missingWindowConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_missing_window', systemAccountId: 'sys_missing_window', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T03:20:00.000Z'
})
const missingWindowTurn = await acceptChatTurn(client, {
  conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', clientMessageId: 'client_replace_missing_window', userContent: '容量窗口损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:21:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, { conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', turnId: missingWindowTurn.turnId, assistantContent: '回答', finishReason: 'stop', traceId: 'trace_missing_window', now: '2026-07-13T03:22:00.000Z' })
database.prepare('DELETE FROM chat_user_storage_windows WHERE system_account_id = ?').run('sys_missing_window')
await assert.rejects(
  acceptChatTurn(client, {
    conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', clientMessageId: 'client_replace_missing_window_new', userContent: '不得掩盖容量窗口损坏', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T03:23:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: missingWindowTurn.turnId
  }),
  /容量窗口数据不一致/
)
assert.equal((await listChatMessages(client, { conversationId: missingWindowConversation.id, systemAccountId: 'sys_missing_window', limit: 20, now: '2026-07-13T03:24:00.000Z' }))[0]?.turnId, missingWindowTurn.turnId, '容量窗口损坏时旧轮次必须保留')

const crossDayAccountId = 'sys_replace_cross_day'
const crossDayConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_cross_day', systemAccountId: crossDayAccountId, apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T23:50:00.000Z'
})
const crossDayOriginal = await acceptChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, clientMessageId: 'client_replace_cross_day_old', userContent: '前一天的问题', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T23:58:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, turnId: crossDayOriginal.turnId, assistantContent: '跨过午夜完成的旧回答', finishReason: 'stop', traceId: 'trace_replace_cross_day_old', now: '2026-07-14T00:01:00.000Z'
})
assert(Number((database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-13') as { content_bytes?: unknown })?.content_bytes ?? 0) > 0, '旧轮次应计入创建时所在日桶')
const crossDayNewContent = '次日修正的问题'
const crossDayMarkerJson = JSON.stringify([{ type: 'input_text', text: '', order: 0 }])
const crossDayNewUserBytes = Buffer.byteLength(crossDayNewContent, 'utf8') + Buffer.byteLength(crossDayMarkerJson, 'utf8')
await acceptChatTurn(client, {
  conversationId: crossDayConversation.id, systemAccountId: crossDayAccountId, clientMessageId: 'client_replace_cross_day_new', userContent: crossDayNewContent, contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-14T00:02:00.000Z', storageQuotaBytes: crossDayNewUserBytes + chatAssistantStorageReservationBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: crossDayOriginal.turnId
})
assert.equal(database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-13'), undefined, '跨日替换必须准确扣除并删除旧日空桶')
assert.equal(Number((database.prepare('SELECT content_bytes FROM chat_user_storage_windows WHERE system_account_id = ? AND bucket_date = ?').get(crossDayAccountId, '2026-07-14') as { content_bytes?: unknown })?.content_bytes ?? 0), crossDayNewUserBytes, '跨日替换的新日桶只能增加新用户消息字节')
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(content_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(crossDayAccountId) as { total?: unknown })?.total ?? 0), crossDayNewUserBytes, '跨日替换后的总额必须等于净额')
assert.equal(Number((database.prepare('SELECT COALESCE(SUM(reserved_bytes), 0) AS total FROM chat_user_storage_windows WHERE system_account_id = ?').get(crossDayAccountId) as { total?: unknown })?.total ?? 0), chatAssistantStorageReservationBytes, '跨日替换必须在新日桶保留助手 reservation')
assertStorageLedgerInvariant(database, crossDayAccountId, '跨日替换')

const imageReplaceConversation = await createChatConversation(client, {
  id: 'chat_conv_replace_image', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-13T03:00:00.000Z'
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
  quotaBytes: 68, retentionDays: 7,
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
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
const oldObservationClaim = await claimChatAssetObservation(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  expectedTurnId: imageReplaceTurn.turnId,
  expectedMessageId: imageReplaceTurn.userMessage.id,
  now: '2026-07-13T03:03:10.000Z'
})
if (!oldObservationClaim?.observationClaimId) throw new Error('图片说明认领失败')
await setChatAssetObservation(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  status: 'ready',
  observation: { summary: '旧问题与旧回答产生的说明' },
  observationRevision: oldObservationClaim.observationRevision,
  claimId: oldObservationClaim.observationClaimId,
  now: '2026-07-13T03:03:20.000Z'
})
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000,
  replaceTurnId: imageReplaceTurn.turnId
})
assert.notEqual(replacedImageTurn.turnId, imageReplaceTurn.turnId)
const reboundImageAsset = await getChatAsset(client, { assetId: imageAssetId, systemAccountId: 'sys_user_1', conversationId: imageReplaceConversation.id })
assert.deepEqual([reboundImageAsset?.turnId, reboundImageAsset?.messageId], [replacedImageTurn.turnId, replacedImageTurn.userMessage.id], '含图片的最近一轮重新生成时必须把原资产原子改绑到新轮次')
assert.equal(reboundImageAsset?.observationStatus, 'not_requested', '重新生成必须失效旧问题与旧回答产生的图片说明')
assert.equal(reboundImageAsset?.observation, undefined)
const delayedOldObservationClaim = await claimChatAssetObservation(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  expectedTurnId: imageReplaceTurn.turnId,
  expectedMessageId: imageReplaceTurn.userMessage.id,
  now: '2026-07-13T03:04:05.000Z'
})
assert.equal(delayedOldObservationClaim, undefined, '重新生成提交后才开始的旧图片说明任务不能认领新轮次 revision')
assert.equal((await getChatAsset(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id
}))?.observationStatus, 'not_requested', '旧调度任务认领失败时不得改变新轮次图片说明状态')
assert.equal(await setChatAssetObservation(client, {
  assetId: imageAssetId,
  systemAccountId: 'sys_user_1',
  conversationId: imageReplaceConversation.id,
  status: 'ready',
  observation: { summary: '迟到的旧说明' },
  observationRevision: oldObservationClaim.observationRevision,
  claimId: oldObservationClaim.observationClaimId,
  now: '2026-07-13T03:04:10.000Z'
}), undefined, '旧说明任务不能覆盖重新生成后的图片状态')
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
    conversationId: replaceConversation.id, systemAccountId: replaceAccountId, clientMessageId: 'client_replace_other_conversation', userContent: '跨会话替换', contentBlocks: [{ type: 'input_text' }], model: 'mock-model', now: '2026-07-13T04:00:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000, replaceTurnId: imageReplaceTurn.turnId
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
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const revisionBeforeFailure = (await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision
await failChatTurn(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  turnId: failedTurn.turnId,
  assistantContent: '部分回答',
  errorCode: 'mock_interrupted',
  traceId: 'trace_chat_failed',
  now: '2026-07-12T00:05:00.000Z'
})
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, Number(revisionBeforeFailure) + 1, '失败终结实际改变可见消息时必须推进一次 revision')
assertStorageLedgerInvariant(database, 'sys_user_1', 'failed 轮次结算')
const contextAfterFailure = await listChatContextMessages(client, {
  conversationId: conversation.id,
  systemAccountId: 'sys_user_1',
  limitTurns: 64,
  now: '2026-07-12T00:06:00.000Z'
})
assert.equal(contextAfterFailure.length, 2, '失败轮次的一问一答都不能进入下一轮上下文')

const canceledTurn = await acceptChatTurn(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_canceled', userContent: '这轮会取消', model: 'mock-model', now: '2026-07-12T00:06:10.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const revisionBeforeCancel = (await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision
await cancelChatTurn(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', turnId: canceledTurn.turnId, assistantContent: '已生成的部分', traceId: 'trace_chat_canceled', now: '2026-07-12T00:06:20.000Z'
})
assert.equal((await getChatConversation(client, conversation.id, 'sys_user_1'))?.messageRevision, Number(revisionBeforeCancel) + 1, '取消终结实际改变可见消息时必须推进一次 revision')
assertStorageLedgerInvariant(database, 'sys_user_1', 'canceled 轮次结算')
const contextAfterCancel = await listChatContextMessages(client, {
  conversationId: conversation.id, systemAccountId: 'sys_user_1', limitTurns: 64, now: '2026-07-12T00:06:30.000Z'
})
assert.equal(contextAfterCancel.length, 2, '取消轮次的用户问题与部分回答都不能进入下一轮上下文')

const revisionsBeforeMissingConditionalStop = database.prepare(`
  SELECT id, message_revision FROM chat_conversations ORDER BY id ASC
`).all()
const messagesBeforeMissingConditionalStop = database.prepare(`
  SELECT * FROM chat_messages ORDER BY id ASC
`).all()
const missingConditionalStop = await cancelActiveChatTurnIfMatches(client, {
  conversationId: 'chat_conv_missing_conditional_stop',
  systemAccountId: 'sys_user_1',
  expectedTurnId: 'turn_missing_conditional_stop',
  now: '2026-07-12T00:06:40.000Z'
})
assert.deepEqual(missingConditionalStop, { state: 'not_found' }, '不存在的会话必须返回 not_found')
assert.deepEqual(database.prepare(`
  SELECT id, message_revision FROM chat_conversations ORDER BY id ASC
`).all(), revisionsBeforeMissingConditionalStop, '不存在的会话 stop 不得推进任何现存会话 revision')
assert.deepEqual(database.prepare(`
  SELECT * FROM chat_messages ORDER BY id ASC
`).all(), messagesBeforeMissingConditionalStop, '不存在的会话 stop 不得产生任何消息副作用')

const conditionalStopConversation = await createChatConversation(client, {
  id: 'chat_conv_conditional_stop', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:07:00.000Z'
})
const conditionalTurnA = await acceptChatTurn(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_conditional_a', userContent: '条件停止 A', model: 'mock-model', now: '2026-07-12T00:07:01.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const firstConditionalStop = await cancelActiveChatTurnIfMatches(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', expectedTurnId: conditionalTurnA.turnId, now: '2026-07-12T00:07:02.000Z'
})
assert.deepEqual(firstConditionalStop, { state: 'canceled', assistantStatus: 'canceled' }, '匹配活动轮次的条件 stop 必须原子取消')
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.messageRevision, 2, '条件 stop 实际取消消息时必须推进一次 revision')
assertStorageLedgerInvariant(database, 'sys_user_1', 'conditional stop')
const repeatedConditionalStop = await cancelActiveChatTurnIfMatches(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', expectedTurnId: conditionalTurnA.turnId, now: '2026-07-12T00:07:03.000Z'
})
assert.deepEqual(repeatedConditionalStop, { state: 'already_terminal', assistantStatus: 'canceled' }, '并发或重复 stop 必须幂等返回终态，不能 500')
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.messageRevision, 2, '重复条件 stop 不得推进 revision')
const conditionalRaceConversation = await createChatConversation(client, {
  id: 'chat_conv_conditional_race', systemAccountId: 'sys_conditional_race', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:07:03.100Z'
})
const conditionalRaceTurn = await acceptChatTurn(client, {
  conversationId: conditionalRaceConversation.id, systemAccountId: 'sys_conditional_race', clientMessageId: 'client_conditional_race', userContent: '条件停止并发', model: 'mock-model', now: '2026-07-12T00:07:03.200Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const conditionalRaceStop = await cancelActiveChatTurnIfMatches(
  simulateLostConditionalStopUpdate(client),
  { conversationId: conditionalRaceConversation.id, systemAccountId: 'sys_conditional_race', expectedTurnId: conditionalRaceTurn.turnId, now: '2026-07-12T00:07:03.300Z' }
)
assert.deepEqual(conditionalRaceStop, { state: 'already_terminal', assistantStatus: 'canceled' }, '条件更新落空时必须重读并返回数据库权威终态，不能抛出 500')
assert.equal((await getChatConversation(client, conditionalRaceConversation.id, 'sys_conditional_race'))?.activeTurnId, undefined, '并发赢家已取消轮次时会话活动标记必须保持已清除')
assert.equal((await getChatConversation(client, conditionalRaceConversation.id, 'sys_conditional_race'))?.messageRevision, 2, '并发 stop 赢家实际取消消息时必须且只能推进一次 revision')
assertStorageLedgerInvariant(database, 'sys_conditional_race', '并发 conditional stop')
const conditionalTurnB = await acceptChatTurn(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'client_conditional_b', userContent: '条件停止 B', model: 'mock-model', now: '2026-07-12T00:07:04.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.messageRevision, 3)
const staleConditionalStop = await cancelActiveChatTurnIfMatches(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', expectedTurnId: conditionalTurnA.turnId, now: '2026-07-12T00:07:05.000Z'
})
assert.deepEqual(staleConditionalStop, { state: 'already_terminal', assistantStatus: 'canceled' }, '旧轮次 stop 不得误杀新轮次')
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.activeTurnId, conditionalTurnB.turnId, '旧轮次 stop 后新轮次必须仍活动')
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.messageRevision, 3, '旧终态轮次 stop 不得推进 revision')
const mismatchedConditionalStop = await cancelActiveChatTurnIfMatches(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', expectedTurnId: 'turn_unknown', now: '2026-07-12T00:07:06.000Z'
})
assert.deepEqual(mismatchedConditionalStop, { state: 'turn_mismatch' }, '未知期望轮次不得取消当前活动轮次')
assert.equal((await getChatConversation(client, conditionalStopConversation.id, 'sys_user_1'))?.messageRevision, 3, '轮次不匹配不得推进 revision')
await cancelChatTurn(client, {
  conversationId: conditionalStopConversation.id, systemAccountId: 'sys_user_1', turnId: conditionalTurnB.turnId, assistantContent: '', now: '2026-07-12T00:07:07.000Z'
})

const stale = await createChatConversation(client, {
  id: 'chat_conv_stale', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-01T00:00:00.000Z'
})
await acceptChatTurn(client, {
  conversationId: stale.id, systemAccountId: 'sys_user_1', clientMessageId: 'stale_1', userContent: '过期问题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
const titleConversation = await createChatConversation(client, {
  id: 'chat_conv_title', systemAccountId: 'sys_user_1', apiKeyId: 'key_1', apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000, now: '2026-07-01T00:00:00.000Z'
})
const oldTitleTurn = await acceptChatTurn(client, {
  conversationId: titleConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'title_old', userContent: '已经过期的旧标题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, { conversationId: titleConversation.id, systemAccountId: 'sys_user_1', turnId: oldTitleTurn.turnId, assistantContent: '旧回答', finishReason: 'stop', traceId: 'trace_title_old', now: '2026-07-01T00:02:00.000Z' })
const newTitleTurn = await acceptChatTurn(client, {
  conversationId: titleConversation.id, systemAccountId: 'sys_user_1', clientMessageId: 'title_new', userContent: '仍在保留期的新标题', model: 'mock-model', now: '2026-07-11T00:01:00.000Z', storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, { conversationId: titleConversation.id, systemAccountId: 'sys_user_1', turnId: newTitleTurn.turnId, assistantContent: '新回答', finishReason: 'stop', traceId: 'trace_title_new', now: '2026-07-11T00:02:00.000Z' })
const cleanup = await cleanupChatRetention(client, {
  retentionDays: 7,
  now: '2026-07-12T00:10:00.000Z', interruptedBefore: '2026-07-12T00:00:00.000Z', limit: 1000
})
assert.equal(cleanup.recoveredTurns, 1, '超时 streaming 轮次应先恢复为失败')
assert.equal(cleanup.deletedMessages, 4, '清理必须按完整轮次成对删除')
assert.equal(cleanup.deletedConversations, 1, '没有保留消息的会话应删除')
assert.equal((await getChatConversation(client, titleConversation.id, 'sys_user_1'))?.title, '仍在保留期的新标题', '标题来源过期后应使用最早保留用户消息重算')
assertStorageLedgerInvariant(database, 'sys_user_1', 'stale 恢复与 retention 清理')

await assert.rejects(
  acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_user_1',
    clientMessageId: 'client_quota',
    userContent: '超限',
    model: 'mock-model',
    now: '2026-07-12T00:07:00.000Z',
    storageQuotaBytes: 1, retentionDays: 7, maxTurnsPerConversation: 1000
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
  apiKeyNameSnapshot: '默认 Key', maxConversationsPerUser: 1000,
  now: '2026-07-12T01:00:00.000Z'
})
const initializationTurn = await acceptChatTurn(client, {
  conversationId: initializationConversation.id,
  systemAccountId: 'sys_user_1',
  clientMessageId: 'client_initialization_failure',
  userContent: '触发初始化失败',
  model: 'mock-model',
  now: '2026-07-12T01:00:01.000Z',
  storageQuotaBytes: testStorageQuotaBytes, retentionDays: 7, maxTurnsPerConversation: 1000
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

const observationTasks = new Map<string, Set<Promise<void>>>()
let finishOldObservation!: () => void
let finishNewObservation!: () => void
const oldObservation = new Promise<void>((resolve) => { finishOldObservation = resolve })
const newObservation = new Promise<void>((resolve) => { finishNewObservation = resolve })
trackActiveChatObservation(observationTasks, 'asset_revision_overlap', oldObservation)
trackActiveChatObservation(observationTasks, 'asset_revision_overlap', newObservation)
assert.equal(listActiveChatObservationTasks(observationTasks, ['asset_revision_overlap']).length, 2, '旧 revision 任务运行时必须允许新 revision 图片说明任务并行进入数据库 claim')
finishOldObservation()
await oldObservation
await new Promise((resolve) => setTimeout(resolve, 0))
assert.deepEqual(listActiveChatObservationTasks(observationTasks, ['asset_revision_overlap']), [newObservation], '旧任务结束不得吞掉新 revision 的活跃任务')
finishNewObservation()
await newObservation
await new Promise((resolve) => setTimeout(resolve, 0))
assert.equal(observationTasks.has('asset_revision_overlap'), false, '最后一个图片说明任务结束后必须清理进程内登记')

console.log('AI 问答 repository 回归通过')

function simulateLostConditionalStopUpdate(base: DatabaseClient): DatabaseClient {
  let simulated = false
  return {
    ...base,
    transaction: async <T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> => base.transaction(async (tx) => operation({
      ...tx,
      execute: async (sql, params = []) => {
        const normalized = sql.replace(/\s+/g, ' ').trim().toLowerCase()
        if (!simulated
          && normalized.startsWith('update "chat_messages"')
          && normalized.includes("set status = 'canceled'")
          && normalized.includes("status = 'streaming'")) {
          simulated = true
          const [, conversationId, systemAccountId, turnId] = params
          const reservation = await tx.one<{ created_at?: unknown; storage_reserved_bytes?: unknown }>(`
            SELECT created_at, storage_reserved_bytes
            FROM ${tx.dialect.qualifyTable('juhe_chat', 'chat_messages')}
            WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
              AND role = 'assistant' AND status = 'streaming'
          `, [conversationId, systemAccountId, turnId])
          assert.equal(Number(reservation?.storage_reserved_bytes), chatAssistantStorageReservationBytes, '测试夹具必须读到并发赢家要释放的 reservation')
          const winnerMessage = await tx.execute(sql, params)
          assert.equal(winnerMessage.changes, 1, '测试夹具必须先模拟并发 stop 赢得消息条件更新')
          const [now] = params
          const winnerReservation = await tx.execute(`
            UPDATE ${tx.dialect.qualifyTable('juhe_chat', 'chat_user_storage_windows')}
            SET reserved_bytes = reserved_bytes - ?, updated_at = ?
            WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?
          `, [chatAssistantStorageReservationBytes, now, systemAccountId, String(reservation?.created_at).slice(0, 10), chatAssistantStorageReservationBytes])
          assert.equal(winnerReservation.changes, 1, '测试夹具必须模拟并发 stop 赢家释放容量预留')
          const winnerConversation = await tx.execute(`
            UPDATE ${tx.dialect.qualifyTable('juhe_chat', 'chat_conversations')}
            SET active_turn_id = NULL, active_started_at = NULL,
                message_revision = message_revision + 1,
                last_message_at = ?, updated_at = ?
            WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
          `, [now, now, conversationId, systemAccountId, turnId])
          assert.equal(winnerConversation.changes, 1, '测试夹具必须模拟并发 stop 完整清除活动轮次')
          return { changes: 0 }
        }
        return tx.execute(sql, params)
      }
    }))
  }
}
function assertStorageLedgerInvariant(database: DatabaseSync, systemAccountId: string, scenario: string): void {
  const messageTotals = database.prepare(`
    SELECT COALESCE(SUM(content_bytes), 0) AS content_bytes,
           COALESCE(SUM(storage_reserved_bytes), 0) AS reserved_bytes
    FROM chat_messages WHERE system_account_id = ?
  `).get(systemAccountId) as { content_bytes?: unknown; reserved_bytes?: unknown }
  const windowTotals = database.prepare(`
    SELECT COALESCE(SUM(content_bytes), 0) AS content_bytes,
           COALESCE(SUM(reserved_bytes), 0) AS reserved_bytes
    FROM chat_user_storage_windows WHERE system_account_id = ?
  `).get(systemAccountId) as { content_bytes?: unknown; reserved_bytes?: unknown }
  assert.deepEqual(
    [Number(windowTotals.content_bytes), Number(windowTotals.reserved_bytes)],
    [Number(messageTotals.content_bytes), Number(messageTotals.reserved_bytes)],
    `${scenario}：容量窗口的 actual/reservation 必须与消息表一致`
  )
}
