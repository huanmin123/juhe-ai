import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { applyChatSchema } from '../../storage/schema.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)

const tables = database.prepare(`
  SELECT name
  FROM sqlite_master
  WHERE type = 'table' AND name LIKE 'chat_%'
  ORDER BY name ASC
`).all().map((row) => String((row as { name?: string }).name ?? ''))

assert.deepEqual(tables, [
  'chat_assets',
  'chat_context_checkpoints',
  'chat_context_entries',
  'chat_conversations',
  'chat_message_idempotency',
  'chat_messages',
  'chat_user_asset_usage',
  'chat_user_storage_windows'
])

const indexes = database.prepare(`
  SELECT name
  FROM sqlite_master
  WHERE type = 'index' AND name LIKE 'idx_chat_%'
  ORDER BY name ASC
`).all().map((row) => String((row as { name?: string }).name ?? ''))

assert.ok(indexes.includes('idx_chat_conversations_owner_recent'))
assert.ok(indexes.includes('idx_chat_conversations_owner_pinned_recent'))
assert.ok(indexes.includes('idx_chat_messages_conversation_sequence'))
assert.ok(indexes.includes('idx_chat_messages_expiry'))
assert.ok(indexes.includes('idx_chat_idempotency_expiry'))
assert.ok(indexes.includes('idx_chat_messages_compaction_source'))
assert.ok(indexes.includes('idx_chat_context_checkpoints_cleanup'))
assert.ok(indexes.includes('idx_chat_assets_cleanup'))

const messageColumns = database.prepare('PRAGMA table_info(chat_messages)').all() as Array<{ name?: string; dflt_value?: unknown }>
const contentBlocksColumn = messageColumns.find((column) => column.name === 'content_blocks_json')
assert.ok(contentBlocksColumn, '聊天消息必须保存结构化内容块')
assert.equal(String(contentBlocksColumn?.dflt_value), "'[]'")
assert.equal(String(messageColumns.find((column) => column.name === 'storage_reserved_bytes')?.dflt_value), '0', 'streaming 助手消息必须有持久化 reservation 字段')

const storageWindowColumns = database.prepare('PRAGMA table_info(chat_user_storage_windows)').all() as Array<{ name?: string; dflt_value?: unknown }>
assert.equal(String(storageWindowColumns.find((column) => column.name === 'reserved_bytes')?.dflt_value), '0', '用户容量日桶必须分开统计实际字节和活动预留')

const conversationColumns = database.prepare('PRAGMA table_info(chat_conversations)').all() as Array<{ name?: string; dflt_value?: unknown }>
assert.equal(String(conversationColumns.find((column) => column.name === 'user_turn_count')?.dflt_value), '0', '会话必须持久化非负用户轮次计数')
assert.equal(String(conversationColumns.find((column) => column.name === 'message_revision')?.dflt_value), '0', '会话必须持久化可见消息 revision')
database.prepare(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at) VALUES ('schema_turn_count', 'owner', 'Key', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z')`).run()
assert.throws(() => database.prepare(`UPDATE chat_conversations SET user_turn_count = -1 WHERE id = 'schema_turn_count'`).run(), /CHECK constraint failed/, '用户轮次计数不得写入负数')
assert.throws(() => database.prepare(`UPDATE chat_conversations SET message_revision = -1 WHERE id = 'schema_turn_count'`).run(), /CHECK constraint failed/, '可见消息 revision 不得写入负数')
assert.ok(conversationColumns.some((column) => column.name === 'active_checkpoint_id'))
assert.equal(String(conversationColumns.find((column) => column.name === 'context_usage_estimated')?.dflt_value), '1')
const assetColumns = database.prepare('PRAGMA table_info(chat_assets)').all() as Array<{ name?: string; dflt_value?: unknown }>
assert.equal(String(assetColumns.find((column) => column.name === 'observation_revision')?.dflt_value), '0')
assert.ok(assetColumns.some((column) => column.name === 'quota_bytes'))

console.log('AI 问答 SQLite schema 回归通过')
