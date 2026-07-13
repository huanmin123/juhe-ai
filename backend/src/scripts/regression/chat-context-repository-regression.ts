import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { acceptChatTurn, completeChatTurn, createChatConversation } from '../../storage/chat.repository.js'
import {
  claimChatContextCompaction,
  cleanupExpiredChatContextCheckpoints,
  getChatContextHead,
  installChatContextCheckpoint,
  loadChatCompactionSourcePage,
  loadChatModelContext,
  recordChatContextCompactionProgress,
  recoverStaleChatContextCompactions,
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
  apiKeyNameSnapshot: '上下文 Key',
  now: '2026-07-13T00:00:00.000Z'
})

for (let index = 1; index <= 3; index += 1) {
  const now = `2026-07-13T00:0${index}:00.000Z`
  const accepted = await acceptChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    clientMessageId: `context_client_${index}`,
    userContent: index === 1 ? '请记住我的项目代号是蓝鲸' : `第 ${index} 轮问题`,
    model: 'context-model',
    now,
    storageQuotaBytes: 1024 * 1024
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
  storageQuotaBytes: 1024 * 1024
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

database.close()
console.log('AI 问答 checkpoint + recent suffix repository 回归通过')
