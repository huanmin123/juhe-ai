import assert from 'node:assert/strict'
import http from 'node:http'
import { DatabaseSync } from 'node:sqlite'

import { compactChatContextOnce } from '../../modules/chat/chat-context-compaction.js'
import { ChatModelContextError, loadChatTransportHistory } from '../../modules/chat/chat-model-context.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyChatSchema } from '../../storage/schema.js'
import { acceptChatTurn, completeChatTurn, createChatConversation, failChatTurn } from '../../storage/chat.repository.js'
import { getChatContextHead, loadChatModelContext } from '../../storage/chat-context.repository.js'

const rememberedCode = '海王星-7349'
let compactionCalls = 0
let emptyRequiredCompactionCalls = 0
let abortedCompactionRequest = false
const server = http.createServer((req, res) => {
  const chunks: Buffer[] = []
  req.on('data', (chunk: Buffer) => chunks.push(chunk))
  req.on('end', () => {
    compactionCalls += 1
    const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as { input?: unknown }
    const serializedInput = JSON.stringify(body.input)
    if (serializedInput.includes('ABORT_COMPACTION_PAGE')) {
      req.once('close', () => { abortedCompactionRequest = true })
      return
    }
    const emptyRequiredFields = serializedInput.includes('EMPTY_REQUIRED_PAGE')
    if (emptyRequiredFields) emptyRequiredCompactionCalls += 1
    else assert.match(serializedInput, /海王星-7349/, '每次压缩请求都必须携带来源消息或已有长期记忆')
    const summary = JSON.stringify(emptyRequiredFields ? {
      durableMemory: [], currentGoal: '', constraints: [], decisions: [], completed: [], pending: [],
      importantToolResults: [], imageMemories: [], recentUserIntent: '', uncertainties: []
    } : {
      durableMemory: [`用户项目代号是${rememberedCode}`],
      currentGoal: compactionCalls === 1 ? '' : '持续维护项目发布计划',
      constraints: ['不能遗忘项目代号'],
      decisions: ['使用检查点压缩旧对话'],
      completed: ['记录项目代号'],
      pending: ['继续回答后续问题'],
      importantToolResults: [],
      imageMemories: [],
      recentUserIntent: compactionCalls === 1 ? '' : '继续讨论项目发布计划',
      uncertainties: []
    })
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ output_text: summary, output: [{ type: 'message', content: [{ type: 'output_text', text: summary }] }] }))
  })
})
await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
const address = server.address()
if (!address || typeof address === 'string') throw new Error('Mock 压缩服务监听失败')

const database = new DatabaseSync(':memory:')
applyChatSchema(database)
const client = createSqliteDatabaseClient(database)
const conversationId = 'context_compaction_conversation'
const ownerId = 'context_compaction_owner'
await createChatConversation(client, {
  id: conversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_compaction_key',
  apiKeyNameSnapshot: '压缩测试 Key', maxConversationsPerUser: 1000,
  now: '2026-07-13T01:00:00.000Z'
})
for (let index = 1; index <= 4; index += 1) {
  const now = `2026-07-13T01:0${index}:00.000Z`
  const content = index === 1
    ? `请记住我的项目代号是 ${rememberedCode}。${'旧上下文填充'.repeat(2_000)}`
    : `第 ${index} 轮上下文。${'历史过程填充'.repeat(2_000)}`
  const accepted = await acceptChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    clientMessageId: `context_compaction_client_${index}`,
    userContent: content,
    model: 'context-compaction-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: `已处理第 ${index} 轮。${'旧回答过程'.repeat(1_000)}`,
    finishReason: 'stop',
    traceId: `context_compaction_trace_${index}`,
    now
  })
}

const result = await compactChatContextOnce({
  client,
  conversationId,
  systemAccountId: ownerId,
  apiKeySecret: 'context-compaction-secret',
  gatewayBaseUrl: `http://127.0.0.1:${address.port}`,
  model: 'context-compaction-model',
  protocol: 'responses',
  effectiveContextLimitTokens: 32_000
})
assert.equal(result.status, 'installed')
assert.ok(result.status === 'installed' && result.afterBytes < result.beforeBytes)
assert.ok(compactionCalls >= 1)

