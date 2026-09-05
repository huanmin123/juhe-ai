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
  'chat_asset_references',
  'chat_assets',
  'chat_context_checkpoints',
  'chat_context_entries',
  'chat_conversations',
  'chat_image_generations',
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
assert.ok(indexes.includes('idx_chat_asset_references_message'))
assert.ok(indexes.includes('idx_chat_asset_references_asset_valid'))
assert.ok(indexes.includes('idx_chat_asset_references_cleanup'))
assert.ok(indexes.includes('idx_chat_assets_cleanup'))

const messageColumns = database.prepare('PRAGMA table_info(chat_messages)').all() as Array<{ name?: string; dflt_value?: unknown }>
const contentBlocksColumn = messageColumns.find((column) => column.name === 'content_blocks_json')
assert.ok(contentBlocksColumn, '聊天消息必须保存结构化内容块')
assert.equal(String(contentBlocksColumn?.dflt_value), "'[]'")
assert.equal(String(messageColumns.find((column) => column.name === 'storage_reserved_bytes')?.dflt_value), '0', 'streaming 助手消息必须有持久化 reservation 字段')

const storageWindowColumns = database.prepare('PRAGMA table_info(chat_user_storage_windows)').all() as Array<{ name?: string; dflt_value?: unknown }>
assert.equal(String(storageWindowColumns.find((column) => column.name === 'reserved_bytes')?.dflt_value), '0', '用户容量日桶必须分开统计实际字节和活动预留')

