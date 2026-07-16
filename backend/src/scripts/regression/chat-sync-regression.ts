import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { withRequestAuthContext } from '../../modules/auth/request-context.js'

const tempRoot = join(tmpdir(), `juhe-ai-chat-sync-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.processRole = 'db-service'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')

const { closeStorageDatabases } = await import('../../storage/database.js')
const { getChatDatabaseClient } = await import('../../storage/chat-client.js')
const {
  acceptChatTurn,
  completeChatTurn,
  createChatConversation,
  getChatConversationSyncHead,
  listChatMessages
} = await import('../../storage/chat.repository.js')
const { chatRouter } = await import('../../modules/chat/chat.routes.js')

const client = await getChatDatabaseClient()
const ownerId = 'sys_chat_sync_owner'
const otherOwnerId = 'sys_chat_sync_other'
const conversation = await createChatConversation(client, {
  id: 'chat_sync_conversation',
  systemAccountId: ownerId,
  apiKeyId: 'key_sync',
  apiKeyNameSnapshot: '同步 Key',
  now: '2026-07-16T00:00:00.000Z',
  maxConversationsPerUser: 50
})

try {
  const initial = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:00:01.000Z'
  })
  assert.deepEqual(initial, {
    conversationId: conversation.id,
    messageRevision: 0,
    lastSequenceNo: 0,
    activeTurn: undefined,
    tail: []
  })
  let syncQueryCount = 0
  await getChatConversationSyncHead({
    ...client,
    query: async <T extends object>(sql: string, params?: readonly unknown[]) => {
      syncQueryCount += 1
      return client.query<T>(sql, params)
    },
    one: async () => { throw new Error('同步 head 不得退化为第二次裸查询') }
  }, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:00:01.000Z'
  })
  assert.equal(syncQueryCount, 1, 'revision、active 和 tail 必须由单条 SQL 的一致快照返回')

  const first = await acceptTurn('sync_client_1', '第一问', '2026-07-16T00:01:00.000Z')
  const acceptedHead = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:01:01.000Z'
  })
  assert.equal(acceptedHead?.messageRevision, 1)
  assert.equal(acceptedHead?.lastSequenceNo, first.assistantMessage.sequenceNo)
  assert.deepEqual(acceptedHead?.activeTurn, {
    turnId: first.turnId,
    assistantMessageId: first.assistantMessage.id,
    startedAt: '2026-07-16T00:01:00.000Z'
  })
  assert.equal(acceptedHead?.tail.length, 2)
  assertNoMessageBodies(acceptedHead)

  await completeChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    turnId: first.turnId,
    assistantContent: '第一答正文',
    contentBlocks: [{ type: 'reasoning', text: '第一答推理' }],
    finishReason: 'stop',
    traceId: 'trace_sync_1',
    now: '2026-07-16T00:02:00.000Z'
  })
  const completedHead = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:02:01.000Z'
  })
  assert.equal(completedHead?.messageRevision, 2)
  assert.equal(completedHead?.activeTurn, undefined)
  assert.equal(completedHead?.tail[1]?.status, 'completed')
  assertNoMessageBodies(completedHead)

  const second = await acceptTurn('sync_client_2', '第二问', '2026-07-16T00:03:00.000Z')
  const appended = await listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    afterSequenceNo: first.assistantMessage.sequenceNo,
    limit: 100,
    now: '2026-07-16T00:03:01.000Z'
  })
  assert.deepEqual(appended.map((message) => message.id), [second.userMessage.id, second.assistantMessage.id])

  const streamingAssistant = await listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    fromSequenceNo: second.assistantMessage.sequenceNo,
    limit: 100,
    now: '2026-07-16T00:03:01.000Z'
  })
  assert.deepEqual(streamingAssistant.map((message) => [message.id, message.status, message.contentText]), [
    [second.assistantMessage.id, 'streaming', '']
  ])
  await completeChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    turnId: second.turnId,
    assistantContent: '第二答正文',
    finishReason: 'stop',
    traceId: 'trace_sync_2',
    now: '2026-07-16T00:04:00.000Z'
  })
  const refreshedAssistant = await listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    fromSequenceNo: second.assistantMessage.sequenceNo,
    limit: 100,
    now: '2026-07-16T00:04:01.000Z'
  })
  assert.deepEqual(refreshedAssistant.map((message) => [message.id, message.status, message.contentText]), [
    [second.assistantMessage.id, 'completed', '第二答正文']
  ])

  const before = await listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    beforeSequenceNo: second.userMessage.sequenceNo,
    limit: 100,
    now: '2026-07-16T00:04:01.000Z'
  })
  assert.deepEqual(before.map((message) => message.sequenceNo), [1, 2])
  await assert.rejects(listChatMessages(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    beforeSequenceNo: 3,
    afterSequenceNo: 1,
    limit: 100,
    now: '2026-07-16T00:04:01.000Z'
  }), /消息游标只能指定一个/)

  const replacement = await acceptTurn('sync_client_2_replace', '第二问修正', '2026-07-16T00:05:00.000Z', second.turnId)
  assert.equal(replacement.userMessage.sequenceNo, second.userMessage.sequenceNo)
  assert.equal(replacement.assistantMessage.sequenceNo, second.assistantMessage.sequenceNo)
  assert.notEqual(replacement.userMessage.id, second.userMessage.id)
  const replacementHead = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:05:01.000Z'
  })
  assert.equal(replacementHead?.tail.length, 2)
  assert.deepEqual(replacementHead?.tail.map((message) => message.id), [replacement.userMessage.id, replacement.assistantMessage.id])
  assert.deepEqual(replacementHead?.activeTurn, {
    turnId: replacement.turnId,
    assistantMessageId: replacement.assistantMessage.id,
    startedAt: '2026-07-16T00:05:00.000Z'
  })
  assertNoMessageBodies(replacementHead)

  assert.equal(await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: otherOwnerId,
    now: '2026-07-16T00:05:01.000Z'
  }), undefined)
  const expiredHead = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-24T00:00:00.000Z'
  })
  assert.equal(expiredHead?.lastSequenceNo, 0)
  assert.deepEqual(expiredHead?.tail, [])
  assert.equal(expiredHead?.activeTurn, undefined)

  const server = http.createServer(createTestApp())
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  try {
    const address = server.address()
    assert(address && typeof address === 'object')
    const baseUrl = `http://127.0.0.1:${address.port}`
    for (const query of [
      'beforeSequenceNo=3&afterSequenceNo=1',
      'beforeSequenceNo=3&fromSequenceNo=1',
      'afterSequenceNo=1&fromSequenceNo=1',
      'beforeSequenceNo=3&afterSequenceNo=1&fromSequenceNo=1'
    ]) {
      const response = await fetch(`${baseUrl}/conversations/${conversation.id}/messages?${query}`, { headers: { 'x-test-owner': ownerId } })
      assert.equal(response.status, 400, `多游标 query 必须返回 400：${query}`)
    }
    const equalKnown = await getJson(`${baseUrl}/conversations/${conversation.id}/sync?knownRevision=${replacementHead?.messageRevision}`, ownerId)
    assert.equal(equalKnown.response.status, 200)
    assert.equal(equalKnown.payload.data.unchanged, true)
    assert.equal(equalKnown.payload.data.messageRevision, replacementHead?.messageRevision)
    assert.deepEqual(equalKnown.payload.data.activeTurn, replacementHead?.activeTurn, 'knownRevision 相等时仍必须返回 active identity')
    assert.equal(typeof equalKnown.payload.data.serverTime, 'string')
    assertNoMessageBodies(equalKnown.payload.data)
    const higherKnown = await getJson(`${baseUrl}/conversations/${conversation.id}/sync?knownRevision=999`, ownerId)
    assert.equal(higherKnown.payload.data.unchanged, false)
    const forbidden = await getJson(`${baseUrl}/conversations/${conversation.id}/sync?knownRevision=0`, otherOwnerId)
    assert.equal(forbidden.response.status, 404)
  } finally {
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }

  await client.execute(`UPDATE chat_messages SET expires_at = ? WHERE id = ?`, [
    '2026-07-16T00:04:30.000Z',
    replacement.assistantMessage.id
  ])
  const incompleteLatestTurnHead = await getChatConversationSyncHead(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    now: '2026-07-16T00:05:01.000Z'
  })
  assert.deepEqual(
    incompleteLatestTurnHead?.tail.map((message) => [message.turnId, message.id]),
    [
      [first.turnId, first.userMessage.id],
      [first.turnId, first.assistantMessage.id]
    ],
    '最新轮次只剩一条未过期消息时必须整体回退上一完整轮次，不能跨轮拼接 tail'
  )
  assert.equal(incompleteLatestTurnHead?.activeTurn, undefined, 'active assistant 已过期时不能恢复 streaming identity')

  console.log('AI 问答 revision 同步回归通过')
} finally {
  closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function acceptTurn(clientMessageId: string, userContent: string, now: string, replaceTurnId?: string) {
  return acceptChatTurn(client, {
    conversationId: conversation.id,
    systemAccountId: ownerId,
    clientMessageId,
    userContent,
    model: 'mock-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024,
    retentionDays: 7,
    maxTurnsPerConversation: 50,
    ...(replaceTurnId ? { replaceTurnId } : {})
  })
}

function assertNoMessageBodies(value: unknown): void {
  const serialized = JSON.stringify(value)
  for (const forbidden of ['contentText', 'contentBlocks', 'reasoning', 'tool_call', '第一答正文', '第一答推理']) {
    assert.equal(serialized.includes(forbidden), false, `同步 head 不得包含 ${forbidden}`)
  }
}

function createTestApp() {
  const app = express()
  app.use(express.json())
  app.use((req, _res, next) => {
    const owner = String(req.header('x-test-owner') ?? ownerId)
    withRequestAuthContext({
      systemAccountId: owner,
      role: 'user',
      username: owner,
      displayName: owner,
      mustChangePassword: false,
      sessionId: `session_${owner}`
    }, next)
  })
  app.use(chatRouter)
  app.use((error: unknown, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
    res.status(500).json({ message: error instanceof Error ? error.message : String(error) })
  })
  return app
}

async function getJson(url: string, owner: string): Promise<{ response: Response; payload: any }> {
  const response = await fetch(url, { headers: { 'x-test-owner': owner } })
  return { response, payload: await response.json() }
}
