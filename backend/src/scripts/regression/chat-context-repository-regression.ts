import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { acceptChatTurn, completeChatTurn, createChatConversation, failChatTurn } from '../../storage/chat.repository.js'
import {
  claimChatContextCompaction,
  cleanupExpiredChatContextCheckpoints,
  failChatContextCompaction,
  getChatContextHead,
  installChatContextCheckpoint,
  loadChatCompactionSourcePage,
  loadChatModelContext,
  recordChatContextCompactionProgress,
  recoverStaleChatContextCompactions,
  releaseChatContextCompactionClaim,
  requestChatContextCompaction
} from '../../storage/chat-context.repository.js'

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const ownerId = 'context_owner'
const conversationId = 'context_conversation'
await createChatConversation(client, {
  id: conversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_key',
  apiKeyNameSnapshot: '上下文 Key', maxConversationsPerUser: 1000,
  now: '2026-07-13T00:00:00.000Z'
})
const initialHead = await getChatContextHead(client, { conversationId, systemAccountId: ownerId })
assert.deepEqual({
  contextRetryAt: initialHead?.contextRetryAt,
  contextAttemptCount: initialHead?.contextAttemptCount,
  contextErrorCode: initialHead?.contextErrorCode
}, {
  contextRetryAt: undefined,
  contextAttemptCount: 0,
  contextErrorCode: undefined
}, '新会话必须返回空退避与零尝试次数')

for (let index = 1; index <= 3; index += 1) {
  const now = `2026-07-13T00:0${index}:00.000Z`
  const accepted = await acceptChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    clientMessageId: `context_client_${index}`,
    userContent: index === 1 ? '请记住我的项目代号是蓝鲸' : `第 ${index} 轮问题`,
    model: 'context-model',
    now,
    storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: index === 1 ? '我会记住项目代号蓝鲸' : `第 ${index} 轮回答`,
    finishReason: 'stop',
    traceId: `trace_${index}`,
    now
  })
}

const before = await loadChatModelContext(client, { conversationId, systemAccountId: ownerId, now: '2026-07-13T00:10:00.000Z', maxRows: 50, maxBytes: 1024 * 1024 })
assert.equal(before?.entries.length, 0)
assert.deepEqual(before?.suffix.map((message) => message.sequenceNo), [1, 2, 3, 4, 5, 6])
assert.equal(before?.head.nextSequenceNo, 7)

assert.equal(await requestChatContextCompaction(client, {
  conversationId,
  systemAccountId: ownerId,
  expectedRevision: 3,
  sourceThroughSequence: 4,
  now: '2026-07-13T00:11:00.000Z'
}), true)
const claim = await claimChatContextCompaction(client, {
  conversationId,
  systemAccountId: ownerId,
  expectedRevision: 3,
  sourceThroughSequence: 4,
  now: '2026-07-13T00:11:01.000Z',
  staleClaimBefore: '2026-07-13T00:00:00.000Z'
})
assert.ok(claim)
const page = await loadChatCompactionSourcePage(client, {
  conversationId,
  systemAccountId: ownerId,
  claimId: claim!.claimId,
  afterSequence: 0,
  now: '2026-07-13T00:11:02.000Z',
  limit: 20,
  maxBytes: 1024 * 1024
})
assert.deepEqual(page?.messages.map((message) => message.sequenceNo), [1, 2, 3, 4])
assert.equal(page?.nextAfterSequence, 4)
assert.equal(await recordChatContextCompactionProgress(client, {
  conversationId,
  systemAccountId: ownerId,
  claimId: claim!.claimId,
  throughSequence: 4,
  earliestExpiresAt: page!.earliestExpiresAt!,
  now: '2026-07-13T00:11:03.000Z'
}), true)
const memory = { durableMemory: ['用户项目代号是蓝鲸'], currentGoal: '继续当前对话', recentUserIntent: '继续第 3 轮问题' }
const serialized = JSON.stringify(memory)
const checkpoint = await installChatContextCheckpoint(client, {
  claimId: claim!.claimId,
  conversationId,
  systemAccountId: ownerId,
  sourceRevision: 3,
  sourceThroughSequence: 4,
  expiresAt: page!.earliestExpiresAt!,
  payloadDigest: createHash('sha256').update(serialized).digest('hex'),
  estimatedInputTokens: 40,
  activeContextTokens: 50,
  effectiveContextLimitTokens: 1_000,
  requestBodyBytes: Buffer.byteLength(serialized),
  modelId: 'context-model',
  endpointFamily: 'responses',
  promptVersion: 'context-test-v1',
  entries: [{ kind: 'durable_memory', content: memory, provenance: 'assistant', trustLevel: 'assistant_derived', tokenCount: 40 }],
  now: '2026-07-13T00:11:04.000Z'
})
assert.equal(checkpoint.status, 'active')
const installedHead = await getChatContextHead(client, { conversationId, systemAccountId: ownerId })
assert.deepEqual({
  contextState: installedHead?.contextState,
  contextRetryAt: installedHead?.contextRetryAt,
  contextAttemptCount: installedHead?.contextAttemptCount,
  contextErrorCode: installedHead?.contextErrorCode
}, {
  contextState: 'ready',
  contextRetryAt: undefined,
  contextAttemptCount: 0,
  contextErrorCode: undefined
}, '成功安装 checkpoint 必须暴露已清零的权威失败状态')