const stored = await loadChatModelContext(client, {
  conversationId,
  systemAccountId: ownerId,
  now: '2026-07-13T01:10:00.000Z',
  maxRows: 100,
  maxBytes: 4 * 1024 * 1024
})
assert.match(JSON.stringify(stored?.entries), new RegExp(rememberedCode))
assert.deepEqual(stored?.suffix.map((message) => message.sequenceNo), [7, 8], '压缩后只能保留最近一整轮原文尾部')
assert.equal(stored?.checkpoint?.expiresAt, '2026-07-20T01:01:00.000Z', 'checkpoint 到期时间必须严格继承最早压缩来源消息')
assert.doesNotMatch(JSON.stringify(stored?.entries), /旧上下文填充旧上下文填充旧上下文填充/, 'checkpoint 不得继续保存旧大段原文')

const transport = await loadChatTransportHistory({
  client,
  conversationId,
  systemAccountId: ownerId,
  protocol: 'responses',
  now: '2026-07-13T01:10:00.000Z'
})
assert.match(JSON.stringify(transport.history), new RegExp(rememberedCode), '后续模型请求必须重新携带压缩后的关键记忆')
assert.match(JSON.stringify(transport.history), /第 4 轮上下文/, '后续模型请求必须保留最近原文尾部')
const head = await getChatContextHead(client, { conversationId, systemAccountId: ownerId })
assert.equal(head?.contextState, 'ready')
assert.equal(head?.contextRevision, 5)

for (let index = 5; index <= 6; index += 1) {
  const now = `2026-07-13T01:1${index}:00.000Z`
  const accepted = await acceptChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    clientMessageId: `context_compaction_client_${index}`,
    userContent: `第 ${index} 轮只讨论发布步骤，不再重复项目代号`,
    model: 'context-compaction-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: `已处理第 ${index} 轮`,
    finishReason: 'stop',
    traceId: `context_compaction_trace_${index}`,
    now
  })
}
const secondResult = await compactChatContextOnce({
  client,
  conversationId,
  systemAccountId: ownerId,
  apiKeySecret: 'context-compaction-secret',
  gatewayBaseUrl: `http://127.0.0.1:${address.port}`,
  model: 'context-compaction-model',
  protocol: 'responses',
  effectiveContextLimitTokens: 32_000
})
assert.equal(secondResult.status, 'installed')
const twiceCompacted = await loadChatModelContext(client, {
  conversationId,
  systemAccountId: ownerId,
  now: '2026-07-13T01:20:00.000Z',
  maxRows: 100,
  maxBytes: 4 * 1024 * 1024
})
assert.match(JSON.stringify(twiceCompacted?.entries), new RegExp(rememberedCode), '第二次压缩必须继承第一次 checkpoint 的长期记忆')
assert.deepEqual(twiceCompacted?.suffix.map((message) => message.sequenceNo), [11, 12])

