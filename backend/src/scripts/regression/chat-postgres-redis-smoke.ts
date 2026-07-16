import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { Pool } from 'pg'
import { createClient } from 'redis'

import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { acceptChatTurn, cancelActiveChatTurnIfMatches, chatAssistantStorageReservationBytes, ChatConflictError, cleanupChatRetention, completeChatTurn, createChatConversation, listChatContextMessages, listChatConversations, listChatMessages, updateChatConversation } from '../../storage/chat.repository.js'
import { claimChatAssetObservation, claimExpiredChatAssetsForCleanup, completeChatAssetDeletion, completeChatAssetProcessing, createChatAsset, getChatAsset, setChatAssetObservation } from '../../storage/chat-assets.repository.js'
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
  const conversation = await createChatConversation(client, { id: 'chat_pg_conv', systemAccountId: 'sys_pg', apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:00:00.000Z' })
  const turn = await acceptChatTurn(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', clientMessageId: 'pg_client_1', userContent: 'PG 测试', model: 'mock-model', now: '2026-07-12T00:01:00.000Z', storageQuotaBytes: 1024 * 1024 , retentionDays: 7, maxTurnsPerConversation: 1000})
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
    storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000,
    replaceTurnId: turn.turnId
  })
  assert.equal(replacement.userMessage.sequenceNo, turn.userMessage.sequenceNo)
  assert.equal(replacement.assistantMessage.sequenceNo, turn.assistantMessage.sequenceNo)
  await completeChatTurn(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', turnId: replacement.turnId, assistantContent: 'PG 修正后的回答', finishReason: 'stop', traceId: 'trace_pg_replace', now: '2026-07-12T00:05:00.000Z' })
  assert.deepEqual((await listChatMessages(client, { conversationId: conversation.id, systemAccountId: 'sys_pg', limit: 20, now: '2026-07-12T00:06:00.000Z' })).map((item) => item.contentText), ['PG 修正后的问题', 'PG 修正后的回答'])
  const expiredConversation = await createChatConversation(client, { id: 'chat_pg_expired', systemAccountId: 'sys_pg', apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-01T00:00:00.000Z' })
  const expiredTurn = await acceptChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', clientMessageId: 'pg_expired_1', userContent: '过期问题', model: 'mock-model', now: '2026-07-01T00:01:00.000Z', storageQuotaBytes: 1024 * 1024 , retentionDays: 7, maxTurnsPerConversation: 1000})
  await completeChatTurn(client, { conversationId: expiredConversation.id, systemAccountId: 'sys_pg', turnId: expiredTurn.turnId, assistantContent: '过期回答', finishReason: 'stop', traceId: 'trace_expired', now: '2026-07-01T00:02:00.000Z' })
  const cleanup = await cleanupChatRetention(client, { now: '2026-07-12T00:10:00.000Z', interruptedBefore: '2026-07-12T00:00:00.000Z', limit: 1000, retentionDays: 7 })
  assert(cleanup.droppedPartitions >= 1, 'PostgreSQL 应直接删除完全过期的日分区')
  assert(cleanup.deletedConversations >= 1, '日分区删除后应收口空会话')
  const expiredStorageWindow = await targetPool.query(`
    SELECT COUNT(*)::int AS total
    FROM juhe_chat.chat_user_storage_windows
    WHERE system_account_id = 'sys_pg' AND bucket_date = '2026-07-01'
  `)
  assert.equal(Number(expiredStorageWindow.rows[0]?.total), 0, '日分区删除后必须同步回收已无消息引用的旧容量窗口')
  const partitions = await targetPool.query("SELECT COUNT(*)::int AS total FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent JOIN pg_namespace n ON n.oid=p.relnamespace WHERE n.nspname='juhe_chat' AND p.relname='chat_messages'")
  assert(Number(partitions.rows[0]?.total) >= 2, '写入时应确保当天和下一天消息分区')

  const quotaAccountId = 'sys_pg_quota_race'
  const [quotaConversationA, quotaConversationB] = await Promise.all([
    createChatConversation(client, { id: 'chat_pg_quota_a', systemAccountId: quotaAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:20:00.000Z' }),
    createChatConversation(client, { id: 'chat_pg_quota_b', systemAccountId: quotaAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:20:00.000Z' })
  ])
  const quotaContent = 'x'.repeat(1000)
  const quotaBytes = chatAssistantStorageReservationBytes + 2500
  const quotaResults = await Promise.allSettled([
    acceptChatTurn(client, { conversationId: quotaConversationA.id, systemAccountId: quotaAccountId, clientMessageId: 'pg_quota_a', userContent: quotaContent, model: 'mock-model', now: '2026-07-12T00:21:00.000Z', storageQuotaBytes: quotaBytes , retentionDays: 7, maxTurnsPerConversation: 1000}),
    acceptChatTurn(client, { conversationId: quotaConversationB.id, systemAccountId: quotaAccountId, clientMessageId: 'pg_quota_b', userContent: quotaContent, model: 'mock-model', now: '2026-07-12T00:21:00.000Z', storageQuotaBytes: quotaBytes , retentionDays: 7, maxTurnsPerConversation: 1000})
  ])
  assert.equal(quotaResults.filter((result) => result.status === 'fulfilled').length, 1, `同一用户跨会话并发提交只能有一个通过存储配额：${quotaResults.map((result) => result.status === 'rejected' ? String(result.reason instanceof Error ? `${result.reason.name}:${result.reason.message}` : result.reason) : 'fulfilled').join(' | ')}`)
  const quotaRejected = quotaResults.find((result): result is PromiseRejectedResult => result.status === 'rejected')
  assert(quotaRejected?.reason instanceof ChatConflictError && quotaRejected.reason.code === 'chat_storage_quota_exceeded', '另一个并发提交必须返回稳定容量冲突')
  const quotaWindow = await targetPool.query("SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0)::bigint AS total FROM juhe_chat.chat_user_storage_windows WHERE system_account_id = $1", [quotaAccountId])
  const storedQuotaBytes = Number(quotaWindow.rows[0]?.total ?? 0)
  assert(storedQuotaBytes > 0 && storedQuotaBytes <= quotaBytes, '并发配额冲突后容量窗口不得超过上限')
  const quotaMessages = await targetPool.query(`
    SELECT COALESCE(SUM(content_bytes + storage_reserved_bytes), 0)::bigint AS total
    FROM juhe_chat.chat_messages WHERE system_account_id = $1
  `, [quotaAccountId])
  assert.equal(Number(quotaMessages.rows[0]?.total ?? 0), storedQuotaBytes, 'PG 并发接受后容量窗口 actual/reservation 必须与消息表一致')

  const conversationLimitOwnerId = 'sys_pg_conversation_limit_race'
  for (let index = 0; index < 49; index += 1) {
    await createChatConversation(client, {
      id: `chat_pg_conversation_limit_${String(index).padStart(2, '0')}`,
      systemAccountId: conversationLimitOwnerId,
      apiKeyId: 'key_pg',
      apiKeyNameSnapshot: 'PG 会话上限 Key',
      now: '2026-07-12T00:25:00.000Z',
      maxConversationsPerUser: 50
    })
  }
  const conversationLimitResults = await Promise.allSettled([
    createChatConversation(client, { id: 'chat_pg_conversation_limit_a', systemAccountId: conversationLimitOwnerId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG 会话上限 Key', now: '2026-07-12T00:25:01.000Z', maxConversationsPerUser: 50 }),
    createChatConversation(client, { id: 'chat_pg_conversation_limit_b', systemAccountId: conversationLimitOwnerId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG 会话上限 Key', now: '2026-07-12T00:25:01.000Z', maxConversationsPerUser: 50 })
  ])
  assert.equal(conversationLimitResults.filter((result) => result.status === 'fulfilled').length, 1, 'PG 49+2 并发创建必须恰好成功一个')
  const conversationLimitRejected = conversationLimitResults.find((result): result is PromiseRejectedResult => result.status === 'rejected')
  assert(conversationLimitRejected?.reason instanceof ChatConflictError && conversationLimitRejected.reason.code === 'chat_conversation_limit_exceeded', 'PG 49+2 另一个创建必须返回稳定会话上限冲突')
  const conversationLimitCount = await targetPool.query('SELECT COUNT(*)::int AS total FROM juhe_chat.chat_conversations WHERE system_account_id = $1', [conversationLimitOwnerId])
  assert.equal(Number(conversationLimitCount.rows[0]?.total), 50, 'PG 49+2 并发创建后最终会话数必须为 50')
  await targetPool.query('DELETE FROM juhe_chat.chat_conversations WHERE system_account_id = $1', [conversationLimitOwnerId])

  const turnLimitOwnerId = 'sys_pg_turn_limit_race'
  const turnLimitConversation = await createChatConversation(client, {
    id: 'chat_pg_turn_limit_race', systemAccountId: turnLimitOwnerId, apiKeyId: 'key_pg',
    apiKeyNameSnapshot: 'PG 轮次上限 Key', now: '2026-07-12T00:26:00.000Z', maxConversationsPerUser: 50
  })
  await targetPool.query('UPDATE juhe_chat.chat_conversations SET user_turn_count = 49 WHERE id = $1 AND system_account_id = $2', [turnLimitConversation.id, turnLimitOwnerId])
  const turnCountsBefore = await targetPool.query(`
    SELECT
      (SELECT COUNT(*)::int FROM juhe_chat.chat_messages WHERE conversation_id = $1) AS messages,
      (SELECT COUNT(*)::int FROM juhe_chat.chat_message_idempotency WHERE conversation_id = $1) AS idempotency
  `, [turnLimitConversation.id])
  const turnLimitResults = await Promise.allSettled([
    acceptChatTurn(client, { conversationId: turnLimitConversation.id, systemAccountId: turnLimitOwnerId, clientMessageId: 'pg_turn_limit_a', userContent: '并发第 50 轮 A', model: 'mock-model', now: '2026-07-12T00:26:01.000Z', storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 50 }),
    acceptChatTurn(client, { conversationId: turnLimitConversation.id, systemAccountId: turnLimitOwnerId, clientMessageId: 'pg_turn_limit_b', userContent: '并发第 50 轮 B', model: 'mock-model', now: '2026-07-12T00:26:01.000Z', storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 50 })
  ])
  assert.equal(turnLimitResults.filter((result) => result.status === 'fulfilled').length, 1, 'PG 49 轮后两个不同 clientMessageId 并发接受必须恰好成功一个')
  const turnLimitRejected = turnLimitResults.find((result): result is PromiseRejectedResult => result.status === 'rejected')
  assert(turnLimitRejected?.reason instanceof ChatConflictError && turnLimitRejected.reason.code === 'chat_turn_limit_exceeded', 'PG 并发第 50 轮另一个接受必须返回稳定轮次上限冲突')
  const turnCountsAfter = await targetPool.query(`
    SELECT c.user_turn_count,
      (SELECT COUNT(*)::int FROM juhe_chat.chat_messages WHERE conversation_id = c.id) AS messages,
      (SELECT COUNT(*)::int FROM juhe_chat.chat_message_idempotency WHERE conversation_id = c.id) AS idempotency
    FROM juhe_chat.chat_conversations c WHERE c.id = $1 AND c.system_account_id = $2
  `, [turnLimitConversation.id, turnLimitOwnerId])
  assert.equal(Number(turnCountsAfter.rows[0]?.user_turn_count), 50, 'PG 并发轮次门禁后最终用户轮次必须为 50')
  assert.equal(Number(turnCountsAfter.rows[0]?.messages) - Number(turnCountsBefore.rows[0]?.messages), 2, 'PG 并发轮次门禁只允许新增一对消息')
  assert.equal(Number(turnCountsAfter.rows[0]?.idempotency) - Number(turnCountsBefore.rows[0]?.idempotency), 1, 'PG 并发轮次门禁只允许新增一条幂等记录')
  await targetPool.query('DELETE FROM juhe_chat.chat_conversations WHERE system_account_id = $1', [turnLimitOwnerId])

  const stopRaceOwnerId = 'sys_pg_stop_complete_race'
  const stopRaceConversation = await createChatConversation(client, {
    id: 'chat_pg_stop_complete_race', systemAccountId: stopRaceOwnerId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T00:30:00.000Z'
  })
  const stopRaceTurn = await acceptChatTurn(client, {
    conversationId: stopRaceConversation.id, systemAccountId: stopRaceOwnerId, clientMessageId: 'pg_stop_complete_race', userContent: '并发 stop 与 complete', model: 'mock-model', now: '2026-07-12T00:30:01.000Z', storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  const completeLockReached = deferredSignal()
  const releaseCompleteLock = deferredSignal()
  const stopConversationLockReached = deferredSignal()
  const completeClient = createInterposedPostgresClient(targetPool, async (sql) => {
    if (completeLockReached.settled || !isChatFinalizeFirstLock(sql)) return
    completeLockReached.resolve()
    await releaseCompleteLock.promise
  })
  const stopClient = createInterposedPostgresClient(targetPool, async (sql) => {
    if (!stopConversationLockReached.settled && isChatConversationLock(sql)) stopConversationLockReached.resolve()
  })
  const completionOutcome = settle(completeChatTurn(completeClient, {
    conversationId: stopRaceConversation.id, systemAccountId: stopRaceOwnerId, turnId: stopRaceTurn.turnId, assistantContent: '并发完成回答', finishReason: 'stop', traceId: 'trace_pg_stop_complete_race', now: '2026-07-12T00:30:02.000Z'
  }))
  await withTimeout(completeLockReached.promise, 5_000, 'complete 未取得首个写锁')
  const stopOutcome = settle(cancelActiveChatTurnIfMatches(stopClient, {
    conversationId: stopRaceConversation.id, systemAccountId: stopRaceOwnerId, expectedTurnId: stopRaceTurn.turnId, now: '2026-07-12T00:30:03.000Z'
  }))
  await Promise.race([stopConversationLockReached.promise, delay(200)])
  releaseCompleteLock.resolve()
  const [completed, stopped] = await Promise.all([completionOutcome, stopOutcome])
  assert.equal(completed.status, 'fulfilled', `complete 与 stop 双连接交错不得产生 PostgreSQL 死锁：${outcomeError(completed)}`)
  assert.equal(stopped.status, 'fulfilled', `stop 与 complete 双连接交错不得产生 PostgreSQL 死锁：${outcomeError(stopped)}`)
  if (stopped.status === 'fulfilled') {
    assert.deepEqual(stopped.value, { state: 'already_terminal', assistantStatus: 'completed' }, 'complete 先持有统一首锁后 stop 必须读到权威 completed 终态')
  }
  const stopRaceState = await targetPool.query(`
    SELECT c.active_turn_id, m.status
    FROM juhe_chat.chat_conversations c
    JOIN juhe_chat.chat_messages m ON m.conversation_id = c.id AND m.turn_id = $2 AND m.role = 'assistant'
    WHERE c.id = $1
  `, [stopRaceConversation.id, stopRaceTurn.turnId])
  assert.deepEqual([stopRaceState.rows[0]?.active_turn_id ?? null, stopRaceState.rows[0]?.status], [null, 'completed'], '并发终结后会话与消息必须落在同一个权威终态')

  const cursorAccountId = 'sys_pg_cursor'
  const cursorPinned = await createChatConversation(client, { id: 'chat_pg_cursor_pinned', systemAccountId: cursorAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T01:00:00.000Z' })
  const cursorUnpinned = await createChatConversation(client, { id: 'chat_pg_cursor_unpinned', systemAccountId: cursorAccountId, apiKeyId: 'key_pg', apiKeyNameSnapshot: 'PG Key', maxConversationsPerUser: 1000, now: '2026-07-12T02:00:00.000Z' })
  await updateChatConversation(client, { conversationId: cursorPinned.id, systemAccountId: cursorAccountId, isPinned: true, now: '2026-07-12T03:00:00.000Z' })
  const cursorFirstPage = await listChatConversations(client, { systemAccountId: cursorAccountId, limit: 1 })
  const cursorSecondPage = await listChatConversations(client, { systemAccountId: cursorAccountId, beforeIsPinned: cursorFirstPage[0]?.isPinned, beforeLastMessageAt: cursorFirstPage[0]?.lastMessageAt, beforeId: cursorFirstPage[0]?.id, limit: 1 })
  assert.deepEqual([cursorFirstPage[0]?.id, cursorSecondPage[0]?.id], [cursorPinned.id, cursorUnpinned.id], 'PostgreSQL 置顶三元游标必须跨到更晚的非置顶会话')

  const observationRaceOwnerId = 'sys_pg_observation_race'
  const observationRaceConversation = await createChatConversation(client, {
    id: 'chat_pg_observation_race',
    systemAccountId: observationRaceOwnerId,
    apiKeyId: 'key_pg',
    apiKeyNameSnapshot: 'PG Observation Race Key', maxConversationsPerUser: 1000,
    now: '2026-07-12T05:30:00.000Z'
  })
  const observationRaceAsset = await createChatAsset(client, {
    id: `chat_asset_${'e'.repeat(32)}`,
    systemAccountId: observationRaceOwnerId,
    conversationId: observationRaceConversation.id,
    originalFilename: 'PG 图片说明竞态.png',
    originalMimeType: 'image/png',
    originalWidth: 32,
    originalHeight: 32,
    originalBytes: 128,
    originalSha256: 'c'.repeat(64),
    quotaBytes: 96, retentionDays: 7,
    now: '2026-07-12T05:30:10.000Z'
  })
  await completeChatAssetProcessing(client, {
    assetId: observationRaceAsset.id,
    systemAccountId: observationRaceOwnerId,
    conversationId: observationRaceConversation.id,
    processedMimeType: 'image/png',
    processedWidth: 32,
    processedHeight: 32,
    processedBytes: 96,
    processedSha256: 'd'.repeat(64),
    storageKey: `ee/ff/${observationRaceAsset.id}.png`,
    now: '2026-07-12T05:30:20.000Z'
  })
  const observationRaceOldTurn = await acceptChatTurn(client, {
    conversationId: observationRaceConversation.id,
    systemAccountId: observationRaceOwnerId,
    clientMessageId: 'pg_observation_race_old',
    userContent: '旧图片问题',
    contentBlocks: [{ type: 'input_text', text: '旧图片问题' }, { type: 'input_image', assetId: observationRaceAsset.id }],
    model: 'mock-model',
    now: '2026-07-12T05:31:00.000Z',
    storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: observationRaceConversation.id,
    systemAccountId: observationRaceOwnerId,
    turnId: observationRaceOldTurn.turnId,
    assistantContent: '旧图片回答',
    finishReason: 'stop',
    traceId: 'trace_pg_observation_race_old',
    now: '2026-07-12T05:31:30.000Z'
  })
  const observationRaceNewTurn = await acceptChatTurn(client, {
    conversationId: observationRaceConversation.id,
    systemAccountId: observationRaceOwnerId,
    clientMessageId: 'pg_observation_race_new',
    userContent: '新图片问题',
    contentBlocks: [{ type: 'input_text', text: '新图片问题' }, { type: 'input_image', assetId: observationRaceAsset.id }],
    model: 'mock-model',
    now: '2026-07-12T05:32:00.000Z',
    storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000,
    replaceTurnId: observationRaceOldTurn.turnId
  })
  assert.equal(await claimChatAssetObservation(client, {
    assetId: observationRaceAsset.id,
    systemAccountId: observationRaceOwnerId,
    conversationId: observationRaceConversation.id,
    expectedTurnId: observationRaceOldTurn.turnId,
    expectedMessageId: observationRaceOldTurn.userMessage.id,
    now: '2026-07-12T05:32:10.000Z'
  }), undefined, 'PG replace 后才开始的旧图片说明任务不能认领新轮次 revision')
  const observationRaceNewClaim = await claimChatAssetObservation(client, {
    assetId: observationRaceAsset.id,
    systemAccountId: observationRaceOwnerId,
    conversationId: observationRaceConversation.id,
    expectedTurnId: observationRaceNewTurn.turnId,
    expectedMessageId: observationRaceNewTurn.userMessage.id,
    now: '2026-07-12T05:32:20.000Z'
  })
  assert.ok(observationRaceNewClaim?.observationClaimId, 'PG 当前轮图片说明任务必须能认领当前 revision')
  await setChatAssetObservation(client, {
    assetId: observationRaceAsset.id,
    systemAccountId: observationRaceOwnerId,
    conversationId: observationRaceConversation.id,
    status: 'ready',
    observation: { summary: '新图片问题与新回答产生的说明' },
    observationRevision: observationRaceNewClaim.observationRevision,
    claimId: observationRaceNewClaim.observationClaimId,
    now: '2026-07-12T05:32:30.000Z'
  })
  await completeChatTurn(client, {
    conversationId: observationRaceConversation.id,
    systemAccountId: observationRaceOwnerId,
    turnId: observationRaceNewTurn.turnId,
    assistantContent: '新图片回答',
    finishReason: 'stop',
    traceId: 'trace_pg_observation_race_new',
    now: '2026-07-12T05:33:00.000Z'
  })

  const contextOwnerId = 'sys_pg_context'
  const contextConversation = await createChatConversation(client, {
    id: 'chat_pg_context',
    systemAccountId: contextOwnerId,
    apiKeyId: 'key_pg',
    apiKeyNameSnapshot: 'PG Context Key', maxConversationsPerUser: 1000,
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
    quotaBytes: 96, retentionDays: 7,
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
      storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
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
  const pgAssetBinding = await getChatAsset(client, {
    assetId: pgAsset.id,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id
  })
  assert.ok(pgAssetBinding?.turnId && pgAssetBinding.messageId, 'PG 图片说明认领前必须存在已提交的轮次和消息绑定')
  const pgObservationClaim = await claimChatAssetObservation(client, {
    assetId: pgAsset.id,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id,
    expectedTurnId: pgAssetBinding.turnId,
    expectedMessageId: pgAssetBinding.messageId,
    now: '2026-07-12T04:03:30.000Z'
  })
  assert.ok(pgObservationClaim?.observationClaimId)
  await setChatAssetObservation(client, {
    assetId: pgAsset.id,
    systemAccountId: contextOwnerId,
    conversationId: contextConversation.id,
    status: 'ready',
    observation: { summary: '图片包含 PG-CONTEXT-731' },
    observationRevision: pgObservationClaim.observationRevision,
    claimId: pgObservationClaim.observationClaimId,
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
function createInterposedPostgresClient(pool: Pool, afterQuery: (sql: string) => Promise<void>) {
  return createPostgresDatabaseClient({
    query: async (text, values) => {
      const result = await pool.query(text, values ? [...values] : undefined)
      return { rows: result.rows as Array<Record<string, unknown>>, rowCount: result.rowCount }
    },
    connect: async () => {
      const connection = await pool.connect()
      return {
        query: async (text: string, values?: readonly unknown[]) => {
          const result = await connection.query(text, values ? [...values] : undefined)
          await afterQuery(text)
          return { rows: result.rows as Array<Record<string, unknown>>, rowCount: result.rowCount }
        },
        release: () => connection.release()
      }
    }
  })
}

function isChatConversationLock(sql: string): boolean {
  const normalized = sql.replace(/\s+/g, ' ').trim().toLowerCase()
  return normalized.startsWith('select * from "juhe_chat"."chat_conversations"') && normalized.endsWith('for update')
}

function isChatFinalizeFirstLock(sql: string): boolean {
  const normalized = sql.replace(/\s+/g, ' ').trim().toLowerCase()
  return isChatConversationLock(sql)
    || (normalized.startsWith('update "juhe_chat"."chat_messages"')
      && normalized.includes('set status = $1')
      && normalized.includes("role = 'assistant' and status = 'streaming'"))
}

function deferredSignal(): { promise: Promise<void>; resolve: () => void; settled: boolean } {
  let resolvePromise!: () => void
  const signal = {
    promise: new Promise<void>((resolve) => { resolvePromise = resolve }),
    resolve: () => {},
    settled: false
  }
  signal.resolve = () => {
    if (signal.settled) return
    signal.settled = true
    resolvePromise()
  }
  return signal
}

async function withTimeout(promise: Promise<void>, timeoutMs: number, message: string): Promise<void> {
  let timeout: NodeJS.Timeout | undefined
  try {
    await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => { timeout = setTimeout(() => reject(new Error(message)), timeoutMs) })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

async function settle<T>(promise: Promise<T>): Promise<PromiseSettledResult<T>> {
  try {
    return { status: 'fulfilled', value: await promise }
  } catch (reason) {
    return { status: 'rejected', reason }
  }
}

function outcomeError(result: PromiseSettledResult<unknown>): string {
  if (result.status === 'fulfilled') return 'fulfilled'
  return result.reason instanceof Error ? `${result.reason.name}:${result.reason.message}` : String(result.reason)
}