const after = await loadChatModelContext(client, { conversationId, systemAccountId: ownerId, now: '2026-07-13T00:12:00.000Z', maxRows: 50, maxBytes: 1024 * 1024 })
assert.equal(after?.entries.length, 1)
assert.match(JSON.stringify(after?.entries[0]?.content), /蓝鲸/)
assert.deepEqual(after?.suffix.map((message) => message.sequenceNo), [5, 6], 'checkpoint 后必须保留最近一整轮原文')
assert.equal(after?.head.compactedThroughSequence, 4)

const fourth = await acceptChatTurn(client, {
  conversationId,
  systemAccountId: ownerId,
  clientMessageId: 'context_client_4',
  userContent: '第 4 轮问题',
  model: 'context-model',
  now: '2026-07-13T00:13:00.000Z',
  storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, {
  conversationId,
  systemAccountId: ownerId,
  turnId: fourth.turnId,
  assistantContent: '第 4 轮回答',
  finishReason: 'stop',
  traceId: 'trace_4',
  now: '2026-07-13T00:13:30.000Z'
})
assert.equal(await requestChatContextCompaction(client, {
  conversationId,
  systemAccountId: ownerId,
  expectedRevision: 5,
  sourceThroughSequence: 6,
  now: '2026-07-13T00:14:00.000Z'
}), true)
const staleClaim = await claimChatContextCompaction(client, {
  conversationId,
  systemAccountId: ownerId,
  expectedRevision: 5,
  sourceThroughSequence: 6,
  now: '2026-07-13T00:14:01.000Z',
  staleClaimBefore: '2026-07-13T00:00:00.000Z'
})
assert.ok(staleClaim)
assert.equal(await recoverStaleChatContextCompactions(client, {
  now: page!.earliestExpiresAt!,
  staleClaimBefore: '2026-07-20T00:00:00.000Z',
  limit: 10
}), 1, '维护任务必须释放长期卡住的压缩认领，避免过期 checkpoint 清理饥饿')

const cleaned = await cleanupExpiredChatContextCheckpoints(client, { now: page!.earliestExpiresAt!, limit: 10 })
assert.equal(cleaned.deletedCheckpoints, 1)
const detached = await getChatContextHead(client, { conversationId, systemAccountId: ownerId })
assert.equal(detached?.activeCheckpointId, undefined)
assert.equal(detached?.compactedThroughSequence, 0)

const expiredOwnerId = 'context_expired_checkpoint_owner'
const expiredConversationId = 'context_expired_checkpoint_conversation'
await createChatConversation(client, {
  id: expiredConversationId,
  systemAccountId: expiredOwnerId,
  apiKeyId: 'context_expired_checkpoint_key',
  apiKeyNameSnapshot: '过期 checkpoint Key', maxConversationsPerUser: 1000,
  now: '2026-07-14T03:00:00.000Z'
})
for (let index = 1; index <= 3; index += 1) {
  const now = `2026-07-14T03:0${index}:00.000Z`
  const accepted = await acceptChatTurn(client, {
    conversationId: expiredConversationId,
    systemAccountId: expiredOwnerId,
    clientMessageId: `expired_checkpoint_${index}`,
    userContent: `过期 checkpoint 问题 ${index}`,
    model: 'mock-model',
    now,
    storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: expiredConversationId,
    systemAccountId: expiredOwnerId,
    turnId: accepted.turnId,
    assistantContent: `过期 checkpoint 回答 ${index}`,
    finishReason: 'stop',
    traceId: `expired_checkpoint_trace_${index}`,
    now
  })
}
assert.equal(await requestChatContextCompaction(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  expectedRevision: 3,
  sourceThroughSequence: 4,
  now: '2026-07-14T03:04:00.000Z'
}), true)
const expiredCheckpointClaim = await claimChatContextCompaction(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  expectedRevision: 3,
  sourceThroughSequence: 4,
  now: '2026-07-14T03:04:01.000Z',
  staleClaimBefore: '2026-07-14T02:00:00.000Z'
})
assert.ok(expiredCheckpointClaim)
const expiredCheckpointSource = await loadChatCompactionSourcePage(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  claimId: expiredCheckpointClaim!.claimId,
  afterSequence: 0,
  now: '2026-07-14T03:04:02.000Z',
  limit: 20,
  maxBytes: 1024 * 1024
})
assert.deepEqual(expiredCheckpointSource?.messages.map((message) => message.sequenceNo), [1, 2, 3, 4])
assert.equal(await recordChatContextCompactionProgress(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  claimId: expiredCheckpointClaim!.claimId,
  throughSequence: 4,
  earliestExpiresAt: expiredCheckpointSource!.earliestExpiresAt!,
  now: '2026-07-14T03:04:03.000Z'
}), true)
const expiredMemory = { durableMemory: ['旧 checkpoint 记忆'], currentGoal: '验证过期恢复', recentUserIntent: '保留最近一轮' }
const expiredSerialized = JSON.stringify(expiredMemory)
await installChatContextCheckpoint(client, {
  claimId: expiredCheckpointClaim!.claimId,
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  sourceRevision: 3,
  sourceThroughSequence: 4,
  expiresAt: '2026-07-14T03:30:00.000Z',
  payloadDigest: createHash('sha256').update(expiredSerialized).digest('hex'),
  estimatedInputTokens: 20,
  activeContextTokens: 30,
  effectiveContextLimitTokens: 1_000,
  requestBodyBytes: Buffer.byteLength(expiredSerialized),
  modelId: 'mock-model',
  endpointFamily: 'responses',
  promptVersion: 'expired-checkpoint-test-v1',
  entries: [{ kind: 'durable_memory', content: expiredMemory, provenance: 'assistant', trustLevel: 'assistant_derived', tokenCount: 20 }],
  now: '2026-07-14T03:04:04.000Z'
})

