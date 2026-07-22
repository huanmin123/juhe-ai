import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createChatAsset } from '../../storage/chat-assets.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import {
  acceptChatTurn,
  ChatConflictError,
  clearChatConversation,
  completeChatTurn,
  createChatConversation,
  getChatConversation,
  updateChatConversation
} from '../../storage/chat.repository.js'
import { applyChatSchema } from '../../storage/schema.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const storageQuotaBytes = 64 * 1024 * 1024

try {
  const activeOwnerId = 'chat_clear_active_owner'
  const activeConversation = await createChatConversation(client, {
    id: 'chat_clear_active_conversation',
    systemAccountId: activeOwnerId,
    apiKeyId: 'chat_clear_active_key',
    apiKeyNameSnapshot: '活动回答 Key',
    maxConversationsPerUser: 50,
    now: '2026-07-10T00:00:00.000Z'
  })
  await acceptChatTurn(client, {
    conversationId: activeConversation.id,
    systemAccountId: activeOwnerId,
    clientMessageId: 'chat_clear_active_submission',
    userContent: '正在回答',
    model: 'mock-model',
    now: '2026-07-10T00:01:00.000Z',
    storageQuotaBytes,
    retentionDays: 7,
    maxTurnsPerConversation: 50
  })
  await assert.rejects(
    clearChatConversation(client, {
      conversationId: activeConversation.id,
      systemAccountId: activeOwnerId,
      now: '2026-07-10T00:02:00.000Z'
    }),
    (error) => error instanceof ChatConflictError && error.code === 'chat_message_in_progress'
  )
  assert.equal(countRows('chat_messages', activeConversation.id), 2, '活动回答冲突必须回滚且保留原消息')

  const compactingOwnerId = 'chat_clear_compacting_owner'
  const compactingConversation = await createChatConversation(client, {
    id: 'chat_clear_compacting_conversation',
    systemAccountId: compactingOwnerId,
    apiKeyId: 'chat_clear_compacting_key',
    apiKeyNameSnapshot: '压缩中 Key',
    maxConversationsPerUser: 50,
    now: '2026-07-10T01:00:00.000Z'
  })
  for (let index = 1; index <= 2; index += 1) {
    const accepted = await acceptChatTurn(client, {
      conversationId: compactingConversation.id,
      systemAccountId: compactingOwnerId,
      clientMessageId: `chat_clear_compacting_${index}`,
      userContent: `压缩测试问题 ${index}`,
      model: 'mock-model',
      now: `2026-07-10T01:0${index}:00.000Z`,
      storageQuotaBytes,
      retentionDays: 7,
      maxTurnsPerConversation: 50
    })
    await completeChatTurn(client, {
      conversationId: compactingConversation.id,
      systemAccountId: compactingOwnerId,
      turnId: accepted.turnId,
      assistantContent: `压缩测试回答 ${index}`,
      finishReason: 'stop',
      traceId: `trace_chat_clear_compacting_${index}`,
      now: `2026-07-10T01:1${index}:00.000Z`
    })
  }
  database.prepare(`
    UPDATE chat_conversations
    SET context_revision = 2, context_state = 'compacting',
        context_claim_id = 'chat_clear_claim', context_claim_revision = 2,
        context_claim_through_sequence = 2, context_claimed_at = ?,
        context_progress_sequence = 0
    WHERE id = ?
  `).run('2026-07-10T01:20:00.000Z', compactingConversation.id)
  await assert.rejects(
    clearChatConversation(client, {
      conversationId: compactingConversation.id,
      systemAccountId: compactingOwnerId,
      now: '2026-07-10T01:21:00.000Z'
    }),
    (error) => error instanceof ChatConflictError && error.code === 'chat_context_compacting'
  )
  assert.equal(
    (database.prepare('SELECT context_state FROM chat_conversations WHERE id = ?').get(compactingConversation.id) as { context_state?: unknown }).context_state,
    'compacting',
    '压缩冲突必须保留原认领'
  )

  const ownerId = 'chat_clear_owner'
  const retainedConversation = await createChatConversation(client, {
    id: 'chat_clear_retained_conversation',
    systemAccountId: ownerId,
    apiKeyId: 'chat_clear_retained_key',
    apiKeyNameSnapshot: '保留会话 Key',
    maxConversationsPerUser: 50,
    now: '2026-07-10T02:00:00.000Z'
  })
  const retainedTurn = await acceptChatTurn(client, {
    conversationId: retainedConversation.id,
    systemAccountId: ownerId,
    clientMessageId: 'chat_clear_retained_submission',
    userContent: '同日保留的其他会话',
    model: 'retained-model',
    now: '2026-07-10T02:01:00.000Z',
    storageQuotaBytes,
    retentionDays: 7,
    maxTurnsPerConversation: 50
  })
  await completeChatTurn(client, {
    conversationId: retainedConversation.id,
    systemAccountId: ownerId,
    turnId: retainedTurn.turnId,
    assistantContent: '这部分容量不能被清空扣减',
    finishReason: 'stop',
    traceId: 'trace_chat_clear_retained',
    now: '2026-07-10T02:02:00.000Z'
  })

  const conversation = await createChatConversation(client, {
    id: 'chat_clear_conversation',
    systemAccountId: ownerId,
    apiKeyId: 'chat_clear_key',
    apiKeyNameSnapshot: '清空测试 Key',
    maxConversationsPerUser: 50,
    now: '2026-07-10T03:00:00.000Z'
  })
  await updateChatConversation(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    title: '需要清空的标题',
    isPinned: true,
    now: '2026-07-10T03:00:30.000Z'
  })
  const assetId = 'chat_asset_11111111111111111111111111111111'
  await createChatAsset(client, {
    id: assetId,
    systemAccountId: ownerId,
    conversationId: conversation.id,
    sourceKind: 'user_upload',
    originalFilename: 'clear.png',
    originalMimeType: 'image/png',
    originalWidth: 1,
    originalHeight: 1,
    originalBytes: 8,
    originalSha256: '1'.repeat(64),
    quotaBytes: 8,
    retentionDays: 7,
    now: '2026-07-10T03:01:00.000Z'
  })
  database.prepare(`
    UPDATE chat_assets
    SET processing_status = 'ready', processed_mime_type = 'image/png',
        processed_width = 1, processed_height = 1, processed_bytes = 8,
        processed_sha256 = ?, storage_key = 'chat-clear/clear.png'
    WHERE id = ?
  `).run('2'.repeat(64), assetId)
  const completedTurn = await acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    clientMessageId: 'chat_clear_completed_submission',
    userContent: '[图片]\n第一问',
    contentBlocks: [{ type: 'input_image', assetId }, { type: 'input_text', text: '第一问' }],
    model: 'clear-first-model',
    now: '2026-07-10T03:02:00.000Z',
    storageQuotaBytes,
    retentionDays: 7,
    maxTurnsPerConversation: 50
  })
  await completeChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    turnId: completedTurn.turnId,
    assistantContent: '第一答',
    finishReason: 'stop',
    traceId: 'trace_chat_clear_completed',
    now: '2026-07-10T03:03:00.000Z'
  })
  database.prepare(`
    INSERT INTO chat_image_generations (
      asset_id, conversation_id, system_account_id, operation, model, prompt,
      source_asset_ids_json, root_asset_id, size, quality, output_format, created_at, expires_at
    ) VALUES (?, ?, ?, 'generate', 'gpt-image-2', '清空前生成图', '[]', ?, '1024x1024', 'auto', 'webp', ?, ?)
  `).run(assetId, conversation.id, ownerId, assetId, '2026-07-10T03:02:00.000Z', '2026-07-17T03:02:00.000Z')
  const staleReservedTurn = await acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    clientMessageId: 'chat_clear_stale_reserved_submission',
    userContent: '第二问保留 reservation 账本',
    model: 'clear-last-model',
    now: '2026-07-11T03:00:00.000Z',
    storageQuotaBytes,
    retentionDays: 7,
    maxTurnsPerConversation: 50
  })
  database.prepare('UPDATE chat_conversations SET active_turn_id = NULL, active_started_at = NULL WHERE id = ?').run(conversation.id)

  const checkpointId = 'chat_clear_checkpoint'
  database.prepare(`
    INSERT INTO chat_context_checkpoints (
      id, conversation_id, system_account_id, version, source_revision,
      source_from_sequence, source_through_sequence, recent_tail_from_sequence,
      entry_from_sequence, entry_through_sequence, payload_digest,
      request_body_bytes, model_id, endpoint_family, prompt_version,
      status, quality_status, created_at, expires_at
    ) VALUES (?, ?, ?, 1, 7, 1, 2, 3, 1, 1, ?, 64, ?, 'responses', 'v1',
              'active', 'passed', ?, ?)
  `).run(
    checkpointId,
    conversation.id,
    ownerId,
    '3'.repeat(64),
    'clear-first-model',
    '2026-07-11T03:01:00.000Z',
    '2026-07-18T03:01:00.000Z'
  )
  const contextEntryJson = JSON.stringify({ summary: '待清空摘要' })
  database.prepare(`
    INSERT INTO chat_context_entries (
      conversation_id, checkpoint_id, sequence, kind, content_json, content_bytes,
      provenance, trust_level, created_at, expires_at
    ) VALUES (?, ?, 1, 'durable_memory', ?, ?, 'assistant', 'assistant_derived', ?, ?)
  `).run(
    conversation.id,
    checkpointId,
    contextEntryJson,
    Buffer.byteLength(contextEntryJson, 'utf8'),
    '2026-07-11T03:01:00.000Z',
    '2026-07-18T03:01:00.000Z'
  )
  database.prepare(`
    UPDATE chat_conversations
    SET context_revision = 7, active_checkpoint_id = ?, compacted_through_sequence = 2,
        context_state = 'compact_failed', active_context_tokens = 321,
        effective_context_limit_tokens = 4096, context_usage_estimated = 0,
        context_retry_at = '2026-07-12T04:00:00.000Z', context_attempt_count = 3,
        context_error_code = 'chat_context_test_failure', title_source_message_id = ?
    WHERE id = ?
  `).run(checkpointId, completedTurn.userMessage.id, conversation.id)

  assert.equal(countRows('chat_messages', conversation.id), 4)
  assert.equal(countRows('chat_message_idempotency', conversation.id), 2)
  assert.equal(countRows('chat_asset_references', conversation.id), 1)
  assert.equal(countRows('chat_context_checkpoints', conversation.id), 1)
  assert.equal(countRows('chat_context_entries', conversation.id), 1)
  assert.equal(countRows('chat_image_generations', conversation.id), 1)
  const storageBefore = storageByBucket(ownerId)
  const targetStorage = targetStorageByBucket(conversation.id, ownerId)
  const clearNow = '2026-07-12T00:00:00.000Z'
  const revisionBefore = await getChatConversation(client, conversation.id, ownerId)

  assert.equal(await clearChatConversation(client, {
    conversationId: conversation.id,
    systemAccountId: 'chat_clear_other_owner',
    now: clearNow
  }), undefined, '清空不得跨 owner 命中')
  assert.equal(countRows('chat_messages', conversation.id), 4, '跨 owner 请求不得产生副作用')

  const cleared = await clearChatConversation(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: clearNow
  })
  assert(cleared)
  assert.deepEqual({
    id: cleared.id,
    systemAccountId: cleared.systemAccountId,
    apiKeyId: cleared.apiKeyId,
    apiKeyNameSnapshot: cleared.apiKeyNameSnapshot,
    isPinned: cleared.isPinned,
    lastModel: cleared.lastModel,
    defaultImageModel: cleared.defaultImageModel
  }, {
    id: conversation.id,
    systemAccountId: ownerId,
    apiKeyId: 'chat_clear_key',
    apiKeyNameSnapshot: '清空测试 Key',
    isPinned: true,
    lastModel: 'clear-last-model',
    defaultImageModel: 'gpt-image-2'
  }, '清空必须保留对话壳身份、Key、置顶与最近模型')
  assert.equal(cleared.title, '新对话')
  assert.equal(cleared.userTurnCount, 0)
  assert.equal(cleared.activeTurnId, undefined)
  assert.equal(cleared.messageRevision, revisionBefore!.messageRevision + 1)
  assert.equal(cleared.lastMessageAt, clearNow)
  assert.equal(cleared.updatedAt, clearNow)

  const rawConversation = database.prepare('SELECT * FROM chat_conversations WHERE id = ?').get(conversation.id) as Record<string, unknown>
  assert.equal(Number(rawConversation.next_sequence_no), 1)
  assert.equal(Number(rawConversation.context_revision), 8)
  assert.equal(rawConversation.title_source_message_id, null)
  assert.equal(rawConversation.active_started_at, null)
  assert.equal(rawConversation.active_checkpoint_id, null)
  assert.equal(Number(rawConversation.compacted_through_sequence), 0)
  assert.equal(rawConversation.context_state, 'ready')
  assert.equal(rawConversation.active_context_tokens, null)
  assert.equal(rawConversation.effective_context_limit_tokens, null)
  assert.equal(Number(rawConversation.context_usage_estimated), 1)
  assert.equal(rawConversation.context_claim_id, null)
  assert.equal(rawConversation.context_claim_revision, null)
  assert.equal(rawConversation.context_claim_through_sequence, null)
  assert.equal(rawConversation.context_claimed_at, null)
  assert.equal(rawConversation.context_retry_at, null)
  assert.equal(Number(rawConversation.context_attempt_count), 0)
  assert.equal(rawConversation.context_error_code, null)
  assert.equal(Number(rawConversation.context_progress_sequence), 0)
  assert.equal(rawConversation.context_progress_earliest_expires_at, null)

  for (const table of [
    'chat_messages',
    'chat_message_idempotency',
    'chat_asset_references',
    'chat_context_checkpoints',
    'chat_context_entries',
    'chat_image_generations'
  ]) {
    assert.equal(countRows(table, conversation.id), 0, `${table} 必须被清空`)
  }
  assert.equal(countRows('chat_messages', retainedConversation.id), 2, '清空不得删除其他会话消息')
  assert.equal(await getChatConversation(client, conversation.id, ownerId) !== undefined, true, '清空不得删除会话壳')

  const asset = database.prepare(`
    SELECT expires_at, cleanup_status FROM chat_assets
    WHERE id = ? AND system_account_id = ? AND conversation_id = ?
  `).get(assetId, ownerId, conversation.id) as Record<string, unknown>
  assert.equal(asset.expires_at, clearNow, '资产必须进入现有过期清理队列')
  assert.equal(asset.cleanup_status, 'active', '请求线程不得直接删除资产')

  const expectedStorage = new Map(storageBefore)
  for (const [bucketDate, usage] of targetStorage) {
    const before = expectedStorage.get(bucketDate)
    assert(before, `容量窗口缺失 ${bucketDate}`)
    const contentBytes = before.contentBytes - usage.contentBytes
    const reservedBytes = before.reservedBytes - usage.reservedBytes
    assert(contentBytes >= 0 && reservedBytes >= 0, '清空容量扣减不得为负')
    if (contentBytes === 0 && reservedBytes === 0) expectedStorage.delete(bucketDate)
    else expectedStorage.set(bucketDate, { contentBytes, reservedBytes })
  }
  assert.deepEqual(storageByBucket(ownerId), expectedStorage, '清空必须只扣减目标会话的分桶正文与 reservation')
  assert.equal(staleReservedTurn.assistantMessage.status, 'streaming', '回归夹具必须覆盖 stale reservation')

  console.log('AI 问答清空会话存储事务回归通过')
} finally {
  database.close()
}

