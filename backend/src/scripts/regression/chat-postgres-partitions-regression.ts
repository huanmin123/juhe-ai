import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import {
  chatMessagePartitionBounds,
  chatMessagePartitionDateKeyFromIso,
  postgresChatMessagePartitionName
} from '../../storage/postgres-chat-message-partitions.js'
import { createSqliteDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { acceptChatTurn, completeChatTurn, createChatConversation, listChatMessages } from '../../storage/chat.repository.js'
import { applyChatSchema } from '../../storage/schema.js'

assert.equal(chatMessagePartitionDateKeyFromIso('2026-07-12T23:59:59.000Z'), '20260712')
assert.equal(chatMessagePartitionDateKeyFromIso('invalid'), undefined)
assert.equal(postgresChatMessagePartitionName('20260712'), 'chat_messages_20260712')
assert.deepEqual(chatMessagePartitionBounds('20260712'), {
  startDate: '2026-07-12',
  endDate: '2026-07-13'
})
assert.throws(() => postgresChatMessagePartitionName('2026-07-12'), /日期无效/)

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const postgresContractQueries: Array<{ sql: string; params: readonly unknown[] }> = []
const postgresContractClient = asPostgresContractClient(createSqliteDatabaseClient(database), postgresContractQueries)
const conversation = await createChatConversation(postgresContractClient, {
  id: 'chat_pg_contract_replace',
  systemAccountId: 'sys_pg_contract',
  apiKeyId: 'key_pg_contract',
  apiKeyNameSnapshot: 'PG Contract Key',
  now: '2026-08-20T00:00:00.000Z'
})
const original = await acceptChatTurn(postgresContractClient, {
  conversationId: conversation.id,
  systemAccountId: 'sys_pg_contract',
  clientMessageId: 'client_pg_original',
  userContent: 'PG 旧问题',
  contentBlocks: [{ type: 'input_text' }],
  model: 'mock-model',
  now: '2026-08-20T00:01:00.000Z',
  storageQuotaBytes: 4096
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
  storageQuotaBytes: 4096,
  replaceTurnId: original.turnId
})
assert.equal(replacement.userMessage.sequenceNo, original.userMessage.sequenceNo)
assert.equal(replacement.assistantMessage.sequenceNo, original.assistantMessage.sequenceNo)
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

console.log('AI 问答 PostgreSQL 日分区回归通过')

function asPostgresContractClient(sqliteClient: DatabaseClient, queries: Array<{ sql: string; params: readonly unknown[] }>): DatabaseClient {
  const wrap = (client: DatabaseClient): DatabaseClient => ({
    driver: 'postgres',
    dialect: client.dialect,
    query: (sql, params = []) => {
      queries.push({ sql, params })
      return client.query(stripPostgresOnlySql(sql), params)
    },
    one: (sql, params) => client.one(stripPostgresOnlySql(sql), params),
    execute: (sql, params) => {
      if (/PARTITION OF juhe_chat\.chat_messages/.test(sql)) return Promise.resolve({ changes: 0 })
      return client.execute(stripPostgresOnlySql(sql), params)
    },
    transaction: (operation) => client.transaction((tx) => operation(wrap(tx)))
  })
  return wrap(sqliteClient)
}

function stripPostgresOnlySql(sql: string): string {
  return sql
    .replace(/\s+FOR UPDATE\s*$/i, '')
    .replace(/"chat_user_storage_windows"\.content_bytes/g, 'content_bytes')
}