const expiredEffectiveContext = await loadChatModelContext(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  now: '2026-07-14T04:00:00.000Z',
  maxRows: 20,
  maxBytes: 1024 * 1024
})
assert.equal(expiredEffectiveContext?.head.compactedThroughSequence, 0, '只读装载必须把过期 checkpoint 视为无效起点')
assert.deepEqual(expiredEffectiveContext?.suffix.map((message) => message.sequenceNo), [1, 2, 3, 4, 5, 6], 'checkpoint 过期时仍在保留期内的原消息必须重新进入模型上下文')
const reclaimAfterExpiry = await claimChatContextCompaction(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  expectedRevision: expiredEffectiveContext!.head.contextRevision,
  sourceThroughSequence: 4,
  now: '2026-07-14T04:00:01.000Z',
  staleClaimBefore: '2026-07-14T03:00:00.000Z'
})
assert.ok(reclaimAfterExpiry, '过期 checkpoint 的持久游标不能阻止重新认领')
assert.equal(reclaimAfterExpiry?.sourceFromSequence, 1, '重新认领必须从第一条仍有效消息开始')
assert.equal(reclaimAfterExpiry?.progressSequence, 0, '重新认领进度必须与有效 checkpoint 起点一致')
const reclaimedPage = await loadChatCompactionSourcePage(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  claimId: reclaimAfterExpiry!.claimId,
  afterSequence: 0,
  now: '2026-07-14T04:00:02.000Z',
  limit: 20,
  maxBytes: 1024 * 1024
})
assert.deepEqual(reclaimedPage?.messages.map((message) => message.sequenceNo), [1, 2, 3, 4], '重新压缩必须重新读取旧 checkpoint 覆盖的全部有效消息，不能漏掉 seq3/4')
assert.equal(await recordChatContextCompactionProgress(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  claimId: reclaimAfterExpiry!.claimId,
  throughSequence: 4,
  earliestExpiresAt: reclaimedPage!.earliestExpiresAt!,
  now: '2026-07-14T04:00:03.000Z'
}), true)
const rebuiltMemory = { durableMemory: ['从原消息重建的记忆'], currentGoal: '验证重建完成', recentUserIntent: '保留最近一轮' }
const rebuiltSerialized = JSON.stringify(rebuiltMemory)
await installChatContextCheckpoint(client, {
  claimId: reclaimAfterExpiry!.claimId,
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  sourceRevision: reclaimAfterExpiry!.sourceRevision,
  sourceThroughSequence: 4,
  expiresAt: reclaimedPage!.earliestExpiresAt!,
  payloadDigest: createHash('sha256').update(rebuiltSerialized).digest('hex'),
  estimatedInputTokens: 20,
  activeContextTokens: 30,
  effectiveContextLimitTokens: 1_000,
  requestBodyBytes: Buffer.byteLength(rebuiltSerialized),
  modelId: 'mock-model',
  endpointFamily: 'responses',
  promptVersion: 'expired-checkpoint-rebuild-test-v1',
  entries: [{ kind: 'durable_memory', content: rebuiltMemory, provenance: 'assistant', trustLevel: 'assistant_derived', tokenCount: 20 }],
  now: '2026-07-14T04:00:04.000Z'
})
const rebuiltContext = await loadChatModelContext(client, {
  conversationId: expiredConversationId,
  systemAccountId: expiredOwnerId,
  now: '2026-07-14T04:00:05.000Z',
  maxRows: 20,
  maxBytes: 1024 * 1024
})
assert.deepEqual(rebuiltContext?.suffix.map((message) => message.sequenceNo), [5, 6], '重建 checkpoint 后必须保留最近一整轮原文')