const emptyRequiredConversationId = 'context_empty_required_pages'
await createChatConversation(client, {
  id: emptyRequiredConversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_compaction_key',
  apiKeyNameSnapshot: '多页空字段降级测试', maxConversationsPerUser: 1000,
  now: '2026-07-13T01:30:00.000Z'
})
for (let index = 1; index <= 22; index += 1) {
  const now = new Date(Date.UTC(2026, 6, 13, 1, 30, index)).toISOString()
  const accepted = await acceptChatTurn(client, {
    conversationId: emptyRequiredConversationId,
    systemAccountId: ownerId,
    clientMessageId: `empty_required_${index}`,
    userContent: `EMPTY_REQUIRED_PAGE INTENT_${String(index).padStart(2, '0')} ${'多页历史填充'.repeat(200)}`,
    model: 'context-compaction-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: emptyRequiredConversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: `已处理 INTENT_${String(index).padStart(2, '0')} ${'多页回答填充'.repeat(100)}`,
    finishReason: 'stop',
    traceId: `empty_required_trace_${index}`,
    now
  })
}
const emptyRequiredResult = await compactChatContextOnce({
  client,
  conversationId: emptyRequiredConversationId,
  systemAccountId: ownerId,
  apiKeySecret: 'context-compaction-secret',
  gatewayBaseUrl: `http://127.0.0.1:${address.port}`,
  model: 'context-compaction-model',
  protocol: 'responses',
  effectiveContextLimitTokens: 32_000
})
assert.equal(emptyRequiredResult.status, 'installed')
assert.equal(emptyRequiredCompactionCalls, 2, '42 条压缩来源消息必须分为两个完整轮次页面')
const emptyRequiredStored = await loadChatModelContext(client, {
  conversationId: emptyRequiredConversationId,
  systemAccountId: ownerId,
  now: '2026-07-13T01:40:00.000Z',
  maxRows: 100,
  maxBytes: 4 * 1024 * 1024
})
const emptyRequiredTaskState = emptyRequiredStored?.entries.find((entry) => entry.kind === 'task_state')
assert.match(JSON.stringify(emptyRequiredTaskState?.content), /INTENT_21/, '多页摘要连续缺失必填字段时必须使用最后一页的最近用户意图')
assert.doesNotMatch(JSON.stringify(emptyRequiredTaskState?.content), /INTENT_20/, '第一页降级值不能永久遮蔽第二页用户意图')

const failedConversationId = 'context_failed_pair_conversation'
await createChatConversation(client, {
  id: failedConversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_compaction_key',
  apiKeyNameSnapshot: '失败轮配对测试', maxConversationsPerUser: 1000,
  now: '2026-07-13T02:00:00.000Z'
})
const failedTurn = await acceptChatTurn(client, {
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  clientMessageId: 'failed_pair_1',
  userContent: '这轮会失败',
  model: 'context-compaction-model',
  now: '2026-07-13T02:01:00.000Z',
  storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
})
await failChatTurn(client, {
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  turnId: failedTurn.turnId,
  assistantContent: '',
  errorCode: 'mock_failed',
  now: '2026-07-13T02:01:30.000Z'
})
const successfulTurn = await acceptChatTurn(client, {
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  clientMessageId: 'failed_pair_2',
  userContent: '失败后这轮必须保留',
  model: 'context-compaction-model',
  now: '2026-07-13T02:02:00.000Z',
  storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
})
await completeChatTurn(client, {
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  turnId: successfulTurn.turnId,
  assistantContent: '失败后的成功回答',
  finishReason: 'stop',
  traceId: 'failed_pair_trace',
  now: '2026-07-13T02:02:30.000Z'
})
const afterFailedTurn = await loadChatTransportHistory({
  client,
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  protocol: 'responses',
  now: '2026-07-13T02:03:00.000Z'
})
assert.deepEqual(afterFailedTurn.history.map((message) => message.content), [[{ type: 'input_text', text: '失败后这轮必须保留' }], '失败后的成功回答'], 'Responses 历史用户纯文本必须固定为 input_text block，且失败轮不能打乱后续成功轮的配对')
const afterFailedTurnChat = await loadChatTransportHistory({
  client,
  conversationId: failedConversationId,
  systemAccountId: ownerId,
  protocol: 'chat_completions',
  now: '2026-07-13T02:03:00.000Z'
})
assert.deepEqual(afterFailedTurnChat.history.map((message) => message.content), ['失败后这轮必须保留', '失败后的成功回答'], 'Chat Completions 历史纯文本必须继续使用 string')

