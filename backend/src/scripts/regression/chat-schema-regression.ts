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
assert.ok(indexes.includes('idx_chat_messages_conversation_sequence'))
assert.ok(indexes.includes('idx_chat_messages_expiry'))
assert.ok(indexes.includes('idx_chat_idempotency_expiry'))

console.log('AI 问答 SQLite schema 回归通过')