const failureOwnerId = 'context_failure_owner'
const failureConversationId = 'context_failure_conversation'
await createChatConversation(client, {
  id: failureConversationId,
  systemAccountId: failureOwnerId,
  apiKeyId: 'context_failure_key',
  apiKeyNameSnapshot: '失败轮次上下文 Key', maxConversationsPerUser: 1000,
  now: '2026-07-14T00:00:00.000Z'
})
const failed = await acceptChatTurn(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, clientMessageId: 'failed_context_1',
  userContent: 'FAILED_SECRET_PROMPT', model: 'mock-model', now: '2026-07-14T00:01:00.000Z', storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
})
await failChatTurn(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, turnId: failed.turnId,
  assistantContent: '', errorCode: 'mock_failed', errorMessage: 'Mock 上下文失败', traceId: 'failed_trace', now: '2026-07-14T00:01:30.000Z'
})
for (let index = 2; index <= 3; index += 1) {
  const accepted = await acceptChatTurn(client, {
    conversationId: failureConversationId, systemAccountId: failureOwnerId, clientMessageId: `failed_context_${index}`,
    userContent: `成功问题 ${index}`, model: 'mock-model', now: `2026-07-14T00:0${index}:00.000Z`, storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: failureConversationId, systemAccountId: failureOwnerId, turnId: accepted.turnId,
    assistantContent: `成功回答 ${index}`, finishReason: 'stop', traceId: `success_trace_${index}`, now: `2026-07-14T00:0${index}:30.000Z`
  })
}
const pairedModelPage = await loadChatModelContext(client, {
  conversationId: failureConversationId,
  systemAccountId: failureOwnerId,
  now: '2026-07-14T00:04:00.000Z',
  maxRows: 2,
  maxBytes: 512 * 1024
})
assert.deepEqual(pairedModelPage?.suffix.map((message) => message.contentText), ['成功问题 2', '成功回答 2'], '模型上下文装载上限必须只计算完整成功轮次，失败用户消息不能占用行预算')
assert.equal(pairedModelPage?.complete, false, '仍有下一完整轮次时必须明确要求压缩')
assert.equal(await requestChatContextCompaction(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, expectedRevision: 3,
  sourceThroughSequence: 4, now: '2026-07-14T00:04:00.000Z'
}), true)
const failureClaim = await claimChatContextCompaction(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, expectedRevision: 3,
  sourceThroughSequence: 4, now: '2026-07-14T00:04:01.000Z', staleClaimBefore: '2026-07-13T00:00:00.000Z'
})
assert.ok(failureClaim)
const completeOnlyPage = await loadChatCompactionSourcePage(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, claimId: failureClaim!.claimId,
  afterSequence: 0, now: '2026-07-14T00:04:02.000Z', limit: 40, maxBytes: 512 * 1024
})
assert.deepEqual(completeOnlyPage?.messages.map((message) => message.contentText), ['成功问题 2', '成功回答 2'], '压缩来源只能包含完整成功轮次')
assert.doesNotMatch(JSON.stringify(completeOnlyPage), /FAILED_SECRET_PROMPT/)
assert.equal(await recordChatContextCompactionProgress(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, claimId: failureClaim!.claimId,
  throughSequence: 4, earliestExpiresAt: completeOnlyPage!.earliestExpiresAt!, now: '2026-07-14T00:10:00.000Z'
}), true)
const renewedClaimedAt = (database.prepare('SELECT context_claimed_at FROM chat_conversations WHERE id = ?').get(failureConversationId) as { context_claimed_at?: unknown }).context_claimed_at
assert.equal(renewedClaimedAt, '2026-07-14T00:10:00.000Z', '每页压缩进度必须续租认领时间')

