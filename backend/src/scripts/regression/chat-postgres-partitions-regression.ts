import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import {
  chatMessagePartitionBounds,
  chatMessagePartitionDateKeyFromIso,
  postgresChatMessagePartitionName
} from '../../storage/postgres-chat-message-partitions.js'
import { createSqliteDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import {
  acceptChatTurn,
  chatAssistantStorageReservationBytes,
  completeChatTurn,
  cleanupChatRetention,
  createChatConversation,
  getChatConversation,
  listChatMessages
} from '../../storage/chat.repository.js'
import { applyChatSchema } from '../../storage/schema.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

assert.equal(chatMessagePartitionDateKeyFromIso('2026-07-12T23:59:59.000Z'), '20260712')
assert.equal(chatMessagePartitionDateKeyFromIso('invalid'), undefined)
assert.equal(postgresChatMessagePartitionName('20260712'), 'chat_messages_20260712')
assert.deepEqual(chatMessagePartitionBounds('20260712'), {
  startDate: '2026-07-12',
  endDate: '2026-07-13'
})
assert.throws(() => postgresChatMessagePartitionName('2026-07-12'), /日期无效/)

const postgresSchemaStatements = collectPostgresSchemaStatements()
const assetReferencesCreateSql = postgresSchemaStatements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_asset_references\b/i.test(statement.sql))?.sql ?? ''
assert.match(assetReferencesCreateSql, /asset_id text NOT NULL[\s\S]+conversation_id text NOT NULL[\s\S]+turn_id text NOT NULL[\s\S]+message_id text NOT NULL/, 'PG schema 必须生成资产引用绑定字段')
assert.match(assetReferencesCreateSql, /reference_kind text NOT NULL[\s\S]+content_order integer NOT NULL[\s\S]+created_at text NOT NULL[\s\S]+expires_at text NOT NULL/, 'PG schema 必须保留引用类型、内容顺序和生命周期字段')
assert.match(assetReferencesCreateSql, /FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE/, 'PG 资产引用必须按会话绑定资产并随资产删除')
assert.doesNotMatch(assetReferencesCreateSql, /PARTITION BY/, '资产引用关系不应随消息日分区')

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
interface CapturedQuery { sql: string; params: readonly unknown[]; inTransaction: boolean }
const postgresContractQueries: CapturedQuery[] = []
const postgresContractClient = asPostgresContractClient(createSqliteDatabaseClient(database), postgresContractQueries)
const conversation = await createChatConversation(postgresContractClient, {
  id: 'chat_pg_contract_replace',
  systemAccountId: 'sys_pg_contract',
  apiKeyId: 'key_pg_contract',
  apiKeyNameSnapshot: 'PG Contract Key', maxConversationsPerUser: 1000,
  now: '2026-08-20T00:00:00.000Z'
})
const createLockIndex = postgresContractQueries.findIndex((item) => /pg_advisory_xact_lock/.test(item.sql))
const createCountIndex = postgresContractQueries.findIndex((item) => /SELECT COUNT\(\*\) AS total FROM\s+"chat_conversations"/.test(item.sql))
const createInsertIndex = postgresContractQueries.findIndex((item) => /INSERT INTO\s+"chat_conversations"/.test(item.sql))
assert(createLockIndex >= 0 && createLockIndex < createCountIndex && createCountIndex < createInsertIndex, 'PostgreSQL 会话创建必须在同一事务中先取得用户级 advisory lock，再计数并插入')
const original = await acceptChatTurn(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  clientMessageId: 'client_pg_original',
  userContent: 'PG 旧问题',
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-08-20T00:01:00.000Z',
  storageQuotaBytes: chatAssistantStorageReservationBytes + 4096, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  turnId: original.turnId,
  assistantContent: 'PG 旧回答',
  finishReason: 'stop',
  traceId: 'trace_pg_contract_original',
  now: '2026-08-20T00:02:00.000Z'
})
const replacement = await acceptChatTurn(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  clientMessageId: 'client_pg_replacement',
  userContent: 'PG 新问题',
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-08-20T00:03:00.000Z',
  storageQuotaBytes: chatAssistantStorageReservationBytes + 4096, retentionDays: 7, maxTurnsPerConversation: 1000,
  replaceTurnId: original.turnId
})
assert.equal(replacement.userMessage.sequenceNo, original.userMessage.sequenceNo)
assert.equal(replacement.assistantMessage.sequenceNo, original.assistantMessage.sequenceNo)
assert(postgresContractQueries.filter((item) => /pg_advisory_xact_lock/.test(item.sql)).length >= 3, 'PostgreSQL 创建、接受和替换轮次都必须获取同一用户级事务锁')
assert(postgresContractQueries.some((item) => /user_turn_count\s*=\s*user_turn_count\s*\+\s*1/.test(item.sql)), 'PostgreSQL 普通接受必须在会话 UPDATE 中原子增加用户轮次计数')
assert.deepEqual((await listChatMessages(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  limit: 20,
  now: '2026-08-20T00:04:00.000Z'
})).map((message) => [message.turnId, message.contentText]), [
  [replacement.turnId, 'PG 新问题'],
  [replacement.turnId, '']
])
const unboundedListQuery = [...postgresContractQueries].reverse().find((item) => /FROM\s+"chat_messages"/i.test(item.sql) && /expires_at\s*>\s*\?/i.test(item.sql))
assert(unboundedListQuery, 'PostgreSQL 契约必须捕获消息列表查询')
assert.doesNotMatch(unboundedListQuery.sql, /sequence_no\s*<\s*\?/i, '无 beforeSequenceNo 时不得绑定超出 PostgreSQL integer 的哨兵值')
assert.equal(unboundedListQuery.params.includes(Number.MAX_SAFE_INTEGER), false, '无游标列表不得向 PostgreSQL 绑定 Number.MAX_SAFE_INTEGER')
await listChatMessages(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  beforeSequenceNo: Number.MAX_SAFE_INTEGER,
  limit: 20,
  now: '2026-08-20T00:04:00.000Z'
})
const oversizedCursorQuery = [...postgresContractQueries].reverse().find((item) => /FROM\s+"chat_messages"/i.test(item.sql) && /expires_at\s*>\s*\?/i.test(item.sql))
assert(oversizedCursorQuery, 'PostgreSQL 契约必须捕获超范围游标查询')
assert.doesNotMatch(oversizedCursorQuery.sql, /sequence_no\s*<\s*\?/i, '超过 PostgreSQL int4 上限的游标应按无界查询处理')
assert.equal(oversizedCursorQuery.params.includes(Number.MAX_SAFE_INTEGER), false, 'repository 不得向 PostgreSQL 绑定超 int4 游标')

