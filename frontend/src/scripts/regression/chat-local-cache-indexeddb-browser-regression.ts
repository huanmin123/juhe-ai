import { NativeIndexedDbChatCacheAdapter } from '../../views/chat/chatLocalCache'
import type { ChatMessage } from '../../types/domain/chat'

const result = document.querySelector('#result')!
function assert(condition: unknown, message: string): asserts condition { if (!condition) throw new Error(message) }
function request<T>(value: IDBRequest<T>): Promise<T> { return new Promise((resolve, reject) => { value.onsuccess = () => resolve(value.result); value.onerror = () => reject(value.error) }) }
function txDone(tx: IDBTransaction): Promise<void> { return new Promise((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error) }) }
function message(sequenceNo: number, conversationId = 'tail', expiresAt = '2099-01-01T00:00:00.000Z'): ChatMessage { return { id: `m_${sequenceNo}`, conversationId, turnId: `t_${sequenceNo}`, sequenceNo, role: sequenceNo % 2 ? 'user' : 'assistant', status: 'completed', contentText: `message-${sequenceNo}`, model: 'model', createdAt: '2026-07-16T00:00:00.000Z', expiresAt } }

try {
  await new Promise<void>((resolve) => { const deletion = indexedDB.deleteDatabase('juhe-ai-chat-cache'); deletion.onsuccess = () => resolve(); deletion.onerror = () => resolve(); deletion.onblocked = () => resolve() })
  const adapter = new NativeIndexedDbChatCacheAdapter()
  await adapter.putMessages('A', 'tail', Array.from({ length: 250 }, (_, index) => message(index + 1)), { now: 1 })
  const tail = await adapter.readConversation('A', 'tail')
  assert(tail.messages.length === 200 && tail.messages[0]?.sequenceNo === 51 && tail.messages[199]?.sequenceNo === 250, '必须返回最新 200 条并升序')
  const beforeHead = tail.head!.byteSize
  assert(await adapter.getTotalBytes() === beforeHead, '全局 byte metadata 必须与首个会话一致')
  await adapter.putHead({ systemAccountId: 'A', conversationId: 'tail', messageRevision: 9, lastAccessAt: 2, byteSize: 0, title: 'updated' })
  assert((await adapter.readConversation('A', 'tail')).head?.byteSize === beforeHead, 'putHead 必须事务合并保留 byteSize')

  const database = await new Promise<IDBDatabase>((resolve, reject) => { const opening = indexedDB.open('juhe-ai-chat-cache', 1); opening.onsuccess = () => resolve(opening.result); opening.onerror = () => reject(opening.error) })
  const seed = database.transaction(['messages', 'running_turns'], 'readwrite'); const seeded = txDone(seed)
  seed.objectStore('messages').put({ ...message(1, 'orphan'), systemAccountId: 'A', byteSize: 10 })
  seed.objectStore('running_turns').put({ systemAccountId: 'A', conversationId: 'orphan', turnId: 't', startedAt: 'x' })
  await seeded
  await adapter.clearAccount('A')
  const verify = database.transaction(['conversation_heads', 'messages', 'running_turns'], 'readonly'); const verified = txDone(verify)
  const counts = await Promise.all([request(verify.objectStore('conversation_heads').index('by_account').count('A')), request(verify.objectStore('messages').index('by_account').count('A')), request(verify.objectStore('running_turns').index('by_account').count('A'))])
  await verified
  assert(counts.every((count) => count === 0), 'clearAccount 必须清理无 head 的 messages/running_turns')
  assert(await adapter.getTotalBytes() === 0, 'clearAccount 必须同步扣减账户 head bytes')

  await adapter.putMessages('B', 'expiry', [message(1, 'expiry', '2026-01-01T00:00:00.000Z'), message(2, 'expiry')], { now: 3 })
  const expiryBefore = (await adapter.readConversation('B', 'expiry')).head!.byteSize
  const cleanup = await adapter.cleanupExpired('2026-07-16T00:00:00.000Z', 1, 1)
  const expiryAfter = await adapter.readConversation('B', 'expiry')
  assert(cleanup.messages === 1 && expiryAfter.messages.length === 1 && expiryAfter.head!.byteSize < expiryBefore, 'expiry 游标必须有界删除并扣减 head bytes')
  adapter.close(); database.close()
  result.textContent = 'PASS: native IndexedDB adapter regression'
  document.body.dataset.status = 'passed'
} catch (error) {
  result.textContent = `FAIL: ${error instanceof Error ? error.message : 'unknown'}`
  document.body.dataset.status = 'failed'
  throw error
}
