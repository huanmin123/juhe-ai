import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { Pool } from 'pg'
import { createClient } from 'redis'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { acceptChatTurn, cleanupChatRetention, completeChatTurn, createChatConversation, listChatContextMessages } from '../../storage/chat.repository.js'

const adminUrl = requiredEnv('JUHE_AI_TEST_POSTGRES_URL')
const redisUrl = requiredEnv('JUHE_AI_TEST_REDIS_URL')
const databaseName = `juhe_ai_chat_test_${randomUUID().replace(/-/g, '').slice(0, 12)}`
const adminPool = new Pool({ connectionString: adminUrl, max: 1 })
let targetPool: Pool | undefined
let redis: ReturnType<typeof createClient> | undefined

try {
  await adminPool.query(`CREATE DATABASE "${databaseName}"`)
  const targetUrl = new URL(adminUrl)
  targetUrl.pathname = `/${databaseName}`
  targetPool = new Pool({ connectionString: targetUrl.toString(), max: 4 })
  const client = createPostgresDatabaseClient(targetPool)
  const schema = await applyPostgresSchema(client)
  assert(schema.schemaCount >= 6)
  const conversation = await createChatConversation(client, { id: 'chat_pg_conv', systemAccountId: 'sys_pg', apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-12T00:00:00.000Z' })
  const turn = await acceptChatTurn(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', clientMessageId: 'pg_client_1', userContent: 'PG 测试', model: 'mock-model', now: '2026-07-12T00:01:00.000Z', storageQuotaBytes: 1024 * 1024 })
  await completeChatTurn(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', turnId: turn.turnId, assistantContent: 'PG 回答', finishReason: 'stop', traceId: 'trace_pg', now: '2026-07-12T00:02:00.000Z' })
  const context = await listChatContextMessages(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', limitTurns: 64, now: '2026-07-12T00:03:00.000Z' })
  assert.deepEqual(context.map((item) => item.content), ['PG 测试', 'PG 回答'])
  const expiredConversation = await createChatConversation(client, { id: 'chat_pg_expired', systemAccountId: 'sys_pg', apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-01T00:00:00.000Z' })
  const expiredTurn = await acceptChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', clientMessageId: 'pg_expired_1', userContent: '过期问题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: 1024 * 1024 })
  await completeChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', turnId: expiredTurn.turnId, assistantContent: '过期回答', finishReason: 'stop', traceId: 'trace_expired', now: '2026-07-01T00:02:00.000Z' })
  const cleanup = await cleanupChatRetention(client, { now: '2026-07-12T00:10:00.000Z', interruptedBefore: '2026-07-12T00:00:00.000Z', limit: 1000 })
  assert(cleanup.droppedPartitions >= 1, 'PostgreSQL 应直接删除完全过期的日分区')
  assert(cleanup.deletedConversations >= 1, '日分区删除后应收口空会话')
  const partitions = await targetPool.query("SELECT COUNT(*)::int AS total FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent JOIN pg_namespace n ON n.oid=p.relnamespace WHERE n.nspname='juhe_chat' AND p.relname='chat_messages'")
  assert(Number(partitions.rows[0]?.total) >= 2, '写入时应确保当天和下一天消息分区')

  redis = createClient({ url: redisUrl })
  await redis.connect()
  const redisKey = `juhe-ai:chat-smoke:${databaseName}`
  await redis.set(redisKey, 'ok', { EX: 30 })
  assert.equal(await redis.get(redisKey), 'ok')
  await redis.del(redisKey)
  console.log('AI 问答 PostgreSQL/Redis smoke 通过：独立数据库、juhe_chat schema、日分区、repository 和临时 Redis namespace 均正常')
} finally {
  if (redis?.isOpen) await redis.quit().catch(() => undefined)
  if (targetPool) await targetPool.end().catch(() => undefined)
  await adminPool.query(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, [databaseName]).catch(() => undefined)
  await adminPool.query(`DROP DATABASE IF EXISTS "${databaseName}"`).catch(() => undefined)
  await adminPool.end()
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`缺少 ${name}`)
  return value
}
