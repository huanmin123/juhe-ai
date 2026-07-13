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

const conversationColumns = database.prepare('PRAGMA table_info(chat_conversations)').all() as Array<{ name?: string; dflt_value?: unknown }>
assert.ok(conversationColumns.some((column) => column.name === 'active_checkpoint_id'))
assert.equal(String(conversationColumns.find((column) => column.name === 'context_usage_estimated')?.dflt_value), '1')

console.log('AI 问答 SQLite schema 回归通过')
