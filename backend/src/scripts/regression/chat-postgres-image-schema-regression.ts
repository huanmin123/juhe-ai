import assert from 'node:assert/strict'

import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

const statements = collectPostgresSchemaStatements()
const conversationSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_conversations\b/i.test(statement.sql))?.sql ?? ''
const lineageSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_image_generations\b/i.test(statement.sql))?.sql ?? ''

assert.match(conversationSql, /default_image_model text NOT NULL DEFAULT 'gpt-image-2'/)
assert.match(lineageSql, /asset_id text PRIMARY KEY/)
assert.match(lineageSql, /source_asset_ids_json text NOT NULL DEFAULT '\[\]'/)
assert.match(lineageSql, /FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE/)
assert.match(lineageSql, /FOREIGN KEY \(root_asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE/)
assert.match(lineageSql, /jsonb_typeof\(source_asset_ids_json::jsonb\) = 'array'/)

console.log('AI 问答 PostgreSQL 默认图像模型与谱系 SQL 回归通过')
