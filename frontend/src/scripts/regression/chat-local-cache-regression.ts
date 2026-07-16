import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  ChatLocalCache,
  calculateChatCacheBudget,
  cloneVisibleChatMessage,
  type ChatCacheConversationHead,
  type ChatCacheEvictionCursor,
  type ChatCachePutContext,
  type ChatLocalCacheStorageAdapter,
  type ChatRunningTurn
} from '../../views/chat/chatLocalCache'
import type { ChatMessage } from '../../types/domain/chat'

class MemoryAdapter implements ChatLocalCacheStorageAdapter {
  readonly heads = new Map<string, ChatCacheConversationHead>()
  readonly messages = new Map<string, ChatMessage>()
  readonly running = new Map<string, ChatRunningTurn>()
  failWith?: Error
  putFailures: Error[] = []
  putMessagesCalls = 0

  private key(account: string, conversation: string): string { return `${account}\u0000${conversation}` }
  private messageKey(account: string, conversation: string, sequenceNo: number): string { return `${this.key(account, conversation)}\u0000${sequenceNo}` }
  private fail(): void { if (this.failWith) throw this.failWith }

  async readConversation(account: string, conversation: string) {
    this.fail()
    const prefix = `${this.key(account, conversation)}\u0000`
    return {
      head: this.heads.get(this.key(account, conversation)),
      messages: [...this.messages.entries()].filter(([key]) => key.startsWith(prefix)).map(([, value]) => structuredClone(value)).sort((a, b) => a.sequenceNo - b.sequenceNo),
      runningTurn: this.running.get(this.key(account, conversation))
    }
  }

