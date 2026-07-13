import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { Pool } from 'pg'
import { createClient } from 'redis'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { acceptChatTurn, ChatConflictError, cleanupChatRetention, completeChatTurn, createChatConversation, listChatContextMessages, listChatConversations, listChatMessages, updateChatConversation } from '../../storage/chat.repository.js'
import { claimExpiredChatAssetsForCleanup, completeChatAssetDeletion, completeChatAssetProcessing, createChatAsset, getChatAsset, setChatAssetObservation } from '../../storage/chat-assets.repository.js'
import { claimChatContextCompaction, cleanupExpiredChatContextCheckpoints, installChatContextCheckpoint, loadChatCompactionSourcePage, loadChatModelContext, recordChatContextCompactionProgress, requestChatContextCompaction } from '../../storage/chat-context.repository.js'
import { runChatSmokeWithCleanup } from './chat-smoke-cleanup.js'

const adminUrl = requiredEnv('JUHE_AI_TEST_POSTGRES_URL')
const redisUrl = requiredEnv('JUHE_AI_TEST_REDIS_URL')
const databaseName = `juhe_ai_chat_test_${randomUUID().replace(/-/g, '').slice(0, 12)}`
const adminPool = new Pool({ connectionString: adminUrl, max: 1 })
let targetPool: Pool | undefined
let redis: ReturnType<typeof createClient> | undefined