const cleanupQueryStart = postgresContractQueries.length
const revisionBeforePartitionCleanup = (await getChatConversation(postgresContractClient, conversation.id, 'sys_pg_contract'))?.messageRevision
const partitionCleanup = await cleanupChatRetention(postgresContractClient, {
  now: '2026-08-21T00:00:00.000Z', interruptedBefore: '2026-08-01T00:00:00.000Z', retentionDays: 7, limit: 100
})
assert.equal(partitionCleanup.droppedPartitions, 2)
assert.equal((await getChatConversation(postgresContractClient, conversation.id, 'sys_pg_contract'))?.messageRevision, Number(revisionBeforePartitionCleanup) + 1)
const cleanupQueries = postgresContractQueries.slice(cleanupQueryStart)
const affectedIndexes = cleanupQueries.flatMap((item, index) => /SELECT DISTINCT conversation_id, system_account_id[\s\S]+chat_messages_2026080[12]/.test(item.sql) ? [index] : [])
const revisionIndexes = cleanupQueries.flatMap((item, index) => /UPDATE\s+"chat_conversations"[\s\S]+message_revision = message_revision \+ 1/.test(item.sql) ? [index] : [])
const dropIndexes = cleanupQueries.flatMap((item, index) => /DROP TABLE IF EXISTS juhe_chat\."chat_messages_2026080[12]"/.test(item.sql) ? [index] : [])
assert.equal(affectedIndexes.length, 2, 'PG 分区清理必须逐分区收集受影响会话')
assert.equal(revisionIndexes.length, 1, '同一 cleanup 中同一会话跨多个分区只能推进一次 revision')
assert.equal(dropIndexes.length, 2, 'PG 分区清理必须 DROP 每个已过期合法分区')
assert(Math.max(...affectedIndexes) < revisionIndexes[0] && revisionIndexes[0] < Math.min(...dropIndexes), 'PG 分区清理必须先收集全部会话、推进 revision，再 DROP')
const revisionIndex = revisionIndexes[0]
assert(affectedIndexes.every((index) => cleanupQueries[index]?.inTransaction), 'PG 受影响会话查询必须位于 cleanup transaction')
assert.equal(cleanupQueries[revisionIndex]?.inTransaction, true, 'PG revision UPDATE 必须位于 cleanup transaction')
assert(dropIndexes.every((index) => cleanupQueries[index]?.inTransaction), 'PG DROP 必须位于 cleanup transaction')

console.log('AI 问答 PostgreSQL 日分区回归通过')

function asPostgresContractClient(sqliteClient: DatabaseClient, queries: CapturedQuery[]): DatabaseClient {
  const wrap = (client: DatabaseClient, inTransaction = false): DatabaseClient => ({
    driver: 'postgres',
    dialect: client.dialect,
    query: (sql, params = []) => {
      queries.push({ sql, params, inTransaction })
      if (/FROM pg_inherits/.test(sql)) return Promise.resolve([
        { partition_name: 'chat_messages_20260801' },
        { partition_name: 'chat_messages_20260802' },
        { partition_name: 'chat_messages_20260820' },
        { partition_name: 'chat_messages_20260801_unsafe' }
      ] as never)
      if (/SELECT DISTINCT conversation_id, system_account_id[\s\S]+chat_messages_2026080[12]/.test(sql)) {
        return Promise.resolve([{ conversation_id: 'chat_pg_contract_replace', system_account_id: 'sys_pg_contract' }] as never)
      }
      return client.query(stripPostgresOnlySql(sql), params)
    },
    one: (sql, params = []) => {
      queries.push({ sql, params, inTransaction })
      return client.one(stripPostgresOnlySql(sql), params)
    },
    execute: (sql, params) => {
      queries.push({ sql, params: params ?? [], inTransaction })
      if (/PARTITION OF juhe_chat\.chat_messages/.test(sql)) return Promise.resolve({ changes: 0 })
      if (/DROP TABLE IF EXISTS juhe_chat\./.test(sql)) return Promise.resolve({ changes: 0 })
      if (/pg_advisory_xact_lock/.test(sql)) return Promise.resolve({ changes: 1 })
      return client.execute(stripPostgresOnlySql(sql), params)
    },
    transaction: (operation) => client.transaction((tx) => operation(wrap(tx, true)))
  })
  return wrap(sqliteClient)
}
function stripPostgresOnlySql(sql: string): string {
  return sql
    .replace(/\s+FOR UPDATE\s*$/i, '')
    .replace(/"chat_user_storage_windows"\.content_bytes/g, 'content_bytes')
}
