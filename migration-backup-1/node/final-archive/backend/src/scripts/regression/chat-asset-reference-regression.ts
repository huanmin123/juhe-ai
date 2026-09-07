import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import {
  hasValidChatAssetReference,
  insertChatAssetReference,
  listActiveChatAssetReferences,
  removeChatAssetReferencesForMessage
} from '../../storage/chat-assets.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { clearChatConversation, getChatConversation } from '../../storage/chat.repository.js'
import { applyChatSchema } from '../../storage/schema.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)

const ownerId = 'system_asset_reference_owner'
const otherOwnerId = 'system_asset_reference_other'
const conversationId = 'chat_asset_reference_conversation'
const otherConversationId = 'chat_asset_reference_other_conversation'
const messageId = 'chat_asset_reference_message'
const assetId = 'chat_asset_11111111111111111111111111111111'
const expiredAssetId = 'chat_asset_22222222222222222222222222222222'
const pendingAssetId = 'chat_asset_33333333333333333333333333333333'
const now = '2026-07-18T00:00:00.000Z'
const expiresAt = '2026-07-21T00:00:00.000Z'

for (const [id, systemAccountId] of [
  [conversationId, ownerId],
  [otherConversationId, otherOwnerId]
]) {
  database.prepare(`
    INSERT INTO chat_conversations (
      id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at
    ) VALUES (?, ?, 'Asset Reference Key', ?, ?, ?)
  `).run(id, systemAccountId, now, now, now)
}

const insertAsset = database.prepare(`
  INSERT INTO chat_assets (
    id, system_account_id, conversation_id, source_kind,
    original_filename, original_mime_type, original_bytes, original_sha256,
    processed_mime_type, processed_width, processed_height, processed_bytes,
    processed_sha256, storage_key, preview_mime_type, preview_width, preview_height,
    preview_bytes, preview_sha256, preview_storage_key,
    quota_bytes, processing_status, cleanup_status, cleanup_attempt_count,
    created_at, updated_at, expires_at
  ) VALUES (?, ?, ?, 'assistant_generated', 'generated.png', 'image/png', 128, ?,
    'image/png', 64, 64, 128, ?, 'generated/asset.png', 'image/webp', 32, 32,
    64, ?, 'generated/asset-preview.webp', 192, ?, 'active', 0, ?, ?, ?)
`)
insertAsset.run(assetId, ownerId, conversationId, 'a'.repeat(64), 'd'.repeat(64), 'p'.repeat(64), 'ready', now, now, expiresAt)
insertAsset.run(expiredAssetId, ownerId, conversationId, 'b'.repeat(64), 'e'.repeat(64), 'q'.repeat(64), 'ready', now, now, '2026-07-17T00:00:00.000Z')
insertAsset.run(pendingAssetId, ownerId, conversationId, 'c'.repeat(64), 'f'.repeat(64), 'r'.repeat(64), 'pending', now, now, expiresAt)

const inserted = await insertChatAssetReference(client, {
  assetId,
  systemAccountId: ownerId,
  conversationId,
  turnId: 'chat_asset_reference_turn',
  messageId,
  referenceKind: 'assistant_output',
  contentOrder: 1,
  createdAt: now,
  expiresAt,
  now
})
assert.equal(inserted?.assetId, assetId)
assert.equal(await hasValidChatAssetReference(client, {
  assetId,
  systemAccountId: ownerId,
  conversationId,
  now
}), true)

assert.equal(await insertChatAssetReference(client, {
  assetId,
  systemAccountId: otherOwnerId,
  conversationId,
  turnId: 'chat_asset_reference_turn',
  messageId,
  referenceKind: 'assistant_output',
  contentOrder: 2,
  createdAt: now,
  expiresAt,
  now
}), undefined, '其他用户不能写入资产引用')

assert.equal(await insertChatAssetReference(client, {
  assetId: expiredAssetId,
  systemAccountId: ownerId,
  conversationId,
  turnId: 'chat_asset_reference_turn',
  messageId,
  referenceKind: 'assistant_output',
  contentOrder: 2,
  createdAt: now,
  expiresAt,
  now
}), undefined, '过期资产不能写入引用')

assert.equal(await insertChatAssetReference(client, {
  assetId: pendingAssetId,
  systemAccountId: ownerId,
  conversationId,
  turnId: 'chat_asset_reference_turn',
  messageId,
  referenceKind: 'assistant_output',
  contentOrder: 2,
  createdAt: now,
  expiresAt,
  now
}), undefined, '未处理完成的资产不能写入引用')

assert.equal(await hasValidChatAssetReference(client, {
  assetId,
  systemAccountId: otherOwnerId,
  conversationId,
  now
}), false, '其他用户不能验证资产引用')

assert.deepEqual(await listActiveChatAssetReferences(client, {
  systemAccountId: ownerId,
  conversationId,
  messageId,
  now
}), [inserted])
assert.deepEqual(await listActiveChatAssetReferences(client, {
  systemAccountId: otherOwnerId,
  conversationId,
  messageId,
  now
}), [], '其他用户不能读取资产引用')

assert.equal(await removeChatAssetReferencesForMessage(client, {
  systemAccountId: otherOwnerId,
  conversationId,
  messageId,
  now
}), 0, '其他用户不能删除资产引用')
assert.equal(await removeChatAssetReferencesForMessage(client, {
  systemAccountId: ownerId,
  conversationId,
  messageId,
  now
}), 1)
const reinserted = await insertChatAssetReference(client, {
  assetId,
  systemAccountId: ownerId,
  conversationId,
  turnId: 'chat_asset_reference_turn',
  messageId,
  referenceKind: 'assistant_output',
  contentOrder: 1,
  createdAt: now,
  expiresAt,
  now
})
assert.ok(reinserted)
const clearNow = '2026-07-18T00:01:00.000Z'
assert.ok(await clearChatConversation(client, { conversationId, systemAccountId: ownerId, now: clearNow }))
assert.deepEqual(await listActiveChatAssetReferences(client, {
  systemAccountId: ownerId,
  conversationId,
  messageId,
  now: clearNow
}), [], '清空会话必须删除资产引用')
assert.equal((database.prepare('SELECT expires_at FROM chat_assets WHERE id = ?').get(assetId) as { expires_at?: unknown }).expires_at, clearNow, '清空后资产本体应保留但进入过期队列')
assert.equal((await getChatConversation(client, conversationId, ownerId))?.messageRevision, 1, '删除引用后必须推进会话 revision')

console.log('聊天资产引用 repository 回归通过')