const conversationColumns = database.prepare('PRAGMA table_info(chat_conversations)').all() as Array<{ name?: string; dflt_value?: unknown; notnull?: number }>
assert.equal(String(conversationColumns.find((column) => column.name === 'user_turn_count')?.dflt_value), '0', '会话必须持久化非负用户轮次计数')
assert.equal(String(conversationColumns.find((column) => column.name === 'message_revision')?.dflt_value), '0', '会话必须持久化可见消息 revision')
assert.equal(String(conversationColumns.find((column) => column.name === 'default_image_model')?.dflt_value), "'gpt-image-2'", '会话必须持久化默认图像模型')
assert.equal(conversationColumns.find((column) => column.name === 'default_image_model')?.notnull, 1, '默认图像模型不得为空')
database.prepare(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at) VALUES ('schema_turn_count', 'owner', 'Key', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z')`).run()
assert.throws(() => database.prepare(`UPDATE chat_conversations SET user_turn_count = -1 WHERE id = 'schema_turn_count'`).run(), /CHECK constraint failed/, '用户轮次计数不得写入负数')
assert.throws(() => database.prepare(`UPDATE chat_conversations SET message_revision = -1 WHERE id = 'schema_turn_count'`).run(), /CHECK constraint failed/, '可见消息 revision 不得写入负数')
assert.ok(conversationColumns.some((column) => column.name === 'active_checkpoint_id'))
assert.equal(String(conversationColumns.find((column) => column.name === 'context_usage_estimated')?.dflt_value), '1')
const assetColumns = database.prepare('PRAGMA table_info(chat_assets)').all() as Array<{ name?: string; dflt_value?: unknown; notnull?: number }>
assert.equal(String(assetColumns.find((column) => column.name === 'observation_revision')?.dflt_value), '0')
assert.ok(assetColumns.some((column) => column.name === 'quota_bytes'))
assert.equal(assetColumns.find((column) => column.name === 'source_kind')?.notnull, 1, '图片资产来源类型必须非空')
assert.equal(String(assetColumns.find((column) => column.name === 'source_kind')?.dflt_value), "'user_upload'", '现有上传 INSERT 省略来源类型时必须默认为用户上传')

const assetCreateSql = String((database.prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chat_assets'").get() as { sql?: unknown } | undefined)?.sql ?? '')
assert.match(assetCreateSql, /source_kind\s+TEXT\s+NOT NULL\s+DEFAULT\s+'user_upload'/i, '图片资产必须持久化带上传默认值的来源类型')
assert.match(assetCreateSql, /source_kind IN \('user_upload', 'assistant_generated'\)/, '图片资产来源必须限制为用户上传或助手生成')
assert.match(assetCreateSql, /processed_mime_type IN \('image\/jpeg', 'image\/png', 'image\/webp'\)/, '处理后图片必须允许 JPEG、PNG 和 WebP')
assert.match(assetCreateSql, /UNIQUE \(id, conversation_id\)/, '图片资产必须提供会话范围复合候选键')

const assetReferenceColumns = database.prepare('PRAGMA table_info(chat_asset_references)').all() as Array<{ name?: string; notnull?: number }>
assert.deepEqual(assetReferenceColumns.map((column) => column.name), [
  'asset_id',
  'conversation_id',
  'turn_id',
  'message_id',
  'reference_kind',
  'content_order',
  'created_at',
  'expires_at'
])
assert.equal(assetReferenceColumns.every((column) => column.notnull === 1), true, '资产引用字段必须全部非空')
const assetReferenceCreateSql = String((database.prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chat_asset_references'").get() as { sql?: unknown } | undefined)?.sql ?? '')
assert.match(assetReferenceCreateSql, /UNIQUE \(message_id, content_order\)/, '同一消息的内容顺序必须唯一')
assert.match(assetReferenceCreateSql, /reference_kind IN \('user_input', 'assistant_output'\)/, '资产引用类型必须限制为用户输入或助手输出')

const imageGenerationColumns = database.prepare('PRAGMA table_info(chat_image_generations)').all() as Array<{ name?: string; notnull?: number; pk?: number }>
assert.deepEqual(imageGenerationColumns.map((column) => column.name), [
  'asset_id',
  'conversation_id',
  'system_account_id',
  'operation',
  'model',
  'prompt',
  'source_asset_ids_json',
  'root_asset_id',
  'size',
  'quality',
  'output_format',
  'created_at',
  'expires_at'
])
assert.equal(imageGenerationColumns.find((column) => column.name === 'asset_id')?.pk, 1, '每个生成资产只能有一条图像谱系记录')
assert.equal(imageGenerationColumns.every((column) => column.pk === 1 || column.notnull === 1), true, '图像谱系字段必须全部非空')
const imageGenerationCreateSql = String((database.prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chat_image_generations'").get() as { sql?: unknown } | undefined)?.sql ?? '')
assert.match(imageGenerationCreateSql, /FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE/, '图像谱系输出资产必须属于当前会话')
assert.match(imageGenerationCreateSql, /FOREIGN KEY \(root_asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE/, '图像谱系根资产必须属于当前会话，整会话清理时允许级联释放谱系')
assert.match(imageGenerationCreateSql, /operation IN \('generate', 'edit'\)/, '图像谱系操作必须限制为生成或编辑')
assert.match(imageGenerationCreateSql, /json_valid\(source_asset_ids_json\)[\s\S]+json_type\(source_asset_ids_json\) = 'array'/, '图像谱系来源资产必须保存为 JSON 数组')

database.prepare(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at) VALUES ('schema_asset_other_conversation', 'owner', 'Key', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z', '2026-07-15T00:00:00.000Z')`).run()
const insertAssetWithoutSourceKind = database.prepare(`
  INSERT INTO chat_assets (
    id, system_account_id, conversation_id, original_filename, original_mime_type,
    original_bytes, original_sha256, quota_bytes, created_at, updated_at, expires_at
  ) VALUES (?, 'owner', 'schema_turn_count', 'image.png', 'image/png', 128, ?, 128, ?, ?, ?)
`)
const assetTimestamp = '2026-07-15T00:00:00.000Z'
insertAssetWithoutSourceKind.run('schema_asset_default_source', 'a'.repeat(64), assetTimestamp, assetTimestamp, '2026-07-18T00:00:00.000Z')
assert.equal(
  (database.prepare("SELECT source_kind FROM chat_assets WHERE id = 'schema_asset_default_source'").get() as { source_kind?: unknown } | undefined)?.source_kind,
  'user_upload',
  '省略 source_kind 的现有上传 INSERT 必须继续成功'
)
assert.throws(
  () => database.prepare(`
    INSERT INTO chat_assets (
      id, system_account_id, conversation_id, source_kind, original_filename, original_mime_type,
      original_bytes, original_sha256, quota_bytes, created_at, updated_at, expires_at
    ) VALUES ('schema_asset_invalid_source', 'owner', 'schema_turn_count', 'external', 'image.png', 'image/png', 128, ?, 128, ?, ?, ?)
  `).run('b'.repeat(64), assetTimestamp, assetTimestamp, '2026-07-18T00:00:00.000Z'),
  /CHECK constraint failed/,
  '不得写入未知图片资产来源'
)
database.prepare("UPDATE chat_assets SET processed_mime_type = 'image/jpeg' WHERE id = 'schema_asset_default_source'").run()
database.prepare("UPDATE chat_assets SET processed_mime_type = 'image/png' WHERE id = 'schema_asset_default_source'").run()
database.prepare("UPDATE chat_assets SET processed_mime_type = 'image/webp' WHERE id = 'schema_asset_default_source'").run()
assert.throws(
  () => database.prepare("UPDATE chat_assets SET processed_mime_type = 'image/gif' WHERE id = 'schema_asset_default_source'").run(),
  /CHECK constraint failed/,
  '不得写入白名单外的处理后图片 MIME'
)

const insertAssetReference = database.prepare(`
  INSERT INTO chat_asset_references (
    asset_id, conversation_id, turn_id, message_id, reference_kind, content_order, created_at, expires_at
  ) VALUES ('schema_asset_default_source', ?, ?, ?, ?, ?, ?, ?)
`)
insertAssetReference.run('schema_turn_count', 'turn_a', 'message_a', 'user_input', 0, assetTimestamp, '2026-07-18T00:00:00.000Z')
assert.throws(
  () => insertAssetReference.run('schema_turn_count', 'turn_a', 'message_negative_order', 'user_input', -1, assetTimestamp, '2026-07-18T00:00:00.000Z'),
  /CHECK constraint failed/,
  '资产引用内容顺序不得为负数'
)
assert.throws(
  () => insertAssetReference.run('schema_turn_count', 'turn_a', 'message_a', 'user_input', 0, assetTimestamp, '2026-07-18T00:00:00.000Z'),
  /UNIQUE constraint failed/,
  '同一消息内容顺序不得重复绑定资产'
)
assert.throws(
  () => insertAssetReference.run('schema_asset_other_conversation', 'turn_b', 'message_b', 'user_input', 0, assetTimestamp, '2026-07-18T00:00:00.000Z'),
  /FOREIGN KEY constraint failed/,
  '不得把其他会话的资产绑定到当前消息'
)
database.prepare("DELETE FROM chat_assets WHERE id = 'schema_asset_default_source'").run()
assert.equal(Number((database.prepare("SELECT COUNT(*) AS total FROM chat_asset_references WHERE asset_id = 'schema_asset_default_source'").get() as { total?: unknown } | undefined)?.total ?? -1), 0, '删除父资产必须级联删除消息引用')

console.log('AI 问答 SQLite schema 回归通过')