await runChatSmokeWithCleanup({
  run: async () => {
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
  const replacement = await acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: 'sys_pg',
    clientMessageId: 'pg_client_replace',
    userContent: 'PG 修正后的问题',
    contentBlocks: [{ type: 'input_text' }],
    model: 'mock-model',
    now: '2026-07-12T00:04:00.000Z',
    storageQuotaBytes: 1024 * 1024,
    replaceTurnId: turn.turnId
  })
  assert.equal(replacement.userMessage.sequenceNo, turn.userMessage.sequenceNo)
  assert.equal(replacement.assistantMessage.sequenceNo, turn.assistantMessage.sequenceNo)
  await completeChatTurn(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', turnId: replacement.turnId, assistantContent: 'PG 修正后的回答', finishReason: 'stop', traceId: 'trace_pg_replace', now: '2026-07-12T00:05:00.000Z' })
  assert.deepEqual((await listChatMessages(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', limit: 20, now: '2026-07-12T00:06:00.000Z' })).map((item) => item.contentText), ['PG 修正后的问题', 'PG 修正后的回答'])
  const expiredConversation = await createChatConversation(client, { id: 'chat_pg_expired', systemAccountId: 'sys_pg', apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-01T00:00:00.000Z' })
  const expiredTurn = await acceptChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', clientMessageId: 'pg_expired_1', userContent: '过期问题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: 1024 * 1024 })
  await completeChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', turnId: expiredTurn.turnId, assistantContent: '过期回答', finishReason: 'stop', traceId: 'trace_expired', now: '2026-07-01T00:02:00.000Z' })
  const cleanup = await cleanupChatRetention(client, { now: '2026-07-12T00:10:00.000Z', interruptedBefore: '2026-07-12T00:00:00.000Z', limit: 1000 })
  assert(cleanup.droppedPartitions >= 1, 'PostgreSQL 应直接删除完全过期的日分区')
  assert(cleanup.deletedConversations >= 1, '日分区删除后应收口空会话')
  const partitions = await targetPool.query("SELECT COUNT(*)::int AS total FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent JOIN pg_namespace n ON n.oid=p.relnamespace WHERE n.nspname='juhe_chat' AND p.relname='chat_messages'")
  assert(Number(partitions.rows[0]?.total) >= 2, '写入时应确保当天和下一天消息分区')

  const quotaAccountId = 'sys_pg_quota_race'
  const [quotaConversationA, quotaConversationB] = await Promise.all([
    createChatConversation(client, { id: 'chat_pg_quota_a', systemAccountId: quotaAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-12T00:20:00.000Z' }),
    createChatConversation(client, { id: 'chat_pg_quota_b', systemAccountId: quotaAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-12T00:20:00.000Z' })
  ])
  const quotaContent = 'x'.repeat(1000)
  const quotaBytes = 2500
  const quotaResults = await Promise.allSettled([
    acceptChatTurn(client, { conversationId: quotaConversationA.id, systemAccountId: quotaAccountId, clientMessageId: 'pg_quota_a', userContent: quotaContent, model: 'mock-model', now: '2026-07-12T00:21:00.000Z', storageQuotaBytes: quotaBytes }),
    acceptChatTurn(client, { conversationId: quotaConversationB.id, systemAccountId: quotaAccountId, clientMessageId: 'pg_quota_b', userContent: quotaContent, model: 'mock-model', now: '2026-07-12T00:21:00.000Z', storageQuotaBytes: quotaBytes })
  ])
  assert.equal(quotaResults.filter((result) => result.status === 'fulfilled').length, 1, `同一用户跨会话并发提交只能有一个通过存储配额：${quotaResults.map((result) => result.status === 'rejected' ? String(result.reason instanceof Error ? `${result.reason.name}:${result.reason.message}` : result.reason) : 'fulfilled').join(' | ')}`)
  const quotaRejected = quotaResults.find((result): result is PromiseRejectedResult => result.status === 'rejected')
  assert(quotaRejected?.reason instanceof ChatConflictError && quotaRejected.reason.code === 'chat_storage_quota_exceeded', '另一个并发提交必须返回稳定容量冲突')
  const quotaWindow = await targetPool.query("SELECT COALESCE(SUM(content_bytes), 0)::bigint AS total FROM juhe_chat.chat_user_storage_windows WHERE system_account_id = $1", [quotaAccountId])
  const storedQuotaBytes = Number(quotaWindow.rows[0]?.total ?? 0)
  assert(storedQuotaBytes > 0 && storedQuotaBytes <= quotaBytes, '并发配额冲突后容量窗口不得超过上限')

  const cursorAccountId = 'sys_pg_cursor'
  const cursorPinned = await createChatConversation(client, { id: 'chat_pg_cursor_pinned', systemAccountId: cursorAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-12T01:00:00.000Z' })
  const cursorUnpinned = await createChatConversation(client, { id: 'chat_pg_cursor_unpinned', systemAccountId: cursorAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', now: '2026-07-12T02:00:00.000Z' })
  await updateChatConversation(client, { conversationId: cursorPinned.id, systemAccountId: cursorAccountId, isPinned: true, now: '2026-07-12T03:00:00.000Z' })
  const cursorFirstPage = await listChatConversations(client, { systemAccountId: cursorAccountId, limit: 1 })
  const cursorSecondPage = await listChatConversations(client, { systemAccountId: cursorAccountId, beforeIsPinned: cursorFirstPage[0]?.isPinned, beforeLastMessageAt: cursorFirstPage[0]?.lastMessageAt, beforeId: cursorFirstPage[0]?.id, limit: 1 })
  assert.deepEqual([cursorFirstPage[0]?.id, cursorSecondPage[0]?.id], [cursorPinned.id, cursorUnpinned.id], 'PostgreSQL 置顶三元游标必须跨到更晚的非置顶会话')

  const contextOwnerId = 'sys_pg_context'
  const contextConversation = await createChatConversation(client, {
    id: 'chat_pg_context',
    systemAccountId: contextOwnerId,
    apiKeyId: 'key_pg',
    apiKeyNameSnapshot: 'PG Context Key',
    now: '2026-07-12T04:00:00.000Z'
  })
  const pgAsset = await createChatAsset(client, {
    id: `chat_asset_${'d'.repeat(32)}`,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id,
    originalFilename: 'PG 上下文图片.png',
    originalMimeType: 'image/png',
    originalWidth: 32,
    originalHeight: 32,
    originalBytes: 128,
    originalSha256: 'a'.repeat(64),
    now: '2026-07-12T04:00:10.000Z'
  })
  await completeChatAssetProcessing(client, {
    assetId: pgAsset.id,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id,
    processedMimeType: 'image/png',
    processedWidth: 32,
    processedHeight: 32,
    processedBytes: 96,
    processedSha256: 'b'.repeat(64),
    storageKey: `aa/bb/${pgAsset.id}.png`,
    now: '2026-07-12T04:00:20.000Z'
  })
  for (let index = 1; index <= 3; index += 1) {
    const now = `2026-07-12T04:0${index}:00.000Z`
    const accepted = await acceptChatTurn(client, {
      conversationId: contextConversation.id,
      systemAccountId: contextOwnerId,
      clientMessageId: `pg_context_${index}`,
      userContent: index === 1 ? '请记住 PG-CONTEXT-731 和图片' : `PG 上下文第 ${index} 轮`,
      contentBlocks: index === 1 ? [
        { type: 'input_text', text: '请记住 PG-CONTEXT-731' },
        { type: 'input_image', assetId: pgAsset.id }
      ] : undefined,
      model: 'mock-model',
      now,
      storageQuotaBytes: 1024 * 1024
    })
    await completeChatTurn(client, {
      conversationId: contextConversation.id,
      systemAccountId: contextOwnerId,
      turnId: accepted.turnId,
      assistantContent: `PG 上下文回答 ${index}`,
      finishReason: 'stop',
      traceId: `trace_pg_context_${index}`,
      now
    })
  }
  await setChatAssetObservation(client, {
    assetId: pgAsset.id,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id,
    status: 'ready',
    observation: { summary: '图片包含 PG-CONTEXT-731' },
    now: '2026-07-12T04:04:00.000Z'
  })
  const boundAsset = await getChatAsset(client, { assetId: pgAsset.id, systemAccountId: contextOwnerId, conversationId: contextConversation.id })
  assert.ok(boundAsset?.turnId && boundAsset.messageId && boundAsset.observationStatus === 'ready', 'PG chat_assets 必须绑定用户消息并保存图片说明')
  assert.equal(await requestChatContextCompaction(client, {
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    expectedRevision: 3,
    sourceThroughSequence: 4,
    now: '2026-07-12T04:05:00.000Z'
  }), true)
  const contextClaim = await claimChatContextCompaction(client, {
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    expectedRevision: 3,
    sourceThroughSequence: 4,
    now: '2026-07-12T04:05:01.000Z',
    staleClaimBefore: '2026-07-12T03:00:00.000Z'
  })
  assert.ok(contextClaim)
  const contextPage = await loadChatCompactionSourcePage(client, {
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    claimId: contextClaim!.claimId,
    afterSequence: 0,
    now: '2026-07-12T04:05:02.000Z',
    limit: 20,
    maxBytes: 1024 * 1024
  })
  assert.deepEqual(contextPage?.messages.map((message) => message.sequenceNo), [1, 2, 3, 4])
  assert.equal(await recordChatContextCompactionProgress(client, {
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    claimId: contextClaim!.claimId,
    throughSequence: 4,
    earliestExpiresAt: contextPage!.earliestExpiresAt!,
    now: '2026-07-12T04:05:03.000Z'
  }), true)
  const contextEntries = [
    { kind: 'durable_memory' as const, content: { durableMemory: ['PG-CONTEXT-731'], constraints: [], decisions: [] }, provenance: 'assistant' as const, trustLevel: 'assistant_derived' as const },
    { kind: 'task_state' as const, content: { currentGoal: '验证 PG 上下文', completed: [], pending: [], recentUserIntent: '继续测试', uncertainties: [] }, provenance: 'assistant' as const, trustLevel: 'assistant_derived' as const }
  ]
  const contextPayload = JSON.stringify(contextEntries)
  await installChatContextCheckpoint(client, {
    claimId: contextClaim!.claimId,
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    sourceRevision: 3,
    sourceThroughSequence: 4,
    expiresAt: contextPage!.earliestExpiresAt!,
    payloadDigest: createHash('sha256').update(contextPayload).digest('hex'),
    estimatedInputTokens: 80,
    requestBodyBytes: Buffer.byteLength(contextPayload),
    modelId: 'mock-model',
    endpointFamily: 'responses',
    promptVersion: 'pg-smoke-v1',
    entries: contextEntries,
    now: '2026-07-12T04:05:04.000Z'
  })
  const pgContext = await loadChatModelContext(client, {
    conversationId: contextConversation.id,
    systemAccountId: contextOwnerId,
    now: '2026-07-12T04:06:00.000Z',
    maxRows: 50,
    maxBytes: 1024 * 1024
  })
  assert.match(JSON.stringify(pgContext?.entries), /PG-CONTEXT-731/)
  assert.deepEqual(pgContext?.suffix.map((message) => message.sequenceNo), [5, 6])
  const pgTableCounts = await targetPool.query(`
    SELECT
      (SELECT COUNT(*)::int FROM juhe_chat.chat_assets WHERE conversation_id = $1) AS assets,
      (SELECT COUNT(*)::int FROM juhe_chat.chat_context_checkpoints WHERE conversation_id = $1) AS checkpoints,
      (SELECT COUNT(*)::int FROM juhe_chat.chat_context_entries WHERE conversation_id = $1) AS entries
  `, [contextConversation.id])
  assert.deepEqual([Number(pgTableCounts.rows[0]?.assets), Number(pgTableCounts.rows[0]?.checkpoints), Number(pgTableCounts.rows[0]?.entries)], [1, 1, 2])
  const checkpointCleanup = await cleanupExpiredChatContextCheckpoints(client, { now: contextPage!.earliestExpiresAt!, limit: 10 })
  assert.equal(checkpointCleanup.deletedCheckpoints, 1, 'PG 过期 checkpoint 必须级联删除 entries')
  const afterCheckpointCleanup = await targetPool.query('SELECT COUNT(*)::int AS total FROM juhe_chat.chat_context_entries WHERE conversation_id = $1', [contextConversation.id])
  assert.equal(Number(afterCheckpointCleanup.rows[0]?.total), 0)
  const assetCleanupClaim = await claimExpiredChatAssetsForCleanup(client, { now: boundAsset!.expiresAt, limit: 10 })
  assert.deepEqual(assetCleanupClaim.assets.map((asset) => asset.id), [pgAsset.id])
  assert.equal(await completeChatAssetDeletion(client, { assetId: pgAsset.id, claimId: assetCleanupClaim.claimId }), true)

  redis = createClient({ url: redisUrl })
  await redis.connect()
  const redisKey = `juhe-ai:chat-smoke:${databaseName}`
  await redis.set(redisKey, 'ok', { EX: 30 })
  assert.equal(await redis.get(redisKey), 'ok')
  await redis.del(redisKey)
  },
  cleanupSteps: [
    { name: 'redis-quit', run: async () => { if (redis?.isOpen) await redis.quit() } },
    { name: 'target-pool-end', run: async () => { if (targetPool) await targetPool.end() } },
    { name: 'terminate-target-connections', run: async () => { await adminPool.query(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, [databaseName]) } },
    { name: 'drop-target-database', run: async () => { await adminPool.query(`DROP DATABASE IF EXISTS "${databaseName}"`) } },
    { name: 'admin-pool-end', run: async () => { await adminPool.end() } }
  ],
  onSuccess: () => {
    console.log('AI 问答 PostgreSQL/Redis smoke 通过：独立数据库、juhe_chat schema、日分区、repository、临时 Redis namespace 和清理验证均正常')
  }
})

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`缺少 ${name}`)
  return value
}