function countRows(table: string, conversationId: string): number {
  const allowedTables = new Set([
    'chat_messages',
    'chat_message_idempotency',
    'chat_asset_references',
    'chat_context_checkpoints',
    'chat_context_entries',
    'chat_image_generations'
  ])
  if (!allowedTables.has(table)) throw new Error(`不允许的回归表：${table}`)
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${table} WHERE conversation_id = ?`).get(conversationId) as { total?: unknown }
  return Number(row.total ?? 0)
}

function storageByBucket(systemAccountId: string): Map<string, { contentBytes: number; reservedBytes: number }> {
  const rows = database.prepare(`
    SELECT bucket_date, content_bytes, reserved_bytes
    FROM chat_user_storage_windows
    WHERE system_account_id = ?
    ORDER BY bucket_date ASC
  `).all(systemAccountId) as Array<Record<string, unknown>>
  return new Map(rows.map((row) => [String(row.bucket_date), {
    contentBytes: Number(row.content_bytes),
    reservedBytes: Number(row.reserved_bytes)
  }]))
}

function targetStorageByBucket(conversationId: string, systemAccountId: string): Map<string, { contentBytes: number; reservedBytes: number }> {
  const rows = database.prepare(`
    SELECT substr(created_at, 1, 10) AS bucket_date,
           SUM(content_bytes) AS content_bytes,
           SUM(storage_reserved_bytes) AS reserved_bytes
    FROM chat_messages
    WHERE conversation_id = ? AND system_account_id = ?
    GROUP BY substr(created_at, 1, 10)
    ORDER BY bucket_date ASC
  `).all(conversationId, systemAccountId) as Array<Record<string, unknown>>
  return new Map(rows.map((row) => [String(row.bucket_date), {
    contentBytes: Number(row.content_bytes),
    reservedBytes: Number(row.reserved_bytes)
  }]))
}