  async putHead(head: ChatCacheConversationHead): Promise<void> { this.fail(); this.heads.set(this.key(head.systemAccountId, head.conversationId), structuredClone(head)) }
  async putMessages(account: string, conversation: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCacheConversationHead> {
    this.putMessagesCalls += 1
    const putFailure = this.putFailures.shift()
    if (putFailure) throw putFailure
    this.fail()
    const key = this.key(account, conversation)
    const head = this.heads.get(key) ?? { systemAccountId: account, conversationId: conversation, messageRevision: 0, lastAccessAt: context.now, byteSize: 0 }
    let bytes = head.byteSize
    for (const message of messages) {
      const messageKey = this.messageKey(account, conversation, message.sequenceNo)
      const old = this.messages.get(messageKey)
      if (old) bytes -= Buffer.byteLength(JSON.stringify(old))
      this.messages.set(messageKey, structuredClone(message))
      bytes += Buffer.byteLength(JSON.stringify(message))
    }
    const next = { ...head, byteSize: bytes, lastAccessAt: context.now }
    this.heads.set(key, next)
    return structuredClone(next)
  }
  async deleteFromSequence(account: string, conversation: string, sequenceNo: number): Promise<void> {
    this.fail(); const prefix = `${this.key(account, conversation)}\u0000`
    for (const [key, value] of this.messages) if (key.startsWith(prefix) && value.sequenceNo >= sequenceNo) this.messages.delete(key)
  }
  async deleteConversation(account: string, conversation: string): Promise<void> {
    this.fail(); const prefix = `${this.key(account, conversation)}\u0000`
    for (const key of this.messages.keys()) if (key.startsWith(prefix)) this.messages.delete(key)
    this.heads.delete(this.key(account, conversation)); this.running.delete(this.key(account, conversation))
  }
  async putRunningTurn(turn: ChatRunningTurn): Promise<void> { this.fail(); this.running.set(this.key(turn.systemAccountId, turn.conversationId), structuredClone(turn)) }
  async removeRunningTurn(account: string, conversation: string): Promise<void> { this.fail(); this.running.delete(this.key(account, conversation)) }
  async touch(account: string, conversation: string, now: number): Promise<void> { this.fail(); const key = this.key(account, conversation); const head = this.heads.get(key); if (head) this.heads.set(key, { ...head, lastAccessAt: now }) }
  async clearAccount(account: string): Promise<void> { this.fail(); for (const head of [...this.heads.values()]) if (head.systemAccountId === account) await this.deleteConversation(account, head.conversationId) }
  async listEvictionCandidates(limit: number, after?: ChatCacheEvictionCursor): Promise<ChatCacheConversationHead[]> {
    this.fail()
    const ordered = [...this.heads.values()].sort((a, b) => a.lastAccessAt - b.lastAccessAt || a.systemAccountId.localeCompare(b.systemAccountId) || a.conversationId.localeCompare(b.conversationId))
    const start = after ? ordered.findIndex((head) => head.lastAccessAt === after.lastAccessAt && head.systemAccountId === after.systemAccountId && head.conversationId === after.conversationId) + 1 : 0
    return ordered.slice(start, start + limit)
  }
  async cleanupExpired(serverTime: string, conversationLimit: number, messageLimit: number): Promise<{ conversations: number; messages: number }> {
    this.fail(); let conversations = 0; let messages = 0
    for (const head of [...this.heads.values()].sort((a, b) => a.lastAccessAt - b.lastAccessAt)) {
      if (conversations >= conversationLimit || messages >= messageLimit) break
      conversations += 1
      const prefix = `${this.key(head.systemAccountId, head.conversationId)}\u0000`
      for (const [key, message] of this.messages) {
        if (messages >= messageLimit) break
        if (key.startsWith(prefix) && message.expiresAt <= serverTime) { this.messages.delete(key); messages += 1 }
      }
      if (![...this.messages.keys()].some((key) => key.startsWith(prefix)) && !this.running.has(this.key(head.systemAccountId, head.conversationId))) this.heads.delete(this.key(head.systemAccountId, head.conversationId))
    }
    return { conversations, messages }
  }
  close(): void {}
}

function message(sequenceNo: number, text: string, expiresAt = '2026-07-20T00:00:00.000Z'): ChatMessage {
  return { id: `m${sequenceNo}`, conversationId: 'c1', turnId: `t${sequenceNo}`, sequenceNo, role: sequenceNo % 2 ? 'user' : 'assistant', status: 'completed', contentText: text, model: 'm', createdAt: '2026-07-16T00:00:00.000Z', expiresAt }
}

assert.equal(calculateChatCacheBudget({ quota: 2 * 1024 ** 3 }), 256 * 1024 ** 2)
assert.equal(calculateChatCacheBudget({ quota: 100 * 1024 ** 2 }), 20 * 1024 ** 2)
assert.equal(calculateChatCacheBudget(undefined), 64 * 1024 ** 2)

const sanitized = cloneVisibleChatMessage({
  ...message(1, 'visible'), apiKey: 'secret', token: 'secret', password: 'secret', proxy: 'secret', upstreamBody: { hidden: true }, checkpoint: 'secret',
  contentBlocks: [
    { type: 'input_text', text: 'visible', order: 0, metadata: { safe: true, token: 'secret' } },
    { type: 'input_image', assetId: 'asset_1', order: 1, dataUrl: 'data:image/png;base64,AAAA', hiddenDescription: 'secret' },
    { type: 'reasoning', text: 'thinking', extra: 'drop' },
    { type: 'tool_call', id: 'tool_1', toolType: 'search', status: 'completed', item: { query: 'safe', password: 'secret' } }
  ]
} as unknown as ChatMessage)
assert.ok(sanitized)
assert.equal(JSON.stringify(sanitized).includes('secret'), false)
assert.equal(JSON.stringify(sanitized).includes('base64'), false)
assert.equal((sanitized!.contentBlocks?.[1] as { assetId?: string }).assetId, 'asset_1')
assert.equal('item' in (sanitized!.contentBlocks?.[3] ?? {}), false, 'tool_call 原始 item 不得进入展示缓存')
const deepToolPayload = cloneVisibleChatMessage({
  ...message(2, 'safe'),
  contentBlocks: [{ type: 'tool_call', id: 'tool_deep', toolType: 'search', status: 'completed', item: { query: 'safe', nested: { a: { b: { c: { d: { e: { password: 'deep-secret', raw: 'data:image/png;base64,AAAA' } } } } } } } }],
  toolEvents: [{ id: 'tool_deep', type: 'search', status: 'completed', item: { authorization: 'Bearer secret', response: { token: 'secret', payload: 'raw-upstream-body' } } }]
})
assert.ok(deepToolPayload)
assert.equal(JSON.stringify(deepToolPayload).includes('deep-secret'), false)
assert.equal(JSON.stringify(deepToolPayload).includes('base64'), false)
assert.equal(JSON.stringify(deepToolPayload).includes('raw-upstream-body'), false)
assert.equal(deepToolPayload!.toolEvents?.[0]?.item, undefined)
assert.equal(cloneVisibleChatMessage({ ...message(1, 'x'), contentText: 'data:image/png;base64,AAAA' }), undefined)
assert.equal(cloneVisibleChatMessage({ ...message(1, 'x'), blob: new Blob(['x']) } as unknown as ChatMessage), undefined)
const cyclic = message(1, 'x') as ChatMessage & { metadata?: unknown }; cyclic.metadata = cyclic
assert.equal(cloneVisibleChatMessage(cyclic), undefined)

let now = 10
const adapter = new MemoryAdapter()
const cache = new ChatLocalCache({ adapter, clock: () => now, estimate: async () => ({ quota: 1_000_000 }) })
await cache.putHead('A', { conversationId: 'c1', messageRevision: 1 })
await cache.putHead('B', { conversationId: 'c1', messageRevision: 2 })
await cache.putMessages('A', 'c1', [message(2, 'two'), message(1, 'one')])
assert.deepEqual((await cache.readConversation('A', 'c1')).value?.messages.map((item) => item.sequenceNo), [1, 2])
assert.equal((await cache.readConversation('B', 'c1')).value?.messages.length, 0)
const oldBytes = adapter.heads.get('A\u0000c1')!.byteSize
await cache.putMessages('A', 'c1', [message(1, 'replacement-longer')])
assert.notEqual(adapter.heads.get('A\u0000c1')!.byteSize, oldBytes)
assert.equal((await cache.readConversation('A', 'c1')).value?.messages.length, 2)
await cache.deleteFromSequence('A', 'c1', 2)
assert.deepEqual((await cache.readConversation('A', 'c1')).value?.messages.map((item) => item.sequenceNo), [1])
await cache.putRunningTurn('A', 'c1', { turnId: 'turn', assistantMessageId: 'assistant', startedAt: '2026-07-16T00:00:00.000Z' })
assert.equal((await cache.readConversation('A', 'c1')).value?.runningTurn?.turnId, 'turn')
await cache.removeRunningTurn('A', 'c1')
assert.equal((await cache.readConversation('A', 'c1')).value?.runningTurn, undefined)
await cache.clearAccount('A')
assert.equal((await cache.readConversation('A', 'c1')).value?.head, undefined)
assert.ok((await cache.readConversation('B', 'c1')).value?.head)

const lruAdapter = new MemoryAdapter()
const lru = new ChatLocalCache({ adapter: lruAdapter, clock: () => 100, estimate: async () => ({ quota: 500 }) })
for (const [conversationId, lastAccessAt] of [['old', 1], ['current', 2], ['running', 3], ['pending', 4], ['new', 5]] as const) {
  await lruAdapter.putHead({ systemAccountId: 'A', conversationId, messageRevision: 1, lastAccessAt, byteSize: 80 })
}
await lruAdapter.putRunningTurn({ systemAccountId: 'A', conversationId: 'running', turnId: 't', startedAt: 'x' })
await lru.putMessages('A', 'new', [{ ...message(1, 'x'.repeat(200)), conversationId: 'new' }], { currentConversationId: 'current', pendingConfirmationConversationIds: new Set(['pending']) })
assert.equal(lruAdapter.heads.has('A\u0000old'), false)
assert.equal(lruAdapter.heads.has('A\u0000current'), true)
assert.equal(lruAdapter.heads.has('A\u0000running'), true)
assert.equal(lruAdapter.heads.has('A\u0000pending'), true)

const pagedAdapter = new MemoryAdapter()
for (let index = 1; index <= 16; index += 1) await pagedAdapter.putHead({ systemAccountId: 'A', conversationId: `active_${index}`, messageRevision: 1, lastAccessAt: index, byteSize: 10, activeTurnId: `turn_${index}` })
await pagedAdapter.putHead({ systemAccountId: 'A', conversationId: 'victim', messageRevision: 1, lastAccessAt: 17, byteSize: 10 })
pagedAdapter.putFailures.push(Object.assign(new Error('quota'), { name: 'QuotaExceededError' }))
const pagedCache = new ChatLocalCache({ adapter: pagedAdapter })
const pagedResult = await pagedCache.putMessages('A', 'written', [{ ...message(1, 'safe'), conversationId: 'written' }])
assert.equal(pagedResult.ok, true, '首次 quota 淘汰后应重试成功')
assert.equal(pagedAdapter.heads.has('A\u0000victim'), false, '前一页全是 active turn 时必须继续有界翻页淘汰后页')
assert.equal([...pagedAdapter.heads.values()].filter((head) => head.activeTurnId).length, 16, 'activeTurnId head 不得淘汰')

const disabledAdapter = new MemoryAdapter()
disabledAdapter.putFailures.push(Object.assign(new Error('quota-1'), { name: 'QuotaExceededError' }), Object.assign(new Error('quota-2'), { name: 'QuotaExceededError' }))
const disabledCache = new ChatLocalCache({ adapter: disabledAdapter })
const disabledResult = await disabledCache.putMessages('A', 'c1', [message(1, 'safe')])
assert.deepEqual({ ok: disabledResult.ok, enabled: disabledResult.enabled }, { ok: false, enabled: false })
const callsAfterDisable = disabledAdapter.putMessagesCalls
const disabledNoop = await disabledCache.putMessages('A', 'c1', [message(2, 'no-op')])
assert.equal(disabledNoop.enabled, false)
assert.equal(disabledAdapter.putMessagesCalls, callsAfterDisable, '禁用后写入不得再访问 storage')

const expiryAdapter = new MemoryAdapter()
const expiry = new ChatLocalCache({ adapter: expiryAdapter })
await expiry.putMessages('A', 'c1', [message(1, 'old', '2026-07-15T00:00:00.000Z'), message(2, 'new')])
await expiry.putMessages('A', 'c2', [{ ...message(1, 'old', '2026-07-15T00:00:00.000Z'), conversationId: 'c2' }])
const cleaned = await expiry.cleanupExpired('2026-07-16T00:00:00.000Z', { conversationLimit: 1, messageLimit: 1 })
assert.deepEqual(cleaned.value, { conversations: 1, messages: 1 })
assert.equal(expiryAdapter.heads.size, 2, '有界清理不得越过单次会话上限')

for (const error of [Object.assign(new Error('quota'), { name: 'QuotaExceededError' }), Object.assign(new Error('security'), { name: 'SecurityError' })]) {
  const failing = new MemoryAdapter(); failing.failWith = error
  const diagnostics: string[] = []
  const degraded = new ChatLocalCache({ adapter: failing, diagnostic: (code) => diagnostics.push(code) })
  const result = await degraded.putMessages('A', 'c1', [message(1, 'x')])
  assert.equal(result.ok, false)
  assert.ok(diagnostics[0]?.startsWith('cache_'))
}

console.log('AI 问答本地展示缓存回归通过')

const source = readFileSync(new URL('../../views/chat/chatLocalCache.ts', import.meta.url), 'utf8')
for (const store of ['conversation_heads', 'messages', 'running_turns']) assert.match(source, new RegExp(`createObjectStore\\('${store}'`))
assert.match(source, /keyPath: \['systemAccountId', 'conversationId', 'sequenceNo'\]/)
assert.match(source, /createIndex\('by_conversation', \['systemAccountId', 'conversationId'\]\)/)
assert.match(source, /db\.transaction\(\['conversation_heads', 'messages'\], 'readwrite'\)/, '消息与字节元数据必须在同一事务更新')
assert.doesNotMatch(source, /getAll\(\s*\)/, '生产 IndexedDB 读取必须带 key range 或 limit，不能无界全库读取')