const acceptedDuringCompaction = await acceptChatTurn(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, clientMessageId: 'accepted_during_compaction',
  userContent: '压缩期间继续提问', model: 'mock-model', now: '2026-07-14T00:11:00.000Z', storageQuotaBytes: 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
})
assert.ok(acceptedDuringCompaction.turnId)
const invalidatedClaimHead = await getChatContextHead(client, { conversationId: failureConversationId, systemAccountId: failureOwnerId })
assert.equal(invalidatedClaimHead?.contextState, 'compact_pending', '接受新消息必须原子失效旧压缩认领且不阻塞发送')
assert.equal((database.prepare('SELECT context_claim_id FROM chat_conversations WHERE id = ?').get(failureConversationId) as { context_claim_id?: unknown }).context_claim_id, null)
assert.equal((database.prepare('SELECT context_attempt_count FROM chat_conversations WHERE id = ?').get(failureConversationId) as { context_attempt_count?: unknown }).context_attempt_count, 0, '被新消息正常打断的压缩不能计入连续失败次数')
await completeChatTurn(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, turnId: acceptedDuringCompaction.turnId,
  assistantContent: '继续回答', finishReason: 'stop', traceId: 'during_compaction_trace', now: '2026-07-14T00:11:30.000Z'
})
const resumedClaim = await claimChatContextCompaction(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, expectedRevision: 4,
  sourceThroughSequence: 4, now: '2026-07-14T00:12:00.000Z', staleClaimBefore: '2026-07-13T00:00:00.000Z'
})
assert.equal(resumedClaim?.attemptCount, 1, '正常中断后重新 claim 仍应是第一次可能失败的尝试')
assert.equal(await releaseChatContextCompactionClaim(client, {
  conversationId: failureConversationId, systemAccountId: failureOwnerId, claimId: resumedClaim!.claimId, now: '2026-07-14T00:12:01.000Z'
}), true)