const oversizedConversationId = 'context_row_limit_conversation'
await createChatConversation(client, {
  id: oversizedConversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_compaction_key',
  apiKeyNameSnapshot: '行数上限测试', maxConversationsPerUser: 1000,
  now: '2026-07-13T03:00:00.000Z'
})
for (let index = 1; index <= 258; index += 1) {
  const now = new Date(Date.UTC(2026, 6, 13, 3, 0, index)).toISOString()
  const accepted = await acceptChatTurn(client, {
    conversationId: oversizedConversationId,
    systemAccountId: ownerId,
    clientMessageId: `row_limit_${index}`,
    userContent: index === 1 ? `请永久记住 ${rememberedCode}` : `短消息 ${index}`,
    model: 'context-compaction-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: oversizedConversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: `短回答 ${index}`,
    finishReason: 'stop',
    traceId: `row_limit_trace_${index}`,
    now
  })
}
await assert.rejects(
  loadChatTransportHistory({ client, conversationId: oversizedConversationId, systemAccountId: ownerId, protocol: 'responses', now: '2026-07-13T04:00:00.000Z' }),
  (error: unknown) => error instanceof ChatModelContextError && error.reason === 'load_limit',
  '超过 512 条消息时应返回可触发预压缩的明确原因'
)
const rowLimitCompaction = await compactChatContextOnce({
  client,
  conversationId: oversizedConversationId,
  systemAccountId: ownerId,
  apiKeySecret: 'context-compaction-secret',
  gatewayBaseUrl: `http://127.0.0.1:${address.port}`,
  model: 'context-compaction-model',
  protocol: 'responses',
  effectiveContextLimitTokens: 32_000
})
assert.equal(rowLimitCompaction.status, 'installed', '本地装载超过 512 条后仍必须能通过分页来源完成压缩')
const rowLimitTransport = await loadChatTransportHistory({ client, conversationId: oversizedConversationId, systemAccountId: ownerId, protocol: 'responses', now: '2026-07-13T04:00:00.000Z' })
assert.match(JSON.stringify(rowLimitTransport.history), new RegExp(rememberedCode))

const abortConversationId = 'context_compaction_abort_conversation'
await createChatConversation(client, {
  id: abortConversationId,
  systemAccountId: ownerId,
  apiKeyId: 'context_compaction_key',
  apiKeyNameSnapshot: '压缩中断测试', maxConversationsPerUser: 1000,
  now: '2026-07-13T05:00:00.000Z'
})
for (let index = 1; index <= 2; index += 1) {
  const now = new Date(Date.UTC(2026, 6, 13, 5, index)).toISOString()
  const accepted = await acceptChatTurn(client, {
    conversationId: abortConversationId,
    systemAccountId: ownerId,
    clientMessageId: `abort_compaction_${index}`,
    userContent: `ABORT_COMPACTION_PAGE ${index} ${'等待中断的上下文'.repeat(500)}`,
    model: 'context-compaction-model',
    now,
    storageQuotaBytes: 64 * 1024 * 1024, retentionDays: 7, maxTurnsPerConversation: 1000
  })
  await completeChatTurn(client, {
    conversationId: abortConversationId,
    systemAccountId: ownerId,
    turnId: accepted.turnId,
    assistantContent: `等待中断的回答 ${index}`,
    finishReason: 'stop',
    traceId: `abort_compaction_trace_${index}`,
    now
  })
}
const abortController = new AbortController()
const abortStartedAt = Date.now()
setTimeout(() => abortController.abort(new Error('shared_budget_expired')), 25)
const abortedCompaction = await compactChatContextOnce({
  client,
  conversationId: abortConversationId,
  systemAccountId: ownerId,
  apiKeySecret: 'context-compaction-secret',
  gatewayBaseUrl: `http://127.0.0.1:${address.port}`,
  model: 'context-compaction-model',
  protocol: 'responses',
  effectiveContextLimitTokens: 32_000,
  signal: abortController.signal,
  requestTimeoutMs: 50
})
assert.equal(abortedCompaction.status, 'failed')
assert.ok(Date.now() - abortStartedAt < 2_000, '共享 budget 必须中断分页模型 fetch，不能等待固定 120 秒')
assert.equal(abortedCompactionRequest, true, '中断信号必须实际关闭分页模型 HTTP 请求')

await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
database.close()
console.log('AI 问答主动压缩、检查点安装与关键记忆恢复回归通过')
