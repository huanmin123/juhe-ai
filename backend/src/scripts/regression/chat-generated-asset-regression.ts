import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import {
  commitChatAssetsToMessage,
  commitChatGeneratedAsset,
  hasValidChatAssetReference,
  insertChatAssetReference,
  removeChatAssetReferencesForMessage
} from '../../storage/chat-assets.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)

const now = '2026-07-18T00:00:00.000Z'
const expiresAt = '2026-07-21T00:00:00.000Z'
const conversationId = 'chat_generated_asset_conversation'
const assetId = 'chat_asset_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

database.prepare(`
  INSERT INTO chat_conversations (
    id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at
  ) VALUES (?, 'system_generated_asset', 'Generated Asset Key', ?, ?, ?)
`).run(conversationId, now, now, now)
database.prepare(`
  INSERT INTO chat_assets (
    id, system_account_id, conversation_id, source_kind,
    original_filename, original_mime_type, original_width, original_height,
    original_bytes, original_sha256,
    processed_mime_type, processed_width, processed_height, processed_bytes,
    processed_sha256, storage_key, processing_status, quota_bytes,
    preview_mime_type, preview_width, preview_height, preview_bytes, preview_sha256, preview_storage_key,
    turn_id, message_id, committed_at, created_at, updated_at, expires_at
  ) VALUES (
    ?, 'system_generated_asset', ?, 'assistant_generated',
    'generated.png', 'image/png', 64, 64,
    128, ?,
    'image/png', 64, 64, 128,
    ?, 'generated/asset.png', 'ready', 128,
    'image/webp', 64, 64, 64, ?, 'generated/asset-preview.webp',
    'turn_generated', 'message_generated', ?, ?, ?, ?
  )
`).run(assetId, conversationId, 'a'.repeat(64), 'b'.repeat(64), 'd'.repeat(64), now, now, now, expiresAt)

await insertChatAssetReference(client, {
  assetId,
  systemAccountId: 'system_generated_asset',
  conversationId,
  turnId: 'turn_generated',
  messageId: 'message_generated',
  referenceKind: 'assistant_output',
  contentOrder: 0,
  createdAt: now,
  expiresAt,
  now
})

assert.equal(await hasValidChatAssetReference(client, {
  assetId,
  systemAccountId: 'system_generated_asset',
  conversationId,
  now
}), true, '助手生成资产写入输出引用后必须存在有效引用')

database.prepare(`
  INSERT INTO chat_messages (
    id, conversation_id, system_account_id, turn_id, sequence_no, role, status,
    content_text, content_bytes, storage_reserved_bytes, model, created_at, expires_at
  ) VALUES ('message_generated_tx', ?, 'system_generated_asset', 'turn_generated_tx', 2, 'assistant', 'streaming', '', 0, 1, 'gpt-image-2', ?, ?)
`).run(conversationId, now, expiresAt)
const committed = await commitChatGeneratedAsset(client, {
  id: 'chat_asset_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  systemAccountId: 'system_generated_asset',
  conversationId,
  turnId: 'turn_generated_tx',
  messageId: 'message_generated_tx',
  contentOrder: 0,
  mimeType: 'image/png',
  width: 1024,
  height: 1024,
  bytes: 2 * 1024 * 1024,
  sha256: 'c'.repeat(64),
  storageKey: 'generated/large.png',
  previewMimeType: 'image/webp',
  previewWidth: 640,
  previewHeight: 640,
  previewBytes: 128 * 1024,
  previewSha256: 'd'.repeat(64),
  previewStorageKey: 'generated/large-preview.webp',
  now,
  retentionDays: 3,
  generation: {
    operation: 'generate', model: 'gpt-image-2', prompt: '生成大图', sourceAssetIds: [],
    size: '1024x1024', quality: 'auto', outputFormat: 'png'
  }
})
assert.equal(committed.sourceKind, 'assistant_generated')
assert.equal(committed.processedBytes, 2 * 1024 * 1024, '生成资产必须允许超过用户上传 1 MiB 的独立上限')
assert.equal(committed.previewMimeType, 'image/webp')
assert.equal(committed.previewBytes, 128 * 1024)
assert.equal(committed.previewStorageKey, 'generated/large-preview.webp')
const quota = database.prepare('SELECT quota_bytes FROM chat_assets WHERE id = ?').get(committed.id) as { quota_bytes: number }
assert.equal(quota.quota_bytes, 2 * 1024 * 1024 + 128 * 1024, '生成资产配额必须同时计算原图和 preview')
assert.equal(await hasValidChatAssetReference(client, {
  assetId: committed.id,
  systemAccountId: 'system_generated_asset',
  conversationId,
  now
}), true, '生成资产提交事务必须同步写入 assistant_output 引用')

database.prepare(`
  INSERT INTO chat_messages (
    id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id, role, status,
    content_text, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at
  ) VALUES ('message_reuse_user', ?, 'system_generated_asset', 'turn_reuse', 3, 'client_reuse', 'user', 'completed', '[图片]', 6, 0, 'gpt-5.6', ?, ?, ?)
`).run(conversationId, now, now, expiresAt)
await commitChatAssetsToMessage(client, {
  assetIds: [committed.id],
  systemAccountId: 'system_generated_asset',
  conversationId,
  messageId: 'message_reuse_user',
  now,
  retentionDays: 3
})
const origin = database.prepare('SELECT turn_id, message_id FROM chat_assets WHERE id = ?').get(committed.id) as { turn_id: string; message_id: string }
assert.equal(origin.turn_id, 'turn_generated_tx', '再次引用生成图片不能覆盖助手输出的来源轮次')
assert.equal(origin.message_id, 'message_generated_tx', '再次引用生成图片不能覆盖助手输出的来源消息')

await removeChatAssetReferencesForMessage(client, {
  systemAccountId: 'system_generated_asset',
  conversationId,
  messageId: 'message_generated_tx',
  now
})
assert.equal(await hasValidChatAssetReference(client, {
  assetId: committed.id,
  systemAccountId: 'system_generated_asset',
  conversationId,
  now
}), true, '删除助手输出引用后，后续用户输入引用必须继续保持资产可读')
await removeChatAssetReferencesForMessage(client, {
  systemAccountId: 'system_generated_asset',
  conversationId,
  messageId: 'message_reuse_user',
  now
})
assert.equal(await hasValidChatAssetReference(client, {
  assetId: committed.id,
  systemAccountId: 'system_generated_asset',
  conversationId,
  now
}), false, '删除最后一个有效消息引用后资产必须不再可读')

console.log('AI 问答生成资产引用回归通过')