const retryOwnerId = 'context_retry_owner'
const retryConversationId = 'context_retry_conversation'
await createChatConversation(client, { id: retryConversationId, systemAccountId: retryOwnerId, apiKeyId: 'retry_key', apiKeyNameSnapshot: '重试 Key', maxConversationsPerUser: 1000, now: '2026-07-14T01:00:00.000Z' })
for (let index = 1; index <= 2; index += 1) {
  const accepted = await acceptChatTurn(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, clientMessageId: `retry_${index}`, userContent: `重试问题 ${index}`, model: 'mock-model', now: `2026-07-14T01:0${index}:00.000Z`, storageQuotaBytes: 1024 * 1024 , retentionDays: 7, maxTurnsPerConversation: 1000})
  await completeChatTurn(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, turnId: accepted.turnId, assistantContent: `重试回答 ${index}`, finishReason: 'stop', traceId: `retry_trace_${index}`, now: `2026-07-14T01:0${index}:30.000Z` })
}
await requestChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T01:03:00.000Z' })
const retryClaim = await claimChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T01:03:01.000Z', staleClaimBefore: '2026-07-13T00:00:00.000Z' })
assert.ok(retryClaim)
await failChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, claimId: retryClaim!.claimId, errorCode: 'temporary_failure', retryAt: '2026-07-14T01:30:00.000Z', now: '2026-07-14T01:03:02.000Z' })
const failedHead = await getChatContextHead(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId })
assert.deepEqual({
  contextState: failedHead?.contextState,
  contextRetryAt: failedHead?.contextRetryAt,
  contextAttemptCount: failedHead?.contextAttemptCount,
  contextErrorCode: failedHead?.contextErrorCode
}, {
  contextState: 'compact_failed',
  contextRetryAt: '2026-07-14T01:30:00.000Z',
  contextAttemptCount: 1,
  contextErrorCode: 'temporary_failure'
}, '压缩失败 head 必须返回安全错误码、退避时间和连续尝试次数')
assert.equal(await requestChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T01:10:00.000Z' }), false, 'retry_at 到期前不能绕过压缩退避')
assert.equal(await claimChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T01:10:00.000Z', staleClaimBefore: '2026-07-13T00:00:00.000Z' }), undefined)
assert.equal(await requestChatContextCompaction(client, { conversationId: retryConversationId, systemAccountId: retryOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T01:30:00.000Z' }), true, 'retry_at 到期后应恢复压缩调度')

const oversizedOwnerId = 'context_oversized_pair_owner'
const oversizedConversationId = 'context_oversized_pair_conversation'
await createChatConversation(client, { id: oversizedConversationId, systemAccountId: oversizedOwnerId, apiKeyId: 'oversized_key', apiKeyNameSnapshot: '超大轮次 Key', maxConversationsPerUser: 1000, now: '2026-07-14T02:00:00.000Z' })
const oversizedUser = 'u'.repeat(190 * 1024)
const oversizedAssistant = 'a'.repeat(190 * 1024)
const oversizedFirst = await acceptChatTurn(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, clientMessageId: 'oversized_1', userContent: oversizedUser, model: 'mock-model', now: '2026-07-14T02:01:00.000Z', storageQuotaBytes: 8 * 1024 * 1024 , retentionDays: 7, maxTurnsPerConversation: 1000})
await completeChatTurn(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, turnId: oversizedFirst.turnId, assistantContent: oversizedAssistant, finishReason: 'stop', traceId: 'oversized_1_trace', now: '2026-07-14T02:01:30.000Z' })
const oversizedTail = await acceptChatTurn(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, clientMessageId: 'oversized_2', userContent: '保留最近轮次', model: 'mock-model', now: '2026-07-14T02:02:00.000Z', storageQuotaBytes: 8 * 1024 * 1024 , retentionDays: 7, maxTurnsPerConversation: 1000})
await completeChatTurn(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, turnId: oversizedTail.turnId, assistantContent: '最近回答', finishReason: 'stop', traceId: 'oversized_2_trace', now: '2026-07-14T02:02:30.000Z' })
assert.equal(await requestChatContextCompaction(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T02:03:00.000Z' }), true)
const oversizedClaim = await claimChatContextCompaction(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, expectedRevision: 2, sourceThroughSequence: 2, now: '2026-07-14T02:03:01.000Z', staleClaimBefore: '2026-07-13T00:00:00.000Z' })
assert.ok(oversizedClaim)
const oversizedPage = await loadChatCompactionSourcePage(client, { conversationId: oversizedConversationId, systemAccountId: oversizedOwnerId, claimId: oversizedClaim!.claimId, afterSequence: 0, now: '2026-07-14T02:03:02.000Z', limit: 40, maxBytes: 512 * 1024 })
assert.deepEqual(oversizedPage?.messages.map((message) => [message.sequenceNo, message.role]), [[1, 'user'], [2, 'assistant']], '单个完整轮次超过页预算时必须受控整轮装入，不能拆成半轮')
assert.equal(oversizedPage?.hasMore, false)
assert((oversizedPage?.loadedBytes ?? 0) > 512 * 1024, '受控放宽必须显式反映实际装载字节')

database.close()
console.log('AI 问答 checkpoint + recent suffix repository 回归通过')
